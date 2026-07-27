// Package gitio is the faithful git plumbing wrapper (design.md §12):
// hash-object, ancestry, similarity diff, patch-id, reflog, refs,
// commit-tree/update-ref, fetch/push. Primary Evidence implementor.
//
// gitio interprets nothing: it answers questions git can answer; every
// threshold, band, and guard lives in core. M2 carries the walking-skeleton
// subset — hash-object, rev-parse, ancestry; the similarity/forensics
// surface arrives with M5/M6.
package gitio

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"meltcloud.io/dm/internal/core/record"
)

// Repo is a handle on one git repository: the working tree dm serves and
// the clone-wide git dir that .dm state lives in (design.md §8.2 — the
// common dir, so all worktrees of a clone share one replica/pending).
type Repo struct {
	// Top is the working tree root; every git command runs from here and
	// every record.Path is relative to it.
	Top string
	// GitDir is the common git dir (absolute).
	GitDir string
}

// Open locates the repository containing dir.
func Open(dir string) (*Repo, error) {
	top, err := runGit(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, fmt.Errorf("not inside a git working tree: %w", err)
	}
	common, err := runGit(dir, "rev-parse", "--git-common-dir")
	if err != nil {
		return nil, err
	}
	if !filepath.IsAbs(common) {
		// rev-parse prints the common dir relative to the cwd it ran in.
		common = filepath.Join(dir, common)
	}
	common, err = filepath.Abs(common)
	if err != nil {
		return nil, err
	}
	return &Repo{Top: top, GitDir: common}, nil
}

// Head resolves HEAD to a commit SHA. An unborn HEAD (no commits yet) is an
// error — dm needs an origin to stamp (§2.1 row 1 assumes commits exist).
func (r *Repo) Head() (record.SHA, error) {
	out, err := r.git("rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return "", fmt.Errorf("resolving HEAD (repo has no commits?): %w", err)
	}
	return record.SHA(out), nil
}

// WorkingBlob hashes the working-tree file at path exactly as git would
// check it in (attribute filters apply, so a clean file hashes to its tree
// blob). nil means the path is absent or not a regular file.
func (r *Repo) WorkingBlob(path record.Path) (*record.SHA, error) {
	fi, err := os.Lstat(filepath.Join(r.Top, filepath.FromSlash(string(path))))
	if err != nil || !fi.Mode().IsRegular() {
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		return nil, nil
	}
	out, err := r.git("hash-object", "--", string(path))
	if err != nil {
		return nil, fmt.Errorf("hashing %s: %w", path, err)
	}
	sha := record.SHA(out)
	return &sha, nil
}

// HasObject reports whether the object database already holds sha.
func (r *Repo) HasObject(sha record.SHA) (bool, error) {
	_, err := r.git("cat-file", "-e", string(sha))
	if err == nil {
		return true, nil
	}
	var ge *gitError
	if errors.As(err, &ge) && ge.code == 1 {
		return false, nil
	}
	return false, err
}

// IsAncestor reports whether a is an ancestor of (or equal to) b.
func (r *Repo) IsAncestor(a, b record.SHA) (bool, error) {
	_, err := r.git("merge-base", "--is-ancestor", string(a), string(b))
	if err == nil {
		return true, nil
	}
	var ge *gitError
	if errors.As(err, &ge) && ge.code == 1 {
		return false, nil
	}
	return false, err
}

// ---- exec plumbing ----

// gitError is a git invocation that exited non-zero; the code disambiguates
// boolean plumbing answers (merge-base/cat-file exit 1 = "no").
type gitError struct {
	args   []string
	code   int
	stderr string
}

func (e *gitError) Error() string {
	msg := strings.TrimSpace(e.stderr)
	if msg == "" {
		msg = fmt.Sprintf("exit status %d", e.code)
	}
	return fmt.Sprintf("git %s: %s", strings.Join(e.args, " "), msg)
}

func (r *Repo) git(args ...string) (string, error) {
	return runGit(r.Top, args...)
}

func runGit(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var out, errOut strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return "", &gitError{args: args, code: exitErr.ExitCode(), stderr: errOut.String()}
		}
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return strings.TrimSuffix(out.String(), "\n"), nil
}
