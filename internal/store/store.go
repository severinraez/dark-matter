// Package store owns the store tree layout (design.md §8.1: record shards,
// stats files, blobs, epoch marker), tip ⇄ record-set materialization, and
// candidate commit builds. It rides gitio for object/ref plumbing and makes
// no merge or sweep decisions (those are core/union's).
package store

import (
	"fmt"
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
			stats, err := union.ParseStats(contents[sha])
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
	return s.commit(set, base, baseSet, blobSrc, "dm sync", when)
}

// Compact builds the §8.7 orphan re-commit: the swept set as a full tree
// built from scratch, no parents — the store's history carries no
// information, only the tip tree is data. The build is canonical (git
// trees are content-addressed), so two concurrent compactors over the same
// tip produce the same tree SHA and the gc race is benign.
func (s *Store) Compact(set union.Set, blobSrc func(record.SHA) ([]byte, bool), when time.Time) (record.SHA, error) {
	return s.commit(set, nil, union.NewSet(), blobSrc, "dm gc", when)
}

// TipBlobSource returns a blobSrc reading anchored-blob content out of a
// tip's own blobs/ tree. The path carries the anchor (§8.1), so the lookup
// holds even where filters made the git object name differ from the anchor
// SHA — which is why gc sources the surviving blobs here, not from the
// object database by anchor.
func (s *Store) TipBlobSource(tip record.SHA) (func(record.SHA) ([]byte, bool), error) {
	entries, err := s.Repo.LsTree(tip)
	if err != nil {
		return nil, err
	}
	objects := map[record.SHA]record.SHA{}
	for _, e := range entries {
		parts := strings.Split(e.Path, "/")
		if len(parts) == 3 && parts[0] == "blobs" {
			objects[record.SHA(parts[1]+parts[2])] = e.SHA
		}
	}
	return func(anchor record.SHA) ([]byte, bool) {
		obj, ok := objects[anchor]
		if !ok {
			return nil, false
		}
		content, err := s.Repo.CatBlob(obj)
		return content, err == nil
	}, nil
}

func (s *Store) commit(set union.Set, base *record.SHA, baseSet union.Set, blobSrc func(record.SHA) ([]byte, bool), msg string, when time.Time) (record.SHA, error) {
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
		serialized := union.FormatStats(stats)
		if baseStats, ok := baseSet.Stats[replica]; ok && string(union.FormatStats(baseStats)) == string(serialized) {
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
	return s.Repo.CommitTree(tree, parents, msg, when)
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
