package gitio

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"meltcloud.io/dm/internal/core/record"
)

// testRepo builds a real repository with pinned identity/timestamps.
type testRepo struct {
	t    *testing.T
	dir  string
	repo *Repo
}

func newTestRepo(t *testing.T) *testRepo {
	t.Helper()
	dir := t.TempDir()
	tr := &testRepo{t: t, dir: dir}
	tr.git("init", "-q", "-b", "main")
	tr.git("config", "user.name", "t")
	tr.git("config", "user.email", "t@example.invalid")
	tr.git("config", "commit.gpgsign", "false")
	return tr
}

func (tr *testRepo) git(args ...string) string {
	tr.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = tr.dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_DATE=1600000000 +0000", "GIT_COMMITTER_DATE=1600000000 +0000")
	out, err := cmd.CombinedOutput()
	if err != nil {
		tr.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func (tr *testRepo) write(path, content string) {
	tr.t.Helper()
	abs := filepath.Join(tr.dir, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		tr.t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		tr.t.Fatal(err)
	}
}

func (tr *testRepo) commit(msg string) record.SHA {
	tr.t.Helper()
	tr.git("add", "-A")
	tr.git("commit", "-q", "-m", msg)
	return record.SHA(tr.git("rev-parse", "HEAD"))
}

func (tr *testRepo) evidence() *Evidence {
	tr.t.Helper()
	if tr.repo == nil {
		r, err := Open(tr.dir)
		if err != nil {
			tr.t.Fatal(err)
		}
		tr.repo = r
	}
	head, err := tr.repo.Head()
	if err != nil {
		tr.t.Fatal(err)
	}
	return tr.repo.Evidence(head)
}

func TestEvidenceManifest(t *testing.T) {
	tr := newTestRepo(t)
	tr.write("a.rb", "alpha\n")
	tr.write("lib/b.rb", "beta\n")
	tr.commit("base")
	// Dirt: an edit, a deletion, and an untracked file.
	tr.write("a.rb", "alpha edited\n")
	tr.write("untracked.rb", "new\n")
	if err := os.Remove(filepath.Join(tr.dir, "lib", "b.rb")); err != nil {
		t.Fatal(err)
	}
	ev := tr.evidence()

	clean, err := ev.Clean()
	if err != nil || clean {
		t.Fatalf("Clean() = %v, %v; want dirty", clean, err)
	}
	editedSHA := record.SHA(tr.git("hash-object", "--", "a.rb"))
	if got, err := ev.WorkingBlob("a.rb"); err != nil || got == nil || *got != editedSHA {
		t.Errorf("WorkingBlob(a.rb) = %v, %v; want the dirty blob %s", got, err, editedSHA)
	}
	if got, err := ev.WorkingBlob("lib/b.rb"); err != nil || got != nil {
		t.Errorf("WorkingBlob(deleted) = %v, %v; want nil", got, err)
	}
	if paths, err := ev.PathsOf(editedSHA); err != nil || len(paths) != 1 || paths[0] != "a.rb" {
		t.Errorf("PathsOf(dirty blob) = %v, %v; want [a.rb]", paths, err)
	}
	untrackedSHA := record.SHA(tr.git("hash-object", "--", "untracked.rb"))
	if paths, err := ev.PathsOf(untrackedSHA); err != nil || len(paths) != 1 || paths[0] != "untracked.rb" {
		t.Errorf("PathsOf(untracked blob) = %v, %v; want [untracked.rb]", paths, err)
	}
	if paths, err := ev.PathsUnder("lib/"); err != nil || len(paths) != 0 {
		t.Errorf("PathsUnder(lib/) = %v, %v; want empty after the deletion", paths, err)
	}
	if paths, err := ev.PathsUnder(record.RootFolder); err != nil || len(paths) != 2 {
		t.Errorf("PathsUnder(./) = %v, %v; want both surviving files", paths, err)
	}
}

func TestEvidenceCleanTree(t *testing.T) {
	tr := newTestRepo(t)
	tr.write("a.rb", "alpha\n")
	tr.commit("base")
	ev := tr.evidence()
	if clean, err := ev.Clean(); err != nil || !clean {
		t.Fatalf("Clean() = %v, %v; want clean", clean, err)
	}
}

func TestEvidenceTreeFingerprint(t *testing.T) {
	tr := newTestRepo(t)
	tr.write("api/a.rb", "alpha\n")
	tr.write("api/v1/c.rb", "gamma\n")
	origin := tr.commit("base")
	tr.git("mv", "api", "svc")
	tr.commit("move")
	ev := tr.evidence()

	fp, err := ev.TreeAt(origin, "api/")
	if err != nil || fp == nil {
		t.Fatalf("TreeAt(origin, api/) = %v, %v; want a fingerprint", fp, err)
	}
	if len(fp.Members) != 2 {
		t.Errorf("members %v, want the two files", fp.Members)
	}
	// The pure move preserves the tree SHA: it reappears at svc/.
	paths, err := ev.PathsOfTree(fp.Tree)
	if err != nil || len(paths) != 1 || paths[0] != "svc/" {
		t.Errorf("PathsOfTree = %v, %v; want [svc/]", paths, err)
	}
	// Absent path and file-not-folder both answer nil.
	if fp, err := ev.TreeAt(origin, "nope/"); err != nil || fp != nil {
		t.Errorf("TreeAt(absent) = %v, %v; want nil", fp, err)
	}
	// A missing origin degrades to nil, never errors (§9.3).
	if fp, err := ev.TreeAt("0123456789abcdef0123456789abcdef01234567", "api/"); err != nil || fp != nil {
		t.Errorf("TreeAt(missing commit) = %v, %v; want nil", fp, err)
	}
	// The root folder fingerprints too.
	if fp, err := ev.TreeAt(origin, record.RootFolder); err != nil || fp == nil {
		t.Errorf("TreeAt(root) = %v, %v; want the root tree", fp, err)
	}
}

func TestEvidenceRenamePairs(t *testing.T) {
	tr := newTestRepo(t)
	tr.write("a.rb", "one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\n")
	origin := tr.commit("base")
	// Move with an edit, staged — the classic §9.2 case.
	tr.git("mv", "a.rb", "moved.rb")
	tr.write("moved.rb", "one\ntwo\nthree\nfour\nfive\nsix\nseven\nEDITED\n")
	tr.git("add", "-A")
	ev := tr.evidence()

	pairs, err := ev.RenamePairs(origin)
	if err != nil || len(pairs) != 1 {
		t.Fatalf("RenamePairs = %v, %v; want one pair", pairs, err)
	}
	p := pairs[0]
	if p.From != "a.rb" || p.To != "moved.rb" || p.Score < 50 || p.Score >= 100 {
		t.Errorf("pair %+v, want a.rb→moved.rb with a partial score", p)
	}
	// A missing origin yields no pairs, no error.
	if pairs, err := ev.RenamePairs("0123456789abcdef0123456789abcdef01234567"); err != nil || pairs != nil {
		t.Errorf("RenamePairs(missing) = %v, %v; want nil", pairs, err)
	}
}

func TestEvidenceScore(t *testing.T) {
	tr := newTestRepo(t)
	content := "one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\nnine\nten\n"
	tr.write("a.rb", content)
	tr.commit("base")
	blob := record.SHA(tr.git("hash-object", "--", "a.rb"))
	tr.write("same.rb", content)
	tr.write("close.rb", strings.Replace(content, "ten\n", "TEN\n", 1))
	tr.write("far.rb", "completely\ndifferent\n")
	ev := tr.evidence()

	scores, err := ev.Score(blob, []record.Path{"same.rb", "close.rb", "far.rb", "absent.rb"})
	if err != nil {
		t.Fatal(err)
	}
	if scores["same.rb"] != 100 {
		t.Errorf("identical content scored %d, want 100", scores["same.rb"])
	}
	if s := scores["close.rb"]; s < 80 || s >= 100 {
		t.Errorf("one-line edit scored %d, want within the accept band", s)
	}
	if s := scores["far.rb"]; s > 10 {
		t.Errorf("unrelated content scored %d, want ~0", s)
	}
	if _, ok := scores["absent.rb"]; ok {
		t.Error("absent candidate must not score")
	}
	// A blob the odb lacks scores nothing without a BlobSource…
	missing := record.SHA(strings.Repeat("ab", 20))
	if scores, err := ev.Score(missing, []record.Path{"same.rb"}); err != nil || len(scores) != 0 {
		t.Errorf("Score(missing blob) = %v, %v; want empty", scores, err)
	}
	// …and scores from the staged bytes with one (§8.1 pending blobs).
	ev2 := tr.evidence()
	ev2.BlobSource = func(sha record.SHA) ([]byte, bool) {
		if sha == missing {
			return []byte(content), true
		}
		return nil, false
	}
	if scores, err := ev2.Score(missing, []record.Path{"same.rb"}); err != nil || scores["same.rb"] != 100 {
		t.Errorf("Score(staged blob) = %v, %v; want 100", scores, err)
	}
}
