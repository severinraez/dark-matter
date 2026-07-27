package e2e

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// vdFacts extracts `VD … <origin> landed <landed-as> <matcher>` /
// `VD … <origin> unlanded` facts from a dump, as "origin→landed-as:matcher"
// or "origin→unlanded" strings.
var vdRe = regexp.MustCompile(`(?m)^VD \S+ (\S+) (?:landed (\S+) (\S+)|(unlanded))$`)

func vdFacts(dump string) []string {
	var out []string
	for _, m := range vdRe.FindAllStringSubmatch(dump, -1) {
		if m[4] == "unlanded" {
			out = append(out, m[1]+"→unlanded")
		} else {
			out = append(out, m[1]+"→"+m[2]+":"+m[3])
		}
	}
	return out
}

// The Q3 rig (design.md §14, §11.4): landing-detection precision and
// recall. It generates a squash- and force-push-heavy history with *known
// ground truth* — every seeded note's origin is created by the rig, so
// whether its line genuinely landed, and as what, is a recorded fact, not
// an inference. It then drives the real dm binary (reads + syncs — the
// §9.4 mint moments) and audits every minted VD against the truth.
//
// Measured per matcher id: the false-binding rate (must be ~zero — a
// false positive surfaces wrong notes unflagged and replicates) and the
// fraction of genuinely-landed lines recovered. Deliberate-refusal
// categories (conflicted squash, sub-floor, abandoned) pin the
// precision-first guards: any binding there is a false positive.

// q3Truth is one seeded note's ground truth.
type q3Truth struct {
	origin   string
	category string
	expect   string // landed-as this origin's line truly landed at ("" = never landed / must not bind)
	matcher  string // matcher expected to recover it
	optional bool   // accepted-FP category: binding to expect is allowed, not required
}

// Q3Stats tallies one rig run.
type Q3Stats struct {
	Branches int
	Notes    int

	// Per matcher id: genuinely-landed origins it was expected to
	// recover, and how many it did.
	Expected  map[string]int
	Recovered map[string]int
	// Correct refusals in must-not-bind categories, per category.
	Refused map[string]int
	// The gate: bindings that contradict ground truth (wrong target, or
	// any binding on a line that never landed).
	FalseBindings []string
	// Accepted-FP categories that did bind (duplicate work — §9.4).
	AcceptedFP int
}

// Recall returns matcher m's recovered/expected fraction.
func (s Q3Stats) Recall(m string) float64 {
	if s.Expected[m] == 0 {
		return 1
	}
	return float64(s.Recovered[m]) / float64(s.Expected[m])
}

// Format renders the stats for the record.
func (s Q3Stats) Format() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d branches · %d seeded notes\n", s.Branches, s.Notes)
	var ms []string
	for m := range s.Expected {
		ms = append(ms, m)
	}
	sort.Strings(ms)
	for _, m := range ms {
		fmt.Fprintf(&b, "%s: recovered %d/%d (recall %.3f)\n", m, s.Recovered[m], s.Expected[m], s.Recall(m))
	}
	var cs []string
	for c := range s.Refused {
		cs = append(cs, c)
	}
	sort.Strings(cs)
	for _, c := range cs {
		fmt.Fprintf(&b, "refused (correctly): %s ×%d\n", c, s.Refused[c])
	}
	fmt.Fprintf(&b, "accepted-FP bindings (duplicate work): %d\n", s.AcceptedFP)
	fmt.Fprintf(&b, "false bindings: %d", len(s.FalseBindings))
	for _, f := range s.FalseBindings {
		fmt.Fprintf(&b, "\n  FALSE: %s", f)
	}
	b.WriteByte('\n')
	return b.String()
}

// RunQ3 builds and audits one workload: rounds×(squash-clean,
// squash-conflicted, local-rebase, remote-force-push, abandoned,
// sub-floor, duplicate-work) with rng-varied commit counts and contents.
func RunQ3(t *testing.T, seed int64, rounds int) Q3Stats {
	t.Helper()
	rng := rand.New(rand.NewSource(seed))
	bare := NewBareRemote(t)
	a := NewRepo(t)
	a.WriteFile("base.rb", "class Base\n  def a; end\nend\n")
	a.Commit("base")
	a.Git("remote", "add", "origin", bare)
	a.Git("push", "-q", "origin", "main")
	a.MustDM("", "init")
	b := CloneRepo(t, bare, 2)

	var truths []q3Truth
	stats := Q3Stats{
		Expected:  map[string]int{},
		Recovered: map[string]int{},
		Refused:   map[string]int{},
	}

	// contentFor writes distinctive multi-line content so unrelated
	// branches can never collide on patch-ids.
	contentFor := func(name string, salt int64) string {
		var sb strings.Builder
		fmt.Fprintf(&sb, "# %s\n", name)
		lines := 3 + rng.Intn(4)
		for i := 0; i < lines; i++ {
			fmt.Fprintf(&sb, "def m%d_%d = %d\n", i, salt, rng.Int63())
		}
		return sb.String()
	}

	// buildBranch creates branch name off main with n commits, seeding a
	// note on the first commit's file and (n>1) the tip's file. Leaves
	// the repo on the branch; returns origin shas in commit order.
	buildBranch := func(name string, n int) []string {
		a.Git("checkout", "-q", "main")
		a.Git("checkout", "-q", "-b", name)
		var origins []string
		for c := 0; c < n; c++ {
			file := fmt.Sprintf("%s_c%d.rb", name, c)
			a.WriteFile(file, contentFor(file, rng.Int63()))
			sha := a.Commit(fmt.Sprintf("%s commit %d", name, c))
			if c == 0 || c == n-1 {
				a.MustDM(fmt.Sprintf("a:%s:c:q3 note %s c%d\n", file, name, c))
				origins = append(origins, sha)
			}
		}
		return origins
	}
	advanceMain := func(tag string) {
		a.Git("checkout", "-q", "main")
		file := fmt.Sprintf("main_%s.rb", tag)
		a.WriteFile(file, contentFor(file, rng.Int63()))
		a.Commit("main advances " + tag)
		a.Git("push", "-q", "origin", "main")
	}
	record := func(origins []string, category, expect, matcher string, optional bool) {
		for _, o := range origins {
			truths = append(truths, q3Truth{origin: o, category: category, expect: expect, matcher: matcher, optional: optional})
		}
	}

	for round := 0; round < rounds; round++ {
		tag := fmt.Sprintf("r%d", round)

		// squash-clean: forge squash, remote branch deleted, local kept.
		name := "clean-" + tag
		origins := buildBranch(name, 2+rng.Intn(3))
		a.Git("push", "-q", "origin", name)
		a.Git("checkout", "-q", "main")
		a.Git("merge", "-q", "--squash", name)
		s := a.Commit("squash " + name)
		a.Git("push", "-q", "origin", "main")
		a.Git("push", "-q", "origin", ":"+name)
		record(origins, "squash-clean", s, "m3", false)

		// squash-conflicted: the squash result is adjusted → refusal.
		name = "conflict-" + tag
		origins = buildBranch(name, 2)
		a.Git("checkout", "-q", "main")
		a.Git("merge", "-q", "--squash", name)
		a.WriteFile(name+"_c0.rb", contentFor(name+"_c0.rb", rng.Int63()))
		a.Git("add", "-A")
		a.Commit("adjusted squash " + name)
		a.Git("push", "-q", "origin", "main")
		record(origins, "squash-conflicted", "", "", false)

		// local-rebase: main advances, branch rebases onto it (m1).
		name = "rebase-" + tag
		origins = buildBranch(name, 2+rng.Intn(2))
		advanceMain(tag)
		a.Git("checkout", "-q", name)
		a.Git("rebase", "-q", "main")
		record(origins, "local-rebase", a.Git("rev-parse", "HEAD"), "m1", false)

		// remote-force-push: replica B rebases and force-pushes (m2).
		name = "forced-" + tag
		origins = buildBranch(name, 2)
		a.Git("push", "-q", "origin", name)
		advanceMain(tag + "f")
		b.Git("fetch", "-q", "origin")
		b.Git("checkout", "-q", "-B", name, "origin/"+name)
		b.Git("rebase", "-q", "origin/main")
		newTip := b.Git("rev-parse", "HEAD")
		b.Git("push", "-q", "--force", "origin", name)
		a.Git("branch", "-q", "-D", name) // author's stale local ref gone; evidence is the forced update
		record(origins, "remote-force-push", newTip, "m2", false)

		// abandoned: deleted unmerged — any binding is a false positive.
		name = "dead-" + tag
		origins = buildBranch(name, 1+rng.Intn(2))
		a.Git("checkout", "-q", "main")
		a.Git("branch", "-q", "-D", name)
		record(origins, "abandoned", "", "", false)

		// sub-floor: a one-line branch squashed — floor refusal.
		name = "tiny-" + tag
		a.Git("checkout", "-q", "main")
		a.Git("checkout", "-q", "-b", name)
		a.WriteFile(name+".txt", fmt.Sprintf("line %d\n", rng.Int63()))
		tinyTip := a.Commit("tiny " + name)
		a.MustDM(fmt.Sprintf("a:%s.txt:c:q3 tiny note %s\n", name, name))
		a.Git("checkout", "-q", "main")
		a.Git("merge", "-q", "--squash", name)
		a.Commit("squash " + name)
		a.Git("push", "-q", "origin", "main")
		record([]string{tinyTip}, "sub-floor", "", "", false)

		// duplicate-work: byte-identical content lands independently as
		// one commit; binding is the accepted FP (§9.4).
		name = "dup-" + tag
		origins = buildBranch(name, 1)
		content := readRepoFile(t, a, name+"_c0.rb")
		a.Git("checkout", "-q", "main")
		a.WriteFile(name+"_c0.rb", content)
		twin := a.Commit("independent duplicate of " + name)
		a.Git("push", "-q", "origin", "main")
		record(origins, "duplicate-work", twin, "m3", true)
	}

	// The mint moments: reads bind m1 (and memoize attempts); the sync
	// binds m2/m3 with the fresh target line at hand and shares.
	a.MustDM("", "sync")
	a.MustDM("r:base.rb\n")
	a.MustDM("", "sync")

	// Audit every minted VD against ground truth.
	dump := a.MustDM("", "dump").Stdout
	bound := map[string][2]string{} // origin → (landed-as, matcher)
	for _, f := range vdFacts(dump) {
		origin, rest, _ := strings.Cut(f, "→")
		if rest == "unlanded" {
			continue
		}
		landedAs, matcher, _ := strings.Cut(rest, ":")
		bound[origin] = [2]string{landedAs, matcher}
	}
	stats.Branches = rounds * 7
	stats.Notes = len(truths)
	for _, tr := range truths {
		got, isBound := bound[tr.origin]
		switch {
		case tr.optional:
			if isBound {
				if got[0] == tr.expect {
					stats.AcceptedFP++
				} else {
					stats.FalseBindings = append(stats.FalseBindings,
						fmt.Sprintf("%s (%s): bound to %s, duplicate landed as %s", tr.origin, tr.category, got[0], tr.expect))
				}
			}
		case tr.expect == "":
			if isBound {
				stats.FalseBindings = append(stats.FalseBindings,
					fmt.Sprintf("%s (%s): bound to %s via %s, line never landed", tr.origin, tr.category, got[0], got[1]))
			} else {
				stats.Refused[tr.category]++
			}
		default:
			stats.Expected[tr.matcher]++
			if !isBound {
				continue // a recall miss, tallied by the caller's threshold
			}
			if got[0] == tr.expect && got[1] == tr.matcher {
				stats.Recovered[tr.matcher]++
			} else {
				stats.FalseBindings = append(stats.FalseBindings,
					fmt.Sprintf("%s (%s): bound to %s via %s, want %s via %s", tr.origin, tr.category, got[0], got[1], tr.expect, tr.matcher))
			}
		}
	}
	return stats
}

// readRepoFile reads a working-tree file byte-exactly (the duplicate-work
// category needs identical bytes for identical patch-ids).
func readRepoFile(t *testing.T, r *Repo, path string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(r.Dir, path))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
