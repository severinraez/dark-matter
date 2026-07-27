package e2e

import (
	"regexp"
	"strings"
	"testing"
)

// The §11.4 sync scenario group's stat-counter leg, live end-to-end now
// that reads produce stats (M7): per-replica G-Counter cells merge across
// replicas and converge identically (§7.2 — max reconciles copies of the
// same replica's file, values sum across files; `t` is LWW max).

func TestSyncStatCounterMerge(t *testing.T) {
	a, bare := newSharedRepo(t)
	res := a.MustDM("a:api/handler.rb:c:Shared note\n")
	handle := createdHandle(t, res.Stdout)
	a.MustDM("", "sync")

	// B reads: one impression (surface) + one expansion with its clock's
	// timestamp; the deltas ride B's sync into stats/E2EREPL2 (§8.1).
	b := CloneRepo(t, bare, 2)
	b.MustDM("", "init")
	b.MustDM("r:api/handler.rb\nr:#" + handle + "\n")
	b.MustDM("", "sync")

	// A reads too (impression only), then syncs — pushing its own file
	// and receiving B's; B's next sync receives A's.
	a.MustDM("r:api/handler.rb\n")
	a.MustDM("", "sync")
	b.MustDM("", "sync")

	dumpA, dumpB := a.MustDM("", "dump").Stdout, b.MustDM("", "dump").Stdout
	if dumpA != dumpB {
		t.Fatalf("stat state diverged:\nA:\n%s\nB:\n%s", dumpA, dumpB)
	}
	// Each replica's cell is intact — replica A at 1 and replica B at 1
	// total 2, by summing files, never by overwriting (§7.2).
	rowA := regexp.MustCompile(`stat E2EREPL1 \S+ i:1 x:0 h:0 t:0\n`)
	rowB := regexp.MustCompile(`stat E2EREPL2 \S+ i:1 x:1 h:0 t:[1-9]\d*\n`)
	if !rowA.MatchString(dumpA) || !rowB.MatchString(dumpA) {
		t.Errorf("converged dump lacks the per-replica rows:\n%s", dumpA)
	}
	if strings.Count(dumpA, "stat ") != 2 {
		t.Errorf("want exactly one row per replica:\n%s", dumpA)
	}
}
