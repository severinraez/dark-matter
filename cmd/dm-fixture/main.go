// dm-fixture builds the E2E fixture repository set from a declarative
// manifest (design.md §11.2, architecture.md §10).
//
// Exec-only black box: it drives the built dm binary over its public
// stdin interface plus plain git, and imports nothing from internal/ — a
// review rule, since Go cannot enforce it within one module. The
// determinism overrides it sets (DM_CLOCK, DM_ID_SEED, DM_REPLICA_ID) are
// the production binary's documented env overrides, not internal hooks.
//
// Output layout under -out:
//
//	remote.git   bare remote standing in for the forge
//	base         replica 1 — the seeded fixture repo scenarios copy
//	replica2     replica 2 — force-push author + second stat row
//	shallow      git clone --depth 1 (qualified-clone guard)
//	single       git clone --single-branch (qualified-clone guard)
//	handles.json the created handles, in creation order
//
// The manifest is a flat step list; each step acts on the named repo (the
// acting repo defaults to "base" and follows clone/repo steps). Handles
// created by earlier dm steps substitute into later ones as {h1}, {h2}, …
// in creation order — the only templating, so a storage-format change is
// absorbed by rebuilding, never by editing the manifest.
package main

import (
	_ "embed"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

//go:embed manifest.json
var embeddedManifest []byte

type step struct {
	Do      string   `json:"do"`      // file · commit · git · dm · clone · repo
	Repo    string   `json:"repo"`    // optional explicit acting repo
	Path    string   `json:"path"`    // file: path to write
	Content string   `json:"content"` // file: content
	Msg     string   `json:"msg"`     // commit: message
	Args    []string `json:"args"`    // git/dm: argv · clone: extra clone flags
	Stdin   string   `json:"stdin"`   // dm: batch input
	Name    string   `json:"name"`    // clone/repo: repo name
	N       int      `json:"n"`       // clone: replica number (≥ 2)
}

type manifest struct {
	Steps []step `json:"steps"`
}

// repoState is one repo's pinned identity (mirrors the e2e harness
// constants so fixture SHAs and ids are stable and familiar).
type repoState struct {
	dir       string
	replicaID string
	seedBase  int64
	clockOff  int64
}

type builder struct {
	dmBin   string
	out     string
	remote  string
	repos   map[string]*repoState
	acting  string
	handles []string
	steps   int64 // global step counter → pinned git dates
	dmCalls int64 // global dm counter → pinned clocks and id seeds
}

var createdRe = regexp.MustCompile(`(?m)^\+ #(\S+) created`)

const baseClock = int64(1700000000000)

func main() {
	dmBin := flag.String("dm", "", "path to the built dm binary (required)")
	out := flag.String("out", "", "output directory (required, created)")
	manifestPath := flag.String("manifest", "", "manifest file (default: embedded)")
	flag.Parse()
	if *dmBin == "" || *out == "" {
		fmt.Fprintln(os.Stderr, "usage: dm-fixture -dm <dm-binary> -out <dir> [-manifest <file>]")
		os.Exit(2)
	}
	data := embeddedManifest
	if *manifestPath != "" {
		var err error
		if data, err = os.ReadFile(*manifestPath); err != nil {
			fatal(err)
		}
	}
	var m manifest
	if err := json.Unmarshal(data, &m); err != nil {
		fatal(fmt.Errorf("manifest: %w", err))
	}
	b, err := newBuilder(*dmBin, *out)
	if err != nil {
		fatal(err)
	}
	for i, s := range m.Steps {
		if s.Do == "" { // comment-only step
			continue
		}
		if err := b.run(s); err != nil {
			fatal(fmt.Errorf("step %d (%s): %w", i+1, s.Do, err))
		}
	}
	hs, err := json.MarshalIndent(map[string][]string{"handles": b.handles}, "", "  ")
	if err != nil {
		fatal(err)
	}
	if err := os.WriteFile(filepath.Join(*out, "handles.json"), append(hs, '\n'), 0o644); err != nil {
		fatal(err)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "dm-fixture:", err)
	os.Exit(1)
}

// newBuilder creates the bare remote and the base repo wired to it.
func newBuilder(dmBin, out string) (*builder, error) {
	abs, err := filepath.Abs(out)
	if err != nil {
		return nil, err
	}
	b := &builder{dmBin: dmBin, out: abs, remote: filepath.Join(abs, "remote.git"),
		repos: map[string]*repoState{}, acting: "base"}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return nil, err
	}
	if out, err := exec.Command("git", "init", "-q", "--bare", "-b", "main", b.remote).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("init remote: %v\n%s", err, out)
	}
	base := &repoState{dir: filepath.Join(abs, "base"), replicaID: "FIXREPL1", seedBase: 40000}
	b.repos["base"] = base
	if out, err := exec.Command("git", "init", "-q", "-b", "main", base.dir).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("init base: %v\n%s", err, out)
	}
	if err := b.pinConfig(base); err != nil {
		return nil, err
	}
	return b, b.git(base, "remote", "add", "origin", b.remote)
}

func (b *builder) pinConfig(r *repoState) error {
	for _, kv := range [][2]string{
		{"user.name", "dm fixture"},
		{"user.email", "dm@example.invalid"},
		{"commit.gpgsign", "false"},
	} {
		if err := b.git(r, "config", kv[0], kv[1]); err != nil {
			return err
		}
	}
	return nil
}

func (b *builder) run(s step) error {
	r := b.repos[b.acting]
	if s.Repo != "" {
		var ok bool
		if r, ok = b.repos[s.Repo]; !ok {
			return fmt.Errorf("unknown repo %q", s.Repo)
		}
	}
	switch s.Do {
	case "file":
		abs := filepath.Join(r.dir, filepath.FromSlash(s.Path))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			return err
		}
		return os.WriteFile(abs, []byte(s.Content), 0o644)
	case "commit":
		if err := b.git(r, "add", "-A"); err != nil {
			return err
		}
		return b.git(r, "commit", "-q", "-m", s.Msg)
	case "git":
		return b.git(r, b.expandAll(s.Args)...)
	case "dm":
		return b.dm(r, b.expand(s.Stdin), b.expandAll(s.Args)...)
	case "clone":
		return b.clone(s)
	case "repo":
		if _, ok := b.repos[s.Name]; !ok {
			return fmt.Errorf("unknown repo %q", s.Name)
		}
		b.acting = s.Name
		return nil
	default:
		return fmt.Errorf("unknown step %q", s.Do)
	}
}

// git runs one git command with pinned commit dates (§11.2: every SHA and
// patch-id in the fixture must be stable).
func (b *builder) git(r *repoState, args ...string) error {
	b.steps++
	cmd := exec.Command("git", args...)
	cmd.Dir = r.dir
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("GIT_AUTHOR_DATE=%d +0000", 1600000000+b.steps),
		fmt.Sprintf("GIT_COMMITTER_DATE=%d +0000", 1600000000+b.steps),
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return nil
}

// dm runs one dm invocation with a fresh pinned clock instant and id seed
// (identical seeded entropy across invocations would mint colliding
// entry-ids), captures created handles.
func (b *builder) dm(r *repoState, stdin string, args ...string) error {
	b.dmCalls++
	cmd := exec.Command(b.dmBin, args...)
	cmd.Dir = r.dir
	cmd.Stdin = strings.NewReader(stdin)
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("DM_CLOCK=%d", baseClock+r.clockOff+b.dmCalls*1000),
		fmt.Sprintf("DM_ID_SEED=%d", r.seedBase+b.dmCalls),
		"DM_REPLICA_ID="+r.replicaID,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("dm %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	if strings.Contains(string(out), "✗") {
		return fmt.Errorf("dm %s reported a command error:\n%s", strings.Join(args, " "), out)
	}
	for _, m := range createdRe.FindAllStringSubmatch(string(out), -1) {
		b.handles = append(b.handles, m[1])
	}
	return nil
}

// clone creates a further replica from the remote over file:// (so
// history-shaping flags like --depth apply), pinned like the base.
func (b *builder) clone(s step) error {
	if s.N < 2 {
		return fmt.Errorf("clone %q: replica number n must be ≥ 2", s.Name)
	}
	r := &repoState{
		dir:       filepath.Join(b.out, s.Name),
		replicaID: fmt.Sprintf("FIXREPL%d", s.N),
		seedBase:  40000 + int64(s.N-1)*10000,
		clockOff:  int64(s.N-1) * 500,
	}
	args := append([]string{"clone", "-q"}, s.Args...)
	args = append(args, "file://"+b.remote, r.dir)
	if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	b.repos[s.Name] = r
	b.acting = s.Name
	return b.pinConfig(r)
}

// expand substitutes {hN} with the Nth created handle (1-based).
var handleRef = regexp.MustCompile(`\{h(\d+)\}`)

func (b *builder) expand(s string) string {
	return handleRef.ReplaceAllStringFunc(s, func(ref string) string {
		var n int
		fmt.Sscanf(ref, "{h%d}", &n)
		if n < 1 || n > len(b.handles) {
			return ref // left verbatim → the dm step fails loudly
		}
		return b.handles[n-1]
	})
}

func (b *builder) expandAll(args []string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		out[i] = b.expand(a)
	}
	return out
}
