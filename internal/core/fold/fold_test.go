package fold

import (
	"bytes"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/oklog/ulid/v2"

	"meltcloud.io/dm/internal/core/record"
)

// rid mints a rec-id at millisecond n with a deterministic tail: rec-id
// order in every test below is exactly the numeric argument order (§8.3's
// LWW total order, pinned).
func rid(n int) record.RecID {
	return record.RecID(pinnedULID(n))
}

// eid mints an entry-id likewise.
func eid(n int) record.EntryID {
	return record.EntryID(pinnedULID(n))
}

func pinnedULID(n int) ulid.ULID {
	tail := bytes.Repeat([]byte{byte(n)}, 10)
	u, err := ulid.New(uint64(n), bytes.NewReader(tail))
	if err != nil {
		panic(err)
	}
	return u
}

const (
	originMain   = record.SHA("aaaa") // landed in every test's checkout
	originBranch = record.SHA("bbbb") // an unmerged line
)

var landedMain = map[record.SHA]bool{originMain: true, originBranch: false}

var entry = eid(1)

func cr(n int, body string) record.Create {
	return record.Create{Rec: rid(n), Entry: entry, Subj: record.SubjCode,
		Anchor: record.BlobAnchor("11f1"), Origin: originMain, Path: "a.rb", Body: body}
}

func TestFoldCreateOnly(t *testing.T) {
	got := Fold(entry, []record.Record{cr(1, "v1")}, landedMain)
	want := Entry{ID: entry, Landed: true, Subj: record.SubjCode,
		Anchor: record.BlobAnchor("11f1"), Path: "a.rb", Body: "v1", Rev: 1}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("fold mismatch (-want +got):\n%s", diff)
	}
}

func TestFoldSupersedeWins(t *testing.T) {
	su := record.Supersede{Rec: rid(2), Entry: entry, Subj: record.SubjDev,
		Anchor: record.BlobAnchor("22f2"), Origin: originMain, Path: "b.rb", Body: "v2"}
	// Order-independent: records arrive shuffled, the fold sorts.
	got := Fold(entry, []record.Record{su, cr(1, "v1")}, landedMain)
	want := Entry{ID: entry, Landed: true, Subj: record.SubjDev,
		Anchor: record.BlobAnchor("22f2"), Path: "b.rb", Body: "v2", Rev: 2}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("fold mismatch (-want +got):\n%s", diff)
	}
}

// Concurrent supersede (§8.3 golden): both SUs landed → largest rec-id
// wins; the loser survives as a sibling revision, so churn counts it.
func TestFoldConcurrentSupersede(t *testing.T) {
	suA := record.Supersede{Rec: rid(2), Entry: entry, Subj: record.SubjCode,
		Anchor: record.BlobAnchor("22f2"), Origin: originMain, Path: "a.rb", Body: "replica A"}
	suB := record.Supersede{Rec: rid(3), Entry: entry, Subj: record.SubjCode,
		Anchor: record.BlobAnchor("33f3"), Origin: originMain, Path: "a.rb", Body: "replica B"}
	for name, recs := range map[string][]record.Record{
		"ab": {cr(1, "v1"), suA, suB},
		"ba": {cr(1, "v1"), suB, suA},
	} {
		got := Fold(entry, recs, landedMain)
		if got.Body != "replica B" || got.Anchor.Blob != "33f3" || got.Rev != 3 {
			t.Errorf("%s: folded %+v, want replica B at rev 3", name, got)
		}
	}
}

// u-old-revision-on-branch (§11.4): the SU's line has not landed here, so
// this checkout reads the prior revision — its lineage's newest state.
func TestFoldUnlandedSupersede(t *testing.T) {
	su := record.Supersede{Rec: rid(2), Entry: entry, Subj: record.SubjCode,
		Anchor: record.BlobAnchor("22f2"), Origin: originBranch, Path: "a.rb", Body: "v2"}
	got := Fold(entry, []record.Record{cr(1, "v1"), su}, landedMain)
	if got.Body != "v1" || got.Anchor.Blob != "11f1" || got.Rev != 1 {
		t.Errorf("folded %+v, want the landed v1 state", got)
	}
}

// Tombstone-terminal (§8.3 golden): TB absorbs — a later SU loses
// regardless of rec-id order, and TB needs no origin to land.
func TestFoldTombstoneAbsorbs(t *testing.T) {
	tb := record.Tombstone{Rec: rid(2), Entry: entry}
	su := record.Supersede{Rec: rid(3), Entry: entry, Subj: record.SubjCode,
		Anchor: record.BlobAnchor("33f3"), Origin: originMain, Path: "a.rb", Body: "necromancy"}
	got := Fold(entry, []record.Record{cr(1, "v1"), tb, su}, landedMain)
	if !got.Deleted {
		t.Errorf("folded %+v, want Deleted", got)
	}
}

func TestFoldReAnchor(t *testing.T) {
	ra := record.ReAnchor{Rec: rid(2), Entry: entry,
		Anchor: record.BlobAnchor("22f2"), Origin: originMain, Path: "moved.rb"}
	got := Fold(entry, []record.Record{cr(1, "v1"), ra}, landedMain)
	// Anchor and path re-stamped; body untouched — RA is no revision.
	want := Entry{ID: entry, Landed: true, Subj: record.SubjCode,
		Anchor: record.BlobAnchor("22f2"), Path: "moved.rb", Body: "v1", Rev: 1}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("fold mismatch (-want +got):\n%s", diff)
	}
}

// k-no-retract (§11.4): a k minted on an unmerged line folds only where
// that line has landed; here it hasn't, so the prior anchor stands.
func TestFoldUnlandedReAnchor(t *testing.T) {
	ra := record.ReAnchor{Rec: rid(2), Entry: entry,
		Anchor: record.BlobAnchor("22f2"), Origin: originBranch, Path: "moved.rb"}
	got := Fold(entry, []record.Record{cr(1, "v1"), ra}, landedMain)
	if got.Anchor.Blob != "11f1" || got.Path != "a.rb" {
		t.Errorf("folded %+v, want the pre-k state", got)
	}
}

// RP relocates, never blesses (§6.5): path moves, anchor stays.
func TestFoldRePath(t *testing.T) {
	rp := record.RePath{Rec: rid(2), Entry: entry, Origin: originMain, Path: "new.rb"}
	got := Fold(entry, []record.Record{cr(1, "v1"), rp}, landedMain)
	if got.Path != "new.rb" || got.Anchor.Blob != "11f1" || got.Body != "v1" {
		t.Errorf("folded %+v, want path new.rb with the CR anchor untouched", got)
	}
}

// rp-scoped (§11.4): the move's line hasn't landed here — the hint can
// never mislead a line where the move hasn't happened.
func TestFoldUnlandedRePath(t *testing.T) {
	rp := record.RePath{Rec: rid(2), Entry: entry, Origin: originBranch, Path: "new.rb"}
	got := Fold(entry, []record.Record{cr(1, "v1"), rp}, landedMain)
	if got.Path != "a.rb" {
		t.Errorf("folded path %q, want the pre-move a.rb", got.Path)
	}
}

// Rescue fold (§8.3): a landed RA carries the entry alone — body from the
// newest CR/SU preceding it (the revision its writer acted on), anchor
// from the RA.
func TestFoldRescueViaReAnchor(t *testing.T) {
	crBranch := record.Create{Rec: rid(1), Entry: entry, Subj: record.SubjCode,
		Anchor: record.BlobAnchor("11f1"), Origin: originBranch, Path: "a.rb", Body: "branch body"}
	ra := record.ReAnchor{Rec: rid(2), Entry: entry,
		Anchor: record.BlobAnchor("22f2"), Origin: originMain, Path: "rescued.rb"}
	got := Fold(entry, []record.Record{crBranch, ra}, landedMain)
	want := Entry{ID: entry, Landed: true, Subj: record.SubjCode,
		Anchor: record.BlobAnchor("22f2"), Path: "rescued.rb", Body: "branch body", Rev: 1}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("fold mismatch (-want +got):\n%s", diff)
	}
}

// Rescue via RP only: no landed RA supplies an anchor, so the anchor folds
// from the preceding CR/SU too.
func TestFoldRescueViaRePath(t *testing.T) {
	crBranch := record.Create{Rec: rid(1), Entry: entry, Subj: record.SubjCode,
		Anchor: record.BlobAnchor("11f1"), Origin: originBranch, Path: "a.rb", Body: "branch body"}
	rp := record.RePath{Rec: rid(2), Entry: entry, Origin: originMain, Path: "new.rb"}
	got := Fold(entry, []record.Record{crBranch, rp}, landedMain)
	if got.Body != "branch body" || got.Anchor.Blob != "11f1" || got.Path != "new.rb" {
		t.Errorf("folded %+v, want rescued body+anchor with the RP path", got)
	}
}

// The rescue reaches for the revision preceding the newest landed record —
// not the globally newest revision.
func TestFoldRescuePicksPrecedingRevision(t *testing.T) {
	cr1 := record.Create{Rec: rid(1), Entry: entry, Subj: record.SubjCode,
		Anchor: record.BlobAnchor("11f1"), Origin: originBranch, Path: "a.rb", Body: "v1"}
	su2 := record.Supersede{Rec: rid(2), Entry: entry, Subj: record.SubjCode,
		Anchor: record.BlobAnchor("22f2"), Origin: originBranch, Path: "a.rb", Body: "v2"}
	ra3 := record.ReAnchor{Rec: rid(3), Entry: entry,
		Anchor: record.BlobAnchor("33f3"), Origin: originMain, Path: "a.rb"}
	su4 := record.Supersede{Rec: rid(4), Entry: entry, Subj: record.SubjCode,
		Anchor: record.BlobAnchor("44f4"), Origin: originBranch, Path: "a.rb", Body: "v3"}
	got := Fold(entry, []record.Record{cr1, su2, ra3, su4}, landedMain)
	if got.Body != "v2" || got.Anchor.Blob != "33f3" {
		t.Errorf("folded %+v, want the v2 body the RA's writer acted on", got)
	}
}

// No landed record at all: the entry is not present at this checkout
// (pending-elsewhere/abandoned — classified in M6, hidden either way).
func TestFoldNothingLanded(t *testing.T) {
	crBranch := record.Create{Rec: rid(1), Entry: entry, Subj: record.SubjCode,
		Anchor: record.BlobAnchor("11f1"), Origin: originBranch, Path: "a.rb", Body: "v1"}
	got := Fold(entry, []record.Record{crBranch}, landedMain)
	if got.Landed || got.Deleted {
		t.Errorf("folded %+v, want not landed", got)
	}
}

func TestFoldDispute(t *testing.T) {
	fbAt := func(n int) record.Feedback {
		return record.Feedback{Rec: rid(n), Entry: entry, Sig: record.SigDisputed, Reason: "wrong"}
	}
	suAt := func(n int, origin record.SHA) record.Supersede {
		return record.Supersede{Rec: rid(n), Entry: entry, Subj: record.SubjCode,
			Anchor: record.BlobAnchor("22f2"), Origin: origin, Path: "a.rb", Body: "v2"}
	}
	raAt := func(n int, origin record.SHA) record.ReAnchor {
		return record.ReAnchor{Rec: rid(n), Entry: entry,
			Anchor: record.BlobAnchor("22f2"), Origin: origin, Path: "a.rb"}
	}
	rpAt := func(n int) record.RePath {
		return record.RePath{Rec: rid(n), Entry: entry, Origin: originMain, Path: "new.rb"}
	}
	tests := []struct {
		name string
		recs []record.Record
		want bool
	}{
		{"fb! disputes", []record.Record{cr(1, "v1"), fbAt(2)}, true},
		{"landed su clears", []record.Record{cr(1, "v1"), fbAt(2), suAt(3, originMain)}, false},
		{"landed ra clears", []record.Record{cr(1, "v1"), fbAt(2), raAt(3, originMain)}, false},
		{"rp never clears", []record.Record{cr(1, "v1"), fbAt(2), rpAt(3)}, true},
		{"unlanded su does not clear here", []record.Record{cr(1, "v1"), fbAt(2), suAt(3, originBranch)}, true},
		{"re-filed after clear disputes again", []record.Record{cr(1, "v1"), fbAt(2), suAt(3, originMain), fbAt(4)}, true},
		{"plus and minus never dispute", []record.Record{cr(1, "v1"),
			record.Feedback{Rec: rid(2), Entry: entry, Sig: record.SigUseful},
			record.Feedback{Rec: rid(3), Entry: entry, Sig: record.SigNotHere}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Fold(entry, tt.recs, landedMain); got.Disputed != tt.want {
				t.Errorf("Disputed = %v, want %v", got.Disputed, tt.want)
			}
		})
	}
}

func TestLinks(t *testing.T) {
	a, b, c := eid(11), eid(12), eid(13)
	ln := func(n int, from, to record.EntryID, comment string) record.Link {
		return record.Link{Rec: rid(n), From: from, To: to, Comment: comment}
	}
	ul := func(n int, from, to record.EntryID) record.Unlink {
		return record.Unlink{Rec: rid(n), From: from, To: to}
	}
	tests := []struct {
		name string
		recs []record.Record
		want []Link
	}{
		{"link stands", []record.Record{ln(1, a, b, "why")},
			[]Link{{Rec: rid(1), From: a, To: b, Comment: "why"}}},
		{"unlink removes", []record.Record{ln(1, a, b, ""), ul(2, a, b)}, nil},
		{"relink wins with fresh comment", []record.Record{ln(1, a, b, "old"), ul(2, a, b), ln(3, a, b, "new")},
			[]Link{{Rec: rid(3), From: a, To: b, Comment: "new"}}},
		{"directed pairs are independent", []record.Record{ln(1, a, b, ""), ln(2, b, a, ""), ul(3, b, a)},
			[]Link{{Rec: rid(1), From: a, To: b}}},
		{"deterministic order by winning rec-id", []record.Record{ln(2, a, c, ""), ln(1, a, b, "")},
			[]Link{{Rec: rid(1), From: a, To: b}, {Rec: rid(2), From: a, To: c}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if diff := cmp.Diff(tt.want, Links(tt.recs)); diff != "" {
				t.Errorf("links mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
