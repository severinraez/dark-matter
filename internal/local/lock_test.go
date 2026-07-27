package local

import (
	"io"
	"testing"
	"time"
)

// The invocation lock serializes: a second contender blocks until release
// and never errors (§8.2). flock conflicts apply across file descriptions,
// so two Dir handles in one process exercise the contention path.
func TestLockSerializes(t *testing.T) {
	d := At(t.TempDir())
	if _, _, err := d.Init("TESTREPL"); err != nil {
		t.Fatal(err)
	}
	release1, err := d.Lock(io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	acquired := make(chan func(), 1)
	go func() {
		release2, err := At(d.Root[:len(d.Root)-len("/.dm")]).Lock(io.Discard)
		if err != nil {
			t.Error(err)
			acquired <- func() {}
			return
		}
		acquired <- release2
	}()
	select {
	case <-acquired:
		t.Fatal("second lock acquired while first still held")
	case <-time.After(100 * time.Millisecond):
	}
	release1()
	select {
	case release2 := <-acquired:
		release2()
	case <-time.After(2 * time.Second):
		t.Fatal("second lock never acquired after release")
	}
}

func TestInitIdempotent(t *testing.T) {
	d := At(t.TempDir())
	existing, id, err := d.Init("REPLICA1")
	if err != nil || existing || id != "REPLICA1" {
		t.Fatalf("first init = (%v, %q, %v)", existing, id, err)
	}
	existing, id, err = d.Init("REPLICA2")
	if err != nil || !existing || id != "REPLICA1" {
		t.Fatalf("re-init = (%v, %q, %v), want existing REPLICA1", existing, id, err)
	}
}
