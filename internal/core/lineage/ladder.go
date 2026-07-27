package lineage

import (
	"fmt"
	"strings"

	"meltcloud.io/dm/internal/core/evidence"
	"meltcloud.io/dm/internal/core/record"
)

// The matcher ladder (§9.4) is precision-first: a false negative is
// worklisted and bulk-repairable, a false positive surfaces wrong notes
// unflagged and replicates. Matchers bind only on evidence that identifies
// the line, never merely the patch — when in doubt, worklist.

// MinDiffFloor is the m3 min-diff floor, also applied to m2's total bound
// diff (short-segment collisions under branch reuse): a segment whose
// cumulative diff changes fewer lines never binds by patch identity —
// trivial diffs collide. Starting guess; rides Q3 calibration (§14).
const MinDiffFloor = 3

// Succession is one m1 candidate: the line ending at Old was locally
// rewritten to New (an action-filtered reflog entry).
type Succession struct {
	Ref evidence.Ref
	Old record.SHA
	New record.SHA
}

// ReflogSuccessions applies the m1 action filter (§9.4): only `rebase*`
// and `commit (amend)` entries are conflict-proof successions; `reset:`,
// `branch -f` ("branch: Reset"), and `checkout -B` never bind. Entries
// arrive oldest first per ref and stay in that order — chained rewrites
// must bind in sequence so each binding's landed-as joins the next
// segment's candidates.
func ReflogSuccessions(entries []evidence.ReflogEntry) []Succession {
	var out []Succession
	for _, e := range entries {
		if !strings.HasPrefix(e.Action, "rebase") && !strings.HasPrefix(e.Action, "commit (amend)") {
			continue
		}
		if !e.Old.Valid() || !e.New.Valid() || e.Old == e.New {
			continue
		}
		out = append(out, Succession{Ref: e.Ref, Old: e.Old, New: e.New})
	}
	return out
}

// Binding is one minted line-landing fact: origin → landed-as, stamped
// with the matcher that established it. App turns bindings into VD records.
type Binding struct {
	Origin   record.SHA
	LandedAs record.SHA
	Matcher  record.Matcher
}

// BindSegment materializes a line-tip binding as per-origin bindings
// (§9.4 — lines land, commits don't): every candidate (store ∪ pending
// origins plus the landed-as commits of existing VDs — or transitive
// chains break) inside (merge-base(tip, landedAs)..tip] gets one, in
// deterministic order.
func BindSegment(m evidence.Match, tip, landedAs record.SHA, candidates []record.SHA, matcher record.Matcher) ([]Binding, error) {
	mb, err := m.MergeBase(tip, landedAs)
	if err != nil {
		return nil, err
	}
	base := record.SHA("")
	if mb != nil {
		base = *mb
	}
	seg, err := m.Segment(base, tip)
	if err != nil {
		return nil, err
	}
	inSegment := make(map[record.SHA]bool, len(seg))
	for _, s := range seg {
		inSegment[s] = true
	}
	var out []Binding
	seen := make(map[record.SHA]bool)
	for _, c := range candidates {
		if inSegment[c] && !seen[c] {
			seen[c] = true
			out = append(out, Binding{Origin: c, LandedAs: landedAs, Matcher: matcher})
		}
	}
	sortBindings(out)
	return out, nil
}

func sortBindings(bs []Binding) {
	for i := 1; i < len(bs); i++ {
		for j := i; j > 0 && bs[j].Origin < bs[j-1].Origin; j-- {
			bs[j], bs[j-1] = bs[j-1], bs[j]
		}
	}
}

// AttemptReplay is m2 — ancestry replay: full-segment, in-order
// per-commit patch-id pairing of (mb..old] against (mb..new], skipping
// empty-diff commits; one unpaired commit → no bind. The total bound diff
// must clear the floor (branch-reuse collisions). Returns bind-or-not and
// the no-bind hint.
func AttemptReplay(m evidence.Match, old, new record.SHA) (bool, string, error) {
	mb, err := m.MergeBase(old, new)
	if err != nil {
		return false, "", err
	}
	if mb == nil {
		return false, "m2: no merge base", nil
	}
	rid, err := m.RangePatchID(*mb, old)
	if err != nil {
		return false, "", err
	}
	if rid == nil {
		return false, "m2: empty segment", nil
	}
	if rid.DiffSize < MinDiffFloor {
		return false, fmt.Sprintf("m2: below floor (%d < %d changed lines)", rid.DiffSize, MinDiffFloor), nil
	}
	segOld, err := m.Segment(*mb, old)
	if err != nil {
		return false, "", err
	}
	segNew, err := m.Segment(*mb, new)
	if err != nil {
		return false, "", err
	}
	newIDs := make([]*evidence.PatchID, len(segNew))
	for i, c := range segNew {
		if newIDs[i], err = m.PatchID(c); err != nil {
			return false, "", err
		}
	}
	j := 0
	for _, c := range segOld {
		pid, err := m.PatchID(c)
		if err != nil {
			return false, "", err
		}
		if pid == nil {
			continue // empty diff: skip (§9.4 m2 guard)
		}
		found := false
		for j < len(newIDs) {
			cand := newIDs[j]
			j++
			if cand != nil && *cand == *pid {
				found = true
				break
			}
		}
		if !found {
			return false, "m2: unpaired commit", nil
		}
	}
	return true, "", nil
}

// AttemptSquash is m3 — cumulative squash: the patch-id of the whole
// segment diff (mb..tip] must equal exactly one commit on the target line
// (mb..target]; two candidates (apply → revert → re-apply) or a sub-floor
// diff never bind. Returns the landed-as commit, or nil with the hint.
func AttemptSquash(m evidence.Match, tip, target record.SHA) (*record.SHA, string, error) {
	mb, err := m.MergeBase(tip, target)
	if err != nil {
		return nil, "", err
	}
	if mb == nil {
		return nil, "m3: no merge base", nil
	}
	rid, err := m.RangePatchID(*mb, tip)
	if err != nil {
		return nil, "", err
	}
	if rid == nil {
		return nil, "m3: empty segment", nil
	}
	if rid.DiffSize < MinDiffFloor {
		return nil, fmt.Sprintf("m3: below floor (%d < %d changed lines)", rid.DiffSize, MinDiffFloor), nil
	}
	seg, err := m.Segment(*mb, target)
	if err != nil {
		return nil, "", err
	}
	var matches []record.SHA
	for _, c := range seg {
		pid, err := m.PatchID(c)
		if err != nil {
			return nil, "", err
		}
		if pid != nil && *pid == rid.PatchID {
			matches = append(matches, c)
		}
	}
	switch len(matches) {
	case 1:
		return &matches[0], "", nil
	case 0:
		return nil, "m3: no squash candidate", nil
	default:
		return nil, fmt.Sprintf("m3: %d identical candidates", len(matches)), nil
	}
}
