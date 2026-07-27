// Package evidence defines the core-owned Evidence port: three role
// interfaces plus their value types (architecture.md §5). Interfaces only —
// no logic lives here.
//
// A method belongs to the role of the question it answers, never the
// consumer; consumers freely take several roles. An evidence value is bound
// to one checkout snapshot (HEAD + working tree) for one locked invocation;
// mint-pass targets are method parameters, never new snapshots.
//
// Facts are generous, judgment is core's: every threshold, band, margin,
// floor, and action-filter lives in the consuming core packages; adapters
// return raw scored/labeled facts down to a hardwired generous floor.
package evidence

import "meltcloud.io/dm/internal/core/record"

// Set is a plain value set.
type Set[T comparable] map[T]struct{}

// Ref names a local or remote ref (the worklist's branch hint).
type Ref string

// RefTip is one ref an origin is reachable from, with its tip — the tip is
// what the mint pass attempts to land (§9.4 fold step 3) and what the
// worklist's line grouping keys on (§9.6).
type RefTip struct {
	Ref Ref
	Tip record.SHA
}

// TreeFP is a folder fingerprint: the tree object at a path plus its member
// files, taken from a commit's tree (design.md §9.3).
type TreeFP struct {
	Tree    record.SHA
	Members []record.Path
}

// RenamePair is one rename/copy pairing from the batched origin→checkout
// similarity diff (-M -C), with git's raw similarity score.
type RenamePair struct {
	From     record.Path
	To       record.Path
	FromBlob record.SHA
	ToBlob   record.SHA
	Score    int // raw similarity percentage, uninterpreted
}

// ReflogEntry is one raw reflog line; core applies the m1 action filter.
type ReflogEntry struct {
	Ref    Ref
	Old    record.SHA
	New    record.SHA
	Action string
}

// PatchID is a canonical per-commit patch id (pinned diff flags).
type PatchID string

// RangeID is a cumulative patch id over a segment plus its diff size (the
// m3 matcher and its floor).
type RangeID struct {
	PatchID  PatchID
	DiffSize int
}

// Lineage answers rule-(b) ancestry and reachability questions
// (design.md §9.4).
type Lineage interface {
	// LandedInHead reports which origins are ancestors of HEAD — rule (b)
	// step 1, batched: one rev-list walk classifies all origins.
	LandedInHead(origins []record.SHA) (Set[record.SHA], error)
	// ReachableFrom reports, per origin, the refs it is reachable from —
	// rule (b) step 3, batched. An origin whose objects are not locally
	// available is unreachable by definition. The decorator applies the
	// ff-aware unreachability cache (§8.2).
	ReachableFrom(origins []record.SHA) (map[record.SHA][]RefTip, error)
	// IsAncestor reports whether a is an ancestor of b — worklist
	// line-grouping among a small set of dead origins (§9.6).
	IsAncestor(a, b record.SHA) (bool, error)
}

// Tree answers rule-(a) presence questions against the checkout snapshot
// (design.md §9.1, §9.3).
type Tree interface {
	// WorkingBlob hashes the working-tree file at path; nil means absent.
	WorkingBlob(path record.Path) (*record.SHA, error)
	// PathsOf lists the checkout paths holding blob, including
	// working-tree dirt.
	PathsOf(blob record.SHA) ([]record.Path, error)
	// PathsUnder lists the checkout paths inside a folder — rule (a)
	// folder layer 1 (existence) and the §9.3 absorbed check (foreign
	// member fraction). Includes working-tree dirt.
	PathsUnder(folder record.Path) ([]record.Path, error)
	// TreeAt returns the folder fingerprint at path in commit's tree; nil
	// means the path is absent there.
	TreeAt(commit record.SHA, path record.Path) (*TreeFP, error)
	// PathsOfTree lists the checkout paths whose tree object is tree —
	// folder layer 2, pure-move detection.
	PathsOfTree(tree record.SHA) ([]record.Path, error)
}

// Match answers similarity and rewrite-forensics questions (design.md
// §9.2, §9.4).
type Match interface {
	// RenamePairs runs the batched origin→checkout similarity diff — one
	// call per distinct origin; one diff yields all pairs.
	RenamePairs(origin record.SHA) ([]RenamePair, error)
	// Score scores blob directly against candidate paths (dirty working
	// tree case).
	Score(blob record.SHA, candidates []record.Path) (map[record.Path]int, error)
	// ReflogEntries returns raw reflog lines, oldest first per ref; core
	// filters actions (m1).
	ReflogEntries() ([]ReflogEntry, error)
	// MergeBase returns the merge base of a and b; nil means none.
	MergeBase(a, b record.SHA) (*record.SHA, error)
	// Segment enumerates (base..tip] oldest-first ("" base = the whole
	// line). Empty-diff commits are detected by PatchID returning nil —
	// the m2 skip rule.
	Segment(base, tip record.SHA) ([]record.SHA, error)
	// PatchID computes the canonical patch id of commit; nil means the
	// commit has an empty diff.
	PatchID(commit record.SHA) (*PatchID, error)
	// RangePatchID computes the cumulative patch id and diff size over
	// (base..tip] (m3 and its floor); nil means an empty range diff.
	RangePatchID(base, tip record.SHA) (*RangeID, error)
}
