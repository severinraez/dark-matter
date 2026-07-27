package view

import (
	"fmt"
	"sort"
	"strings"

	"meltcloud.io/dm/internal/core/record"
	"meltcloud.io/dm/internal/core/resolve"
)

// The read-model app returns and cli renders (design.md §5.1, §4.3): the
// surface a node read returns, the expansion a handle read returns, the
// write acks, the search result, and the worklist report — plus the pure
// policy that shapes them: ranking (§5.6), search term matching (§5.5),
// the crowding threshold (§9.6), and the sync health line (§8.4).

// CrowdingThreshold is the visible-note count at which an `a` ack starts
// nudging to fold with `u` (§9.6 layer 2; §4.3's example ack). An
// implementation constant, tuned in the pilot.
const CrowdingThreshold = 9

// DepthBudgetLines caps the body lines a depth-modified read inlines
// (§5.4): once spent, further bodies stay collapsed — the read degrades
// gracefully instead of dumping.
const DepthBudgetLines = 40

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
// disclosure dimension): collapsed to handle + home + flags. Body is set
// only when a depth-modified read inlined it within budget (§5.4).
type ParentPreview struct {
	Subj   record.Subject
	Handle record.Handle
	Path   record.Path // the covering home (a split candidate when flagged split)
	Flags  []Flag
	Body   string
	Lines  int // full body line count — the cost hint while collapsed
}

// LinkItem is one entry on the surface's link dimension (§5.1): handle +
// label, never expanded at depth 0/1; Body is set at depth 2 within budget.
type LinkItem struct {
	Handle record.Handle
	Subj   record.Subject
	Label  string // the link comment, else the far entry's first body line
	Body   string
	Lines  int
}

// ChildLine is one row of a folder read's child inventory (§5.4): which
// immediate subpath carries notes, counts by subject.
type ChildLine struct {
	Path   record.Path // immediate child of the read folder (file or folder/)
	Counts []SubjectCount
}

// SubjectCount is a per-subject note tally, in ranking subject order.
type SubjectCount struct {
	Subj record.Subject
	N    int
}

// Surface is the budgeted digest of one node (§5.1): own previews, parent
// notes, the link dimension, the child inventory (folder reads), and the
// inventory footer counts. Depth records the disclosure depth that built
// it (§5.4).
type Surface struct {
	Node     record.Path
	Depth    int
	Own      []EntryPreview
	Parents  []ParentPreview
	Links    []LinkItem
	Children []ChildLine
	// LinkEdges counts live link edges touching the surfaced entries —
	// the inventory footer's link count.
	LinkEdges int
	// Hidden approximates the body lines the surface did not show — the
	// footer's "how much more is there" signal (§5.2).
	Hidden int
}

// Created is the `+ #handle created` ack (§4.3). Crowded, when non-zero,
// is the node's visible note count at or past the crowding threshold —
// the ack appends the fold nudge (§9.6).
type Created struct {
	Handle  record.Handle
	Crowded int
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
	AckVdLanded   AckVerb = "vd-landed"
	AckVdUnlanded AckVerb = "vd-unlanded"
)

// Ack is one `✓` write acknowledgment line. Handle is the acted-on entry;
// the other fields apply per verb: Rev for superseded, Sig for feedback,
// To for linked/unlinked (the ack echoes minted handles, so `$N` callers
// still learn the stable ones — §4.2), Subject + Count for vd (the ack
// reports the bound-origin count, §9.4).
type Ack struct {
	Verb    AckVerb
	Handle  record.Handle
	To      record.Handle
	Rev     int
	Sig     record.Sig
	Subject record.SHA
	Count   int
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

// WorklistGroup is one abandoned line (§9.6): entries whose origins died
// with a rewritten line, grouped so a single `vd` disposition repairs the
// whole group — verdict repair is O(lines), not O(notes) (§9.4).
type WorklistGroup struct {
	// Tip is the newest origin of the group — the sha a `vd:tip:landed:…`
	// disposition bulk-binds.
	Tip record.SHA
	// Handles lists the group's entries, creation order.
	Handles []record.Handle
	// Hint is the matcher hint: why inference could not bind (a memoized
	// failed attempt on this line), or "no evidence".
	Hint string
}

// WorklistReport is the hygiene query's result (§9.6): the listed items —
// session-scoped by default, everything on request — plus the global
// remainder as a count.
type WorklistReport struct {
	Items []WorklistItem
	// Abandoned lines, grouped, deterministic order (§9.4/§9.6).
	Groups []WorklistGroup
	// Elsewhere counts the items and groups the scoped listing withheld
	// (§9.6 layer 3: the global remainder is a count, never invisible).
	Elsewhere int
}

// SearchMatch is one `s` hit (§5.5): the entry's handle, home, a snippet
// around the first matching term, and how many more body lines match.
type SearchMatch struct {
	Handle  record.Handle
	Subj    record.Subject
	Path    record.Path
	Snippet string
	More    int
}

// SearchResult is the `s:path:terms` outcome: matches over the node's
// entire read-context, plus how many entries were searched.
type SearchResult struct {
	Node     record.Path
	Matches  []SearchMatch
	Searched int
}

// snippetWidth bounds a search snippet's window (§5.5) — matches are
// handles + snippets, not bodies.
const snippetWidth = 60

// MatchTerms evaluates the §5.5 term grammar against one body: groups is
// the OR of AND-groups (`a+b|c` → [[a b] [c]]); an entry matches when any
// group has all its terms present, case-insensitive. It returns the
// snippet around the winning group's first term and the count of further
// matching lines.
func MatchTerms(body string, groups [][]string) (snippet string, more int, ok bool) {
	lower := strings.ToLower(body)
	var winner []string
	for _, g := range groups {
		all := true
		for _, term := range g {
			if !strings.Contains(lower, strings.ToLower(term)) {
				all = false
				break
			}
		}
		if all {
			winner = g
			break
		}
	}
	if winner == nil {
		return "", 0, false
	}
	idx := strings.Index(lower, strings.ToLower(winner[0]))
	lineStart := strings.LastIndexByte(body[:idx], '\n') + 1
	lineEnd := len(body)
	if nl := strings.IndexByte(body[idx:], '\n'); nl >= 0 {
		lineEnd = idx + nl
	}
	line := body[lineStart:lineEnd]
	if len(line) > snippetWidth {
		start := idx - lineStart - snippetWidth/3
		if start < 0 {
			start = 0
		}
		if start+snippetWidth > len(line) {
			start = len(line) - snippetWidth
		}
		line = line[start : start+snippetWidth]
	}
	snippet = "…" + line + "…"
	matched := 0
	for _, l := range strings.Split(lower, "\n") {
		for _, term := range winner {
			if strings.Contains(l, strings.ToLower(term)) {
				matched++
				break
			}
		}
	}
	if matched > 0 {
		more = matched - 1
	}
	return snippet, more, true
}

// ---- ranking (§5.6) ----

// SubjectOrder is the ranking's subject priority: arch first for
// orientation, then code, dev, ops.
var SubjectOrder = []record.Subject{record.SubjArch, record.SubjCode, record.SubjDev, record.SubjOps}

func subjRank(s record.Subject) int {
	for i, o := range SubjectOrder {
		if s == o {
			return i
		}
	}
	return len(SubjectOrder)
}

// RankOwn orders one node's own previews: flag state (unflagged first),
// then subject priority — proximity is constant within a node, and the
// sort is stable, so equal entries keep creation order (§5.6: three keys,
// fully deterministic, no tuning).
func RankOwn(items []EntryPreview) {
	sort.SliceStable(items, func(i, j int) bool {
		fi, fj := len(items[i].Flags) > 0, len(items[j].Flags) > 0
		if fi != fj {
			return !fi
		}
		return subjRank(items[i].Subj) < subjRank(items[j].Subj)
	})
}

// RankParents orders the parent dimension: proximity (closer ancestors
// first), then flag state within the proximity group, then subject
// priority (§5.6).
func RankParents(items []ParentPreview) {
	depth := func(p record.Path) int {
		if p == record.RootFolder {
			return 0 // the root is every node's furthest ancestor
		}
		return strings.Count(string(p), "/")
	}
	sort.SliceStable(items, func(i, j int) bool {
		di, dj := depth(items[i].Path), depth(items[j].Path)
		if di != dj {
			return di > dj
		}
		fi, fj := len(items[i].Flags) > 0, len(items[j].Flags) > 0
		if fi != fj {
			return !fi
		}
		return subjRank(items[i].Subj) < subjRank(items[j].Subj)
	})
}

// ---- sync health line (§8.4, §9.6 layer 4) ----

// ReasonCount is one health-line segment: how many session-scoped items
// want judgment for a reason.
type ReasonCount struct {
	Reason string
	N      int
}

// HealthLine renders the one-line worklist summary every sync ends with:
// session-scoped counts by reason, then the global remainder
// (`3 stale · 1 orphaned near your work · 41 elsewhere`).
func HealthLine(scoped []ReasonCount, elsewhere int) string {
	var parts []string
	for _, rc := range scoped {
		if rc.N > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", rc.N, rc.Reason))
		}
	}
	if len(parts) == 0 && elsewhere == 0 {
		return "worklist clean"
	}
	line := ""
	if len(parts) > 0 {
		line = strings.Join(parts, " · ") + " near your work"
	}
	if elsewhere > 0 {
		if line != "" {
			line += " · "
		}
		line += fmt.Sprintf("%d elsewhere", elsewhere)
	}
	return line
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
