// Package store owns the store tree layout (design.md §8.1: record shards,
// stats files, blobs, epoch marker), tip ⇄ record-set materialization, and
// candidate commit builds. It rides gitio for object/ref plumbing and makes
// no merge or sweep decisions (those are core/union's).
package store

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"meltcloud.io/dm/internal/core/record"
	"meltcloud.io/dm/internal/core/union"
	"meltcloud.io/dm/internal/gitio"
)

// Ref is the one shared metadata branch (§8.1).
const Ref = "refs/dm/store"

// Refspec is the fetch refspec dm init configures; forced, because a
// compaction rewrites the branch (epoch bump, §8.7) and the local ref is a
// remote mirror — it only ever moves at fetch and successful push.
const Refspec = "+refs/dm/*:refs/dm/*"

// Store materializes and builds store commits in one repository.
type Store struct {
	Repo *gitio.Repo
}

// Tip resolves the local store ref; nil means no store branch yet.
func (s *Store) Tip() (*record.SHA, error) {
	return s.Repo.ResolveRef(Ref)
}

// Read materializes a tip's policy-relevant content (union.Set). A nil tip
// reads as the empty set at epoch 0. Unknown paths are skipped — commit
// builds start from the base tree, so content this version doesn't
// understand rides along untouched.
func (s *Store) Read(tip *record.SHA) (union.Set, error) {
	set := union.NewSet()
	if tip == nil {
		return set, nil
	}
	entries, err := s.Repo.LsTree(*tip)
	if err != nil {
		return union.Set{}, err
	}
	var recordSHAs []record.SHA
	recordIDs := map[record.SHA][]record.RecID{}
	var statSHAs []record.SHA
	statReplica := map[record.SHA][]string{}
	var epochSHA *record.SHA
	for _, e := range entries {
		parts := strings.Split(e.Path, "/")
		switch {
		case e.Path == "epoch":
			sha := e.SHA
			epochSHA = &sha
		case parts[0] == "records" && len(parts) == 3:
			id, err := record.ParseRecID(parts[2])
			if err != nil {
				return union.Set{}, fmt.Errorf("store record %s: %w", e.Path, err)
			}
			recordSHAs = append(recordSHAs, e.SHA)
			recordIDs[e.SHA] = append(recordIDs[e.SHA], id)
		case parts[0] == "stats" && len(parts) == 2:
			statSHAs = append(statSHAs, e.SHA)
			statReplica[e.SHA] = append(statReplica[e.SHA], parts[1])
		case parts[0] == "blobs" && len(parts) == 3:
			set.Blobs[record.SHA(parts[1]+parts[2])] = true
		}
	}
	if epochSHA != nil {
		content, err := s.Repo.CatBlob(*epochSHA)
		if err != nil {
			return union.Set{}, err
		}
		epoch, err := strconv.ParseUint(strings.TrimSpace(string(content)), 10, 64)
		if err != nil {
			return union.Set{}, fmt.Errorf("store epoch marker: %w", err)
		}
		set.Epoch = epoch
	}
	contents, err := s.Repo.CatBlobs(append(append([]record.SHA{}, recordSHAs...), statSHAs...))
	if err != nil {
		return union.Set{}, err
	}
	for sha, ids := range recordIDs {
		for _, id := range ids {
			set.Records[id] = contents[sha]
		}
	}
	for sha, replicas := range statReplica {
		for _, replica := range replicas {
			stats, err := parseStats(contents[sha])
			if err != nil {
				return union.Set{}, fmt.Errorf("stats/%s: %w", replica, err)
			}
			set.Stats[replica] = stats
		}
	}
	return set, nil
}

// Commit builds the candidate store commit for set: the base tip's tree
// plus whatever set adds over base, parented on base (§8.4 — sync commits
// are plain fast-forwards). blobSrc supplies staged-blob content for blob
// SHAs absent from base (nil falls back to the object database alone).
// Returns the commit and its tree; when set adds nothing, the returned
// commit is *base itself* — the caller detects nothing-to-push by
// comparing.
func (s *Store) Commit(set union.Set, base *record.SHA, baseSet union.Set, blobSrc func(record.SHA) ([]byte, bool), when time.Time) (record.SHA, error) {
	var add []gitio.TreeEntry
	for id, data := range set.Records {
		if _, ok := baseSet.Records[id]; ok {
			continue
		}
		sha, err := s.Repo.WriteBlob(data)
		if err != nil {
			return "", err
		}
		add = append(add, gitio.TreeEntry{SHA: sha, Path: recordPath(id)})
	}
	for blob := range set.Blobs {
		if baseSet.Blobs[blob] {
			continue
		}
		content, staged := []byte(nil), false
		if blobSrc != nil {
			content, staged = blobSrc(blob)
		}
		if !staged {
			var err error
			if content, err = s.Repo.CatBlob(blob); err != nil {
				return "", fmt.Errorf("anchored blob %s: not staged and not in the object database: %w", blob, err)
			}
		}
		// Content is written as-is; for unfiltered content the object name
		// equals the anchor SHA, so git dedups it against any commit that
		// already contains the file. The path carries the anchor either way
		// — record → blob is a path address (§8.1).
		sha, err := s.Repo.WriteBlob(content)
		if err != nil {
			return "", err
		}
		add = append(add, gitio.TreeEntry{SHA: sha, Path: blobPath(blob)})
	}
	for replica, stats := range set.Stats {
		serialized := formatStats(stats)
		if baseStats, ok := baseSet.Stats[replica]; ok && string(formatStats(baseStats)) == string(serialized) {
			continue
		}
		sha, err := s.Repo.WriteBlob(serialized)
		if err != nil {
			return "", err
		}
		add = append(add, gitio.TreeEntry{SHA: sha, Path: "stats/" + replica})
	}
	if base == nil || set.Epoch != baseSet.Epoch {
		sha, err := s.Repo.WriteBlob([]byte(strconv.FormatUint(set.Epoch, 10) + "\n"))
		if err != nil {
			return "", err
		}
		add = append(add, gitio.TreeEntry{SHA: sha, Path: "epoch"})
	}
	if len(add) == 0 && base != nil {
		return *base, nil
	}
	var baseTree *record.SHA
	var parents []record.SHA
	if base != nil {
		tree, err := s.Repo.TreeOf(*base)
		if err != nil {
			return "", err
		}
		baseTree = &tree
		parents = []record.SHA{*base}
	}
	tree, err := s.Repo.BuildTree(baseTree, add)
	if err != nil {
		return "", err
	}
	return s.Repo.CommitTree(tree, parents, "dm sync", when)
}

// recordPath shards by the rec-id's leading time chars so tree objects
// stay small and old shards freeze (§8.1).
func recordPath(id record.RecID) string {
	name := id.String()
	return "records/" + name[:4] + "/" + name
}

// blobPath content-addresses like git's odb layout (§8.1).
func blobPath(sha record.SHA) string {
	return "blobs/" + string(sha[:2]) + "/" + string(sha[2:])
}

// ---- stats file codec (§8.1) ----
//
// One file per replica, one row per entry that has stats:
// `<entry-id> i:<n> x:<n> h:<n> t:<ms>`. Canonical form writes every field
// in that order, rows sorted by entry-id; the parser takes any subset
// (missing field = zero, §7.2).

func formatStats(stats union.Stats) []byte {
	ids := make([]string, 0, len(stats))
	byID := make(map[string]union.Row, len(stats))
	for id, row := range stats {
		ids = append(ids, id.String())
		byID[id.String()] = row
	}
	sort.Strings(ids)
	var b strings.Builder
	for _, id := range ids {
		row := byID[id]
		fmt.Fprintf(&b, "%s i:%d x:%d h:%d t:%d\n", id, row.I, row.X, row.H, row.T)
	}
	return []byte(b.String())
}

func parseStats(content []byte) (union.Stats, error) {
	stats := union.Stats{}
	for _, line := range strings.Split(string(content), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		id, err := record.ParseEntryID(fields[0])
		if err != nil {
			return nil, err
		}
		var row union.Row
		for _, f := range fields[1:] {
			key, val, ok := strings.Cut(f, ":")
			if !ok {
				return nil, fmt.Errorf("bad stat field %q", f)
			}
			n, err := strconv.ParseUint(val, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("bad stat field %q: %w", f, err)
			}
			switch key {
			case "i":
				row.I = n
			case "x":
				row.X = n
			case "h":
				row.H = n
			case "t":
				row.T = n
			default:
				return nil, fmt.Errorf("unknown stat field %q", f)
			}
		}
		stats[id] = row
	}
	return stats, nil
}
