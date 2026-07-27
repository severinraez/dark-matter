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
	"os"

	"meltcloud.io/dm/internal/app"
)

const usage = `usage: dm [subcommand]

With no subcommand, dm reads a batch of commands from stdin (one per line).

Subcommands:
  init      create .git/.dm/, configure the refs/dm/* refspec, fetch or create the store
  sync      share: fold pending, fetch, union merge, push with retry
  worklist  the hygiene query (--all: drop session scoping · --stale: include stale/unconfirmed)
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
	case "init":
		return run(stderr, func(det app.Determinism) error {
			return app.Init(".", det, stdout)
		})
	case "dump":
		return run(stderr, func(det app.Determinism) error {
			return app.Dump(".", det, stdout, stderr)
		})
	case "sync":
		return run(stderr, func(det app.Determinism) error {
			opts := app.SyncOptions{SkipFetch: os.Getenv(app.EnvSkipFetch) != ""}
			return app.Sync(".", det, opts, stdout, stderr)
		})
	case "worklist":
		var opts app.WorklistOptions
		for _, arg := range args[1:] {
			switch arg {
			case "--all":
				opts.All = true
			case "--stale":
				opts.Stale = true
			default:
				fmt.Fprintf(stderr, "dm worklist: unknown flag %q (have --all, --stale)\n", arg)
				return 2
			}
		}
		return run(stderr, func(det app.Determinism) error {
			report, err := app.Worklist(".", det, opts, stderr)
			if err != nil {
				return err
			}
			renderWorklist(stdout, report)
			return nil
		})
	case "gc":
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

// run resolves the determinism overrides and reports any failure on stderr.
func run(stderr io.Writer, f func(app.Determinism) error) int {
	det, err := app.DeterminismFromEnv(os.Getenv)
	if err != nil {
		fmt.Fprintf(stderr, "dm: %v\n", err)
		return 1
	}
	if err := f(det); err != nil {
		fmt.Fprintf(stderr, "dm: %v\n", err)
		return 1
	}
	return 0
}

// runBatch is the two-phase batch executor (§4.1): parse everything, reject
// the whole batch on any syntax error, otherwise open a session and execute
// in order.
func runBatch(stdin io.Reader, stdout, stderr io.Writer) int {
	data, err := io.ReadAll(stdin)
	if err != nil {
		fmt.Fprintf(stderr, "dm: reading stdin: %v\n", err)
		return 1
	}
	cmds, perr := ParseBatch(string(data))
	if perr != nil {
		// Batch-level failure: a single ! line at the head of output,
		// nothing applied (§4.4).
		fmt.Fprintf(stdout, "!%s\n", perr)
		return 1
	}
	if len(cmds) == 0 {
		fmt.Fprintf(stdout, "%s0 ok\n", glyphEnd)
		return 0
	}
	return run(stderr, func(det app.Determinism) error {
		s, err := app.OpenSession(".", det, stderr)
		if err != nil {
			return err
		}
		defer s.Close()
		res, err := s.ExecuteBatch(cmds)
		if err != nil {
			return err
		}
		renderBatch(stdout, res)
		return nil
	})
}
