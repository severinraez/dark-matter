package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"testing"

	"meltcloud.io/dm/internal/core/fold"
	"meltcloud.io/dm/internal/core/record"
	"meltcloud.io/dm/internal/core/resolve"
	"meltcloud.io/dm/internal/gitio"
)

// The Q1 replay rig (design.md §14, §11.4): replay a real repository's
// history against notes seeded at an earlier commit and measure how the
// §9.1 layers hold up under ordinary edit churn. The premise to verify:
// everyday editing rarely drops notes to layer 5 (unresolved) — if it
// routinely does, the model fails regardless of the merge story. The same
// runs are the calibration data for the §9.2/§9.3 bands.
//
// The rig drives core/resolve directly over real gitio evidence — the
// pure-core split exists exactly so band policy can be replayed over
// recorded evidence (architecture.md §1).

// ReplayStats tallies one replay run.
type ReplayStats struct {
	Commits int // replayed checkouts (after the seed commit)
	Files   int // seeded file notes
	Folders int // seeded folder notes
	Samples int // note × checkout resolution attempts

	FileLayer   map[int]int    // §9.1 layer → samples
	FolderLayer map[int]int    // §9.3 rank → samples
	State       map[string]int // resolve.State name → samples
}

// Resolved13Fraction is Q1's headline number: the fraction of file-note
// samples answered by layers 1–3.
func (s ReplayStats) Resolved13Fraction() float64 {
	if s.Samples == 0 {
		return 0
	}
	n := s.FileLayer[1] + s.FileLayer[2] + s.FileLayer[3]
	total := 0
	for _, c := range s.FileLayer {
		total += c
	}
	if total == 0 {
		return 0
	}
	return float64(n) / float64(total)
}

// Format renders the stats for the record.
func (s ReplayStats) Format() string {
	var b strings.Builder
	fmt.Fprintf(&b, "replayed %d checkouts · %d file notes · %d folder notes · %d samples\n",
		s.Commits, s.Files, s.Folders, s.Samples)
	layerKeys := func(m map[int]int) []int {
		var ks []int
		for k := range m {
			ks = append(ks, k)
		}
		sort.Ints(ks)
		return ks
	}
	fmt.Fprintf(&b, "file layers:")
	for _, k := range layerKeys(s.FileLayer) {
		fmt.Fprintf(&b, " L%d=%d", k, s.FileLayer[k])
	}
	fmt.Fprintf(&b, "\nfolder ranks:")
	for _, k := range layerKeys(s.FolderLayer) {
		fmt.Fprintf(&b, " R%d=%d", k, s.FolderLayer[k])
	}
	var states []string
	for k := range s.State {
		states = append(states, k)
	}
	sort.Strings(states)
	fmt.Fprintf(&b, "\nstates:")
	for _, k := range states {
		fmt.Fprintf(&b, " %s=%d", k, s.State[k])
	}
	fmt.Fprintf(&b, "\nlayer-1–3 fraction (file notes): %.3f\n", s.Resolved13Fraction())
	return b.String()
}

// seededNote is one synthetic note: the §3.1 anchors as they would have
// been stamped at the seed commit.
type seededNote struct {
	entry fold.Entry
}

// Replay clones src, seeds notes on every sampled file and folder of the
// commit at seedFrac through first-parent history, then resolves each
// note against every later checkout. maxNotes caps the sample (0 = 200).
func Replay(t *testing.T, src string, seedFrac float64, maxNotes int) ReplayStats {
	t.Helper()
	if maxNotes <= 0 {
		maxNotes = 200
	}
	dir := t.TempDir()
	if out, err := exec.Command("git", "clone", "-q", "--no-hardlinks", src, dir).CombinedOutput(); err != nil {
		t.Fatalf("git clone %s: %v\n%s", src, err, out)
	}
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out))
	}

	commits := strings.Fields(git("rev-list", "--first-parent", "--reverse", "HEAD"))
	if len(commits) < 2 {
		t.Fatalf("repo %s has %d first-parent commits; nothing to replay", src, len(commits))
	}
	seedIdx := int(seedFrac * float64(len(commits)-1))
	seed := record.SHA(commits[seedIdx])

	repo, err := gitio.Open(dir)
	if err != nil {
		t.Fatal(err)
	}

	// Seed: every sampled blob and folder of the seed commit's tree, with
	// the anchors `a` would have stamped there (§3.1).
	entries, err := repo.LsTree(seed)
	if err != nil {
		t.Fatal(err)
	}
	step := 1
	if len(entries) > maxNotes {
		step = (len(entries) + maxNotes - 1) / maxNotes
	}
	var notes []seededNote
	folders := map[record.Path]bool{}
	for i, e := range entries {
		if i%step == 0 {
			notes = append(notes, seededNote{entry: fold.Entry{
				Landed: true,
				Anchor: record.BlobAnchor(e.SHA),
				Path:   record.Path(e.Path),
				Origin: seed,
			}})
		}
		if idx := strings.LastIndexByte(e.Path, '/'); idx >= 0 {
			folders[record.Path(e.Path[:idx+1])] = true
		}
	}
	files := len(notes)
	var folderPaths []record.Path
	for p := range folders {
		folderPaths = append(folderPaths, p)
	}
	sort.Slice(folderPaths, func(i, j int) bool { return folderPaths[i] < folderPaths[j] })
	if len(folderPaths) > maxNotes/4 {
		folderPaths = folderPaths[:maxNotes/4]
	}
	for _, p := range folderPaths {
		notes = append(notes, seededNote{entry: fold.Entry{
			Landed: true,
			Anchor: record.PathAnchor(p),
			Path:   p,
			Origin: seed,
		}})
	}

	stats := ReplayStats{
		Files:       files,
		Folders:     len(notes) - files,
		FileLayer:   map[int]int{},
		FolderLayer: map[int]int{},
		State:       map[string]int{},
	}
	for _, sha := range commits[seedIdx+1:] {
		git("checkout", "-q", sha)
		ev := repo.Evidence(record.SHA(sha))
		stats.Commits++
		for _, n := range notes {
			res, err := resolve.Resolve(ev, ev, n.entry)
			if err != nil {
				t.Fatalf("resolving %s at %s: %v", n.entry.Path, sha, err)
			}
			stats.Samples++
			stats.State[res.State.String()]++
			if n.entry.Anchor.IsPath() {
				stats.FolderLayer[res.Layer]++
			} else {
				stats.FileLayer[res.Layer]++
			}
		}
	}
	return stats
}

// replaySource returns the externally-supplied repo for the full Q1 run,
// or "" (DM_Q1_REPO; DM_Q1_NOTES caps the sample).
func replaySource() string { return os.Getenv("DM_Q1_REPO") }
