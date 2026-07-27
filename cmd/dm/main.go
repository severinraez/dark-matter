// dm is a git-native memory layer for LLM agents: notes about a codebase
// that surface exactly where and when they apply (design.md §1).
package main

import (
	"os"

	"meltcloud.io/dm/internal/cli"
)

func main() {
	os.Exit(cli.Dispatch(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
