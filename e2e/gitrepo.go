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
	invocations int64
	commits     int
}

// NewRepo creates an empty repository with pinned identity and branch name.
func NewRepo(t *testing.T) *Repo {
	t.Helper()
	r := &Repo{t: t, Dir: t.TempDir(), ReplicaID: "E2EREPL1"}
	r.Git("init", "-q", "-b", "main")
	r.Git("config", "user.name", "dm fixture")
	r.Git("config", "user.email", "dm@example.invalid")
	r.Git("config", "commit.gpgsign", "false")
	return r
}

// Git runs one git command in the repo and returns trimmed stdout.
func (r *Repo) Git(args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = r.Dir
	cmd.Env = append(os.Environ(),
		// Pinned commit timestamps so every SHA is reproducible (§11.2).
		fmt.Sprintf("GIT_AUTHOR_DATE=%d +0000", 1600000000+r.commits),
		fmt.Sprintf("GIT_COMMITTER_DATE=%d +0000", 1600000000+r.commits),
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
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
	r.invocations++
	env := []string{
		fmt.Sprintf("DM_CLOCK=%d", baseClock+r.invocations*1000),
		fmt.Sprintf("DM_ID_SEED=%d", 40000+r.invocations),
		"DM_REPLICA_ID=" + r.ReplicaID,
	}
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
