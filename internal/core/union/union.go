package union

import (
	"meltcloud.io/dm/internal/core/record"
)

// Set is the policy-relevant content of one store tree (§8.1): the epoch
// generation counter, the append-only record set keyed by rec-id (the
// union-merge key — distinct filenames never collide, identical records
// dedup), the anchored-blob presence set (content rides in store/local —
// blobs are content-addressed, so presence is all merge needs), and the
// per-replica stat register files.
type Set struct {
	Epoch   uint64
	Records map[record.RecID][]byte // canonical record bytes, keyed by filename
	Blobs   map[record.SHA]bool
	Stats   map[string]Stats // replica id → its register file
}

// Stats is one replica's register file: one row per entry that has stats
// (§7.2). Each row is that replica's G-Counter cell plus its LWW `t`.
type Stats map[record.EntryID]Row

// Row is one entry's counters within one replica file: impressions,
// expansions, search-hits, and the last-expanded unix-millisecond
// timestamp (§7.1). Missing field = zero.
type Row struct {
	I, X, H uint64
	T       uint64
}

// NewSet returns an empty set at epoch 0 — the store's initial state.
func NewSet() Set {
	return Set{
		Records: map[record.RecID][]byte{},
		Blobs:   map[record.SHA]bool{},
		Stats:   map[string]Stats{},
	}
}

// Merge is the CRDT store merge (§8.4): same epoch → set union of records
// and blobs with the per-replica stats merged field-wise max; differing
// epochs → the newer-epoch set is adopted wholesale (the epoch rule — a
// compacted-away record must not be re-added by a replica still holding
// the old tree; own not-yet-pushed records re-enter via WithOwn, never
// here). Commutative and idempotent, so the sync retry loop converges.
func Merge(a, b Set) Set {
	if a.Epoch != b.Epoch {
		if b.Epoch > a.Epoch {
			return clone(b)
		}
		return clone(a)
	}
	out := clone(a)
	for id, data := range b.Records {
		if have, ok := out.Records[id]; ok {
			// Distinct records never share a rec-id by construction; if
			// corruption ever produces one, pick deterministically so both
			// replicas still converge.
			if string(data) < string(have) {
				out.Records[id] = data
			}
			continue
		}
		out.Records[id] = data
	}
	for sha := range b.Blobs {
		out.Blobs[sha] = true
	}
	for replica, stats := range b.Stats {
		out.Stats[replica] = mergeStats(out.Stats[replica], stats)
	}
	return out
}

// WithOwn returns s plus this replica's not-yet-pushed contribution:
// pending records and staged blobs (§8.4's "re-contributes only its own").
// Pending stat deltas join in M7.
func WithOwn(s Set, records map[record.RecID][]byte, blobs []record.SHA) Set {
	out := clone(s)
	for id, data := range records {
		if _, ok := out.Records[id]; !ok {
			out.Records[id] = data
		}
	}
	for _, sha := range blobs {
		out.Blobs[sha] = true
	}
	return out
}

// mergeStats reconciles two copies of the same replica's register file:
// union of rows, field-wise max per row (§7.2 — cells are monotonic, so a
// stale snapshot never double-counts; `t` is an LWW register, merge = max).
func mergeStats(a, b Stats) Stats {
	out := make(Stats, len(a)+len(b))
	for id, row := range a {
		out[id] = row
	}
	for id, row := range b {
		out[id] = maxRow(out[id], row)
	}
	return out
}

func maxRow(a, b Row) Row {
	return Row{I: max(a.I, b.I), X: max(a.X, b.X), H: max(a.H, b.H), T: max(a.T, b.T)}
}

func clone(s Set) Set {
	out := Set{
		Epoch:   s.Epoch,
		Records: make(map[record.RecID][]byte, len(s.Records)),
		Blobs:   make(map[record.SHA]bool, len(s.Blobs)),
		Stats:   make(map[string]Stats, len(s.Stats)),
	}
	for id, data := range s.Records {
		out.Records[id] = data
	}
	for sha := range s.Blobs {
		out.Blobs[sha] = true
	}
	for replica, stats := range s.Stats {
		cp := make(Stats, len(stats))
		for id, row := range stats {
			cp[id] = row
		}
		out.Stats[replica] = cp
	}
	return out
}
