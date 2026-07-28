package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// §2.2 folder-note rows and the §9.3 split/absorbed UX, end-to-end.

// seedFolder writes n distinctive multi-line files under dir/ so rename
// detection pairs them individually.
func seedFolder(r *Repo, dir string, n int) {
	for i := 0; i < n; i++ {
		r.WriteFile(fmt.Sprintf("%s/f%d.rb", dir, i),
			fmt.Sprintf("file %d\nalpha %d\nbeta %d\ngamma %d\ndelta %d\n", i, i, i, i, i))
	}
}

// moveMembers moves files [from..to) of api/ into dst/ in the working
// tree (fs-level move; the next Commit's `add -A` stages it — history
// stores no renames either way, §9.2).
func moveMembers(r *Repo, dst string, from, to int) {
	for i := from; i < to; i++ {
		src := fmt.Sprintf("api/f%d.rb", i)
		content, err := os.ReadFile(filepath.Join(r.Dir, filepath.FromSlash(src)))
		if err != nil {
			r.t.Fatal(err)
		}
		r.WriteFile(fmt.Sprintf("%s/f%d.rb", dst, i), string(content))
		r.Git("rm", "-q", src)
	}
}

// TestFolderNoteBasics replays §2.2 rows 1–2: a folder note is visible
// while the path exists and member churn never flags it; file reads under
// it surface the arch parent (§3.2, §5.1).
func TestFolderNoteBasics(t *testing.T) {
	r := NewRepo(t)
	seedFolder(r, "api", 3)
	r.Commit("seed")
	r.MustDM("", "init")
	handle := createdHandle(t, r.MustDM("a:api/:a:api owns transport only\n").Stdout)

	got := r.MustDM("r:api/\n").Stdout
	if !strings.Contains(got, "a #"+handle+" api owns transport only\ncontext: 1 own") {
		t.Errorf("folder read:\n%q\nwant the own note", got)
	}
	// A file read under it shows the parent line with the arch callout.
	got = r.MustDM("r:api/f0.rb\n").Stdout
	if !strings.Contains(got, "↑ 1 parent note (1 arch) #"+handle+" api/\n") ||
		!strings.Contains(got, "context: 0 own · 1 parent") {
		t.Errorf("file read under the folder:\n%q\nwant the parent line", got)
	}
	// Row 2: member churn — add, edit, delete — never flags a place-note.
	r.WriteFile("api/new.rb", "brand new\n")
	r.WriteFile("api/f0.rb", "rewritten\n")
	r.Git("rm", "-q", "api/f1.rb")
	r.Commit("churn")
	got = r.MustDM("r:api/\n").Stdout
	if !strings.Contains(got, "a #"+handle+" api owns transport only\ncontext: 1 own") {
		t.Errorf("folder read after churn:\n%q\nwant still fresh", got)
	}
}

// TestFolderPureMoveFollows replays §2.2 row 4: git mv api/ svc/ with no
// member edits — the tree SHA reappears, the note follows fresh.
func TestFolderPureMoveFollows(t *testing.T) {
	r := NewRepo(t)
	seedFolder(r, "api", 3)
	r.Commit("seed")
	r.MustDM("", "init")
	handle := createdHandle(t, r.MustDM("a:api/:a:place note\n").Stdout)

	r.Git("mv", "api", "svc")
	r.Commit("move")

	got := r.MustDM("r:svc/\n").Stdout
	if !strings.Contains(got, "a #"+handle+" place note\ncontext: 1 own") {
		t.Errorf("folder read at svc/:\n%q\nwant the note fresh (pure move)", got)
	}
	got = r.MustDM("r:svc/f1.rb\n").Stdout
	if !strings.Contains(got, "↑ 1 parent note (1 arch) #"+handle+" svc/\n") {
		t.Errorf("file read under svc/:\n%q\nwant the followed parent", got)
	}
}

// TestFolderChildCarriedByParentMove replays §2.2 row 12: a note on
// api/v1/ follows a `git mv api/ svc/` to svc/v1/ — the parent's move
// carries the child, each note resolving independently.
func TestFolderChildCarriedByParentMove(t *testing.T) {
	r := NewRepo(t)
	seedFolder(r, "api/v1", 2)
	r.Commit("seed")
	r.MustDM("", "init")
	handle := createdHandle(t, r.MustDM("a:api/v1/:d:v1 is frozen\n").Stdout)

	r.Git("mv", "api", "svc")
	r.Commit("move parent")

	got := r.MustDM("r:svc/v1/\n").Stdout
	if !strings.Contains(got, "d #"+handle+" v1 is frozen\ncontext: 1 own") {
		t.Errorf("read at svc/v1/:\n%q\nwant the carried note, fresh", got)
	}
}

// TestFolderMoveWithChurnUnconfirmed replays §2.2 rows 5–6: most members
// move, the rest deleted — follows flagged ⚠unconfirmed; plain `k`
// blesses the follow (§9.3).
func TestFolderMoveWithChurnUnconfirmed(t *testing.T) {
	r := NewRepo(t)
	seedFolder(r, "api", 5)
	r.Commit("seed")
	r.MustDM("", "init")
	handle := createdHandle(t, r.MustDM("a:api/:a:place note\n").Stdout)

	moveMembers(r, "svc", 0, 4)
	r.Git("rm", "-q", "api/f4.rb")
	r.Commit("move most, drop one")

	got := r.MustDM("r:svc/\n").Stdout
	if !strings.Contains(got, "a #"+handle+" place note ⚠unconfirmed\n") {
		t.Errorf("folder read:\n%q\nwant ⚠unconfirmed follow at svc/", got)
	}
	// The drill shows the vote instead of re-deriving it (§9.3).
	got = r.MustDM("r:#" + handle + "\n").Stdout
	if !strings.Contains(got, "svc/ [a] #"+handle+" ⚠unconfirmed\n") ||
		!strings.Contains(got, "→ svc/ 4/4 · cov 80%\n") {
		t.Errorf("drill:\n%q\nwant the vote line", got)
	}
	// Plain k blesses dm's guess: RA re-keys the path anchor to svc/.
	r.MustDM("k:#" + handle + "\n")
	got = r.MustDM("r:svc/\n").Stdout
	if !strings.Contains(got, "a #"+handle+" place note\ncontext: 1 own") {
		t.Errorf("read after k:\n%q\nwant fresh at svc/", got)
	}
}

// TestFolderSplitUX replays §2.2 row 8 and the §9.3 split decision: the
// note surfaces ⚠split at every candidate, evidence rides worklist and
// drill, and `k:#h:path` re-homes the entry.
func TestFolderSplitUX(t *testing.T) {
	r := NewRepo(t)
	seedFolder(r, "api", 10)
	r.Commit("seed")
	r.MustDM("", "init")
	handle := createdHandle(t, r.MustDM("a:api/:a:api governs transport\n").Stdout)

	moveMembers(r, "api-core", 0, 6)
	moveMembers(r, "api-http", 6, 10)
	r.Commit("split")

	// Surfaces at each candidate, flagged and carrying the split.
	for _, member := range []string{"api-core/f0.rb", "api-http/f7.rb"} {
		got := r.MustDM("r:" + member + "\n").Stdout
		if !strings.Contains(got, "#"+handle) || !strings.Contains(got, "⚠split") {
			t.Errorf("read under a candidate (%s):\n%q\nwant the ⚠split parent", member, got)
		}
	}
	// Worklist and drill carry the vote: majority · margin · coverage.
	voteLine := "→ api-core/ 6/10 · api-http/ 4/10 · cov 100%"
	wl := r.MustDM("", "worklist").Stdout
	if !strings.Contains(wl, "#"+handle+" a api/ ⚠split "+voteLine+"\n") {
		t.Errorf("worklist:\n%q\nwant the split line with the vote", wl)
	}
	drill := r.MustDM("r:#" + handle + "\n").Stdout
	if !strings.Contains(drill, voteLine+"\n") {
		t.Errorf("drill:\n%q\nwant the vote line", drill)
	}
	// Plain k cannot pick a home; k:#h:path can.
	res := r.MustDM("k:#" + handle + "\n")
	if !strings.Contains(res.Stdout, "✗ #"+handle+" ambiguous follow — k:#"+handle+":path picks the home: api-core/ · api-http/\n") {
		t.Errorf("plain k on a split:\n%q\nwant the candidate hint", res.Stdout)
	}
	r.MustDM("k:#" + handle + ":api-core/\n")
	if got := r.MustDM("r:api-core/\n").Stdout; !strings.Contains(got, "a #"+handle+" api governs transport\ncontext: 1 own") {
		t.Errorf("read after re-home:\n%q\nwant fresh at api-core/", got)
	}
	if got := r.MustDM("r:api-http/f7.rb\n").Stdout; strings.Contains(got, handle) {
		t.Errorf("read under the losing candidate:\n%q\nwant the note gone there", got)
	}
	if got := r.MustDM("", "worklist").Stdout; !strings.Contains(got, "worklist clean\n") {
		t.Errorf("worklist after re-home:\n%q\nwant clean", got)
	}
}

// TestFolderAbsorbedUnconfirmed replays §2.2 row 9: members absorbed into
// a pre-existing lib/ full of foreign content follow flagged regardless of
// vote strength, with the foreign fraction on the drill.
func TestFolderAbsorbedUnconfirmed(t *testing.T) {
	r := NewRepo(t)
	seedFolder(r, "api", 2)
	for i := 0; i < 8; i++ {
		r.WriteFile(fmt.Sprintf("lib/foreign%d.rb", i), fmt.Sprintf("foreign %d\ncontent %d\n", i, i))
	}
	r.Commit("seed")
	r.MustDM("", "init")
	handle := createdHandle(t, r.MustDM("a:api/:a:api note\n").Stdout)

	moveMembers(r, "lib", 0, 2)
	r.Commit("absorb")

	got := r.MustDM("r:lib/\n").Stdout
	if !strings.Contains(got, "a #"+handle+" api note ⚠unconfirmed\n") {
		t.Errorf("read at lib/:\n%q\nwant the absorbed follow flagged", got)
	}
	drill := r.MustDM("r:#" + handle + "\n").Stdout
	if !strings.Contains(drill, "→ lib/ 2/2 · cov 100% · target 80% foreign\n") {
		t.Errorf("drill:\n%q\nwant the foreign-fraction evidence", drill)
	}
}

// TestFolderExactPathWins replays §2.2 row 13: the folder moved *and* a
// new unrelated api/ occupies the old path — exact path wins, fresh, the
// deliberate cost of path-as-key.
func TestFolderExactPathWins(t *testing.T) {
	r := NewRepo(t)
	seedFolder(r, "api", 3)
	r.Commit("seed")
	r.MustDM("", "init")
	handle := createdHandle(t, r.MustDM("a:api/:a:place note\n").Stdout)

	r.Git("mv", "api", "svc")
	r.WriteFile("api/occupant.rb", "unrelated\n")
	r.Commit("move and reoccupy")

	got := r.MustDM("r:api/\n").Stdout
	if !strings.Contains(got, "a #"+handle+" place note\ncontext: 1 own") {
		t.Errorf("read at api/:\n%q\nwant the note fresh on the occupant", got)
	}
	if got := r.MustDM("r:svc/\n").Stdout; strings.Contains(got, handle) {
		t.Errorf("read at svc/:\n%q\nwant nothing (exact path won)", got)
	}
}

// TestFolderDeleteOrphansAndResurrects replays §2.2 rows 11 and 14:
// deletion orphans (worklist, never destroyed); a much-later re-created
// api/ resurrects the note fresh — the same consequence of path keying.
func TestFolderDeleteOrphansAndResurrects(t *testing.T) {
	r := NewRepo(t)
	seedFolder(r, "api", 2)
	r.Commit("seed")
	r.MustDM("", "init")
	handle := createdHandle(t, r.MustDM("a:api/:a:place note\n").Stdout)

	r.Git("rm", "-q", "-r", "api")
	r.Commit("delete folder")

	if got := r.MustDM("", "worklist").Stdout; !strings.Contains(got, "#"+handle+" a api/ orphaned\n") {
		t.Errorf("worklist:\n%q\nwant the orphaned folder note", got)
	}
	r.WriteFile("api/reborn.rb", "new life\n")
	r.Commit("recreate path")
	got := r.MustDM("r:api/\n").Stdout
	if !strings.Contains(got, "a #"+handle+" place note\ncontext: 1 own") {
		t.Errorf("read after resurrection:\n%q\nwant the note back, fresh", got)
	}
}

// TestRootFolderNote: a truly global note homed at the repo root (§3.4)
// parents every read.
func TestRootFolderNote(t *testing.T) {
	r := NewRepo(t)
	r.WriteFile("deep/nested/file.rb", "content\n")
	r.Commit("seed")
	r.MustDM("", "init")
	handle := createdHandle(t, r.MustDM("a:./:a:monorepo layout: services under deep/\n").Stdout)

	got := r.MustDM("r:deep/nested/file.rb\n").Stdout
	if !strings.Contains(got, "↑ 1 parent note (1 arch) #"+handle+" ./\n") {
		t.Errorf("nested read:\n%q\nwant the root parent", got)
	}
}

// TestFolderUncommittedRename replays §2.2 row 7: a folder renamed in
// the working tree (staged, no commit) already carries its note at the
// new path — as rows 4–6, no commit required first.
func TestFolderUncommittedRename(t *testing.T) {
	r := NewRepo(t)
	seedFolder(r, "api", 3)
	r.Commit("seed")
	r.MustDM("", "init")
	handle := createdHandle(t, r.MustDM("a:api/:a:place note\n").Stdout)

	r.Git("mv", "api", "svc") // staged rename, no commit

	got := r.MustDM("r:svc/\n").Stdout
	if !strings.Contains(got, "a #"+handle+" place note\ncontext: 1 own") {
		t.Errorf("read at the staged path:\n%q\nwant the note, fresh", got)
	}
	if got := r.MustDM("r:api/\n").Stdout; strings.Contains(got, "place note") {
		t.Errorf("read at the old path:\n%q\nwant nothing", got)
	}
}

// TestFolderScatterWorklistOnly replays §2.2 row 10: members scattered
// across several folders with none clearly the successor — ambiguity,
// surfaced on the worklist only (§9.3: scatter has nowhere to surface).
func TestFolderScatterWorklistOnly(t *testing.T) {
	r := NewRepo(t)
	seedFolder(r, "api", 5)
	r.Commit("seed")
	r.MustDM("", "init")
	handle := createdHandle(t, r.MustDM("a:api/:a:place note\n").Stdout)

	// Five destinations at 20% each — every share below the §9.3 split
	// candidate floor (25%), so nothing is even a candidate.
	for i := 0; i < 5; i++ {
		moveMembers(r, string(rune('v'+i)), i, i+1)
	}
	r.Commit("scatter")

	// No candidate carries the note — scatter never surfaces on reads.
	for _, p := range []string{"v/f0.rb", "w/f1.rb", "x/f2.rb"} {
		if got := r.MustDM("r:" + p + "\n").Stdout; strings.Contains(got, handle) {
			t.Errorf("read under %s:\n%q\nwant no scattered parent", p, got)
		}
	}
	// The worklist carries it, and k with an explicit home repairs it.
	wl := r.MustDM("", "worklist").Stdout
	if !strings.Contains(wl, "#"+handle) || !strings.Contains(wl, "scattered") {
		t.Fatalf("worklist:\n%q\nwant the scattered entry", wl)
	}
	r.MustDM("k:#" + handle + ":v/\n")
	if got := r.MustDM("r:v/\n").Stdout; !strings.Contains(got, "a #"+handle+" place note\ncontext: 1 own") {
		t.Errorf("read after re-home:\n%q\nwant fresh at v/", got)
	}
}
