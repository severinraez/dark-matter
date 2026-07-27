package app

import (
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

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
// and the pending overlay. Evidence wiring and the stat accumulator arrive
// with their milestones.
type Session struct {
	Repo   *gitio.Repo
	Local  *local.Dir
	Store  *store.Store
	Head   record.SHA
	Minter *record.Minter

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

	storeRecords []record.Record

	release func()
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

// Close releases the invocation lock. (The stat-delta flush joins in M7.)
func (s *Session) Close() {
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
