package record

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// Fixed ids reused across goldens — the §8.6 worked example's pair plus a
// second entry for links.
const (
	recA   = "01K0N3AB4X8QF3ZJ4WT2P9K6H1"
	entryA = "01K0N3AB4XA3F9C1TQ84MZV2R7" // handle #a3f9c1
	entryB = "01K0N3AB4X7C22A1WJ9RD0PQZ4" // handle #7c22a1
)

func mustRec(t *testing.T, s string) RecID {
	t.Helper()
	r, err := ParseRecID(s)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func mustEntry(t *testing.T, s string) EntryID {
	t.Helper()
	e, err := ParseEntryID(s)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// TestCodecGoldens pins the canonical encoding of every record type
// (byte-identity is a CRDT invariant — these strings are the contract).
func TestCodecGoldens(t *testing.T) {
	rec := func() RecID { return mustRec(t, recA) }
	ea := func() EntryID { return mustEntry(t, entryA) }
	eb := func() EntryID { return mustEntry(t, entryB) }

	cases := []struct {
		name   string
		rec    Record
		golden string
	}{
		{
			// The §8.6 worked example, verbatim shape.
			name: "create-file-note",
			rec: Create{Rec: rec(), Entry: ea(), Subj: SubjCode,
				Anchor: BlobAnchor("9f4e2ab"), Origin: "41c09d7",
				Path: "api/handler.rb", Body: "Validates tenant header before dispatch"},
			golden: "CR " + recA + " " + entryA + " c 9f4e2ab 41c09d7 api/handler.rb Validates tenant header before dispatch\n",
		},
		{
			name: "create-folder-note",
			rec: Create{Rec: rec(), Entry: ea(), Subj: SubjArch,
				Anchor: PathAnchor("api/"), Origin: "41c09d7",
				Path: "api/", Body: "api reaches db only through repo/."},
			golden: "CR " + recA + " " + entryA + " a api/ 41c09d7 api/ api reaches db only through repo/.\n",
		},
		{
			name: "supersede",
			rec: Supersede{Rec: rec(), Entry: ea(), Subj: SubjCode,
				Anchor: BlobAnchor("9f4e2ab"), Origin: "41c09d7",
				Path: "api/handler.rb", Body: "Now partitioned per tenant"},
			golden: "SU " + recA + " " + entryA + " c 9f4e2ab 41c09d7 api/handler.rb Now partitioned per tenant\n",
		},
		{
			name:   "tombstone",
			rec:    Tombstone{Rec: rec(), Entry: ea()},
			golden: "TB " + recA + " " + entryA + "\n",
		},
		{
			name: "reanchor",
			rec: ReAnchor{Rec: rec(), Entry: ea(),
				Anchor: BlobAnchor("9f4e2ab"), Origin: "41c09d7", Path: "svc/handler.rb"},
			golden: "RA " + recA + " " + entryA + " 9f4e2ab 41c09d7 svc/handler.rb\n",
		},
		{
			name:   "repath",
			rec:    RePath{Rec: rec(), Entry: ea(), Origin: "41c09d7", Path: "svc/handler.rb"},
			golden: "RP " + recA + " " + entryA + " 41c09d7 svc/handler.rb\n",
		},
		{
			name:   "link-no-comment",
			rec:    Link{Rec: rec(), From: ea(), To: eb()},
			golden: "LN " + recA + " " + entryA + " " + entryB + "\n",
		},
		{
			name:   "link-comment",
			rec:    Link{Rec: rec(), From: ea(), To: eb(), Comment: "schema STI: see tenancy"},
			golden: "LN " + recA + " " + entryA + " " + entryB + " schema STI: see tenancy\n",
		},
		{
			name:   "unlink",
			rec:    Unlink{Rec: rec(), From: ea(), To: eb()},
			golden: "UL " + recA + " " + entryA + " " + entryB + "\n",
		},
		{
			name:   "feedback-no-reason",
			rec:    Feedback{Rec: rec(), Entry: ea(), Sig: SigUseful},
			golden: "FB " + recA + " " + entryA + " +\n",
		},
		{
			name:   "feedback-reason",
			rec:    Feedback{Rec: rec(), Entry: ea(), Sig: SigDisputed, Reason: "repo/ layer was removed in v3"},
			golden: "FB " + recA + " " + entryA + " ! repo/ layer was removed in v3\n",
		},
		{
			// Trailing fields share the body-last grammar: a reason spans
			// lines via the same continuation framing.
			name:   "feedback-multiline-reason",
			rec:    Feedback{Rec: rec(), Entry: ea(), Sig: SigDisputed, Reason: "wrong since v3\nsee ADR-7"},
			golden: "FB " + recA + " " + entryA + " ! wrong since v3\\\nsee ADR-7\n",
		},
		{
			name:   "verdict-landed",
			rec:    Verdict{Rec: rec(), Origin: "41c09d7", Landed: true, LandedAs: "b7d21f0", Matcher: MatcherSquash},
			golden: "VD " + recA + " 41c09d7 landed b7d21f0 m3\n",
		},
		{
			name:   "verdict-unlanded",
			rec:    Verdict{Rec: rec(), Origin: "41c09d7"},
			golden: "VD " + recA + " 41c09d7 unlanded\n",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Encode(tc.rec)
			if err != nil {
				t.Fatalf("Encode: %v", err)
			}
			if string(got) != tc.golden {
				t.Fatalf("Encode = %q\nwant       %q", got, tc.golden)
			}
			back, err := Decode(got)
			if err != nil {
				t.Fatalf("Decode: %v", err)
			}
			if diff := cmp.Diff(tc.rec, back); diff != "" {
				t.Fatalf("round-trip struct mismatch (-want +got):\n%s", diff)
			}
			again, err := Encode(back)
			if err != nil {
				t.Fatalf("re-Encode: %v", err)
			}
			if string(again) != tc.golden {
				t.Fatalf("re-Encode not byte-identical: %q", again)
			}
		})
	}
}

// TestCodecPathEscaping pins the §11.4 path-escaping rows at codec level:
// the canonical set (% : space C0), and nothing more, encodes.
func TestCodecPathEscaping(t *testing.T) {
	cases := []struct {
		raw     Path
		encoded string
	}{
		{"foo:1", "foo%3A1"},                   // the r:foo%3A1 file
		{"has space/x.rb", "has%20space/x.rb"}, // space
		{"100%.rb", "100%25.rb"},               // literal %
		{"tab\tname", "tab%09name"},            // tab percent-encodes as C0 in paths
		{"new\nline", "new%0Aline"},            // C0
		{"plain/path.go", "plain/path.go"},     // nothing escapes
		{"uni/héllo.rb", "uni/héllo.rb"},       // non-ASCII stays raw
		{"q?&#=+.rb", "q?&#=+.rb"},             // net/url would escape these; the canon must not
	}
	for _, tc := range cases {
		if got := EncodePath(tc.raw); got != tc.encoded {
			t.Errorf("EncodePath(%q) = %q, want %q", tc.raw, got, tc.encoded)
		}
		back, err := DecodePath(tc.encoded)
		if err != nil {
			t.Errorf("DecodePath(%q): %v", tc.encoded, err)
		} else if back != tc.raw {
			t.Errorf("DecodePath(%q) = %q, want %q", tc.encoded, back, tc.raw)
		}
	}

	// Every path dm prints re-parses as valid input: canonical round-trip
	// through a full record.
	r := RePath{Rec: mustRec(t, recA), Entry: mustEntry(t, entryA),
		Origin: "41c09d7", Path: "we ird:100%/tab\there.rb"}
	enc, err := Encode(r)
	if err != nil {
		t.Fatal(err)
	}
	want := "RP " + recA + " " + entryA + " 41c09d7 we%20ird%3A100%25/tab%09here.rb\n"
	if string(enc) != want {
		t.Fatalf("encoded %q, want %q", enc, want)
	}
	back, err := Decode(enc)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(Record(r), back); diff != "" {
		t.Fatalf("mismatch:\n%s", diff)
	}

	// Strict decoding: a % not followed by two hex digits fails loudly.
	for _, bad := range []string{"50%", "50%.rb", "a%3", "a%zz.rb"} {
		if _, err := DecodePath(bad); err == nil {
			t.Errorf("DecodePath(%q): want error", bad)
		}
	}

	// Non-canonical (but decodable) escapes are rejected in stored fields.
	nonCanon := "RP " + recA + " " + entryA + " 41c09d7 a%41.rb\n" // %41 = 'A'
	if _, err := Decode([]byte(nonCanon)); err == nil || !strings.Contains(err.Error(), "non-canonical") {
		t.Errorf("Decode(non-canonical path): got %v, want non-canonical error", err)
	}
}

// TestCodecBodyBytes pins the §11.4 body-bytes rows: tab round-trips
// byte-identically; NUL, other C0, and the framing glyphs reject.
func TestCodecBodyBytes(t *testing.T) {
	base := Create{Rec: mustRec(t, recA), Entry: mustEntry(t, entryA), Subj: SubjDev,
		Anchor: BlobAnchor("9f4e2ab"), Origin: "41c09d7", Path: "Makefile"}

	t.Run("tab-round-trips", func(t *testing.T) {
		r := base
		r.Body = "build:\n\tgo build ./... # gofmt'd\ttabs stay"
		enc, err := Encode(r)
		if err != nil {
			t.Fatal(err)
		}
		back, err := Decode(enc)
		if err != nil {
			t.Fatal(err)
		}
		if back.(Create).Body != r.Body {
			t.Fatalf("body %q, want %q", back.(Create).Body, r.Body)
		}
	})

	t.Run("rejects", func(t *testing.T) {
		for name, body := range map[string]string{
			"nul":        "a\x00b",
			"escape":     "a\x1bb",
			"rs":         "a\x1eb",
			"cr":         "a\rb",
			"next-glyph": "fake ▸r:x block",
			"end-glyph":  "fake ◾1 ok footer",
			"empty":      "",
		} {
			r := base
			r.Body = body
			if _, err := Encode(r); err == nil {
				t.Errorf("%s: Encode accepted %q", name, body)
			}
		}
	})
}

// TestCodecBodyContinuation pins the trailing-\ framing (§4.1/§8.3 and the
// §11.4 row: a line ending \\ ends with a literal backslash, \ continues).
func TestCodecBodyContinuation(t *testing.T) {
	base := Create{Rec: mustRec(t, recA), Entry: mustEntry(t, entryA), Subj: SubjCode,
		Anchor: BlobAnchor("9f4e2ab"), Origin: "41c09d7", Path: "x.rb"}
	head := "CR " + recA + " " + entryA + " c 9f4e2ab 41c09d7 x.rb "

	cases := []struct {
		name   string
		body   string
		golden string
	}{
		{"two-lines", "line one\nline two", head + "line one\\\nline two\n"},
		{"trailing-backslash", `ends with \`, head + `ends with \\` + "\n"},
		{"backslash-then-newline", "ends with \\\nnext", head + "ends with \\\\\\\nnext\n"},
		{"mid-line-backslash-raw", `a\b stays raw`, head + `a\b stays raw` + "\n"},
		{"trailing-empty-line", "para\n", head + "para\\\n\n"},
		{"double-newline", "a\n\nb", head + "a\\\n\\\nb\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := base
			r.Body = tc.body
			enc, err := Encode(r)
			if err != nil {
				t.Fatal(err)
			}
			if string(enc) != tc.golden {
				t.Fatalf("Encode = %q\nwant       %q", enc, tc.golden)
			}
			back, err := Decode(enc)
			if err != nil {
				t.Fatal(err)
			}
			if got := back.(Create).Body; got != tc.body {
				t.Fatalf("body %q, want %q", got, tc.body)
			}
		})
	}

	// A record line whose *path* ends in a backslash frames too — the
	// continuation escape is record-wide, so no field can fake framing.
	rp := RePath{Rec: mustRec(t, recA), Entry: mustEntry(t, entryA),
		Origin: "41c09d7", Path: `dir\`}
	enc, err := Encode(rp)
	if err != nil {
		t.Fatal(err)
	}
	want := "RP " + recA + " " + entryA + " 41c09d7 dir\\\\\n"
	if string(enc) != want {
		t.Fatalf("encoded %q, want %q", enc, want)
	}
	back, err := Decode(enc)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(Record(rp), back); diff != "" {
		t.Fatalf("mismatch:\n%s", diff)
	}
}

func TestDecodeRejects(t *testing.T) {
	valid := "TB " + recA + " " + entryA + "\n"
	if _, err := Decode([]byte(valid)); err != nil {
		t.Fatalf("control: %v", err)
	}
	cases := map[string]string{
		"no-newline":            "TB " + recA + " " + entryA,
		"dangling-continuation": "TB " + recA + " " + entryA + "\\\n",
		"trailing-garbage":      valid + "TB " + recA + " " + entryA + "\n",
		"unknown-type":          "XX " + recA + " " + entryA + "\n",
		"lowercase-ulid":        "TB " + strings.ToLower(recA) + " " + entryA + "\n",
		"double-space":          "TB " + recA + "  " + entryA + "\n",
		"missing-field":         "TB " + recA + "\n",
		"extra-field":           "TB " + recA + " " + entryA + " x\n",
		"bad-subject":           "CR " + recA + " " + entryA + " z 9f4e2ab 41c09d7 x.rb body\n",
		"bad-anchor":            "CR " + recA + " " + entryA + " c not_hex! 41c09d7 x.rb body\n",
		"uppercase-sha":         "CR " + recA + " " + entryA + " c 9F4E2AB 41c09d7 x.rb body\n",
		"empty-body":            "CR " + recA + " " + entryA + " c 9f4e2ab 41c09d7 x.rb \n",
		"bad-sig":               "FB " + recA + " " + entryA + " ?\n",
		"empty-reason":          "FB " + recA + " " + entryA + " ! \n",
		"bad-matcher":           "VD " + recA + " 41c09d7 landed b7d21f0 m4\n",
		"vd-bad-keyword":        "VD " + recA + " 41c09d7 floated b7d21f0 m3\n",
		"vd-unlanded-extra":     "VD " + recA + " 41c09d7 unlanded b7d21f0\n",
		"vd-landed-short":       "VD " + recA + " 41c09d7 landed b7d21f0\n",
	}
	for name, in := range cases {
		if _, err := Decode([]byte(in)); err == nil {
			t.Errorf("%s: Decode accepted %q", name, in)
		}
	}
}

// TestEncodeValidation: malformed structs never encode — nothing
// non-canonical can enter the store from this side either.
func TestEncodeValidation(t *testing.T) {
	rec := mustRec(t, recA)
	entry := mustEntry(t, entryA)
	cases := map[string]Record{
		"zero-rec-id":         Tombstone{Entry: entry},
		"zero-entry-id":       Tombstone{Rec: rec},
		"bad-subject":         Create{Rec: rec, Entry: entry, Subj: "z", Anchor: BlobAnchor("9f4e2ab"), Origin: "41c09d7", Path: "x", Body: "b"},
		"bad-origin":          Create{Rec: rec, Entry: entry, Subj: SubjCode, Anchor: BlobAnchor("9f4e2ab"), Origin: "NOPE", Path: "x", Body: "b"},
		"empty-path":          Create{Rec: rec, Entry: entry, Subj: SubjCode, Anchor: BlobAnchor("9f4e2ab"), Origin: "41c09d7", Body: "b"},
		"folder-key-no-slash": Create{Rec: rec, Entry: entry, Subj: SubjArch, Anchor: PathAnchor("api"), Origin: "41c09d7", Path: "api/", Body: "b"},
		"anchor-both-sides":   Create{Rec: rec, Entry: entry, Subj: SubjArch, Anchor: Anchor{Blob: "9f4e2ab", PathKey: "api/"}, Origin: "41c09d7", Path: "api/", Body: "b"},
		"nul-in-comment":      Link{Rec: rec, From: entry, To: mustEntry(t, entryB), Comment: "a\x00b"},
		"glyph-in-reason":     Feedback{Rec: rec, Entry: entry, Sig: SigDisputed, Reason: "fake ◾ footer"},
		"unlanded-with-as":    Verdict{Rec: rec, Origin: "41c09d7", LandedAs: "b7d21f0"},
		"landed-no-matcher":   Verdict{Rec: rec, Origin: "41c09d7", Landed: true, LandedAs: "b7d21f0"},
	}
	for name, r := range cases {
		if _, err := Encode(r); err == nil {
			t.Errorf("%s: Encode accepted %+v", name, r)
		}
	}
}
