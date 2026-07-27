package e2e

import (
	"regexp"
	"strings"
	"testing"
)

// M3 exit criteria (plan.md): the §11.4 CRUD, two-phase, and macro
// scenario groups pass end-to-end; concurrent-supersede and
// tombstone-terminal folds are pinned as core/fold goldens.

// createdHandles extracts every minted handle from a batch's output, in
// block order.
func createdHandles(t *testing.T, stdout string) []string {
	t.Helper()
	var out []string
	for _, line := range strings.Split(stdout, "\n") {
		if m := createdRe.FindStringSubmatch(line); m != nil {
			out = append(out, m[1])
		}
	}
	if len(out) == 0 {
		t.Fatalf("no created ack in output:\n%s", stdout)
	}
	return out
}

// TestSupersedeLifecycle replays the CRUD core: create → supersede →
// read shows the new revision under the same handle → supersede again →
// tombstone ends it.
func TestSupersedeLifecycle(t *testing.T) {
	r := NewRepo(t)
	r.WriteFile("api/handler.rb", "class Handler\nend\n")
	r.Commit("add handler")
	r.MustDM("", "init")

	handle := createdHandle(t, r.MustDM("a:api/handler.rb:c:Validates tenant header\n").Stdout)

	// Supersede: same handle, rev 2, golden ack (§4.3).
	res := r.MustDM("u:#" + handle + ":Now partitioned per tenant\n")
	want := "▸u:#" + handle + ":Now partitioned per tenant\n" +
		"✓ #" + handle + " superseded (rev 2)\n" +
		"◾1 ok\n"
	if res.Stdout != want {
		t.Errorf("supersede output:\n%q\nwant:\n%q", res.Stdout, want)
	}

	// The read folds to the new body — handle stable across revisions.
	read := r.MustDM("r:api/handler.rb\n").Stdout
	if !strings.Contains(read, "c #"+handle+" Now partitioned per tenant\n") {
		t.Errorf("read after supersede:\n%q\nwant the rev-2 body under the same handle", read)
	}
	if strings.Contains(read, "Validates tenant header") {
		t.Errorf("read still shows the superseded body:\n%q", read)
	}

	// Another revision: rev 3. Both SU records survive in the log.
	res = r.MustDM("u:#" + handle + ":Third thoughts\n")
	if !strings.Contains(res.Stdout, "✓ #"+handle+" superseded (rev 3)\n") {
		t.Errorf("second supersede:\n%q\nwant rev 3", res.Stdout)
	}
	if got := strings.Count(r.MustDM("", "dump").Stdout, "SU "); got != 2 {
		t.Errorf("dump has %d SU records, want 2 (earlier revisions survive in the log)", got)
	}

	// Tombstone: terminal (§8.3). The read empties, every later write and
	// drill answers deleted.
	res = r.MustDM("d:#" + handle + "\n")
	if !strings.Contains(res.Stdout, "✓ #"+handle+" tombstoned\n") {
		t.Errorf("tombstone ack missing:\n%q", res.Stdout)
	}
	read = r.MustDM("r:api/handler.rb\n").Stdout
	if !strings.Contains(read, "context: 0 own") {
		t.Errorf("read after tombstone:\n%q\nwant empty surface", read)
	}
	res = r.MustDM("u:#" + handle + ":necromancy\nr:#" + handle + "\n")
	if strings.Count(res.Stdout, "✗ #"+handle+" deleted\n") != 2 {
		t.Errorf("writes/reads against a tombstone:\n%q\nwant ✗ deleted for both", res.Stdout)
	}
}

// TestReadYourWrites pins the pending overlay within one batch: create,
// supersede via $N, and read — all in a single invocation (§4.2,
// architecture.md §7).
func TestReadYourWrites(t *testing.T) {
	r := NewRepo(t)
	r.WriteFile("f.rb", "content\n")
	r.Commit("add f")
	r.MustDM("", "init")

	res := r.MustDM("a:f.rb:c:first draft\nu:$1:final wording\nr:f.rb\n")
	handle := createdHandle(t, res.Stdout)
	if !strings.Contains(res.Stdout, "✓ #"+handle+" superseded (rev 2)\n") {
		t.Errorf("same-batch $N supersede:\n%q", res.Stdout)
	}
	if !strings.Contains(res.Stdout, "c #"+handle+" final wording\n") {
		t.Errorf("same-batch read misses the superseded body:\n%q", res.Stdout)
	}
	if !strings.HasSuffix(res.Stdout, "◾3 ok\n") {
		t.Errorf("footer of:\n%q\nwant ◾3 ok", res.Stdout)
	}
}

// TestKeepReAnchors replays §9.5's treatment: an in-band drift (edit under
// the anchor) surfaces flagged ⚠stale (§9.1 layer 3); `k` re-blesses it at
// the current blob with no new revision.
func TestKeepReAnchors(t *testing.T) {
	r := NewRepo(t)
	body := "line one\nline two\nline three\nline four\nline five\nline six\n"
	r.WriteFile("g.rb", body)
	r.Commit("add g")
	r.MustDM("", "init")
	handle := createdHandle(t, r.MustDM("a:g.rb:c:About g\n").Stdout)

	// Drift within the §9.2 band: one line edited, the rest intact — the
	// note stays visible at its path, flagged stale.
	r.WriteFile("g.rb", strings.Replace(body, "line four\n", "line 4\n", 1))
	if got := r.MustDM("r:g.rb\n").Stdout; !strings.Contains(got, "c #"+handle+" About g ⚠stale\n") {
		t.Errorf("drifted read:\n%q\nwant the note flagged ⚠stale", got)
	}

	res := r.MustDM("k:#" + handle + "\n")
	if !strings.Contains(res.Stdout, "✓ #"+handle+" re-anchored\n") {
		t.Errorf("keep ack missing:\n%q", res.Stdout)
	}
	// Re-anchored to the dirty bytes: fresh again, and still rev 1 — RA
	// is not a revision (§7.1).
	if got := r.MustDM("r:g.rb\n").Stdout; !strings.Contains(got, "c #"+handle+" About g\ncontext: 1 own") {
		t.Errorf("read after k:\n%q\nwant the note back, unflagged", got)
	}
	res = r.MustDM("u:#" + handle + ":still rev two\n")
	if !strings.Contains(res.Stdout, "superseded (rev 2)") {
		t.Errorf("u after k:\n%q\nwant rev 2 (k minted no revision)", res.Stdout)
	}
	dump := r.MustDM("", "dump").Stdout
	if !strings.Contains(dump, "RA ") {
		t.Errorf("dump lacks the RA record:\n%s", dump)
	}
}

// TestFeedback pins `f` (§7.3): an FB log record with the reason, acked
// with the signal.
func TestFeedback(t *testing.T) {
	r := NewRepo(t)
	r.WriteFile("f.rb", "content\n")
	r.Commit("add f")
	r.MustDM("", "init")
	handle := createdHandle(t, r.MustDM("a:f.rb:c:note\n").Stdout)

	res := r.MustDM("f:#" + handle + ":!:repo layer was removed in v3\nf:#" + handle + ":+\n")
	if !strings.Contains(res.Stdout, "✓ #"+handle+" feedback !\n") ||
		!strings.Contains(res.Stdout, "✓ #"+handle+" feedback +\n") {
		t.Errorf("feedback acks:\n%q", res.Stdout)
	}
	dump := r.MustDM("", "dump").Stdout
	if !regexp.MustCompile(`FB [0-9A-HJKMNP-TV-Z]{26} [0-9A-HJKMNP-TV-Z]{26} ! repo layer was removed in v3\n`).MatchString(dump) {
		t.Errorf("dump lacks the reasoned FB record:\n%s", dump)
	}
}

// TestLinks pins `al`/`dl` (§6.4): create-then-link in one round trip via
// $N, link surfaces at both endpoints, unlink is LWW.
func TestLinks(t *testing.T) {
	r := NewRepo(t)
	r.WriteFile("api/a.rb", "a\n")
	r.WriteFile("api/b.rb", "b\n")
	r.Commit("add files")
	r.MustDM("", "init")

	res := r.MustDM("a:api/a.rb:c:note A\na:api/b.rb:a:note B\nal:$1:$2:depends on B\n")
	hs := createdHandles(t, res.Stdout)
	if len(hs) != 2 {
		t.Fatalf("want 2 creates, got %v", hs)
	}
	hA, hB := hs[0], hs[1]
	// The ack echoes the minted handles, so the agent learns them (§4.2).
	if !strings.Contains(res.Stdout, "✓ #"+hA+" → #"+hB+" linked\n") {
		t.Errorf("link ack:\n%q", res.Stdout)
	}

	// Drill both endpoints, next invocation: links one level + relations.
	res = r.MustDM("r:#" + hA + "\nr:#" + hB + "\n")
	wantA := "api/a.rb [c] #" + hA + "\nnote A\n→ links: #" + hB + " depends on B\n"
	wantB := "api/b.rb [a] #" + hB + "\nnote B\n← linked-from: api/a.rb\n"
	if !strings.Contains(res.Stdout, wantA) || !strings.Contains(res.Stdout, wantB) {
		t.Errorf("expansions:\n%q\nwant to contain:\n%q\nand:\n%q", res.Stdout, wantA, wantB)
	}

	// The surface shows the far endpoint as handle + label — collapsed,
	// its body a size in the hidden tally (§5.1) — and the footer counts
	// the live edge.
	want := "→ #" + hB + " depends on B\ncontext: 1 own · 0 parent · 1 link · ~1 hidden\n"
	if got := r.MustDM("r:api/a.rb\n").Stdout; !strings.Contains(got, want) {
		t.Errorf("surface link dimension:\n%q\nwant to contain %q", got, want)
	}

	// Unlink; a second dl finds nothing — per-command error, batch intact.
	res = r.MustDM("dl:#" + hA + ":#" + hB + "\ndl:#" + hA + ":#" + hB + "\n")
	if !strings.Contains(res.Stdout, "✓ #"+hA+" → #"+hB+" unlinked\n") ||
		!strings.Contains(res.Stdout, "✗ no link #"+hA+" → #"+hB+"\n") ||
		!strings.HasSuffix(res.Stdout, "◾1 ok, 1 error\n") {
		t.Errorf("unlink flow:\n%q", res.Stdout)
	}
	if got := r.MustDM("r:#" + hA + "\n").Stdout; strings.Contains(got, "→ links:") {
		t.Errorf("expansion still shows the unlinked edge:\n%q", got)
	}
}

// TestHandleErrors pins semantic handle resolution as per-command errors
// (§5.3, architecture.md §7): unknown handles never reject the batch.
func TestHandleErrors(t *testing.T) {
	r := NewRepo(t)
	r.WriteFile("f.rb", "content\n")
	r.Commit("add f")
	r.MustDM("", "init")

	res := r.MustDM("u:#zzzzzz:body\na:f.rb:c:survives\n")
	if !strings.Contains(res.Stdout, "✗ #zzzzzz unknown\n") {
		t.Errorf("unknown handle:\n%q", res.Stdout)
	}
	if !strings.HasSuffix(res.Stdout, "◾1 ok, 1 error\n") {
		t.Errorf("footer:\n%q\nwant 1 ok, 1 error", res.Stdout)
	}

	// A $N pointing at a create that failed in phase 2 errors per-command.
	res = r.MustDM("a:missing.rb:c:ghost\nd:$1\n")
	if !strings.Contains(res.Stdout, "✗ $1: referenced create failed\n") ||
		!strings.HasSuffix(res.Stdout, "◾0 ok, 2 error\n") {
		t.Errorf("backref to failed create:\n%q", res.Stdout)
	}
}

// TestUnmergedBranchHandleUnknown pins §5.3: a pending-elsewhere entry is
// indistinguishable from a nonexistent one — its handle reads as unknown,
// and it surfaces the moment the origin lands.
func TestUnmergedBranchHandleUnknown(t *testing.T) {
	r := NewRepo(t)
	r.WriteFile("f.rb", "content\n")
	r.Commit("add f")
	r.MustDM("", "init")

	r.Git("checkout", "-q", "-b", "feature")
	r.WriteFile("f.rb", "feature content\n")
	r.Commit("feature work")
	handle := createdHandle(t, r.MustDM("a:f.rb:c:feature-only note\n").Stdout)

	r.Git("checkout", "-q", "main")
	res := r.MustDM("r:#" + handle + "\n")
	if !strings.Contains(res.Stdout, "✗ #"+handle+" unknown\n") {
		t.Errorf("on main:\n%q\nwant unknown (pending-elsewhere hides the handle)", res.Stdout)
	}
	r.Git("merge", "-q", "--no-edit", "feature")
	res = r.MustDM("r:#" + handle + "\n")
	if !strings.Contains(res.Stdout, "feature-only note") {
		t.Errorf("after landing:\n%q\nwant the entry addressable", res.Stdout)
	}
}

// TestSupersedeScopedToLineage replays §11.4 u-old-revision-on-branch: an
// unmerged branch keeps reading the revision that corresponds to its
// content; the new revision arrives when main lands there (§8.3
// per-record fold).
func TestSupersedeScopedToLineage(t *testing.T) {
	r := NewRepo(t)
	r.WriteFile("f.rb", "shared content\n")
	r.Commit("add f")
	r.MustDM("", "init")
	handle := createdHandle(t, r.MustDM("a:f.rb:c:original wording\n").Stdout)

	// Branch off, advance main, then supersede on main: the SU's origin
	// is a commit the branch does not descend from. The branch's tree
	// still matches the anchor (content unchanged) — only the SU's line
	// is missing there.
	r.Git("checkout", "-q", "-b", "feature")
	r.Git("checkout", "-q", "main")
	r.WriteFile("other.rb", "unrelated\n")
	r.Commit("main advances")
	r.MustDM("u:#" + handle + ":main wording\n")

	if got := r.MustDM("r:f.rb\n").Stdout; !strings.Contains(got, "main wording") {
		t.Errorf("main read:\n%q\nwant rev 2", got)
	}
	r.Git("checkout", "-q", "feature")
	got := r.MustDM("r:f.rb\n").Stdout
	if !strings.Contains(got, "c #"+handle+" original wording\n") {
		t.Errorf("branch read:\n%q\nwant its lineage's newest state (rev 1)", got)
	}
	// Land main on the branch: the branch now folds the SU.
	r.Git("merge", "-q", "--no-edit", "main")
	if got := r.MustDM("r:f.rb\n").Stdout; !strings.Contains(got, "main wording") {
		t.Errorf("branch read after landing main:\n%q\nwant rev 2", got)
	}
}

// TestMoveMacro pins `mv` (§6.5, §11.4 macros): per-entry RP with a
// scoping origin, the folded anchor untouched, per-entry errors isolated.
func TestMoveMacro(t *testing.T) {
	r := NewRepo(t)
	r.WriteFile("api/old.rb", "moving content\n")
	r.Commit("add old")
	blob := r.HashObject("api/old.rb")
	r.MustDM("", "init")
	handle := createdHandle(t, r.MustDM("a:api/old.rb:c:travels with the file\n").Stdout)

	// A move dm's M3 exact layer cannot follow: the manual override.
	r.Git("mv", "api/old.rb", "api/new.rb")
	r.Commit("move it")

	res := r.MustDM("mv:api/old.rb:api/new.rb\n")
	want := "▸mv:api/old.rb:api/new.rb\n✓ #" + handle + " moved\n◾1 ok\n"
	if res.Stdout != want {
		t.Errorf("mv output:\n%q\nwant:\n%q", res.Stdout, want)
	}

	// Resolved at the new path, gone from the old one.
	if got := r.MustDM("r:api/new.rb\n").Stdout; !strings.Contains(got, "c #"+handle+" travels with the file\n") {
		t.Errorf("read at destination:\n%q", got)
	}
	if got := r.MustDM("r:api/old.rb\n").Stdout; !strings.Contains(got, "context: 0 own") {
		t.Errorf("read at source:\n%q\nwant empty", got)
	}

	// The RP relocated without blessing: the CR still carries the original
	// anchor, and the RP carries only origin + path (dump asserts, §11.4).
	dump := r.MustDM("", "dump").Stdout
	if !strings.Contains(dump, " "+blob+" ") {
		t.Errorf("dump lost the original anchor %s:\n%s", blob, dump)
	}
	if !regexp.MustCompile(`RP [0-9A-HJKMNP-TV-Z]{26} [0-9A-HJKMNP-TV-Z]{26} [0-9a-f]{40} api/new\.rb\n`).MatchString(dump) {
		t.Errorf("dump lacks the RP record:\n%s", dump)
	}

	// Missing destination: a per-entry error; the rest of the batch holds.
	res = r.MustDM("mv:api/new.rb:api/nowhere.rb\nr:api/new.rb\n")
	if !strings.Contains(res.Stdout, "✗ #"+handle+": api/nowhere.rb: no such file in working tree\n") ||
		!strings.HasSuffix(res.Stdout, "◾1 ok, 1 error\n") {
		t.Errorf("mv to missing destination:\n%q", res.Stdout)
	}
}

// TestMoveFolderRecursive: `mv:api/:svc/` re-paths every entry under the
// prefix (§6.5).
func TestMoveFolderRecursive(t *testing.T) {
	r := NewRepo(t)
	r.WriteFile("api/a.rb", "a\n")
	r.WriteFile("api/sub/b.rb", "b\n")
	r.Commit("add tree")
	r.MustDM("", "init")
	res := r.MustDM("a:api/a.rb:c:note on a\na:api/sub/b.rb:c:note on b\n")
	hs := createdHandles(t, res.Stdout)

	r.Git("mv", "api", "svc")
	r.Commit("rename folder")

	res = r.MustDM("mv:api/:svc/\n")
	for _, h := range hs {
		if !strings.Contains(res.Stdout, "✓ #"+h+" moved\n") {
			t.Errorf("recursive mv:\n%q\nwant #%s moved", res.Stdout, h)
		}
	}
	if got := r.MustDM("r:svc/a.rb\nr:svc/sub/b.rb\n").Stdout; !strings.Contains(got, "note on a") || !strings.Contains(got, "note on b") {
		t.Errorf("reads after recursive mv:\n%q", got)
	}
}

// TestMoveScopedToItsLine replays §11.4 rp-scoped: the RP participates
// only where its origin has landed — a line without the move keeps
// resolving at the old path.
func TestMoveScopedToItsLine(t *testing.T) {
	r := NewRepo(t)
	r.WriteFile("f.rb", "stable content\n")
	r.Commit("add f")
	r.MustDM("", "init")
	handle := createdHandle(t, r.MustDM("a:f.rb:c:sticky note\n").Stdout)

	r.Git("checkout", "-q", "-b", "feature")
	r.Git("mv", "f.rb", "g.rb")
	r.Commit("move on feature")
	r.MustDM("mv:f.rb:g.rb\n")
	if got := r.MustDM("r:g.rb\n").Stdout; !strings.Contains(got, "sticky note") {
		t.Errorf("feature read at new path:\n%q", got)
	}

	// On main the move never happened: the RP hasn't landed, the entry
	// still resolves at the old path.
	r.Git("checkout", "-q", "main")
	if got := r.MustDM("r:f.rb\n").Stdout; !strings.Contains(got, "c #"+handle+" sticky note\n") {
		t.Errorf("main read at old path:\n%q\nwant the pre-move state", got)
	}
	if got := r.MustDM("r:g.rb\n").Stdout; !strings.Contains(got, "context: 0 own") {
		t.Errorf("main read at new path:\n%q\nwant nothing", got)
	}

	// Once feature lands, main follows the RP.
	r.Git("merge", "-q", "--no-edit", "feature")
	if got := r.MustDM("r:g.rb\n").Stdout; !strings.Contains(got, "sticky note") {
		t.Errorf("main read after landing the move:\n%q", got)
	}
}

// TestRemoveMacro pins `rm` (§6.5): one TB per entry at the path,
// recursive for folders.
func TestRemoveMacro(t *testing.T) {
	r := NewRepo(t)
	r.WriteFile("api/a.rb", "a\n")
	r.WriteFile("api/b.rb", "b\n")
	r.Commit("add files")
	r.MustDM("", "init")
	res := r.MustDM("a:api/a.rb:c:note a\na:api/b.rb:d:note b\n")
	hs := createdHandles(t, res.Stdout)

	res = r.MustDM("rm:api/\n")
	for _, h := range hs {
		if !strings.Contains(res.Stdout, "✓ #"+h+" tombstoned\n") {
			t.Errorf("rm output:\n%q\nwant #%s tombstoned", res.Stdout, h)
		}
	}
	if !strings.HasSuffix(res.Stdout, "◾1 ok\n") {
		t.Errorf("rm footer:\n%q", res.Stdout)
	}
	if got := r.MustDM("r:api/a.rb\nr:api/b.rb\n").Stdout; !strings.Contains(got, "context: 0 own · 0 parent · 0 links\n") ||
		strings.Contains(got, "note a") || strings.Contains(got, "note b") {
		t.Errorf("reads after rm:\n%q\nwant empty surfaces", got)
	}
	if got := strings.Count(r.MustDM("", "dump").Stdout, "TB "); got != 2 {
		t.Errorf("dump has %d TB records, want 2", got)
	}

	// A second rm finds nothing left standing.
	res = r.MustDM("rm:api/\n")
	if !strings.Contains(res.Stdout, "✗ no entries at api/\n") {
		t.Errorf("rm on emptied path:\n%q", res.Stdout)
	}
}
