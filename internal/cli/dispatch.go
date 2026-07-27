// Package cli owns the batch grammar, output rendering, and subcommand
// dispatch (architecture.md §2, §8). It holds no semantics: parse produces
// typed app commands, render consumes core/view's read-model.
//
// Files: parse.go (grammar, %-decoding, $N), render.go (glyphs, blocks,
// acks, footer), dispatch.go (this file).
package cli

import (
	"fmt"
	"io"
	"strings"
)

// footerEnd is the end-marker glyph closing every batch stream (§4.3).
const footerEnd = "◾"

const usage = `usage: dm [subcommand]

With no subcommand, dm reads a batch of commands from stdin (one per line).

Subcommands:
  init      create .git/.dm/, configure the refs/dm/* refspec, fetch or create the store
  sync      share: fold pending, fetch, union merge, push with retry
  worklist  the hygiene query
  gc        compact the store
  dump      print full raw store state (tests and debugging)
`

// Dispatch routes one dm invocation (design.md §10.1): no subcommand reads
// a batch from stdin; otherwise one of init/sync/worklist/gc/dump.
func Dispatch(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return runBatch(stdin, stdout, stderr)
	}
	switch args[0] {
	case "init", "sync", "worklist", "gc", "dump":
		fmt.Fprintf(stderr, "dm %s: not implemented yet\n", args[0])
		return 1
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return 0
	default:
		fmt.Fprintf(stderr, "dm: unknown subcommand %q\n", args[0])
		return 2
	}
}

func runBatch(stdin io.Reader, stdout, stderr io.Writer) int {
	data, err := io.ReadAll(stdin)
	if err != nil {
		fmt.Fprintf(stderr, "dm: reading stdin: %v\n", err)
		return 1
	}
	if strings.TrimSpace(string(data)) == "" {
		fmt.Fprintf(stdout, "%s0 ok\n", footerEnd)
		return 0
	}
	fmt.Fprintln(stderr, "dm: batch commands not implemented yet (arrives with M2)")
	return 1
}
