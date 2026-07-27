package record

import (
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/oklog/ulid/v2"
)

// Minter mints record and entry ULIDs (§8.3).
//
// Rec-ids mint in monotonic mode: within one millisecond each successive id
// increments the previous random tail instead of re-rolling it, so a
// batch's records order as written and an `a` followed by a `u` of the same
// entry can never fold backwards. ResumeFrom restores cross-process order
// after the invocation lock is acquired (§8.2).
//
// Entry-ids are exempt: they mint with fresh randomness per id, so
// same-millisecond entries share no tail prefix and handle derivation keeps
// its full 30 bits (§3.5).
//
// Not safe for concurrent use; invocations are serialized by the lock.
type Minter struct {
	now     func() time.Time
	rec     *ulid.MonotonicEntropy
	entry   io.Reader
	last    ulid.ULID // floor: every minted rec-id is strictly greater
	hasLast bool
}

// NewMinter wires a minter from an injectable clock and entropy sources
// (§11.2 determinism: tests pass a pinned clock and seeded readers).
func NewMinter(now func() time.Time, recEntropy, entryEntropy io.Reader) *Minter {
	return &Minter{
		now:   now,
		rec:   ulid.Monotonic(recEntropy, 0),
		entry: entryEntropy,
	}
}

// ResumeFrom raises the rec-id floor: subsequent rec-ids sort strictly
// after r even if the clock has not advanced past it — how two serialized
// same-millisecond processes keep fold order (§8.2).
func (m *Minter) ResumeFrom(r RecID) {
	u := ulid.ULID(r)
	if !m.hasLast || m.last.Compare(u) < 0 {
		m.last, m.hasLast = u, true
	}
}

// MintRecID mints the next record id, monotonically.
func (m *Minter) MintRecID() (RecID, error) {
	ts := ulid.Timestamp(m.now())
	if m.hasLast && ts < m.last.Time() {
		// Clock behind the floor (resume, or a backwards step): mint in
		// the floor's millisecond so order survives.
		ts = m.last.Time()
	}
	id, err := ulid.New(ts, m.rec)
	if errors.Is(err, ulid.ErrMonotonicOverflow) {
		// Random tail exhausted within this millisecond — roll into the
		// next one; order is what is load-bearing, not the wall clock.
		ts++
		id, err = ulid.New(ts, m.rec)
	}
	if err != nil {
		return RecID{}, fmt.Errorf("minting rec-id: %w", err)
	}
	if m.hasLast && id.Compare(m.last) <= 0 {
		id, err = incremented(m.last)
		if err != nil {
			return RecID{}, err
		}
	}
	m.last, m.hasLast = id, true
	return RecID(id), nil
}

// MintEntryID mints a logical entry id with fresh randomness.
func (m *Minter) MintEntryID() (EntryID, error) {
	id, err := ulid.New(ulid.Timestamp(m.now()), m.entry)
	if err != nil {
		return EntryID{}, fmt.Errorf("minting entry-id: %w", err)
	}
	return EntryID(id), nil
}

// incremented returns u+1 over the full 128-bit value (carry rolls from the
// random tail into the timestamp bits, keeping strict order).
func incremented(u ulid.ULID) (ulid.ULID, error) {
	for i := len(u) - 1; i >= 0; i-- {
		u[i]++
		if u[i] != 0 {
			return u, nil
		}
	}
	return ulid.ULID{}, errors.New("rec-id space exhausted")
}
