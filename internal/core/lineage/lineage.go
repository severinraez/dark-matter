// Package lineage owns rule-(b) classification — VD chains, unlanded
// voiding, the qualified/degraded-clone guard — and the matcher ladder
// (m1–m3 guards, mint pass, floors) (design.md §9.4; architecture.md §6).
//
// Evidence roles consumed: Lineage, Match. Classification feeds core/fold
// as plain data (map[origin]landed), keeping fold port-free.
package lineage

import (
	"sort"

	"meltcloud.io/dm/internal/core/evidence"
	"meltcloud.io/dm/internal/core/record"
)

// MaxChainDepth bounds transitive VD resolution (§9.4 fold step 2: a
// landed-as commit may itself have been rewritten). Chains of real rewrites
// are short; the bound only guards degenerate or cyclic verdict data.
const MaxChainDepth = 16

// State is an origin's rule-(b) classification per checkout (§9.4).
type State int

const (
	// Landed: the origin (or a commit its verdict chain lands as) is an
	// ancestor of HEAD — records with this origin participate in the fold.
	Landed State = iota
	// PendingElsewhere: the origin's line exists on another local/remote
	// ref but has not landed here. Hidden, never worklisted (§5.3).
	PendingElsewhere
	// Abandoned: origin unreachable from every ref, no landing binding —
	// derived per read on a qualified clone, never stored (§9.4).
	Abandoned
	// Unknown: a degraded clone's unresolvable origin — behaves exactly
	// like pending-elsewhere (hidden, not worklisted), local-only.
	Unknown
)

// String returns the state's worklist/debug name.
func (s State) String() string {
	switch s {
	case Landed:
		return "landed"
	case PendingElsewhere:
		return "pending-elsewhere"
	case Abandoned:
		return "abandoned"
	}
	return "unknown"
}

// Classification is one origin's rule-(b) answer.
type Classification struct {
	State State
	// Refs are the refs the origin is reachable from (pending-elsewhere) —
	// their tips are the mint pass's m3 candidates and the worklist's
	// branch hints.
	Refs []evidence.RefTip
	// Terminal is the end of the origin's verdict chain — the commit whose
	// reachability decided a non-landed state (the origin itself when no
	// binding exists).
	Terminal record.SHA
}

// WinningVerdicts folds VD records to the winning verdict per origin —
// largest rec-id wins; an unlanded winner voids the binding but stays in
// the map (it blocks re-inference, §9.4 m5).
func WinningVerdicts(recs []record.Record) map[record.SHA]record.Verdict {
	out := make(map[record.SHA]record.Verdict)
	for _, r := range recs {
		vd, ok := r.(record.Verdict)
		if !ok {
			continue
		}
		if cur, seen := out[vd.Origin]; !seen || cur.Rec.Less(vd.Rec) {
			out[vd.Origin] = vd
		}
	}
	return out
}

// LandedBindings reduces winning verdicts to the origin → landed-as map
// the classifier chains through; voided (unlanded-winner) origins drop.
func LandedBindings(winning map[record.SHA]record.Verdict) map[record.SHA]record.SHA {
	out := make(map[record.SHA]record.SHA, len(winning))
	for origin, vd := range winning {
		if vd.Landed {
			out[origin] = vd.LandedAs
		}
	}
	return out
}

// Classify runs the §9.4 fold for every origin against the checkout:
//
//  1. ancestor of HEAD → landed;
//  2. winning landing binding → resolve the landed-as commit through the
//     same fold (transitive, depth-bounded);
//  3. reachable from another local/remote ref → pending-elsewhere;
//  4. else abandoned (qualified clone) or unknown (degraded).
//
// Evidence calls are batched: one LandedInHead over the chain closure, one
// ReachableFrom over the chain terminals.
func Classify(ev evidence.Lineage, bindings map[record.SHA]record.SHA, qualified bool, origins []record.SHA) (map[record.SHA]Classification, error) {
	// Chain closure: every origin plus everything its bindings lead to.
	var closure []record.SHA
	inClosure := make(map[record.SHA]bool)
	add := func(s record.SHA) {
		if !inClosure[s] {
			inClosure[s] = true
			closure = append(closure, s)
		}
	}
	for _, o := range origins {
		s := o
		add(s)
		for d := 0; d < MaxChainDepth; d++ {
			m, ok := bindings[s]
			if !ok || inClosure[m] {
				break
			}
			add(m)
			s = m
		}
	}
	landed, err := ev.LandedInHead(closure)
	if err != nil {
		return nil, err
	}

	// Walk each origin's chain against the landed set; collect terminals
	// of the still-unlanded ones for one reachability batch.
	out := make(map[record.SHA]Classification, len(origins))
	terminalOf := make(map[record.SHA]record.SHA)
	var terminals []record.SHA
	seenTerminal := make(map[record.SHA]bool)
	for _, o := range origins {
		if _, done := out[o]; done {
			continue
		}
		s := o
		visited := map[record.SHA]bool{s: true}
		isLanded := false
		for d := 0; ; d++ {
			if _, ok := landed[s]; ok {
				isLanded = true
				break
			}
			m, ok := bindings[s]
			if !ok || d >= MaxChainDepth || visited[m] {
				break
			}
			visited[m] = true
			s = m
		}
		if isLanded {
			out[o] = Classification{State: Landed, Terminal: s}
			continue
		}
		terminalOf[o] = s
		if !seenTerminal[s] {
			seenTerminal[s] = true
			terminals = append(terminals, s)
		}
	}
	if len(terminals) == 0 {
		return out, nil
	}
	reach, err := ev.ReachableFrom(terminals)
	if err != nil {
		return nil, err
	}
	for o, t := range terminalOf {
		refs := reach[t]
		switch {
		case len(refs) > 0:
			sort.Slice(refs, func(i, j int) bool { return refs[i].Ref < refs[j].Ref })
			out[o] = Classification{State: PendingElsewhere, Refs: refs, Terminal: t}
		case qualified:
			out[o] = Classification{State: Abandoned, Terminal: t}
		default:
			out[o] = Classification{State: Unknown, Terminal: t}
		}
	}
	return out, nil
}
