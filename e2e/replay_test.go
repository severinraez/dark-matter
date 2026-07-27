package e2e

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
)

func lookupEnv(key string) (string, bool) { return os.LookupEnv(key) }

// TestQ1ReplaySynthetic keeps the rig honest in CI: a deterministic
// history with ordinary churn — in-place edits, renames, a folder move,
// deletions — must keep the layer-1–3 fraction high. This is the rig's
// guard test, not the Q1 verdict; the verdict runs against real
// histories (TestQ1ReplayRepo, q1-report.md).
func TestQ1ReplaySynthetic(t *testing.T) {
	r := NewRepo(t)
	// A base tree of distinctive files.
	for i := 0; i < 8; i++ {
		r.WriteFile(fmt.Sprintf("src/f%d.go", i),
			fmt.Sprintf("package p%d\n\nfunc A%d() {}\nfunc B%d() {}\nfunc C%d() {}\n", i, i, i, i))
	}
	r.WriteFile("docs/readme.md", "docs\nline\nmore\n")
	r.Commit("base")

	// Churn, one commit each: edit, rename, move a folder, delete, rewrite.
	r.WriteFile("src/f0.go", "package p0\n\nfunc A0() {}\nfunc B0() {}\nfunc C0() { /* edited */ }\n")
	r.Commit("edit f0")
	r.Git("mv", "src/f1.go", "src/renamed.go")
	r.Commit("rename f1")
	r.Git("mv", "docs", "documentation")
	r.Commit("move docs")
	r.Git("rm", "-q", "src/f2.go")
	r.Commit("delete f2")
	r.WriteFile("src/f3.go", "package p3\n\nfunc A3() { /* touched */ }\nfunc B3() {}\nfunc C3() {}\n")
	r.Commit("edit f3")

	stats := Replay(t, r.Dir, 0.0, 0)
	t.Logf("synthetic replay:\n%s", stats.Format())
	if stats.Samples == 0 {
		t.Fatal("rig produced no samples")
	}
	// One deletion in nine files is the only legitimate layer-5 drop;
	// everything else must resolve via layers 1–3.
	if frac := stats.Resolved13Fraction(); frac < 0.85 {
		t.Errorf("layer-1–3 fraction %.3f, want ≥ 0.85 under ordinary churn", frac)
	}
	if stats.State["orphaned"] == 0 {
		t.Error("the deleted file should orphan its note — the rig must see layer 5 too")
	}
}

// TestQ1ReplayRepo runs the rig against a real repository named by
// DM_Q1_REPO (skipped otherwise) and logs the stats for the record —
// this is the run behind q1-report.md.
func TestQ1ReplayRepo(t *testing.T) {
	src := replaySource()
	if src == "" {
		t.Skip("set DM_Q1_REPO=/path/to/repo to run the Q1 replay")
	}
	maxNotes := 0
	if v := strings.TrimSpace(getenvDefault("DM_Q1_NOTES", "")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			t.Fatalf("DM_Q1_NOTES: %v", err)
		}
		maxNotes = n
	}
	seedFrac := 0.25
	if v := strings.TrimSpace(getenvDefault("DM_Q1_SEED", "")); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil || f < 0 || f >= 1 {
			t.Fatalf("DM_Q1_SEED: want a fraction in [0,1), got %q", v)
		}
		seedFrac = f
	}
	stats := Replay(t, src, seedFrac, maxNotes)
	t.Logf("Q1 replay of %s (seeded at %.0f%% of first-parent history):\n%s", src, seedFrac*100, stats.Format())
}

func getenvDefault(key, def string) string {
	if v, ok := lookupEnv(key); ok {
		return v
	}
	return def
}
