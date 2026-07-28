package e2e

import (
	"os"
	"strconv"
	"testing"
	"time"
)

// TestQ2ReadPath is the always-on Q2 gate: a small matrix cell must
// complete with sane read times (a generous tripwire, not a benchmark —
// the calibrated numbers live in q2-report.md, produced with
// DM_Q2_SCALE). Scale up for report runs:
//
//	DM_Q2_SCALE=5 go test -run TestQ2ReadPath -v -timeout 30m ./e2e/
func TestQ2ReadPath(t *testing.T) {
	if testing.Short() {
		t.Skip("Q2 rig builds a sizeable history; skipped with -short")
	}
	scale := 1
	if s := os.Getenv("DM_Q2_SCALE"); s != "" {
		var err error
		if scale, err = strconv.Atoi(s); err != nil || scale < 1 {
			t.Fatalf("DM_Q2_SCALE %q", s)
		}
	}
	cfg := Q2Config{
		Commits:    100 * scale,
		Stable:     10,
		Unresolved: 10 * scale,
		Dead:       5 * scale,
		Age:        50 * scale,
	}
	// Per-dimension overrides for report runs isolating one axis.
	reportRun := false
	for env, field := range map[string]*int{
		"DM_Q2_COMMITS": &cfg.Commits, "DM_Q2_UNRESOLVED": &cfg.Unresolved,
		"DM_Q2_DEAD": &cfg.Dead, "DM_Q2_AGE": &cfg.Age,
	} {
		if s := os.Getenv(env); s != "" {
			v, err := strconv.Atoi(s)
			if err != nil {
				t.Fatalf("%s %q", env, s)
			}
			*field = v
			reportRun = true
		}
	}
	res := RunQ2(t, cfg)
	t.Logf("Q2 %+v → %s", cfg, res)
	if reportRun || scale > 1 {
		return // measurement run: numbers go to q2-report.md, no gate
	}

	// Tripwire on the default cell only: warm reads are the steady state
	// agents live in; even on slow CI they must stay well under
	// interactive pain. Cold and old-checkout paths get a looser bound
	// (they include the batched diffs and index rebuild).
	warmCeil := 3 * time.Second
	coldCeil := 15 * time.Second
	if res.Warm > warmCeil || res.OldWarm > warmCeil {
		t.Errorf("warm read exceeded %v: %s", warmCeil, res)
	}
	if res.Cold > coldCeil || res.OldCold > coldCeil || res.Worklist > coldCeil {
		t.Errorf("cold/worklist path exceeded %v: %s", coldCeil, res)
	}
}
