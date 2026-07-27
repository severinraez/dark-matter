package record

import (
	"fmt"
	"strings"
)

// The two output-framing glyphs (§4.3); rejected in bodies so a body line
// can never be mistaken for a block boundary.
const (
	glyphNext = "▸" // U+25B8, starts each command block
	glyphEnd  = "◾" // U+25FE, terminal footer
)

// ValidateBody enforces the §4.3 body-byte rules on any trailing free-text
// field (bodies, feedback reasons, link comments): no framing glyphs, no C0
// control bytes except tab and newline. Tab is ordinary text (Makefiles,
// gofmt); newline is the decoded form of the trailing-`\` continuation;
// the channel-hostile bytes (NUL, escape, RS/GS, CR, …) stay banned.
func ValidateBody(body string) error {
	if strings.Contains(body, glyphNext) || strings.Contains(body, glyphEnd) {
		return fmt.Errorf("body contains a reserved framing glyph (%s or %s)", glyphNext, glyphEnd)
	}
	for i := 0; i < len(body); i++ {
		if c := body[i]; c < 0x20 && c != '\t' && c != '\n' {
			return fmt.Errorf("body contains control byte 0x%02X at offset %d (only tab is allowed; write a newline as a trailing \\)", c, i)
		}
	}
	return nil
}
