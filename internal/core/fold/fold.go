// Package fold owns the per-entry fold: LWW registers, the absorbing
// tombstone, the rescue fold, per-record rule-(b) consumption, and FB
// dispute + churn derivation (design.md §8.3, §7; architecture.md §6).
//
// Data-in by design: rule-(b) classification arrives as a plain map, never
// through a port — the most test-pinned semantic in the system unit-tests
// with zero fakes.
package fold

import (
	"sort"

	"meltcloud.io/dm/internal/core/record"
)

// Entry is the state one checkout folds for a logical entry (§8.3). The
// current body/subj, anchor, and path are LWW registers over the entry's
// landed records — rule (b) applied per record: a checkout folds an entry
// from only the records whose origins have landed there.
type Entry struct {
	ID record.EntryID

	// Deleted: a TB exists. Absorbing and terminal everywhere — TB carries
	// no origin, so it needs no landing; a concurrent SU loses regardless
	// of rec-id order and there is no resurrection.
	Deleted bool

	// Landed: at least one fold-participating record's origin landed at
	// this checkout — the entry is present here. False means the entry
	// reads as pending-elsewhere/abandoned (classified in M6); it never
	// surfaces and its handle resolves as unknown (§5.3).
	Landed bool

	Subj   record.Subject
	Anchor record.Anchor
	Path   record.Path
	Body   string

	// Rev is the revision the fold reads: CR = 1, each landed SU +1 (§7.1
	// — RA/RP/TB are not revisions, so re-anchoring never inflates churn).
	Rev int

	// Disputed: an FB `!` newer (rec-id order) than the checkout's latest
	// landed SU/RA (§7.3). Cleared exactly by the housekeeping
	// resolutions u/k; RP is a pure relocation and deliberately clears
	// nothing.
	Disputed bool
}

// Fold computes the entry state a checkout reads from the entry's records
// and the per-origin rule-(b) classification (landed[origin] == true means
// the origin's line has landed at this checkout). Records may arrive in any
// order; the fold sorts by rec-id — the LWW total order.
func Fold(id record.EntryID, recs []record.Record, landed map[record.SHA]bool) Entry {
	sorted := make([]record.Record, len(recs))
	copy(sorted, recs)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID().Less(sorted[j].ID()) })

	e := Entry{ID: id}
	var (
		newestBody   record.Record // landed CR/SU — body + subj register
		newestAnchor record.Record // landed CR/SU/RA — anchor register
		newestPath   record.Record // landed CR/SU/RA/RP — path-hint register
		newestLanded record.Record // newest landed fold participant (rescue base)
		guard        record.RecID  // newest landed SU/RA — the dispute guard
	)
	for _, r := range sorted {
		switch v := r.(type) {
		case record.Tombstone:
			e.Deleted = true
		case record.Create:
			if landed[v.Origin] {
				newestBody, newestAnchor, newestPath, newestLanded = r, r, r, r
			}
		case record.Supersede:
			if landed[v.Origin] {
				newestBody, newestAnchor, newestPath, newestLanded = r, r, r, r
				guard = v.Rec
				e.Rev++ // counted below on top of the base revision
			}
		case record.ReAnchor:
			if landed[v.Origin] {
				newestAnchor, newestPath, newestLanded = r, r, r
				guard = v.Rec
			}
		case record.RePath:
			if landed[v.Origin] {
				newestPath, newestLanded = r, r
			}
		}
	}
	e.Rev++ // the CR
	e.Landed = newestLanded != nil
	if !e.Landed {
		e.Disputed = disputed(sorted, guard)
		return e
	}

	if newestBody != nil {
		applyBody(&e, newestBody)
	} else {
		// Rescue fold (§8.3): a landed RA/RP carries the entry alone. The
		// body — and, where no landed RA supplies one, the anchor — fold
		// from the newest CR/SU preceding the newest landed record in
		// rec-id order: the revision its writer acted on.
		if pre := newestNotePreceding(sorted, newestLanded.ID()); pre != nil {
			applyBody(&e, pre)
			if newestAnchor == nil {
				newestAnchor = pre
			}
		}
	}
	if newestAnchor != nil {
		e.Anchor = anchorOf(newestAnchor)
	}
	e.Path = pathOf(newestPath)
	e.Disputed = disputed(sorted, guard)
	return e
}

func applyBody(e *Entry, r record.Record) {
	switch v := r.(type) {
	case record.Create:
		e.Subj, e.Body = v.Subj, v.Body
	case record.Supersede:
		e.Subj, e.Body = v.Subj, v.Body
	}
}

// newestNotePreceding returns the newest CR/SU with rec-id < bound,
// regardless of landing — the rescue fold reaches for the revision the
// landed RA/RP's writer acted on, which may itself be unlanded here.
func newestNotePreceding(sorted []record.Record, bound record.RecID) record.Record {
	var pre record.Record
	for _, r := range sorted {
		if !r.ID().Less(bound) {
			break
		}
		switch r.(type) {
		case record.Create, record.Supersede:
			pre = r
		}
	}
	return pre
}

func anchorOf(r record.Record) record.Anchor {
	switch v := r.(type) {
	case record.Create:
		return v.Anchor
	case record.Supersede:
		return v.Anchor
	case record.ReAnchor:
		return v.Anchor
	}
	return record.Anchor{}
}

func pathOf(r record.Record) record.Path {
	switch v := r.(type) {
	case record.Create:
		return v.Path
	case record.Supersede:
		return v.Path
	case record.ReAnchor:
		return v.Path
	case record.RePath:
		return v.Path
	}
	return ""
}

// disputed reports an FB `!` newer than the guard (the newest landed
// SU/RA). FB records are global — a complaint travels wherever the entry is
// visible (§8.3) — so no landing check applies to the FB side.
func disputed(sorted []record.Record, guard record.RecID) bool {
	for i := len(sorted) - 1; i >= 0; i-- {
		fb, ok := sorted[i].(record.Feedback)
		if !ok || fb.Sig != record.SigDisputed {
			continue
		}
		return guard.Less(fb.Rec)
	}
	return false
}

// Link is one live directed link edge after folding (§6.4).
type Link struct {
	Rec     record.RecID // the winning LN's rec-id
	From    record.EntryID
	To      record.EntryID
	Comment string
}

// Links folds LN/UL records to the current link set: per directed pair the
// state is LWW by rec-id — the newest of {LN, UL} decides, an LN winner
// carries its comment. Links are global (no origin, no rule (b)). The
// result is ordered by winning rec-id, deterministic under union.
func Links(recs []record.Record) []Link {
	type pair struct{ from, to record.EntryID }
	newest := make(map[pair]record.Record)
	for _, r := range recs {
		var p pair
		switch v := r.(type) {
		case record.Link:
			p = pair{v.From, v.To}
		case record.Unlink:
			p = pair{v.From, v.To}
		default:
			continue
		}
		if cur, ok := newest[p]; !ok || cur.ID().Less(r.ID()) {
			newest[p] = r
		}
	}
	var out []Link
	for _, r := range newest {
		if ln, ok := r.(record.Link); ok {
			out = append(out, Link{Rec: ln.Rec, From: ln.From, To: ln.To, Comment: ln.Comment})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Rec.Less(out[j].Rec) })
	return out
}
