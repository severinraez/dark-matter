package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Inline-built repos for the walking-skeleton scenarios (M2). The seeded
// fixture repository and its builder (design.md §11.2) arrive with later
// milestones; these helpers are what they will be built from.

// baseClock is the pinned DM_CLOCK origin (2023-11-14T22:13:20Z). Each dm
// invocation runs one second later with a distinct id seed: DM_CLOCK alone
// would make two invocations replay identical seeded entropy and mint
// colliding entry-ids.
const baseClock = int64(1700000000000)

// Repo is one scenario's git repository, driving dm with pinned
// determinism overrides (§11.2).
type Repo struct {
	t           *testing.T
	Dir         string
	ReplicaID   string
	seedBase    int64 // per-replica id-seed base: shared seeds would mint colliding ULIDs
	clockOff    int64 // per-replica clock skew (ms), so LWW order across replicas is pinned
	invocations int64
	commits     int
}

// NewRepo creates an empty repository with pinned identity and branch name.
func NewRepo(t *testing.T) *Repo {
	t.Helper()
	r := &Repo{t: t, Dir: t.TempDir(), ReplicaID: "E2EREPL1", seedBase: 40000}
	r.Git("init", "-q", "-b", "main")
	r.pinConfig()
	return r
}

// NewBareRemote creates a bare repository to stand in for the forge remote
// in multi-replica sync scenarios.
func NewBareRemote(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	cmd := exec.Command("git", "init", "-q", "--bare", "-b", "main", dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v\n%s", err, out)
	}
	return dir
}

// CloneRepo clones the remote as replica n (n ≥ 2): distinct pinned
// replica id, id-seed range, and clock skew, so two replicas never mint
// colliding ULIDs and cross-replica LWW outcomes are reproducible.
func CloneRepo(t *testing.T, remote string, n int) *Repo {
	t.Helper()
	r := &Repo{
		t:         t,
		Dir:       t.TempDir(),
		ReplicaID: fmt.Sprintf("E2EREPL%d", n),
		seedBase:  40000 + int64(n-1)*10000,
		clockOff:  int64(n-1) * 500,
	}
	cmd := exec.Command("git", "clone", "-q", remote, r.Dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git clone: %v\n%s", err, out)
	}
	r.pinConfig()
	return r
}

func (r *Repo) pinConfig() {
	r.Git("config", "user.name", "dm fixture")
	r.Git("config", "user.email", "dm@example.invalid")
	r.Git("config", "commit.gpgsign", "false")
}

// Git runs one git command in the repo and returns trimmed stdout.
func (r *Repo) Git(args ...string) string {
	r.t.Helper()
	out, err := gitCommand(r, nil, args...).CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// gitCommand builds a git invocation with the repo's pinned timestamps
// (§11.2) plus any extra environment.
func gitCommand(r *Repo, extra []string, args ...string) *exec.Cmd {
	cmd := exec.Command("git", args...)
	cmd.Dir = r.Dir
	cmd.Env = append(os.Environ(),
		// Pinned commit timestamps so every SHA is reproducible (§11.2).
		fmt.Sprintf("GIT_AUTHOR_DATE=%d +0000", 1600000000+r.commits),
		fmt.Sprintf("GIT_COMMITTER_DATE=%d +0000", 1600000000+r.commits),
	)
	cmd.Env = append(cmd.Env, extra...)
	return cmd
}

// CloneRepoArgs is CloneRepo with extra clone flags, cloning over file://
// so history-shaping flags (--depth, --single-branch) actually apply.
func CloneRepoArgs(t *testing.T, remote string, n int, extra ...string) *Repo {
	t.Helper()
	r := &Repo{
		t:         t,
		Dir:       t.TempDir(),
		ReplicaID: fmt.Sprintf("E2EREPL%d", n),
		seedBase:  40000 + int64(n-1)*10000,
		clockOff:  int64(n-1) * 500,
	}
	args := append([]string{"clone", "-q"}, extra...)
	args = append(args, "file://"+remote, r.Dir)
	cmd := exec.Command("git", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git clone: %v\n%s", err, out)
	}
	r.pinConfig()
	return r
}

// WriteFile writes a working-tree file, creating parent dirs.
func (r *Repo) WriteFile(path, content string) {
	r.t.Helper()
	abs := filepath.Join(r.Dir, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

// Commit stages everything and commits with a pinned timestamp, returning
// the commit SHA.
func (r *Repo) Commit(msg string) string {
	r.t.Helper()
	r.commits++
	r.Git("add", "-A")
	r.Git("commit", "-q", "-m", msg)
	return r.Git("rev-parse", "HEAD")
}

// HashObject returns the blob SHA of a working-tree file.
func (r *Repo) HashObject(path string) string {
	r.t.Helper()
	return r.Git("hash-object", "--", path)
}

// DM runs one dm invocation with pinned determinism env: a fresh clock
// instant and id seed per invocation, one replica id per Repo.
func (r *Repo) DM(stdin string, args ...string) Result {
	r.t.Helper()
	return r.DMEnv(nil, stdin, args...)
}

// DMEnv is DM with extra environment entries (e.g. DM_SKIP_FETCH).
func (r *Repo) DMEnv(extra []string, stdin string, args ...string) Result {
	r.t.Helper()
	r.invocations++
	env := append([]string{
		fmt.Sprintf("DM_CLOCK=%d", baseClock+r.clockOff+r.invocations*1000),
		fmt.Sprintf("DM_ID_SEED=%d", r.seedBase+r.invocations),
		"DM_REPLICA_ID=" + r.ReplicaID,
	}, extra...)
	return RunDM(r.t, r.Dir, stdin, env, args...)
}

// MustDM runs DM and fails the test on a non-zero exit.
func (r *Repo) MustDM(stdin string, args ...string) Result {
	r.t.Helper()
	res := r.DM(stdin, args...)
	if res.Code != 0 {
		r.t.Fatalf("dm %s: exit %d\nstdout: %s\nstderr: %s",
			strings.Join(args, " "), res.Code, res.Stdout, res.Stderr)
	}
	return res
}

// DMDir returns the clone-local state dir.
func (r *Repo) DMDir() string { return filepath.Join(r.Dir, ".git", ".dm") }

// PendingRecordNames lists the pending record filenames — how scenarios
// assert push-clears-pending and fetch-only-keeps-pending (§8.4).
func (r *Repo) PendingRecordNames() []string {
	r.t.Helper()
	entries, err := os.ReadDir(filepath.Join(r.DMDir(), "pending", "records"))
	if err != nil {
		r.t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}
