package view

import (
	"strings"

	"meltcloud.io/dm/internal/core/record"
)

// The M2 read-model subset: the surface a node read returns and the write
// acks (design.md §5.1, §4.3). Ranking, search, worklist, and the crowding
// threshold arrive with their milestones.

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
