package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// M4 exit bar (plan.md): the §11.4 sync scenarios — two-replica
// convergence, non-ff retry, push-fail-fetch-only. The stat-counter merge
// leg lives in stats_test.go.

// newSharedRepo builds a repo with one committed file, wires it to a bare
// remote, and initializes dm.
func newSharedRepo(t *testing.T) (*Repo, string) {
	t.Helper()
	bare := NewBareRemote(t)
	a := NewRepo(t)
	a.WriteFile("api/handler.rb", "class Handler\nend\n")
	a.Commit("c1")
	a.Git("remote", "add", "origin", bare)
	a.Git("push", "-q", "origin", "main")
	a.MustDM("", "init")
	return a, bare
}

func TestSyncTwoReplicaConvergence(t *testing.T) {
	a, bare := newSharedRepo(t)

	// A notes a committed file and a dirty (uncommitted) one, then shares.
	a.WriteFile("scratch.txt", "uncommitted content\n")
	a.MustDM("a:api/handler.rb:c:Note from A\na:scratch.txt:d:Dirty-file note\n")
	if got := a.MustDM("", "sync").Stdout; got != "pushed 2 · received 0\nworklist clean\n" {
		t.Fatalf("A sync output %q", got)
	}
	if names := a.PendingRecordNames(); len(names) != 0 {
		t.Fatalf("A pending not cleared by push: %v", names)
	}

	// B clones; init fetches the store; A's knowledge is visible before B
	// ever syncs (§8.4 — sync is when knowledge is shared, reads see
	// store ∪ pending).
	b := CloneRepo(t, bare, 2)
	if out := b.MustDM("", "init").Stdout; !strings.Contains(out, "store fetched from origin") {
		t.Fatalf("B init output %q", out)
	}
	if dump := b.MustDM("", "dump").Stdout; !strings.Contains(dump, "Note from A") {
		t.Fatalf("B dump before any sync misses A's note:\n%s", dump)
	}
	// The dirty file's anchored blob rides in the store tree — fetchable
	// on B forever, no reflog dependence (§8.1, uncommitted-content case).
	if tree := b.Git("ls-tree", "-r", "--name-only", "refs/dm/store"); !strings.Contains(tree, "blobs/") {
		t.Fatalf("store tree on B carries no anchored blob:\n%s", tree)
	}

	// B contributes and shares; A receives on its next sync. B's health
	// line (§8.4) counts A's dirty-file note: scratch.txt exists only in
	// A's working tree, so on B it is orphaned — and it is not near B's
	// session work, so it is the global remainder, never invisible (§9.6).
	b.MustDM("a:api/handler.rb:c:Note from B\n")
	if got := b.MustDM("", "sync").Stdout; got != "pushed 1 · received 0\n1 elsewhere\n" {
		t.Fatalf("B sync output %q", got)
	}
	if got := a.MustDM("", "sync").Stdout; got != "pushed 0 · received 1\nworklist clean\n" {
		t.Fatalf("A second sync output %q", got)
	}

	// Convergence: identical raw state on both replicas (§11.4 sync +
	// determinism rows — same records, different sync orders).
	dumpA := a.MustDM("", "dump").Stdout
	dumpB := b.MustDM("", "dump").Stdout
	if dumpA != dumpB {
		t.Fatalf("replicas diverged:\nA:\n%s\nB:\n%s", dumpA, dumpB)
	}
	for _, want := range []string{"Note from A", "Note from B", "Dirty-file note"} {
		if !strings.Contains(dumpA, want) {
			t.Fatalf("converged dump misses %q:\n%s", want, dumpA)
		}
	}
}

func TestSyncNonFFRetry(t *testing.T) {
	a, bare := newSharedRepo(t)
	a.MustDM("a:api/handler.rb:c:Note from A\n")
	a.MustDM("", "sync")

	b := CloneRepo(t, bare, 2)
	b.MustDM("", "init")
	b.MustDM("a:api/handler.rb:c:Note from B\n")
	b.MustDM("", "sync") // remote store now ahead of A's mirror

	// A pushes from a stale mirror: DM_SKIP_FETCH suppresses only the
	// initial fetch, so the first push is a genuine non-fast-forward
	// rejection and the retry loop's fetch → union merge → re-push runs.
	a.MustDM("a:api/handler.rb:c:Second note from A\n")
	res := a.DMEnv([]string{"DM_SKIP_FETCH=1"}, "", "sync")
	if res.Code != 0 {
		t.Fatalf("racing sync exit %d\nstdout: %s\nstderr: %s", res.Code, res.Stdout, res.Stderr)
	}
	if res.Stdout != "pushed 1 · received 1\nworklist clean\n" {
		t.Fatalf("racing sync output %q", res.Stdout)
	}
	// Distinguishes the real rejection path from the race never happening
	// (e.g. the skip-fetch staging silently not applying).
	if !strings.Contains(res.Stderr, "retrying (attempt 2)") {
		t.Fatalf("no retry notice — the non-ff path never ran; stderr: %q", res.Stderr)
	}
	if names := a.PendingRecordNames(); len(names) != 0 {
		t.Fatalf("pending survived a landed retry: %v", names)
	}

	// The retry only ever added: after B fetches, both replicas hold all
	// three records identically.
	b.MustDM("", "sync")
	dumpA, dumpB := a.MustDM("", "dump").Stdout, b.MustDM("", "dump").Stdout
	if dumpA != dumpB {
		t.Fatalf("replicas diverged after race:\nA:\n%s\nB:\n%s", dumpA, dumpB)
	}
	for _, want := range []string{"Note from A", "Note from B", "Second note from A"} {
		if !strings.Contains(dumpA, want) {
			t.Fatalf("converged dump misses %q:\n%s", want, dumpA)
		}
	}
}

func TestSyncPushFailFetchOnly(t *testing.T) {
	a, bare := newSharedRepo(t)
	a.MustDM("a:api/handler.rb:c:Note from A\n")
	a.MustDM("", "sync")

	b := CloneRepo(t, bare, 2)
	b.MustDM("", "init")
	b.MustDM("a:api/handler.rb:c:Note from B\n")
	b.MustDM("", "sync")

	// Read-only remote: a declining pre-receive hook, exactly how forges
	// implement denied pushes.
	hook := filepath.Join(bare, "hooks", "pre-receive")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	a.MustDM("a:api/handler.rb:c:Second note from A\n")
	res := a.DM("", "sync")
	if res.Code == 0 {
		t.Fatalf("degraded sync exited 0\nstdout: %s", res.Stdout)
	}
	if res.Stdout != "received 1 · push failed — pending kept (1)\nworklist clean\n" {
		t.Fatalf("degraded sync output %q", res.Stdout)
	}
	if !strings.Contains(res.Stderr, "sync:") {
		t.Fatalf("degraded sync stderr %q, want an error line", res.Stderr)
	}
	// Receive-only behavior, losing nothing (§8.4): the fetch and union
	// merge applied — B's note is visible — while pending stays intact.
	if dump := a.MustDM("", "dump").Stdout; !strings.Contains(dump, "Note from B") {
		t.Fatalf("fetch-only sync did not apply the fetch:\n%s", dump)
	}
	if names := a.PendingRecordNames(); len(names) != 1 {
		t.Fatalf("pending after failed push: %v, want the 1 unshared record", names)
	}

	// Remote writable again: the next successful sync clears pending
	// exactly once and the record lands.
	if err := os.Remove(hook); err != nil {
		t.Fatal(err)
	}
	if got := a.MustDM("", "sync").Stdout; got != "pushed 1 · received 0\nworklist clean\n" {
		t.Fatalf("recovery sync output %q", got)
	}
	if names := a.PendingRecordNames(); len(names) != 0 {
		t.Fatalf("pending not cleared on recovery: %v", names)
	}
	b.MustDM("", "sync")
	if dump := b.MustDM("", "dump").Stdout; !strings.Contains(dump, "Second note from A") {
		t.Fatalf("recovered record never reached B:\n%s", dump)
	}
}

func TestSyncWithoutRemote(t *testing.T) {
	// Never required for local work (§8.4): a remoteless clone folds
	// pending into its local store branch; the ref update is the landing.
	r := NewRepo(t)
	r.WriteFile("api/handler.rb", "class Handler\nend\n")
	r.Commit("c1")
	r.MustDM("", "init")
	r.MustDM("a:api/handler.rb:c:Local-only note\n")
	if got := r.MustDM("", "sync").Stdout; got != "pushed 1 · received 0\nworklist clean\n" {
		t.Fatalf("sync output %q", got)
	}
	if names := r.PendingRecordNames(); len(names) != 0 {
		t.Fatalf("pending not cleared: %v", names)
	}
	// The knowledge moved to the store branch; reads are unchanged.
	if dump := r.MustDM("", "dump").Stdout; !strings.Contains(dump, "Local-only note") {
		t.Fatalf("note lost across local sync:\n%s", dump)
	}
	if got := r.MustDM("", "sync").Stdout; got != "pushed 0 · received 0\nworklist clean\n" {
		t.Fatalf("idempotent sync output %q", got)
	}
	res := r.MustDM("r:api/handler.rb\n")
	if !strings.Contains(res.Stdout, "Local-only note") {
		t.Fatalf("read after sync misses the store-held note:\n%s", res.Stdout)
	}
}
