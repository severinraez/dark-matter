package cli

import (
	"fmt"
	"io"
	"strings"

	"meltcloud.io/dm/internal/app"
	"meltcloud.io/dm/internal/core/record"
	"meltcloud.io/dm/internal/core/resolve"
	"meltcloud.io/dm/internal/core/view"
)

// Rendering (§4.3): one block per command in input order, delimited by
// printable sentinel glyphs at line start, content raw. cli renders view's
// read-model; it decides nothing. Paths always print in the canonical
// percent-encoding, so any path copied from output is valid input (§4.1).

const (
	glyphNext = "▸" // next-marker: prefixes each command echo
	glyphEnd  = "◾" // end-marker: prefixes the terminal footer
	glyphErr  = "✗" // per-command failure line
	glyphOK   = "✓" // write acknowledgment line
)

// renderBatch writes every block and the `◾N ok[, M error]` footer (§4.4).
func renderBatch(w io.Writer, res app.BatchResult) {
	for _, b := range res.Blocks {
		fmt.Fprintf(w, "%s%s\n", glyphNext, b.Echo)
		switch {
		case b.Err != "":
			fmt.Fprintf(w, "%s %s\n", glyphErr, b.Err)
		case b.Created != nil:
			fmt.Fprintf(w, "+ #%s created\n", b.Created.Handle)
		case b.Ack != nil:
			renderAck(w, *b.Ack)
		case b.Macro != nil:
			for _, it := range b.Macro {
				if it.Err != "" {
					fmt.Fprintf(w, "%s %s\n", glyphErr, it.Err)
				} else {
					renderAck(w, it.Ack)
				}
			}
		case b.Surface != nil:
			renderSurface(w, b.Surface)
		case b.Expansion != nil:
			renderExpansion(w, b.Expansion)
		}
	}
	if res.Failed > 0 {
		fmt.Fprintf(w, "%s%d ok, %d error\n", glyphEnd, res.OK, res.Failed)
	} else {
		fmt.Fprintf(w, "%s%d ok\n", glyphEnd, res.OK)
	}
}

// renderAck writes one `✓` line (§4.3).
func renderAck(w io.Writer, a view.Ack) {
	switch a.Verb {
	case view.AckSuperseded:
		fmt.Fprintf(w, "%s #%s superseded (rev %d)\n", glyphOK, a.Handle, a.Rev)
	case view.AckFeedback:
		fmt.Fprintf(w, "%s #%s feedback %s\n", glyphOK, a.Handle, a.Sig)
	case view.AckLinked, view.AckUnlinked:
		fmt.Fprintf(w, "%s #%s → #%s %s\n", glyphOK, a.Handle, a.To, a.Verb)
	case view.AckVdLanded:
		origins := "origins"
		if a.Count == 1 {
			origins = "origin"
		}
		fmt.Fprintf(w, "%s vd landed %s · %d %s bound\n", glyphOK, a.Subject, a.Count, origins)
	case view.AckVdUnlanded:
		fmt.Fprintf(w, "%s vd unlanded %s\n", glyphOK, a.Subject)
	default: // tombstoned · re-anchored · moved
		fmt.Fprintf(w, "%s #%s %s\n", glyphOK, a.Handle, a.Verb)
	}
}

// flagSuffix renders an entry's ⚠-flags, leading space included.
func flagSuffix(flags []view.Flag) string {
	var b strings.Builder
	for _, f := range flags {
		b.WriteString(" ⚠")
		b.WriteString(string(f))
	}
	return b.String()
}

// renderSurface writes own-entry previews, the collapsed parent line, and
// the inventory footer (§5.1): every collapsed item carries its size, and
// the context line is the "how much more is there" signal.
func renderSurface(w io.Writer, s *view.Surface) {
	for _, p := range s.Own {
		fmt.Fprintf(w, "%s #%s %s", p.Subj, p.Handle, p.FirstLine)
		if p.MoreLines > 0 {
			fmt.Fprintf(w, " (+%d lines)", p.MoreLines)
		}
		fmt.Fprint(w, flagSuffix(p.Flags))
		fmt.Fprintln(w)
	}
	if len(s.Parents) > 0 {
		notes, arch := "notes", 0
		var called []string
		for _, p := range s.Parents {
			if p.Subj == record.SubjArch {
				arch++
				called = append(called, fmt.Sprintf("#%s %s%s", p.Handle, record.EncodePath(p.Path), flagSuffix(p.Flags)))
			}
		}
		if len(s.Parents) == 1 {
			notes = "note"
		}
		fmt.Fprintf(w, "↑ %d parent %s", len(s.Parents), notes)
		if arch > 0 {
			fmt.Fprintf(w, " (%d arch) %s", arch, strings.Join(called, " · "))
		}
		fmt.Fprintln(w)
	}
	links := "links"
	if s.Links == 1 {
		links = "link"
	}
	fmt.Fprintf(w, "context: %d own · %d parent · %d %s", len(s.Own), len(s.Parents), s.Links, links)
	if s.Hidden > 0 {
		fmt.Fprintf(w, " · ~%d hidden", s.Hidden)
	}
	fmt.Fprintln(w)
}

// voteLine renders the §9.3 follow evidence — the three heuristic checks,
// shown instead of re-derived: candidates as `path votes/paired`, coverage,
// and the absorbed foreign fraction.
func voteLine(v *resolve.Vote) string {
	var parts []string
	for _, c := range v.Candidates {
		parts = append(parts, fmt.Sprintf("%s %d/%d", record.EncodePath(c.Path), c.Score, v.Paired))
	}
	if len(parts) == 0 {
		parts = append(parts, "no candidate")
	}
	line := "→ " + strings.Join(parts, " · ") + fmt.Sprintf(" · cov %d%%", v.CoveragePct)
	if v.ForeignPct > 0 {
		line += fmt.Sprintf(" · target %d%% foreign", v.ForeignPct)
	}
	return line
}

// runnersLine renders a file note's competing candidates (ambiguity
// policy, §9.1): dm's pick leads, scores attached.
func runnersLine(cands []resolve.Candidate) string {
	parts := make([]string, len(cands))
	for i, c := range cands {
		parts[i] = fmt.Sprintf("%s %d%%", record.EncodePath(c.Path), c.Score)
	}
	return "→ " + strings.Join(parts, " · ")
}

// renderExpansion writes an `r:#handle` block (§5.3): header with flags,
// full body raw, follow evidence when inference ran, then links one level
// and relations — the §4.3 structure glyphs.
func renderExpansion(w io.Writer, e *view.Expansion) {
	fmt.Fprintf(w, "%s [%s] #%s%s\n", record.EncodePath(e.Node), e.Subj, e.Handle, flagSuffix(e.Flags))
	fmt.Fprintln(w, e.Body)
	if e.Vote != nil {
		fmt.Fprintln(w, voteLine(e.Vote))
	}
	if len(e.Links) > 0 {
		parts := make([]string, len(e.Links))
		for i, ln := range e.Links {
			parts[i] = "#" + string(ln.Handle)
			if ln.Comment != "" {
				parts[i] += " " + ln.Comment
			}
		}
		fmt.Fprintf(w, "→ links: %s\n", strings.Join(parts, " · "))
	}
	if len(e.LinkedFrom) > 0 {
		parts := make([]string, len(e.LinkedFrom))
		for i, p := range e.LinkedFrom {
			parts[i] = record.EncodePath(p)
		}
		fmt.Fprintf(w, "← linked-from: %s\n", strings.Join(parts, " · "))
	}
}

// renderWorklist writes the hygiene report (§9.6): one line per entry
// wanting judgment, evidence attached; one line per abandoned line group
// (`tip · branch hint · note count · matcher hint` — §9.4, one vd repairs
// the group); and a count footer. Golden format pinned by tests.
func renderWorklist(w io.Writer, r *view.WorklistReport) {
	for _, it := range r.Items {
		fmt.Fprintf(w, "#%s %s %s", it.Handle, it.Subj, record.EncodePath(it.Path))
		for _, reason := range it.Reasons {
			switch reason {
			case "split", "disputed":
				fmt.Fprintf(w, " ⚠%s", reason)
			default:
				fmt.Fprintf(w, " %s", reason)
			}
		}
		switch {
		case it.Vote != nil:
			fmt.Fprintf(w, " %s", voteLine(it.Vote))
		case len(it.Runners) > 0:
			fmt.Fprintf(w, " %s", runnersLine(it.Runners))
		}
		fmt.Fprintln(w)
	}
	for _, g := range r.Groups {
		notes := "notes"
		if len(g.Handles) == 1 {
			notes = "note"
		}
		handles := make([]string, len(g.Handles))
		for i, h := range g.Handles {
			handles[i] = "#" + string(h)
		}
		fmt.Fprintf(w, "line %s · no ref · %d %s (%s) · %s · vd:%s:landed:<sha> repairs\n",
			g.Tip, len(g.Handles), notes, strings.Join(handles, " "), g.Hint, g.Tip)
	}
	total := len(r.Items) + len(r.Groups)
	if total == 0 {
		fmt.Fprintln(w, "worklist clean")
		return
	}
	fmt.Fprintf(w, "%d to review\n", total)
}
