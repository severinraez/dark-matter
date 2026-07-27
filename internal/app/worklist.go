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

// WorklistOptions are the §9.6 listing knobs: All drops the session
// scoping (the flag that lists everything), Stale adds the on-request
// stale/unconfirmed notes to the listing.
type WorklistOptions struct {
	All   bool
	Stale bool
}

// Worklist is `dm worklist` (§9.6): everything that wants agent judgment —
// orphaned notes (rule (a) layer 5), abandoned notes grouped by line
// (rule (b)), ambiguous follows (splits, scatters, multi-candidate
// matches), disputed notes, and on request stale/unconfirmed ones — as a
// pure query over store ∪ pending and the current checkout; running it
// changes nothing beyond the m1 mint any read performs. The default
// listing is bounded and context-scoped: it leads with items homed on or
// near paths this session has read or written and counts the remainder.
func Worklist(dir string, det Determinism, opts WorklistOptions, notice io.Writer) (*view.WorklistReport, error) {
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
	data, err := ex.collectWorklist()
	if err != nil {
		return nil, err
	}
	scope, err := ex.sessionScope()
	if err != nil {
		return nil, err
	}
	return assembleWorklist(data, scope, opts), nil
}

// worklistData is the unfiltered hygiene classification: every item that
// could want judgment (stale/unconfirmed ones included — the listing
// filters, the health line counts them all) and every abandoned group,
// with the paths scoping needs.
type worklistData struct {
	items      []view.WorklistItem
	groups     []view.WorklistGroup
	groupPaths [][]record.Path // per group: its entries' folded homes
}

// collectWorklist classifies every non-deleted entry: landed ones against
// rule (a) and the dispute fold, non-landed ones against rule (b) —
// abandoned entries group by line so one vd repairs a whole group (§9.4);
// pending-elsewhere and unknown stay invisible everywhere (§5.3).
func (ex *executor) collectWorklist() (worklistData, error) {
	var data worklistData
	type deadEntry struct {
		id     record.EntryID
		origin record.SHA
		path   record.Path
	}
	var dead []deadEntry
	for _, id := range ex.order {
		e, err := ex.fold(id)
		if err != nil {
			return worklistData{}, err
		}
		if e.Deleted {
			continue
		}
		if !e.Landed {
			ls, err := ex.entryLineage(id)
			if err != nil {
				return worklistData{}, err
			}
			if ls == lineage.Abandoned {
				dead = append(dead, deadEntry{id, ex.lineOrigin(id), e.Path})
			}
			continue
		}
		res, err := ex.resolution(id)
		if err != nil {
			return worklistData{}, err
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
			} else {
				// On-request listing only (§9.6); always health-counted.
				reasons = append(reasons, "unconfirmed")
			}
		case resolve.Stale:
			reasons = append(reasons, "stale")
		}
		if e.Disputed {
			reasons = append(reasons, "disputed")
		}
		if len(reasons) == 0 {
			continue
		}
		data.items = append(data.items, view.WorklistItem{
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
				return worklistData{}, err
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
	groupPaths := make([][]record.Path, len(tips))
	for gi, tip := range tips {
		groups[gi] = view.WorklistGroup{Tip: tip, Hint: ex.lineHint(tip)}
	}
	for _, d := range dead {
		gi := groupOf[d.origin]
		groups[gi].Handles = append(groups[gi].Handles, ex.handles[d.id])
		groupPaths[gi] = append(groupPaths[gi], d.path)
	}
	order := make([]int, len(tips))
	for i := range order {
		order[i] = i
	}
	sort.Slice(order, func(i, j int) bool { return groups[order[i]].Tip < groups[order[j]].Tip })
	for _, gi := range order {
		data.groups = append(data.groups, groups[gi])
		data.groupPaths = append(data.groupPaths, groupPaths[gi])
	}
	return data, nil
}

// staleOnly reports an item the default listing withholds (§9.6's
// on-request stale/unconfirmed notes).
func staleOnly(it view.WorklistItem) bool {
	for _, r := range it.Reasons {
		if r != "stale" && r != "unconfirmed" {
			return false
		}
	}
	return true
}

// assembleWorklist applies the listing policy (§9.6 layer 3): filter the
// on-request items, then scope to the session's paths — everything when
// asked, or when the session has no context to scope by — counting the
// withheld remainder.
func assembleWorklist(data worklistData, scope map[record.Path]bool, opts WorklistOptions) *view.WorklistReport {
	report := &view.WorklistReport{}
	listAll := opts.All || len(scope) == 0
	for _, it := range data.items {
		if !opts.Stale && staleOnly(it) {
			continue
		}
		if listAll || nearScope(it.Path, scope) {
			report.Items = append(report.Items, it)
		} else {
			report.Elsewhere++
		}
	}
	for gi, g := range data.groups {
		if listAll || anyNearScope(data.groupPaths[gi], scope) {
			report.Groups = append(report.Groups, g)
		} else {
			report.Elsewhere++
		}
	}
	return report
}

// sessionScope is the path set this session has read or written, derived
// from the pending records and the pending stat accumulator — no new
// state (§9.6).
func (ex *executor) sessionScope() (map[record.Path]bool, error) {
	scope := make(map[record.Path]bool)
	addEntry := func(id record.EntryID) error {
		if _, known := ex.handles[id]; !known {
			return nil // dangling reference — ignored (§8.7)
		}
		e, err := ex.fold(id)
		if err != nil {
			return err
		}
		if e.Path != "" {
			scope[e.Path] = true
		}
		return nil
	}
	for _, rec := range ex.s.Pending {
		switch v := rec.(type) {
		case record.Create:
			scope[v.Path] = true
		case record.Supersede:
			scope[v.Path] = true
		case record.ReAnchor:
			scope[v.Path] = true
		case record.RePath:
			scope[v.Path] = true
		case record.Tombstone:
			if err := addEntry(v.Entry); err != nil {
				return nil, err
			}
		case record.Feedback:
			if err := addEntry(v.Entry); err != nil {
				return nil, err
			}
		}
	}
	for id := range ex.s.PendingStats {
		if err := addEntry(id); err != nil {
			return nil, err
		}
	}
	for id := range ex.s.deltas {
		if err := addEntry(id); err != nil {
			return nil, err
		}
	}
	return scope, nil
}

// nearScope reports whether a path sits on or near the session's work:
// the path itself, containment either way (folders), or the same
// directory.
func nearScope(p record.Path, scope map[record.Path]bool) bool {
	if scope[p] {
		return true
	}
	for s := range scope {
		if s.IsFolder() && p.Under(s) {
			return true
		}
		if p.IsFolder() && s.Under(p) {
			return true
		}
		if dirOf(p) == dirOf(s) {
			return true
		}
	}
	return false
}

func anyNearScope(paths []record.Path, scope map[record.Path]bool) bool {
	for _, p := range paths {
		if nearScope(p, scope) {
			return true
		}
	}
	return false
}

// dirOf is a path's containing directory as a folder path ("" at the
// root); a folder path is its own directory.
func dirOf(p record.Path) record.Path {
	s := strings.TrimSuffix(string(p), "/")
	idx := strings.LastIndexByte(s, '/')
	if idx < 0 {
		return ""
	}
	return record.Path(s[:idx+1])
}

// healthReasonOrder pins the health line's segment order (§8.4).
var healthReasonOrder = []string{"stale", "unconfirmed", "orphaned", "scattered", "split", "ambiguous", "disputed", "abandoned"}

// healthTally reduces the classification to the sync health line's inputs
// (§8.4): session-scoped counts by primary reason — stale included, the
// backlog is never invisible — and the global remainder.
func healthTally(data worklistData, scope map[record.Path]bool) ([]view.ReasonCount, int) {
	counts := make(map[string]int)
	elsewhere := 0
	scopeless := len(scope) == 0
	for _, it := range data.items {
		if !scopeless && nearScope(it.Path, scope) {
			counts[it.Reasons[0]]++
		} else {
			elsewhere++
		}
	}
	for gi := range data.groups {
		if !scopeless && anyNearScope(data.groupPaths[gi], scope) {
			counts["abandoned"]++
		} else {
			elsewhere++
		}
	}
	var scoped []view.ReasonCount
	for _, reason := range healthReasonOrder {
		if counts[reason] > 0 {
			scoped = append(scoped, view.ReasonCount{Reason: reason, N: counts[reason]})
		}
	}
	return scoped, elsewhere
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
