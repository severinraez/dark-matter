package e2e

import (
	"strings"
	"testing"
)

// M5 exit criteria (plan.md): the §2 visibility rows that need no lineage
// machinery, the §9.1/§9.2 resolution layers and bands, the pinned-RP
// flow, and the worklist states — all end-to-end against real repos.

// tenLines is a body comfortably inside the similarity bands: one edited
// line keeps ≥80%, a full rewrite drops to ~0.
const tenLines = "l1\nl2\nl3\nl4\nl5\nl6\nl7\nl8\nl9\nl10\n"

// TestPureRenameFollows replays §2.1 row 11 (no edits): the anchored blob
// reappears at the new path — layer 2, fresh, no flag.
func TestPureRenameFollows(t *testing.T) {
	r := NewRepo(t)
	r.WriteFile("api/handler.rb", tenLines)
	r.Commit("add handler")
	r.MustDM("", "init")
	handle := createdHandle(t, r.MustDM("a:api/handler.rb:c:About handler\n").Stdout)

	r.Git("mv", "api/handler.rb", "api/renamed.rb")
	r.Commit("rename")

	got := r.MustDM("r:api/renamed.rb\n").Stdout
	if !strings.Contains(got, "c #"+handle+" About handler\ncontext: 1 own") {
		t.Errorf("read at the new path:\n%q\nwant the note fresh (layer 2)", got)
	}
	if got := r.MustDM("r:api/handler.rb\n").Stdout; !strings.Contains(got, "context: 0 own") {
		t.Errorf("read at the old path:\n%q\nwant empty", got)
	}
}

// TestMoveWithEditInBand replays §9.2's flagship: mv + one-line edit stays
// inside the accept band — the note follows ⚠stale; `k` re-blesses it.
func TestMoveWithEditInBand(t *testing.T) {
	r := NewRepo(t)
	r.WriteFile("api/handler.rb", tenLines)
	r.Commit("add handler")
	r.MustDM("", "init")
	handle := createdHandle(t, r.MustDM("a:api/handler.rb:c:About handler\n").Stdout)

	r.Git("rm", "-q", "api/handler.rb")
	r.WriteFile("svc/handler.rb", strings.Replace(tenLines, "l5\n", "edited\n", 1))
	r.Commit("move with edit")

	got := r.MustDM("r:svc/handler.rb\n").Stdout
	if !strings.Contains(got, "c #"+handle+" About handler ⚠stale\n") {
		t.Errorf("read after move+edit:\n%q\nwant the note ⚠stale at the new path", got)
	}
	// k re-anchors at the resolved home — dm's guess is what plain k
	// blesses (§9.3) — and the flag clears.
	r.MustDM("k:#" + handle + "\n")
	got = r.MustDM("r:svc/handler.rb\n").Stdout
	if !strings.Contains(got, "c #"+handle+" About handler\ncontext: 1 own") {
		t.Errorf("read after k:\n%q\nwant fresh at svc/handler.rb", got)
	}
}

// TestUncommittedMoveFollows replays §2.1 row 11's uncommitted half: a
// staged rename (no commit) already carries the note — visibility is
// working-tree based.
func TestUncommittedMoveFollows(t *testing.T) {
	r := NewRepo(t)
	r.WriteFile("a.rb", tenLines)
	r.Commit("add a")
	r.MustDM("", "init")
	handle := createdHandle(t, r.MustDM("a:a.rb:c:note\n").Stdout)

	r.Git("mv", "a.rb", "b.rb")
	got := r.MustDM("r:b.rb\n").Stdout
	if !strings.Contains(got, "c #"+handle+" note\ncontext: 1 own") {
		t.Errorf("read at the staged-rename path:\n%q\nwant the note fresh", got)
	}
}

// TestRewriteBelowFloorOrphans: a full rewrite at the same path is
// indistinguishable from delete-and-create (§9.2) — the note orphans,
// leaves reads, appears on the worklist, and only repair verbs may touch
// it (§5.3/§9.6).
func TestRewriteBelowFloorOrphans(t *testing.T) {
	r := NewRepo(t)
	r.WriteFile("a.rb", tenLines)
	r.Commit("add a")
	r.MustDM("", "init")
	handle := createdHandle(t, r.MustDM("a:a.rb:c:note\n").Stdout)

	r.WriteFile("a.rb", "completely\nforeign\ncontent\n")
	r.Commit("rewrite")

	if got := r.MustDM("r:a.rb\n").Stdout; !strings.Contains(got, "context: 0 own") {
		t.Errorf("read after rewrite:\n%q\nwant the note gone from reads", got)
	}
	wl := r.MustDM("", "worklist").Stdout
	if !strings.Contains(wl, "#"+handle+" c a.rb orphaned\n") || !strings.Contains(wl, "1 to review\n") {
		t.Errorf("worklist:\n%q\nwant the orphaned line", wl)
	}
	// Non-repair verbs refuse a worklist-state entry…
	res := r.MustDM("r:#" + handle + "\nf:#" + handle + ":+\n")
	if strings.Count(res.Stdout, "✗ #"+handle+" on the worklist — repair with k/u/d/mv/rm\n") != 2 {
		t.Errorf("worklist-state addressing:\n%q\nwant both refused", res.Stdout)
	}
	// …while k with an explicit home repairs it.
	res = r.MustDM("k:#" + handle + ":a.rb\nr:a.rb\n")
	if !strings.Contains(res.Stdout, "✓ #"+handle+" re-anchored\n") ||
		!strings.Contains(res.Stdout, "c #"+handle+" note\n") {
		t.Errorf("repair via k:#h:path:\n%q\nwant re-anchored and visible", res.Stdout)
	}
	if got := r.MustDM("", "worklist").Stdout; !strings.Contains(got, "worklist clean\n") {
		t.Errorf("worklist after repair:\n%q\nwant clean", got)
	}
}

// TestDeletedFileOrphansAndTombstones replays §2.1 row 12: deletion
// orphans (never destroys); `d` retires it from the worklist.
func TestDeletedFileOrphansAndTombstones(t *testing.T) {
	r := NewRepo(t)
	r.WriteFile("gone.rb", tenLines)
	r.Commit("add")
	r.MustDM("", "init")
	handle := createdHandle(t, r.MustDM("a:gone.rb:c:note\n").Stdout)

	r.Git("rm", "-q", "gone.rb")
	r.Commit("delete")

	wl := r.MustDM("", "worklist").Stdout
	if !strings.Contains(wl, "#"+handle+" c gone.rb orphaned\n") {
		t.Errorf("worklist:\n%q\nwant the orphan", wl)
	}
	if got := r.MustDM("d:#" + handle + "\n").Stdout; !strings.Contains(got, "✓ #"+handle+" tombstoned\n") {
		t.Errorf("tombstone from the worklist:\n%q", got)
	}
	if got := r.MustDM("", "worklist").Stdout; !strings.Contains(got, "worklist clean\n") {
		t.Errorf("worklist after d:\n%q\nwant clean", got)
	}
}

// TestPinnedLayerAfterMv replays the §11.4 macro rows: a below-threshold
// move mv'd then read resolves *pinned* at the new path, ⚠stale (§9.1
// layer 4); on a branch without the move the entry still resolves at the
// old path via content (the RP hasn't landed there); `k` retires the pin.
func TestPinnedLayerAfterMv(t *testing.T) {
	r := NewRepo(t)
	r.WriteFile("old.rb", tenLines)
	r.Commit("add old")
	r.MustDM("", "init")
	handle := createdHandle(t, r.MustDM("a:old.rb:c:note\n").Stdout)

	// The move happens on a branch, with a rewrite far below the band.
	r.Git("checkout", "-q", "-b", "mover")
	r.Git("mv", "old.rb", "new.rb")
	r.WriteFile("new.rb", "totally\nrewritten\n")
	r.Commit("heavy rewrite move")
	if got := r.MustDM("mv:old.rb:new.rb\n").Stdout; !strings.Contains(got, "✓ #"+handle+" moved\n") {
		t.Fatalf("mv:\n%q", got)
	}

	// Pinned: surfaces at the asserted path, stale — the agent said where
	// it lives; nobody has said it still matches.
	got := r.MustDM("r:new.rb\n").Stdout
	if !strings.Contains(got, "c #"+handle+" note ⚠stale\n") {
		t.Errorf("pinned read:\n%q\nwant ⚠stale at new.rb", got)
	}
	// rp-scoped: on main the move never happened — the RP hasn't landed,
	// layers 1/2 still find the old blob at the old path, fresh.
	r.Git("checkout", "-q", "main")
	got = r.MustDM("r:old.rb\n").Stdout
	if !strings.Contains(got, "c #"+handle+" note\ncontext: 1 own") {
		t.Errorf("read on main:\n%q\nwant the pre-move state, fresh", got)
	}
	// Back on the mover line, k re-anchors to the rewritten bytes and
	// clears the pin.
	r.Git("checkout", "-q", "mover")
	r.MustDM("k:#" + handle + "\n")
	got = r.MustDM("r:new.rb\n").Stdout
	if !strings.Contains(got, "c #"+handle+" note\ncontext: 1 own") {
		t.Errorf("read after k:\n%q\nwant fresh at new.rb", got)
	}
}

// TestTrueMergeLands replays §2.1 row 9's true-merge case: a branch
// origin becomes an ancestor of main at the merge — zero writes, the
// notes appear.
func TestTrueMergeLands(t *testing.T) {
	r := NewRepo(t)
	r.WriteFile("f.rb", tenLines)
	r.Commit("base")
	r.MustDM("", "init")

	r.Git("checkout", "-q", "-b", "feature")
	r.WriteFile("g.rb", "feature file\n")
	r.Commit("add g")
	handle := createdHandle(t, r.MustDM("a:g.rb:c:branch note\n").Stdout)

	r.Git("checkout", "-q", "main")
	if got := r.MustDM("r:g.rb\n").Stdout; !strings.Contains(got, "context: 0 own") {
		t.Fatalf("pre-merge read on main:\n%q\nwant nothing (rule (b))", got)
	}
	r.Git("merge", "-q", "--no-ff", "-m", "merge feature", "feature")
	got := r.MustDM("r:g.rb\n").Stdout
	if !strings.Contains(got, "c #"+handle+" branch note\ncontext: 1 own") {
		t.Errorf("post-merge read:\n%q\nwant the branch note landed", got)
	}
}

// TestDetachedHead replays §2.1 row 15: at an old commit, notes whose
// origin had landed as of that commit are visible; later notes are not —
// same rule, no special-casing.
func TestDetachedHead(t *testing.T) {
	r := NewRepo(t)
	r.WriteFile("early.rb", tenLines)
	old := r.Commit("early")
	r.MustDM("", "init")
	early := createdHandle(t, r.MustDM("a:early.rb:c:early note\n").Stdout)

	r.WriteFile("late.rb", "later\n")
	r.Commit("late")
	late := createdHandle(t, r.MustDM("a:late.rb:c:late note\na:early.rb:d:late note on early file\n").Stdout)

	r.Git("checkout", "-q", old)
	got := r.MustDM("r:early.rb\nr:late.rb\n").Stdout
	if !strings.Contains(got, "c #"+early+" early note\n") {
		t.Errorf("detached read:\n%q\nwant the early note visible", got)
	}
	if strings.Contains(got, late) || strings.Contains(got, "late note") {
		t.Errorf("detached read:\n%q\nwant no later-origin notes (strict lineage)", got)
	}
}

// TestDisputedFlagSurfaces pins the ⚠disputed thread through M5's
// surfaces: an `f !` flags previews, drills, and the worklist; `u`
// (outranking the complaint, §7.3) clears all three.
func TestDisputedFlagSurfaces(t *testing.T) {
	r := NewRepo(t)
	r.WriteFile("a.rb", tenLines)
	r.Commit("add")
	r.MustDM("", "init")
	handle := createdHandle(t, r.MustDM("a:a.rb:c:claim\n").Stdout)
	r.MustDM("f:#" + handle + ":!:wrong since v3\n")

	if got := r.MustDM("r:a.rb\n").Stdout; !strings.Contains(got, "c #"+handle+" claim ⚠disputed\n") {
		t.Errorf("preview:\n%q\nwant ⚠disputed", got)
	}
	if got := r.MustDM("r:#" + handle + "\n").Stdout; !strings.Contains(got, "a.rb [c] #"+handle+" ⚠disputed\n") {
		t.Errorf("drill header:\n%q\nwant ⚠disputed", got)
	}
	if got := r.MustDM("", "worklist").Stdout; !strings.Contains(got, "#"+handle+" c a.rb ⚠disputed\n") {
		t.Errorf("worklist:\n%q\nwant the disputed item", got)
	}
	r.MustDM("u:#" + handle + ":correct claim\n")
	if got := r.MustDM("r:a.rb\n").Stdout; strings.Contains(got, "⚠disputed") {
		t.Errorf("preview after u:\n%q\nwant the dispute cleared", got)
	}
	if got := r.MustDM("", "worklist").Stdout; !strings.Contains(got, "worklist clean\n") {
		t.Errorf("worklist after u:\n%q\nwant clean", got)
	}
}

// TestAmbiguousCopiesWorklisted: two exact copies of the anchored content
// and none at the described path — affinity picks, the surface flags
// ⚠unconfirmed, and the worklist carries the candidates (§9.1 ambiguity
// policy: dm detects, agent decides).
func TestAmbiguousCopiesWorklisted(t *testing.T) {
	r := NewRepo(t)
	r.WriteFile("a.rb", tenLines)
	r.Commit("add")
	r.MustDM("", "init")
	handle := createdHandle(t, r.MustDM("a:a.rb:c:note\n").Stdout)

	r.Git("rm", "-q", "a.rb")
	r.WriteFile("lib/a.rb", tenLines)
	r.WriteFile("vendor/copy.rb", tenLines)
	r.Commit("duplicate elsewhere")

	got := r.MustDM("r:lib/a.rb\n").Stdout
	if !strings.Contains(got, "c #"+handle+" note ⚠unconfirmed\n") {
		t.Errorf("read at the affine copy:\n%q\nwant ⚠unconfirmed", got)
	}
	wl := r.MustDM("", "worklist").Stdout
	if !strings.Contains(wl, "#"+handle+" c a.rb ambiguous → lib/a.rb 100% · vendor/copy.rb 100%\n") {
		t.Errorf("worklist:\n%q\nwant the candidate list", wl)
	}
}
