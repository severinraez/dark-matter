// Package local owns .git/.dm/* (design.md §8.2): pending records, blobs
// and stats, the replica id, cache file mechanics, and the invocation lock.
// Cache keys and invalidation semantics are dictated from above. M2 carries
// the walking-skeleton subset — pending append/scan, blob staging, replica,
// lock; the cache mechanics arrive with M4/M5.
package local

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"meltcloud.io/dm/internal/core/record"
)

// Dir is a clone's .dm state directory inside the common git dir. Living
// there keeps it invisible to git status, per-clone, and shared across all
// code worktrees (§8.2).
type Dir struct {
	Root string // <git-common-dir>/.dm
}

// At returns the state dir for a git dir; nothing is touched on disk.
func At(gitDir string) *Dir {
	return &Dir{Root: filepath.Join(gitDir, ".dm")}
}

func (d *Dir) replicaPath() string       { return filepath.Join(d.Root, "replica") }
func (d *Dir) pendingRecordsDir() string { return filepath.Join(d.Root, "pending", "records") }
func (d *Dir) pendingBlobsDir() string   { return filepath.Join(d.Root, "pending", "blobs") }
func (d *Dir) cacheDir() string          { return filepath.Join(d.Root, "cache") }
func (d *Dir) lockPath() string          { return filepath.Join(d.Root, "lock") }

// Initialized reports whether dm init has run for this clone (the replica
// file is the marker — it is the one piece minted exactly once, §8.5).
func (d *Dir) Initialized() bool {
	_, err := os.Stat(d.replicaPath())
	return err == nil
}

// Init creates the layout and writes the replica id. Idempotent: an
// existing replica id is kept (re-init must never rotate it, §8.5), missing
// directories are created.
func (d *Dir) Init(replicaID string) (existing bool, id string, err error) {
	for _, dir := range []string{d.pendingRecordsDir(), d.pendingBlobsDir(), d.cacheDir()} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return false, "", err
		}
	}
	if cur, err := d.ReplicaID(); err == nil {
		return true, cur, nil
	}
	if err := os.WriteFile(d.replicaPath(), []byte(replicaID+"\n"), 0o644); err != nil {
		return false, "", err
	}
	return false, replicaID, nil
}

// ReplicaID reads the per-clone replica id.
func (d *Dir) ReplicaID() (string, error) {
	b, err := os.ReadFile(d.replicaPath())
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(string(b))
	if id == "" {
		return "", errors.New("empty replica id file")
	}
	return id, nil
}

// AppendPendingRecord durably adds one canonically-encoded record file,
// named by its rec-id (§8.2). Called before the command's ack renders —
// per-command durability (architecture.md §7). Rec-ids are unique by
// construction, so an existing file is a bug, not a race.
func (d *Dir) AppendPendingRecord(id record.RecID, encoded []byte) error {
	f, err := os.OpenFile(filepath.Join(d.pendingRecordsDir(), id.String()),
		os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("appending pending record: %w", err)
	}
	if _, err := f.Write(encoded); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// PendingRecord is one raw pending file: name (the rec-id string) + bytes.
type PendingRecord struct {
	Name string
	Data []byte
}

// ScanPendingRecords returns every pending record, sorted by filename —
// rec-id order, which is fold order. Pending is never indexed; it is
// scanned linearly, per-session small by construction (§8.2).
func (d *Dir) ScanPendingRecords() ([]PendingRecord, error) {
	entries, err := os.ReadDir(d.pendingRecordsDir())
	if err != nil {
		return nil, err
	}
	var out []PendingRecord
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(d.pendingRecordsDir(), e.Name()))
		if err != nil {
			return nil, err
		}
		out = append(out, PendingRecord{Name: e.Name(), Data: data})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// StageBlob stages anchored content the odb lacks under pending/blobs/<sha>
// (§8.1: deliberately not hash-object -w — a loose unreferenced object can
// be GC'd before the next sync). Idempotent: content is content-addressed.
func (d *Dir) StageBlob(sha record.SHA, content []byte) error {
	path := filepath.Join(d.pendingBlobsDir(), string(sha))
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	tmp, err := os.CreateTemp(d.pendingBlobsDir(), "tmp-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}
