package gitio

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"meltcloud.io/dm/internal/core/evidence"
	"meltcloud.io/dm/internal/core/record"
)

// The rewrite-forensics half of evidence (M6): the Lineage role and the
// reflog/patch-id/segment surface of Match (§9.4). Still policy-free —
// action filters, floors, and every bind/no-bind decision live in
// core/lineage; this file answers questions git can answer.

// ---- evidence.Lineage ----

// LandedInHead reports which origins are ancestors of HEAD. An origin
// whose objects are not locally available has not landed.
func (e *Evidence) LandedInHead(origins []record.SHA) (evidence.Set[record.SHA], error) {
	out := evidence.Set[record.SHA]{}
	for _, o := range origins {
		ok, err := e.IsAncestor(o, e.head)
		if err != nil {
			return nil, err
		}
		if ok {
			out[o] = struct{}{}
		}
	}
	return out, nil
}

// IsAncestor reports whether a is an ancestor of (or equal to) b, reading
// a missing object as "no" — an unfetched commit is nobody's ancestor here.
func (e *Evidence) IsAncestor(a, b record.SHA) (bool, error) {
	ok, err := e.repo.IsAncestor(a, b)
	if err != nil && isMissingObject(err) {
		return false, nil
	}
	return ok, err
}

// ReachableFrom reports, per origin, the local/remote branch refs it is
// reachable from. Origins with locally-missing objects are unreachable by
// definition (§9.4 — a wrong local conclusion self-heals on the next
// fetch). The store's own refs/dm/* namespace never counts.
func (e *Evidence) ReachableFrom(origins []record.SHA) (map[record.SHA][]evidence.RefTip, error) {
	out := make(map[record.SHA][]evidence.RefTip, len(origins))
	for _, o := range origins {
		raw, err := e.repo.git("for-each-ref", "--contains="+string(o),
			"--format=%(refname:short) %(objectname)", "refs/heads", "refs/remotes")
		if err != nil {
			if isMissingObject(err) {
				out[o] = nil
				continue
			}
			return nil, err
		}
		var refs []evidence.RefTip
		for _, line := range strings.Split(raw, "\n") {
			if line == "" {
				continue
			}
			name, tip, ok := strings.Cut(line, " ")
			if !ok {
				return nil, fmt.Errorf("unexpected for-each-ref line %q", line)
			}
			refs = append(refs, evidence.RefTip{Ref: evidence.Ref(name), Tip: record.SHA(tip)})
		}
		out[o] = refs
	}
	return out, nil
}

// RefSnapshot lists every local/remote branch ref and its tip — the refs
// fingerprint the unreachability cache revalidates under (§8.2), and the
// before/after states the forced-update collection compares.
func (r *Repo) RefSnapshot() (map[evidence.Ref]record.SHA, error) {
	out, err := r.git("for-each-ref", "--format=%(refname) %(objectname)",
		"refs/heads", "refs/remotes")
	if err != nil {
		return nil, err
	}
	snap := make(map[evidence.Ref]record.SHA)
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		name, tip, ok := strings.Cut(line, " ")
		if !ok {
			return nil, fmt.Errorf("unexpected for-each-ref line %q", line)
		}
		snap[evidence.Ref(name)] = record.SHA(tip)
	}
	return snap, nil
}

// RevList enumerates the commits reachable from include but not exclude
// ("" = none), newest first — the unreachability cache's delta scan.
func (r *Repo) RevList(include record.SHA, exclude record.SHA) ([]record.SHA, error) {
	args := []string{"rev-list", string(include)}
	if exclude != "" {
		args = append(args, "^"+string(exclude))
	}
	out, err := r.git(args...)
	if err != nil {
		return nil, err
	}
	var shas []record.SHA
	for _, line := range strings.Split(out, "\n") {
		if line != "" {
			shas = append(shas, record.SHA(line))
		}
	}
	return shas, nil
}

// IsShallow reports a shallow clone — half of the §9.4 qualified-clone
// guard's facts (the guard decision itself is core's; app assembles it).
func (r *Repo) IsShallow() (bool, error) {
	out, err := r.git("rev-parse", "--is-shallow-repository")
	if err != nil {
		return false, err
	}
	return out == "true", nil
}

// ResolveCommit resolves a commit-ish (possibly abbreviated) to its full
// SHA; nil means unknown here.
func (r *Repo) ResolveCommit(sha record.SHA) (*record.SHA, error) {
	return r.ResolveRef(string(sha))
}

// ---- evidence.Match forensics ----

// ReflogEntries returns every local branch's reflog, oldest first per
// branch, with old/new pairs reconstructed from the log chain (entry N's
// old is entry N+1's new; the creation entry has no old and is dropped).
// Core applies the m1 action filter.
func (e *Evidence) ReflogEntries() ([]evidence.ReflogEntry, error) {
	branches, err := e.repo.git("for-each-ref", "--format=%(refname:short)", "refs/heads")
	if err != nil {
		return nil, err
	}
	var out []evidence.ReflogEntry
	for _, branch := range strings.Split(branches, "\n") {
		if branch == "" {
			continue
		}
		raw, err := e.repo.git("reflog", "show", "--format=%H%x00%gs", branch)
		if err != nil {
			var ge *gitError
			if errors.As(err, &ge) && ge.code == 128 {
				continue // no reflog for this ref
			}
			return nil, err
		}
		lines := strings.Split(raw, "\n") // newest first
		for i := len(lines) - 2; i >= 0; i-- {
			sha, action, ok := strings.Cut(lines[i], "\x00")
			if !ok {
				return nil, fmt.Errorf("unexpected reflog line %q", lines[i])
			}
			oldSha, _, ok := strings.Cut(lines[i+1], "\x00")
			if !ok {
				return nil, fmt.Errorf("unexpected reflog line %q", lines[i+1])
			}
			out = append(out, evidence.ReflogEntry{
				Ref:    evidence.Ref(branch),
				Old:    record.SHA(oldSha),
				New:    record.SHA(sha),
				Action: action,
			})
		}
	}
	return out, nil
}

// MergeBase returns the merge base of a and b; nil when the histories are
// unrelated or an object is missing locally.
func (e *Evidence) MergeBase(a, b record.SHA) (*record.SHA, error) {
	out, err := e.repo.git("merge-base", string(a), string(b))
	if err != nil {
		var ge *gitError
		if (errors.As(err, &ge) && ge.code == 1) || isMissingObject(err) {
			return nil, nil
		}
		return nil, err
	}
	sha := record.SHA(out)
	return &sha, nil
}

// Segment enumerates (base..tip] oldest first ("" base = the whole line).
func (e *Evidence) Segment(base, tip record.SHA) ([]record.SHA, error) {
	args := []string{"rev-list", "--reverse", string(tip)}
	if base != "" {
		args = append(args, "^"+string(base))
	}
	out, err := e.repo.git(args...)
	if err != nil {
		return nil, err
	}
	var shas []record.SHA
	for _, line := range strings.Split(out, "\n") {
		if line != "" {
			shas = append(shas, record.SHA(line))
		}
	}
	return shas, nil
}

// PatchID computes the canonical (--stable) patch id of one commit; nil
// means an empty diff (empty commits, and merges under the pinned
// first-parent-less diff-tree flags).
func (e *Evidence) PatchID(commit record.SHA) (*evidence.PatchID, error) {
	if id, ok := e.patchIDs[commit]; ok {
		return id, nil
	}
	patch, err := e.repo.gitRaw(nil, nil, "diff-tree", "--patch", "--no-color", "--root", string(commit))
	if err != nil {
		return nil, err
	}
	id, err := e.patchIDOf(patch)
	if err != nil {
		return nil, err
	}
	e.patchIDs[commit] = id
	return id, nil
}

// RangePatchID computes the cumulative patch id over (base..tip] — the
// whole-segment diff m3 compares — plus its size in changed lines (the
// floor's unit).
func (e *Evidence) RangePatchID(base, tip record.SHA) (*evidence.RangeID, error) {
	patch, err := e.repo.gitRaw(nil, nil, "diff", "--no-color", string(base), string(tip))
	if err != nil {
		return nil, err
	}
	id, err := e.patchIDOf(patch)
	if err != nil {
		return nil, err
	}
	if id == nil {
		return nil, nil
	}
	numstat, err := e.repo.git("diff", "--numstat", string(base), string(tip))
	if err != nil {
		return nil, err
	}
	size := 0
	for _, line := range strings.Split(numstat, "\n") {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		for _, f := range fields[:2] {
			if n, err := strconv.Atoi(f); err == nil {
				size += n
			} else {
				size++ // binary file: counts as one change
			}
		}
	}
	return &evidence.RangeID{PatchID: *id, DiffSize: size}, nil
}

// patchIDOf pipes one patch through `git patch-id --stable`; nil = empty.
func (e *Evidence) patchIDOf(patch []byte) (*evidence.PatchID, error) {
	if len(patch) == 0 {
		return nil, nil
	}
	out, err := e.repo.gitRaw(patch, nil, "patch-id", "--stable")
	if err != nil {
		return nil, err
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		return nil, nil // header-only input (e.g. a merge commit's bare diff-tree line)
	}
	id := evidence.PatchID(fields[0])
	return &id, nil
}
