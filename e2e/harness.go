// Package e2e is the end-to-end harness (design.md §11.1): tests set up
// real git repos, drive the built dm binary over stdin, and assert on
// stdout and store state. Go-native helpers over a txtar DSL — the
// scenarios are dominated by git choreography that wants real functions.
//
// The harness grows with the milestones: M0 builds the binary and drives
// it; the fixture repo, goldens, and git helpers arrive with M2+.
package e2e

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

var (
	buildOnce sync.Once
	buildDir  string
	buildErr  error
)

// DMBinary builds cmd/dm once per test process and returns the binary path.
func DMBinary(t *testing.T) string {
	t.Helper()
	buildOnce.Do(func() {
		buildDir, buildErr = os.MkdirTemp("", "dm-e2e-*")
		if buildErr != nil {
			return
		}
		bin := filepath.Join(buildDir, "dm")
		cmd := exec.Command("go", "build", "-o", bin, "meltcloud.io/dm/cmd/dm")
		cmd.Dir = repoRoot()
		if out, err := cmd.CombinedOutput(); err != nil {
			buildErr = errors.New("building dm: " + err.Error() + "\n" + string(out))
		}
	})
	if buildErr != nil {
		t.Fatal(buildErr)
	}
	return filepath.Join(buildDir, "dm")
}

// repoRoot returns the module root (the e2e package's parent).
func repoRoot() string {
	wd, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	return filepath.Dir(wd)
}

// Result is one dm invocation's observable outcome.
type Result struct {
	Stdout string
	Stderr string
	Code   int
}

// RunDM runs dm in dir with the given stdin and env overrides appended to
// the inherited environment (use the app.Env* determinism vars to pin
// clocks and ids).
func RunDM(t *testing.T, dir, stdin string, env []string, args ...string) Result {
	t.Helper()
	cmd := exec.Command(DMBinary(t), args...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(stdin)
	cmd.Env = append(os.Environ(), env...)
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	err := cmd.Run()
	code := 0
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("running dm: %v", err)
		}
		code = exitErr.ExitCode()
	}
	return Result{Stdout: out.String(), Stderr: errOut.String(), Code: code}
}
