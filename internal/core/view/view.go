package view

import (
	"strings"

	"meltcloud.io/dm/internal/core/record"
)

// The read-model app returns and cli renders (design.md §5.1, §4.3): the
// surface a node read returns, the expansion a handle read returns, and
// the write acks. Ranking, search, worklist, and the crowding threshold
// arrive with their milestones.

// EntryPreview is one own-entry line of a surface: subject, handle, the
// body's first line, and the hidden-size hint (§5.1, §5.2).
type EntryPreview struct {
	Subj      record.Subject
	Handle    record.Handle
	FirstLine string
	MoreLines int // body lines beyond the first — the "(+N lines)" hint
	// Flags (⚠stale …) arrive with M5 resolution.
}

// Surface is the budgeted digest of one node (§5.1). M2 carries own
// entries only; parents, links, and the hidden tally arrive with their
// dimensions (M5/M7) but are part of the inventory footer from day one.
type Surface struct {
	Node    record.Path
	Own     []EntryPreview
	Parents int
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
// plus its links one level and relations.
type Expansion struct {
	Node       record.Path
	Subj       record.Subject
	Handle     record.Handle
	Body       string
	Links      []LinkPreview // outgoing, live endpoints only
	LinkedFrom []record.Path // nodes whose entries link here, deduped
}

// LinkPreview is one collapsed link line item: the target's handle and the
// link's optional comment.
type LinkPreview struct {
	Handle  record.Handle
	Comment string
}

// Preview builds the one-line preview of an entry body.
func Preview(subj record.Subject, handle record.Handle, body string) EntryPreview {
	lines := strings.Split(body, "\n")
	return EntryPreview{
		Subj:      subj,
		Handle:    handle,
		FirstLine: lines[0],
		MoreLines: len(lines) - 1,
	}
}
