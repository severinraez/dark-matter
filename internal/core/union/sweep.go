package union

import (
	"fmt"
	"sort"
	"time"

	"meltcloud.io/dm/internal/core/record"
)

// ShadowWindow is the shadowed-revision retention window (§8.7): a CR/SU
// hidden behind a later SU drops once its rec-id ULID is older than three
// months, which recency-scopes the derived churn count — recent churn is
// the volatility signal; ancient churn on a since-stable note is noise.
const ShadowWindow = 90 * 24 * time.Hour

// Dropped tallies what one sweep reclaimed.
type Dropped struct {
	Records int
	Blobs   int
}

// Sweep is `dm gc`'s deterministic mark-and-sweep (§8.7), the epoch bumped
// by one. What it keeps / drops:
//
//   - Tombstones: the TB line itself is kept forever — the permanent
//     suppressor that keeps a late resurrection dead. Everything it
//     suppresses (the entry's CR/SU bodies, RA/RP, LN/UL, FB, stat rows,
//     anchored blobs) is reclaimed now.
//   - Shadowed revisions: CR/SU hidden behind a later SU drop when older
//     than ShadowWindow — except the newest CR/SU preceding each surviving
//     RA/RP (rec-id order), which is pinned: it is the body the rescue fold
//     reaches for (§8.3).
//   - Verdicts: a VD drops iff no surviving record's origin matches its
//     origin and no surviving VD's landed-as references it (bindings chain,
//     §9.4) — landed verdicts are irreplaceable evidence once the origin
//     commit is GC'd. TB lines pin no verdicts: TB has no origin field.
//   - Blobs: mark-and-sweep from surviving records' anchor fields.
//   - Stat rows keyed to purged entries drop from each replica file.
//   - Headless records — referencing an entry with no CR/SU and no TB —
//     are garbage: dangling references are ignored at read, so dropping
//     them keeps the store a valid CRDT state under late union of any
//     replica's pending (§8.7's safety invariant).
//
// Pure function of (s, now): two concurrent compactors over the same tip
// produce the same swept set, so the gc race is benign.
func Sweep(s Set, now time.Time) (Set, Dropped, error) {
	ids := make([]record.RecID, 0, len(s.Records))
	recs := make(map[record.RecID]record.Record, len(s.Records))
	for id, data := range s.Records {
		r, err := record.Decode(data)
		if err != nil {
			return Set{}, Dropped{}, fmt.Errorf("store record %s: %w", id, err)
		}
		recs[id] = r
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i].Less(ids[j]) })

	// Group the entry-keyed records; LN/UL (two endpoints) and VD (no
	// entry) sweep by their own rules below.
	type group struct {
		tombstoned bool
		tombs      []record.RecID // TB lines, kept forever
		notes      []record.RecID // CR/SU in rec-id order — the revision chain
		other      []record.RecID // RA/RP/FB — no sweep rule targets these
		marks      []record.RecID // RA/RP subset of other — the rescue pins
	}
	groups := map[record.EntryID]*group{}
	at := func(e record.EntryID) *group {
		g, ok := groups[e]
		if !ok {
			g = &group{}
			groups[e] = g
		}
		return g
	}
	for _, id := range ids {
		switch v := recs[id].(type) {
		case record.Tombstone:
			g := at(v.Entry)
			g.tombstoned = true
			g.tombs = append(g.tombs, id)
		case record.Create:
			g := at(v.Entry)
			g.notes = append(g.notes, id)
		case record.Supersede:
			g := at(v.Entry)
			g.notes = append(g.notes, id)
		case record.ReAnchor:
			g := at(v.Entry)
			g.other = append(g.other, id)
			g.marks = append(g.marks, id)
		case record.RePath:
			g := at(v.Entry)
			g.other = append(g.other, id)
			g.marks = append(g.marks, id)
		case record.Feedback:
			g := at(v.Entry)
			g.other = append(g.other, id)
		}
	}

	keep := map[record.RecID]bool{}
	living := map[record.EntryID]bool{}
	for e, g := range groups {
		if g.tombstoned {
			for _, id := range g.tombs {
				keep[id] = true
			}
			continue
		}
		if len(g.notes) == 0 {
			continue // headless — garbage
		}
		living[e] = true
		for _, id := range g.other {
			keep[id] = true
		}
		// Revision chain: the newest CR/SU is the current state, always
		// kept; each surviving RA/RP pins the newest CR/SU preceding it —
		// the rescue fold's body; the rest are shadowed, window-scoped.
		pinned := map[record.RecID]bool{g.notes[len(g.notes)-1]: true}
		for _, mark := range g.marks {
			var pre *record.RecID
			for i := range g.notes {
				if g.notes[i].Less(mark) {
					pre = &g.notes[i]
				}
			}
			if pre != nil {
				pinned[*pre] = true
			}
		}
		for _, id := range g.notes {
			if pinned[id] || now.Sub(time.UnixMilli(int64(id.Time()))) <= ShadowWindow {
				keep[id] = true
			}
		}
	}

	// Links survive while both endpoints do; a link into a tombstoned or
	// purged entry is dangling — ignored at read, garbage here.
	for _, id := range ids {
		switch v := recs[id].(type) {
		case record.Link:
			if living[v.From] && living[v.To] {
				keep[id] = true
			}
		case record.Unlink:
			if living[v.From] && living[v.To] {
				keep[id] = true
			}
		}
	}

	// Verdict retention: fixed point over the origins surviving records
	// still need, chained through landed-as (F1 → S1 → S1′ keeps both VDs
	// while any record carries F1).
	needed := map[record.SHA]bool{}
	for id := range keep {
		if origin := noteOrigin(recs[id]); origin != "" {
			needed[origin] = true
		}
	}
	var verdicts []record.RecID
	for _, id := range ids {
		if _, ok := recs[id].(record.Verdict); ok {
			verdicts = append(verdicts, id)
		}
	}
	for changed := true; changed; {
		changed = false
		for _, id := range verdicts {
			vd := recs[id].(record.Verdict)
			if keep[id] || !needed[vd.Origin] {
				continue
			}
			keep[id] = true
			if vd.Landed && !needed[vd.LandedAs] {
				needed[vd.LandedAs] = true
				changed = true
			}
		}
	}

	out := NewSet()
	out.Epoch = s.Epoch + 1
	for id := range keep {
		out.Records[id] = s.Records[id]
	}
	for id := range keep {
		if blob := anchorBlob(recs[id]); blob != "" && s.Blobs[blob] {
			out.Blobs[blob] = true
		}
	}
	for replica, stats := range s.Stats {
		kept := Stats{}
		for e, row := range stats {
			if living[e] {
				kept[e] = row
			}
		}
		if len(kept) > 0 {
			out.Stats[replica] = kept
		}
	}
	return out, Dropped{
		Records: len(s.Records) - len(out.Records),
		Blobs:   len(s.Blobs) - len(out.Blobs),
	}, nil
}

// noteOrigin returns the rule-(b) origin of an origin-carrying note record
// ("" otherwise). VD origins are deliberately excluded: verdicts chain
// through landed-as, they never pin each other by origin alone.
func noteOrigin(r record.Record) record.SHA {
	switch v := r.(type) {
	case record.Create:
		return v.Origin
	case record.Supersede:
		return v.Origin
	case record.ReAnchor:
		return v.Origin
	case record.RePath:
		return v.Origin
	}
	return ""
}

// anchorBlob returns a record's content-anchor blob SHA ("" for folder
// path keys and anchor-less records).
func anchorBlob(r record.Record) record.SHA {
	switch v := r.(type) {
	case record.Create:
		return v.Anchor.Blob
	case record.Supersede:
		return v.Anchor.Blob
	case record.ReAnchor:
		return v.Anchor.Blob
	}
	return ""
}
