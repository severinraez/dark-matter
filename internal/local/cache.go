package local

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// Cache file mechanics (§8.2): content-keyed, immutable files under
// .git/.dm/cache/ — the filename is the key, so presence = validity and
// there is no invalidation logic. Files are built in a temp file and
// renamed into place: atomic, race-safe without locks (concurrent rebuilds
// produce byte-identical content, so last-rename-wins is harmless).
// Disposable, never load-bearing: deleting the directory must be
// unobservable to core-level results.
//
// Key/invalidation *semantics* are dictated from above (evcache, app);
// this file only moves bytes.

// cacheMagic is the versioned header guarding format changes: any mismatch
// or short file reads as a miss and the entry is deleted for rebuild.
var cacheMagic = []byte("DMCACHE1\n")

// ReadCache returns a cache entry's payload; ok=false is a miss (absent,
// or a stale-format/corrupt file, which is deleted on sight).
func (d *Dir) ReadCache(key string) (payload []byte, ok bool) {
	path := filepath.Join(d.cacheDir(), key)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	if !bytes.HasPrefix(data, cacheMagic) {
		os.Remove(path)
		return nil, false
	}
	return data[len(cacheMagic):], true
}

// WriteCache builds a cache entry atomically. Failures are swallowed into
// a no-op where possible by the caller — the cache is never load-bearing —
// but genuine I/O errors still surface for visibility.
func (d *Dir) WriteCache(key string, payload []byte) error {
	if err := os.MkdirAll(d.cacheDir(), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(d.cacheDir(), "tmp-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(cacheMagic); err == nil {
		_, err = tmp.Write(payload)
		if cerr := tmp.Close(); err == nil {
			err = cerr
		}
	} else {
		tmp.Close()
	}
	if err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(d.cacheDir(), key))
}

// ListCache lists the cache keys with the given prefix, sorted. Callers
// treat the result as advisory — entries may vanish or be stale-format;
// ReadCache remains the arbiter.
func (d *Dir) ListCache(prefix string) ([]string, error) {
	entries, err := os.ReadDir(d.cacheDir())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var keys []string
	for _, e := range entries {
		if name := e.Name(); strings.HasPrefix(name, prefix) && !strings.HasPrefix(name, "tmp-") {
			keys = append(keys, name)
		}
	}
	return keys, nil
}

// DropCache removes cache entries with the given key prefix for which keep
// returns false — the opportunistic GC hook for non-matching keys (§8.2).
// Best-effort.
func (d *Dir) DropCache(prefix string, keep func(key string) bool) error {
	entries, err := os.ReadDir(d.cacheDir())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, prefix) || keep(name) {
			continue
		}
		os.Remove(filepath.Join(d.cacheDir(), name))
	}
	return nil
}
