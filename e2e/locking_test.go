package e2e

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"testing"
)

// The §11.4 locking and determinism scenario groups (§8.2, §8.3), at the
// process boundary: real concurrent dm invocations racing on one clone,
// and identical record sets folded through opposite sync orders.

// TestLockingConcurrentInvocationsSerialize races N dm processes on one
// clone: the exclusive lock on .git/.dm/lock serializes them, every write
// lands, and the store side stays uncorrupted (§8.2).
func TestLockingConcurrentInvocationsSerialize(t *testing.T) {
	r := NewRepo(t)
	r.WriteFile("f.rb", "content\n")
	r.Commit("base")
	r.MustDM("", "init")

	const n = 8
	bin := DMBinary(t)
	results := make([]Result, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Same pinned millisecond for every racer: serialization
			// order, not the clock, must keep rec-ids distinct and
			// ordered (§8.2 same-millisecond handoff, under contention).
			cmd := exec.Command(bin)
			cmd.Dir = r.Dir
			cmd.Stdin = strings.NewReader(fmt.Sprintf("a:f.rb:c:racer %d note\n", i))
			cmd.Env = append(os.Environ(),
				fmt.Sprintf("DM_CLOCK=%d", baseClock),
				fmt.Sprintf("DM_ID_SEED=%d", 90000+i),
				"DM_REPLICA_ID="+r.ReplicaID)
			var out, errOut bytes.Buffer
			cmd.Stdout = &out
			cmd.Stderr = &errOut
			err := cmd.Run()
			code := 0
			if err != nil {
				if exitErr, ok := err.(*exec.ExitError); ok {
					code = exitErr.ExitCode()
				} else {
					code = -1
				}
			}
			results[i] = Result{Stdout: out.String(), Stderr: errOut.String(), Code: code}
		}(i)
	}
	wg.Wait()

	for i, res := range results {
		if res.Code != 0 || !strings.Contains(res.Stdout, " created") {
			t.Fatalf("racer %d failed: code=%d\nstdout=%q\nstderr=%q",
				i, res.Code, res.Stdout, res.Stderr)
		}
	}
	// All eight writes landed with distinct, sorted rec-ids — dump
	// prints in fold (rec-id) order, so duplicates or disorder surface.
	dump := r.MustDM("", "dump").Stdout
	recs := regexp.MustCompile(`(?m)^CR (\S+) `).FindAllStringSubmatch(dump, -1)
	if len(recs) != n {
		t.Fatalf("dump has %d CR records, want %d:\n%q", len(recs), n, dump)
	}
	seen := map[string]bool{}
	prev := ""
	for _, m := range recs {
		if seen[m[1]] {
			t.Fatalf("duplicate rec-id %s:\n%q", m[1], dump)
		}
		seen[m[1]] = true
		if m[1] <= prev {
			t.Fatalf("rec-ids out of order (%s after %s):\n%q", m[1], prev, dump)
		}
		prev = m[1]
	}
	for i := 0; i < n; i++ {
		if !strings.Contains(dump, fmt.Sprintf("racer %d note", i)) {
			t.Errorf("racer %d's write lost:\n%q", i, dump)
		}
	}
}

// TestLockingSameMillisecondHandoff pins §8.2's monotonic resume across
// processes: two sequential invocations on the same pinned millisecond
// mint strictly increasing rec-ids — the second process resumes past the
// first's pending records instead of re-minting from the raw clock.
func TestLockingSameMillisecondHandoff(t *testing.T) {
	r := NewRepo(t)
	r.WriteFile("f.rb", "content\n")
	r.Commit("base")
	r.MustDM("", "init")

	clock := []string{fmt.Sprintf("DM_CLOCK=%d", baseClock)}
	r.DMEnv(clock, "a:f.rb:c:first process\n")
	r.DMEnv(clock, "a:f.rb:c:second process\n")

	dump := r.MustDM("", "dump").Stdout
	re := regexp.MustCompile(`(?m)^CR (\S+) \S+ c \S+ \S+ \S+ (first|second) process$`)
	ms := re.FindAllStringSubmatch(dump, -1)
	if len(ms) != 2 {
		t.Fatalf("want both records in dump:\n%q", dump)
	}
	// Fold order (dump order) must match write order: the second
	// process's rec-id sorts after the first's despite the shared
	// millisecond.
	if ms[0][2] != "first" || ms[1][2] != "second" {
		t.Errorf("rec-id order lost across the handoff:\n%q", dump)
	}
	if ms[0][1] >= ms[1][1] {
		t.Errorf("rec-ids not strictly increasing: %s then %s", ms[0][1], ms[1][1])
	}
}

// TestDeterminismSyncOrderIrrelevant replays the §11.4 determinism row:
// the same records pushed through opposite sync orders fold to an
// identical state on every replica in both universes (§8.3 CRDT
// byte-identity).
func TestDeterminismSyncOrderIrrelevant(t *testing.T) {
	// buildUniverse creates remote + two replicas with pinned, identical
	// identities and writes, then syncs in the given order until both
	// replicas hold everything.
	buildUniverse := func(bFirst bool) (string, string) {
		a, bare := newSharedRepo(t)
		b := CloneRepo(t, bare, 2)
		b.MustDM("", "init")
		a.MustDM("a:api/handler.rb:c:Note from A\n")
		b.MustDM("a:api/handler.rb:c:Note from B\nf:$1:+\n")
		order := []*Repo{a, b}
		if bFirst {
			order = []*Repo{b, a}
		}
		// Two rounds: after them, both replicas have folded both sides
		// regardless of who pushed first.
		for i := 0; i < 2; i++ {
			order[0].MustDM("", "sync")
			order[1].MustDM("", "sync")
		}
		return a.MustDM("", "dump").Stdout, b.MustDM("", "dump").Stdout
	}

	a1, b1 := buildUniverse(false)
	a2, b2 := buildUniverse(true)
	if a1 != b1 {
		t.Fatalf("universe 1 replicas diverged:\nA:\n%s\nB:\n%s", a1, b1)
	}
	if a2 != b2 {
		t.Fatalf("universe 2 replicas diverged:\nA:\n%s\nB:\n%s", a2, b2)
	}
	if a1 != a2 {
		t.Fatalf("sync order changed the folded state:\nA-first:\n%s\nB-first:\n%s", a1, a2)
	}
	for _, want := range []string{"Note from A", "Note from B"} {
		if !strings.Contains(a1, want) {
			t.Fatalf("converged dump misses %q:\n%s", want, a1)
		}
	}
}
