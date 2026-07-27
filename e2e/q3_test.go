package e2e

import (
	"strconv"
	"testing"
)

// TestQ3PrecisionRecall is the Q3 gate run (design.md §14): the
// false-binding rate must be zero — precision-first is the ladder's
// construction, and any false binding is a bug, not a tuning matter —
// while the clean categories must be fully recovered by their matcher.
// DM_Q3_ROUNDS scales the workload (default 3 → 21 branches, 35 notes);
// q3-report.md records the larger runs.
func TestQ3PrecisionRecall(t *testing.T) {
	rounds := 3
	if v := getenvDefault("DM_Q3_ROUNDS", ""); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			t.Fatalf("DM_Q3_ROUNDS: want a positive integer, got %q", v)
		}
		rounds = n
	}
	stats := RunQ3(t, 42, rounds)
	t.Logf("Q3 run (seed 42, %d rounds):\n%s", rounds, stats.Format())

	if len(stats.FalseBindings) != 0 {
		t.Errorf("false-binding rate must be ~0, got %d:\n%s", len(stats.FalseBindings), stats.Format())
	}
	for _, m := range []string{"m1", "m2", "m3"} {
		if r := stats.Recall(m); r < 1 {
			t.Errorf("%s recall %.3f — the synthetic clean categories must fully recover", m, r)
		}
	}
	for _, category := range []string{"squash-conflicted", "abandoned", "sub-floor"} {
		if stats.Refused[category] == 0 {
			t.Errorf("category %s produced no correct refusals — the rig lost its FN coverage", category)
		}
	}
}
