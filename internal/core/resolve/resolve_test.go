package resolve

import (
	"errors"
	"sort"
	"testing"

	"github.com/google/go-cmp/cmp"

	"meltcloud.io/dm/internal/core/evidence"
	"meltcloud.io/dm/internal/core/fold"
	"meltcloud.io/dm/internal/core/record"
)

// Fakes for the Tree and Match roles — exactly the two the calibration
// rigs fake (architecture.md §5); resolution never touches the forensics
// half.

type fakeTree struct {
	manifest  map[record.Path]record.SHA
	trees     map[string]*evidence.TreeFP  // "<commit>:<path>" → fingerprint
	treePaths map[record.SHA][]record.Path // tree SHA → checkout folders
}

func (f *fakeTree) WorkingBlob(p record.Path) (*record.SHA, error) {
	if sha, ok := f.manifest[p]; ok {
		return &sha, nil
	}
	return nil, nil
}

func (f *fakeTree) PathsOf(blob record.SHA) ([]record.Path, error) {
	var out []record.Path
	for p, sha := range f.manifest {
		if sha == blob {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

func (f *fakeTree) PathsUnder(folder record.Path) ([]record.Path, error) {
	var out []record.Path
	for p := range f.manifest {
		if p.Under(folder) {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

func (f *fakeTree) TreeAt(commit record.SHA, path record.Path) (*evidence.TreeFP, error) {
	return f.trees[string(commit)+":"+string(path)], nil
}

func (f *fakeTree) PathsOfTree(tree record.SHA) ([]record.Path, error) {
	return f.treePaths[tree], nil
}

type fakeMatch struct {
	pairs  map[record.SHA][]evidence.RenamePair
	scores map[record.SHA]map[record.Path]int
}

func (f *fakeMatch) RenamePairs(origin record.SHA) ([]evidence.RenamePair, error) {
	return f.pairs[origin], nil
}

func (f *fakeMatch) Score(blob record.SHA, candidates []record.Path) (map[record.Path]int, error) {
	out := map[record.Path]int{}
	for _, c := range candidates {
		if s, ok := f.scores[blob][c]; ok {
			out[c] = s
		}
	}
	return out, nil
}

var errForensics = errors.New("forensics are not resolution's business")

func (f *fakeMatch) ReflogEntries() ([]evidence.ReflogEntry, error)  { return nil, errForensics }
func (f *fakeMatch) MergeBase(a, b record.SHA) (*record.SHA, error)  { return nil, errForensics }
func (f *fakeMatch) Segment(a, b record.SHA) ([]record.SHA, error)   { return nil, errForensics }
func (f *fakeMatch) PatchID(c record.SHA) (*evidence.PatchID, error) { return nil, errForensics }
func (f *fakeMatch) RangePatchID(a, b record.SHA) (*evidence.RangeID, error) {
	return nil, errForensics
}

const (
	anchor = record.SHA("aaaa11")
	origin = record.SHA("00ff00")
)

func fileEntry(path record.Path) fold.Entry {
	return fold.Entry{Landed: true, Anchor: record.BlobAnchor(anchor), Path: path, Origin: origin}
}

func folderEntry(path record.Path) fold.Entry {
	return fold.Entry{Landed: true, Anchor: record.PathAnchor(path), Path: path, Origin: origin}
}

// ---- file layers (§9.1) ----

func TestFileExactLayer1(t *testing.T) {
	tr := &fakeTree{manifest: map[record.Path]record.SHA{"a.rb": anchor}}
	got, err := Resolve(tr, &fakeMatch{}, fileEntry("a.rb"))
	if err != nil {
		t.Fatal(err)
	}
	want := Resolution{State: Fresh, Path: "a.rb", Layer: 1, Score: 100}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("resolution mismatch (-want +got):\n%s", diff)
	}
}

func TestFileMovedLayer2(t *testing.T) {
	tr := &fakeTree{manifest: map[record.Path]record.SHA{"lib/a.rb": anchor}}
	got, err := Resolve(tr, &fakeMatch{}, fileEntry("a.rb"))
	if err != nil {
		t.Fatal(err)
	}
	if got.State != Fresh || got.Path != "lib/a.rb" || got.Layer != 2 {
		t.Errorf("resolved %+v, want fresh at lib/a.rb via layer 2", got)
	}
}

// Multiple exact copies with none at the described path: resolved by
// inference — affinity (same basename) picks, the rest are flagged
// runners (§9.1 ambiguity policy).
func TestFileMovedAmbiguous(t *testing.T) {
	tr := &fakeTree{manifest: map[record.Path]record.SHA{
		"lib/other.rb": anchor,
		"lib/a.rb":     anchor,
	}}
	got, err := Resolve(tr, &fakeMatch{}, fileEntry("a.rb"))
	if err != nil {
		t.Fatal(err)
	}
	if got.State != Unconfirmed || got.Path != "lib/a.rb" || len(got.Runners) != 1 {
		t.Errorf("resolved %+v, want unconfirmed at lib/a.rb with one runner", got)
	}
}

// Row 10: in-place edit inside the accept band → stale at the same path.
func TestFileEditedInPlaceStale(t *testing.T) {
	tr := &fakeTree{manifest: map[record.Path]record.SHA{"a.rb": "bbbb22"}}
	m := &fakeMatch{scores: map[record.SHA]map[record.Path]int{anchor: {"a.rb": 90}}}
	got, err := Resolve(tr, m, fileEntry("a.rb"))
	if err != nil {
		t.Fatal(err)
	}
	if got.State != Stale || got.Path != "a.rb" || got.Layer != 3 || got.Score != 90 {
		t.Errorf("resolved %+v, want stale at a.rb (layer 3, 90)", got)
	}
}

// Move-with-edit (§9.2): the rename pair supplies the candidate, the band
// accepts, the note follows flagged stale.
func TestFileMoveWithEditStale(t *testing.T) {
	tr := &fakeTree{manifest: map[record.Path]record.SHA{"svc/a.rb": "bbbb22"}}
	m := &fakeMatch{
		pairs: map[record.SHA][]evidence.RenamePair{origin: {
			{From: "a.rb", To: "svc/a.rb", FromBlob: anchor, ToBlob: "bbbb22", Score: 85},
		}},
		scores: map[record.SHA]map[record.Path]int{anchor: {"svc/a.rb": 85}},
	}
	got, err := Resolve(tr, m, fileEntry("a.rb"))
	if err != nil {
		t.Fatal(err)
	}
	if got.State != Stale || got.Path != "svc/a.rb" || got.Layer != 3 {
		t.Errorf("resolved %+v, want stale at svc/a.rb", got)
	}
}

// Mid-band score → follow flagged unconfirmed for bulk blessing (§9.2).
func TestFileMidBandUnconfirmed(t *testing.T) {
	tr := &fakeTree{manifest: map[record.Path]record.SHA{"a.rb": "bbbb22"}}
	m := &fakeMatch{scores: map[record.SHA]map[record.Path]int{anchor: {"a.rb": 65}}}
	got, err := Resolve(tr, m, fileEntry("a.rb"))
	if err != nil {
		t.Fatal(err)
	}
	if got.State != Unconfirmed || got.Path != "a.rb" {
		t.Errorf("resolved %+v, want unconfirmed at a.rb", got)
	}
}

// A high score with a close runner-up is never auto-accepted; the margin is
// the load-bearing part, and thin margins tie-break on path-hint affinity
// (§9.2).
func TestFileCloseRunnerUnconfirmedAffinityWins(t *testing.T) {
	tr := &fakeTree{manifest: map[record.Path]record.SHA{
		"fixtures/a.rb": "cccc33",
		"svc/a.rb":      "bbbb22",
	}}
	m := &fakeMatch{
		pairs: map[record.SHA][]evidence.RenamePair{origin: {
			{From: "a.rb", To: "fixtures/a.rb", FromBlob: anchor, ToBlob: "cccc33", Score: 92},
			{From: "a.rb", To: "svc/a.rb", FromBlob: anchor, ToBlob: "bbbb22", Score: 88},
		}},
		// The boilerplate copy scores marginally higher than the true move.
		scores: map[record.SHA]map[record.Path]int{anchor: {"fixtures/a.rb": 92, "svc/a.rb": 88}},
	}
	e := fileEntry("a.rb")
	got, err := Resolve(tr, m, e)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != Unconfirmed {
		t.Errorf("resolved %+v, want unconfirmed (close runner-up)", got)
	}
	// Both candidates share the basename; equal affinity keeps the higher
	// score in front. A hint-affine runner would take the lead instead:
	e2 := fileEntry("svc/a.rb")
	got2, err := Resolve(tr, m, e2)
	if err != nil {
		t.Fatal(err)
	}
	if got2.Path != "svc/a.rb" {
		t.Errorf("resolved %+v, want the hint-affine candidate to lead on a thin margin", got2)
	}
}

// Below the floor: nothing resolves; without a pin the note orphans.
func TestFileBelowFloorOrphaned(t *testing.T) {
	tr := &fakeTree{manifest: map[record.Path]record.SHA{"a.rb": "bbbb22"}}
	m := &fakeMatch{scores: map[record.SHA]map[record.Path]int{anchor: {"a.rb": 30}}}
	got, err := Resolve(tr, m, fileEntry("a.rb"))
	if err != nil {
		t.Fatal(err)
	}
	if got.State != Orphaned || got.Layer != 5 {
		t.Errorf("resolved %+v, want orphaned via layer 5", got)
	}
}

// Layer 4 (§9.1 pinned): a landed RP asserts the path; content below the
// floor still surfaces there, stale — and with folder-note path semantics,
// a deleted destination orphans instead.
func TestFilePinnedLayer4(t *testing.T) {
	m := &fakeMatch{scores: map[record.SHA]map[record.Path]int{anchor: {"pinned.rb": 10}}}
	e := fileEntry("pinned.rb")
	e.PinnedPath = true
	tr := &fakeTree{manifest: map[record.Path]record.SHA{"pinned.rb": "ffff99"}}
	got, err := Resolve(tr, m, e)
	if err != nil {
		t.Fatal(err)
	}
	want := Resolution{State: Stale, Path: "pinned.rb", Layer: 4, Pinned: true}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("resolution mismatch (-want +got):\n%s", diff)
	}
	gone := &fakeTree{manifest: map[record.Path]record.SHA{}}
	got, err = Resolve(gone, m, e)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != Orphaned {
		t.Errorf("resolved %+v, want orphaned when the pinned destination is gone", got)
	}
}

// ---- folder notes (§9.3) ----

const folderTree = record.SHA("dddd44")

// fp registers the origin fingerprint for api/ with the given members.
func fpTrees(members ...record.Path) map[string]*evidence.TreeFP {
	return map[string]*evidence.TreeFP{
		string(origin) + ":api/": {Tree: folderTree, Members: members},
	}
}

func TestFolderExactWins(t *testing.T) {
	// Row 13: the folder moved to svc/ AND a new api/ occupies the old
	// path — exact path wins, fresh, deliberately.
	tr := &fakeTree{
		manifest:  map[record.Path]record.SHA{"api/new.rb": "9999aa", "svc/a.rb": "1111bb"},
		trees:     fpTrees("a.rb"),
		treePaths: map[record.SHA][]record.Path{folderTree: {"svc/"}},
	}
	got, err := Resolve(tr, &fakeMatch{}, folderEntry("api/"))
	if err != nil {
		t.Fatal(err)
	}
	if got.State != Fresh || got.Path != "api/" || got.Layer != 1 {
		t.Errorf("resolved %+v, want fresh at api/ (exact path wins)", got)
	}
}

func TestFolderPureMove(t *testing.T) {
	// Row 4: git mv api/ svc/ with no member edits — the tree SHA
	// reappears at svc/, fresh.
	tr := &fakeTree{
		manifest:  map[record.Path]record.SHA{"svc/a.rb": "1111bb"},
		trees:     fpTrees("a.rb"),
		treePaths: map[record.SHA][]record.Path{folderTree: {"svc/"}},
	}
	got, err := Resolve(tr, &fakeMatch{}, folderEntry("api/"))
	if err != nil {
		t.Fatal(err)
	}
	if got.State != Fresh || got.Path != "svc/" || got.Layer != 2 {
		t.Errorf("resolved %+v, want fresh at svc/ via the tree fingerprint", got)
	}
}

// votePairs builds n rename pairs api/f<i>.rb → <to>f<i>.rb.
func votePairs(n int, to string) []evidence.RenamePair {
	var out []evidence.RenamePair
	for i := 0; i < n; i++ {
		name := record.Path("f" + string(rune('0'+i)) + ".rb")
		out = append(out, evidence.RenamePair{
			From: "api/" + name, To: record.Path(to) + name, Score: 80,
		})
	}
	return out
}

func voteMembers(n int) []record.Path {
	var out []record.Path
	for i := 0; i < n; i++ {
		out = append(out, record.Path("f"+string(rune('0'+i))+".rb"))
	}
	return out
}

func manifestUnder(prefix string, n int) map[record.Path]record.SHA {
	out := map[record.Path]record.SHA{}
	for i := 0; i < n; i++ {
		out[record.Path(prefix+"f"+string(rune('0'+i))+".rb")] = record.SHA("1111bb")
	}
	return out
}

func TestFolderVoteUnanimousFresh(t *testing.T) {
	// Row 5 (uncommitted or with edits): every member pairs to svc/,
	// full coverage, target all ours → fresh.
	tr := &fakeTree{manifest: manifestUnder("svc/", 4), trees: fpTrees(voteMembers(4)...)}
	m := &fakeMatch{pairs: map[record.SHA][]evidence.RenamePair{origin: votePairs(4, "svc/")}}
	got, err := Resolve(tr, m, folderEntry("api/"))
	if err != nil {
		t.Fatal(err)
	}
	if got.State != Fresh || got.Path != "svc/" || got.Layer != 3 {
		t.Errorf("resolved %+v, want fresh at svc/ (unanimous, full coverage)", got)
	}
	if v := got.Vote; v == nil || !v.Unanimous || v.CoveragePct != 100 {
		t.Errorf("vote %+v, want unanimous at cov 100", got.Vote)
	}
}

func TestFolderVotePartialUnconfirmed(t *testing.T) {
	// Row 6: 6 of 10 members move to svc/, the rest deleted — follows,
	// flagged (coverage 60, not unanimous-full).
	tr := &fakeTree{manifest: manifestUnder("svc/", 6), trees: fpTrees(voteMembers(10)...)}
	m := &fakeMatch{pairs: map[record.SHA][]evidence.RenamePair{origin: votePairs(6, "svc/")}}
	got, err := Resolve(tr, m, folderEntry("api/"))
	if err != nil {
		t.Fatal(err)
	}
	if got.State != Unconfirmed || got.Path != "svc/" {
		t.Errorf("resolved %+v, want unconfirmed at svc/", got)
	}
	if got.Vote.CoveragePct != 60 {
		t.Errorf("coverage %d, want 60", got.Vote.CoveragePct)
	}
}

func TestFolderAbsorbedUnconfirmed(t *testing.T) {
	// Row 9: members absorbed into a pre-existing lib/ full of foreign
	// content — unanimous full coverage, still flagged.
	manifest := manifestUnder("lib/", 3)
	for i := 0; i < 17; i++ {
		manifest[record.Path("lib/foreign"+string(rune('a'+i))+".rb")] = "2222cc"
	}
	tr := &fakeTree{manifest: manifest, trees: fpTrees(voteMembers(3)...)}
	m := &fakeMatch{pairs: map[record.SHA][]evidence.RenamePair{origin: votePairs(3, "lib/")}}
	got, err := Resolve(tr, m, folderEntry("api/"))
	if err != nil {
		t.Fatal(err)
	}
	if got.State != Unconfirmed || got.Path != "lib/" {
		t.Errorf("resolved %+v, want unconfirmed at lib/ (absorbed)", got)
	}
	if got.Vote.ForeignPct != 85 {
		t.Errorf("foreign %d%%, want 85 (17 of 20)", got.Vote.ForeignPct)
	}
}

func TestFolderSplit(t *testing.T) {
	// Row 8 / §9.3's example: 6/10 to api-core/, 4/10 to api-http/ — a
	// majority without a clear margin is a split, both candidates carried.
	pairs := append(votePairs(6, "api-core/"), evidence.RenamePair{From: "api/g1.rb", To: "api-http/g1.rb"},
		evidence.RenamePair{From: "api/g2.rb", To: "api-http/g2.rb"},
		evidence.RenamePair{From: "api/g3.rb", To: "api-http/g3.rb"},
		evidence.RenamePair{From: "api/g4.rb", To: "api-http/g4.rb"})
	members := append(voteMembers(6), "g1.rb", "g2.rb", "g3.rb", "g4.rb")
	tr := &fakeTree{manifest: manifestUnder("api-core/", 6), trees: fpTrees(members...)}
	m := &fakeMatch{pairs: map[record.SHA][]evidence.RenamePair{origin: pairs}}
	got, err := Resolve(tr, m, folderEntry("api/"))
	if err != nil {
		t.Fatal(err)
	}
	if got.State != Split || got.Path != "" {
		t.Errorf("resolved %+v, want split with no single home", got)
	}
	want := []Candidate{{Path: "api-core/", Score: 6}, {Path: "api-http/", Score: 4}}
	if diff := cmp.Diff(want, got.Vote.Candidates); diff != "" {
		t.Errorf("candidates mismatch (-want +got):\n%s", diff)
	}
	if got.Vote.CoveragePct != 100 || got.Vote.Paired != 10 {
		t.Errorf("vote %+v, want 10/10 paired", got.Vote)
	}
}

func TestFolderScatterWorklistOnly(t *testing.T) {
	// Row 10: members scattered across many folders, none ≥ the candidate
	// share — worklist-only.
	var pairs []evidence.RenamePair
	for i := 0; i < 5; i++ {
		d := string(rune('a' + i))
		pairs = append(pairs, evidence.RenamePair{
			From: record.Path("api/f" + string(rune('0'+i)) + ".rb"),
			To:   record.Path(d + "/f" + string(rune('0'+i)) + ".rb"),
		})
	}
	tr := &fakeTree{manifest: map[record.Path]record.SHA{"a/f0.rb": "1111bb"}, trees: fpTrees(voteMembers(5)...)}
	m := &fakeMatch{pairs: map[record.SHA][]evidence.RenamePair{origin: pairs}}
	got, err := Resolve(tr, m, folderEntry("api/"))
	if err != nil {
		t.Fatal(err)
	}
	if got.State != Scattered {
		t.Errorf("resolved %+v, want scattered", got)
	}
}

func TestFolderDeletedOrphaned(t *testing.T) {
	// Row 11: members deleted, nothing moved — orphaned.
	tr := &fakeTree{manifest: map[record.Path]record.SHA{}, trees: fpTrees(voteMembers(3)...)}
	got, err := Resolve(tr, &fakeMatch{}, folderEntry("api/"))
	if err != nil {
		t.Fatal(err)
	}
	if got.State != Orphaned {
		t.Errorf("resolved %+v, want orphaned", got)
	}
}

// A 2-member folder with one pair and one deletion: 100% of pairs but 50%
// coverage — too thin to auto-follow (§9.3), so it stays ambiguous.
func TestFolderThinCoverageNoAutoFollow(t *testing.T) {
	tr := &fakeTree{manifest: manifestUnder("svc/", 1), trees: fpTrees(voteMembers(2)...)}
	m := &fakeMatch{pairs: map[record.SHA][]evidence.RenamePair{origin: votePairs(1, "svc/")}}
	got, err := Resolve(tr, m, folderEntry("api/"))
	if err != nil {
		t.Fatal(err)
	}
	if got.State != Split || len(got.Vote.Candidates) != 1 {
		t.Errorf("resolved %+v, want the single candidate held as ambiguous", got)
	}
}

// Renamed-within-the-move members still vote for their destination folder
// (the dir-suffix fallback of the prefix mapping).
func TestFolderVoteRenamedMember(t *testing.T) {
	pairs := []evidence.RenamePair{
		{From: "api/a.rb", To: "svc/a.rb"},
		{From: "api/b.rb", To: "svc/renamed.rb"},
	}
	tr := &fakeTree{manifest: manifestUnder("svc/", 2), trees: fpTrees("a.rb", "b.rb")}
	m := &fakeMatch{pairs: map[record.SHA][]evidence.RenamePair{origin: pairs}}
	got, err := Resolve(tr, m, folderEntry("api/"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Path != "svc/" || !got.Vote.Unanimous {
		t.Errorf("resolved %+v, want a unanimous svc/ vote", got)
	}
}
