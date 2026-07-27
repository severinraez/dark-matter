package gitio

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"meltcloud.io/dm/internal/core/evidence"
	"meltcloud.io/dm/internal/core/record"
)

// Evidence is gitio's implementation of the evidence.Tree and
// evidence.Match roles (architecture.md §5), bound to one checkout snapshot
// (HEAD + working tree) for one locked invocation. The Lineage role and the
// rewrite-forensics half of Match (reflog, patch-ids, segments) arrive with
// M6 — their methods error until then; core's M5 consumers never call them.
//
// Faithful and policy-free: rename/copy detection runs at a hardwired
// generous floor (renameFloor) and returns raw scores; every band, margin,
// and guard lives in core/resolve.
type Evidence struct {
	repo *Repo
	head record.SHA

	// Checkout manifest: every path → blob of the working tree (HEAD's
	// tree overlaid with status dirt), built lazily once per snapshot.
	manifest map[record.Path]record.SHA
	byBlob   map[record.SHA][]record.Path
	clean    bool // no status dirt: the manifest equals HEAD's tree
	loaded   bool

	pairs   map[record.SHA][]evidence.RenamePair // per-origin diff memo
	treeAt  map[string]*evidence.TreeFP          // (commit,path) memo
	headTop map[record.SHA][]record.Path         // tree SHA → HEAD dirs, lazy

	// BlobSource supplies anchored-blob bytes the object database lacks —
	// a not-yet-synced dirty anchor staged under pending/blobs (§8.1).
	// Wired by evcache; nil falls back to the odb alone.
	BlobSource func(record.SHA) ([]byte, bool)
}

// renameFloor is the generous fixed detection floor (percent) for the
// batched origin→checkout similarity diff; core applies the real §9.2
// bands on top of the raw scores.
const renameFloor = 25

// Evidence binds a snapshot value to the repo's current HEAD and working
// tree.
func (r *Repo) Evidence(head record.SHA) *Evidence {
	return &Evidence{
		repo:   r,
		head:   head,
		pairs:  make(map[record.SHA][]evidence.RenamePair),
		treeAt: make(map[string]*evidence.TreeFP),
	}
}

// Head returns the snapshot's HEAD commit.
func (e *Evidence) Head() record.SHA { return e.head }

// HeadTree returns HEAD's tree SHA — the checkout half of the match-memo
// key (§8.2).
func (e *Evidence) HeadTree() (record.SHA, error) {
	return e.repo.TreeOf(e.head)
}

// Clean reports whether the working tree carries no dirt over HEAD — the
// condition under which origin→checkout diff results are keyed by HEAD's
// tree and therefore cacheable (§8.2: the working tree itself has no
// stable key to memoize under).
func (e *Evidence) Clean() (bool, error) {
	if err := e.load(); err != nil {
		return false, err
	}
	return e.clean, nil
}

// ---- evidence.Tree ----

// WorkingBlob reports the blob at path in the checkout; nil = absent.
func (e *Evidence) WorkingBlob(path record.Path) (*record.SHA, error) {
	if err := e.load(); err != nil {
		return nil, err
	}
	sha, ok := e.manifest[path]
	if !ok {
		return nil, nil
	}
	return &sha, nil
}

// PathsOf lists the checkout paths holding blob, sorted.
func (e *Evidence) PathsOf(blob record.SHA) ([]record.Path, error) {
	if err := e.load(); err != nil {
		return nil, err
	}
	return e.byBlob[blob], nil
}

// PathsUnder lists the checkout paths inside folder, sorted.
func (e *Evidence) PathsUnder(folder record.Path) ([]record.Path, error) {
	if err := e.load(); err != nil {
		return nil, err
	}
	var out []record.Path
	for p := range e.manifest {
		if p.Under(folder) {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

// TreeAt returns the folder fingerprint at path in commit's tree; nil when
// the path is absent there, is not a folder, or the commit's objects are
// not locally available (an unfetched foreign origin — no fingerprint is
// derivable, §9.3).
func (e *Evidence) TreeAt(commit record.SHA, path record.Path) (*evidence.TreeFP, error) {
	key := string(commit) + ":" + string(path)
	if fp, ok := e.treeAt[key]; ok {
		return fp, nil
	}
	spec := string(commit) + "^{tree}"
	if path != record.RootFolder {
		spec = string(commit) + ":" + strings.TrimSuffix(string(path), "/")
	}
	out, err := e.repo.git("rev-parse", "--verify", "--quiet", spec)
	if err != nil {
		var ge *gitError
		if (errors.As(err, &ge) && ge.code == 1) || isMissingObject(err) {
			// Path absent there, or the commit's objects are not local.
			e.treeAt[key] = nil
			return nil, nil
		}
		return nil, err
	}
	sha := record.SHA(out)
	if typ, err := e.repo.git("cat-file", "-t", out); err != nil || typ != "tree" {
		if err != nil {
			return nil, err
		}
		e.treeAt[key] = nil // a file, not a folder
		return nil, nil
	}
	entries, err := e.repo.LsTree(sha)
	if err != nil {
		return nil, err
	}
	fp := &evidence.TreeFP{Tree: sha}
	for _, en := range entries {
		fp.Members = append(fp.Members, record.Path(en.Path))
	}
	e.treeAt[key] = fp
	return fp, nil
}

// PathsOfTree lists the checkout folders whose committed tree object is
// tree (HEAD side — a pure move preserves the tree SHA by construction,
// §9.3; uncommitted moves are caught by the file-level rename vote).
func (e *Evidence) PathsOfTree(tree record.SHA) ([]record.Path, error) {
	if e.headTop == nil {
		byTree := make(map[record.SHA][]record.Path)
		root, err := e.repo.TreeOf(e.head)
		if err != nil {
			return nil, err
		}
		byTree[root] = append(byTree[root], record.RootFolder)
		out, err := e.repo.git("ls-tree", "-r", "-d", "-z", string(e.head))
		if err != nil {
			return nil, err
		}
		for _, line := range strings.Split(out, "\x00") {
			if line == "" {
				continue
			}
			meta, path, ok := strings.Cut(line, "\t")
			fields := strings.Fields(meta)
			if !ok || len(fields) != 3 {
				return nil, fmt.Errorf("unexpected ls-tree entry %q", line)
			}
			sha := record.SHA(fields[2])
			byTree[sha] = append(byTree[sha], record.Path(path+"/"))
		}
		e.headTop = byTree
	}
	return e.headTop[tree], nil
}

// ---- evidence.Match (the §9.2 similarity half; forensics are M6) ----

// RenamePairs runs the batched origin→checkout similarity diff (-M -C at
// the generous floor) and returns every rename/copy pairing with git's raw
// score. An origin whose objects are not locally available yields no pairs.
func (e *Evidence) RenamePairs(origin record.SHA) ([]evidence.RenamePair, error) {
	if pairs, ok := e.pairs[origin]; ok {
		return pairs, nil
	}
	floor := strconv.Itoa(renameFloor) + "%"
	out, err := e.repo.gitRaw(nil, nil, "diff", "--raw", "-z",
		"--find-renames="+floor, "--find-copies="+floor, string(origin))
	if err != nil {
		if isMissingObject(err) {
			e.pairs[origin] = nil
			return nil, nil
		}
		return nil, err
	}
	pairs, err := parseRawRenames(string(out))
	if err != nil {
		return nil, err
	}
	e.pairs[origin] = pairs
	return pairs, nil
}

// parseRawRenames extracts R/C entries from `diff --raw -z` output:
// `:<mode> <mode> <sha> <sha> <status>` NUL path NUL [path NUL].
func parseRawRenames(out string) ([]evidence.RenamePair, error) {
	fields := strings.Split(out, "\x00")
	var pairs []evidence.RenamePair
	for i := 0; i < len(fields); {
		header := fields[i]
		if header == "" {
			i++
			continue
		}
		if !strings.HasPrefix(header, ":") {
			return nil, fmt.Errorf("unexpected diff --raw entry %q", header)
		}
		h := strings.Fields(header[1:])
		if len(h) != 5 {
			return nil, fmt.Errorf("unexpected diff --raw header %q", header)
		}
		status := h[4]
		twoPaths := status[0] == 'R' || status[0] == 'C'
		if i+1 >= len(fields) || (twoPaths && i+2 >= len(fields)) {
			return nil, errors.New("diff --raw output truncated")
		}
		if !twoPaths {
			i += 2
			continue
		}
		score, err := strconv.Atoi(status[1:])
		if err != nil {
			return nil, fmt.Errorf("unexpected similarity status %q", status)
		}
		pairs = append(pairs, evidence.RenamePair{
			From:     record.Path(fields[i+1]),
			To:       record.Path(fields[i+2]),
			FromBlob: record.SHA(h[2]),
			ToBlob:   record.SHA(h[3]),
			Score:    score,
		})
		i += 3
	}
	return pairs, nil
}

// Score rates blob against each candidate's checkout content, 0–100 — the
// direct blob-vs-candidate scoring §9.2 calls for (dm can beat stock git by
// scoring against the anchored blob itself; also covers anchors HEAD never
// contained, e.g. a dirty-file `k`). An anchored blob missing from the
// object database scores nothing (empty map) — no similarity is derivable.
func (e *Evidence) Score(blob record.SHA, candidates []record.Path) (map[record.Path]int, error) {
	out := make(map[record.Path]int, len(candidates))
	if len(candidates) == 0 {
		return out, nil
	}
	anchored, err := e.repo.CatBlob(blob)
	if err != nil {
		if !isMissingObject(err) {
			return nil, err
		}
		staged, ok := []byte(nil), false
		if e.BlobSource != nil {
			staged, ok = e.BlobSource(blob)
		}
		if !ok {
			return out, nil
		}
		anchored = staged
	}
	for _, cand := range candidates {
		content, err := os.ReadFile(filepath.Join(e.repo.Top, filepath.FromSlash(string(cand))))
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, err
		}
		out[cand] = similarity(anchored, content)
	}
	return out, nil
}

// similarity is the small line-hash estimator (plan.md: deliberate
// hand-roll): the byte weight of the line multiset intersection over the
// larger side, like git's rename scoring in spirit. Exact fractions are
// Q1-calibration territory; this only needs to order candidates sanely
// against the §9.2 bands.
func similarity(a, b []byte) int {
	if len(a) == 0 && len(b) == 0 {
		return 100
	}
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	counts := make(map[string]int)
	for _, line := range strings.SplitAfter(string(a), "\n") {
		counts[line] += len(line)
	}
	common := 0
	for _, line := range strings.SplitAfter(string(b), "\n") {
		if counts[line] >= len(line) && len(line) > 0 {
			counts[line] -= len(line)
			common += len(line)
		}
	}
	max := len(a)
	if len(b) > max {
		max = len(b)
	}
	return 100 * common / max
}

// ---- M6 forensics stubs (evidence.Match completeness) ----

var errM6 = errors.New("gitio: rewrite forensics arrive with M6")

func (e *Evidence) ReflogEntries() ([]evidence.ReflogEntry, error)          { return nil, errM6 }
func (e *Evidence) MergeBase(a, b record.SHA) (*record.SHA, error)          { return nil, errM6 }
func (e *Evidence) Segment(base, tip record.SHA) ([]evidence.Commit, error) { return nil, errM6 }
func (e *Evidence) PatchID(commit record.SHA) (*evidence.PatchID, error)    { return nil, errM6 }
func (e *Evidence) RangePatchID(base, tip record.SHA) (*evidence.RangeID, error) {
	return nil, errM6
}

// ---- checkout manifest ----

// load builds the manifest once: HEAD's tree overlaid with status dirt
// (edits, adds, deletes, untracked files — the checkout is what the agent
// sees, §2.1 rows 7/10/11).
func (e *Evidence) load() error {
	if e.loaded {
		return nil
	}
	manifest := make(map[record.Path]record.SHA)
	entries, err := e.repo.LsTree(e.head)
	if err != nil {
		return err
	}
	for _, en := range entries {
		manifest[record.Path(en.Path)] = en.SHA
	}
	dirt, err := e.repo.statusPaths()
	if err != nil {
		return err
	}
	var rehash []record.Path
	for _, p := range dirt {
		fi, err := os.Lstat(filepath.Join(e.repo.Top, filepath.FromSlash(string(p))))
		if err != nil || !fi.Mode().IsRegular() {
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			delete(manifest, p) // deleted, or not a regular file
			continue
		}
		rehash = append(rehash, p)
	}
	hashed, err := e.repo.hashFiles(rehash)
	if err != nil {
		return err
	}
	for p, sha := range hashed {
		manifest[p] = sha
	}
	e.manifest = manifest
	e.clean = len(dirt) == 0
	e.byBlob = make(map[record.SHA][]record.Path)
	for p, sha := range manifest {
		e.byBlob[sha] = append(e.byBlob[sha], p)
	}
	for _, paths := range e.byBlob {
		sort.Slice(paths, func(i, j int) bool { return paths[i] < paths[j] })
	}
	e.loaded = true
	return nil
}

// statusPaths lists every path the working tree changes over HEAD
// (modified, added, deleted, or untracked), repo-relative.
func (r *Repo) statusPaths() ([]record.Path, error) {
	out, err := r.gitRaw(nil, nil, "status", "--porcelain", "-z",
		"--untracked-files=all", "--no-renames")
	if err != nil {
		return nil, err
	}
	var paths []record.Path
	for _, entry := range strings.Split(string(out), "\x00") {
		if len(entry) < 4 {
			continue
		}
		paths = append(paths, record.Path(entry[3:]))
	}
	return paths, nil
}

// hashFiles hashes working-tree files as git would check them in, batched
// over one hash-object process (paths containing a newline fall back to
// per-file hashing — stdin-paths is line-framed).
func (r *Repo) hashFiles(paths []record.Path) (map[record.Path]record.SHA, error) {
	out := make(map[record.Path]record.SHA, len(paths))
	var batched []record.Path
	for _, p := range paths {
		if strings.ContainsRune(string(p), '\n') {
			sha, err := r.WorkingBlob(p)
			if err != nil {
				return nil, err
			}
			if sha != nil {
				out[p] = *sha
			}
			continue
		}
		batched = append(batched, p)
	}
	if len(batched) == 0 {
		return out, nil
	}
	var stdin strings.Builder
	for _, p := range batched {
		stdin.WriteString(string(p))
		stdin.WriteByte('\n')
	}
	raw, err := r.gitRaw([]byte(stdin.String()), nil, "hash-object", "--stdin-paths")
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	if len(lines) != len(batched) {
		return nil, fmt.Errorf("hash-object --stdin-paths: %d results for %d paths", len(lines), len(batched))
	}
	for i, p := range batched {
		out[p] = record.SHA(lines[i])
	}
	return out, nil
}

// isMissingObject reports a git failure caused by an object that is not
// locally available (an unfetched foreign origin) rather than by a broken
// invocation — the case evidence answers degrade on instead of erroring.
func isMissingObject(err error) bool {
	var ge *gitError
	if !errors.As(err, &ge) {
		return false
	}
	msg := ge.stderr
	for _, marker := range []string{
		"bad object", "unknown revision", "Not a valid object name",
		"invalid object name", "bad revision", "bad file", "does not exist in",
	} {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}
