package e2e

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// M2 exit criteria (plan.md): §8.6's write→read worked example runs
// end-to-end against a real repo, and §2.1 rows 2 and 7 pass. Pending
// alone carries the slice — no store branch yet.

var createdRe = regexp.MustCompile(`^\+ #([0-9a-hjkmnp-tv-z]{6}) created$`)

// createdHandle extracts the minted handle from an `a` ack block.
func createdHandle(t *testing.T, stdout string) string {
	t.Helper()
	for _, line := range strings.Split(stdout, "\n") {
		if m := createdRe.FindStringSubmatch(line); m != nil {
			return m[1]
		}
	}
	t.Fatalf("no created ack in output:\n%s", stdout)
	return ""
}

// TestWorkedExample replays §8.6: one note, write to read, plus dm init
// and dm dump around it.
func TestWorkedExample(t *testing.T) {
	r := NewRepo(t)
	r.WriteFile("api/handler.rb", "class Handler\nend\n")
	head := r.Commit("add handler")
	blob := r.HashObject("api/handler.rb")

	res := r.MustDM("", "init")
	if !strings.Contains(res.Stdout, "initialized") || !strings.Contains(res.Stdout, r.ReplicaID) {
		t.Fatalf("init output %q", res.Stdout)
	}
	res = r.MustDM("", "init")
	if !strings.Contains(res.Stdout, "already initialized (replica "+r.ReplicaID+")") {
		t.Fatalf("re-init output %q", res.Stdout)
	}

	// Write: one pending record appended, ack echoes the minted handle.
	res = r.MustDM("a:api/handler.rb:c:Validates tenant header before dispatch\n")
	handle := createdHandle(t, res.Stdout)
	want := "▸a:api/handler.rb:c:Validates tenant header before dispatch\n" +
		"+ #" + handle + " created\n" +
		"◾1 ok\n"
	if res.Stdout != want {
		t.Errorf("write output:\n%q\nwant:\n%q", res.Stdout, want)
	}
	records, err := os.ReadDir(filepath.Join(r.DMDir(), "pending", "records"))
	if err != nil || len(records) != 1 {
		t.Fatalf("pending records = %v, %v (want exactly one)", records, err)
	}

	// The committed blob is already in the odb: nothing staged.
	staged, err := os.ReadDir(filepath.Join(r.DMDir(), "pending", "blobs"))
	if err != nil || len(staged) != 0 {
		t.Fatalf("staged blobs = %v, %v (want none for committed content)", staged, err)
	}

	// Read, next invocation: store ∪ pending — no sync step exists or is
	// needed.
	res = r.MustDM("r:api/handler.rb\n")
	want = "▸r:api/handler.rb\n" +
		"c #" + handle + " Validates tenant header before dispatch\n" +
		"context: 1 own · 0 parent · 0 links\n" +
		"◾1 ok\n"
	if res.Stdout != want {
		t.Errorf("read output:\n%q\nwant:\n%q", res.Stdout, want)
	}

	// dm dump: the ground truth is exactly one CR record carrying every
	// §8.6 field: rec-id, entry-id, subj, anchor blob, origin, path, body.
	res = r.MustDM("", "dump")
	dumpRe := regexp.MustCompile(`^CR [0-9A-HJKMNP-TV-Z]{26} [0-9A-HJKMNP-TV-Z]{26} c ` +
		blob + ` ` + head + ` api/handler\.rb Validates tenant header before dispatch\n$`)
	if !dumpRe.MatchString(res.Stdout) {
		t.Errorf("dump output %q does not match %v", res.Stdout, dumpRe)
	}
}

// TestRow2Lineage replays §2.1 row 2: the note is visible whenever the
// tree contains F as described and the checkout descends from the origin.
func TestRow2Lineage(t *testing.T) {
	r := NewRepo(t)
	r.WriteFile("f.rb", "f content\n")
	first := r.Commit("add f")
	r.WriteFile("other.rb", "other\n")
	r.Commit("unrelated progress")
	r.MustDM("", "init")

	res := r.MustDM("a:f.rb:c:F gotcha\n")
	handle := createdHandle(t, res.Stdout)
	visible := "c #" + handle + " F gotcha\ncontext: 1 own · 0 parent · 0 links\n"
	hidden := "context: 0 own · 0 parent · 0 links\n"

	// On main: visible.
	if got := r.MustDM("r:f.rb\n").Stdout; !strings.Contains(got, visible) {
		t.Errorf("on main:\n%q\nwant it to contain %q", got, visible)
	}
	// A branch off main descends from the origin: still visible.
	r.Git("checkout", "-q", "-b", "feature")
	if got := r.MustDM("r:f.rb\n").Stdout; !strings.Contains(got, visible) {
		t.Errorf("on feature branch:\n%q\nwant it to contain %q", got, visible)
	}
	// Detached at the first commit the tree contains F as described, but
	// the origin line has not landed there: invisible (rule (b)).
	r.Git("checkout", "-q", first)
	if got := r.MustDM("r:f.rb\n").Stdout; !strings.Contains(got, hidden) {
		t.Errorf("detached before origin:\n%q\nwant empty surface %q", got, hidden)
	}
	// Back on main: visible again — visibility is a pure read-time query.
	r.Git("checkout", "-q", "main")
	if got := r.MustDM("r:f.rb\n").Stdout; !strings.Contains(got, visible) {
		t.Errorf("back on main:\n%q\nwant it to contain %q", got, visible)
	}
}

// TestRow7ImmediateVisibility replays §2.1 row 7: a note on uncommitted
// content is visible immediately — same batch and next invocation — with
// the dirty blob staged to pending/blobs.
func TestRow7ImmediateVisibility(t *testing.T) {
	r := NewRepo(t)
	r.WriteFile("g.rb", "committed\n")
	r.Commit("add g")
	r.MustDM("", "init")
	r.WriteFile("g.rb", "uncommitted edit\n")
	dirtyBlob := r.HashObject("g.rb")

	// Write and read in one batch: read-your-writes via the pending
	// overlay, no commit or sync step first.
	res := r.MustDM("a:g.rb:c:Note on dirty bytes\nr:g.rb\n")
	handle := createdHandle(t, res.Stdout)
	visible := "c #" + handle + " Note on dirty bytes\ncontext: 1 own · 0 parent · 0 links\n"
	if !strings.Contains(res.Stdout, visible) {
		t.Errorf("same-batch read:\n%q\nwant it to contain %q", res.Stdout, visible)
	}
	if !strings.HasSuffix(res.Stdout, "◾2 ok\n") {
		t.Errorf("footer of:\n%q\nwant ◾2 ok", res.Stdout)
	}
	// Next invocation: still visible.
	if got := r.MustDM("r:g.rb\n").Stdout; !strings.Contains(got, visible) {
		t.Errorf("next invocation:\n%q\nwant it to contain %q", got, visible)
	}
	// The odb lacks the dirty bytes, so they were staged for sync (§8.1).
	if _, err := os.Stat(filepath.Join(r.DMDir(), "pending", "blobs", dirtyBlob)); err != nil {
		t.Errorf("dirty blob not staged: %v", err)
	}
	// Reverting the edit removes the described content from the tree: the
	// note leaves the surface (rule (a); similarity layers arrive M5).
	r.Git("checkout", "--", "g.rb")
	hidden := "context: 0 own · 0 parent · 0 links\n"
	if got := r.MustDM("r:g.rb\n").Stdout; !strings.Contains(got, hidden) {
		t.Errorf("after revert:\n%q\nwant empty surface %q", got, hidden)
	}
}

// TestParseRejectsWholeBatch pins phase 1 of §4.1: any syntax error
// rejects the batch, nothing executes, nothing is written.
func TestParseRejectsWholeBatch(t *testing.T) {
	r := NewRepo(t)
	r.WriteFile("f.rb", "content\n")
	r.Commit("add f")
	r.MustDM("", "init")

	res := r.DM("a:f.rb:c:fine note\na:f.rb:x:bad subject\n")
	if res.Code == 0 {
		t.Errorf("rejected batch exited 0")
	}
	wantHead := "!parse error line 2: expected subject (c|a|d|o) after path\n"
	if res.Stdout != wantHead {
		t.Errorf("stdout %q, want single error line %q", res.Stdout, wantHead)
	}
	if got := r.MustDM("", "dump").Stdout; got != "" {
		t.Errorf("dump after rejected batch = %q, want nothing written", got)
	}
}

// TestPerCommandErrors pins phase 2 isolation (§4.1/§4.4): a failing write
// prints its own ✗ line while the rest of the batch still applies.
func TestPerCommandErrors(t *testing.T) {
	r := NewRepo(t)
	r.WriteFile("present.rb", "content\n")
	r.Commit("add present")
	r.MustDM("", "init")

	res := r.MustDM("a:missing.rb:c:on a ghost\na:present.rb:c:solid note\n")
	if !strings.Contains(res.Stdout, "✗ missing.rb: no such file in working tree\n") {
		t.Errorf("missing-file block absent in:\n%q", res.Stdout)
	}
	handle := createdHandle(t, res.Stdout)
	if !strings.HasSuffix(res.Stdout, "◾1 ok, 1 error\n") {
		t.Errorf("footer of:\n%q\nwant ◾1 ok, 1 error", res.Stdout)
	}
	if got := r.MustDM("r:present.rb\n").Stdout; !strings.Contains(got, "#"+handle+" solid note") {
		t.Errorf("surviving command not applied:\n%q", got)
	}

	// Body-byte rejections are phase-2 write errors, not parse errors
	// (§4.3): the framing glyphs may never appear in a body.
	res = r.MustDM("a:present.rb:c:has a ▸ glyph\n")
	if !strings.Contains(res.Stdout, "✗ ") || !strings.HasSuffix(res.Stdout, "◾0 ok, 1 error\n") {
		t.Errorf("glyph body should fail per-command:\n%q", res.Stdout)
	}
}

// TestMultilineBody pins the `\` continuation end-to-end: the body
// round-trips through the record and the preview carries the size hint.
func TestMultilineBody(t *testing.T) {
	r := NewRepo(t)
	r.WriteFile("f.rb", "content\n")
	r.Commit("add f")
	r.MustDM("", "init")

	res := r.MustDM("a:f.rb:c:first line\\\nsecond line\n")
	handle := createdHandle(t, res.Stdout)
	read := r.MustDM("r:f.rb\n").Stdout
	wantLine := "c #" + handle + " first line (+1 lines)\n"
	if !strings.Contains(read, wantLine) {
		t.Errorf("read:\n%q\nwant preview %q", read, wantLine)
	}
	// The stored record uses the same continuation framing (§8.3).
	dump := r.MustDM("", "dump").Stdout
	if !strings.Contains(dump, "first line\\\nsecond line\n") {
		t.Errorf("dump %q lacks the framed multi-line body", dump)
	}
}

// TestNotInitialized: batch and dump refuse until dm init has run.
func TestNotInitialized(t *testing.T) {
	r := NewRepo(t)
	r.WriteFile("f.rb", "content\n")
	r.Commit("add f")

	res := r.DM("r:f.rb\n")
	if res.Code == 0 || !strings.Contains(res.Stderr, "not initialized") {
		t.Errorf("uninitialized batch: exit %d, stderr %q", res.Code, res.Stderr)
	}
	res = r.DM("", "dump")
	if res.Code == 0 || !strings.Contains(res.Stderr, "not initialized") {
		t.Errorf("uninitialized dump: exit %d, stderr %q", res.Code, res.Stderr)
	}
}
