package e2e

import (
	"regexp"
	"strings"
	"testing"
)

// M8 exit bar (plan.md): the §11.4 compaction block — `dm gc` drops what
// the §8.7 rules say and nothing else; the epoch rule prevents
// resurrection by stale replicas; late pending referencing a purged entry
// lands dangling, is ignored at read, and is swept next pass; TB stays
// dead forever; a landed VD survives while any record carries its origin;
// the concurrent gc + sync race converges; pending survives a lost push
// race.

var doomedEntryRe = regexp.MustCompile(`(?m)^CR \S+ (\S+) .* Doomed note$`)

func TestGCSweepsTombstonedEntryAndNothingElse(t *testing.T) {
	a, _ := newSharedRepo(t)

	// A doomed note on a dirty file (staged anchored blob), linked from a
	// live note, read once (stat row), disputed once (FB) — one of
	// everything a TB suppresses (§8.7).
	a.WriteFile("scratch.txt", "uncommitted content\n")
	handles := createdHandles(t, a.MustDM("a:api/handler.rb:c:Live note\na:scratch.txt:c:Doomed note\nal:$2:$1:context\n").Stdout)
	doomed := handles[1]
	a.MustDM("r:scratch.txt\n")
	a.MustDM("f:#" + doomed + ":!:wrong\n")
	a.MustDM("", "sync")
	a.MustDM("d:#"+doomed+"\n")
	a.MustDM("", "sync")

	pre := a.MustDM("", "dump").Stdout
	m := doomedEntryRe.FindStringSubmatch(pre)
	if m == nil {
		t.Fatalf("doomed CR missing before gc:\n%s", pre)
	}
	doomedEntry := m[1]
	for _, want := range []string{"LN ", "FB ", "stat E2EREPL1 " + doomedEntry} {
		if !strings.Contains(pre, want) {
			t.Fatalf("pre-gc dump misses %q:\n%s", want, pre)
		}
	}

	// The sweep reclaims the CR body, the link, the FB — 3 records — and
	// the staged blob and stat row; the TB line and the live note stay.
	if got := a.MustDM("", "gc").Stdout; got != "compacted: dropped 3 records · 1 blobs · epoch 1\n" {
		t.Fatalf("gc output %q", got)
	}
	post := a.MustDM("", "dump").Stdout
	if !strings.Contains(post, "Live note") || !strings.Contains(post, "TB ") {
		t.Fatalf("gc swept a keeper:\n%s", post)
	}
	for _, gone := range []string{"Doomed note", "LN ", "FB ", "stat E2EREPL1 " + doomedEntry} {
		if strings.Contains(post, gone) {
			t.Fatalf("gc kept %q:\n%s", gone, post)
		}
	}

	// Orphan re-commit mechanics (§8.7): no parents (the history carries no
	// information), epoch marker bumped, the unreferenced blob gone.
	if count := a.Git("rev-list", "--count", "refs/dm/store"); count != "1" {
		t.Fatalf("store history depth %s after gc, want 1 (orphan)", count)
	}
	if epoch := a.Git("cat-file", "-p", "refs/dm/store:epoch"); epoch != "1" {
		t.Fatalf("epoch marker %q, want 1", epoch)
	}
	if tree := a.Git("ls-tree", "-r", "--name-only", "refs/dm/store"); strings.Contains(tree, "blobs/") {
		t.Fatalf("unreferenced anchored blob survived the sweep:\n%s", tree)
	}
	// Reads are unaffected.
	if got := a.MustDM("r:api/handler.rb\n").Stdout; !strings.Contains(got, "Live note") {
		t.Fatalf("read after gc:\n%s", got)
	}
	// A second gc has nothing left to drop; the epoch still bumps.
	if got := a.MustDM("", "gc").Stdout; got != "compacted: dropped 0 records · 0 blobs · epoch 2\n" {
		t.Fatalf("second gc output %q", got)
	}
}

func TestGCShadowedRevisionWindow(t *testing.T) {
	r := NewRepo(t)
	r.WriteFile("f.rb", "content\n")
	r.Commit("c1")
	r.MustDM("", "init")

	// Two revisions age past the 3-month window (§8.7); the third is
	// current. Only the newest survives — the shadowed old bodies drop.
	handle := createdHandle(t, r.MustDM("a:f.rb:c:V one\n").Stdout)
	r.MustDM("u:#" + handle + ":V two\n")
	r.invocations += 8_000_000 // ≈ 92 days of pinned clock (1s per invocation)
	r.MustDM("u:#" + handle + ":V three\n")
	r.MustDM("", "sync")

	if got := r.MustDM("", "gc").Stdout; got != "compacted: dropped 2 records · 0 blobs · epoch 1\n" {
		t.Fatalf("gc output %q", got)
	}
	dump := r.MustDM("", "dump").Stdout
	if strings.Contains(dump, "V one") || strings.Contains(dump, "V two") {
		t.Fatalf("aged shadowed revisions survived:\n%s", dump)
	}
	if !strings.Contains(dump, "V three") {
		t.Fatalf("current revision swept:\n%s", dump)
	}
	// The entry folds from the surviving SU alone — same handle, same body.
	if got := r.MustDM("r:f.rb\n").Stdout; !strings.Contains(got, "#"+handle+" V three") {
		t.Fatalf("read after window sweep:\n%s", got)
	}
}

func TestGCEpochNoResurrectionAndDanglingLateLanding(t *testing.T) {
	a, bare := newSharedRepo(t)
	handles := createdHandles(t, a.MustDM("a:api/handler.rb:c:Live note\na:api/handler.rb:c:Doomed note\n").Stdout)
	doomed := handles[1]
	a.MustDM("", "sync")

	// B holds the pre-compaction tree and a pending SU on the entry A is
	// about to purge.
	b := CloneRepo(t, bare, 2)
	b.MustDM("", "init")
	b.MustDM("u:#" + doomed + ":Late revision from B\n")

	a.MustDM("d:#"+doomed+"\n")
	a.MustDM("", "sync")
	if got := a.MustDM("", "gc").Stdout; got != "compacted: dropped 1 records · 0 blobs · epoch 1\n" {
		t.Fatalf("gc output %q", got)
	}

	// Epoch rule (§8.4): B's sync adopts the newer-epoch tree wholesale and
	// re-contributes only its own unpushed SU — the purged CR it merely
	// received is never re-uploaded. The SU lands dangling on the TB'd
	// entry: present in the raw state, ignored at read.
	b.MustDM("", "sync")
	dump := b.MustDM("", "dump").Stdout
	if strings.Contains(dump, "Doomed note") {
		t.Fatalf("stale replica resurrected a purged record:\n%s", dump)
	}
	if !strings.Contains(dump, "TB ") || !strings.Contains(dump, "Late revision from B") {
		t.Fatalf("TB line or B's dangling SU missing:\n%s", dump)
	}
	read := b.MustDM("r:api/handler.rb\n").Stdout
	if !strings.Contains(read, "Live note") || strings.Contains(read, "Late revision") {
		t.Fatalf("dangling record not ignored at read:\n%s", read)
	}

	// TB stays dead forever; the dangling SU is garbage for the next sweep.
	a.MustDM("", "sync")
	if got := a.MustDM("", "gc").Stdout; got != "compacted: dropped 1 records · 0 blobs · epoch 2\n" {
		t.Fatalf("second gc output %q", got)
	}
	final := a.MustDM("", "dump").Stdout
	if strings.Contains(final, "Late revision from B") {
		t.Fatalf("dangling SU survived the next sweep:\n%s", final)
	}
	if !strings.Contains(final, "TB ") || !strings.Contains(final, "Live note") {
		t.Fatalf("TB or live note lost across sweeps:\n%s", final)
	}
}

func TestGCLandedVerdictSurvivesWhileOriginCarried(t *testing.T) {
	r := NewRepo(t)
	r.WriteFile("f.rb", "v1\n")
	r.Commit("c1")
	r.MustDM("", "init")

	// A note born on a branch, manually disposed as landed (m5) — the
	// divergent squash content forecloses any automatic bind.
	r.Git("checkout", "-q", "-b", "feature")
	r.WriteFile("f.rb", "v2\n")
	origin := r.Commit("feature work")
	handle := createdHandle(t, r.MustDM("a:f.rb:c:Branch note\n").Stdout)
	r.Git("checkout", "-q", "main")
	r.WriteFile("f.rb", "v2 reworded\n")
	squash := r.Commit("squashed rework")
	r.MustDM("vd:" + origin + ":landed:" + squash + "\n")
	r.MustDM("", "sync")

	// Landed verdicts are irreplaceable evidence (§8.7): the VD survives
	// while the note's records carry its origin.
	if got := r.MustDM("", "gc").Stdout; got != "compacted: dropped 0 records · 0 blobs · epoch 1\n" {
		t.Fatalf("gc output %q", got)
	}
	if dump := r.MustDM("", "dump").Stdout; !hasVD(dump, origin, squash, "m5") {
		t.Fatalf("landed VD swept while its origin is carried, got %v", vdFacts(dump))
	}

	// Tombstone the note: nothing carries the origin any more — the VD is
	// droppable on the next sweep (TB lines pin no verdicts).
	r.MustDM("d:#"+handle+"\n")
	r.MustDM("", "sync")
	if got := r.MustDM("", "gc").Stdout; got != "compacted: dropped 2 records · 0 blobs · epoch 2\n" {
		t.Fatalf("second gc output %q", got)
	}
	dump := r.MustDM("", "dump").Stdout
	if facts := vdFacts(dump); len(facts) != 0 {
		t.Fatalf("orphaned verdict survived: %v", facts)
	}
	if !strings.Contains(dump, "TB ") {
		t.Fatalf("TB line lost:\n%s", dump)
	}
}

func TestGCSyncRaceLeaseRetryConverges(t *testing.T) {
	a, bare := newSharedRepo(t)
	a.MustDM("a:api/handler.rb:c:Note from A\n")
	a.MustDM("", "sync")

	b := CloneRepo(t, bare, 2)
	b.MustDM("", "init")
	b.MustDM("a:api/handler.rb:c:Note from B\n")
	b.MustDM("", "sync") // remote store now ahead of A's mirror

	// A compacts from a stale mirror: the lease expects the stale tip, so
	// the CAS push rejects — plain --force here would have erased B's
	// concurrently-landed note (§8.7). The retry refetches, re-sweeps
	// (absorbing B's record), re-pushes.
	res := a.DMEnv([]string{"DM_SKIP_FETCH=1"}, "", "gc")
	if res.Code != 0 {
		t.Fatalf("racing gc exit %d\nstdout: %s\nstderr: %s", res.Code, res.Stdout, res.Stderr)
	}
	if !strings.Contains(res.Stderr, "gc: store moved under the push, retrying (attempt 2)") {
		t.Fatalf("no lease-retry notice — the CAS race never ran; stderr: %q", res.Stderr)
	}
	if res.Stdout != "compacted: dropped 0 records · 0 blobs · epoch 1\n" {
		t.Fatalf("racing gc output %q", res.Stdout)
	}
	dump := a.MustDM("", "dump").Stdout
	for _, want := range []string{"Note from A", "Note from B"} {
		if !strings.Contains(dump, want) {
			t.Fatalf("compaction lost %q in the race:\n%s", want, dump)
		}
	}
	b.MustDM("", "sync")
	if dumpB := b.MustDM("", "dump").Stdout; dumpB != dump {
		t.Fatalf("replicas diverged after gc race:\nA:\n%s\nB:\n%s", dump, dumpB)
	}
}

func TestGCPendingSurvivesLostPushRace(t *testing.T) {
	a, bare := newSharedRepo(t)
	a.MustDM("a:api/handler.rb:c:Note from A\n")
	a.MustDM("", "sync")

	b := CloneRepo(t, bare, 2)
	b.MustDM("", "init")

	// A compacts; B — mirror still pre-compaction — races a sync against
	// the orphan tip. The first push is non-ff; pending clears only on the
	// push that lands, so nothing is lost (§8.4, §11.4).
	a.MustDM("", "gc")
	b.MustDM("a:api/handler.rb:c:Note from B\n")
	res := b.DMEnv([]string{"DM_SKIP_FETCH=1"}, "", "sync")
	if res.Code != 0 {
		t.Fatalf("racing sync exit %d\nstdout: %s\nstderr: %s", res.Code, res.Stdout, res.Stderr)
	}
	if !strings.Contains(res.Stderr, "retrying (attempt 2)") {
		t.Fatalf("no retry notice — the lost-race path never ran; stderr: %q", res.Stderr)
	}
	if res.Stdout != "pushed 1 · received 0\nworklist clean\n" {
		t.Fatalf("racing sync output %q", res.Stdout)
	}
	if names := b.PendingRecordNames(); len(names) != 0 {
		t.Fatalf("pending survived a landed retry: %v", names)
	}
	// B landed inside the compacted epoch — no epoch regression, both
	// records present on both replicas.
	if epoch := b.Git("cat-file", "-p", "refs/dm/store:epoch"); epoch != "1" {
		t.Fatalf("epoch %q after the race, want 1", epoch)
	}
	a.MustDM("", "sync")
	dumpA, dumpB := a.MustDM("", "dump").Stdout, b.MustDM("", "dump").Stdout
	if dumpA != dumpB {
		t.Fatalf("replicas diverged after race:\nA:\n%s\nB:\n%s", dumpA, dumpB)
	}
	for _, want := range []string{"Note from A", "Note from B"} {
		if !strings.Contains(dumpA, want) {
			t.Fatalf("converged dump misses %q:\n%s", want, dumpA)
		}
	}
}
