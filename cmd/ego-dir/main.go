// Command ego-dir gives AI agents graph-augmented code intelligence on top of
// language servers.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

// Exit codes.
const (
	exitOK      = 0
	exitUsage   = 1 // bad flags or input, detected before a server starts
	exitRuntime = 2 // the language server or a request failed
)

const usage = `usage: ego-dir <command> [flags]

Commands:
  inspect_ego         ego-graph of the symbol at a position: callers, callees,
                      implementations and type dependencies up to --depth hops;
                      on a module's name ("package x") the module and its imports
  resolve_definition  declaration of the symbol at a position
  find_references     uses of the symbol at a position
  mcp                 serve the three commands above as Model Context Protocol
                      tools on stdin/stdout, until stdin closes
  version             print the version
  help                print this help

Flags (all position commands):
  --file PATH         source file, relative to --root (required)
  --line N            1-based line (required)
  --col N             1-based column, in characters (required)
  --root DIR          workspace root (default: current directory)
  --server "CMD ARGS" language server command (default: chosen by file extension)
  --format md|json    output format (default: md)
  --timeout DURATION  overall deadline (default: 30s)

Flags (inspect_ego only):
  --depth N           hop radius, 1-5 (default: 1)
  --edges LIST        comma list of calls,implements,instantiates,imports,contains,
                      references, or all (default: all except contains and references)
  --direction DIR     in, out or both (default: both)
  --body MODE         bodies to include: ego, all or none (default: ego)

Flags (mcp), fixed for the server's lifetime:
  --root DIR          workspace root; tool calls cannot read outside it
                      (default: current directory)
  --server "CMD ARGS" language server command (default: chosen by file extension)
  --timeout DURATION  deadline for each tool call (default: 30s)

Exit codes: 0 success, 1 usage error, 2 language server or runtime error.
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// usageError marks errors that exit with exitUsage.
type usageError struct{ err error }

func (e usageError) Error() string { return e.err.Error() }
func (e usageError) Unwrap() error { return e.err }

func usagef(format string, args ...any) error {
	return usageError{fmt.Errorf(format, args...)}
}

// run is the testable entry point. Results go to stdout only on success;
// errors go to stderr. It returns the process exit code.
func run(args []string, stdout, stderr io.Writer) int {
	return runWithInput(args, os.Stdin, stdout, stderr)
}

// runWithInput is run with stdin, which only the mcp command reads.
func runWithInput(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return exitUsage
	}
	cmd, rest := args[0], args[1:]
	var err error
	switch cmd {
	case "version", "--version":
		if len(rest) != 0 {
			err = usagef("version takes no arguments")
			break
		}
		fmt.Fprintf(stdout, "ego-dir %s\n", version)
		return exitOK
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return exitOK
	case "inspect_ego", "resolve_definition", "find_references":
		err = runCommand(cmd, rest, stdout)
	case "mcp":
		err = runMCP(rest, stdin, stdout, stderr)
	default:
		err = usagef("unknown command %q", cmd)
	}
	if err == nil {
		return exitOK
	}
	fmt.Fprintf(stderr, "ego-dir: %v\n", err)
	var ue usageError
	if errors.As(err, &ue) {
		fmt.Fprintln(stderr, "run 'ego-dir help' for usage")
		return exitUsage
	}
	return exitRuntime
}
