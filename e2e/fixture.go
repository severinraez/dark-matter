package e2e

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
)

// The §11.2 fixture repository: built once per test process by the
// harness-owned cmd/dm-fixture tool (exec-only, A§10) from its declarative
// manifest, then copied per test — copy-per-test replaces worktrees
// because scenarios mutate refs/dm/store and branch refs independently.
// Copying duplicates the pinned replica ids; deliberate and harmless
// (§8.5's caveat, §11.2).

var (
	fixOnce    sync.Once
	fixDir     string
	fixHandles []string
	fixErr     error
)

// fixtureDir builds dm + dm-fixture and assembles the fixture once,
// returning the assembled directory.
func fixtureDir(t *testing.T) string {
	t.Helper()
	dm := DMBinary(t) // ensure the dm build ran (and its buildDir exists)
	fixOnce.Do(func() {
		bin := filepath.Join(buildDir, "dm-fixture")
		cmd := exec.Command("go", "build", "-o", bin, "meltcloud.io/dm/cmd/dm-fixture")
		cmd.Dir = repoRoot()
		if out, err := cmd.CombinedOutput(); err != nil {
			fixErr = errors.New("building dm-fixture: " + err.Error() + "\n" + string(out))
			return
		}
		fixDir, fixErr = os.MkdirTemp("", "dm-fixture-*")
		if fixErr != nil {
			return
		}
		if out, err := exec.Command(bin, "-dm", dm, "-out", fixDir).CombinedOutput(); err != nil {
			fixErr = errors.New("dm-fixture: " + err.Error() + "\n" + string(out))
			return
		}
		data, err := os.ReadFile(filepath.Join(fixDir, "handles.json"))
		if err != nil {
			fixErr = err
			return
		}
		var hs struct {
			Handles []string `json:"handles"`
		}
		if fixErr = json.Unmarshal(data, &hs); fixErr == nil {
			fixHandles = hs.Handles
		}
	})
	if fixErr != nil {
		t.Fatal(fixErr)
	}
	return fixDir
}

// Fixture is one test's private copy of the fixture repository set.
type Fixture struct {
	Base     *Repo // replica 1 — the seeded repo most scenarios drive
	Replica2 *Repo // replica 2 — second stat row, force-push author
	Shallow  *Repo // git clone --depth 1 (qualified-clone guard)
	Single   *Repo // git clone --single-branch (qualified-clone guard)
	Remote   string
	Handles  []string // created handles, manifest order ({h1} = Handles[0])
}

// H returns handle {hN} (1-based, as the manifest references them).
func (f *Fixture) H(n int) string { return f.Handles[n-1] }

// CopyFixture copies the assembled fixture into the test's temp dir and
// re-points every clone's origin at the copied remote. The returned Repos
// continue the builder's pinned identities with disjoint id-seed ranges
// and later clock instants, so continued writes never collide with seeded
// ones.
func CopyFixture(t *testing.T) *Fixture {
	t.Helper()
	src := fixtureDir(t)
	dst := t.TempDir()
	for _, name := range []string{"remote.git", "base", "replica2", "shallow", "single"} {
		if out, err := exec.Command("cp", "-a",
			filepath.Join(src, name), filepath.Join(dst, name)).CombinedOutput(); err != nil {
			t.Fatalf("copying fixture %s: %v\n%s", name, err, out)
		}
	}
	f := &Fixture{Remote: filepath.Join(dst, "remote.git"), Handles: fixHandles}
	mk := func(name string, n int) *Repo {
		r := &Repo{
			t:         t,
			Dir:       filepath.Join(dst, name),
			ReplicaID: replicaName(n),
			seedBase:  300000 + int64(n-1)*10000,
			clockOff:  100000 + int64(n-1)*500,
		}
		r.Git("remote", "set-url", "origin", f.Remote)
		return r
	}
	f.Base = mk("base", 1)
	f.Replica2 = mk("replica2", 2)
	f.Shallow = mk("shallow", 3)
	f.Single = mk("single", 4)
	return f
}

// replicaName mirrors the builder's pinned replica ids.
func replicaName(n int) string {
	return "FIXREPL" + string(rune('0'+n))
}
