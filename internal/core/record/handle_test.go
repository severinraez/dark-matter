package record

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// The §8.6 worked example, pinned: entry-id 01K0N3AB4XA3F9C1TQ84MZV2R7 →
// handle #a3f9c1 (first 6 chars of the random tail, lowercased — never the
// wall-clock prefix).
func TestHandleDerivationGolden(t *testing.T) {
	e := mustEntry(t, "01K0N3AB4XA3F9C1TQ84MZV2R7")
	if got := e.Tail(); got != "a3f9c1tq84mzv2r7" {
		t.Fatalf("Tail = %q", got)
	}
	if got := e.Handle(); got != "a3f9c1" {
		t.Fatalf("Handle = %q", got)
	}
	want := []Handle{"a3f9c1", "a3f9c1tq", "a3f9c1tq84", "a3f9c1tq84mz", "a3f9c1tq84mzv2", "a3f9c1tq84mzv2r7"}
	if diff := cmp.Diff(want, HandleCandidates(e)); diff != "" {
		t.Fatalf("candidates (-want +got):\n%s", diff)
	}
}

// Write-time collision extension (§3.5): dm grabs a couple more chars when
// the handle already exists in store ∪ pending.
func TestDeriveHandleCollisions(t *testing.T) {
	e := mustEntry(t, "01K0N3AB4XA3F9C1TQ84MZV2R7")

	cases := []struct {
		name  string
		taken map[Handle]bool
		want  Handle
	}{
		{"no-collision", nil, "a3f9c1"},
		{"short-taken", map[Handle]bool{"a3f9c1": true}, "a3f9c1tq"},
		{"two-levels", map[Handle]bool{"a3f9c1": true, "a3f9c1tq": true}, "a3f9c1tq84"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DeriveHandle(e, func(h Handle) bool { return tc.taken[h] })
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("DeriveHandle = %q, want %q", got, tc.want)
			}
		})
	}

	t.Run("full-tail-collision-errors", func(t *testing.T) {
		_, err := DeriveHandle(e, func(Handle) bool { return true })
		if err == nil || !strings.Contains(err.Error(), "full-tail collision") {
			t.Fatalf("err = %v", err)
		}
	})
}
