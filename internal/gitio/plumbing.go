package gitio

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"meltcloud.io/dm/internal/core/record"
)

// Object, ref, tree, and transport plumbing beyond the walking-skeleton
// subset — the store/sync surface (M4). Still policy-free: every method
// answers a question git can answer or performs one git effect.

// ResolveRef resolves a fully-qualified ref to a commit SHA; nil means the
// ref does not exist.
func (r *Repo) ResolveRef(ref string) (*record.SHA, error) {
	out, err := r.git("rev-parse", "--verify", "--quiet", ref+"^{commit}")
	if err != nil {
		var ge *gitError
		if errors.As(err, &ge) && ge.code == 1 {
			return nil, nil
		}
		return nil, err
	}
	sha := record.SHA(out)
	return &sha, nil
}

// UpdateRef points ref at sha, creating it if absent.
func (r *Repo) UpdateRef(ref string, sha record.SHA) error {
	_, err := r.git("update-ref", ref, string(sha))
	return err
}

// TreeOf resolves a commit to its tree SHA.
func (r *Repo) TreeOf(commit record.SHA) (record.SHA, error) {
	out, err := r.git("rev-parse", string(commit)+"^{tree}")
	if err != nil {
		return "", err
	}
	return record.SHA(out), nil
}

// TreeEntry is one blob inside a tree listing: its object name and its
// slash path relative to the tree root.
type TreeEntry struct {
	SHA  record.SHA
	Path string
}

// LsTree lists every blob under a tree-ish, recursively.
func (r *Repo) LsTree(treeish record.SHA) ([]TreeEntry, error) {
	out, err := r.git("ls-tree", "-r", "-z", string(treeish))
	if err != nil {
		return nil, err
	}
	var entries []TreeEntry
	for _, line := range strings.Split(out, "\x00") {
		if line == "" {
			continue
		}
		// <mode> SP <type> SP <sha> TAB <path>
		meta, path, ok := strings.Cut(line, "\t")
		fields := strings.Fields(meta)
		if !ok || len(fields) != 3 {
			return nil, fmt.Errorf("unexpected ls-tree entry %q", line)
		}
		if fields[1] != "blob" {
			continue
		}
		entries = append(entries, TreeEntry{SHA: record.SHA(fields[2]), Path: path})
	}
	return entries, nil
}

// CatBlob returns one blob's content.
func (r *Repo) CatBlob(sha record.SHA) ([]byte, error) {
	out, err := r.gitRaw(nil, nil, "cat-file", "blob", string(sha))
	if err != nil {
		return nil, err
	}
	return out, nil
}

// CatBlobs returns many blobs' contents in one cat-file --batch process.
func (r *Repo) CatBlobs(shas []record.SHA) (map[record.SHA][]byte, error) {
	out := make(map[record.SHA][]byte, len(shas))
	if len(shas) == 0 {
		return out, nil
	}
	cmd := exec.Command("git", "cat-file", "--batch")
	cmd.Dir = r.Top
	var stdin bytes.Buffer
	for _, sha := range shas {
		fmt.Fprintln(&stdin, sha)
	}
	cmd.Stdin = &stdin
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var errOut strings.Builder
	cmd.Stderr = &errOut
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	br := bufio.NewReader(stdout)
	var readErr error
	for _, sha := range shas {
		header, err := br.ReadString('\n')
		if err != nil {
			readErr = fmt.Errorf("cat-file --batch: %w", err)
			break
		}
		fields := strings.Fields(strings.TrimSuffix(header, "\n"))
		// <sha> SP <type> SP <size>, or <sha> SP missing.
		if len(fields) != 3 {
			readErr = fmt.Errorf("cat-file --batch: object %s: %s", sha, strings.TrimSpace(header))
			break
		}
		size, err := strconv.Atoi(fields[2])
		if err != nil {
			readErr = fmt.Errorf("cat-file --batch header %q: %w", strings.TrimSpace(header), err)
			break
		}
		content := make([]byte, size+1) // content + trailing LF
		if _, err := io.ReadFull(br, content); err != nil {
			readErr = fmt.Errorf("cat-file --batch: reading %s: %w", sha, err)
			break
		}
		out[record.SHA(fields[0])] = content[:size]
	}
	if err := cmd.Wait(); readErr == nil && err != nil {
		readErr = fmt.Errorf("git cat-file --batch: %v: %s", err, strings.TrimSpace(errOut.String()))
	}
	if readErr != nil {
		return nil, readErr
	}
	return out, nil
}

// WriteBlob writes content into the object database and returns its name.
func (r *Repo) WriteBlob(content []byte) (record.SHA, error) {
	out, err := r.gitRaw(content, nil, "hash-object", "-w", "--stdin")
	if err != nil {
		return "", err
	}
	return record.SHA(strings.TrimSpace(string(out))), nil
}

// BuildTree writes a tree that is base (nil = empty) plus the given blob
// entries, via a temporary index — one update-index --index-info feed, so
// the cost is two execs regardless of entry count.
func (r *Repo) BuildTree(base *record.SHA, add []TreeEntry) (record.SHA, error) {
	tmp, err := os.CreateTemp("", "dm-index-*")
	if err != nil {
		return "", err
	}
	tmp.Close()
	os.Remove(tmp.Name()) // read-tree wants to create it fresh
	defer os.Remove(tmp.Name())
	env := []string{"GIT_INDEX_FILE=" + tmp.Name()}
	if base != nil {
		if _, err := r.gitRaw(nil, env, "read-tree", string(*base)); err != nil {
			return "", err
		}
	} else {
		if _, err := r.gitRaw(nil, env, "read-tree", "--empty"); err != nil {
			return "", err
		}
	}
	if len(add) > 0 {
		var feed bytes.Buffer
		for _, e := range add {
			fmt.Fprintf(&feed, "100644 blob %s\t%s\n", e.SHA, e.Path)
		}
		if _, err := r.gitRaw(feed.Bytes(), env, "update-index", "--add", "--index-info"); err != nil {
			return "", err
		}
	}
	out, err := r.gitRaw(nil, env, "write-tree")
	if err != nil {
		return "", err
	}
	return record.SHA(strings.TrimSpace(string(out))), nil
}

// dmIdent is the fixed identity on store commits: they are machine-built
// union states, not authored work, and a fixed ident keeps their SHAs a
// pure function of content, parents, and the (injectable) clock (§11.2).
const dmIdent = "dm <dm@dm>"

// CommitTree writes a commit for tree with the fixed dm ident and the
// given timestamp for both author and committer dates.
func (r *Repo) CommitTree(tree record.SHA, parents []record.SHA, msg string, when time.Time) (record.SHA, error) {
	name, email, _ := strings.Cut(strings.TrimSuffix(dmIdent, ">"), " <")
	date := fmt.Sprintf("%d +0000", when.Unix())
	env := []string{
		"GIT_AUTHOR_NAME=" + name, "GIT_AUTHOR_EMAIL=" + email, "GIT_AUTHOR_DATE=" + date,
		"GIT_COMMITTER_NAME=" + name, "GIT_COMMITTER_EMAIL=" + email, "GIT_COMMITTER_DATE=" + date,
	}
	args := []string{"commit-tree", string(tree), "-m", msg}
	for _, p := range parents {
		args = append(args, "-p", string(p))
	}
	out, err := r.gitRaw(nil, env, args...)
	if err != nil {
		return "", err
	}
	return record.SHA(strings.TrimSpace(string(out))), nil
}

// ---- transport ----

// Remotes lists the configured remote names.
func (r *Repo) Remotes() ([]string, error) {
	out, err := r.git("remote")
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, nil
	}
	return strings.Split(out, "\n"), nil
}

// ConfigValues returns every value of a multi-valued config key; a missing
// key is an empty list.
func (r *Repo) ConfigValues(key string) ([]string, error) {
	out, err := r.git("config", "--get-all", key)
	if err != nil {
		var ge *gitError
		if errors.As(err, &ge) && ge.code == 1 {
			return nil, nil
		}
		return nil, err
	}
	return strings.Split(out, "\n"), nil
}

// ConfigAdd appends one value to a multi-valued config key.
func (r *Repo) ConfigAdd(key, value string) error {
	_, err := r.git("config", "--add", key, value)
	return err
}

// LsRemote resolves one exact ref on a remote; nil means the remote does
// not have it.
func (r *Repo) LsRemote(remote, ref string) (*record.SHA, error) {
	out, err := r.git("ls-remote", "--exit-code", remote, ref)
	if err != nil {
		var ge *gitError
		if errors.As(err, &ge) && ge.code == 2 {
			return nil, nil
		}
		return nil, err
	}
	sha, _, _ := strings.Cut(out, "\t")
	s := record.SHA(sha)
	return &s, nil
}

// Fetch runs one fetch over the remote's configured refspecs (code refs
// plus refs/dm/* once dm init has added its refspec).
func (r *Repo) Fetch(remote string) error {
	_, err := r.git("fetch", "--quiet", remote)
	return err
}

// ErrPushRejected reports a per-ref push rejection — the remote ref moved
// (non-fast-forward / fetch first), the sync retry-loop case, as opposed
// to transport or permission failure.
var ErrPushRejected = errors.New("push rejected: remote ref moved")

// Push updates dst on the remote to src. A stale-remote rejection returns
// ErrPushRejected (wrapped); any other failure is returned verbatim.
func (r *Repo) Push(remote string, src record.SHA, dst string) error {
	out, err := r.gitRaw(nil, nil, "push", "--porcelain", remote, string(src)+":"+dst)
	if err == nil {
		return nil
	}
	// Porcelain per-ref status: `!<TAB>src:dst<TAB>[rejected] (reason)`.
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.HasPrefix(line, "!") {
			continue
		}
		if strings.Contains(line, "non-fast-forward") || strings.Contains(line, "fetch first") {
			return fmt.Errorf("%w: %s", ErrPushRejected, strings.TrimSpace(line))
		}
	}
	return err
}

// gitRaw runs git with optional stdin bytes and extra env, returning raw
// stdout (git's own exec plumbing keeps trimmed-string semantics).
func (r *Repo) gitRaw(stdin []byte, env []string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = r.Top
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return out.Bytes(), &gitError{args: args, code: exitErr.ExitCode(), stderr: errOut.String()}
		}
		return nil, fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return out.Bytes(), nil
}
