package app

import (
	"testing"
	"time"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestDeterminismDefaults(t *testing.T) {
	d, err := DeterminismFromEnv(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if d.ReplicaID != "" {
		t.Fatalf("ReplicaID = %q, want unset", d.ReplicaID)
	}
	if before, now := time.Now(), d.Now(); now.Before(before.Add(-time.Minute)) {
		t.Fatalf("default clock not live: %v", now)
	}
	// Default entropy is real: two minters must diverge.
	a, _ := DeterminismFromEnv(env(nil))
	ia, _ := d.NewMinter().MintEntryID()
	ib, _ := a.NewMinter().MintEntryID()
	if ia == ib {
		t.Fatal("crypto entropy produced identical entry-ids")
	}
}

func TestDeterminismOverrides(t *testing.T) {
	vars := map[string]string{
		EnvClock:     "1700000000000",
		EnvIDSeed:    "42",
		EnvReplicaID: "t3stpin1",
	}
	d, err := DeterminismFromEnv(env(vars))
	if err != nil {
		t.Fatal(err)
	}
	if got := d.Now(); got != time.UnixMilli(1700000000000).UTC() {
		t.Fatalf("Now = %v", got)
	}
	if d.Now() != d.Now() {
		t.Fatal("pinned clock advanced")
	}
	if d.ReplicaID != "t3stpin1" {
		t.Fatalf("ReplicaID = %q", d.ReplicaID)
	}

	// Same env → identical minted sequences (the §11.2 guarantee the whole
	// golden-test strategy rests on).
	d2, _ := DeterminismFromEnv(env(vars))
	m1, m2 := d.NewMinter(), d2.NewMinter()
	for i := 0; i < 20; i++ {
		r1, _ := m1.MintRecID()
		r2, _ := m2.MintRecID()
		if r1 != r2 {
			t.Fatalf("rec %d: %s vs %s", i, r1, r2)
		}
		e1, _ := m1.MintEntryID()
		e2, _ := m2.MintEntryID()
		if e1 != e2 {
			t.Fatalf("entry %d: %s vs %s", i, e1, e2)
		}
	}
}

func TestDeterminismBadValues(t *testing.T) {
	for name, vars := range map[string]map[string]string{
		"clock-not-int":  {EnvClock: "yesterday"},
		"clock-negative": {EnvClock: "-5"},
		"seed-not-int":   {EnvIDSeed: "abc"},
	} {
		if _, err := DeterminismFromEnv(env(vars)); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}
