package record

import (
	mrand "math/rand"
	"testing"
	"time"
)

func fixedClock(ms int64) func() time.Time {
	at := time.UnixMilli(ms).UTC()
	return func() time.Time { return at }
}

func seededMinter(ms, seed int64) *Minter {
	return NewMinter(fixedClock(ms),
		mrand.New(mrand.NewSource(seed)),
		mrand.New(mrand.NewSource(seed+1)))
}

// Monotonic minting (§8.3): within one millisecond a batch's rec-ids order
// as written — successive tails increment, never re-roll.
func TestMintRecIDMonotonicSameMillisecond(t *testing.T) {
	m := seededMinter(1700000000000, 42)
	var prev RecID
	for i := 0; i < 1000; i++ {
		id, err := m.MintRecID()
		if err != nil {
			t.Fatal(err)
		}
		if id.Time() != 1700000000000 {
			t.Fatalf("id %d: time bits %d moved off the pinned clock", i, id.Time())
		}
		if i > 0 && !prev.Less(id) {
			t.Fatalf("id %d (%s) does not sort after %s", i, id, prev)
		}
		prev = id
	}
}

// Determinism (§11.2): same clock + seed → the identical id sequence.
func TestMintDeterministicUnderSeed(t *testing.T) {
	a, b := seededMinter(1700000000000, 7), seededMinter(1700000000000, 7)
	for i := 0; i < 50; i++ {
		ra, _ := a.MintRecID()
		rb, _ := b.MintRecID()
		if ra != rb {
			t.Fatalf("rec %d: %s vs %s", i, ra, rb)
		}
		ea, _ := a.MintEntryID()
		eb, _ := b.MintEntryID()
		if ea != eb {
			t.Fatalf("entry %d: %s vs %s", i, ea, eb)
		}
	}
}

// Entry-ids mint with fresh randomness: same-millisecond entries share no
// tail prefix (§3.5 — handles keep their full 30 bits), unlike the
// deliberately incrementing rec-ids.
func TestMintEntryIDsFreshTails(t *testing.T) {
	m := seededMinter(1700000000000, 42)
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		e, err := m.MintEntryID()
		if err != nil {
			t.Fatal(err)
		}
		h := string(e.Handle())
		if seen[h] {
			t.Fatalf("entry %d: duplicate 6-char handle prefix %q under fresh entropy", i, h)
		}
		seen[h] = true
	}
}

// ResumeFrom (§8.2): after the lock hands over, minting resumes strictly
// after the largest pending rec-id even though the clock hasn't advanced.
func TestMintResumeFromFloor(t *testing.T) {
	first := seededMinter(1700000000000, 1)
	var last RecID
	for i := 0; i < 3; i++ {
		last, _ = first.MintRecID()
	}

	second := seededMinter(1700000000000, 1) // same ms, same seed: would re-mint the same ids
	second.ResumeFrom(last)
	id, err := second.MintRecID()
	if err != nil {
		t.Fatal(err)
	}
	if !last.Less(id) {
		t.Fatalf("resumed id %s does not sort after floor %s", id, last)
	}
	if id.Time() != last.Time() {
		t.Fatalf("resumed id left the floor's millisecond: %d vs %d", id.Time(), last.Time())
	}
}

// A clock behind the floor (skew, §8.3) still mints strictly ascending ids.
func TestMintClockBehindFloor(t *testing.T) {
	ahead := seededMinter(1800000000000, 1)
	floor, _ := ahead.MintRecID()

	behind := seededMinter(1700000000000, 2)
	behind.ResumeFrom(floor)
	prev := floor
	for i := 0; i < 5; i++ {
		id, err := behind.MintRecID()
		if err != nil {
			t.Fatal(err)
		}
		if !prev.Less(id) {
			t.Fatalf("id %s does not sort after %s", id, prev)
		}
		prev = id
	}
}

// maxThenOnes yields 0xFF for the first 10 bytes (an already-maximal
// random tail) and 0x01 forever after (sane increment samples).
type maxThenOnes struct{ n int }

func (r *maxThenOnes) Read(p []byte) (int, error) {
	for i := range p {
		if r.n < 10 {
			p[i] = 0xFF
		} else {
			p[i] = 0x01
		}
		r.n++
	}
	return len(p), nil
}

// Tail exhaustion within a millisecond (the oklog monotonic-overflow edge)
// rolls into the next millisecond instead of failing or folding backwards.
func TestMintRecIDOverflowRolls(t *testing.T) {
	m := NewMinter(fixedClock(1700000000000), &maxThenOnes{}, mrand.New(mrand.NewSource(1)))
	first, err := m.MintRecID()
	if err != nil {
		t.Fatal(err)
	}
	second, err := m.MintRecID()
	if err != nil {
		t.Fatal(err)
	}
	if !first.Less(second) {
		t.Fatalf("%s does not sort after %s", second, first)
	}
	if first.Time() != 1700000000000 || second.Time() != 1700000000001 {
		t.Fatalf("overflow did not roll into the next millisecond: %d → %d", first.Time(), second.Time())
	}
}

// Pinned golden sequence: the exact ids a pinned clock + seed produce.
// math/rand's algorithm is covered by the Go 1 compatibility promise, so
// these strings are stable; they are what fixture manifests will rely on.
func TestMintGoldenSequence(t *testing.T) {
	m := seededMinter(1700000000000, 42)
	wantRecs := []string{
		"01HF7YAT00AE67Z5NHCJZHQ5XV",
		"01HF7YAT00AE67Z5NHCN8T55NH",
		"01HF7YAT00AE67Z5NHCRVQZRFF",
	}
	wantEntries := []string{
		"01HF7YAT0057CG93KFHSYGQ2Q0",
		"01HF7YAT001JCHG2N1Z5SX0T5Q",
		"01HF7YAT00T63C7EBQCD9X8GGJ",
	}
	for i := 0; i < 3; i++ {
		r, err := m.MintRecID()
		if err != nil {
			t.Fatal(err)
		}
		e, err := m.MintEntryID()
		if err != nil {
			t.Fatal(err)
		}
		if r.String() != wantRecs[i] {
			t.Errorf("rec %d = %s, want %s", i, r, wantRecs[i])
		}
		if e.String() != wantEntries[i] {
			t.Errorf("entry %d = %s, want %s", i, e, wantEntries[i])
		}
	}
}
