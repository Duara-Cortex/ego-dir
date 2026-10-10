// Command ego-dir gives AI agents graph-augmented code intelligence on top of
// language servers.
package main

import (
	"fmt"
	"io"
	"os"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run is the testable entry point. It returns the process exit code:
// 0 on success, 1 on a usage error.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 1 && (args[0] == "version" || args[0] == "--version") {
		fmt.Fprintf(stdout, "ego-dir %s\n", version)
		return 0
	}
	fmt.Fprintln(stderr, "usage: ego-dir version")
	return 1
}
