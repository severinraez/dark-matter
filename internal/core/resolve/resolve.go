// Package resolve owns rule (a): file resolution layers 1–5, the folder
// fingerprint and follow vote, the similarity bands and margins, and the
// per-entry placement classification (design.md §9.1–§9.3; architecture.md
// §6).
//
// Resolution is a two-point comparison: the entry's write-time state
// (anchored blob / path key, origin commit) against the current checkout,
// consumed through the Tree and Match evidence roles. Nothing here is
// persisted — every answer is recomputed per read (§9.2: matches land in
// the disposable cache below the port).
package resolve

import (
	"sort"
	"strings"

	"meltcloud.io/dm/internal/core/evidence"
	"meltcloud.io/dm/internal/core/fold"
	"meltcloud.io/dm/internal/core/record"
)

// The acceptance bands (§9.2, §9.3) — constants by design, calibrated by
// the Q1 replay rig (§14); these are the documented starting guesses.
const (
	// AcceptScore/AcceptMargin gate layer-3 auto-accept: ≥ AcceptScore
	// similarity to the anchored blob AND the best candidate beating the
	// runner-up by ≥ AcceptMargin points → ⚠stale. The margin is the
	// load-bearing part: false pairings come from clusters of
	// near-identical candidates, not lone high scorers.
	AcceptScore  = 80
	AcceptMargin = 20
	// FollowScore is the inference-band floor: candidates below it never
	// resolve a note (→ pinned/orphaned); between FollowScore and
	// auto-accept the follow is flagged ⚠unconfirmed for bulk blessing.
	FollowScore = 50

	// Folder follow-heuristic checks (§9.3): the winner needs a majority
	// of paired members, a clear margin over the runner-up (in percentage
	// points of paired members), and pair coverage of a minimum fraction
	// of the origin member set.
	FolderMajorityPct = 50 // winner share must exceed this
	FolderMarginPct   = 50 // winner share − runner-up share, ≥
	FolderCoveragePct = 60 // paired members / origin members, ≥
	// SplitCandidatePct: a target prefix is a split candidate when it
	// holds at least this share of paired members; below it on every
	// prefix, the members scattered (worklist-only, §9.3).
	SplitCandidatePct = 25
	// AbsorbedForeignPct: a follow whose target is at least this foreign
	// (members not paired in from the noted folder) is flagged
	// ⚠unconfirmed regardless of vote strength (§9.3 absorbed).
	AbsorbedForeignPct = 50
)

// State is an entry's rule-(a) placement per checkout (§2.1/§2.2 — the
// content half; the rule-(b) states pending-elsewhere/abandoned/unknown are
// entry-level lineage classifications, M6).
type State int

const (
	// Fresh: visible, unflagged — the checkout contains what was described.
	Fresh State = iota
	// Stale: visible, ⚠stale — located, but the content drifted (files;
	// includes the pinned layer-4 case).
	Stale
	// Unconfirmed: visible, ⚠unconfirmed — followed by inference (mid-band
	// file match, non-unanimous or absorbed folder follow).
	Unconfirmed
	// Split: visible at every candidate, ⚠split — an ambiguous folder
	// follow the agent must settle (k:#h:path picks the home).
	Split
	// Scattered: worklist-only — members dispersed with no candidate
	// (§9.3: scatter has nowhere to surface).
	Scattered
	// Orphaned: worklist-only — the described content no longer exists
	// anywhere dm can place it (§9.1 layer 5).
	Orphaned
)

// String returns the state's worklist/debug name.
func (s State) String() string {
	switch s {
	case Fresh:
		return "fresh"
	case Stale:
		return "stale"
	case Unconfirmed:
		return "unconfirmed"
	case Split:
		return "split"
	case Scattered:
		return "scattered"
	}
	return "orphaned"
}

// Visible reports whether the state surfaces in normal reads (worklist
// states never do, §5.3 — split does, flagged at each candidate).
func (s State) Visible() bool {
	return s == Fresh || s == Stale || s == Unconfirmed || s == Split
}

// Candidate is one possible home with its evidence weight: similarity
// percent for files, paired-member votes for folder prefixes.
type Candidate struct {
	Path  record.Path
	Score int
}

// Vote is the folder follow-heuristic evidence (§9.3), computed once at
// resolution and carried for the worklist/drill lines instead of being
// re-derived.
type Vote struct {
	Candidates  []Candidate // target prefixes by descending votes
	Paired      int         // origin members with an attributed rename pair
	Members     int         // origin member set size
	CoveragePct int
	Unanimous   bool
	ForeignPct  int // winner-target members not paired in from the folder
}

// Resolution is one entry's rule-(a) answer against the checkout.
type Resolution struct {
	State State
	// Path is the resolved home; empty when there is no single home
	// (split, scattered, orphaned).
	Path record.Path
	// Layer records which §9.1 layer (files, 1–5) or §9.3 rank (folders,
	// 1–4) decided — the Q1 rig's measurement.
	Layer int
	// Pinned: file layer 4 — the path is agent-asserted by an RP, with
	// folder-note path semantics until confirmed.
	Pinned bool
	// Score is the accepted candidate's similarity (file layer 3; 100 for
	// exact layers).
	Score int
	// Runners are competing candidates the agent should know about (file
	// ambiguity policy: prefer the path hint / affinity, flag the rest).
	Runners []Candidate
	// Vote is the folder follow evidence (folder layer 3 and beyond).
	Vote *Vote
}

// Flagged reports whether the resolution carries a visibility flag.
func (r Resolution) Flagged() bool { return r.State != Fresh }

// Resolve classifies one landed, non-deleted entry against the checkout.
func Resolve(tr evidence.Tree, m evidence.Match, e fold.Entry) (Resolution, error) {
	if e.Anchor.IsPath() {
		return folder(tr, m, e)
	}
	return file(tr, m, e)
}

// ---- file notes: §9.1 layers 1–5 ----

func file(tr evidence.Tree, m evidence.Match, e fold.Entry) (Resolution, error) {
	anchor := e.Anchor.Blob
	hint := e.Path

	// Layer 1 — exact: the anchored blob at the path hint.
	working, err := tr.WorkingBlob(hint)
	if err != nil {
		return Resolution{}, err
	}
	if working != nil && *working == anchor {
		return Resolution{State: Fresh, Path: hint, Layer: 1, Score: 100}, nil
	}

	// Layer 2 — moved: the anchored blob at another path (pure
	// rename/extract — the case path-keying can never handle).
	paths, err := tr.PathsOf(anchor)
	if err != nil {
		return Resolution{}, err
	}
	if len(paths) > 0 {
		ranked := make([]Candidate, len(paths))
		for i, p := range paths {
			ranked[i] = Candidate{Path: p, Score: 100}
		}
		sortCandidates(ranked, hint)
		res := Resolution{State: Fresh, Path: ranked[0].Path, Layer: 2, Score: 100, Runners: ranked[1:]}
		if len(ranked) > 1 {
			// Multiple exact copies and none at the described path:
			// resolved by inference — prefer affinity, flag the rest.
			res.State = Unconfirmed
		}
		return res, nil
	}

	// Layer 3 — edited: similarity-match against the checkout. Candidates
	// are the file still at the hint (in-place edit) plus every
	// rename/copy pairing of the anchored blob or the hint path from the
	// batched origin→checkout diff.
	seen := map[record.Path]bool{}
	var candidates []record.Path
	if working != nil {
		seen[hint] = true
		candidates = append(candidates, hint)
	}
	if e.Origin != "" {
		pairs, err := m.RenamePairs(e.Origin)
		if err != nil {
			return Resolution{}, err
		}
		for _, p := range pairs {
			if (p.FromBlob == anchor || p.From == hint) && !seen[p.To] {
				seen[p.To] = true
				candidates = append(candidates, p.To)
			}
		}
	}
	scores, err := m.Score(anchor, candidates)
	if err != nil {
		return Resolution{}, err
	}
	var ranked []Candidate
	for p, s := range scores {
		if s >= FollowScore {
			ranked = append(ranked, Candidate{Path: p, Score: s})
		}
	}
	sortCandidates(ranked, hint)
	if len(ranked) > 0 {
		best := ranked[0]
		state := Unconfirmed // §9.2 inference band: flagged for bulk blessing
		if best.Score >= AcceptScore &&
			(len(ranked) == 1 || best.Score-ranked[1].Score >= AcceptMargin) {
			state = Stale // auto-accept: it described the pre-edit content
		}
		return Resolution{State: state, Path: best.Path, Layer: 3, Score: best.Score, Runners: ranked[1:]}, nil
	}

	// Layer 4 — pinned: no content match, but the latest landed
	// path-affecting record is an explicit RP (§6.5): the agent said
	// *where* it lives; nobody has yet said it still *matches*.
	if e.PinnedPath && working != nil {
		return Resolution{State: Stale, Path: hint, Layer: 4, Pinned: true}, nil
	}

	// Layer 5 — unresolved: orphaned, hygiene worklist.
	return Resolution{State: Orphaned, Layer: 5}, nil
}

// ---- folder notes: §9.3 path as key ----

func folder(tr evidence.Tree, m evidence.Match, e fold.Entry) (Resolution, error) {
	folded := e.Path // where to look now — landed RPs participate (§6.5)

	// Rank 1 — exact: the path exists in the checkout. Exact path wins
	// even over a real move (§2.2 row 13, deliberate); member churn never
	// matters — the note describes the place, not the contents.
	under, err := tr.PathsUnder(folded)
	if err != nil {
		return Resolution{}, err
	}
	if len(under) > 0 {
		return Resolution{State: Fresh, Path: folded, Layer: 1, Score: 100}, nil
	}

	// The origin-side fingerprint: the origin's tree at the anchor's path
	// key — what the anchored blob supplies for files.
	var fp *evidence.TreeFP
	if e.Origin != "" {
		if fp, err = tr.TreeAt(e.Origin, e.Anchor.PathKey); err != nil {
			return Resolution{}, err
		}
	}

	// Rank 2 — moved, pure: the fingerprint tree appears elsewhere (git mv
	// with no member edits preserves the tree SHA by construction).
	if fp != nil {
		occurrences, err := tr.PathsOfTree(fp.Tree)
		if err != nil {
			return Resolution{}, err
		}
		var live []Candidate
		for _, p := range occurrences {
			if p == folded {
				continue
			}
			present, err := tr.PathsUnder(p) // committed occurrence must survive dirt
			if err != nil {
				return Resolution{}, err
			}
			if len(present) > 0 {
				live = append(live, Candidate{Path: p, Score: 100})
			}
		}
		if len(live) > 0 {
			sortCandidates(live, folded)
			return Resolution{State: Fresh, Path: live[0].Path, Layer: 2, Score: 100, Runners: live[1:]}, nil
		}
	}
	if fp == nil || len(fp.Members) == 0 {
		// No fingerprint derivable (unfetched origin, or nothing was ever
		// committed there): nothing to vote with.
		return Resolution{State: Orphaned, Layer: 4}, nil
	}

	// Rank 3 — moved with churn: aggregate the file-level rename pairs and
	// vote by target prefix (§9.3). Each origin member votes at most once.
	pairs, err := m.RenamePairs(e.Origin)
	if err != nil {
		return Resolution{}, err
	}
	votes := map[record.Path]int{}
	votedMember := map[string]bool{}
	for _, p := range pairs {
		rel, ok := strings.CutPrefix(string(p.From), string(e.Anchor.PathKey))
		if !ok && e.Anchor.PathKey == record.RootFolder {
			rel, ok = string(p.From), true
		}
		if !ok || votedMember[rel] {
			continue
		}
		prefix, ok := targetPrefix(p.To, rel)
		if !ok {
			continue
		}
		votedMember[rel] = true
		votes[prefix]++
	}
	paired := len(votedMember)
	if paired == 0 {
		// Rank 4 — members gone with no rename pairs: folder deleted.
		return Resolution{State: Orphaned, Layer: 4}, nil
	}

	var ranked []Candidate
	for p, v := range votes {
		ranked = append(ranked, Candidate{Path: p, Score: v})
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].Score != ranked[j].Score {
			return ranked[i].Score > ranked[j].Score
		}
		return ranked[i].Path < ranked[j].Path
	})
	winner := ranked[0]
	winnerShare := winner.Score * 100 / paired
	runnerShare := 0
	if len(ranked) > 1 {
		runnerShare = ranked[1].Score * 100 / paired
	}
	members := len(fp.Members)
	coverage := paired * 100 / members
	vote := &Vote{
		Candidates:  ranked,
		Paired:      paired,
		Members:     members,
		CoveragePct: coverage,
		Unanimous:   winner.Score == paired,
	}
	targetMembers, err := tr.PathsUnder(winner.Path)
	if err != nil {
		return Resolution{}, err
	}
	if len(targetMembers) > winner.Score {
		vote.ForeignPct = (len(targetMembers) - winner.Score) * 100 / len(targetMembers)
	}

	// The three checks: majority · margin · coverage (§9.3).
	if winnerShare > FolderMajorityPct &&
		winnerShare-runnerShare >= FolderMarginPct &&
		coverage >= FolderCoveragePct {
		if vote.Unanimous && coverage == 100 && vote.ForeignPct < AbsorbedForeignPct {
			return Resolution{State: Fresh, Path: winner.Path, Layer: 3, Vote: vote}, nil
		}
		// Partial evidence, or an absorbed target that may no longer be
		// what the note described: follow flagged regardless of vote
		// strength (§2.2 rows 5/6/9).
		return Resolution{State: Unconfirmed, Path: winner.Path, Layer: 3, Vote: vote}, nil
	}
	var candidates []Candidate
	for _, c := range ranked {
		if c.Score*100/paired >= SplitCandidatePct {
			candidates = append(candidates, c)
		}
	}
	vote.Candidates = candidates
	if len(candidates) > 0 {
		// Split: ambiguity, surfaced ⚠split at every candidate — the
		// readers under them are the ones with context to resolve it.
		return Resolution{State: Split, Layer: 3, Vote: vote}, nil
	}
	// Scatter: no candidate at all — worklist-only by necessity.
	return Resolution{State: Scattered, Layer: 3, Vote: vote}, nil
}

// targetPrefix maps one rename pair to the folder prefix it votes for:
// where did the member (origin-relative path rel) land? Tries the full
// member-relative suffix first, then the member's directory (the member
// itself may have been renamed within the move).
func targetPrefix(to record.Path, rel string) (record.Path, bool) {
	if s := string(to); strings.HasSuffix(s, rel) {
		if p := s[:len(s)-len(rel)]; p == "" {
			return record.RootFolder, true
		} else if strings.HasSuffix(p, "/") {
			return record.Path(p), true
		}
	}
	relDir := dirOf(rel)
	toDir := folderOf(to)
	if relDir == "" {
		return toDir, true
	}
	if s := string(toDir); strings.HasSuffix(s, relDir) {
		if p := s[:len(s)-len(relDir)]; p == "" {
			return record.RootFolder, true
		} else if strings.HasSuffix(p, "/") {
			return record.Path(p), true
		}
	}
	return "", false
}

// ---- candidate ordering: score, then path-hint affinity (§9.2) ----

// sortCandidates orders by descending score, ties on path-hint affinity
// (same basename, then directory distance), then lexicographically for
// determinism — and then applies the §9.2 thin-margin rule: among the
// candidates within AcceptMargin of the top score, the best-affinity one
// leads (a score margin below the acceptance margin is not decisive; the
// hint is).
func sortCandidates(cands []Candidate, hint record.Path) {
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].Score != cands[j].Score {
			return cands[i].Score > cands[j].Score
		}
		ai, aj := affinity(cands[i].Path, hint), affinity(cands[j].Path, hint)
		if ai != aj {
			return ai < aj
		}
		return cands[i].Path < cands[j].Path
	})
	if len(cands) < 2 {
		return
	}
	lead := 0
	for i := 1; i < len(cands) && cands[0].Score-cands[i].Score < AcceptMargin; i++ {
		if affinity(cands[i].Path, hint) < affinity(cands[lead].Path, hint) {
			lead = i
		}
	}
	if lead != 0 {
		best := cands[lead]
		copy(cands[1:lead+1], cands[:lead])
		cands[0] = best
	}
}

// affinity is a smaller-is-closer rank of a candidate against the hint:
// same basename beats not, then directory distance (segments not shared
// with the hint's directory).
func affinity(p, hint record.Path) int {
	rank := 0
	if baseOf(p) != baseOf(hint) {
		rank += 1 << 16
	}
	return rank + dirDistance(p, hint)
}

func baseOf(p record.Path) string {
	s := strings.TrimSuffix(string(p), "/")
	if i := strings.LastIndexByte(s, '/'); i >= 0 {
		return s[i+1:]
	}
	return s
}

// dirOf returns the directory part of a relative slash path including the
// trailing slash, "" when flat.
func dirOf(rel string) string {
	if i := strings.LastIndexByte(rel, '/'); i >= 0 {
		return rel[:i+1]
	}
	return ""
}

// folderOf returns the folder containing p (trailing slash; "./" at root).
func folderOf(p record.Path) record.Path {
	if d := dirOf(strings.TrimSuffix(string(p), "/")); d != "" {
		return record.Path(d)
	}
	return record.RootFolder
}

// dirDistance counts the unshared directory segments between two paths.
func dirDistance(a, b record.Path) int {
	as := strings.Split(string(folderOf(a)), "/")
	bs := strings.Split(string(folderOf(b)), "/")
	common := 0
	for common < len(as) && common < len(bs) && as[common] == bs[common] {
		common++
	}
	return len(as) + len(bs) - 2*common
}
