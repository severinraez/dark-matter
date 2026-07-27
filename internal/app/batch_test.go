package app

import (
	"bytes"
	"strings"
	"testing"

	"github.com/oklog/ulid/v2"

	"meltcloud.io/dm/internal/core/record"
)

// entryWithEntropy crafts an entry-id from exact tail entropy, so tests can
// force the §3.5 cross-replica collision case (two entries sharing a
// 6-char handle prefix) that real minting makes vanishingly rare.
func entryWithEntropy(t *testing.T, entropy [10]byte) record.EntryID {
	t.Helper()
	u, err := ulid.New(0, bytes.NewReader(entropy[:]))
	if err != nil {
		t.Fatal(err)
	}
	return record.EntryID(u)
}

func TestExtendDistinct(t *testing.T) {
	// Identical first 5 entropy bytes = identical first 8 tail chars (40
	// bits), diverging after: ambiguous at 6 and 8, distinct at 10.
	a := entryWithEntropy(t, [10]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10})
	b := entryWithEntropy(t, [10]byte{1, 2, 3, 4, 5, 0xff, 7, 8, 9, 10})
	if a.Tail()[:8] != b.Tail()[:8] {
		t.Fatalf("test setup: tails %s / %s should share 8 chars", a.Tail(), b.Tail())
	}

	ext := extendDistinct([]record.EntryID{a, b}, 6)
	ha, hb := ext[a], ext[b]
	if ha == hb {
		t.Fatalf("extended handles collide: %s", ha)
	}
	if len(ha) != 10 || len(hb) != 10 {
		t.Errorf("extended lengths %d/%d, want the first distinct ladder step (10)", len(ha), len(hb))
	}
	if !strings.HasPrefix(string(ha), a.Tail()[:6]) || !strings.HasPrefix(string(hb), b.Tail()[:6]) {
		t.Errorf("extensions %s/%s do not extend the ambiguous prefix", ha, hb)
	}
}

func TestExtendDistinctFullTailCollision(t *testing.T) {
	// The pathological limit: identical whole tails. The ladder can never
	// distinguish them; the full tails come back rather than a panic.
	a := entryWithEntropy(t, [10]byte{9, 9, 9, 9, 9, 9, 9, 9, 9, 9})
	b := entryWithEntropy(t, [10]byte{9, 9, 9, 9, 9, 9, 9, 9, 9, 9})
	ext := extendDistinct([]record.EntryID{a, b}, 6)
	if string(ext[a]) != a.Tail() || string(ext[b]) != b.Tail() {
		t.Errorf("want full tails, got %s / %s", ext[a], ext[b])
	}
}
