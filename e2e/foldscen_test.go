package e2e

import (
	"strings"
	"testing"
)

// The §11.4 per-record fold scenario group (§8.3): each record's origin
// scopes where it folds, so revisions, confirmations, and moves apply
// per line, not globally. The remaining two named scenarios ride the CRUD
// group (u-old-revision-on-branch → TestSupersedeScopedToLineage,
// rp-scoped → TestMoveScopedToItsLine).

// TestKeepNoRetract replays k-no-retract: a `k` on drifted main folds
// only where main's HEAD has landed — an unmerged branch keeps reading
// its pre-`k` state, fresh, until it merges main.
func TestKeepNoRetract(t *testing.T) {
	r := NewRepo(t)
	r.WriteFile("f.rb", tenLines)
	r.Commit("base")
	r.MustDM("", "init")
	handle := createdHandle(t, r.MustDM("a:f.rb:c:careful note\n").Stdout)

	// Branch off, then drift f on main (a small edit — high §9.2 band,
	// so the note goes ⚠stale, not mid-band ⚠unconfirmed) and re-bless
	// there: the RA's origin is a main commit the branch does not
	// contain.
	r.Git("checkout", "-q", "-b", "feature")
	r.Git("checkout", "-q", "main")
	r.WriteFile("f.rb", strings.Replace(tenLines, "l5\n", "edited\n", 1))
	r.Commit("main drifts f")
	if got := r.MustDM("r:f.rb\n").Stdout; !strings.Contains(got, "⚠stale") {
		t.Fatalf("main pre-k read should be stale:\n%q", got)
	}
	r.MustDM("k:#" + handle + "\n")
	if got := r.MustDM("r:f.rb\n").Stdout; strings.Contains(got, "⚠stale") {
		t.Errorf("main post-k read still stale:\n%q", got)
	}

	// The branch still holds the original content: it reads the pre-`k`
	// state — fresh via the original anchor, the new RA out of scope.
	r.Git("checkout", "-q", "feature")
	got := r.MustDM("r:f.rb\n").Stdout
	if !strings.Contains(got, "c #"+handle+" careful note\n") || strings.Contains(got, "⚠") {
		t.Errorf("branch read:\n%q\nwant the pre-k state, fresh and unflagged", got)
	}

	// Landing main on the branch brings the drifted content *and* the
	// RA together: the branch folds it and stays fresh.
	r.Git("merge", "-q", "--no-edit", "main")
	got = r.MustDM("r:f.rb\n").Stdout
	if !strings.Contains(got, "c #"+handle+" careful note\n") || strings.Contains(got, "⚠") {
		t.Errorf("branch read after merging main:\n%q\nwant the RA folded, fresh", got)
	}
}

// TestKeepOnAbandonedBranchContained replays k-on-abandoned-branch-contained:
// a `k` whose origin line is deleted unmerged folds nowhere, the dangling
// RA is inert but kept — no sweep rule targets it — and a re-pushed
// branch folds it again (§9.4 resurrection).
func TestKeepOnAbandonedBranchContained(t *testing.T) {
	r := NewRepo(t)
	r.WriteFile("f.rb", "original content\n")
	r.Commit("base")
	r.MustDM("", "init")
	handle := createdHandle(t, r.MustDM("a:f.rb:c:contained note\n").Stdout)

	// On a topic branch, drift f and re-bless the drifted content.
	r.Git("checkout", "-q", "-b", "fk")
	r.WriteFile("f.rb", "original content\nplus fk drift\n")
	tip := r.Commit("fk drifts f")
	r.MustDM("k:#" + handle + "\n")
	if got := r.MustDM("r:f.rb\n").Stdout; strings.Contains(got, "⚠stale") {
		t.Fatalf("fk post-k read should be fresh:\n%q", got)
	}

	// Delete fk unmerged: every other line folds the pre-`k` state.
	r.Git("checkout", "-q", "main")
	r.Git("branch", "-q", "-D", "fk")
	got := r.MustDM("r:f.rb\n").Stdout
	if !strings.Contains(got, "c #"+handle+" contained note\n") || strings.Contains(got, "⚠") {
		t.Errorf("main read after abandoning fk:\n%q\nwant pre-k state, fresh", got)
	}
	// The dangling RA is inert and kept.
	if dump := r.MustDM("", "dump").Stdout; !strings.Contains(dump, "\nRA ") {
		t.Errorf("dangling RA swept from the record set:\n%q", dump)
	}

	// A re-pushed fk folds the RA again — resurrection, no repair verb.
	r.Git("branch", "-q", "fk", tip)
	r.Git("checkout", "-q", "fk")
	got = r.MustDM("r:f.rb\n").Stdout
	if !strings.Contains(got, "c #"+handle+" contained note\n") || strings.Contains(got, "⚠") {
		t.Errorf("resurrected fk read:\n%q\nwant the RA folded again, fresh", got)
	}
}

// TestAbandonedRescue replays abandoned-rescue (§8.3 rescue fold): an
// entry with no landed record anywhere is worklisted, and a `k` minting
// an RA with a live origin makes it visible on that line with the
// pre-rescue body.
func TestAbandonedRescue(t *testing.T) {
	r := NewRepo(t)
	r.WriteFile("f.rb", "stable content\n")
	r.Commit("base")
	r.MustDM("", "init")

	// The note is written on a doomed branch: its origin never lands.
	r.Git("checkout", "-q", "-b", "doomed")
	r.WriteFile("scratch.rb", "throwaway\n")
	r.Commit("doomed work")
	handle := createdHandle(t, r.MustDM("a:f.rb:c:rescued knowledge\n").Stdout)

	r.Git("checkout", "-q", "main")
	r.Git("branch", "-q", "-D", "doomed")

	// Abandoned: absent from reads, on the worklist — never destroyed.
	if got := r.MustDM("r:f.rb\n").Stdout; strings.Contains(got, "rescued knowledge") {
		t.Fatalf("abandoned note still read:\n%q", got)
	}
	// The worklist groups the dead line: tip, no-ref marker, note count
	// with handles, and the one-vd repair hint (§9.6).
	wl := r.MustDM("", "worklist").Stdout
	if !strings.Contains(wl, "#"+handle) || !strings.Contains(wl, "no ref") ||
		!strings.Contains(wl, "1 note") {
		t.Fatalf("worklist misses the abandoned line group:\n%q", wl)
	}

	// Plain `k` refuses — an abandoned entry has no resolved home to
	// bless — and teaches the explicit form.
	res := r.MustDM("k:#" + handle + "\n")
	if !strings.Contains(res.Stdout, "✗ #"+handle+" abandoned — name the new home: k:#"+handle+":path\n") {
		t.Fatalf("plain k on abandoned:\n%q\nwant the teaching refusal", res.Stdout)
	}
	// `k:#h:path` rescues: an RA with a live origin; the fold carries
	// the pre-rescue body onto this line.
	r.MustDM("k:#" + handle + ":f.rb\n")
	got := r.MustDM("r:f.rb\n").Stdout
	if !strings.Contains(got, "c #"+handle+" rescued knowledge\n") {
		t.Errorf("post-rescue read:\n%q\nwant the pre-rescue body visible", got)
	}
	if wl := r.MustDM("", "worklist").Stdout; strings.Contains(wl, "#"+handle) {
		t.Errorf("rescued entry still worklisted:\n%q", wl)
	}
}

// TestConcurrentSupersedePerLine replays concurrent-su-per-line: `SU`s on
// two unmerged lines each fold on their own line; the first checkout
// containing both folds to the larger rec-id (LWW under the pinned
// clock, §8.3).
func TestConcurrentSupersedePerLine(t *testing.T) {
	r := NewRepo(t)
	r.WriteFile("f.rb", "shared content\n")
	r.Commit("base")
	r.MustDM("", "init")
	handle := createdHandle(t, r.MustDM("a:f.rb:c:first wording\n").Stdout)

	r.Git("checkout", "-q", "-b", "b1")
	r.WriteFile("g1.rb", "one\n")
	r.Commit("b1 work")
	r.MustDM("u:#" + handle + ":wording from b1\n")

	r.Git("checkout", "-q", "main")
	r.Git("checkout", "-q", "-b", "b2")
	r.WriteFile("g2.rb", "two\n")
	r.Commit("b2 work")
	r.MustDM("u:#" + handle + ":wording from b2\n") // later invocation → larger rec-id

	// Each line reads its own revision; main still reads rev 1.
	if got := r.MustDM("r:f.rb\n").Stdout; !strings.Contains(got, "wording from b2") {
		t.Errorf("b2 read:\n%q", got)
	}
	r.Git("checkout", "-q", "b1")
	if got := r.MustDM("r:f.rb\n").Stdout; !strings.Contains(got, "wording from b1") {
		t.Errorf("b1 read:\n%q", got)
	}
	r.Git("checkout", "-q", "main")
	if got := r.MustDM("r:f.rb\n").Stdout; !strings.Contains(got, "first wording") {
		t.Errorf("main read before landing:\n%q", got)
	}

	// The first checkout containing both lines folds deterministically
	// to the larger rec-id — b2's, minted later on the pinned clock.
	r.Git("merge", "-q", "--no-edit", "b1", "b2")
	got := r.MustDM("r:f.rb\n").Stdout
	if !strings.Contains(got, "c #"+handle+" wording from b2\n") ||
		strings.Contains(got, "wording from b1") {
		t.Errorf("merged read:\n%q\nwant the larger rec-id's revision only", got)
	}
}
