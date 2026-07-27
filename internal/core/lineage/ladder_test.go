package lineage

import (
	"testing"

	"meltcloud.io/dm/internal/core/evidence"
	"meltcloud.io/dm/internal/core/record"
)

// fakeMatch scripts the forensics half of the port: linear "lines" of
// commits with assigned patch-ids, so every ladder guard replays as pure
// policy over recorded evidence (architecture.md §1).
type fakeMatch struct {
	mergeBase map[[2]record.SHA]record.SHA
	segments  map[[2]record.SHA][]record.SHA // (base, tip) → (base..tip] oldest first
	patchIDs  map[record.SHA]string          // "" or absent = empty diff
	ranges    map[[2]record.SHA]evidence.RangeID
}

func (f fakeMatch) MergeBase(a, b record.SHA) (*record.SHA, error) {
	if mb, ok := f.mergeBase[[2]record.SHA{a, b}]; ok {
		return &mb, nil
	}
	if mb, ok := f.mergeBase[[2]record.SHA{b, a}]; ok {
		return &mb, nil
	}
	return nil, nil
}

func (f fakeMatch) Segment(base, tip record.SHA) ([]record.SHA, error) {
	return f.segments[[2]record.SHA{base, tip}], nil
}

func (f fakeMatch) PatchID(c record.SHA) (*evidence.PatchID, error) {
	id, ok := f.patchIDs[c]
	if !ok || id == "" {
		return nil, nil
	}
	pid := evidence.PatchID(id)
	return &pid, nil
}

func (f fakeMatch) RangePatchID(base, tip record.SHA) (*evidence.RangeID, error) {
	rid, ok := f.ranges[[2]record.SHA{base, tip}]
	if !ok {
		return nil, nil
	}
	return &rid, nil
}

func (f fakeMatch) RenamePairs(record.SHA) ([]evidence.RenamePair, error) { return nil, nil }
func (f fakeMatch) Score(record.SHA, []record.Path) (map[record.Path]int, error) {
	return nil, nil
}
func (f fakeMatch) ReflogEntries() ([]evidence.ReflogEntry, error) { return nil, nil }

func TestBindSegmentEnumeration(t *testing.T) {
	m := fakeMatch{
		mergeBase: map[[2]record.SHA]record.SHA{{"f3", "f3p"}: "mb"},
		segments:  map[[2]record.SHA][]record.SHA{{"mb", "f3"}: {"f1", "f2", "f3"}},
	}
	// Candidates: two note origins in the segment, one existing-VD
	// landed-as (s1) also in it — the chain clause — one outside.
	got, err := BindSegment(m, "f3", "f3p", []record.SHA{"f2", "f1", "s1", "elsewhere", "f2"}, record.MatcherReflog)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d bindings %+v, want f1 and f2", len(got), got)
	}
	if got[0].Origin != "f1" || got[1].Origin != "f2" {
		t.Errorf("bindings %+v not in deterministic origin order", got)
	}
	for _, b := range got {
		if b.LandedAs != "f3p" || b.Matcher != record.MatcherReflog {
			t.Errorf("binding %+v: want landed-as f3p, matcher m1", b)
		}
	}
}

func TestAttemptReplayFullSegmentInOrder(t *testing.T) {
	base := fakeMatch{
		mergeBase: map[[2]record.SHA]record.SHA{{"old3", "new3"}: "mb"},
		segments: map[[2]record.SHA][]record.SHA{
			{"mb", "old3"}: {"o1", "o2", "o3"},
			{"mb", "new3"}: {"m1", "m2", "n1", "n2", "n3"}, // rebase onto fresh base commits
		},
		patchIDs: map[record.SHA]string{
			"o1": "pa", "o2": "pb", "o3": "pc",
			"m1": "px", "m2": "py",
			"n1": "pa", "n2": "pb", "n3": "pc",
		},
		ranges: map[[2]record.SHA]evidence.RangeID{{"mb", "old3"}: {PatchID: "cum", DiffSize: 12}},
	}

	if ok, hint, err := AttemptReplay(base, "old3", "new3"); err != nil || !ok {
		t.Fatalf("clean replay: ok=%v hint=%q err=%v, want bind", ok, hint, err)
	}

	// Tip-only pairing: earlier commit rewritten by conflict → no bind.
	conflicted := base
	conflicted.patchIDs = map[record.SHA]string{
		"o1": "pa", "o2": "pb", "o3": "pc",
		"m1": "px", "m2": "py",
		"n1": "pa", "n2": "DIFFERENT", "n3": "pc",
	}
	if ok, hint, _ := AttemptReplay(conflicted, "old3", "new3"); ok || hint != "m2: unpaired commit" {
		t.Errorf("conflicted replay: ok=%v hint=%q, want the full-segment guard to refuse", ok, hint)
	}

	// Out of order pairing must also refuse.
	swapped := base
	swapped.patchIDs = map[record.SHA]string{
		"o1": "pa", "o2": "pb", "o3": "pc",
		"n1": "pb", "n2": "pa", "n3": "pc",
	}
	if ok, _, _ := AttemptReplay(swapped, "old3", "new3"); ok {
		t.Error("out-of-order pairing bound — in-order is the guard")
	}

	// Empty-diff commits on the old side are skipped, not required to pair.
	withEmpty := base
	withEmpty.patchIDs = map[record.SHA]string{
		"o1": "pa", "o2": "", "o3": "pc",
		"n1": "pa", "n3": "pc",
	}
	if ok, hint, _ := AttemptReplay(withEmpty, "old3", "new3"); !ok {
		t.Errorf("empty-diff skip: hint=%q, want bind", hint)
	}

	// Below the floor (trivial segment under branch reuse) → no bind.
	floor := base
	floor.ranges = map[[2]record.SHA]evidence.RangeID{{"mb", "old3"}: {PatchID: "cum", DiffSize: MinDiffFloor - 1}}
	if ok, hint, _ := AttemptReplay(floor, "old3", "new3"); ok || hint == "" {
		t.Errorf("sub-floor replay bound (hint=%q)", hint)
	}

	// No merge base (unrelated occupant after branch reuse) → no bind.
	if ok, hint, _ := AttemptReplay(fakeMatch{}, "old3", "new3"); ok || hint != "m2: no merge base" {
		t.Errorf("disjoint replay: ok=%v hint=%q", ok, hint)
	}
}

func TestAttemptSquash(t *testing.T) {
	base := fakeMatch{
		mergeBase: map[[2]record.SHA]record.SHA{{"f3", "main"}: "mb"},
		segments:  map[[2]record.SHA][]record.SHA{{"mb", "main"}: {"c1", "sq", "c2"}},
		patchIDs:  map[record.SHA]string{"c1": "pa", "sq": "cum", "c2": "pb"},
		ranges:    map[[2]record.SHA]evidence.RangeID{{"mb", "f3"}: {PatchID: "cum", DiffSize: 9}},
	}
	landedAs, hint, err := AttemptSquash(base, "f3", "main")
	if err != nil || landedAs == nil || *landedAs != "sq" {
		t.Fatalf("clean squash: landedAs=%v hint=%q err=%v, want sq", landedAs, hint, err)
	}

	// Two patch-id-identical candidates (apply → revert → re-apply): no bind.
	twins := base
	twins.patchIDs = map[record.SHA]string{"c1": "cum", "sq": "cum", "c2": "pb"}
	if landedAs, hint, _ := AttemptSquash(twins, "f3", "main"); landedAs != nil || hint != "m3: 2 identical candidates" {
		t.Errorf("twin candidates: landedAs=%v hint=%q, want refusal", landedAs, hint)
	}

	// Divergent squash content: no candidate.
	divergent := base
	divergent.patchIDs = map[record.SHA]string{"c1": "pa", "sq": "OTHER", "c2": "pb"}
	if landedAs, hint, _ := AttemptSquash(divergent, "f3", "main"); landedAs != nil || hint != "m3: no squash candidate" {
		t.Errorf("divergent squash: landedAs=%v hint=%q", landedAs, hint)
	}

	// One-line branch: below the floor.
	tiny := base
	tiny.ranges = map[[2]record.SHA]evidence.RangeID{{"mb", "f3"}: {PatchID: "cum", DiffSize: 1}}
	if landedAs, hint, _ := AttemptSquash(tiny, "f3", "main"); landedAs != nil || hint == "" {
		t.Errorf("sub-floor squash: landedAs=%v hint=%q", landedAs, hint)
	}
}
