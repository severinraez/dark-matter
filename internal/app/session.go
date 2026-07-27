package app

import (
	"errors"
	"fmt"
	"io"

	"meltcloud.io/dm/internal/core/record"
	"meltcloud.io/dm/internal/gitio"
	"meltcloud.io/dm/internal/local"
)

// ErrNotInitialized reports a repo dm init has not run in.
var ErrNotInitialized = errors.New("not initialized — run dm init")

// Session is app's per-invocation context (architecture.md §7): the lock,
// the checkout snapshot, resumed monotonic minting, and the pending
// overlay. The M2 subset — store tip/index, evidence wiring, and the stat
// accumulator arrive with their milestones (no store branch yet: pending
// alone carries reads).
type Session struct {
	Repo   *gitio.Repo
	Local  *local.Dir
	Head   record.SHA
	Minter *record.Minter

	// Pending is the overlay in rec-id (fold) order; writes within the
	// batch append here too, so later commands read earlier writes.
	Pending []record.Record

	release func()
}

// OpenSession locks the clone and snapshots everything one invocation
// reads: HEAD, and the scanned pending overlay. notice receives the
// lock-wait holder line (§8.2).
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
	s := &Session{Repo: repo, Local: l, Minter: det.NewMinter(), release: release}
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

// Close releases the invocation lock. (The stat-delta flush joins in M7.)
func (s *Session) Close() {
	if s.release != nil {
		s.release()
		s.release = nil
	}
}
