package union

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"meltcloud.io/dm/internal/core/record"
)

func rid(t *testing.T, s string) record.RecID {
	t.Helper()
	id, err := record.ParseRecID(s)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func eid(t *testing.T, s string) record.EntryID {
	t.Helper()
	id, err := record.ParseEntryID(s)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

const (
	recA = "01ARZ3NDEKTSV4RRFFQ69G5FAA"
	recB = "01ARZ3NDEKTSV4RRFFQ69G5FAB"
	recC = "01ARZ3NDEKTSV4RRFFQ69G5FAC"
	ent1 = "01ARZ3NDEKTSV4RRFFQ69G5FA1"
	ent2 = "01ARZ3NDEKTSV4RRFFQ69G5FA2"
)

func TestMergeUnionsRecordsAndBlobs(t *testing.T) {
	a := NewSet()
	a.Records[rid(t, recA)] = []byte("shared\n")
	a.Records[rid(t, recB)] = []byte("only-a\n")
	a.Blobs["aa00"] = true
	b := NewSet()
	b.Records[rid(t, recA)] = []byte("shared\n")
	b.Records[rid(t, recC)] = []byte("only-b\n")
	b.Blobs["bb11"] = true

	got := Merge(a, b)
	want := NewSet()
	want.Records[rid(t, recA)] = []byte("shared\n")
	want.Records[rid(t, recB)] = []byte("only-a\n")
	want.Records[rid(t, recC)] = []byte("only-b\n")
	want.Blobs["aa00"] = true
	want.Blobs["bb11"] = true
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("merge mismatch (-want +got):\n%s", diff)
	}
	// Commutative: the retry loop must converge from either side.
	if diff := cmp.Diff(got, Merge(b, a)); diff != "" {
		t.Fatalf("merge not commutative (-ab +ba):\n%s", diff)
	}
	// Idempotent.
	if diff := cmp.Diff(got, Merge(got, got)); diff != "" {
		t.Fatalf("merge not idempotent:\n%s", diff)
	}
}

func TestMergeDoesNotMutateInputs(t *testing.T) {
	a := NewSet()
	a.Records[rid(t, recA)] = []byte("a\n")
	b := NewSet()
	b.Records[rid(t, recB)] = []byte("b\n")
	Merge(a, b)
	if len(a.Records) != 1 || len(b.Records) != 1 {
		t.Fatalf("inputs mutated: a=%d b=%d records", len(a.Records), len(b.Records))
	}
}

func TestEpochRuleNewerWinsWholesale(t *testing.T) {
	old := NewSet()
	old.Records[rid(t, recA)] = []byte("compacted-away\n")
	old.Records[rid(t, recB)] = []byte("kept\n")
	old.Blobs["aa00"] = true

	compacted := NewSet()
	compacted.Epoch = 1
	compacted.Records[rid(t, recB)] = []byte("kept\n")

	for name, got := range map[string]Set{
		"old-local":   Merge(old, compacted),
		"old-fetched": Merge(compacted, old),
	} {
		if got.Epoch != 1 {
			t.Fatalf("%s: epoch %d, want 1", name, got.Epoch)
		}
		if _, resurrected := got.Records[rid(t, recA)]; resurrected {
			t.Fatalf("%s: compacted-away record resurrected", name)
		}
		if _, kept := got.Records[rid(t, recB)]; !kept {
			t.Fatalf("%s: surviving record lost", name)
		}
		if got.Blobs["aa00"] {
			t.Fatalf("%s: compacted-away blob resurrected", name)
		}
	}
}

func TestWithOwnRecontributesAcrossEpochs(t *testing.T) {
	// A stale replica syncing across a compaction re-contributes only its
	// own not-yet-pushed records (§8.4): pending rides WithOwn after the
	// epoch rule dropped everything merely received.
	compacted := NewSet()
	compacted.Epoch = 1
	merged := Merge(NewSet(), compacted)
	got := WithOwn(merged, map[record.RecID][]byte{rid(t, recC): []byte("own\n")}, []record.SHA{"cc22"})
	if string(got.Records[rid(t, recC)]) != "own\n" {
		t.Fatal("own pending record not contributed")
	}
	if !got.Blobs["cc22"] {
		t.Fatal("own staged blob not contributed")
	}
	if got.Epoch != 1 {
		t.Fatalf("epoch %d, want 1", got.Epoch)
	}
	if len(merged.Records) != 0 {
		t.Fatal("WithOwn mutated its input")
	}
}

func TestStatsMergeFieldWiseMaxThenSum(t *testing.T) {
	// Two copies of the same replica's file reconcile field-wise max; the
	// displayed value sums across replica files (§7.2): A at 2 and B at 3
	// always total 5.
	e := eid(t, ent1)
	a := NewSet()
	a.Stats["AAAAAAAA"] = Stats{e: {I: 2, X: 1, T: 100}}
	b := NewSet()
	b.Stats["AAAAAAAA"] = Stats{e: {I: 1, X: 4, H: 2, T: 300}}
	b.Stats["BBBBBBBB"] = Stats{e: {I: 3}}

	got := Merge(a, b)
	wantA := Row{I: 2, X: 4, H: 2, T: 300}
	if got.Stats["AAAAAAAA"][e] != wantA {
		t.Fatalf("same-replica merge %+v, want %+v", got.Stats["AAAAAAAA"][e], wantA)
	}
	if got.Stats["BBBBBBBB"][e] != (Row{I: 3}) {
		t.Fatalf("other replica file %+v, want {I:3}", got.Stats["BBBBBBBB"][e])
	}
	if total := got.Stats["AAAAAAAA"][e].I + got.Stats["BBBBBBBB"][e].I; total != 5 {
		t.Fatalf("summed impressions %d, want 5", total)
	}
}

func TestStatsMergeUnionsRows(t *testing.T) {
	a := NewSet()
	a.Stats["AAAAAAAA"] = Stats{eid(t, ent1): {I: 1}}
	b := NewSet()
	b.Stats["AAAAAAAA"] = Stats{eid(t, ent2): {X: 1}}
	got := Merge(a, b)
	if len(got.Stats["AAAAAAAA"]) != 2 {
		t.Fatalf("rows %d, want 2", len(got.Stats["AAAAAAAA"]))
	}
}

func TestMergeRecIDCollisionDeterministic(t *testing.T) {
	// Not reachable by construction (rec-ids are unique); if corruption
	// produces two payloads under one filename, both merge orders must
	// still pick the same winner.
	a := NewSet()
	a.Records[rid(t, recA)] = []byte("bbb\n")
	b := NewSet()
	b.Records[rid(t, recA)] = []byte("aaa\n")
	ab := Merge(a, b)
	ba := Merge(b, a)
	if string(ab.Records[rid(t, recA)]) != "aaa\n" || string(ba.Records[rid(t, recA)]) != "aaa\n" {
		t.Fatalf("collision winner ab=%q ba=%q, want aaa", ab.Records[rid(t, recA)], ba.Records[rid(t, recA)])
	}
}
