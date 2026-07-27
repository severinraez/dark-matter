package cli

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"meltcloud.io/dm/internal/app"
)

func TestParseBatch(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []app.Command
	}{
		{
			name:  "create and read",
			input: "a:api/handler.rb:c:Validates tenant header before dispatch\nr:api/handler.rb\n",
			want: []app.Command{
				app.CmdCreate{
					RawText: "a:api/handler.rb:c:Validates tenant header before dispatch",
					Path:    "api/handler.rb", Subj: "c",
					Body: "Validates tenant header before dispatch",
				},
				app.CmdRead{RawText: "r:api/handler.rb", Path: "api/handler.rb"},
			},
		},
		{
			name:  "body keeps raw colons",
			input: "a:x.rb:d:make test TARGET=y:z\n",
			want: []app.Command{app.CmdCreate{
				RawText: "a:x.rb:d:make test TARGET=y:z",
				Path:    "x.rb", Subj: "d", Body: "make test TARGET=y:z",
			}},
		},
		{
			name:  "trailing backslash continues the body across lines",
			input: "a:x.rb:c:first\\\nsecond\n",
			want: []app.Command{app.CmdCreate{
				RawText: "a:x.rb:c:first\\\nsecond",
				Path:    "x.rb", Subj: "c", Body: "first\nsecond",
			}},
		},
		{
			name:  "doubled backslash ends the command with a literal backslash",
			input: "a:x.rb:c:ends in backslash\\\\\nr:x.rb\n",
			want: []app.Command{
				app.CmdCreate{
					RawText: "a:x.rb:c:ends in backslash\\\\",
					Path:    "x.rb", Subj: "c", Body: `ends in backslash\`,
				},
				app.CmdRead{RawText: "r:x.rb", Path: "x.rb"},
			},
		},
		{
			name:  "search with AND binding tighter than OR",
			input: "s:db/schema.rb:tenant+scope|isolation\n",
			want: []app.Command{app.CmdSearch{
				RawText: "s:db/schema.rb:tenant+scope|isolation",
				Path:    "db/schema.rb",
				Terms:   [][]string{{"tenant", "scope"}, {"isolation"}},
			}},
		},
		{
			name:  "search terms are never percent-decoded",
			input: "s:x.rb:50%off\n",
			want: []app.Command{app.CmdSearch{
				RawText: "s:x.rb:50%off",
				Path:    "x.rb",
				Terms:   [][]string{{"50%off"}},
			}},
		},
		{
			name:  "percent-decoded path",
			input: "r:foo%3A1\n",
			want:  []app.Command{app.CmdRead{RawText: "r:foo%3A1", Path: "foo:1"}},
		},
		{
			name:  "depth read",
			input: "r:foo:1\n",
			want:  []app.Command{app.CmdRead{RawText: "r:foo:1", Path: "foo", Depth: 1}},
		},
		{
			name:  "handle read",
			input: "r:#a3f9c1\n",
			want:  []app.Command{app.CmdRead{RawText: "r:#a3f9c1", Target: app.Ref{Handle: "a3f9c1"}}},
		},
		{
			name:  "blank lines are skipped",
			input: "\nr:foo\n\n",
			want:  []app.Command{app.CmdRead{RawText: "r:foo", Path: "foo"}},
		},
		{
			name:  "supersede with greedy body",
			input: "u:#a3f9c1:new body: with colons\n",
			want: []app.Command{app.CmdSupersede{
				RawText: "u:#a3f9c1:new body: with colons",
				Target:  app.Ref{Handle: "a3f9c1"}, Body: "new body: with colons",
			}},
		},
		{
			name:  "tombstone",
			input: "d:#a3f9c1\n",
			want:  []app.Command{app.CmdTombstone{RawText: "d:#a3f9c1", Target: app.Ref{Handle: "a3f9c1"}}},
		},
		{
			name:  "keep in place and keep with explicit raw-colon path",
			input: "k:#a3f9c1\nk:#a3f9c1:web/foo:1\n",
			want: []app.Command{
				app.CmdReAnchor{RawText: "k:#a3f9c1", Target: app.Ref{Handle: "a3f9c1"}},
				// The k path is the final field: a raw colon survives (§9.3).
				app.CmdReAnchor{RawText: "k:#a3f9c1:web/foo:1", Target: app.Ref{Handle: "a3f9c1"}, Path: "web/foo:1"},
			},
		},
		{
			name:  "feedback with and without reason",
			input: "f:#a3f9c1:!:repo layer removed in v3\nf:#a3f9c1:+\n",
			want: []app.Command{
				app.CmdFeedback{RawText: "f:#a3f9c1:!:repo layer removed in v3",
					Target: app.Ref{Handle: "a3f9c1"}, Sig: "!", Reason: "repo layer removed in v3"},
				app.CmdFeedback{RawText: "f:#a3f9c1:+", Target: app.Ref{Handle: "a3f9c1"}, Sig: "+"},
			},
		},
		{
			name:  "link and unlink",
			input: "al:#aaaaaa:#bbbbbb:schema STI\ndl:#aaaaaa:#bbbbbb\n",
			want: []app.Command{
				app.CmdLink{RawText: "al:#aaaaaa:#bbbbbb:schema STI",
					From: app.Ref{Handle: "aaaaaa"}, To: app.Ref{Handle: "bbbbbb"}, Comment: "schema STI"},
				app.CmdUnlink{RawText: "dl:#aaaaaa:#bbbbbb",
					From: app.Ref{Handle: "aaaaaa"}, To: app.Ref{Handle: "bbbbbb"}},
			},
		},
		{
			name:  "move and remove",
			input: "mv:api/old.rb:api/new.rb\nmv:api/:svc/\nrm:api/\n",
			want: []app.Command{
				app.CmdMove{RawText: "mv:api/old.rb:api/new.rb", Old: "api/old.rb", New: "api/new.rb"},
				app.CmdMove{RawText: "mv:api/:svc/", Old: "api/", New: "svc/"},
				app.CmdRemove{RawText: "rm:api/", Path: "api/"},
			},
		},
		{
			name:  "$N backrefs anywhere a handle goes",
			input: "a:x.rb:c:one\na:y.rb:c:two\nal:$1:$2:related\nu:$1:revised\nr:$2\n",
			want: []app.Command{
				app.CmdCreate{RawText: "a:x.rb:c:one", Path: "x.rb", Subj: "c", Body: "one"},
				app.CmdCreate{RawText: "a:y.rb:c:two", Path: "y.rb", Subj: "c", Body: "two"},
				app.CmdLink{RawText: "al:$1:$2:related",
					From: app.Ref{Backref: 1}, To: app.Ref{Backref: 2}, Comment: "related"},
				app.CmdSupersede{RawText: "u:$1:revised", Target: app.Ref{Backref: 1}, Body: "revised"},
				app.CmdRead{RawText: "r:$2", Target: app.Ref{Backref: 2}},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, perr := ParseBatch(tt.input)
			if perr != nil {
				t.Fatalf("ParseBatch: %v", perr)
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("commands mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestParseBatchRejects(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		line    int
		wantMsg string // substring
	}{
		{"missing fields", "a:foo\n", 1, "expected a:path:subj:body"},
		{"bad subject", "r:ok\na:foo:x:body\n", 2, "expected subject (c|a|d|o) after path"},
		{"empty body", "a:foo:c:\n", 1, "empty body"},
		{"unknown command", "q:foo\n", 1, `unknown command "q"`},
		{"search missing terms", "s:foo\n", 1, "expected s:path:t1|t2"},
		{"search empty term", "s:foo:a||b\n", 1, "empty search term"},
		{"search empty and-term", "s:foo:a+\n", 1, "empty search term"},
		{"bare percent gets the hint", "r:50%off\n", 1, `write %25`},
		{"non-numeric depth", "r:foo:bar\n", 1, "expected numeric depth"},
		{"too many read fields", "r:a:b:c\n", 1, "expected r:path or r:path:depth"},
		{"ends mid-continuation", "a:x.rb:c:body\\\n", 1, "mid-continuation"},
		{"missing colon", "sync\n", 1, "expected cmd:... form"},
		{"empty supersede body", "u:#a3f9c1:\n", 1, "empty body"},
		{"tombstone rejects extra field", "d:#a3f9c1:x\n", 1, "expected d:#handle"},
		{"bad feedback signal", "f:#a3f9c1:?\n", 1, "expected signal (+|-|!)"},
		{"empty feedback reason", "f:#a3f9c1:+:\n", 1, "empty reason"},
		{"unlink wants exactly two handles", "dl:#aaaaaa\n", 1, "expected dl:#a:#b"},
		{"bad ref field", "d:a3f9c1\n", 1, "expected #handle or $N"},
		{"mv slash mismatch", "mv:api/:svc\n", 1, "must both be files or both folders"},
		// $N violations (§11.4 two-phase): forward, out-of-range,
		// non-create — all reject at parse.
		{"backref forward", "u:$1:body\na:x.rb:c:note\n", 1, "$1 must reference an earlier command"},
		{"backref out of range", "a:x.rb:c:note\nd:$9\n", 2, "$9 must reference an earlier command"},
		{"backref self", "a:x.rb:c:note\nu:$2:body\n", 2, "$2 must reference an earlier command"},
		{"backref non-create", "a:x.rb:c:note\nr:x.rb\nd:$2\n", 3, "$2 does not reference an a command"},
		{"backref zero", "a:x.rb:c:note\nd:$0\n", 2, "expected $N with N ≥ 1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmds, perr := ParseBatch(tt.input)
			if perr == nil {
				t.Fatalf("ParseBatch accepted %q: %#v", tt.input, cmds)
			}
			if perr.Line != tt.line {
				t.Errorf("error line = %d, want %d (%v)", perr.Line, tt.line, perr)
			}
			if !strings.Contains(perr.Msg, tt.wantMsg) {
				t.Errorf("error %q does not contain %q", perr.Msg, tt.wantMsg)
			}
		})
	}
}
