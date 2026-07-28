package e2e

import (
	"strings"
	"testing"
)

// The §11.4 visibility group: the §2.1 file-note table replayed row by
// row — the table is the acceptance spec. Rows 1–12 are one narrative
// (the table reads as a story); rows 13–16 need their own repos.
// Delegations: row 15 → TestDetachedHead, row 16 → the per-record fold
// group (TestSupersedeScopedToLineage / TestKeepNoRetract). The §2.2
// folder table lives in folder_test.go.

const (
	visF = "f1\nf2\nf3\nf4\nf5\nf6\nf7\nf8\nf9\nf10\n"
	visG = "g1\ng2\ng3\ng4\ng5\ng6\ng7\ng8\ng9\ng10\n"
)

func TestVisibilityFileTableRows1To12(t *testing.T) {
	// Row 1 — repo created, commits exist, then dm initialized: ready,
	// no notes, full history usable from here on.
	r := NewRepo(t)
	r.WriteFile("f.rb", visF)
	r.WriteFile("g.rb", visG)
	r.Commit("pre-dm history")
	r.MustDM("", "init")
	if got := r.MustDM("r:f.rb\n").Stdout; !strings.Contains(got, "context: 0 own") {
		t.Fatalf("row 1 — fresh init should read empty:\n%q", got)
	}

	// Row 2 — note on F while on main: visible on main.
	fNote := createdHandle(t, r.MustDM("a:f.rb:c:F note\n").Stdout)
	if got := r.MustDM("r:f.rb\n").Stdout; !strings.Contains(got, "c #"+fNote+" F note\n") {
		t.Fatalf("row 2 — main read:\n%q", got)
	}

	// Row 3 — branch B; notes on G *and on unchanged F* from B: both
	// visible on B, neither on main (B-origin notes stay invisible
	// there even for files B didn't change).
	r.Git("checkout", "-q", "-b", "topic")
	r.WriteFile("g.rb", visG+"b side\n")
	r.Commit("B edits g")
	res := r.MustDM("a:g.rb:c:G note\na:f.rb:c:B note on unchanged F\n")
	hs := createdHandles(t, res.Stdout)
	gNote, bNote := hs[0], hs[1]
	got := r.MustDM("r:f.rb\nr:g.rb\n").Stdout
	if !strings.Contains(got, "F note") || !strings.Contains(got, "B note on unchanged F") ||
		!strings.Contains(got, "G note") {
		t.Fatalf("row 3 — B read:\n%q\nwant F, B-on-F, and G notes", got)
	}
	r.Git("checkout", "-q", "main")
	got = r.MustDM("r:f.rb\nr:g.rb\n").Stdout
	if !strings.Contains(got, "F note") || strings.Contains(got, "B note") ||
		strings.Contains(got, "G note") {
		t.Fatalf("row 3 — main read:\n%q\nwant the F note only", got)
	}

	// Row 4 — main progresses with a note on new file H: main reads
	// F+H; B reads F+G — H's content isn't in B's tree.
	r.WriteFile("h.rb", "h line\n")
	r.Commit("main adds h")
	r.MustDM("a:h.rb:c:H note\n")
	if got := r.MustDM("r:h.rb\n").Stdout; !strings.Contains(got, "H note") {
		t.Fatalf("row 4 — main read:\n%q", got)
	}
	r.Git("checkout", "-q", "topic")
	if got := r.MustDM("r:h.rb\n").Stdout; strings.Contains(got, "H note") {
		t.Fatalf("row 4 — B read:\n%q\nwant no H note (no h.rb in B's tree)", got)
	}

	// Row 5 — B rebased onto main (clean): F+G+H all visible on B;
	// main unchanged.
	r.Git("rebase", "-q", "main")
	got = r.MustDM("r:f.rb\nr:g.rb\nr:h.rb\n").Stdout
	for _, want := range []string{"F note", "G note", "H note", "B note on unchanged F"} {
		if !strings.Contains(got, want) {
			t.Fatalf("row 5 — rebased B read misses %q:\n%q", want, got)
		}
	}
	if strings.Contains(got, "⚠") {
		t.Fatalf("row 5 — clean rebase must not flag:\n%q", got)
	}
	r.Git("checkout", "-q", "main")
	if got := r.MustDM("r:g.rb\n").Stdout; strings.Contains(got, "G note") {
		t.Fatalf("row 5 — main read:\n%q\nwant main unchanged", got)
	}

	// Row 6 — another branch C: unmerged lines never see each other's
	// notes.
	r.Git("checkout", "-q", "-b", "side")
	r.WriteFile("c.rb", "c line\n")
	r.Commit("C adds c")
	cNote := createdHandle(t, r.MustDM("a:c.rb:c:C note\n").Stdout)
	if got := r.MustDM("r:g.rb\n").Stdout; strings.Contains(got, "G note") {
		t.Fatalf("row 6 — C read:\n%q\nwant no B-origin notes", got)
	}
	r.Git("checkout", "-q", "topic")
	got = r.MustDM("r:c.rb\n").Stdout
	if strings.Contains(got, "C note") || strings.Contains(got, cNote) {
		t.Fatalf("row 6 — B read:\n%q\nwant no C-origin notes", got)
	}

	// Row 7 — note added on B: visible immediately, no commit or sync
	// step first.
	res = r.MustDM("a:f.rb:c:immediate note\nr:f.rb\n")
	imNote := createdHandle(t, res.Stdout)
	if !strings.Contains(res.Stdout, "immediate note") ||
		!strings.Contains(res.Stdout, "c #"+imNote+" immediate note") {
		t.Fatalf("row 7 — same-batch read:\n%q", res.Stdout)
	}

	// Row 8 — B rebased onto main again, conflict resolution changes G:
	// the G note is flagged, not hidden; all other notes unaffected.
	r.Git("checkout", "-q", "main")
	r.WriteFile("g.rb", visG+"m side\n")
	r.Commit("main edits g")
	r.Git("checkout", "-q", "topic")
	if out, err := r.gitAllowFail("rebase", "main"); err == nil {
		t.Fatalf("row 8 — expected a conflict, rebase succeeded:\n%s", out)
	}
	r.WriteFile("g.rb", visG+"b side\nm side\n")
	r.Git("add", "-A")
	r.gitEnv([]string{"GIT_EDITOR=true"}, "rebase", "--continue")
	got = r.MustDM("r:g.rb\nr:f.rb\n").Stdout
	if !strings.Contains(got, "c #"+gNote+" G note ⚠stale\n") {
		t.Fatalf("row 8 — conflicted G must be flagged, not hidden:\n%q", got)
	}
	if !strings.Contains(got, "F note") || strings.Count(got, "⚠") != 1 {
		t.Fatalf("row 8 — other notes must be unaffected:\n%q", got)
	}

	// Row 9 — B merged into main: B's notes appear on main, G's still
	// carrying its flag until someone re-confirms it.
	r.Git("checkout", "-q", "main")
	r.Git("merge", "-q", "--no-ff", "-m", "merge topic", "topic")
	got = r.MustDM("r:g.rb\nr:f.rb\n").Stdout
	if !strings.Contains(got, "c #"+gNote+" G note ⚠stale\n") ||
		!strings.Contains(got, "B note on unchanged F") ||
		!strings.Contains(got, "immediate note") {
		t.Fatalf("row 9 — post-merge main read:\n%q\nwant B's notes, G still flagged", got)
	}

	// Row 10 — noted file edited in the working tree, uncommitted:
	// still visible, flagged stale; restoring the file clears it.
	r.WriteFile("f.rb", strings.Replace(visF, "f5\n", "edited\n", 1))
	if got := r.MustDM("r:f.rb\n").Stdout; !strings.Contains(got, "c #"+fNote+" F note ⚠stale\n") {
		t.Fatalf("row 10 — dirty read:\n%q\nwant ⚠stale", got)
	}
	r.Git("checkout", "-q", "--", "f.rb")
	if got := r.MustDM("r:f.rb\n").Stdout; !strings.Contains(got, "c #"+fNote+" F note\n") ||
		strings.Contains(got, "F note ⚠") {
		t.Fatalf("row 10 — restored read:\n%q\nwant fresh again", got)
	}

	// Row 11 — noted file renamed: notes follow to the new path, fresh
	// (no content change).
	r.Git("mv", "f.rb", "core.rb")
	r.Commit("rename f")
	got = r.MustDM("r:core.rb\n").Stdout
	if !strings.Contains(got, "F note") || !strings.Contains(got, "immediate note") ||
		strings.Contains(got, "⚠") {
		t.Fatalf("row 11 — read at the new path:\n%q\nwant all F notes, fresh", got)
	}

	// Row 12 — noted file deleted on this line: notes orphaned — out of
	// reads, on the worklist, never silently destroyed.
	r.Git("rm", "-q", "core.rb")
	r.commits++
	r.Git("commit", "-q", "-m", "delete f")
	if got := r.MustDM("r:core.rb\n").Stdout; strings.Contains(got, "F note") {
		t.Fatalf("row 12 — deleted-file read:\n%q\nwant no notes", got)
	}
	wl := r.MustDM("", "worklist").Stdout
	for _, h := range []string{fNote, bNote, imNote} {
		if !strings.Contains(wl, "#"+h) {
			t.Fatalf("row 12 — worklist misses #%s:\n%q", h, wl)
		}
	}
	if !strings.Contains(wl, "orphaned") {
		t.Fatalf("row 12 — worklist:\n%q\nwant orphaned entries", wl)
	}
	// The H note is untouched by all of it.
	if got := r.MustDM("r:h.rb\n").Stdout; !strings.Contains(got, "H note") {
		t.Fatalf("row 12 — h.rb read:\n%q", got)
	}
}

// TestVisibilityRow13AbandonedVsPending: a deleted unmerged branch's
// notes stop being visible anywhere and appear on the worklist as an
// abandoned line — distinguishable from notes merely pending on a branch
// that still exists, which are not worklist debt.
func TestVisibilityRow13AbandonedVsPending(t *testing.T) {
	r := NewRepo(t)
	r.WriteFile("f.rb", visF)
	r.Commit("base")
	r.MustDM("", "init")

	r.Git("checkout", "-q", "-b", "doomed")
	r.WriteFile("d.rb", "doomed work\n")
	r.Commit("doomed")
	dead := createdHandle(t, r.MustDM("a:f.rb:c:doomed note\n").Stdout)

	r.Git("checkout", "-q", "main")
	r.Git("checkout", "-q", "-b", "alive")
	r.WriteFile("a.rb", "alive work\n")
	r.Commit("alive")
	live := createdHandle(t, r.MustDM("a:f.rb:c:alive note\n").Stdout)

	r.Git("checkout", "-q", "main")
	r.Git("branch", "-q", "-D", "doomed")

	// Neither note reads on main (rule (b)) …
	got := r.MustDM("r:f.rb\n").Stdout
	if strings.Contains(got, "doomed note") || strings.Contains(got, "alive note") {
		t.Fatalf("main read:\n%q\nwant neither branch note", got)
	}
	// … but only the dead line is worklist debt, grouped with the no-ref
	// marker; the live branch's note is pending-elsewhere, not debt.
	wl := r.MustDM("", "worklist").Stdout
	if !strings.Contains(wl, "#"+dead) || !strings.Contains(wl, "no ref") {
		t.Fatalf("worklist:\n%q\nwant the abandoned line grouped", wl)
	}
	if strings.Contains(wl, "#"+live) {
		t.Fatalf("worklist:\n%q\nwant the pending-elsewhere note absent", wl)
	}
	// The live note still reads normally on its own line.
	r.Git("checkout", "-q", "alive")
	if got := r.MustDM("r:f.rb\n").Stdout; !strings.Contains(got, "alive note") {
		t.Fatalf("alive read:\n%q", got)
	}
}

// TestVisibilityRow14TeammateClone: visibility is a pure function of
// tree state + lineage — a teammate at the same state sees exactly the
// same notes with the same flags.
func TestVisibilityRow14TeammateClone(t *testing.T) {
	a, bare := newSharedRepo(t)
	a.WriteFile("f.rb", visF)
	a.Commit("add f")
	a.Git("push", "-q", "origin", "main")
	handle := createdHandle(t, a.MustDM("a:f.rb:c:shared note\n").Stdout)
	// Drift f so the note carries a flag — the flag must travel too.
	a.WriteFile("f.rb", strings.Replace(visF, "f5\n", "edited\n", 1))
	a.Commit("drift f")
	a.Git("push", "-q", "origin", "main")
	a.MustDM("", "sync")

	b := CloneRepo(t, bare, 2)
	b.MustDM("", "init")
	surfA := a.MustDM("r:f.rb\n").Stdout
	surfB := b.MustDM("r:f.rb\n").Stdout
	if surfA != surfB {
		t.Fatalf("replicas at the same state read differently:\nA:\n%q\nB:\n%q", surfA, surfB)
	}
	if !strings.Contains(surfB, "c #"+handle+" shared note ⚠stale\n") {
		t.Fatalf("teammate read:\n%q\nwant the note with its flag", surfB)
	}
}
