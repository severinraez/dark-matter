package e2e

import (
	"regexp"
	"strings"
	"testing"
)

// The §11.4 path-escaping and body-bytes scenario groups, end to end:
// the canonical percent-encoding (§4.1/§8.3) exercised through the real
// binary against real files, and the §4.3 body-byte rules through write,
// store, and surface. Codec-level goldens live in core/record; these pin
// the same rules at the process boundary.

func TestPathEscapingColonVsDepth(t *testing.T) {
	r := NewRepo(t)
	// A file literally named `foo:1` beside a folder named `foo` — the
	// case the escaping exists for: `r:foo%3A1` must read the file while
	// `r:foo:1` is a depth-1 read of the folder.
	r.WriteFile("foo:1", "colon file\n")
	r.WriteFile("foo/inner.rb", "class Inner\nend\n")
	r.Commit("base")
	r.MustDM("", "init")

	res := r.MustDM("a:foo%3A1:c:note on the colon file\na:foo/inner.rb:c:note on the inner file\na:foo/:a:folder note\n")
	hs := createdHandles(t, res.Stdout)
	colonH := hs[0]

	got := r.MustDM("r:foo%3A1\n").Stdout
	if !strings.Contains(got, "▸r:foo%3A1\n") ||
		!strings.Contains(got, "c #"+colonH+" note on the colon file\n") {
		t.Errorf("r:foo%%3A1 should read the colon file:\n%q", got)
	}
	if strings.Contains(got, "inner file") || strings.Contains(got, "folder note") {
		t.Errorf("colon-file read must not touch the folder:\n%q", got)
	}

	// Same characters, unescaped: a depth-1 read of node `foo` — the
	// trailing `:1` parses as the depth modifier, never as part of the
	// path. (Folder nodes are addressed with the trailing slash, as
	// everywhere in §4/§5: `foo` names no node here, so the read is
	// empty — the point is that it is *not* a read of the colon file.)
	res2 := r.MustDM("r:foo:1\n")
	if !strings.Contains(res2.Stdout, "▸r:foo:1\n") ||
		!strings.HasSuffix(res2.Stdout, "◾1 ok\n") {
		t.Errorf("r:foo:1 must parse as path foo + depth 1:\n%q", res2.Stdout)
	}
	if strings.Contains(res2.Stdout, "colon file") {
		t.Errorf("depth read must not surface the colon file:\n%q", res2.Stdout)
	}
	// The folder node itself, at depth: own note plus child inventory.
	got = r.MustDM("r:foo/:1\n").Stdout
	if !strings.Contains(got, "folder note") ||
		!strings.Contains(got, "↓ foo/inner.rb 1c\n") {
		t.Errorf("r:foo/:1 folder read:\n%q", got)
	}
}

func TestPathEscapingBarePercentRejects(t *testing.T) {
	r := NewRepo(t)
	r.WriteFile("50off.rb", "content\n")
	r.Commit("base")
	r.MustDM("", "init")

	// A bare `%` (not followed by two hex digits) rejects the whole
	// batch at phase 1 with the teaching hint — a forgotten escape fails
	// loudly instead of naming the wrong file (§4.1).
	res := r.DM("a:50%off.rb:c:discount logic\na:50off.rb:c:fine\n")
	if res.Code == 0 {
		t.Errorf("bare %% batch exited 0:\n%q", res.Stdout)
	}
	if !strings.Contains(res.Stdout, "%25") || !strings.Contains(res.Stdout, "%3A") {
		t.Errorf("rejection must carry the %%25/%%3A hint:\n%q", res.Stdout)
	}
	if got := r.MustDM("", "dump").Stdout; got != "" {
		t.Errorf("phase-1 rejection wrote records:\n%q", got)
	}
}

// TestPathRoundTrip pins the canonical round-trip: every path dm prints
// re-parses as valid input (§4.1/§8.3). A path exercising the whole
// canonical set (space and colon) is written, printed, and the printed
// form fed back verbatim.
func TestPathRoundTrip(t *testing.T) {
	r := NewRepo(t)
	r.WriteFile("my file:v2.rb", "tricky name\n")
	r.Commit("base")
	r.MustDM("", "init")

	handle := createdHandle(t, r.MustDM("a:my%20file%3Av2.rb:c:on a hostile name\n").Stdout)

	// The expansion header prints the canonical path.
	got := r.MustDM("r:#" + handle + "\n").Stdout
	m := regexp.MustCompile(`(?m)^(\S+) \[c\] #` + handle + `$`).FindStringSubmatch(got)
	if m == nil {
		t.Fatalf("expansion header with path not found in:\n%q", got)
	}
	printed := m[1]
	if printed != "my%20file%3Av2.rb" {
		t.Errorf("printed path %q, want the canonical encoding", printed)
	}
	// The printed path is valid input and names the same file.
	back := r.MustDM("r:" + printed + "\n").Stdout
	if !strings.Contains(back, "c #"+handle+" on a hostile name\n") {
		t.Errorf("printed path did not round-trip as input:\n%q", back)
	}
	// The dump encoding round-trips too (dump prints canonical records).
	dump := r.MustDM("", "dump").Stdout
	if !strings.Contains(dump, " my%20file%3Av2.rb ") {
		t.Errorf("dump lacks the canonical path:\n%q", dump)
	}
}

func TestBodyBackslashSemantics(t *testing.T) {
	r := NewRepo(t)
	r.WriteFile("f.rb", "content\n")
	r.Commit("base")
	r.MustDM("", "init")

	// A body line ending `\\` ends the command with a literal backslash;
	// a single `\` continues to the next line (§4.1). One batch, both
	// forms — and `:` in a body needs no escaping.
	res := r.MustDM("a:f.rb:c:ends with a backslash\\\\\n" +
		"a:f.rb:c:spans\\\ntwo lines\n" +
		"a:f.rb:c:ratio is 3:1\n")
	hs := createdHandles(t, res.Stdout)
	if len(hs) != 3 {
		t.Fatalf("want 3 creates in:\n%q", res.Stdout)
	}

	got := r.MustDM("r:#" + hs[0] + "\n").Stdout
	if !strings.Contains(got, "ends with a backslash\\\n") {
		t.Errorf("terminal \\\\ should decode to one literal backslash:\n%q", got)
	}
	got = r.MustDM("r:#" + hs[1] + "\n").Stdout
	if !strings.Contains(got, "spans\ntwo lines\n") {
		t.Errorf("continuation body:\n%q", got)
	}
	got = r.MustDM("r:#" + hs[2] + "\n").Stdout
	if !strings.Contains(got, "ratio is 3:1\n") {
		t.Errorf("unescaped colon in body:\n%q", got)
	}
}

// TestBodyBytesTabRoundTrip pins §11.4 body bytes: a tab-bearing body (a
// Makefile snippet) round-trips byte-identically through write, store,
// and surface.
func TestBodyBytesTabRoundTrip(t *testing.T) {
	r := NewRepo(t)
	r.WriteFile("Makefile", "build:\n\tgo build ./...\n")
	r.Commit("base")
	r.MustDM("", "init")

	body := "run with:\\\nbuild:\\\n\tgo build ./..." // decoded: run with:\nbuild:\n\tgo build ./...
	handle := createdHandle(t, r.MustDM("a:Makefile:d:"+body+"\n").Stdout)
	wantBody := "run with:\nbuild:\n\tgo build ./...\n"

	// Surface expansion: raw bytes, tab intact.
	if got := r.MustDM("r:#" + handle + "\n").Stdout; !strings.Contains(got, wantBody) {
		t.Errorf("expansion before sync:\n%q\nwant body %q", got, wantBody)
	}
	// Through the store: sync folds pending into refs/dm/store; the
	// record, and the read, stay byte-identical.
	preDump := r.MustDM("", "dump").Stdout
	r.MustDM("", "sync")
	if names := r.PendingRecordNames(); len(names) != 0 {
		t.Fatalf("pending not cleared: %v", names)
	}
	if postDump := r.MustDM("", "dump").Stdout; postDump != preDump {
		t.Errorf("store round-trip changed bytes:\npre:  %q\npost: %q", preDump, postDump)
	}
	if got := r.MustDM("r:#" + handle + "\n").Stdout; !strings.Contains(got, wantBody) {
		t.Errorf("expansion after sync:\n%q", got)
	}
}

func TestBodyBytesRejects(t *testing.T) {
	r := NewRepo(t)
	r.WriteFile("f.rb", "content\n")
	r.Commit("base")
	r.MustDM("", "init")

	// NUL, another C0 control, and each framing glyph: all reject with a
	// per-command ✗ (§4.3); a healthy sibling command still applies.
	for _, tc := range []struct{ name, body string }{
		{"nul", "has a \x00 byte"},
		{"bell", "has a \x07 byte"},
		{"escape", "has an \x1b byte"},
		{"cr", "has a \r byte"},
		{"next-glyph", "has a ▸ glyph"},
		{"end-glyph", "has a ◾ glyph"},
	} {
		res := r.MustDM("a:f.rb:c:" + tc.body + "\na:f.rb:c:healthy " + tc.name + "\n")
		if !strings.Contains(res.Stdout, "✗ ") ||
			!strings.HasSuffix(res.Stdout, "◾1 ok, 1 error\n") {
			t.Errorf("%s: want one ✗ and one ok:\n%q", tc.name, res.Stdout)
		}
	}
	// Nothing hostile reached the store side: dump stays parseable and
	// glyph-free beyond its own framing.
	dump := r.MustDM("", "dump").Stdout
	for _, banned := range []string{"\x00", "\x07", "\x1b", "\r", "▸", "◾"} {
		if strings.Contains(dump, banned) {
			t.Errorf("dump carries rejected byte %q:\n%q", banned, dump)
		}
	}
}
