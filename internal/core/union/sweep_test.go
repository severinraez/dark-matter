package union

import (
	"bytes"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/oklog/ulid/v2"

	"meltcloud.io/dm/internal/core/record"
)

// Pinned-clock sweep fixtures. sweepNow anchors every age computation;
// recent ids mint days before it, old ids well past the ShadowWindow.
var sweepNow = time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

// ridAt mints a rec-id at a given instant with a pinned tail byte, so
// tests control both the LWW order and the ULID age the window rule reads.
func ridAt(t *testing.T, at time.Time, tail byte) record.RecID {
	t.Helper()
	u, err := ulid.New(ulid.Timestamp(at), bytes.NewReader([]byte{0, 0, 0, 0, 0, 0, 0, 0, 0, tail}))
	if err != nil {
		t.Fatal(err)
	}
	return record.RecID(u)
}

func recent(t *testing.T, tail byte) record.RecID {
	return ridAt(t, sweepNow.Add(-time.Duration(tail)*time.Hour), tail)
}

func old(t *testing.T, tail byte) record.RecID {
	return ridAt(t, sweepNow.Add(-ShadowWindow-30*24*time.Hour+time.Duration(tail)*time.Hour), tail)
}

// add encodes a record into the set — Sweep consumes canonical bytes, as
// the store holds them.
func add(t *testing.T, s *Set, r record.Record) {
	t.Helper()
	data, err := record.Encode(r)
	if err != nil {
		t.Fatal(err)
	}
	s.Records[r.ID()] = data
}

func sweep(t *testing.T, s Set) (Set, Dropped) {
	t.Helper()
	out, dropped, err := Sweep(s, sweepNow)
	if err != nil {
		t.Fatal(err)
	}
	return out, dropped
}

func has(s Set, id record.RecID) bool { _, ok := s.Records[id]; return ok }

func TestSweepTombstonePurgesEverythingItSuppresses(t *testing.T) {
	e, other := eid(t, ent1), eid(t, ent2)
	cr := record.Create{Rec: recent(t, 1), Entry: e, Subj: record.SubjCode,
		Anchor: record.BlobAnchor("aabb01"), Origin: "abcd01", Path: "f.go", Body: "doomed"}
	ra := record.ReAnchor{Rec: recent(t, 2), Entry: e,
		Anchor: record.BlobAnchor("aabb02"), Origin: "abcd02", Path: "f.go"}
	fb := record.Feedback{Rec: recent(t, 3), Entry: e, Sig: record.SigDisputed}
	tb := record.Tombstone{Rec: recent(t, 4), Entry: e}
	ln := record.Link{Rec: recent(t, 5), From: other, To: e}
	liveCR := record.Create{Rec: recent(t, 6), Entry: other, Subj: record.SubjCode,
		Anchor: record.BlobAnchor("aabb03"), Origin: "abcd03", Path: "g.go", Body: "live"}

	s := NewSet()
	for _, r := range []record.Record{cr, ra, fb, tb, ln, liveCR} {
		add(t, &s, r)
	}
	s.Blobs["aabb01"], s.Blobs["aabb02"], s.Blobs["aabb03"] = true, true, true
	s.Stats["AAAAAAAA"] = Stats{e: {I: 3}, other: {I: 1}}

	out, dropped := sweep(t, s)
	// The TB line alone survives the entry — the permanent suppressor; the
	// live entry is untouched; the link into the dead entry is dangling.
	if !has(out, tb.Rec) || !has(out, liveCR.Rec) {
		t.Fatal("TB line or live entry swept")
	}
	for _, id := range []record.RecID{cr.Rec, ra.Rec, fb.Rec, ln.Rec} {
		if has(out, id) {
			t.Fatalf("suppressed record %s survived", id)
		}
	}
	if out.Blobs["aabb01"] || out.Blobs["aabb02"] || !out.Blobs["aabb03"] {
		t.Fatalf("blob sweep wrong: %v", out.Blobs)
	}
	if _, ok := out.Stats["AAAAAAAA"][e]; ok {
		t.Fatal("stat row for purged entry survived")
	}
	if out.Stats["AAAAAAAA"][other] != (Row{I: 1}) {
		t.Fatal("stat row for living entry lost")
	}
	if dropped != (Dropped{Records: 4, Blobs: 2}) {
		t.Fatalf("dropped tally %+v", dropped)
	}
	if out.Epoch != 1 {
		t.Fatalf("epoch %d, want 1", out.Epoch)
	}
}

func TestSweepTombstoneKeptForeverEvenOld(t *testing.T) {
	tb := record.Tombstone{Rec: old(t, 1), Entry: eid(t, ent1)}
	s := NewSet()
	add(t, &s, tb)
	out, _ := sweep(t, s)
	if !has(out, tb.Rec) {
		t.Fatal("ancient TB swept — TB is kept forever")
	}
	// And it stays across generations.
	out2, _ := sweep(t, out)
	if !has(out2, tb.Rec) || out2.Epoch != 2 {
		t.Fatalf("second sweep lost the TB or the epoch: %d", out2.Epoch)
	}
}

func TestSweepShadowedRevisionWindow(t *testing.T) {
	e := eid(t, ent1)
	oldCR := record.Create{Rec: old(t, 1), Entry: e, Subj: record.SubjCode,
		Anchor: record.BlobAnchor("aabb01"), Origin: "abcd01", Path: "f.go", Body: "v1"}
	oldSU := record.Supersede{Rec: old(t, 2), Entry: e, Subj: record.SubjCode,
		Anchor: record.BlobAnchor("aabb02"), Origin: "abcd02", Path: "f.go", Body: "v2"}
	recentSU := record.Supersede{Rec: recent(t, 3), Entry: e, Subj: record.SubjCode,
		Anchor: record.BlobAnchor("aabb03"), Origin: "abcd03", Path: "f.go", Body: "v3"}
	newest := record.Supersede{Rec: recent(t, 2), Entry: e, Subj: record.SubjCode,
		Anchor: record.BlobAnchor("aabb04"), Origin: "abcd04", Path: "f.go", Body: "v4"}

	s := NewSet()
	for _, r := range []record.Record{oldCR, oldSU, recentSU, newest} {
		add(t, &s, r)
	}
	out, _ := sweep(t, s)
	// Old shadowed revisions drop; within the window they stay (the
	// recency-scoped churn signal); the newest survives regardless.
	if has(out, oldCR.Rec) || has(out, oldSU.Rec) {
		t.Fatal("shadowed revisions older than the window survived")
	}
	if !has(out, recentSU.Rec) || !has(out, newest.Rec) {
		t.Fatal("in-window shadowed revision or newest revision swept")
	}
}

func TestSweepNewestRevisionKeptRegardlessOfAge(t *testing.T) {
	e := eid(t, ent1)
	cr := record.Create{Rec: old(t, 1), Entry: e, Subj: record.SubjCode,
		Anchor: record.BlobAnchor("aabb01"), Origin: "abcd01", Path: "f.go", Body: "only"}
	s := NewSet()
	add(t, &s, cr)
	s.Blobs["aabb01"] = true
	out, dropped := sweep(t, s)
	if !has(out, cr.Rec) || !out.Blobs["aabb01"] {
		t.Fatal("an entry's only (newest) revision must never sweep")
	}
	if dropped != (Dropped{}) {
		t.Fatalf("dropped tally %+v, want zero", dropped)
	}
}

func TestSweepRescuePinsRevisionUnderRAAndRP(t *testing.T) {
	e := eid(t, ent1)
	// Chain: CR(old) → SU1(old) → RA → SU2(old) → RP → SU3(newest).
	// The RA pins SU1 (the newest note preceding it — the rescue body);
	// the RP pins SU2; the CR is unpinned old shadow and drops.
	cr := record.Create{Rec: old(t, 1), Entry: e, Subj: record.SubjCode,
		Anchor: record.BlobAnchor("aabb01"), Origin: "abcd01", Path: "f.go", Body: "v1"}
	su1 := record.Supersede{Rec: old(t, 2), Entry: e, Subj: record.SubjCode,
		Anchor: record.BlobAnchor("aabb02"), Origin: "abcd02", Path: "f.go", Body: "v2"}
	ra := record.ReAnchor{Rec: old(t, 3), Entry: e,
		Anchor: record.BlobAnchor("aabb03"), Origin: "abcd03", Path: "f.go"}
	su2 := record.Supersede{Rec: old(t, 4), Entry: e, Subj: record.SubjCode,
		Anchor: record.BlobAnchor("aabb04"), Origin: "abcd04", Path: "f.go", Body: "v3"}
	rp := record.RePath{Rec: old(t, 5), Entry: e, Origin: "abcd05", Path: "g.go"}
	su3 := record.Supersede{Rec: recent(t, 1), Entry: e, Subj: record.SubjCode,
		Anchor: record.BlobAnchor("aabb05"), Origin: "abcd06", Path: "g.go", Body: "v4"}

	s := NewSet()
	for _, r := range []record.Record{cr, su1, ra, su2, rp, su3} {
		add(t, &s, r)
	}
	out, _ := sweep(t, s)
	if has(out, cr.Rec) {
		t.Fatal("unpinned old shadow survived")
	}
	for name, id := range map[string]record.RecID{
		"RA-pinned SU1": su1.Rec, "RA": ra.Rec, "RP-pinned SU2": su2.Rec, "RP": rp.Rec, "newest SU3": su3.Rec,
	} {
		if !has(out, id) {
			t.Fatalf("%s swept", name)
		}
	}
}

func TestSweepHeadlessRecordsAreGarbage(t *testing.T) {
	// Records referencing an entry with no CR/SU and no TB — the state a
	// late-landing record reaches after its entry was purged. Ignored at
	// read, swept here (§8.7 safety invariant).
	ghost, live := eid(t, ent1), eid(t, ent2)
	ra := record.ReAnchor{Rec: recent(t, 1), Entry: ghost,
		Anchor: record.BlobAnchor("aabb01"), Origin: "abcd01", Path: "f.go"}
	fb := record.Feedback{Rec: recent(t, 2), Entry: ghost, Sig: record.SigUseful}
	cr := record.Create{Rec: recent(t, 3), Entry: live, Subj: record.SubjCode,
		Anchor: record.BlobAnchor("aabb02"), Origin: "abcd02", Path: "g.go", Body: "live"}
	ln := record.Link{Rec: recent(t, 4), From: live, To: ghost}
	ul := record.Unlink{Rec: recent(t, 5), From: ghost, To: live}

	s := NewSet()
	for _, r := range []record.Record{ra, fb, cr, ln, ul} {
		add(t, &s, r)
	}
	s.Stats["AAAAAAAA"] = Stats{ghost: {I: 2}}
	out, _ := sweep(t, s)
	for _, id := range []record.RecID{ra.Rec, fb.Rec, ln.Rec, ul.Rec} {
		if has(out, id) {
			t.Fatalf("headless/dangling record %s survived", id)
		}
	}
	if !has(out, cr.Rec) {
		t.Fatal("living entry swept")
	}
	if len(out.Stats) != 0 {
		t.Fatalf("stat file for purged rows survived: %v", out.Stats)
	}
}

func TestSweepLinkBetweenLivingEntriesKept(t *testing.T) {
	e1, e2 := eid(t, ent1), eid(t, ent2)
	cr1 := record.Create{Rec: recent(t, 1), Entry: e1, Subj: record.SubjCode,
		Anchor: record.BlobAnchor("aabb01"), Origin: "abcd01", Path: "f.go", Body: "one"}
	cr2 := record.Create{Rec: recent(t, 2), Entry: e2, Subj: record.SubjCode,
		Anchor: record.BlobAnchor("aabb02"), Origin: "abcd02", Path: "g.go", Body: "two"}
	ln := record.Link{Rec: recent(t, 3), From: e1, To: e2, Comment: "see also"}
	ul := record.Unlink{Rec: recent(t, 4), From: e1, To: e2}
	s := NewSet()
	for _, r := range []record.Record{cr1, cr2, ln, ul} {
		add(t, &s, r)
	}
	out, _ := sweep(t, s)
	if !has(out, ln.Rec) || !has(out, ul.Rec) {
		t.Fatal("LN/UL between living entries swept")
	}
}

func TestSweepVerdictRetentionAndChain(t *testing.T) {
	e := eid(t, ent1)
	cr := record.Create{Rec: recent(t, 1), Entry: e, Subj: record.SubjCode,
		Anchor: record.BlobAnchor("aabb01"), Origin: "f1f1f1", Path: "f.go", Body: "note"}
	// Chain F1 → S1 → S1′: both irreplaceable while the CR carries F1.
	vd1 := record.Verdict{Rec: recent(t, 2), Origin: "f1f1f1", Landed: true, LandedAs: "51e051", Matcher: record.MatcherSquash}
	vd2 := record.Verdict{Rec: recent(t, 3), Origin: "51e051", Landed: true, LandedAs: "51e052", Matcher: record.MatcherReflog}
	// An unlanded voiding on a needed origin stays (it is the LWW winner).
	vd3 := record.Verdict{Rec: recent(t, 4), Origin: "f1f1f1"}
	// A verdict no surviving record needs drops — even though its own
	// landed-as chains onward, nothing roots it.
	vd4 := record.Verdict{Rec: recent(t, 5), Origin: "deadaa", Landed: true, LandedAs: "deadab", Matcher: record.MatcherPaired}
	vd5 := record.Verdict{Rec: recent(t, 6), Origin: "deadab", Landed: true, LandedAs: "deadac", Matcher: record.MatcherReflog}

	s := NewSet()
	for _, r := range []record.Record{cr, vd1, vd2, vd3, vd4, vd5} {
		add(t, &s, r)
	}
	out, _ := sweep(t, s)
	for name, id := range map[string]record.RecID{"VD F1→S1": vd1.Rec, "chained VD S1→S1′": vd2.Rec, "unlanded VD on F1": vd3.Rec} {
		if !has(out, id) {
			t.Fatalf("%s swept while a record carries its origin", name)
		}
	}
	if has(out, vd4.Rec) || has(out, vd5.Rec) {
		t.Fatal("rootless verdict chain survived")
	}

	// Tombstone the entry: nothing carries F1 any more — the whole chain
	// drops on the next sweep. TB pins no verdicts (it has no origin).
	add(t, &out, record.Tombstone{Rec: recent(t, 7), Entry: e})
	out2, _ := sweep(t, out)
	for _, id := range []record.RecID{vd1.Rec, vd2.Rec, vd3.Rec} {
		if has(out2, id) {
			t.Fatalf("verdict %s survived its last origin-carrying record", id)
		}
	}
}

func TestSweepDeterministic(t *testing.T) {
	e := eid(t, ent1)
	s := NewSet()
	add(t, &s, record.Create{Rec: old(t, 1), Entry: e, Subj: record.SubjCode,
		Anchor: record.BlobAnchor("aabb01"), Origin: "abcd01", Path: "f.go", Body: "v1"})
	add(t, &s, record.Supersede{Rec: recent(t, 1), Entry: e, Subj: record.SubjCode,
		Anchor: record.BlobAnchor("aabb02"), Origin: "abcd02", Path: "f.go", Body: "v2"})
	s.Blobs["aabb01"], s.Blobs["aabb02"] = true, true
	s.Stats["AAAAAAAA"] = Stats{e: {I: 1}}

	a, _ := sweep(t, s)
	b, _ := sweep(t, s)
	if diff := cmp.Diff(a, b); diff != "" {
		t.Fatalf("two sweeps over the same tip diverged (-a +b):\n%s", diff)
	}
	if len(s.Records) != 2 {
		t.Fatal("sweep mutated its input")
	}
}
