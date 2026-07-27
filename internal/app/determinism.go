// Package app owns one use case per verb, the invocation lock, the
// read-transaction (store ∪ pending), sync's retry loop, gc's CAS loop, and
// all wiring (architecture.md §2, §7). App sequences, core decides.
package app

import (
	"crypto/rand"
	"fmt"
	"io"
	mrand "math/rand"
	"strconv"
	"time"

	"meltcloud.io/dm/internal/core/record"
)

// Determinism env overrides (design.md §11.2): injected values, not ports.
// The production binary honors them so the E2E harness and the fixture
// builder can pin every source of nondeterminism; inert, debug-useful
// residue otherwise.
const (
	// EnvClock pins the clock: unix milliseconds; every Now() call
	// returns exactly this instant (same-millisecond ordering is carried
	// by monotonic rec-id minting, §8.3).
	EnvClock = "DM_CLOCK"
	// EnvIDSeed seeds the id entropy sources (int64): rec-id and
	// entry-id tails become reproducible.
	EnvIDSeed = "DM_ID_SEED"
	// EnvReplicaID pins the replica id minted at dm init (§8.5).
	EnvReplicaID = "DM_REPLICA_ID"
)

// Determinism carries the injectable values every session is wired with.
// The zero value is invalid; use DeterminismFromEnv.
type Determinism struct {
	Now          func() time.Time
	RecEntropy   io.Reader // rec-id random tails (wrapped monotonic by the minter)
	EntryEntropy io.Reader // entry-id random tails (fresh per id)
	ReplicaID    string    // "" = mint a random one at init
}

// DeterminismFromEnv resolves the overrides from getenv (usually os.Getenv;
// injectable for tests), defaulting to the real clock and crypto/rand.
func DeterminismFromEnv(getenv func(string) string) (Determinism, error) {
	d := Determinism{
		Now:          time.Now,
		RecEntropy:   rand.Reader,
		EntryEntropy: rand.Reader,
	}
	if v := getenv(EnvClock); v != "" {
		ms, err := strconv.ParseInt(v, 10, 64)
		if err != nil || ms < 0 {
			return Determinism{}, fmt.Errorf("%s: want unix milliseconds, got %q", EnvClock, v)
		}
		at := time.UnixMilli(ms).UTC()
		d.Now = func() time.Time { return at }
	}
	if v := getenv(EnvIDSeed); v != "" {
		seed, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return Determinism{}, fmt.Errorf("%s: want int64 seed, got %q", EnvIDSeed, v)
		}
		// Two independent deterministic streams so rec-id minting never
		// perturbs entry-id tails (handle stability across scenarios).
		d.RecEntropy = mrand.New(mrand.NewSource(seed))
		d.EntryEntropy = mrand.New(mrand.NewSource(seed + 1))
	}
	d.ReplicaID = getenv(EnvReplicaID)
	return d, nil
}

// NewMinter wires a record minter from the resolved values.
func (d Determinism) NewMinter() *record.Minter {
	return record.NewMinter(d.Now, d.RecEntropy, d.EntryEntropy)
}
