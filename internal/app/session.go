package app

import (
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"meltcloud.io/dm/internal/core/record"
	"meltcloud.io/dm/internal/core/union"
	"meltcloud.io/dm/internal/gitio"
	"meltcloud.io/dm/internal/local"
	"meltcloud.io/dm/internal/store"
)

// ErrNotInitialized reports a repo dm init has not run in.
var ErrNotInitialized = errors.New("not initialized — run dm init")

// Session is app's per-invocation context (architecture.md §7): the lock,
// the checkout snapshot, resumed monotonic minting, the store snapshot,
// the pending overlay, and the stat-delta accumulator.
type Session struct {
	Repo    *gitio.Repo
	Local   *local.Dir
	Store   *store.Store
	Head    record.SHA
	Minter  *record.Minter
	Now     func() time.Time
	Replica string

	// StoreTip and StoreSet snapshot the local store mirror at open; the
	// tip only moves at dm sync (§8.2), which holds this same lock.
	StoreTip *record.SHA
	StoreSet union.Set

	// Qualified is the §9.4 clone guard: full history (not shallow) and an
	// all-refs fetch refspec. Degraded clones consume existing bindings
	// but never infer verdicts or classify abandoned.
	Qualified bool

	// Pending is the overlay in rec-id (fold) order; writes within the
	// batch append here too, so later commands read earlier writes.
	Pending []record.Record

	// PendingStats is the on-disk stat-delta accumulator at open (§8.1);
	// deltas collects this invocation's events (§7.1), flushed into it at
	// Close in one write. Both are deltas against the store's register
	// file, not absolute values.
	PendingStats union.Stats
	deltas       union.Stats

	storeRecords []record.Record

	release func()
}

// BumpImpression records one collapsed showing in a surface (§7.1, weak).
func (s *Session) BumpImpression(id record.EntryID) {
	row := s.deltas[id]
	row.I++
	s.deltas[id] = row
}

// BumpExpansion records one `r:#handle` opening — the real click — and
// refreshes the last-expanded LWW register (§7.1).
func (s *Session) BumpExpansion(id record.EntryID) {
	row := s.deltas[id]
	row.X++
	row.T = max(row.T, uint64(s.Now().UnixMilli()))
	s.deltas[id] = row
}

// BumpSearchHit records one `s` match — distinct from impressions, never
// correlated with a later expansion (§7.1).
func (s *Session) BumpSearchHit(id record.EntryID) {
	row := s.deltas[id]
	row.H++
	s.deltas[id] = row
}

// StatsView is the read-side merge of store ∪ pending (§8.1): the store's
// per-replica register files with this replica's pending deltas (disk
// accumulator + this invocation's events) folded into its own file.
func (s *Session) StatsView() map[string]union.Stats {
	out := make(map[string]union.Stats, len(s.StoreSet.Stats)+1)
	for replica, stats := range s.StoreSet.Stats {
		out[replica] = stats
	}
	own := union.AddStats(s.PendingStats, s.deltas)
	if len(own) > 0 {
		out[s.Replica] = union.AddStats(out[s.Replica], own)
	}
	return out
}

// OpenSession locks the clone and snapshots everything one invocation
// reads: HEAD, the store tip, and the scanned pending overlay. notice
// receives the lock-wait holder line (§8.2).
func OpenSession(dir string, det Determinism, notice io.Writer) (*Session, error) {
	repo, err := gitio.Open(dir)
	if err != nil {
		return nil, err
	}
	l := local.At(repo.GitDir)
	if !l.Initialized() {
		return nil, ErrNotInitialized
	}
	release, err := l.Lock(notice)
	if err != nil {
		return nil, err
	}
	s := &Session{
		Repo:   repo,
		Local:  l,
		Store:  &store.Store{Repo: repo},
		Minter: det.NewMinter(),
		Now:    det.Now,
		deltas: union.Stats{},

		release: release,
	}
	if err := s.load(); err != nil {
		release()
		return nil, err
	}
	return s, nil
}

func (s *Session) load() error {
	head, err := s.Repo.Head()
	if err != nil {
		return err
	}
	s.Head = head
	if s.Replica, err = s.Local.ReplicaID(); err != nil {
		return err
	}
	rawStats, err := s.Local.ReadPendingStats()
	if err != nil {
		return err
	}
	if s.PendingStats, err = union.ParseStats(rawStats); err != nil {
		return fmt.Errorf("pending stats: %w", err)
	}
	if s.Qualified, err = qualified(s.Repo); err != nil {
		return err
	}
	if s.StoreTip, err = s.Store.Tip(); err != nil {
		return err
	}
	// A missing ref reads as the empty store — robust for clones whose
	// init predates the store branch.
	if s.StoreSet, err = s.Store.Read(s.StoreTip); err != nil {
		return err
	}
	for id, data := range s.StoreSet.Records {
		rec, err := record.Decode(data)
		if err != nil {
			return fmt.Errorf("store record %s: %w", id, err)
		}
		if rec.ID() != id {
			return fmt.Errorf("store record %s: filename does not match rec-id %s", id, rec.ID())
		}
		s.storeRecords = append(s.storeRecords, rec)
	}
	sort.Slice(s.storeRecords, func(i, j int) bool {
		return s.storeRecords[i].ID().Less(s.storeRecords[j].ID())
	})
	raw, err := s.Local.ScanPendingRecords()
	if err != nil {
		return err
	}
	for _, pr := range raw {
		rec, err := record.Decode(pr.Data)
		if err != nil {
			return fmt.Errorf("pending record %s: %w", pr.Name, err)
		}
		if rec.ID().String() != pr.Name {
			return fmt.Errorf("pending record %s: filename does not match rec-id %s", pr.Name, rec.ID())
		}
		s.Pending = append(s.Pending, rec)
		// Resume monotonic minting past everything already pending, so two
		// serialized same-millisecond invocations can never fold out of
		// order (§8.2).
		s.Minter.ResumeFrom(rec.ID())
	}
	return nil
}

// View is the read-transaction's record set: store ∪ pending, rec-id
// (fold) order, deduped by rec-id — a record can transiently be both when
// a push landed but the clear was interrupted (§8.4).
func (s *Session) View() []record.Record {
	out := make([]record.Record, 0, len(s.storeRecords)+len(s.Pending))
	out = append(out, s.storeRecords...)
	for _, rec := range s.Pending {
		if _, inStore := s.StoreSet.Records[rec.ID()]; !inStore {
			out = append(out, rec)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID().Less(out[j].ID()) })
	return out
}

// Close flushes this invocation's stat deltas into the pending accumulator
// (one write, still under the lock — architecture.md §7) and releases the
// invocation lock. A failed stat flush is swallowed: stats are advisory
// signals and must never fail the read that produced them.
func (s *Session) Close() {
	if len(s.deltas) > 0 {
		merged := union.AddStats(s.PendingStats, s.deltas)
		_ = s.Local.WritePendingStats(union.FormatStats(merged))
		s.PendingStats = merged
		s.deltas = union.Stats{}
	}
	if s.release != nil {
		s.release()
		s.release = nil
	}
}

// qualified assembles the §9.4 clone-guard facts: not shallow, and — when
// a share remote exists — a fetch refspec covering all of refs/heads/*
// (`--single-branch` narrows it to one branch). A remoteless clone is its
// own authority. Conservative in the right direction: an unqualified
// reader under-shows, it never mass-condemns.
func qualified(repo *gitio.Repo) (bool, error) {
	shallow, err := repo.IsShallow()
	if err != nil || shallow {
		return false, err
	}
	remote, err := shareRemote(repo)
	if err != nil || remote == "" {
		return err == nil, err
	}
	specs, err := repo.ConfigValues("remote." + remote + ".fetch")
	if err != nil {
		return false, err
	}
	for _, spec := range specs {
		if strings.Contains(spec, "refs/heads/*") {
			return true, nil
		}
	}
	return false, nil
}
