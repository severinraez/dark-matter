package evcache

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"meltcloud.io/dm/internal/core/evidence"
	"meltcloud.io/dm/internal/core/record"
	"meltcloud.io/dm/internal/gitio"
	"meltcloud.io/dm/internal/local"
)

// The unreachability cache's contract (§8.2): behavior identical to the
// raw query — deleting the cache must be unobservable — with the dead set
// revalidated delta-scoped, and every resurrection path (ref reappears, ff
// brings the origin in) flipping the answer back.

type unreachRig struct {
	t    *testing.T
	dir  string
	repo *gitio.Repo
	dm   *local.Dir
}

func newUnreachRig(t *testing.T) *unreachRig {
	t.Helper()
	dir := t.TempDir()
	r := &unreachRig{t: t, dir: dir}
	r.git("init", "-q", "-b", "main")
	r.git("config", "user.name", "t")
	r.git("config", "user.email", "t@t")
	r.git("config", "commit.gpgsign", "false")
	var err error
	if r.repo, err = gitio.Open(dir); err != nil {
		t.Fatal(err)
	}
	r.dm = local.At(r.repo.GitDir)
	if _, _, err := r.dm.Init("UNREACH1"); err != nil {
		t.Fatal(err)
	}
	return r
}

func (r *unreachRig) git(args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = r.dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_DATE=1600000000 +0000", "GIT_COMMITTER_DATE=1600000000 +0000")
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func (r *unreachRig) commit(msg string) record.SHA {
	r.t.Helper()
	name := strings.ReplaceAll(msg, " ", "-") + ".txt"
	if err := os.WriteFile(filepath.Join(r.dir, name), []byte(msg+"\n"), 0o644); err != nil {
		r.t.Fatal(err)
	}
	r.git("add", "-A")
	r.git("commit", "-q", "--allow-empty", "-m", msg)
	return record.SHA(r.git("rev-parse", "HEAD"))
}

func (r *unreachRig) cached() *Cached {
	head := record.SHA(r.git("rev-parse", "HEAD"))
	return New(r.repo.Evidence(head), r.dm)
}

func (r *unreachRig) reach(c *Cached, origin record.SHA) []string {
	r.t.Helper()
	res, err := c.ReachableFrom([]record.SHA{origin})
	if err != nil {
		r.t.Fatal(err)
	}
	var names []string
	for _, rt := range res[origin] {
		names = append(names, string(rt.Ref))
	}
	return names
}

func TestUnreachCacheResurrectionPaths(t *testing.T) {
	r := newUnreachRig(t)
	r.commit("base")
	r.git("checkout", "-q", "-b", "feature")
	dead := r.commit("on feature")
	r.git("checkout", "-q", "main")

	// Reachable while the ref exists.
	if refs := r.reach(r.cached(), dead); len(refs) != 1 || refs[0] != "feature" {
		t.Fatalf("reachable refs %v, want [feature]", refs)
	}

	// Branch deleted: unreachable, and the dead set caches it.
	r.git("branch", "-q", "-D", "feature")
	if refs := r.reach(r.cached(), dead); len(refs) != 0 {
		t.Fatalf("after delete: refs %v, want none", refs)
	}
	if _, deadSet, ok := r.cached().readUnreach(); !ok || !deadSet[dead] {
		t.Fatal("dead origin not cached")
	}

	// An unrelated ff (new commits on main) keeps the cached answer after
	// its delta scan — and must still answer unreachable.
	r.commit("unrelated main progress")
	if refs := r.reach(r.cached(), dead); len(refs) != 0 {
		t.Fatalf("after unrelated ff: refs %v, want none", refs)
	}
	if _, deadSet, ok := r.cached().readUnreach(); !ok || !deadSet[dead] {
		t.Fatal("unrelated ff evicted the cached dead origin")
	}

	// The ref reappearing at the old tip flips it back with no repair.
	r.git("branch", "-q", "feature", string(dead))
	if refs := r.reach(r.cached(), dead); len(refs) != 1 || refs[0] != "feature" {
		t.Fatalf("after resurrection: refs %v, want [feature]", refs)
	}

	// An ff whose delta merges the dead line in flips it likewise.
	r.git("branch", "-q", "-D", "feature")
	if refs := r.reach(r.cached(), dead); len(refs) != 0 {
		t.Fatalf("re-deleted: refs %v, want none", refs)
	}
	r.git("merge", "-q", "--no-ff", "-m", "land it", string(dead))
	if refs := r.reach(r.cached(), dead); len(refs) != 1 || refs[0] != "main" {
		t.Fatalf("after merging the dead line: refs %v, want [main]", refs)
	}
}

func TestUnreachCacheDisposable(t *testing.T) {
	r := newUnreachRig(t)
	r.commit("base")
	r.git("checkout", "-q", "-b", "gone")
	dead := r.commit("doomed")
	r.git("checkout", "-q", "main")
	r.git("branch", "-q", "-D", "gone")

	if refs := r.reach(r.cached(), dead); len(refs) != 0 {
		t.Fatalf("refs %v, want none", refs)
	}
	// Deleting the cache directory must be unobservable (§8.2).
	if err := os.RemoveAll(filepath.Join(r.repo.GitDir, ".dm", "cache")); err != nil {
		t.Fatal(err)
	}
	if refs := r.reach(r.cached(), dead); len(refs) != 0 {
		t.Fatalf("after cache wipe: refs %v, want none", refs)
	}
}

func TestUnreachStateCodecRoundTrip(t *testing.T) {
	refMap := map[evidence.Ref]record.SHA{
		"refs/heads/main":       "aaaa",
		"refs/remotes/origin/x": "bbbb",
	}
	in := make(map[record.SHA]bool)
	for i := 0; i < 3; i++ {
		in[record.SHA(fmt.Sprintf("c%dc%d", i, i))] = true
	}
	gotRefs, gotDead, err := decodeUnreach(encodeUnreach(refMap, in))
	if err != nil {
		t.Fatal(err)
	}
	if len(gotRefs) != len(refMap) || len(gotDead) != len(in) {
		t.Fatalf("round trip lost entries: %v %v", gotRefs, gotDead)
	}
	for k, v := range refMap {
		if gotRefs[k] != v {
			t.Errorf("ref %s: got %s want %s", k, gotRefs[k], v)
		}
	}
	for sha := range in {
		if !gotDead[sha] {
			t.Errorf("dead %s lost", sha)
		}
	}
}
