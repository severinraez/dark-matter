// Package evcache is evidence.Cached: the concrete evidence value app wires
// and hands to core — a caching decorator composing gitio (raw queries)
// with local's cache files, implementing the Evidence port's role
// interfaces (architecture.md §2, §5). Core stays cache-oblivious; deleting
// .git/.dm/cache/ must be unobservable to core-level results.
//
// Two persistent memos live below the port (architecture.md §5 decision
// 4): the similarity memos — RenamePairs results under
// `match-<origin>-<checkout-tree>` (§8.2; the working tree has no stable
// key to memoize under, so a dirty checkout bypasses the persistent layer —
// gitio's own in-memory per-invocation memo still applies) — and the
// ff-aware unreachability cache on the Lineage role (unreach.go). The
// `nomatch-<tip>-<target-tip>` memo caches a core conclusion, not a raw
// query, and sits above the port in app.
package evcache

import (
	"encoding/binary"
	"errors"

	"meltcloud.io/dm/internal/core/evidence"
	"meltcloud.io/dm/internal/core/record"
	"meltcloud.io/dm/internal/gitio"
	"meltcloud.io/dm/internal/local"
)

// Cached decorates a gitio evidence snapshot with persistent memos.
// It implements evidence.Tree and evidence.Match.
type Cached struct {
	*gitio.Evidence
	dir *local.Dir
}

// New wires the decorator for one invocation's snapshot, including the
// staged-blob fallback: an anchored blob the odb lacks (a not-yet-synced
// dirty anchor) is served from pending/blobs (§8.1).
func New(ev *gitio.Evidence, dir *local.Dir) *Cached {
	ev.BlobSource = func(sha record.SHA) ([]byte, bool) {
		content, err := dir.ReadPendingBlob(sha)
		return content, err == nil
	}
	return &Cached{Evidence: ev, dir: dir}
}

// RenamePairs consults the match memo before diffing: a clean checkout's
// origin→checkout pairs are a pure function of (origin, HEAD tree), so
// presence = validity (§8.2). Dirty checkouts recompute — same answer
// where it overlaps, never a stale one.
func (c *Cached) RenamePairs(origin record.SHA) ([]evidence.RenamePair, error) {
	clean, err := c.Evidence.Clean()
	if err != nil {
		return nil, err
	}
	if !clean {
		return c.Evidence.RenamePairs(origin)
	}
	tree, err := c.Evidence.HeadTree()
	if err != nil {
		return nil, err
	}
	key := "match-" + string(origin) + "-" + string(tree)
	if payload, ok := c.dir.ReadCache(key); ok {
		if pairs, err := decodePairs(payload); err == nil {
			return pairs, nil
		}
		// Corrupt payload: fall through and rebuild (never load-bearing).
	}
	pairs, err := c.Evidence.RenamePairs(origin)
	if err != nil {
		return nil, err
	}
	if err := c.dir.WriteCache(key, encodePairs(pairs)); err != nil {
		return nil, err
	}
	return pairs, nil
}

// ---- pair payload codec: length-prefixed binary (§8.2) ----

func encodePairs(pairs []evidence.RenamePair) []byte {
	var b []byte
	b = binary.AppendUvarint(b, uint64(len(pairs)))
	for _, p := range pairs {
		b = binary.AppendUvarint(b, uint64(p.Score))
		for _, s := range []string{string(p.From), string(p.To), string(p.FromBlob), string(p.ToBlob)} {
			b = binary.AppendUvarint(b, uint64(len(s)))
			b = append(b, s...)
		}
	}
	return b
}

func decodePairs(payload []byte) ([]evidence.RenamePair, error) {
	n, payload, err := takeUvarint(payload)
	if err != nil {
		return nil, err
	}
	pairs := make([]evidence.RenamePair, 0, n)
	for i := uint64(0); i < n; i++ {
		var p evidence.RenamePair
		var score uint64
		if score, payload, err = takeUvarint(payload); err != nil {
			return nil, err
		}
		p.Score = int(score)
		var fields [4]string
		for j := range fields {
			var l uint64
			if l, payload, err = takeUvarint(payload); err != nil {
				return nil, err
			}
			if uint64(len(payload)) < l {
				return nil, errors.New("truncated match memo")
			}
			fields[j], payload = string(payload[:l]), payload[l:]
		}
		p.From, p.To = record.Path(fields[0]), record.Path(fields[1])
		p.FromBlob, p.ToBlob = record.SHA(fields[2]), record.SHA(fields[3])
		pairs = append(pairs, p)
	}
	if len(payload) != 0 {
		return nil, errors.New("trailing bytes in match memo")
	}
	return pairs, nil
}

func takeUvarint(b []byte) (uint64, []byte, error) {
	v, n := binary.Uvarint(b)
	if n <= 0 {
		return 0, nil, errors.New("bad uvarint in cache payload")
	}
	return v, b[n:], nil
}

func appendUvarintLen(b []byte, n int) []byte {
	return binary.AppendUvarint(b, uint64(n))
}

func appendString(b []byte, s string) []byte {
	b = binary.AppendUvarint(b, uint64(len(s)))
	return append(b, s...)
}

func takeString(b []byte) (string, []byte, error) {
	l, b, err := takeUvarint(b)
	if err != nil {
		return "", nil, err
	}
	if uint64(len(b)) < l {
		return "", nil, errors.New("truncated cache payload")
	}
	return string(b[:l]), b[l:], nil
}
