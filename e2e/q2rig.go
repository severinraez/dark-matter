package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The Q2 rig (design.md §14, §11.4): read-path performance on the §8.2
// residue. It generates a synthetic repo along the profiling matrix —
// repo size (commits × files) × cold/warm cache × checkout age ×
// unresolved-note count × dead-origin count — then times real dm reads.
//
// What each dimension stresses:
//   - cold: first read after a cache wipe — index rebuild plus the
//     batched origin→checkout diffs;
//   - warm: the steady state — index load plus tree lookups;
//   - checkout age: reads at an old detached commit (tree lookups against
//     a checkout far from every origin);
//   - unresolved notes: diff multiplicity — one batched diff per distinct
//     origin among unresolved notes, origins deliberately spread over
//     distinct commits;
//   - dead origins: derived-abandoned re-checks (live, bounded by the
//     ff-aware unreachability cache);
//   - worklist: the hygiene pass over all of the above at once.

// Q2Config is one cell row of the profiling matrix.
type Q2Config struct {
	Commits    int // history length; each commit adds/edits one churn file
	Stable     int // stable files noted with resolved (layer-1) notes
	Unresolved int // notes on churn files later rewritten (distinct origins)
	Dead       int // notes on deleted branches (derived abandoned)
	Age        int // checkout age in commits for the old-checkout timing
}

// Q2Result carries the measured wall times.
type Q2Result struct {
	Cold, Warm, OldCold, OldWarm, Worklist time.Duration
}

func (r Q2Result) String() string {
	return fmt.Sprintf("cold %v · warm %v · old-cold %v · old-warm %v · worklist %v",
		r.Cold.Round(time.Millisecond), r.Warm.Round(time.Millisecond),
		r.OldCold.Round(time.Millisecond), r.OldWarm.Round(time.Millisecond),
		r.Worklist.Round(time.Millisecond))
}

// RunQ2 builds the synthetic repo for one config and measures the reads.
func RunQ2(t *testing.T, cfg Q2Config) Q2Result {
	t.Helper()
	r := NewRepo(t)

	// Stable files, noted at the end so they resolve at layer 1.
	for i := 0; i < cfg.Stable; i++ {
		r.WriteFile(fmt.Sprintf("stable/s%d.rb", i),
			fmt.Sprintf("stable %d\nline a\nline b\nline c\n", i))
	}
	r.WriteFile("churn/seed.rb", "seed\n")
	r.Commit("seed")
	r.MustDM("", "init")

	// History: one churn file per commit. Unresolved notes ride distinct
	// commits spread across the history (distinct origins → distinct
	// batched diffs), and their files are fully rewritten afterwards so
	// they stay unresolved (layer 5) at every later read.
	noteEvery := 0
	if cfg.Unresolved > 0 {
		noteEvery = cfg.Commits / cfg.Unresolved
		if noteEvery == 0 {
			noteEvery = 1
		}
	}
	noted := 0
	for i := 0; i < cfg.Commits; i++ {
		r.WriteFile(fmt.Sprintf("churn/c%d.rb", i),
			fmt.Sprintf("churn %d\nalpha\nbeta\ngamma\ndelta\n", i))
		r.Commit(fmt.Sprintf("commit %d", i))
		if noteEvery > 0 && i%noteEvery == 0 && noted < cfg.Unresolved {
			r.MustDM(fmt.Sprintf("a:churn/c%d.rb:c:unresolved note %d\n", i, i))
			noted++
		}
	}
	// Rewrite every noted churn file in one commit: below the similarity
	// floor, so each note's origin stays a live diff source.
	if noted > 0 {
		for i, n := 0, 0; i < cfg.Commits && n < noted; i++ {
			if noteEvery > 0 && i%noteEvery == 0 {
				r.WriteFile(fmt.Sprintf("churn/c%d.rb", i),
					fmt.Sprintf("totally\nforeign\nrewrite %d\n", i))
				n++
			}
		}
		r.Commit("rewrite noted churn files")
	}

	// Dead origins: a branch, a note, a deletion — each leaves a
	// derived-abandoned line the worklist and reads must re-check.
	for i := 0; i < cfg.Dead; i++ {
		branch := fmt.Sprintf("dead-%d", i)
		r.Git("checkout", "-q", "-b", branch)
		r.WriteFile(fmt.Sprintf("dead/d%d.rb", i), fmt.Sprintf("dead work %d\n", i))
		r.Commit("dead work " + branch)
		r.MustDM(fmt.Sprintf("a:dead/d%d.rb:c:dead note %d\n", i, i))
		r.Git("checkout", "-q", "main")
		r.Git("branch", "-q", "-D", branch)
	}

	// Resolved notes on the stable files, then everything into the store
	// (remoteless sync — the read path under test is store ∪ pending).
	var batch strings.Builder
	for i := 0; i < cfg.Stable; i++ {
		fmt.Fprintf(&batch, "a:stable/s%d.rb:c:stable note %d\n", i, i)
	}
	r.MustDM(batch.String())
	r.MustDM("", "sync")

	read := "r:stable/s0.rb\nr:stable/\n"
	var res Q2Result
	res.Cold = r.timeDM(t, true, read)
	res.Warm = r.timeDM(t, false, read)

	if cfg.Age > 0 {
		r.Git("checkout", "-q", fmt.Sprintf("HEAD~%d", cfg.Age))
		res.OldCold = r.timeDM(t, true, read)
		res.OldWarm = r.timeDM(t, false, read)
		r.Git("checkout", "-q", "main")
	}
	res.Worklist = r.timeDMArgs(t, false, "worklist", "--all")
	return res
}

// timeDM times one batch invocation, optionally wiping the §8.2 cache
// first (cache files are disposable by contract — deleting the directory
// must be unobservable to results).
func (r *Repo) timeDM(t *testing.T, cold bool, stdin string) time.Duration {
	t.Helper()
	if cold {
		if err := os.RemoveAll(filepath.Join(r.DMDir(), "cache")); err != nil {
			t.Fatal(err)
		}
	}
	start := time.Now()
	r.MustDM(stdin)
	return time.Since(start)
}

func (r *Repo) timeDMArgs(t *testing.T, cold bool, args ...string) time.Duration {
	t.Helper()
	if cold {
		if err := os.RemoveAll(filepath.Join(r.DMDir(), "cache")); err != nil {
			t.Fatal(err)
		}
	}
	start := time.Now()
	r.MustDM("", args...)
	return time.Since(start)
}
