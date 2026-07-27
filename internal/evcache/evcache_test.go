package evcache

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"meltcloud.io/dm/internal/core/evidence"
	"meltcloud.io/dm/internal/core/record"
	"meltcloud.io/dm/internal/gitio"
	"meltcloud.io/dm/internal/local"
)

type fixture struct {
	t      *testing.T
	dir    string
	repo   *gitio.Repo
	local  *local.Dir
	origin record.SHA
}

// newFixture builds a repo with a staged move-with-edit over one commit —
// enough to make RenamePairs answer something worth memoizing.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_DATE=1600000000 +0000", "GIT_COMMITTER_DATE=1600000000 +0000")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	git("init", "-q", "-b", "main")
	git("config", "user.name", "t")
	git("config", "user.email", "t@example.invalid")
	if err := os.WriteFile(filepath.Join(dir, "a.rb"),
		[]byte("one\ntwo\nthree\nfour\nfive\nsix\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "-A")
	git("commit", "-q", "-m", "base")
	git("mv", "a.rb", "moved.rb")
	if err := os.WriteFile(filepath.Join(dir, "moved.rb"),
		[]byte("one\ntwo\nthree\nfour\nfive\nEDIT\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "-A")
	git("commit", "-q", "-m", "move")

	repo, err := gitio.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	l := local.At(repo.GitDir)
	if _, _, err := l.Init("CACHET1"); err != nil {
		t.Fatal(err)
	}
	origin := record.SHA("")
	{
		cmd := exec.Command("git", "rev-parse", "HEAD~1")
		cmd.Dir = dir
		out, err := cmd.Output()
		if err != nil {
			t.Fatal(err)
		}
		origin = record.SHA(strings.TrimSpace(string(out)))
	}
	return &fixture{t: t, dir: dir, repo: repo, local: l, origin: origin}
}

func (f *fixture) cached() *Cached {
	f.t.Helper()
	head, err := f.repo.Head()
	if err != nil {
		f.t.Fatal(err)
	}
	return New(f.repo.Evidence(head), f.local)
}

func (f *fixture) cacheFiles() []string {
	f.t.Helper()
	entries, err := os.ReadDir(filepath.Join(f.repo.GitDir, ".dm", "cache"))
	if err != nil {
		f.t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// TestMatchMemo pins the §8.2 memo behavior: a clean checkout persists the
// pairs under match-<origin>-<tree>, a second snapshot answers identically
// from the file, and deleting or corrupting the cache is unobservable.
func TestMatchMemo(t *testing.T) {
	f := newFixture(t)

	first, err := f.cached().RenamePairs(f.origin)
	if err != nil || len(first) != 1 {
		t.Fatalf("RenamePairs = %v, %v; want the one move pair", first, err)
	}
	files := f.cacheFiles()
	if len(files) != 1 || !strings.HasPrefix(files[0], "match-"+string(f.origin)+"-") {
		t.Fatalf("cache files %v, want one match-<origin>-<tree> memo", files)
	}

	// Fresh snapshot: served from the memo, byte-identical answer.
	second, err := f.cached().RenamePairs(f.origin)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(first, second); diff != "" {
		t.Errorf("memoized answer differs (-first +second):\n%s", diff)
	}

	// Corrupt payload: rebuilt, never load-bearing.
	memo := filepath.Join(f.repo.GitDir, ".dm", "cache", files[0])
	if err := os.WriteFile(memo, []byte("DMCACHE1\ngarbage"), 0o644); err != nil {
		t.Fatal(err)
	}
	third, err := f.cached().RenamePairs(f.origin)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(first, third); diff != "" {
		t.Errorf("answer after corruption differs (-want +got):\n%s", diff)
	}

	// Stale format header: read as a miss, deleted, rebuilt.
	if err := os.WriteFile(memo, []byte("DMCACHE0\nold"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := f.cached().RenamePairs(f.origin); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(memo); err != nil || !strings.HasPrefix(string(data), "DMCACHE1\n") {
		t.Errorf("memo after format bump = %q, %v; want rebuilt with the current header", data, err)
	}

	// Deleting the whole cache dir is unobservable (§8.2 disposability).
	if err := os.RemoveAll(filepath.Join(f.repo.GitDir, ".dm", "cache")); err != nil {
		t.Fatal(err)
	}
	fourth, err := f.cached().RenamePairs(f.origin)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(first, fourth); diff != "" {
		t.Errorf("answer after cache wipe differs (-want +got):\n%s", diff)
	}
}

// TestDirtyTreeBypassesMemo: the working tree has no stable key to memoize
// under (§8.2), so a dirty checkout recomputes and persists nothing.
func TestDirtyTreeBypassesMemo(t *testing.T) {
	f := newFixture(t)
	if err := os.WriteFile(filepath.Join(f.dir, "dirt.rb"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pairs, err := f.cached().RenamePairs(f.origin)
	if err != nil || len(pairs) != 1 {
		t.Fatalf("RenamePairs = %v, %v; want the move pair", pairs, err)
	}
	if files := f.cacheFiles(); len(files) != 0 {
		t.Errorf("cache files %v, want none for a dirty checkout", files)
	}
}

// TestPairCodecRoundTrip pins the length-prefixed payload codec.
func TestPairCodecRoundTrip(t *testing.T) {
	pairs := []evidence.RenamePair{
		{From: "a b.rb", To: "x/y:z.rb", FromBlob: "11aa", ToBlob: "22bb", Score: 87},
		{From: "plain.rb", To: "moved.rb", FromBlob: "33cc", ToBlob: "44dd", Score: 100},
	}
	got, err := decodePairs(encodePairs(pairs))
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(pairs, got); diff != "" {
		t.Errorf("round-trip mismatch (-want +got):\n%s", diff)
	}
	if _, err := decodePairs([]byte("garbage that is not a payload")); err == nil {
		t.Error("garbage payload must not decode")
	}
	if got, err := decodePairs(encodePairs(nil)); err != nil || len(got) != 0 {
		t.Errorf("empty round-trip = %v, %v", got, err)
	}
}
