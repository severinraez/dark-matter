package app

import (
	"sort"

	"meltcloud.io/dm/internal/core/evidence"
	"meltcloud.io/dm/internal/core/lineage"
	"meltcloud.io/dm/internal/core/record"
)

// One mint-pass component, three trigger moments (architecture.md §7
// decision 5): m1 at any read-bearing invocation (newExecutor); m2/m3 at
// sync and at reads after a fetch — derived, not detected: attempted iff
// classification hit non-landed origins and no nomatch-<tip>-<target-tip>
// memo exists. The memo check is app's (§5 decision 4: it caches a core
// conclusion, not a raw query); the ladder itself is core/lineage.

// forcedUpdate is one remote-tracking ref rewrite observed across a fetch
// — the m1r candidate generator: it feeds m2 and binds nothing itself
// (a forced update can't distinguish a forge rebase from branch reuse).
type forcedUpdate struct {
	old, new record.SHA
}

// forcedUpdates diffs two ref snapshots to the non-fast-forward
// remote-tracking updates the fetch applied.
func forcedUpdates(repo interface {
	IsAncestor(a, b record.SHA) (bool, error)
}, before, after map[evidence.Ref]record.SHA) ([]forcedUpdate, error) {
	var names []string
	for name := range before {
		names = append(names, string(name))
	}
	sort.Strings(names)
	var out []forcedUpdate
	for _, name := range names {
		old := before[evidence.Ref(name)]
		new, still := after[evidence.Ref(name)]
		if !still || old == new {
			continue
		}
		ff, err := repo.IsAncestor(old, new)
		if err != nil {
			return nil, err
		}
		if !ff {
			out = append(out, forcedUpdate{old: old, new: new})
		}
	}
	return out, nil
}

// bindCandidates lists the SHAs a segment binding may cover: every
// fold-participating record origin of a live (non-tombstoned) entry plus
// the landed-as commit of every landed VD (or transitive chains break —
// §9.4). Origins that already carry a winning verdict are excluded unless
// override is set (m5): inference fills gaps, it never fights a standing
// verdict — which is also what keeps a manual `unlanded` voiding stable.
func (ex *executor) bindCandidates(override bool) []record.SHA {
	winning := lineage.WinningVerdicts(ex.verdicts)
	var out []record.SHA
	seen := make(map[record.SHA]bool)
	add := func(s record.SHA) {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	for _, recs := range ex.records {
		if tombstoned(recs) {
			continue // hygiene bounds the dead-origin tail (§9.4)
		}
		for _, o := range recordOrigins(recs) {
			if _, bound := winning[o]; bound && !override {
				continue
			}
			add(o)
		}
	}
	for _, vd := range winning {
		if !vd.Landed {
			continue
		}
		if _, bound := winning[vd.LandedAs]; bound && !override {
			continue
		}
		add(vd.LandedAs)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func tombstoned(recs []record.Record) bool {
	for _, r := range recs {
		if _, ok := r.(record.Tombstone); ok {
			return true
		}
	}
	return false
}

// mintVerdict mints the rec-id and durably appends one VD to pending; the
// executor absorbs it so the same invocation's classification sees it.
func (ex *executor) mintVerdict(vd record.Verdict) error {
	recID, err := ex.s.Minter.MintRecID()
	if err != nil {
		return err
	}
	vd.Rec = recID
	return ex.commit(vd)
}

// mintM1 is the cheap reflog scan (§9.4): action-filtered successions of
// local branch refs, oldest first so chained rewrites bind in sequence,
// each binding its segment's still-unbound candidates. Qualified clones
// only — a degraded clone never judges.
func (ex *executor) mintM1() error {
	if !ex.s.Qualified {
		return nil
	}
	entries, err := ex.ev.ReflogEntries()
	if err != nil {
		return err
	}
	for _, sc := range lineage.ReflogSuccessions(entries) {
		// A succession whose old tip is an ancestor of the new one is a
		// plain advance (ff rebase, no-op), not a rewrite.
		ff, err := ex.ev.IsAncestor(sc.Old, sc.New)
		if err != nil {
			return err
		}
		if ff {
			continue
		}
		candidates := ex.bindCandidates(false)
		if len(candidates) == 0 {
			return nil
		}
		bindings, err := lineage.BindSegment(ex.ev, sc.Old, sc.New, candidates, record.MatcherReflog)
		if err != nil {
			return err
		}
		for _, b := range bindings {
			if err := ex.mintVerdict(record.Verdict{Origin: b.Origin, Landed: true, LandedAs: b.LandedAs, Matcher: b.Matcher}); err != nil {
				return err
			}
		}
	}
	return nil
}

// mintPass runs the heavy matchers — m2 over the fetch's forced-update
// pairs, m3 over the reachable tips of still-non-landed origins against
// the target lines (HEAD, plus the share remote's default branch). Every
// failed (tip, target) attempt memoizes under nomatch-<tip>-<target> and
// never repeats until the target line moves (§8.2); the memo payload is
// the worklist's matcher hint.
func (ex *executor) mintPass(pairs []forcedUpdate) error {
	if !ex.s.Qualified {
		return nil
	}
	// m2 — ancestry replay of forced updates.
	for _, p := range pairs {
		ff, err := ex.ev.IsAncestor(p.old, p.new)
		if err != nil {
			return err
		}
		if ff {
			continue
		}
		if _, memoized := ex.s.Local.ReadCache(nomatchKey(p.old, p.new)); memoized {
			continue
		}
		ok, hint, err := lineage.AttemptReplay(ex.ev, p.old, p.new)
		if err != nil {
			return err
		}
		if !ok {
			if err := ex.s.Local.WriteCache(nomatchKey(p.old, p.new), []byte(hint)); err != nil {
				return err
			}
			continue
		}
		if err := ex.bindLine(p.old, p.new, record.MatcherPaired); err != nil {
			return err
		}
	}
	// m3 — cumulative squash for lines that still haven't landed.
	nonLanded, err := ex.nonLandedClassifications()
	if err != nil {
		return err
	}
	if len(nonLanded) == 0 {
		return nil
	}
	targets, err := ex.mintTargets()
	if err != nil {
		return err
	}
	tips := candidateTips(nonLanded)
	for _, tip := range tips {
		for _, target := range targets {
			if tip == target {
				continue
			}
			landed, err := ex.ev.IsAncestor(tip, target)
			if err != nil {
				return err
			}
			if landed {
				continue // the line is in the target's lineage; rule (b) step 1 handles it
			}
			if _, memoized := ex.s.Local.ReadCache(nomatchKey(tip, target)); memoized {
				continue
			}
			landedAs, hint, err := lineage.AttemptSquash(ex.ev, tip, target)
			if err != nil {
				return err
			}
			if landedAs == nil {
				if err := ex.s.Local.WriteCache(nomatchKey(tip, target), []byte(hint)); err != nil {
					return err
				}
				continue
			}
			if err := ex.bindLine(tip, *landedAs, record.MatcherSquash); err != nil {
				return err
			}
		}
	}
	return nil
}

// bindLine materializes one proven line landing as per-origin VDs.
func (ex *executor) bindLine(tip, landedAs record.SHA, matcher record.Matcher) error {
	bindings, err := lineage.BindSegment(ex.ev, tip, landedAs, ex.bindCandidates(false), matcher)
	if err != nil {
		return err
	}
	for _, b := range bindings {
		if err := ex.mintVerdict(record.Verdict{Origin: b.Origin, Landed: true, LandedAs: b.LandedAs, Matcher: b.Matcher}); err != nil {
			return err
		}
	}
	return nil
}

// nonLandedClassifications classifies every live record origin and keeps
// the non-landed ones — the "attempt iff classification hit non-landed
// origins" condition and the m3 tip source.
func (ex *executor) nonLandedClassifications() (map[record.SHA]lineage.Classification, error) {
	var origins []record.SHA
	seen := make(map[record.SHA]bool)
	for _, recs := range ex.records {
		if tombstoned(recs) {
			continue
		}
		for _, o := range recordOrigins(recs) {
			if !seen[o] {
				seen[o] = true
				origins = append(origins, o)
			}
		}
	}
	sort.Slice(origins, func(i, j int) bool { return origins[i] < origins[j] })
	class, err := ex.classify(origins)
	if err != nil {
		return nil, err
	}
	out := make(map[record.SHA]lineage.Classification)
	for o, c := range class {
		if c.State != lineage.Landed {
			out[o] = c
		}
	}
	return out, nil
}

// candidateTips collects the ref tips non-landed origins are reachable
// from — the lines m3 attempts to land — deterministically ordered.
func candidateTips(class map[record.SHA]lineage.Classification) []record.SHA {
	var tips []record.SHA
	seen := make(map[record.SHA]bool)
	for _, c := range class {
		for _, rt := range c.Refs {
			if !seen[rt.Tip] {
				seen[rt.Tip] = true
				tips = append(tips, rt.Tip)
			}
		}
	}
	sort.Slice(tips, func(i, j int) bool { return tips[i] < tips[j] })
	return tips
}

// mintTargets lists the target lines a rewritten branch may have landed
// on: the current checkout, and the share remote's default branch (the
// forge-squash case does not require the author to sit on main).
func (ex *executor) mintTargets() ([]record.SHA, error) {
	targets := []record.SHA{ex.s.Head}
	remote, err := shareRemote(ex.s.Repo)
	if err != nil {
		return nil, err
	}
	if remote != "" {
		def, err := ex.s.Repo.ResolveRef("refs/remotes/" + remote + "/HEAD")
		if err != nil {
			return nil, err
		}
		if def != nil && *def != ex.s.Head {
			targets = append(targets, *def)
		}
	}
	return targets, nil
}

// nomatchKey names a failed (tip, target-tip) attempt memo (§8.2).
func nomatchKey(tip, target record.SHA) string {
	return "nomatch-" + string(tip) + "-" + string(target)
}
