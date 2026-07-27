package cli

import (
	"fmt"
	"io"
	"strings"

	"meltcloud.io/dm/internal/app"
	"meltcloud.io/dm/internal/core/record"
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
	default: // tombstoned · re-anchored · moved
		fmt.Fprintf(w, "%s #%s %s\n", glyphOK, a.Handle, a.Verb)
	}
}

// renderSurface writes own-entry previews and the inventory footer (§5.1):
// every collapsed item carries its size, and the context line is the "how
// much more is there" signal.
func renderSurface(w io.Writer, s *view.Surface) {
	for _, p := range s.Own {
		fmt.Fprintf(w, "%s #%s %s", p.Subj, p.Handle, p.FirstLine)
		if p.MoreLines > 0 {
			fmt.Fprintf(w, " (+%d lines)", p.MoreLines)
		}
		fmt.Fprintln(w)
	}
	links := "links"
	if s.Links == 1 {
		links = "link"
	}
	fmt.Fprintf(w, "context: %d own · %d parent · %d %s", len(s.Own), s.Parents, s.Links, links)
	if s.Hidden > 0 {
		fmt.Fprintf(w, " · ~%d hidden", s.Hidden)
	}
	fmt.Fprintln(w)
}

// renderExpansion writes an `r:#handle` block (§5.3): header, full body
// raw, then links one level and relations — the §4.3 structure glyphs.
func renderExpansion(w io.Writer, e *view.Expansion) {
	fmt.Fprintf(w, "%s [%s] #%s\n", record.EncodePath(e.Node), e.Subj, e.Handle)
	fmt.Fprintln(w, e.Body)
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
