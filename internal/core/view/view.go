package view

import (
	"strings"

	"meltcloud.io/dm/internal/core/record"
	"meltcloud.io/dm/internal/core/resolve"
)

// The read-model app returns and cli renders (design.md §5.1, §4.3): the
// surface a node read returns, the expansion a handle read returns, the
// write acks, and the worklist report. Ranking beyond flag demotion,
// search, and the crowding threshold arrive with M7.

// Flag is a behavior-changing surface marker (§7.4) — rendered with the ⚠
// glyph by cli.
type Flag string

const (
	FlagStale       Flag = "stale"       // content drift (§9.5)
	FlagUnconfirmed Flag = "unconfirmed" // uncertain follow (§9.2/§9.3)
	FlagSplit       Flag = "split"       // ambiguous folder follow (§9.3)
	FlagDisputed    Flag = "disputed"    // FB ! newer than the last bless (§7.3)
)

// Flags assembles an entry's surface flags from its resolution state and
// dispute fold, in the pinned display order.
func Flags(state resolve.State, disputed bool) []Flag {
	var out []Flag
	switch state {
	case resolve.Stale:
		out = append(out, FlagStale)
	case resolve.Unconfirmed:
		out = append(out, FlagUnconfirmed)
	case resolve.Split:
		out = append(out, FlagSplit)
	}
	if disputed {
		out = append(out, FlagDisputed)
	}
	return out
}

// EntryPreview is one own-entry line of a surface: subject, handle, the
// body's first line, the hidden-size hint, and any flags (§5.1, §5.2).
type EntryPreview struct {
	Subj      record.Subject
	Handle    record.Handle
	FirstLine string
	MoreLines int // body lines beyond the first — the "(+N lines)" hint
	Flags     []Flag
}

// ParentPreview is one folder note covering the read node (§3.2's parent
// disclosure dimension): collapsed to handle + home + flags.
type ParentPreview struct {
	Subj   record.Subject
	Handle record.Handle
	Path   record.Path // the covering home (a split candidate when flagged split)
	Flags  []Flag
}

// Surface is the budgeted digest of one node (§5.1): own previews, parent
// notes collapsed, and the inventory footer counts.
type Surface struct {
	Node    record.Path
	Own     []EntryPreview
	Parents []ParentPreview
	Links   int
	Hidden  int
}

// Created is the `+ #handle created` ack (§4.3).
type Created struct {
	Handle record.Handle
}

// AckVerb names a `✓` write ack's kind (§4.3).
type AckVerb string

const (
	AckSuperseded AckVerb = "superseded"
	AckTombstoned AckVerb = "tombstoned"
	AckReAnchored AckVerb = "re-anchored"
	AckFeedback   AckVerb = "feedback"
	AckLinked     AckVerb = "linked"
	AckUnlinked   AckVerb = "unlinked"
	AckMoved      AckVerb = "moved"
)

// Ack is one `✓` write acknowledgment line. Handle is the acted-on entry;
// the other fields apply per verb: Rev for superseded, Sig for feedback,
// To for linked/unlinked (the ack echoes minted handles, so `$N` callers
// still learn the stable ones — §4.2).
type Ack struct {
	Verb   AckVerb
	Handle record.Handle
	To     record.Handle
	Rev    int
	Sig    record.Sig
}

// Expansion is the `r:#handle` result (§5.3): the full body of one entry
// plus its links one level, relations, flags, and — for inferred follows —
// the follow evidence the drill shows instead of re-deriving (§9.3).
type Expansion struct {
	Node       record.Path
	Subj       record.Subject
	Handle     record.Handle
	Flags      []Flag
	Body       string
	Vote       *resolve.Vote // folder follow evidence, when inference ran
	Links      []LinkPreview // outgoing, live endpoints only
	LinkedFrom []record.Path // nodes whose entries link here, deduped
}

// LinkPreview is one collapsed link line item: the target's handle and the
// link's optional comment.
type LinkPreview struct {
	Handle  record.Handle
	Comment string
}

// WorklistItem is one entry wanting agent judgment (§9.6), with the
// already-computed evidence its disposition needs.
type WorklistItem struct {
	Handle  record.Handle
	Subj    record.Subject
	Path    record.Path // the entry's folded home — where the debt sits
	Reasons []string    // orphaned · scattered · split · ambiguous · disputed
	Vote    *resolve.Vote
	Runners []resolve.Candidate
}

// WorklistReport is the hygiene query's result (§9.6). Session scoping and
// the abandoned/lineage groups arrive with M6/M7.
type WorklistReport struct {
	Items []WorklistItem
}

// Preview builds the one-line preview of an entry body.
func Preview(subj record.Subject, handle record.Handle, body string, flags []Flag) EntryPreview {
	lines := strings.Split(body, "\n")
	return EntryPreview{
		Subj:      subj,
		Handle:    handle,
		FirstLine: lines[0],
		MoreLines: len(lines) - 1,
		Flags:     flags,
	}
}
