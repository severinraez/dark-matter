package cli

import (
	"fmt"
	"strconv"
	"strings"

	"meltcloud.io/dm/internal/app"
	"meltcloud.io/dm/internal/core/record"
)

// Phase 1 of the two-phase contract (§4.1): parse the entire input; any
// syntax error rejects the whole batch and nothing executes. M3 carries
// the full CRUD grammar; `s` (M7) and `vd` (M6) arrive with their
// milestones.
//
// Grammar mechanics pinned here for good:
//   - one command per line, `cmd:...`; fields split left-to-right on raw
//     `:`, final field greedy;
//   - a physical line's trailing run of n backslashes decodes to n/2
//     literal backslashes, an odd run continues the command onto the next
//     line (the encoding of a body newline) — the same framing as the
//     record codec;
//   - path-valued fields percent-decode strictly (core/record owns the
//     canon); bodies are never decoded and validate in phase 2;
//   - handle-valued fields accept `$N`, the entry created by the batch's
//     Nth command — pure positional syntax, so a forward, out-of-range, or
//     non-create reference rejects the batch here (§4.2); resolving a
//     literal `#handle` is semantic and stays phase 2 (architecture.md §7).

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
	var creates []bool // per parsed command: was it an `a`? ($N targets)
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
		cmd, perr := parseCommand(logical.String(), raw.String(), start+1, creates)
		if perr != nil {
			return nil, perr
		}
		cmds = append(cmds, cmd)
		_, isCreate := cmd.(app.CmdCreate)
		creates = append(creates, isCreate)
	}
	return cmds, nil
}

func parseCommand(logical, raw string, line int, creates []bool) (app.Command, *ParseError) {
	name, rest, ok := strings.Cut(logical, ":")
	if !ok {
		return nil, &ParseError{line, fmt.Sprintf("expected cmd:... form, got %q", logical)}
	}
	switch name {
	case "a":
		return parseCreate(rest, raw, line)
	case "r":
		return parseRead(rest, raw, line, creates)
	case "u":
		return parseSupersede(rest, raw, line, creates)
	case "d":
		return parseTombstone(rest, raw, line, creates)
	case "k":
		return parseReAnchor(rest, raw, line, creates)
	case "f":
		return parseFeedback(rest, raw, line, creates)
	case "al":
		return parseLink(rest, raw, line, creates)
	case "dl":
		return parseUnlink(rest, raw, line, creates)
	case "mv":
		return parseMove(rest, raw, line)
	case "rm":
		return parseRemove(rest, raw, line)
	case "vd":
		return parseVerdict(rest, raw, line)
	case "s":
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

// parseRead parses `r:path`, `r:path:N`, `r:#handle`/`r:$N` (§4.2, §5.3,
// §5.4). After the mandatory path a raw colon can only be a field
// separator, so `r:foo:1` is a depth-1 read of foo and the file `foo:1` is
// `r:foo%3A1`.
func parseRead(rest, raw string, line int, creates []bool) (app.Command, *ParseError) {
	if rest == "" {
		return nil, &ParseError{line, "expected r:path or r:#handle"}
	}
	if isRefField(rest) {
		if strings.Contains(rest, ":") {
			return nil, &ParseError{line, "expected r:#handle"}
		}
		ref, perr := parseRef(rest, line, creates)
		if perr != nil {
			return nil, perr
		}
		return app.CmdRead{RawText: raw, Target: ref}, nil
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

// parseSupersede parses `u:#handle:body` (§4.2); body greedy as always.
func parseSupersede(rest, raw string, line int, creates []bool) (app.Command, *ParseError) {
	refField, body, ok := strings.Cut(rest, ":")
	if !ok {
		return nil, &ParseError{line, "expected u:#handle:body"}
	}
	ref, perr := parseRef(refField, line, creates)
	if perr != nil {
		return nil, perr
	}
	if body == "" {
		return nil, &ParseError{line, "empty body"}
	}
	return app.CmdSupersede{RawText: raw, Target: ref, Body: body}, nil
}

// parseTombstone parses `d:#handle` (§4.2).
func parseTombstone(rest, raw string, line int, creates []bool) (app.Command, *ParseError) {
	if strings.Contains(rest, ":") {
		return nil, &ParseError{line, "expected d:#handle"}
	}
	ref, perr := parseRef(rest, line, creates)
	if perr != nil {
		return nil, perr
	}
	return app.CmdTombstone{RawText: raw, Target: ref}, nil
}

// parseReAnchor parses `k:#handle[:path]` (§4.2, §9.3). The path is the
// final field, greedy, so a raw `:` in it needs no escaping — it still
// percent-decodes like every path field (§4.1).
func parseReAnchor(rest, raw string, line int, creates []bool) (app.Command, *ParseError) {
	refField, pathField, hasPath := strings.Cut(rest, ":")
	ref, perr := parseRef(refField, line, creates)
	if perr != nil {
		return nil, perr
	}
	var path record.Path
	if hasPath {
		path, perr = parsePathField(pathField, line)
		if perr != nil {
			return nil, perr
		}
	}
	return app.CmdReAnchor{RawText: raw, Target: ref, Path: path}, nil
}

// parseFeedback parses `f:#handle:sig[:reason]` (§7.3); the reason is
// free text, final, greedy.
func parseFeedback(rest, raw string, line int, creates []bool) (app.Command, *ParseError) {
	f := strings.SplitN(rest, ":", 3)
	if len(f) < 2 {
		return nil, &ParseError{line, "expected f:#handle:sig[:reason]"}
	}
	ref, perr := parseRef(f[0], line, creates)
	if perr != nil {
		return nil, perr
	}
	sig := record.Sig(f[1])
	if !sig.Valid() {
		return nil, &ParseError{line, "expected signal (+|-|!) after handle"}
	}
	var reason string
	if len(f) == 3 {
		reason = f[2]
		if reason == "" {
			return nil, &ParseError{line, "empty reason"}
		}
	}
	return app.CmdFeedback{RawText: raw, Target: ref, Sig: sig, Reason: reason}, nil
}

// parseLink parses `al:#a:#b[:note]` (§6.4); the note is free text,
// final, greedy.
func parseLink(rest, raw string, line int, creates []bool) (app.Command, *ParseError) {
	f := strings.SplitN(rest, ":", 3)
	if len(f) < 2 {
		return nil, &ParseError{line, "expected al:#a:#b[:note]"}
	}
	from, perr := parseRef(f[0], line, creates)
	if perr != nil {
		return nil, perr
	}
	to, perr := parseRef(f[1], line, creates)
	if perr != nil {
		return nil, perr
	}
	var comment string
	if len(f) == 3 {
		comment = f[2]
		if comment == "" {
			return nil, &ParseError{line, "empty note"}
		}
	}
	return app.CmdLink{RawText: raw, From: from, To: to, Comment: comment}, nil
}

// parseUnlink parses `dl:#a:#b` (§6.4).
func parseUnlink(rest, raw string, line int, creates []bool) (app.Command, *ParseError) {
	f := strings.Split(rest, ":")
	if len(f) != 2 {
		return nil, &ParseError{line, "expected dl:#a:#b"}
	}
	from, perr := parseRef(f[0], line, creates)
	if perr != nil {
		return nil, perr
	}
	to, perr := parseRef(f[1], line, creates)
	if perr != nil {
		return nil, perr
	}
	return app.CmdUnlink{RawText: raw, From: from, To: to}, nil
}

// parseMove parses `mv:old:new` (§6.5). new is the final path field,
// greedy; both are percent-decoded; folder moves need both sides to be
// folders (trailing slash), file moves neither.
func parseMove(rest, raw string, line int) (app.Command, *ParseError) {
	oldField, newField, ok := strings.Cut(rest, ":")
	if !ok {
		return nil, &ParseError{line, "expected mv:old:new"}
	}
	oldPath, perr := parsePathField(oldField, line)
	if perr != nil {
		return nil, perr
	}
	newPath, perr := parsePathField(newField, line)
	if perr != nil {
		return nil, perr
	}
	if oldPath.IsFolder() != newPath.IsFolder() {
		return nil, &ParseError{line, "mv paths must both be files or both folders (trailing /)"}
	}
	return app.CmdMove{RawText: raw, Old: oldPath, New: newPath}, nil
}

// parseRemove parses `rm:path` (§6.5); the path is final, greedy.
func parseRemove(rest, raw string, line int) (app.Command, *ParseError) {
	path, perr := parsePathField(rest, line)
	if perr != nil {
		return nil, perr
	}
	return app.CmdRemove{RawText: raw, Path: path}, nil
}

// parseVerdict parses `vd:sha1:landed:sha2` | `vd:sha1:unlanded` (§4.2,
// §9.4). SHAs are shape-checked here (lowercase hex); resolving them
// against the repository is semantic and stays phase 2.
func parseVerdict(rest, raw string, line int) (app.Command, *ParseError) {
	f := strings.Split(rest, ":")
	if len(f) < 2 || len(f) > 3 {
		return nil, &ParseError{line, "expected vd:sha1:landed:sha2 or vd:sha1:unlanded"}
	}
	subject := record.SHA(f[0])
	if !subject.Valid() {
		return nil, &ParseError{line, fmt.Sprintf("expected a commit sha, got %q", f[0])}
	}
	switch f[1] {
	case "unlanded":
		if len(f) != 2 {
			return nil, &ParseError{line, "vd:sha1:unlanded takes no further fields"}
		}
		return app.CmdVerdict{RawText: raw, Subject: subject}, nil
	case "landed":
		if len(f) != 3 {
			return nil, &ParseError{line, "expected vd:sha1:landed:sha2"}
		}
		landedAs := record.SHA(f[2])
		if !landedAs.Valid() {
			return nil, &ParseError{line, fmt.Sprintf("expected a commit sha, got %q", f[2])}
		}
		return app.CmdVerdict{RawText: raw, Subject: subject, Landed: true, LandedAs: landedAs}, nil
	default:
		return nil, &ParseError{line, "expected landed or unlanded after the sha"}
	}
}

// isRefField reports a handle-shaped field (`#…` or `$…`).
func isRefField(field string) bool {
	return strings.HasPrefix(field, "#") || strings.HasPrefix(field, "$")
}

// parseRef parses a handle-valued field: `#handle`, or the `$N` same-batch
// backreference (§4.2). `$N` is pure positional syntax, validated here —
// it must point to an earlier command that is an `a`; violations reject
// the whole batch.
func parseRef(field string, line int, creates []bool) (app.Ref, *ParseError) {
	if h, ok := strings.CutPrefix(field, "#"); ok {
		if h == "" {
			return app.Ref{}, &ParseError{line, "empty handle after #"}
		}
		return app.Ref{Handle: record.Handle(h)}, nil
	}
	if nField, ok := strings.CutPrefix(field, "$"); ok {
		n, err := strconv.Atoi(nField)
		if err != nil || n < 1 {
			return app.Ref{}, &ParseError{line, fmt.Sprintf("expected $N with N ≥ 1, got %q", field)}
		}
		if n > len(creates) {
			return app.Ref{}, &ParseError{line, fmt.Sprintf("$%d must reference an earlier command", n)}
		}
		if !creates[n-1] {
			return app.Ref{}, &ParseError{line, fmt.Sprintf("$%d does not reference an a command", n)}
		}
		return app.Ref{Backref: n}, nil
	}
	return app.Ref{}, &ParseError{line, fmt.Sprintf("expected #handle or $N, got %q", field)}
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
