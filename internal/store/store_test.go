package store

import (
	"os/exec"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"meltcloud.io/dm/internal/core/record"
	"meltcloud.io/dm/internal/core/union"
	"meltcloud.io/dm/internal/gitio"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.name", "t"},
		{"config", "user.email", "t@t"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	repo, err := gitio.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return &Store{Repo: repo}
}

func TestReadEmptyTip(t *testing.T) {
	s := newStore(t)
	set, err := s.Read(nil)
	if err != nil {
		t.Fatal(err)
	}
	if set.Epoch != 0 || len(set.Records) != 0 {
		t.Fatalf("empty tip read %+v", set)
	}
}

// TestCommitReadRoundTrip pins the §8.1 layout: records shard by rec-id
// time prefix, blobs content-address odb-style, stats live one file per
// replica, the epoch marker rides at the root — and Read inverts Commit.
func TestCommitReadRoundTrip(t *testing.T) {
	s := newStore(t)
	when := time.Unix(1700000000, 0)

	rec1, err := record.ParseRecID("01ARZ3NDEKTSV4RRFFQ69G5FAA")
	if err != nil {
		t.Fatal(err)
	}
	entry, err := record.ParseEntryID("01ARZ3NDEKTSV4RRFFQ69G5FA1")
	if err != nil {
		t.Fatal(err)
	}
	blobContent := []byte("staged uncommitted bytes\n")
	blobSHA, err := s.Repo.WriteBlob(blobContent) // content-addressed name
	if err != nil {
		t.Fatal(err)
	}

	set := union.NewSet()
	set.Records[rec1] = []byte("TB 01ARZ3NDEKTSV4RRFFQ69G5FAA 01ARZ3NDEKTSV4RRFFQ69G5FA1\n")
	set.Blobs[blobSHA] = true
	set.Stats["AAAAAAAA"] = union.Stats{entry: {I: 2, X: 1, T: 1700000000000}}

	tip, err := s.Commit(set, nil, union.NewSet(),
		func(record.SHA) ([]byte, bool) { return blobContent, true }, when)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Read(&tip)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(set, got); diff != "" {
		t.Fatalf("round-trip mismatch (-wrote +read):\n%s", diff)
	}

	// Layout pins.
	entries, err := s.Repo.LsTree(tip)
	if err != nil {
		t.Fatal(err)
	}
	paths := map[string]record.SHA{}
	for _, e := range entries {
		paths[e.Path] = e.SHA
	}
	for _, want := range []string{
		"records/01AR/01ARZ3NDEKTSV4RRFFQ69G5FAA",
		"blobs/" + string(blobSHA[:2]) + "/" + string(blobSHA[2:]),
		"stats/AAAAAAAA",
		"epoch",
	} {
		if _, ok := paths[want]; !ok {
			t.Errorf("missing tree path %s (have %v)", want, paths)
		}
	}
	// Unfiltered staged content lands under its own object name.
	if got := paths["blobs/"+string(blobSHA[:2])+"/"+string(blobSHA[2:])]; got != blobSHA {
		t.Errorf("staged blob object %s, want %s", got, blobSHA)
	}
}

// TestCommitNothingNewReturnsBase pins the nothing-to-push detection: a
// set that adds nothing over base yields base itself, and a second commit
// of the same content on top of the first is likewise identity.
func TestCommitNothingNewReturnsBase(t *testing.T) {
	s := newStore(t)
	when := time.Unix(1700000000, 0)
	rec1, err := record.ParseRecID("01ARZ3NDEKTSV4RRFFQ69G5FAA")
	if err != nil {
		t.Fatal(err)
	}
	set := union.NewSet()
	set.Records[rec1] = []byte("TB 01ARZ3NDEKTSV4RRFFQ69G5FAA 01ARZ3NDEKTSV4RRFFQ69G5FA1\n")
	tip, err := s.Commit(set, nil, union.NewSet(), nil, when)
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.Commit(set, &tip, set, nil, when)
	if err != nil {
		t.Fatal(err)
	}
	if again != tip {
		t.Fatalf("no-op commit produced %s, want base %s", again, tip)
	}
}

// TestCommitIsDeterministic: same content, parents, and clock → same SHA
// (§11.2 — the fixed dm ident keeps store history reproducible).
func TestCommitIsDeterministic(t *testing.T) {
	when := time.Unix(1700000000, 0)
	rec1, err := record.ParseRecID("01ARZ3NDEKTSV4RRFFQ69G5FAA")
	if err != nil {
		t.Fatal(err)
	}
	set := union.NewSet()
	set.Records[rec1] = []byte("TB 01ARZ3NDEKTSV4RRFFQ69G5FAA 01ARZ3NDEKTSV4RRFFQ69G5FA1\n")
	var tips []record.SHA
	for i := 0; i < 2; i++ {
		s := newStore(t)
		tip, err := s.Commit(set, nil, union.NewSet(), nil, when)
		if err != nil {
			t.Fatal(err)
		}
		tips = append(tips, tip)
	}
	if tips[0] != tips[1] {
		t.Fatalf("same content committed to %s and %s in two repos", tips[0], tips[1])
	}
}
