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
			want:  []app.Command{app.CmdRead{RawText: "r:#a3f9c1", Handle: "a3f9c1"}},
		},
		{
			name:  "blank lines are skipped",
			input: "\nr:foo\n\n",
			want:  []app.Command{app.CmdRead{RawText: "r:foo", Path: "foo"}},
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
		{"not yet implemented verb", "u:#a3f9c1:body\n", 1, `command "u" not implemented yet`},
		{"bare percent gets the hint", "r:50%off\n", 1, `write %25`},
		{"non-numeric depth", "r:foo:bar\n", 1, "expected numeric depth"},
		{"too many read fields", "r:a:b:c\n", 1, "expected r:path or r:path:depth"},
		{"ends mid-continuation", "a:x.rb:c:body\\\n", 1, "mid-continuation"},
		{"missing colon", "sync\n", 1, "expected cmd:... form"},
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
