// Package lsppool keeps language servers running between calls, so a
// long-lived process (ego-dir mcp) does not start a server for every call.
//
// A Pool holds one server per (command, root), started on the first call
// that needs it with a context the pool owns: a call's deadline or
// cancellation ends only that call's requests ($/cancelRequest), never the
// server. A server unused for Options.Idle is shut down; at most
// Options.Max run at once, the least recently used idle one making way for
// a new one.
//
// Each call holds a Session. What a server knows is kept current for it:
//   - Open documents are shared between sessions and reference-counted. The
//     first session to open a document sends didOpen; a session that opens
//     it with different text sends didChange; the last to release it sends
//     didClose.
//   - Files that are not open: before each call the root is scanned (size,
//     modification time and, for files modified too recently to trust
//     those, a hash) and the changes are sent as
//     workspace/didChangeWatchedFiles. A changed file another call holds
//     open is sent as didChange too, since the server answers from the
//     open text. The server is restarted instead when
//     it has not registered for that notification, when a module or
//     workspace file changed (go.mod, go.sum, go.work), or when more than
//     MaxChanges files changed at once.
//
// A server that stops is dropped and logged with the end of its stderr. The
// call that saw it stop gets an error carrying that stderr; the next call
// starts a new server.
package lsppool

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/Duara-Cortex/ego-dir/internal/lsp"
)

// Defaults for Options.
const (
	DefaultMax          = 4
	DefaultStartTimeout = 30 * time.Second
)

// MaxChanges is the most changed files sent to a server at once; more mean
// the workspace was replaced (a branch switch, say), and the server is
// restarted.
const MaxChanges = 500

// closeTimeout bounds the shutdown of one server.
const closeTimeout = 5 * time.Second

// ErrClosed is returned by Acquire after Close.
var ErrClosed = errors.New("lsppool: pool is closed")

// StartFunc starts a language server for root and initialises it.
type StartFunc func(ctx context.Context, command []string, root string) (*lsp.Client, error)

// Options configures a Pool.
type Options struct {
	// Idle is how long a server may go unused before it is shut down. It
	// must be positive.
	Idle time.Duration
	// Max is how many servers may run at once. Zero means DefaultMax.
	Max int
	// StartTimeout bounds starting a server. Zero means
	// DefaultStartTimeout.
	StartTimeout time.Duration
	// Log receives a line about each server that stopped unexpectedly or
	// failed to shut down. Nil discards them.
	Log io.Writer
	// Start starts a server. Nil means lsp.StartWith with WatchFiles set.
	Start StartFunc
}

// Pool shares language servers between calls. It is safe for concurrent
// use.
type Pool struct {
	opts   Options
	ctx    context.Context // owns every server's start
	cancel context.CancelFunc

	mu       sync.Mutex
	entries  map[string]*entry // current servers by key
	changed  chan struct{}     // closed and replaced when a server becomes idle or goes
	closed   bool
	idleSeq  int            // numbers idle periods, oldest lowest
	stopping sync.WaitGroup // shutdowns under way
}

// entry is one server.
type entry struct {
	key     string
	command []string
	root    string

	ready  chan struct{} // closed when the start ends
	client *lsp.Client   // set before ready is closed; nil if the start failed
	err    error         // why the start failed

	// Guarded by Pool.mu.
	users    int  // sessions holding it, and callers waiting for its start
	retired  bool // takes no new sessions; stops when the last one leaves
	stopped  bool // shutdown begun
	crashed  bool // stopped by itself
	idleGen  int  // the idle period's number; invalidates older timers
	idleTime *time.Timer

	scanMu sync.Mutex
	snap   *snapshot // the files under root as the server last learnt of them

	docMu sync.Mutex
	docs  map[string]*document // open documents by URI
}

// document is an open document, shared by the sessions that opened it.
type document struct {
	refs    int
	version int
	text    string
}

// New returns an empty pool.
func New(opts Options) *Pool {
	if opts.Max <= 0 {
		opts.Max = DefaultMax
	}
	if opts.StartTimeout <= 0 {
		opts.StartTimeout = DefaultStartTimeout
	}
	if opts.Log == nil {
		opts.Log = io.Discard
	}
	if opts.Start == nil {
		opts.Start = func(ctx context.Context, command []string, root string) (*lsp.Client, error) {
			return lsp.StartWith(ctx, command, root, lsp.Config{WatchFiles: true})
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Pool{opts: opts, ctx: ctx, cancel: cancel, entries: map[string]*entry{}, changed: make(chan struct{})}
}

// Acquire returns a session on the server for command and root, starting
// it if need be, with the server told of every file that changed since its
// last call. It waits under ctx for the server to start; if ctx ends first,
// the start goes on and the server is kept for the next call.
func (p *Pool) Acquire(ctx context.Context, command []string, root string) (*Session, error) {
	for attempt := 0; ; attempt++ {
		e, err := p.join(ctx, command, root)
		if err != nil {
			return nil, err
		}
		restart, err := e.refresh(ctx, attempt == 0)
		if (restart || err != nil) && attempt == 0 {
			p.retire(e)
			continue // once, with a new server
		}
		if err != nil {
			p.leave(e)
			return nil, e.client.Annotate(err)
		}
		return &Session{p: p, e: e, docs: map[string]bool{}}, nil
	}
}

// Close shuts every server down and waits for them, at most until ctx
// ends. Sessions should be released first. Acquire fails after Close.
func (p *Pool) Close(ctx context.Context) error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	for _, e := range p.entries {
		p.stopLocked(e)
	}
	p.entries = map[string]*entry{}
	p.notifyLocked()
	p.mu.Unlock()
	p.cancel() // abandon starts under way
	done := make(chan struct{})
	go func() {
		p.stopping.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("lsppool: shut down language servers: %w", ctx.Err())
	}
}

func key(command []string, root string) string {
	return root + "\x00" + strings.Join(command, "\x00")
}

// join adds a user to the entry for command and root, starting one if
// there is none, and waits for it to be ready.
func (p *Pool) join(ctx context.Context, command []string, root string) (*entry, error) {
	k := key(command, root)
	for {
		p.mu.Lock()
		if p.closed {
			p.mu.Unlock()
			return nil, ErrClosed
		}
		e := p.entries[k]
		if e != nil && e.dead() {
			p.removeLocked(e)
			e = nil
		}
		if e == nil {
			if len(p.entries) >= p.opts.Max {
				victim := p.idlestLocked()
				if victim == nil {
					wait := p.changed
					p.mu.Unlock()
					select {
					case <-wait:
						continue
					case <-ctx.Done():
						return nil, ctx.Err()
					}
				}
				p.removeLocked(victim)
				p.stopLocked(victim)
			}
			e = &entry{key: k, command: command, root: root, ready: make(chan struct{}), docs: map[string]*document{}}
			p.entries[k] = e
			go p.start(e)
		}
		e.users++
		e.stopIdleLocked()
		p.mu.Unlock()

		select {
		case <-e.ready:
		case <-ctx.Done():
			p.leave(e)
			return nil, ctx.Err()
		}
		if e.err != nil {
			p.leave(e)
			return nil, e.err
		}
		return e, nil
	}
}

// start starts e's server under the pool's context. The files under the
// root are recorded first, so no later change is missed.
func (p *Pool) start(e *entry) {
	e.snap, _ = scan(e.root, nil)
	ctx, cancel := context.WithTimeout(p.ctx, p.opts.StartTimeout)
	defer cancel()
	e.client, e.err = p.opts.Start(ctx, e.command, e.root)
	close(e.ready)

	p.mu.Lock()
	defer p.mu.Unlock()
	if e.err != nil {
		p.removeLocked(e)
		p.notifyLocked()
		return
	}
	go p.watch(e)
	if e.users == 0 && !e.stopped {
		p.idleLocked(e)
	}
}

// watch drops e when its server stops by itself.
func (p *Pool) watch(e *entry) {
	<-e.client.Done()
	p.mu.Lock()
	if e.stopped {
		p.mu.Unlock()
		return
	}
	e.crashed = true
	p.removeLocked(e)
	p.stopLocked(e) // reaps the process
	p.notifyLocked()
	p.mu.Unlock()
	err := e.client.Annotate(errors.New("stopped unexpectedly"))
	fmt.Fprintf(p.opts.Log, "ego-dir: language server %s: %v\n", strings.Join(e.command, " "), err)
}

// leave removes a user from e. The last user of a retired or replaced
// entry stops it; the last user of a current one starts its idle timer.
func (p *Pool) leave(e *entry) {
	p.mu.Lock()
	defer p.mu.Unlock()
	e.users--
	if e.users > 0 {
		return
	}
	select {
	case <-e.ready:
	default:
		return // start will see there are no users
	}
	if e.client == nil {
		return
	}
	if e.retired || p.entries[e.key] != e {
		p.stopLocked(e)
	} else {
		p.idleLocked(e)
	}
	p.notifyLocked()
}

// retire stops e taking sessions and removes the caller's use of it.
func (p *Pool) retire(e *entry) {
	p.mu.Lock()
	e.retired = true
	p.removeLocked(e)
	p.mu.Unlock()
	p.leave(e)
}

// dead reports whether e started and its server has since stopped.
func (e *entry) dead() bool {
	select {
	case <-e.ready:
	default:
		return false
	}
	if e.client == nil {
		return true
	}
	select {
	case <-e.client.Done():
		return true
	default:
		return false
	}
}

// idlestLocked returns the least recently used entry with no users, or nil.
func (p *Pool) idlestLocked() *entry {
	var best *entry
	for _, e := range p.entries {
		if e.users == 0 && e.idleTime != nil && (best == nil || e.idleGen < best.idleGen) {
			best = e
		}
	}
	return best
}

// idleLocked starts e's idle timer.
func (p *Pool) idleLocked(e *entry) {
	e.stopIdleLocked()
	p.idleSeq++
	e.idleGen = p.idleSeq
	gen := e.idleGen
	e.idleTime = time.AfterFunc(p.opts.Idle, func() { p.expire(e, gen) })
}

func (e *entry) stopIdleLocked() {
	if e.idleTime != nil {
		e.idleTime.Stop()
		e.idleTime = nil
	}
}

// expire stops e if it is still the idle entry its timer was set for.
func (p *Pool) expire(e *entry, gen int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if e.idleGen != gen || e.idleTime == nil || e.users > 0 || p.entries[e.key] != e {
		return
	}
	p.removeLocked(e)
	p.stopLocked(e)
	p.notifyLocked()
}

func (p *Pool) removeLocked(e *entry) {
	if p.entries[e.key] == e {
		delete(p.entries, e.key)
	}
}

// stopLocked shuts e's server down in the background, once.
func (p *Pool) stopLocked(e *entry) {
	if e.stopped {
		return
	}
	e.stopped = true
	e.stopIdleLocked()
	crashed := e.crashed
	p.stopping.Add(1)
	go func() {
		defer p.stopping.Done()
		<-e.ready
		if e.client == nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), closeTimeout)
		defer cancel()
		if err := e.client.Close(ctx); err != nil && !crashed {
			fmt.Fprintf(p.opts.Log, "ego-dir: stop language server %s: %v\n", strings.Join(e.command, " "), err)
		}
	}()
}

func (p *Pool) notifyLocked() {
	close(p.changed)
	p.changed = make(chan struct{})
}

// refresh tells e's server of the files changed under its root since its
// last call. It reports restart, without telling it, when the server
// should be replaced instead and allowRestart is set.
func (e *entry) refresh(ctx context.Context, allowRestart bool) (restart bool, err error) {
	e.scanMu.Lock()
	defer e.scanMu.Unlock()
	next, changes := scan(e.root, e.snap)
	if len(changes) == 0 {
		e.snap = next
		return false, nil
	}
	watching := e.client.WatchesFiles()
	if allowRestart && (!watching || needsRestart(changes)) {
		return true, nil
	}
	if watching {
		if err := e.client.DidChangeWatchedFiles(ctx, changes); err != nil {
			return false, err
		}
	}
	if err := e.syncOpen(ctx, changes); err != nil {
		return false, err
	}
	e.snap = next
	return false, nil
}

// syncOpen sends the new text of changed files that another call holds
// open: the server answers from an open document's text, not the disk.
func (e *entry) syncOpen(ctx context.Context, changes []lsp.FileEvent) error {
	e.docMu.Lock()
	defer e.docMu.Unlock()
	for _, c := range changes {
		d := e.docs[c.URI]
		if d == nil || c.Type == lsp.FileDeleted {
			continue
		}
		path, err := lsp.URIToPath(c.URI)
		if err != nil {
			continue
		}
		b, err := os.ReadFile(path)
		if err != nil || string(b) == d.text {
			continue
		}
		if err := e.client.DidChange(ctx, c.URI, d.version+1, string(b)); err != nil {
			return err
		}
		d.version++
		d.text = string(b)
	}
	return nil
}
