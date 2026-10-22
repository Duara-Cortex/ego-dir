package lsppool

import (
	"context"
	"sync"

	"github.com/Duara-Cortex/ego-dir/internal/lsp"
)

// Session is one call's use of a pooled server. It has the requests of
// lspsource.Client and is safe for concurrent use. Release it when the
// call ends.
type Session struct {
	p    *Pool
	e    *entry
	docs map[string]bool // documents this session opened; guarded by e.docMu
	once sync.Once
}

// DidOpen opens a document for the session. If another session has it
// open with different text, the server is sent the new text instead.
func (s *Session) DidOpen(ctx context.Context, uri, languageID, text string) error {
	e := s.e
	e.docMu.Lock()
	defer e.docMu.Unlock()
	d := e.docs[uri]
	if d == nil {
		if err := e.client.DidOpen(ctx, uri, languageID, text); err != nil {
			return err
		}
		e.docs[uri] = &document{refs: 1, version: 1, text: text}
		s.docs[uri] = true
		return nil
	}
	if d.text != text {
		if err := e.client.DidChange(ctx, uri, d.version+1, text); err != nil {
			return err
		}
		d.version++
		d.text = text
	}
	if !s.docs[uri] {
		d.refs++
		s.docs[uri] = true
	}
	return nil
}

// Release closes the documents no other session holds and returns the
// server to the pool. err is the call's error: if the server has stopped,
// the end of its stderr is added. Release may be called more than once.
func (s *Session) Release(ctx context.Context, err error) error {
	s.once.Do(func() {
		e := s.e
		e.docMu.Lock()
		for uri := range s.docs {
			d := e.docs[uri]
			if d.refs--; d.refs == 0 {
				_ = e.client.DidClose(ctx, uri)
				delete(e.docs, uri)
			}
		}
		e.docMu.Unlock()
		s.p.leave(e)
	})
	if err != nil {
		select {
		case <-s.e.client.Done():
			return s.e.client.Annotate(err)
		default:
		}
	}
	return err
}

// Definition is lsp.Client.Definition.
func (s *Session) Definition(ctx context.Context, uri string, pos lsp.Position) ([]lsp.Location, error) {
	return s.e.client.Definition(ctx, uri, pos)
}

// TypeDefinition is lsp.Client.TypeDefinition.
func (s *Session) TypeDefinition(ctx context.Context, uri string, pos lsp.Position) ([]lsp.Location, error) {
	return s.e.client.TypeDefinition(ctx, uri, pos)
}

// Implementation is lsp.Client.Implementation.
func (s *Session) Implementation(ctx context.Context, uri string, pos lsp.Position) ([]lsp.Location, error) {
	return s.e.client.Implementation(ctx, uri, pos)
}

// References is lsp.Client.References.
func (s *Session) References(ctx context.Context, uri string, pos lsp.Position, includeDeclaration bool) ([]lsp.Location, error) {
	return s.e.client.References(ctx, uri, pos, includeDeclaration)
}

// Hover is lsp.Client.Hover.
func (s *Session) Hover(ctx context.Context, uri string, pos lsp.Position) (string, error) {
	return s.e.client.Hover(ctx, uri, pos)
}

// DocumentSymbols is lsp.Client.DocumentSymbols.
func (s *Session) DocumentSymbols(ctx context.Context, uri string) ([]lsp.DocumentSymbol, error) {
	return s.e.client.DocumentSymbols(ctx, uri)
}

// PrepareCallHierarchy is lsp.Client.PrepareCallHierarchy.
func (s *Session) PrepareCallHierarchy(ctx context.Context, uri string, pos lsp.Position) ([]lsp.CallHierarchyItem, error) {
	return s.e.client.PrepareCallHierarchy(ctx, uri, pos)
}

// IncomingCalls is lsp.Client.IncomingCalls.
func (s *Session) IncomingCalls(ctx context.Context, item lsp.CallHierarchyItem) ([]lsp.CallHierarchyIncomingCall, error) {
	return s.e.client.IncomingCalls(ctx, item)
}

// OutgoingCalls is lsp.Client.OutgoingCalls.
func (s *Session) OutgoingCalls(ctx context.Context, item lsp.CallHierarchyItem) ([]lsp.CallHierarchyOutgoingCall, error) {
	return s.e.client.OutgoingCalls(ctx, item)
}
