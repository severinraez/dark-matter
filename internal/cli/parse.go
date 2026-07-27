package cli

import (
	"fmt"
	"strconv"
	"strings"

	"meltcloud.io/dm/internal/app"
	"meltcloud.io/dm/internal/core/record"
)

// Phase 1 of the two-phase contract (§4.1): parse the entire input; any
// syntax error rejects the whole batch and nothing executes. M2 carries the
// walking-skeleton grammar — `a` and `r`; the remaining §4.2 verbs and `$N`
// arrive with M3.
//
// Grammar mechanics pinned here for good:
//   - one command per line, `cmd:...`; fields split left-to-right on raw
//     `:`, final field greedy;
//   - a physical line's trailing run of n backslashes decodes to n/2
//     literal backslashes, an odd run continues the command onto the next
//     line (the encoding of a body newline) — the same framing as the
//     record codec;
//   - path-valued fields percent-decode strictly (core/record owns the
//     canon); bodies are never decoded and validate in phase 2.

// ParseError is a batch-rejecting syntax error, positioned at the first
// physical line of the offending command.
type ParseError struct {
	Line int
	Msg  string
}

func (e *ParseError) Error() string {
	return fmt.Sprintf("parse error line %d: %s", e.Line, e.Msg)
}

// escapeHint teaches the two path escapes exactly when a forgotten one
// fails loudly (§4.1).
const escapeHint = ` (":" in a path? write %3A · literal "%"? write %25)`

// ParseBatch splits input into commands and parses each; the first syntax
// error rejects everything.
func ParseBatch(input string) ([]app.Command, *ParseError) {
	lines := strings.Split(input, "\n")
	// The final newline is the last line's terminator, not a line of its
	// own — drop the artifact so a trailing `\` before EOF is caught as
	// mid-continuation instead of swallowing it.
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	var cmds []app.Command
	i := 0
	for i < len(lines) {
		if lines[i] == "" {
			i++
			continue
		}
		start := i
		var logical, raw strings.Builder
		for {
			ln := lines[i]
			n := trailingBackslashes(ln)
			logical.WriteString(ln[:len(ln)-n])
			for j := 0; j < n/2; j++ {
				logical.WriteByte('\\')
			}
			raw.WriteString(ln)
			i++
			if n%2 == 0 {
				break
			}
			if i >= len(lines) {
				return nil, &ParseError{start + 1, "batch ends mid-continuation (trailing \\ expects another line)"}
			}
			logical.WriteByte('\n')
			raw.WriteByte('\n')
		}
		cmd, perr := parseCommand(logical.String(), raw.String(), start+1)
		if perr != nil {
			return nil, perr
		}
		cmds = append(cmds, cmd)
	}
	return cmds, nil
}

func parseCommand(logical, raw string, line int) (app.Command, *ParseError) {
	name, rest, ok := strings.Cut(logical, ":")
	if !ok {
		return nil, &ParseError{line, fmt.Sprintf("expected cmd:... form, got %q", logical)}
	}
	switch name {
	case "a":
		return parseCreate(rest, raw, line)
	case "r":
		return parseRead(rest, raw, line)
	case "s", "u", "d", "f", "k", "mv", "rm", "vd", "al", "dl":
		return nil, &ParseError{line, fmt.Sprintf("command %q not implemented yet", name)}
	default:
		return nil, &ParseError{line, fmt.Sprintf("unknown command %q", name)}
	}
}

// parseCreate parses `a:path:subj:body` (§4.2). The body is the final
// field, greedy: `:` inside it needs no escaping. Body bytes validate in
// phase 2 (§4.3 write-time rejections), not here.
func parseCreate(rest, raw string, line int) (app.Command, *ParseError) {
	f := strings.SplitN(rest, ":", 3)
	if len(f) != 3 {
		return nil, &ParseError{line, "expected a:path:subj:body"}
	}
	path, perr := parsePathField(f[0], line)
	if perr != nil {
		return nil, perr
	}
	subj := record.Subject(f[1])
	if !subj.Valid() {
		return nil, &ParseError{line, "expected subject (c|a|d|o) after path"}
	}
	if f[2] == "" {
		return nil, &ParseError{line, "empty body"}
	}
	return app.CmdCreate{RawText: raw, Path: path, Subj: subj, Body: f[2]}, nil
}

// parseRead parses `r:path`, `r:path:N`, `r:#handle` (§4.2, §5.3, §5.4).
// After the mandatory path a raw colon can only be a field separator, so
// `r:foo:1` is a depth-1 read of foo and the file `foo:1` is `r:foo%3A1`.
func parseRead(rest, raw string, line int) (app.Command, *ParseError) {
	if rest == "" {
		return nil, &ParseError{line, "expected r:path or r:#handle"}
	}
	if h, ok := strings.CutPrefix(rest, "#"); ok {
		if h == "" || strings.Contains(h, ":") {
			return nil, &ParseError{line, "expected r:#handle"}
		}
		return app.CmdRead{RawText: raw, Handle: record.Handle(h)}, nil
	}
	f := strings.Split(rest, ":")
	if len(f) > 2 {
		return nil, &ParseError{line, "expected r:path or r:path:depth" + escapeHint}
	}
	path, perr := parsePathField(f[0], line)
	if perr != nil {
		return nil, perr
	}
	depth := 0
	if len(f) == 2 {
		n, err := strconv.Atoi(f[1])
		if err != nil || n < 0 {
			return nil, &ParseError{line, fmt.Sprintf("expected numeric depth, got %q", f[1]) + escapeHint}
		}
		depth = n
	}
	return app.CmdRead{RawText: raw, Path: path, Depth: depth}, nil
}

// parsePathField percent-decodes a path-valued field, strictly (§4.1): a
// `%` not followed by two hex digits rejects the batch with the hint.
func parsePathField(field string, line int) (record.Path, *ParseError) {
	if field == "" {
		return "", &ParseError{line, "empty path"}
	}
	p, err := record.DecodePath(field)
	if err != nil {
		return "", &ParseError{line, err.Error() + escapeHint}
	}
	return p, nil
}

func trailingBackslashes(s string) int {
	n := 0
	for n < len(s) && s[len(s)-1-n] == '\\' {
		n++
	}
	return n
}
