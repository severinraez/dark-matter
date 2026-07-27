package evcache

import (
	"errors"
	"sort"

	"meltcloud.io/dm/internal/core/evidence"
	"meltcloud.io/dm/internal/core/record"
)

// The ff-aware unreachability cache (§8.2): {origin → unreachable} under a
// refs fingerprint, revalidated incrementally and always delta-scoped. A
// fast-forward rescans only its delta (old..new) — a ff *can* resurrect a
// dead origin, by fetching the origin itself or a merge parent descending
// from it — while non-ff updates and new refs rescan from their tips;
// commits already covered by the fingerprint are never rewalked. Deleted
// refs only shrink reachability and need no rescan.
//
// Disposable like every cache: a lost or corrupt state file recomputes
// wholesale; a revalidation error falls back to the raw query.

// unreachKey names the single state file. Unlike the content-keyed memos,
// this cache is incremental by design — the payload carries the refs
// snapshot it is valid under.
const unreachKey = "unreach"

// ReachableFrom answers the Lineage-role reachability question, skipping
// the full ref scan for origins the cache knows are dead under a
// revalidated refs fingerprint.
func (c *Cached) ReachableFrom(origins []record.SHA) (map[record.SHA][]evidence.RefTip, error) {
	cur, err := c.Evidence.Repo().RefSnapshot()
	if err != nil {
		return nil, err
	}
	dead := c.revalidatedDead(cur)

	out := make(map[record.SHA][]evidence.RefTip, len(origins))
	var need []record.SHA
	for _, o := range origins {
		if dead[o] {
			out[o] = nil
		} else {
			need = append(need, o)
		}
	}
	if len(need) > 0 {
		fresh, err := c.Evidence.ReachableFrom(need)
		if err != nil {
			return nil, err
		}
		for o, refs := range fresh {
			out[o] = refs
			if len(refs) == 0 {
				dead[o] = true
			}
		}
	}
	c.writeUnreach(cur, dead)
	return out, nil
}

// revalidatedDead loads the cached dead set and re-admits only the origins
// the refs delta could have resurrected. Any inconsistency degrades to an
// empty set — the cache is never load-bearing.
func (c *Cached) revalidatedDead(cur map[evidence.Ref]record.SHA) map[record.SHA]bool {
	prevRefs, prevDead, ok := c.readUnreach()
	if !ok || len(prevDead) == 0 {
		return map[record.SHA]bool{}
	}
	// Classify ref movement against the fingerprint.
	var ffDeltas [][2]record.SHA // (old, new) fast-forwards: delta-scoped rescan
	var newTips []record.SHA     // new refs and non-ff updates: rescan from tips
	for name, tip := range cur {
		old, had := prevRefs[name]
		if !had {
			newTips = append(newTips, tip)
			continue
		}
		if old == tip {
			continue
		}
		if ff, err := c.Evidence.IsAncestor(old, tip); err == nil && ff {
			ffDeltas = append(ffDeltas, [2]record.SHA{old, tip})
		} else {
			newTips = append(newTips, tip)
		}
	}
	// The delta commit set: everything a ff brought in (merged side
	// branches included — rev-list new ^old covers them by construction).
	delta := map[record.SHA]bool{}
	for _, d := range ffDeltas {
		shas, err := c.Evidence.Repo().RevList(d[1], d[0])
		if err != nil {
			return map[record.SHA]bool{}
		}
		for _, s := range shas {
			delta[s] = true
		}
	}
	dead := make(map[record.SHA]bool, len(prevDead))
	for o := range prevDead {
		if delta[o] {
			continue // resurrected (or at least suspect): recheck fully
		}
		suspect := false
		for _, tip := range newTips {
			if ok, err := c.Evidence.IsAncestor(o, tip); err != nil || ok {
				suspect = true
				break
			}
		}
		if !suspect {
			dead[o] = true
		}
	}
	return dead
}

// ---- state codec: refs fingerprint + dead set (§8.2 binary style) ----

func (c *Cached) readUnreach() (map[evidence.Ref]record.SHA, map[record.SHA]bool, bool) {
	payload, ok := c.dir.ReadCache(unreachKey)
	if !ok {
		return nil, nil, false
	}
	refs, dead, err := decodeUnreach(payload)
	if err != nil {
		return nil, nil, false // corrupt: recompute wholesale
	}
	return refs, dead, true
}

func (c *Cached) writeUnreach(refs map[evidence.Ref]record.SHA, dead map[record.SHA]bool) {
	// Best-effort: a failed write only costs the next invocation a rescan.
	_ = c.dir.WriteCache(unreachKey, encodeUnreach(refs, dead))
}

func encodeUnreach(refs map[evidence.Ref]record.SHA, dead map[record.SHA]bool) []byte {
	names := make([]string, 0, len(refs))
	for name := range refs {
		names = append(names, string(name))
	}
	sort.Strings(names)
	shas := make([]string, 0, len(dead))
	for sha := range dead {
		shas = append(shas, string(sha))
	}
	sort.Strings(shas)

	var b []byte
	b = appendUvarintLen(b, len(names))
	for _, name := range names {
		b = appendString(b, name)
		b = appendString(b, string(refs[evidence.Ref(name)]))
	}
	b = appendUvarintLen(b, len(shas))
	for _, sha := range shas {
		b = appendString(b, sha)
	}
	return b
}

func decodeUnreach(payload []byte) (map[evidence.Ref]record.SHA, map[record.SHA]bool, error) {
	n, payload, err := takeUvarint(payload)
	if err != nil {
		return nil, nil, err
	}
	refs := make(map[evidence.Ref]record.SHA, n)
	for i := uint64(0); i < n; i++ {
		var name, tip string
		if name, payload, err = takeString(payload); err != nil {
			return nil, nil, err
		}
		if tip, payload, err = takeString(payload); err != nil {
			return nil, nil, err
		}
		refs[evidence.Ref(name)] = record.SHA(tip)
	}
	if n, payload, err = takeUvarint(payload); err != nil {
		return nil, nil, err
	}
	dead := make(map[record.SHA]bool, n)
	for i := uint64(0); i < n; i++ {
		var sha string
		if sha, payload, err = takeString(payload); err != nil {
			return nil, nil, err
		}
		dead[record.SHA(sha)] = true
	}
	if len(payload) != 0 {
		return nil, nil, errors.New("trailing bytes in unreachability state")
	}
	return refs, dead, nil
}
