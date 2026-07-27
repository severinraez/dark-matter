package app

import (
	"io"
	"sort"
	"strings"

	"meltcloud.io/dm/internal/core/lineage"
	"meltcloud.io/dm/internal/core/record"
	"meltcloud.io/dm/internal/core/resolve"
	"meltcloud.io/dm/internal/core/view"
)

// Worklist is `dm worklist` (§9.6): everything that wants agent judgment —
// orphaned notes (rule (a) layer 5), abandoned notes grouped by line
// (rule (b)), ambiguous follows (splits, scatters, multi-candidate
// matches), and disputed notes — as a pure query over store ∪ pending and
// the current checkout; running it changes nothing beyond the m1 mint any
// read performs. Session scoping and the on-request stale/unconfirmed
// listing arrive with M7.
func Worklist(dir string, det Determinism, notice io.Writer) (*view.WorklistReport, error) {
	s, err := OpenSession(dir, det, notice)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	ex, err := s.newExecutor()
	if err != nil {
		return nil, err
	}
	if err := ex.mintPass(nil); err != nil {
		return nil, err
	}
	return ex.worklist()
}

// worklist classifies every non-deleted entry: landed ones against rule
// (a) and the dispute fold, non-landed ones against rule (b) — abandoned
// entries group by line so one vd repairs a whole group (§9.4);
// pending-elsewhere and unknown stay invisible everywhere (§5.3).
func (ex *executor) worklist() (*view.WorklistReport, error) {
	report := &view.WorklistReport{}
	type deadEntry struct {
		id     record.EntryID
		origin record.SHA
	}
	var dead []deadEntry
	for _, id := range ex.order {
		e, err := ex.fold(id)
		if err != nil {
			return nil, err
		}
		if e.Deleted {
			continue
		}
		if !e.Landed {
			ls, err := ex.entryLineage(id)
			if err != nil {
				return nil, err
			}
			if ls == lineage.Abandoned {
				dead = append(dead, deadEntry{id, ex.lineOrigin(id)})
			}
			continue
		}
		res, err := ex.resolution(id)
		if err != nil {
			return nil, err
		}
		var reasons []string
		runners := res.Runners
		switch res.State {
		case resolve.Orphaned, resolve.Scattered, resolve.Split:
			reasons = append(reasons, res.State.String())
		case resolve.Unconfirmed:
			if len(res.Runners) > 0 {
				// Multi-candidate match: dm picked by affinity, the agent
				// decides (§9.1 ambiguity policy). The item lists every
				// candidate, dm's pick first.
				reasons = append(reasons, "ambiguous")
				runners = append([]resolve.Candidate{{Path: res.Path, Score: res.Score}}, res.Runners...)
			}
		}
		if e.Disputed {
			reasons = append(reasons, "disputed")
		}
		if len(reasons) == 0 {
			continue
		}
		report.Items = append(report.Items, view.WorklistItem{
			Handle:  ex.handles[id],
			Subj:    e.Subj,
			Path:    e.Path,
			Reasons: reasons,
			Vote:    res.Vote,
			Runners: runners,
		})
	}

	// Group the abandoned entries by line — origins related by ancestry
	// share a rewritten line; where ancestry is incomputable (evidence
	// destroyed, objects never fetched) each origin stands alone (§9.4).
	groupOf := make(map[record.SHA]int)
	var tips []record.SHA
	for _, d := range dead {
		if _, seen := groupOf[d.origin]; seen {
			continue
		}
		placed := false
		for gi, tip := range tips {
			related, err := ex.sameLine(d.origin, tip)
			if err != nil {
				return nil, err
			}
			if related {
				groupOf[d.origin] = gi
				if newer, err := ex.ev.IsAncestor(tips[gi], d.origin); err == nil && newer {
					tips[gi] = d.origin // the group tip is the newest origin
				}
				placed = true
				break
			}
		}
		if !placed {
			groupOf[d.origin] = len(tips)
			tips = append(tips, d.origin)
		}
	}
	groups := make([]view.WorklistGroup, len(tips))
	for gi, tip := range tips {
		groups[gi] = view.WorklistGroup{Tip: tip, Hint: ex.lineHint(tip)}
	}
	for _, d := range dead {
		gi := groupOf[d.origin]
		groups[gi].Handles = append(groups[gi].Handles, ex.handles[d.id])
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].Tip < groups[j].Tip })
	report.Groups = groups
	return report, nil
}

// lineOrigin is the entry's newest fold-participating origin — the line
// its latest state died on.
func (ex *executor) lineOrigin(id record.EntryID) record.SHA {
	origins := recordOrigins(ex.records[id])
	if len(origins) == 0 {
		return ""
	}
	return origins[len(origins)-1]
}

// sameLine reports whether two origins share a line (either is ancestor of
// the other); incomputable ancestry reads as unrelated.
func (ex *executor) sameLine(a, b record.SHA) (bool, error) {
	if a == b {
		return true, nil
	}
	if ok, err := ex.ev.IsAncestor(a, b); err != nil || ok {
		return ok, err
	}
	return ex.ev.IsAncestor(b, a)
}

// lineHint finds the matcher hint for a dead line: the memoized failed
// attempt (nomatch-<tip>-<target>, §8.2) whose tip lies on the group's
// line. The memo is pure memoization of a recomputable attempt, so the
// worklist stays cache-consistent; with no attempt on record the line
// reads "no evidence".
func (ex *executor) lineHint(groupTip record.SHA) string {
	keys, err := ex.s.Local.ListCache("nomatch-")
	if err != nil {
		return "no evidence"
	}
	for _, key := range keys {
		parts := strings.Split(key, "-")
		if len(parts) != 3 {
			continue
		}
		memoTip := record.SHA(parts[1])
		if ok, err := ex.sameLine(memoTip, groupTip); err != nil || !ok {
			continue
		}
		if payload, ok := ex.s.Local.ReadCache(key); ok && len(payload) > 0 {
			return string(payload)
		}
	}
	return "no evidence"
}
