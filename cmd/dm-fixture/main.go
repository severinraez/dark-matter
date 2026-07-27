// dm-fixture builds the E2E fixture repository from a declarative manifest
// (design.md §11.2, architecture.md §10).
//
// Exec-only black box: it drives the built dm binary over its public stdin
// interface plus plain git, and imports nothing from internal/ — a review
// rule, since Go cannot enforce it within one module.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "dm-fixture: not implemented yet (arrives with the fixture milestones)")
	os.Exit(1)
}
