package e2e

import (
	"fmt"
	"strings"
	"testing"
)

// The §11.4 landing-inference block — M6's exit bar: every scenario
// asserts minted VDs *with their matcher id* via `dm dump`. Notation
// follows the design: feature = F1→F3 off main, note N1 with origin F1,
// N2 with origin F2 (intermediate), squash commit S.

// landingRepo is the block's base state: a shared remote, an author clone
// with main plus feature (F1..F3, each ≥ the m2/m3 min-diff floor), notes
// N1 (fa.rb, origin F1) and N2 (fb.rb, origin F2) already synced to the
// store, and main advanced past the branch point.
type landingRepo struct {
	*Repo
	bare       string
	F1, F2, F3 string
	MainTip    string // main's tip after the post-branch advance
}

const (
	bodyN1 = "N1 gotcha about fa"
	bodyN2 = "N2 gotcha about fb"
)

func newLandingRepo(t *testing.T) *landingRepo {
	t.Helper()
	bare := NewBareRemote(t)
	a := NewRepo(t)
	l := &landingRepo{Repo: a, bare: bare}
	a.WriteFile("base.rb", "class Base\n  def a; end\n  def b; end\nend\n")
	a.Commit("base")
	a.Git("remote", "add", "origin", bare)
	a.Git("push", "-q", "origin", "main")
	a.MustDM("", "init")

	a.Git("checkout", "-q", "-b", "feature")
	a.WriteFile("fa.rb", "class Fa\n  def one; end\n  def two; end\n  def three; end\nend\n")
	l.F1 = a.Commit("F1: add fa")
	a.MustDM("a:fa.rb:c:" + bodyN1 + "\n")
	a.WriteFile("fb.rb", "class Fb\n  def uno; end\n  def dos; end\n  def tres; end\nend\n")
	l.F2 = a.Commit("F2: add fb")
	a.MustDM("a:fb.rb:c:" + bodyN2 + "\n")
	a.WriteFile("fc.rb", "class Fc\n  def x; end\n  def y; end\n  def z; end\nend\n")
	l.F3 = a.Commit("F3: add fc")
	a.MustDM("", "sync") // notes shared before any rewrite

	a.Git("checkout", "-q", "main")
	a.WriteFile("m.rb", "class M\n  def m1; end\n  def m2; end\nend\n")
	l.MainTip = a.Commit("main advances")
	a.Git("push", "-q", "origin", "main")
	a.Git("checkout", "-q", "feature")
	return l
}

func hasVD(dump, origin, landedAs, matcher string) bool {
	for _, f := range vdFacts(dump) {
		if f == origin+"→"+landedAs+":"+matcher {
			return true
		}
	}
	return false
}

// ---- m1 — local reflog succession ----

func TestM1RebaseClean(t *testing.T) {
	l := newLandingRepo(t)
	l.Git("rebase", "-q", "main")
	newTip := l.Git("rev-parse", "HEAD")

	// The next read mints per-origin m1 VDs into pending — before any
	// sync — and shows N1+N2 on the rebased branch.
	res := l.MustDM("r:fa.rb\nr:fb.rb\n")
	for _, body := range []string{bodyN1, bodyN2} {
		if !strings.Contains(res.Stdout, body) {
			t.Errorf("rebased read misses %q:\n%s", body, res.Stdout)
		}
	}
	dump := l.MustDM("", "dump").Stdout
	if !hasVD(dump, l.F1, newTip, "m1") || !hasVD(dump, l.F2, newTip, "m1") {
		t.Fatalf("want m1 VDs %s→%s and %s→%s, got %v", l.F1, newTip, l.F2, newTip, vdFacts(dump))
	}
	if names := l.PendingRecordNames(); len(names) == 0 {
		t.Error("m1 VDs should sit in pending until the next sync")
	}
}

func TestM1RebaseConflict(t *testing.T) {
	l := newLandingRepo(t)
	// main also adds fa.rb (different content) so the rebase conflicts.
	l.Git("checkout", "-q", "main")
	l.WriteFile("fa.rb", "class Fa\n  def one; end\n  def two; end\n  def three; end\n  # main side\nend\n")
	l.Commit("main touches fa")
	l.Git("checkout", "-q", "feature")
	if out, err := l.gitAllowFail("rebase", "main"); err == nil {
		t.Fatalf("expected a conflict, rebase succeeded:\n%s", out)
	}
	// Resolve: near the noted content, but drifted (stays in the §9.2
	// auto-accept band so rule (a) flags rather than orphans).
	l.WriteFile("fa.rb", "class Fa\n  def one; end\n  def two; end\n  def three; end\n  # resolved\nend\n")
	l.Git("add", "-A")
	l.gitEnv([]string{"GIT_EDITOR=true"}, "rebase", "--continue")
	newTip := l.Git("rev-parse", "HEAD")

	// m1 binds regardless — the reflog needs no patch match — and fa.rb's
	// note surfaces stale via rule (a).
	res := l.MustDM("r:fa.rb\nr:fb.rb\n")
	if !strings.Contains(res.Stdout, bodyN1) || !strings.Contains(res.Stdout, "⚠stale") {
		t.Errorf("want N1 visible and ⚠stale after conflicted rebase:\n%s", res.Stdout)
	}
	if !strings.Contains(res.Stdout, bodyN2) {
		t.Errorf("want N2 visible after conflicted rebase:\n%s", res.Stdout)
	}
	dump := l.MustDM("", "dump").Stdout
	if !hasVD(dump, l.F1, newTip, "m1") || !hasVD(dump, l.F2, newTip, "m1") {
		t.Fatalf("conflicted rebase must still m1-bind, got %v", vdFacts(dump))
	}
}

func TestM1Amend(t *testing.T) {
	l := newLandingRepo(t)
	l.WriteFile("fc.rb", "class Fc\n  def x; end\n  def y; end\n  def z; end\n  def amended; end\nend\n")
	l.Git("add", "-A")
	l.commits++
	l.Git("commit", "-q", "--amend", "-m", "F3 amended")
	newTip := l.Git("rev-parse", "HEAD")

	// A note written at F3 would be the direct subject; N1/N2 (F1/F2) are
	// not in the amended segment (mb..F3] = {F3}. Write one at F3 first?
	// The base state has no F3 note, so assert the segment discipline:
	// no VD for F1/F2, which stay landed via ancestry anyway.
	l.MustDM("r:fc.rb\n")
	dump := l.MustDM("", "dump").Stdout
	for _, f := range vdFacts(dump) {
		t.Errorf("amend with no origin in segment minted %s", f)
	}

	// Now a note on the amended tip, amended again: the origin binds.
	l.MustDM("a:fc.rb:c:note at amended tip\n")
	l.WriteFile("fc.rb", "class Fc\n  def x; end\n  def y; end\n  def z; end\n  def amended2; end\nend\n")
	l.Git("add", "-A")
	l.commits++
	l.Git("commit", "-q", "--amend", "-m", "F3 amended twice")
	final := l.Git("rev-parse", "HEAD")
	res := l.MustDM("r:fc.rb\n")
	if !strings.Contains(res.Stdout, "note at amended tip") {
		t.Errorf("note lost across amend:\n%s", res.Stdout)
	}
	dump = l.MustDM("", "dump").Stdout
	if !hasVD(dump, newTip, final, "m1") {
		t.Fatalf("want m1 VD %s→%s after amend, got %v", newTip, final, vdFacts(dump))
	}
}

func TestM1ResetNoBind(t *testing.T) {
	l := newLandingRepo(t)
	l.Git("reset", "-q", "--hard", "main")

	// FP guard: the `reset:` action never binds; the origins classify
	// derived-abandoned (worklisted, hidden from reads).
	res := l.MustDM("r:fa.rb\nr:fb.rb\n")
	if strings.Contains(res.Stdout, bodyN1) || strings.Contains(res.Stdout, bodyN2) {
		t.Errorf("reset must not carry notes along:\n%s", res.Stdout)
	}
	dump := l.MustDM("", "dump").Stdout
	if facts := vdFacts(dump); len(facts) != 0 {
		t.Fatalf("reset minted VDs: %v", facts)
	}
	wl := l.MustDM("", "worklist").Stdout
	if !strings.Contains(wl, "line "+l.F2) || !strings.Contains(wl, "2 notes") {
		t.Errorf("abandoned line not grouped on worklist:\n%s", wl)
	}
}

func TestM1BranchReuseNoBind(t *testing.T) {
	l := newLandingRepo(t)
	l.Git("checkout", "-q", "main")
	l.WriteFile("other.rb", "class Other\n  def p; end\n  def q; end\nend\n")
	unrelated := l.Commit("unrelated occupant")
	l.Git("checkout", "-q", "-B", "feature", unrelated)

	// FP guard: `branch: Reset` (checkout -B) is filtered; no binding.
	l.MustDM("r:fa.rb\n")
	dump := l.MustDM("", "dump").Stdout
	if facts := vdFacts(dump); len(facts) != 0 {
		t.Fatalf("branch reuse minted VDs: %v", facts)
	}
}

func TestM1DropAcceptedFP(t *testing.T) {
	l := newLandingRepo(t)
	// Interactive rebase that drops F2, expressed as --onto (same reflog
	// action): F3 replays onto F1, F2's commit disappears.
	l.Git("rebase", "-q", "--onto", l.F1, l.F2, "feature")
	tip := l.Git("rev-parse", "HEAD")

	// The segment (mb..F3] still contained F2, so its note binds with the
	// line (the accepted FP, pinned): N2's file fb.rb was dropped with the
	// commit, so rule (a) orphans it — bounded exactly as designed.
	l.MustDM("r:fa.rb\n")
	full := l.MustDM("", "dump").Stdout
	if !hasVD(full, l.F2, tip, "m1") {
		t.Fatalf("dropped commit's origin must bind with its segment (accepted FP), got %v", vdFacts(full))
	}
	// N1 (F1 kept under the new line) stays landed and surfaces.
	res := l.MustDM("r:fa.rb\n")
	if !strings.Contains(res.Stdout, bodyN1) {
		t.Errorf("N1 must land through the drop rebase:\n%s", res.Stdout)
	}
	// N2 sits on dropped content: invisible in reads, orphaned on the
	// worklist (rule (a) bounds the accepted FP).
	if res := l.MustDM("r:fb.rb\n"); strings.Contains(res.Stdout, bodyN2) {
		t.Errorf("N2's content was dropped; it must not surface:\n%s", res.Stdout)
	}
	wl := l.MustDM("", "worklist").Stdout
	if !strings.Contains(wl, "orphaned") {
		t.Errorf("dropped-content note should be orphaned on the worklist:\n%s", wl)
	}
}

// ---- helpers the conflict/interactive scenarios need ----

// gitAllowFail runs git and returns its combined output and error instead
// of failing the test.
func (r *Repo) gitAllowFail(args ...string) (string, error) {
	cmd := gitCommand(r, nil, args...)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// gitEnv runs git with extra environment (e.g. GIT_EDITOR).
func (r *Repo) gitEnv(extra []string, args ...string) string {
	r.t.Helper()
	cmd := gitCommand(r, extra, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// ---- m1r / m2 — rewrites performed elsewhere ----

// pushFeature publishes the author's feature branch so a second replica
// can rewrite it remotely.
func (l *landingRepo) pushFeature() {
	l.Git("push", "-q", "origin", "feature")
}

func TestM2RemoteRebaseClean(t *testing.T) {
	l := newLandingRepo(t)
	l.pushFeature()
	l.Git("checkout", "-q", "main") // author stays off the branch

	b := CloneRepo(t, l.bare, 2)
	b.MustDM("", "init")
	b.Git("checkout", "-q", "-b", "feature", "origin/feature")
	b.Git("rebase", "-q", "origin/main")
	b.Git("push", "-q", "--force-with-lease", "origin", "feature")
	newTip := b.Git("rev-parse", "HEAD")

	// A's fetch sees the forced update (m1r candidate only); the full
	// in-order pairing mints m2 VDs.
	l.MustDM("", "sync")
	dump := l.MustDM("", "dump").Stdout
	if !hasVD(dump, l.F1, newTip, "m2") || !hasVD(dump, l.F2, newTip, "m2") {
		t.Fatalf("want m2 VDs onto %s, got %v", newTip, vdFacts(dump))
	}
	// On the rebased line the notes surface through the chain.
	l.Git("checkout", "-q", "-B", "feature", "origin/feature")
	res := l.MustDM("r:fa.rb\nr:fb.rb\n")
	if !strings.Contains(res.Stdout, bodyN1) || !strings.Contains(res.Stdout, bodyN2) {
		t.Errorf("notes missing on remotely-rebased line:\n%s", res.Stdout)
	}
}

func TestM1RAloneNoBind(t *testing.T) {
	l := newLandingRepo(t)
	l.pushFeature()
	l.Git("checkout", "-q", "main")
	l.Git("branch", "-q", "-D", "feature") // author's local ref gone too

	b := CloneRepo(t, l.bare, 2)
	b.MustDM("", "init")
	// Branch reuse: the name now points at unrelated content.
	b.WriteFile("occupant.rb", "class Occupant\n  def a; end\n  def b; end\nend\n")
	b.Git("checkout", "-q", "-b", "tmp", "origin/main")
	occupant := b.Commit("unrelated occupant")
	b.Git("push", "-q", "--force", "origin", "tmp:feature")
	_ = occupant

	// FP guard: a forced update alone is a candidate generator, never a
	// binder — pairing fails, old origins classify derived-abandoned.
	l.MustDM("", "sync")
	dump := l.MustDM("", "dump").Stdout
	if facts := vdFacts(dump); len(facts) != 0 {
		t.Fatalf("forced update to unrelated occupant minted VDs: %v", facts)
	}
	wl := l.MustDM("", "worklist").Stdout
	if !strings.Contains(wl, "line ") {
		t.Errorf("old origins should worklist as an abandoned line:\n%s", wl)
	}
}

func TestM2TipOnlyNoBind(t *testing.T) {
	l := newLandingRepo(t)
	l.pushFeature()
	l.Git("checkout", "-q", "main")
	l.Git("branch", "-q", "-D", "feature")

	b := CloneRepo(t, l.bare, 2)
	b.MustDM("", "init")
	// A new line where only the tip commit's patch pairs: cherry-pick F3
	// alone onto main and force-push as feature.
	b.Git("checkout", "-q", "-b", "rebuilt", "origin/main")
	b.Git("cherry-pick", b.Git("rev-parse", "origin/feature"))
	b.Git("push", "-q", "--force", "origin", "rebuilt:feature")

	// FP guard: tips pair but F1/F2 don't — full-segment in-order pairing
	// refuses.
	l.MustDM("", "sync")
	dump := l.MustDM("", "dump").Stdout
	if facts := vdFacts(dump); len(facts) != 0 {
		t.Fatalf("tip-only pairing minted VDs: %v", facts)
	}
}

func TestM2ConflictHealsViaAuthorM1(t *testing.T) {
	l := newLandingRepo(t)
	// Conflict material: main also adds fa.rb.
	l.Git("checkout", "-q", "main")
	l.WriteFile("fa.rb", "class Fa\n  def one; end\n  def two; end\n  def three; end\n  # main side\nend\n")
	l.Commit("main touches fa")
	l.Git("push", "-q", "origin", "main")
	l.Git("checkout", "-q", "feature")
	l.pushFeature()

	b := CloneRepo(t, l.bare, 2)
	b.MustDM("", "init")
	b.Git("checkout", "-q", "-b", "feature", "origin/feature")
	if out, err := b.gitAllowFail("rebase", "origin/main"); err == nil {
		t.Fatalf("expected conflicted rebase, got:\n%s", out)
	}
	b.WriteFile("fa.rb", "class Fa\n  def one; end\n  def two; end\n  def three; end\n  # b resolved\nend\n")
	b.Git("add", "-A")
	b.gitEnv([]string{"GIT_EDITOR=true"}, "rebase", "--continue")
	newTip := b.Git("rev-parse", "HEAD")
	b.Git("push", "-q", "--force-with-lease", "origin", "feature")

	// A: pairing fails on the conflict-adjusted commit → no record; A
	// derives abandoned for the old line (its local feature ref was never
	// moved — delete it to make the old line truly dead on A).
	l.Git("checkout", "-q", "main")
	l.Git("branch", "-q", "-D", "feature")
	l.MustDM("", "sync")
	if facts := vdFacts(l.MustDM("", "dump").Stdout); len(facts) != 0 {
		t.Fatalf("conflicted remote rebase minted VDs on A: %v", facts)
	}

	// B performed the rebase: its read mints m1; both sync; A's next read
	// shows the chain landed and a clear worklist.
	b.MustDM("r:fa.rb\n")
	if !hasVD(b.MustDM("", "dump").Stdout, l.F1, newTip, "m1") {
		t.Fatalf("B's reflog must m1-bind its own conflicted rebase, got %v", vdFacts(b.MustDM("", "dump").Stdout))
	}
	b.MustDM("", "sync")
	l.MustDM("", "sync")
	l.Git("checkout", "-q", "-B", "feature", "origin/feature")
	res := l.MustDM("r:fa.rb\n")
	if !strings.Contains(res.Stdout, bodyN1) {
		t.Errorf("healed line must surface N1 on A:\n%s", res.Stdout)
	}
	wl := l.MustDM("", "worklist").Stdout
	if strings.Contains(wl, "line ") {
		t.Errorf("worklist should be clear of abandoned groups after healing:\n%s", wl)
	}
}

func TestM2FloorNoBind(t *testing.T) {
	bare := NewBareRemote(t)
	a := NewRepo(t)
	a.WriteFile("base.rb", "class Base\n  def a; end\nend\n")
	a.Commit("base")
	a.Git("remote", "add", "origin", bare)
	a.Git("push", "-q", "origin", "main")
	a.MustDM("", "init")
	// A trivial one-line branch, noted, shared, pushed.
	a.Git("checkout", "-q", "-b", "tiny")
	a.WriteFile("t.txt", "x\n")
	f1 := a.Commit("tiny change")
	a.MustDM("a:t.txt:c:tiny note\n")
	a.MustDM("", "sync")
	a.Git("push", "-q", "origin", "tiny")
	a.Git("checkout", "-q", "main")
	a.Git("branch", "-q", "-D", "tiny")

	b := CloneRepo(t, bare, 2)
	b.MustDM("", "init")
	b.Git("checkout", "-q", "-b", "tiny", "origin/tiny")
	b.Git("rebase", "-q", "origin/main") // no-op base, force a rewrite via amend
	b.commits++
	b.Git("commit", "-q", "--amend", "--no-edit", "--reset-author")
	b.Git("push", "-q", "--force", "origin", "tiny")

	// FP guard: the segment pairs, but its total diff (1 line) is below
	// the floor — under branch reuse this shape collides, so no bind.
	a.MustDM("", "sync")
	if facts := vdFacts(a.MustDM("", "dump").Stdout); len(facts) != 0 {
		t.Fatalf("sub-floor segment minted VDs: %v", facts)
	}
	_ = f1
}

// ---- m3 — cumulative squash ----

// squashFeature squash-merges feature into main as one commit S and
// returns S; the remote branch is deleted (forge auto-delete), the local
// ref survives — the flagship shape.
func (l *landingRepo) squashFeature(msg string) string {
	l.Git("checkout", "-q", "main")
	l.Git("merge", "-q", "--squash", "feature")
	s := l.Commit(msg)
	l.Git("push", "-q", "origin", "main")
	l.Git("push", "-q", "origin", ":feature")
	return s
}

func TestM3SquashMultiCommit(t *testing.T) {
	l := newLandingRepo(t)
	l.pushFeature()
	s := l.squashFeature("feature squashed (S)")

	// Before the author's sync, teammates see nothing — the
	// eventual-consistency window, pinned.
	b := CloneRepo(t, l.bare, 2)
	b.MustDM("", "init")
	res := b.MustDM("r:fa.rb\nr:fb.rb\n")
	if strings.Contains(res.Stdout, bodyN1) || strings.Contains(res.Stdout, bodyN2) {
		t.Fatalf("teammate sees notes before the author's mint sync:\n%s", res.Stdout)
	}

	// The author's single sync binds F3→S, bulk-mints m3 VDs for F1 *and
	// intermediate F2*, and pushes them in the same sync
	// (mint-pass-order, pinned).
	l.MustDM("", "sync")
	dump := l.MustDM("", "dump").Stdout
	if !hasVD(dump, l.F1, s, "m3") || !hasVD(dump, l.F2, s, "m3") {
		t.Fatalf("want m3 VDs %s→%s and %s→%s, got %v", l.F1, s, l.F2, s, vdFacts(dump))
	}
	if names := l.PendingRecordNames(); len(names) != 0 {
		t.Fatalf("mint-pass verdicts did not ride the same sync: %v", names)
	}

	// The teammate needs only its own sync.
	b.MustDM("", "sync")
	res = b.MustDM("r:fa.rb\nr:fb.rb\n")
	if !strings.Contains(res.Stdout, bodyN1) || !strings.Contains(res.Stdout, bodyN2) {
		t.Errorf("teammate misses landed notes after sync:\n%s", res.Stdout)
	}
}

func TestM3UpdateBranchFlow(t *testing.T) {
	l := newLandingRepo(t)
	// A note whose origin is a main-side commit — segment enumeration
	// must never cover it.
	l.Git("checkout", "-q", "main")
	l.MustDM("a:m.rb:c:main-side note\n")
	l.Git("checkout", "-q", "feature")
	// Update-branch: merge main into feature (T2), then squash.
	l.commits++
	l.Git("merge", "-q", "--no-edit", "main")
	l.pushFeature()
	s := l.squashFeature("feature squashed after update-branch")

	l.MustDM("", "sync")
	dump := l.MustDM("", "dump").Stdout
	if !hasVD(dump, l.F1, s, "m3") || !hasVD(dump, l.F2, s, "m3") {
		t.Fatalf("update-branch squash must bind at T2, got %v", vdFacts(dump))
	}
	for _, f := range vdFacts(dump) {
		if strings.HasPrefix(f, l.MainTip+"→") {
			t.Fatalf("segment enumeration covered a main-side commit: %s", f)
		}
	}
}

func TestM3ConflictedSquashFNAndM5Disposition(t *testing.T) {
	l := newLandingRepo(t)
	l.pushFeature()
	// Divergent squash content: the merge result is edited before S —
	// lightly, so rule (a) later flags stale instead of orphaning.
	l.Git("checkout", "-q", "main")
	l.Git("merge", "-q", "--squash", "feature")
	l.WriteFile("fa.rb", "class Fa\n  def one; end\n  def two; end\n  def three; end\n  # adjusted\nend\n")
	l.Git("add", "-A")
	s := l.Commit("conflict-adjusted squash")
	l.Git("push", "-q", "origin", "main")
	l.Git("push", "-q", "origin", ":feature")

	// No bind — and the failed attempt memoizes the matcher hint.
	l.MustDM("", "sync")
	if facts := vdFacts(l.MustDM("", "dump").Stdout); len(facts) != 0 {
		t.Fatalf("divergent squash minted VDs: %v", facts)
	}
	// The author moves on; the local ref dies; the line derives abandoned
	// and the worklist groups it with the hint (golden format:
	// tip · branch hint · note count · matcher hint).
	l.Git("branch", "-q", "-D", "feature")
	wl := l.MustDM("", "worklist").Stdout
	// The group tip is the newest *origin* (F2 — F3 carries no note); the
	// memoized m3 hint attaches because F3's failed attempt shares the line.
	wantLine := "line " + l.F2 + " · no ref · 2 notes"
	if !strings.Contains(wl, wantLine) || !strings.Contains(wl, "m3: no squash candidate") {
		t.Fatalf("worklist group missing or unhinted (want %q + m3 hint):\n%s", wantLine, wl)
	}

	// m5-disposition: one vd for the tip surfaces *all* notes on the line.
	res := l.MustDM(fmt.Sprintf("vd:%s:landed:%s\n", l.F3, s))
	if !strings.Contains(res.Stdout, fmt.Sprintf("✓ vd landed %s · 3 origins bound", s)) {
		t.Fatalf("vd ack wrong (want 3 origins: F1, F2, and the tip):\n%s", res.Stdout)
	}
	dump := l.MustDM("", "dump").Stdout
	if !hasVD(dump, l.F1, s, "m5") || !hasVD(dump, l.F2, s, "m5") || !hasVD(dump, l.F3, s, "m5") {
		t.Fatalf("m5 VDs missing: %v", vdFacts(dump))
	}
	read := l.MustDM("r:fb.rb\n")
	if !strings.Contains(read.Stdout, bodyN2) {
		t.Errorf("disposed line must surface its notes:\n%s", read.Stdout)
	}
	// fa.rb was conflict-adjusted: visible but stale.
	read = l.MustDM("r:fa.rb\n")
	if !strings.Contains(read.Stdout, bodyN1) || !strings.Contains(read.Stdout, "⚠stale") {
		t.Errorf("adjusted file's note should surface ⚠stale:\n%s", read.Stdout)
	}
	if wl := l.MustDM("", "worklist").Stdout; strings.Contains(wl, "line ") {
		t.Errorf("worklist group must clear after m5 disposition:\n%s", wl)
	}
}

func TestM3TwoCandidatesNoBind(t *testing.T) {
	l := newLandingRepo(t)
	// Apply → revert → re-apply the feature's cumulative diff on main.
	l.Git("checkout", "-q", "main")
	l.Git("merge", "-q", "--squash", "feature")
	first := l.Commit("apply")
	l.commits++
	l.Git("revert", "--no-edit", first)
	l.Git("merge", "-q", "--squash", "feature")
	l.Commit("re-apply")

	// FP guard: two patch-id-identical candidates → no auto-bind; the
	// notes stay hidden here (pending-elsewhere via the live local ref).
	res := l.MustDM("r:fa.rb\nr:fb.rb\n")
	if facts := vdFacts(l.MustDM("", "dump").Stdout); len(facts) != 0 {
		t.Fatalf("twin candidates minted VDs: %v", facts)
	}
	_ = res
}

func TestM3FloorFN(t *testing.T) {
	bare := NewBareRemote(t)
	a := NewRepo(t)
	a.WriteFile("base.rb", "class Base\n  def a; end\nend\n")
	a.Commit("base")
	a.Git("remote", "add", "origin", bare)
	a.Git("push", "-q", "origin", "main")
	a.MustDM("", "init")
	a.Git("checkout", "-q", "-b", "one-liner")
	a.WriteFile("o.txt", "just one line\n")
	tip := a.Commit("one-line branch")
	a.MustDM("a:o.txt:c:one-liner note\n")
	a.MustDM("", "sync")
	// Squash it.
	a.Git("checkout", "-q", "main")
	a.Git("merge", "-q", "--squash", "one-liner")
	a.Commit("squash one-liner")

	// Below the floor: no bind (accepted FN); after the ref dies the
	// worklist carries the hint.
	a.MustDM("", "sync")
	if facts := vdFacts(a.MustDM("", "dump").Stdout); len(facts) != 0 {
		t.Fatalf("sub-floor squash minted VDs: %v", facts)
	}
	a.Git("branch", "-q", "-D", "one-liner")
	wl := a.MustDM("", "worklist").Stdout
	if !strings.Contains(wl, "line "+tip) || !strings.Contains(wl, "m3: below floor (1 < 3 changed lines)") {
		t.Fatalf("worklist misses the sub-floor hint:\n%s", wl)
	}
}

func TestM3DuplicateWorkAcceptedFP(t *testing.T) {
	l := newLandingRepo(t)
	// Byte-identical, independently-authored cumulative diff lands on
	// main as one commit (same three files, same content).
	l.Git("checkout", "-q", "main")
	l.WriteFile("fa.rb", "class Fa\n  def one; end\n  def two; end\n  def three; end\nend\n")
	l.WriteFile("fb.rb", "class Fb\n  def uno; end\n  def dos; end\n  def tres; end\nend\n")
	l.WriteFile("fc.rb", "class Fc\n  def x; end\n  def y; end\n  def z; end\nend\n")
	twin := l.Commit("independently authored duplicate")
	// The dead branch: its ref survives locally; the twin binds it
	// (accepted FP, pinned — the identical content did land).
	l.MustDM("", "sync")
	dump := l.MustDM("", "dump").Stdout
	if !hasVD(dump, l.F1, twin, "m3") || !hasVD(dump, l.F2, twin, "m3") {
		t.Fatalf("byte-identical duplicate must bind (accepted FP), got %v", vdFacts(dump))
	}
	res := l.MustDM("r:fa.rb\n")
	if !strings.Contains(res.Stdout, bodyN1) {
		t.Errorf("duplicate-work notes must land:\n%s", res.Stdout)
	}
}

// ---- m5 — override & determinism ----

func TestM5UnlandedOverride(t *testing.T) {
	l := newLandingRepo(t)
	l.Git("rebase", "-q", "main")
	l.MustDM("r:fa.rb\n") // mints m1 VDs
	if !strings.Contains(l.MustDM("r:fa.rb\n").Stdout, bodyN1) {
		t.Fatal("precondition: N1 visible after rebase")
	}

	// An unlanded record with larger rec-id (later invocation clock)
	// voids the binding; the note falls back to reachability
	// classification — F1 is on no ref: abandoned, hidden.
	l.MustDM(fmt.Sprintf("vd:%s:unlanded\n", l.F1))
	if strings.Contains(l.MustDM("r:fa.rb\n").Stdout, bodyN1) {
		t.Fatal("voided binding still surfaces N1")
	}
	// Inference never re-mints over a standing verdict: the reflog still
	// holds the rebase, yet repeated reads leave the void in force.
	l.MustDM("r:fa.rb\nr:fb.rb\n")
	facts := vdFacts(l.MustDM("", "dump").Stdout)
	m1F1 := 0
	for _, f := range facts {
		if strings.HasPrefix(f, l.F1+"→") && strings.HasSuffix(f, ":m1") {
			m1F1++
		}
	}
	if m1F1 != 1 {
		t.Fatalf("m1 re-minted over a manual unlanded (%d F1 m1-bindings): %v", m1F1, facts)
	}
	wl := l.MustDM("", "worklist").Stdout
	if !strings.Contains(wl, "line ") {
		t.Errorf("voided origin should fall to the abandoned worklist:\n%s", wl)
	}
}

func TestVdLWWDeterminism(t *testing.T) {
	l := newLandingRepo(t)
	l.pushFeature()
	s := l.squashFeature("real landing S")
	other := l.MainTip // a plausible-but-wrong landing subject

	b := CloneRepo(t, l.bare, 2)
	b.MustDM("", "init")

	// Conflicting manual verdicts: A first (wrong), B later (right — with
	// the larger pinned clock). Both replicas must fold the same winner
	// in both sync orders.
	l.Git("branch", "-q", "-D", "feature")
	l.MustDM(fmt.Sprintf("vd:%s:landed:%s\n", l.F1, other))
	bClock := baseClock + 900_000 // decisively later than any A invocation
	b.DMEnv([]string{fmt.Sprintf("DM_CLOCK=%d", bClock)}, fmt.Sprintf("vd:%s:landed:%s\n", l.F1, s))
	l.MustDM("", "sync")
	b.MustDM("", "sync")
	l.MustDM("", "sync")

	dumpA, dumpB := l.MustDM("", "dump").Stdout, b.MustDM("", "dump").Stdout
	if dumpA != dumpB {
		t.Fatalf("replicas diverged:\nA:\n%s\nB:\n%s", dumpA, dumpB)
	}
	// The winner is B's binding: F1 → S, so N1 surfaces on main on both.
	for name, r := range map[string]*Repo{"A": l.Repo, "B": b} {
		if res := r.MustDM("r:fa.rb\n"); !strings.Contains(res.Stdout, bodyN1) {
			t.Errorf("%s does not fold the LWW winner (N1 hidden):\n%s", name, res.Stdout)
		}
	}
}

// ---- qualified-clone guard ----

func TestShallowNoMint(t *testing.T) {
	l := newLandingRepo(t)
	l.pushFeature()
	l.squashFeature("S")
	l.MustDM("", "sync") // full clone mints and shares m3 VDs? no — wipe below

	// A shallow clone: unresolved origins read unknown (hidden, not
	// worklisted), zero VDs minted — even where a full clone would judge.
	shallow := CloneRepoArgs(t, l.bare, 3, "--depth", "1")
	shallow.MustDM("", "init")
	shallow.MustDM("r:fa.rb\n")
	// The store already carries the author's m3 VDs (consuming them is the
	// single-branch test's subject); assert the shallow clone itself
	// minted nothing: its pending stays empty.
	if names := shallow.PendingRecordNames(); len(names) != 0 {
		t.Fatalf("shallow clone minted records: %v", names)
	}
	if wl := shallow.MustDM("", "worklist").Stdout; strings.Contains(wl, "line ") {
		t.Errorf("shallow clone must not classify abandoned (unknown is hidden):\n%s", wl)
	}
}

func TestShallowUnknownVsFullAbandoned(t *testing.T) {
	// The same state reads unknown (hidden) on a shallow clone and
	// abandoned (worklisted) on a full clone — pins the poisoning fix.
	bare := NewBareRemote(t)
	a := NewRepo(t)
	a.WriteFile("base.rb", "class Base\nend\n")
	a.Commit("base")
	a.Git("remote", "add", "origin", bare)
	a.Git("push", "-q", "origin", "main")
	a.MustDM("", "init")
	a.Git("checkout", "-q", "-b", "doomed")
	a.WriteFile("d.txt", "doomed content\nmore\nlines\n")
	a.Commit("doomed work")
	a.MustDM("a:d.txt:c:doomed note\n")
	a.MustDM("", "sync")
	a.Git("checkout", "-q", "main")
	a.Git("branch", "-q", "-D", "doomed") // never pushed: objects exist nowhere else

	full := CloneRepo(t, bare, 2)
	full.MustDM("", "init")
	if wl := full.MustDM("", "worklist").Stdout; !strings.Contains(wl, "line ") {
		t.Errorf("full clone should worklist the abandoned line:\n%s", wl)
	}
	shallow := CloneRepoArgs(t, bare, 3, "--depth", "1")
	shallow.MustDM("", "init")
	if wl := shallow.MustDM("", "worklist").Stdout; strings.Contains(wl, "line ") {
		t.Errorf("shallow clone must read unknown, not abandoned:\n%s", wl)
	}
	if names := shallow.PendingRecordNames(); len(names) != 0 {
		t.Fatalf("shallow clone minted: %v", names)
	}
}

func TestSingleBranchNoMintConsumesVDs(t *testing.T) {
	l := newLandingRepo(t)
	l.pushFeature()
	s := l.squashFeature("S")
	l.MustDM("", "sync") // author mints + shares the m3 VDs

	sb := CloneRepoArgs(t, l.bare, 4, "--single-branch", "--branch", "main")
	sb.MustDM("", "init")
	// Existing landed VDs are consumed normally…
	res := sb.MustDM("r:fa.rb\nr:fb.rb\n")
	if !strings.Contains(res.Stdout, bodyN1) || !strings.Contains(res.Stdout, bodyN2) {
		t.Errorf("single-branch clone must consume landed VDs (chain to %s):\n%s", s, res.Stdout)
	}
	// …but the clone never judges: nothing minted.
	if names := sb.PendingRecordNames(); len(names) != 0 {
		t.Fatalf("single-branch clone minted: %v", names)
	}
}

// ---- derived abandoned & the unreachability cache ----

func TestAbandonedDerivedNotStoredAndResurrect(t *testing.T) {
	l := newLandingRepo(t)
	tip := l.Git("rev-parse", "feature")
	l.Git("checkout", "-q", "main")
	l.Git("branch", "-q", "-D", "feature")

	// Worklisted as abandoned; the dump holds no record of it (derived,
	// never stored).
	wl := l.MustDM("", "worklist").Stdout
	if !strings.Contains(wl, "line "+l.F2) {
		t.Fatalf("abandoned line missing from worklist:\n%s", wl)
	}
	dump := l.MustDM("", "dump").Stdout
	if strings.Contains(dump, "abandoned") || len(vdFacts(dump)) != 0 {
		t.Fatalf("abandoned state leaked into the store:\n%s", dump)
	}

	// Recreating the ref at the old tip flips the state back to
	// pending-elsewhere on the next read — no repair verb involved.
	l.Git("branch", "-q", "feature", tip)
	if wl := l.MustDM("", "worklist").Stdout; strings.Contains(wl, "line ") {
		t.Errorf("resurrected ref must clear the abandoned group:\n%s", wl)
	}
	// Still hidden on main (pending-elsewhere), of course.
	if res := l.MustDM("r:fa.rb\n"); strings.Contains(res.Stdout, bodyN1) {
		t.Errorf("pending-elsewhere note surfaced on main:\n%s", res.Stdout)
	}
}

func TestCacheFFDeltaKeepsAndMergeResurrects(t *testing.T) {
	l := newLandingRepo(t)
	l.Git("checkout", "-q", "main")
	l.Git("branch", "-q", "-D", "feature")
	if wl := l.MustDM("", "worklist").Stdout; !strings.Contains(wl, "line ") {
		t.Fatal("precondition: abandoned group present")
	}

	// A linear ff of unrelated commits keeps the cached classification
	// (cheap delta path — asserted behaviorally: still abandoned).
	l.WriteFile("unrelated.rb", "class U\n  def a; end\nend\n")
	l.Commit("unrelated progress")
	if wl := l.MustDM("", "worklist").Stdout; !strings.Contains(wl, "line ") {
		t.Errorf("unrelated ff evicted the abandoned classification:\n%s", wl)
	}

	// An update whose delta brings the dead origin's line in flips it on
	// the next read.
	l.commits++
	l.Git("merge", "-q", "--no-ff", "--no-edit", l.F3)
	if wl := l.MustDM("", "worklist").Stdout; strings.Contains(wl, "line ") {
		t.Errorf("merged-in line still classified abandoned:\n%s", wl)
	}
	if res := l.MustDM("r:fa.rb\n"); !strings.Contains(res.Stdout, bodyN1) {
		t.Errorf("merged line's note should now surface:\n%s", res.Stdout)
	}
}

// ---- sync mechanics & chains ----

func TestEvidenceDestroyedWindow(t *testing.T) {
	bare := NewBareRemote(t)
	w := NewRepo(t)
	w.WriteFile("base.rb", "class Base\nend\n")
	w.Commit("base")
	w.Git("remote", "add", "origin", bare)
	w.Git("push", "-q", "origin", "main")
	w.MustDM("", "init")
	w.Git("checkout", "-q", "-b", "lost")
	w.WriteFile("l.txt", "lost work\nmore\nlines\n")
	origin := w.Commit("lost work")
	w.MustDM("a:l.txt:c:note on a lost line\n")
	w.MustDM("", "sync")
	// The writer clone is gone; the branch was never pushed, never
	// fetched elsewhere.

	tm := CloneRepo(t, bare, 2)
	tm.MustDM("", "init")
	wl := tm.MustDM("", "worklist").Stdout
	if !strings.Contains(wl, "line "+origin) || !strings.Contains(wl, "no evidence") {
		t.Fatalf("qualified clone should group the dead line with no evidence:\n%s", wl)
	}
	// m5 repairs with the full sha even though the objects are missing —
	// a single-origin binding.
	mainTip := tm.Git("rev-parse", "main")
	res := tm.MustDM(fmt.Sprintf("vd:%s:landed:%s\n", origin, mainTip))
	if !strings.Contains(res.Stdout, "1 origin bound") {
		t.Fatalf("single-origin disposition ack wrong:\n%s", res.Stdout)
	}
	if wl := tm.MustDM("", "worklist").Stdout; strings.Contains(wl, "line "+origin) {
		t.Errorf("disposed line still grouped:\n%s", wl)
	}
}

func TestVdChainTransitive(t *testing.T) {
	bare := NewBareRemote(t)
	a := NewRepo(t)
	a.WriteFile("base.rb", "class Base\nend\n")
	a.Commit("base")
	a.Git("remote", "add", "origin", bare)
	a.Git("push", "-q", "origin", "main")
	a.MustDM("", "init")

	// dev line off main; feature off dev.
	a.Git("checkout", "-q", "-b", "dev")
	a.WriteFile("d.rb", "class D\n  def dev; end\nend\n")
	a.Commit("dev work")
	a.Git("checkout", "-q", "-b", "feature")
	a.WriteFile("f.rb", "class F\n  def one; end\n  def two; end\n  def three; end\nend\n")
	f1 := a.Commit("feature work (F1)")
	a.MustDM("a:f.rb:c:chained note\n")

	// Feature lands on dev as squash S1: the read's mint pass attempts
	// tip-landing for the still-referenced branch (fold step 3) and binds
	// via m3; the ref dies afterwards.
	a.Git("checkout", "-q", "dev")
	a.Git("merge", "-q", "--squash", "feature")
	s1 := a.Commit("S1: feature squashed onto dev")
	a.MustDM("r:f.rb\n")
	a.Git("branch", "-q", "-D", "feature")
	dump := a.MustDM("", "dump").Stdout
	if !hasVD(dump, f1, s1, "m3") {
		t.Fatalf("want m3 VD %s→%s, got %v", f1, s1, vdFacts(dump))
	}

	// main advances; dev rebased onto main: S1→S1′ (m1 on this clone).
	a.Git("checkout", "-q", "main")
	a.WriteFile("m.rb", "class M\nend\n")
	a.Commit("main advances")
	a.Git("checkout", "-q", "dev")
	a.Git("rebase", "-q", "main")
	s1p := a.Git("rev-parse", "HEAD")

	// The fold resolves F1 → S1 → S1′: the note surfaces on rebased dev —
	// pins that segment enumeration includes landed-as commits of
	// existing VDs.
	res := a.MustDM("r:f.rb\n")
	if !strings.Contains(res.Stdout, "chained note") {
		t.Errorf("two-hop chain did not resolve:\n%s", res.Stdout)
	}
	dump = a.MustDM("", "dump").Stdout
	if !hasVD(dump, s1, s1p, "m1") {
		t.Fatalf("want m1 VD %s→%s for the chained landed-as, got %v", s1, s1p, vdFacts(dump))
	}
}

func TestCherryPickNoOverbroadAndBackportGap(t *testing.T) {
	l := newLandingRepo(t)
	// A release line for the backport half.
	l.Git("checkout", "-q", "main")
	l.Git("branch", "-q", "release-1.x", l.Git("rev-parse", "main~1"))

	// cherry-pick-no-overbroad: pick F2 onto main, delete feature.
	l.Git("cherry-pick", l.F2)
	l.Git("branch", "-q", "-D", "feature")
	l.MustDM("r:fa.rb\nr:fb.rb\n")
	if facts := vdFacts(l.MustDM("", "dump").Stdout); len(facts) != 0 {
		t.Fatalf("cherry-pick bound a line: %v", facts)
	}
	// No F-origin notes on main: fb.rb's content is present, but rule (b)
	// fails by design — the note stays off this line.
	if res := l.MustDM("r:fb.rb\n"); strings.Contains(res.Stdout, bodyN2) {
		t.Errorf("picked commit surfaced the whole line's note:\n%s", res.Stdout)
	}
	if wl := l.MustDM("", "worklist").Stdout; !strings.Contains(wl, "line ") {
		t.Errorf("dead F-origins should worklist as abandoned:\n%s", wl)
	}

	// backport-gap: a note on main, its commit picked to the release
	// line, is never visible there (accepted FN, pinned).
	l.MustDM("a:m.rb:c:note on main-side work\n")
	l.Git("checkout", "-q", "release-1.x")
	l.Git("cherry-pick", l.MainTip)
	if res := l.MustDM("r:m.rb\n"); strings.Contains(res.Stdout, "note on main-side work") {
		t.Errorf("backported note surfaced on the release line (rule (b) should fail):\n%s", res.Stdout)
	}
	// And not worklisted either: main still holds the origin
	// (pending-elsewhere, hidden).
	if wl := l.MustDM("", "worklist").Stdout; strings.Contains(wl, "line "+l.MainTip) {
		t.Errorf("pending-elsewhere origin wrongly worklisted:\n%s", wl)
	}
}
