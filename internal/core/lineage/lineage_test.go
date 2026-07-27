package lineage

import (
	"testing"

	"meltcloud.io/dm/internal/core/evidence"
	"meltcloud.io/dm/internal/core/record"
)

// fakeLineage answers from fixed maps — the classifier's evidence is pure
// data, so its policy unit-tests with no git.
type fakeLineage struct {
	landed map[record.SHA]bool
	reach  map[record.SHA][]evidence.RefTip
}

func (f fakeLineage) LandedInHead(origins []record.SHA) (evidence.Set[record.SHA], error) {
	out := evidence.Set[record.SHA]{}
	for _, o := range origins {
		if f.landed[o] {
			out[o] = struct{}{}
		}
	}
	return out, nil
}

func (f fakeLineage) ReachableFrom(origins []record.SHA) (map[record.SHA][]evidence.RefTip, error) {
	out := map[record.SHA][]evidence.RefTip{}
	for _, o := range origins {
		out[o] = f.reach[o]
	}
	return out, nil
}

func (f fakeLineage) IsAncestor(a, b record.SHA) (bool, error) { return false, nil }

func rec(t *testing.T, ms uint64, tail byte) record.RecID {
	t.Helper()
	var r record.RecID
	for i := 0; i < 6; i++ {
		r[5-i] = byte(ms >> (8 * i))
	}
	r[15] = tail
	return r
}

func TestWinningVerdictsLWWAndVoiding(t *testing.T) {
	recs := []record.Record{
		record.Verdict{Rec: rec(t, 1, 1), Origin: "aaaa", Landed: true, LandedAs: "bbbb", Matcher: record.MatcherReflog},
		record.Verdict{Rec: rec(t, 2, 1), Origin: "aaaa", Landed: false}, // newer unlanded voids
		record.Verdict{Rec: rec(t, 1, 2), Origin: "cccc", Landed: true, LandedAs: "dddd", Matcher: record.MatcherSquash},
		record.Verdict{Rec: rec(t, 1, 1), Origin: "cccc", Landed: true, LandedAs: "eeee", Matcher: record.MatcherManual},
	}
	winning := WinningVerdicts(recs)
	if vd := winning["aaaa"]; vd.Landed {
		t.Errorf("aaaa: unlanded with larger rec-id must win, got landed → %s", vd.LandedAs)
	}
	// Same millisecond: the random tail is the deterministic tiebreak.
	if vd := winning["cccc"]; !vd.Landed || vd.LandedAs != "dddd" {
		t.Errorf("cccc: want dddd (larger tail wins), got %+v", vd)
	}
	bindings := LandedBindings(winning)
	if _, ok := bindings["aaaa"]; ok {
		t.Error("voided origin must not appear in landed bindings")
	}
	if bindings["cccc"] != "dddd" {
		t.Errorf("bindings[cccc] = %s, want dddd", bindings["cccc"])
	}
}

func TestClassifyFold(t *testing.T) {
	ev := fakeLineage{
		landed: map[record.SHA]bool{"head1": true, "s2": true},
		reach: map[record.SHA][]evidence.RefTip{
			"feat1": {{Ref: "feature", Tip: "feat9"}},
		},
	}
	bindings := map[record.SHA]record.SHA{
		"o1": "s1", // two-hop chain: o1 → s1 → s2 (landed)
		"s1": "s2",
		"x1": "x2", // chain dead-ends unreachable
	}
	got, err := Classify(ev, bindings, true, []record.SHA{"head1", "o1", "feat1", "x1", "gone"})
	if err != nil {
		t.Fatal(err)
	}
	want := map[record.SHA]State{
		"head1": Landed,           // step 1: ancestor
		"o1":    Landed,           // step 2: transitive chain into HEAD
		"feat1": PendingElsewhere, // step 3: reachable
		"x1":    Abandoned,        // chain terminal x2 unreachable
		"gone":  Abandoned,        // step 4
	}
	for o, s := range want {
		if got[o].State != s {
			t.Errorf("%s: got %s, want %s", o, got[o].State, s)
		}
	}
	if got["feat1"].Refs[0].Tip != "feat9" {
		t.Errorf("feat1 refs = %+v, want tip feat9", got["feat1"].Refs)
	}
	if got["x1"].Terminal != "x2" {
		t.Errorf("x1 terminal = %s, want the chain end x2", got["x1"].Terminal)
	}
}

func TestClassifyDegradedReadsUnknown(t *testing.T) {
	got, err := Classify(fakeLineage{}, nil, false, []record.SHA{"dead"})
	if err != nil {
		t.Fatal(err)
	}
	if got["dead"].State != Unknown {
		t.Errorf("degraded clone: got %s, want unknown (behaves as pending-elsewhere)", got["dead"].State)
	}
}

func TestClassifyChainCycleAndDepth(t *testing.T) {
	// A cyclic verdict chain must terminate, not loop.
	bindings := map[record.SHA]record.SHA{"a1": "b1", "b1": "a1"}
	got, err := Classify(fakeLineage{}, bindings, true, []record.SHA{"a1"})
	if err != nil {
		t.Fatal(err)
	}
	if got["a1"].State != Abandoned {
		t.Errorf("cyclic chain: got %s, want abandoned", got["a1"].State)
	}
}

func TestReflogSuccessionsActionFilter(t *testing.T) {
	entries := []evidence.ReflogEntry{
		{Ref: "feature", Old: "aaaa", New: "bbbb", Action: "rebase (finish): refs/heads/feature onto 1234"},
		{Ref: "feature", Old: "bbbb", New: "cccc", Action: "commit (amend): tweak"},
		{Ref: "feature", Old: "cccc", New: "dddd", Action: "reset: moving to main"},
		{Ref: "feature", Old: "dddd", New: "eeee", Action: "branch: Reset to eeee"},
		{Ref: "feature", Old: "eeee", New: "ffff", Action: "checkout: moving from main to feature"},
		{Ref: "feature", Old: "ffff", New: "ffff", Action: "rebase (finish): no-op"},
		{Ref: "feature", Old: "abcd", New: "beef", Action: "commit: ordinary"},
		{Ref: "feature", Old: "beef", New: "f00d", Action: "cherry-pick: fix"},
	}
	got := ReflogSuccessions(entries)
	if len(got) != 2 {
		t.Fatalf("got %d successions %+v, want exactly the rebase and the amend", len(got), got)
	}
	if got[0].Old != "aaaa" || got[0].New != "bbbb" || got[1].Old != "bbbb" || got[1].New != "cccc" {
		t.Errorf("successions %+v out of order or mispaired", got)
	}
}
