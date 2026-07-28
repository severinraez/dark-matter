package e2e

import (
	"regexp"
	"strings"
	"testing"
)

// The §11.4 guard group: the fixture build commands themselves work — a
// regression in `a` (or any seed-path verb) surfaces here as a focused
// failure, not as every fixture-based scenario mysteriously breaking
// (§11.2). Plus fixture-based scenario slices that lean on the seeded
// state: visibility branches, the m2-ready forced branch, and the
// qualified clones.

// TestFixtureGuardBase pins the seeded base repo: the disclosure surface
// as a golden block, the CRUD/stat records in the store, and the seeded
// worklist debt.
func TestFixtureGuardBase(t *testing.T) {
	fx := CopyFixture(t)
	if len(fx.Handles) != 19 {
		t.Fatalf("fixture created %d handles, want 19: %v", len(fx.Handles), fx.Handles)
	}

	// The §11.2 disclosure dimensions, golden: mixed subjects ranked c
	// before d, the LCA arch parent, both link directions.
	got := fx.Base.MustDM("r:api/handler.rb\n").Stdout
	want := "▸r:api/handler.rb\n" +
		"c #" + fx.H(1) + " Validates tenant header before dispatch\n" +
		"d #" + fx.H(2) + " bin/test runs handler specs\n" +
		"↑ 1 parent note (1 arch) #" + fx.H(3) + " api/\n" +
		"→ #" + fx.H(3) + " governs the handler · #" + fx.H(4) + " tenant model background\n" +
		"context: 2 own · 1 parent · 2 links · ~3 hidden\n" +
		"◾1 ok\n"
	if got != want {
		t.Errorf("seeded surface:\n%q\nwant:\n%q", got, want)
	}

	// Store records: the superseded entry reads rev 2, the tombstoned one
	// is gone, verdicts carry their matcher ids, and the stat rows span
	// both replicas (G-Counter rows merge by replica, §7.2).
	dump := fx.Base.MustDM("", "dump").Stdout
	for _, wantPiece := range []string{
		"\nSU ", " lib/util.rb util now delegates retries to lib/retry\n",
		"\nTB ",
		" m1\n", " m3\n",
		"stat FIXREPL1 ", "stat FIXREPL2 ",
	} {
		if !strings.Contains(dump, wantPiece) {
			t.Errorf("dump misses %q:\n%s", wantPiece, dump)
		}
	}
	if got := fx.Base.MustDM("r:lib/util.rb\n").Stdout; !strings.Contains(got, "util now delegates") ||
		strings.Contains(got, "scaffolding") {
		t.Errorf("util read:\n%q\nwant rev 2 only, tombstone absorbed", got)
	}
	// Two m3 VDs (both squash segment commits), one m1 (the clean rebase).
	if m3s := regexp.MustCompile(` landed \S+ m3\n`).FindAllString(dump, -1); len(m3s) != 2 {
		t.Errorf("want 2 m3 verdicts, got %d:\n%s", len(m3s), dump)
	}

	// Seeded worklist debt: disputed, below-band orphan, split with its
	// vote, and the abandoned line grouped for one-vd repair.
	wl := fx.Base.MustDM("", "worklist", "--all").Stdout
	for _, wantLine := range []string{
		"#" + fx.H(4) + " c db/schema.rb ⚠disputed\n",
		"#" + fx.H(11) + " c band/rewrite.rb orphaned\n",
		"#" + fx.H(12) + " a split/ ⚠split → split-core/ 4/6 · split-http/ 2/6 · cov 100%\n",
		"1 note (#" + fx.H(18) + ")",
		"no ref",
		"4 to review\n",
	} {
		if !strings.Contains(wl, wantLine) {
			t.Errorf("worklist misses %q:\n%q", wantLine, wl)
		}
	}
}

// TestFixtureVisibilityBranches walks the seeded §2 branch states: the
// unmerged and rebased branches keep their notes to themselves, the
// squash-landed branch's notes read on main via the stored m3 verdicts,
// and the resolution surface reads as seeded.
func TestFixtureVisibilityBranches(t *testing.T) {
	fx := CopyFixture(t)
	b := fx.Base

	// On main: squash-landed notes visible; branch-only notes invisible.
	got := b.MustDM("r:ts1.rb\nr:ts2.rb\nr:tr.rb\nr:api/admin.rb\nr:drift.rb\nr:ren/new_name.rb\nr:band/moved.rb\n").Stdout
	for _, want := range []string{
		"c #" + fx.H(16) + " squashed note one\n",
		"c #" + fx.H(17) + " squashed note two\n",
		"c #" + fx.H(13) + " drift subject ⚠stale\n",
		"c #" + fx.H(9) + " pure-rename subject\n",
		"c #" + fx.H(10) + " in-band move subject ⚠stale\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("main reads miss %q:\n%q", want, got)
		}
	}
	for _, absent := range []string{"rebased branch note", "unmerged branch note"} {
		if strings.Contains(got, absent) {
			t.Errorf("main reads leak %q:\n%q", absent, got)
		}
	}

	// On their own lines the branch notes read normally.
	b.Git("checkout", "-q", "topic-rebased")
	if got := b.MustDM("r:tr.rb\n").Stdout; !strings.Contains(got, "c #"+fx.H(15)+" rebased branch note\n") {
		t.Errorf("topic-rebased read:\n%q", got)
	}
	b.Git("checkout", "-q", "topic-unmerged")
	if got := b.MustDM("r:api/admin.rb\n").Stdout; !strings.Contains(got, "c #"+fx.H(14)+" unmerged branch note\n") {
		t.Errorf("topic-unmerged read:\n%q", got)
	}
}

// TestFixtureForcedBranchM2 continues the seeded m2-ready state: replica
// 2 force-push-rewrote topic-forced with plain git (no dm saw it), so the
// base's next sync observes the forced update and mints the m2 binding.
func TestFixtureForcedBranchM2(t *testing.T) {
	fx := CopyFixture(t)
	dump := fx.Base.MustDM("", "dump").Stdout
	if strings.Contains(dump, " m2\n") {
		t.Fatalf("fixture must ship m2-ready, not m2-minted:\n%s", dump)
	}
	fx.Base.MustDM("", "sync")
	dump = fx.Base.MustDM("", "dump").Stdout
	if !strings.Contains(dump, " m2\n") {
		t.Errorf("sync across the forced update should mint m2:\n%s", dump)
	}
}

// TestFixtureQualifiedClones pins the §9.4 qualified-clone guard on the
// seeded clones: unresolvable origins read as unknown — hidden, not
// worklisted, no verdicts minted — while landed verdicts are still
// consumed normally.
func TestFixtureQualifiedClones(t *testing.T) {
	fx := CopyFixture(t)

	// Depth-1 clone: no ancestry is provable at all, so every origin —
	// even a genuinely landed one — reads unknown: hidden, never
	// worklisted, nothing minted (the poisoning fix, §9.4).
	got := fx.Shallow.MustDM("r:api/handler.rb\nr:ts1.rb\n").Stdout
	if strings.Contains(got, "#") {
		t.Errorf("shallow clone read:\n%q\nwant everything unknown-hidden", got)
	}

	// Single-branch clone: main's full history is present, so landed
	// knowledge (including squash-landed via the stored m3 VDs) reads
	// normally; only the missing branches' origins are unknown.
	got = fx.Single.MustDM("r:api/handler.rb\nr:ts1.rb\n").Stdout
	if !strings.Contains(got, "Validates tenant header before dispatch") ||
		!strings.Contains(got, "squashed note one") {
		t.Errorf("single-branch clone read:\n%q\nwant landed notes visible", got)
	}

	for name, r := range map[string]*Repo{"shallow": fx.Shallow, "single": fx.Single} {
		// The abandoned line reads unknown here: hidden, not worklisted —
		// the same store state a full clone turns into grouped debt.
		wl := r.MustDM("", "worklist", "--all").Stdout
		if strings.Contains(wl, "no ref") || strings.Contains(wl, fx.H(18)) {
			t.Errorf("%s clone worklist:\n%q\nwant no abandoned-line debt (unknown, §9.4)", name, wl)
		}
		// And no verdicts were minted by the qualified clone's reads.
		if dump := r.MustDM("", "dump").Stdout; strings.Contains(dump, " unlanded\n") {
			t.Errorf("%s clone minted verdicts:\n%s", name, dump)
		}
	}
	// The full clone's worklist does carry the abandoned line — the guard
	// contrast (§11.4 shallow-no-mint).
	if wl := fx.Base.MustDM("", "worklist", "--all").Stdout; !strings.Contains(wl, "no ref") {
		t.Errorf("full clone worklist:\n%q\nwant the abandoned line", wl)
	}
}
