package record

import "testing"

func TestValidateBody(t *testing.T) {
	ok := []string{
		"plain text",
		"colons: and `backticks` and spaces need no escaping",
		"tab\tis ordinary text (Makefiles, gofmt)",
		"multi\nline\nbodies carry real newlines",
		"unicode → glyphs like ↑ ← ★ ⚠ are fine; only the framing pair is reserved",
	}
	for _, b := range ok {
		if err := ValidateBody(b); err != nil {
			t.Errorf("ValidateBody(%q) = %v", b, err)
		}
	}
	bad := []string{
		"nul\x00byte",
		"escape\x1bbyte",
		"rs\x1ebyte",
		"gs\x1dbyte",
		"cr\rbyte",
		"vertical\x0btab",
		"block marker ▸ inside",
		"footer marker ◾ inside",
	}
	for _, b := range bad {
		if err := ValidateBody(b); err == nil {
			t.Errorf("ValidateBody(%q): want error", b)
		}
	}
}
