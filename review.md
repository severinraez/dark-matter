# Review — content-addressable model (design.md)

> **Scope:** adversarial review of the *adopted* architecture (design.md §2–§12).
> Distinct from [archive/review.md](archive/review.md), which reviewed the rejected
> companion-branch model. Overall: the anchor model genuinely deletes the companion
> failure cluster; the storage/CLI core (§3–§8) holds up. The challenges concentrate
> in rule (b)'s inference layer (A), plus a handful of safety and ops gaps with
> cheap fixes (B, C).
>
> **Status:** A1–A3 resolved 2026-07-21 — the rule (b) redesign (lines-land
> model, derived abandoned, qualified-clone guard, precision-first matchers) and
> its test scenarios are folded into design.md (§9.4, §8.2–§8.4, §8.7, §11,
> §14 Q3, §15). The annex below is the working record; §11.4 is now normative.
> C7 (v1 waits on the lock, §8.2), C8 (force-with-lease CAS, §8.7), B5
> (`RP` re-path record, §6.5/§8.3/§9.1), and the B5 follow-up (`k`'s origin
> re-stamp — resolved by per-record rule (b), §8.3) closed 2026-07-21.
> B4, B6, C9, D, E open.

---

## A. The landing-inference layer (rule b) — load-bearing correctness

### A1 — Squash-merge landing detection fails the common case; failure mode is false "abandoned" · **critical**

§2.1 row 9 promises notes land under "any strategy," but the mechanism (§9.4,
§8.4 row 9) is two words: "patch-id / tree containment." Under squash-merge, most
origins are *intermediate* branch commits — their per-commit patch-ids never appear
on main (the squash patch is the cumulative branch diff), and tree containment
fails whenever main advanced or conflicts were resolved. The ordinary flow —
multi-commit branch, forge squash-merge, forge auto-deletes the branch — therefore
resolves to **abandoned**, making the notes invisible everywhere (§2.1 row 13).

It is sticky: abandoned is memoized and rechecking stops; landed-beats-abandoned
only helps if some replica can still *compute* landed, but the branch deletion
destroys the evidence (the origin commit object) — and the writing clone is often
ephemeral (CI/sandbox). Repair is manual `k` per note (verdicts are per-origin, so
O(notes) per event).

This is the one place the design still reconstructs git-history intent after the
fact — the same stance §13 indicts — specced in a clause.

### A2 — Shallow / single-branch clones poison the store with wrong verdicts · **critical**

§8.4 row 13: abandoned = "origin unreachable from every local/remote ref and
unlanded" — a *local-visibility* conclusion promoted to a *global, replicated*
fact. A `--depth 1` or `--single-branch` clone (the default in CI and agent
sandboxes — the tool's primary users) sees almost every origin as unreachable and
cannot compute patch-ids, so its first read session mass-mints abandoned verdicts
that union-sync to the whole team. Nothing in the design guards this.

Minimum fix: verdict minting requires a qualified clone (full history, all-refs
refspec); degraded clones read but never judge.

### A3 — Cherry-pick and backport flows unhandled, in both directions · **high**

Origin-as-lineage is a proxy; any flow that moves content across lines without
merging the origin breaks it:

- **Backport gap:** a fix noted on main, cherry-picked to `release-1.x`, never
  surfaces on the release line — origin never lands there, rule (b) fails forever.
- **Over-broad landing:** once the source branch is deleted, a patch-id match on a
  single cherry-picked origin commit "lands" *every* note stamped with that origin,
  including notes about files that commit never touched.
- **Short-circuit:** while the branch is alive, reachable-from-other-ref resolves
  to pending-elsewhere before any landed check runs, so even the picked commit's
  own notes stay hidden on the target line.

Release-branch shops hit this weekly. At minimum it needs a decision block
accepting it explicitly; the doc is currently silent.

---

## B. Safety & security

### B4 — Anchored blobs can exfiltrate uncommitted secrets to the shared remote · **high**

§8.1: a file note on uncommitted content stages the file's *full bytes* into the
store tree and pushes them on sync. Gitignored files are by definition never in
the odb, so noting one *always* ships its bytes — and gitignored often means
deliberately-not-committed (`.env`, local configs). The design names ops notes for
"where secrets live (never values)" (§3.3) — but the mechanism copies values.

Fix: refuse (or loudly warn on) anchoring ignore-listed paths.

### B5 — `RA` overloading gives `mv` silent blessing side effects · **medium-high**

`⚠disputed` is defined as "FB `!` newer than latest `SU`/`RA`" (§7.3), and `mv`
expands to one `RA` per entry (§6.5). A folder-recursive `mv` therefore re-anchors
every file note underneath to current blobs and HEAD — clearing every ⚠stale *and*
every ⚠disputed in bulk, with nobody reviewing any note. That contradicts §8.3's
own principle that anchors are re-stamped only by a write confirming the note
still matches. `k` blessing is intentional; `mv` blessing is an accident of the
shared record type.

Fix: `mv` preserves the old content anchor (repath only), or the dispute rule
exempts `mv`-minted `RA`s.

**Resolution:** `mv` now expands to a new `RP` (re-path) record (§6.5, §8.3): a
new path hint plus a **scoping origin** — the entry's content anchor is
untouched, and under per-record rule (b) (below) the `RP` folds only on lines
where the move's origin has landed, so it can never mislead a line where the
move didn't happen. `⚠stale` and `⚠disputed` survive moves, sibling-branch
visibility is untouched, and the new pinned resolution layer (§9.1 layer 4)
surfaces below-threshold moves as `⚠stale` at the asserted path until `k`/`u`
confirms. (`RP` initially carried no origin at all; the per-record fold added
the scoping origin — the record scopes *where the move applies*, it never
re-stamps the entry's anchor state.)

**Follow-up resolved — per-record rule (b) (§8.3):** every fold-participating
record (`CR`/`SU`/`RA`/`RP`) carries its own origin, and a checkout folds an
entry from its **landed** records only, LWW among those. `k` can no longer
retract a note from unmerged branches (they fold the prior state until the
confirming line lands), `k`/`u` on later-abandoned lines are contained, the
abandoned-rescue path survives (`k` mints an `RA` with a live origin), and an
unmerged branch reads the superseded revision matching its content instead of
nothing. Considered and rejected along the way: origin = earliest commit
containing the blob (partial fix only — branches older than the drift still
lose the note; "earliest" is ill-defined in a DAG and O(history) to compute;
as a general policy it would erode rule (b)'s deliberate isolation).

### B6 — `rm:path` recursive + absorbing `TB` + no undo = one-line mass destruction · **medium-high**

`rm:.` tombstones every visible entry; `TB` is terminal, beats concurrent `SU`,
replicates on sync, and restoration means re-`a` from `dm dump` archaeology with
new ids and lost links/stats/history. "Nothing is silently destroyed" holds at the
log layer but not at the verb layer — and agents make mistakes at scale.

Fix candidates: confirmation threshold on bulk expansion (N entries → require a
flag); an `undelete` that re-creates from the log before gc.

---

## C. Concurrency & ops

### C7 — Exclusive invocation lock vs. the multi-agent clone · **medium**

§8.2 justifies one exclusive lock with "batches run in milliseconds," but Q2's own
cold path (index rebuild + batched diffs on large repos) is seconds. Reads take
the exclusive lock because reads write stats; `.git/.dm` is shared across all
worktrees; contenders error after a brief block. Parallel agents in worktrees of
one clone — the standard setup — trade lock errors on every cold read.

Fix: per-process stat-delta files (unioned like everything else) let reads take a
shared lock; the CRDT machinery already supports it.

**Resolution (v1):** contenders wait for the lock instead of erroring (§8.2, with
a stderr notice after ~1s naming the holder); the shared-lock + per-process
stat-shard upgrade is deferred (§15).

### C8 — Compaction's push cannot be "the same non-ff retry loop as sync" · **medium (severe if built wrong)**

Sync pushes fast-forwards; an orphan re-commit (§8.7) is *never* an ff. Compaction
needs a forced update, and with plain `--force` the fetch→sweep→push window can
permanently drop a concurrent sync's records — unrecoverably, because
push-clears-pending means the victim no longer holds them. Must be specced as
compare-and-swap (`--force-with-lease=refs/dm/store:<fetched-tip>`).

**Resolved:** §8.7 now specifies the explicit-value force-with-lease CAS and
notes that sync's own parented pushes are plain fast-forwards — sync never
forces.

### C9 — Store growth has no pressure valve · **low-medium**

Reads mutate stats → every session syncs → per-replica stats files rewritten per
sync → commits accumulate on `refs/dm/store` indefinitely; gc is manual-only
(§8.7, §15) and nothing surfaces bloat (the health line counts worklist, not store
size). Minimum: add a size/garbage figure to the sync health line so the manual
trigger has a trigger.

---

## D. Product-level bets

### D10 — Strict rule (b) applied uniformly to line-independent knowledge · **design bet, unmeasured**

A flaky-test note (`d`) or deploy gotcha (`o`) learned on branch B is true
everywhere *now*, yet invisible on main until B lands — and buried if B is
abandoned, though the knowledge outlives the branch. Compounding: §5.3 makes
pending-elsewhere indistinguishable from nonexistent even to `s`, with zero
discovery affordance. A count in the inventory footer ("2 pending on unmerged
lines") leaks no bodies but restores discoverability. The Q1 pilot measures
resolution and hygiene but not this cost — knowledge latency = merge latency is
unmeasured.

### D11 — Layer-2 exact-blob follows treat exact as certain; low-entropy blobs are everywhere · **medium**

§9.1 layer 2: empty files, `__init__.py`, license stubs, generated boilerplate
share SHAs across dozens of paths. Delete the noted file while one identical twin
exists elsewhere → silent wrong follow, *fresh*, no flag — layer 3 has a
uniqueness margin precisely because clusters are the danger; layer 2 has none.

Fix: size/duplication floor — tiny or many-instance blobs demand path-hint
agreement or flag ⚠unconfirmed.

### D12 — Zero provenance is a choice the doc never makes · **medium**

Records deliberately carry no replica/author (§8.3). Fine for merge semantics —
but "which agent keeps writing these wrong notes," feedback weighting (self-`+`
vs. teammate-`+`), and worklist triage all want authorship, and retrofitting it
into an append-only union store is a schema change. If omitting it is deliberate,
it deserves a decision block; right now it is just absent.

---

## E. Nits

- **Fork / read-only-remote topology unaddressed** — CI tokens are often
  read-only; fork PR flows have no path for the store branch; ephemeral agents
  that cannot push lose their session's knowledge with the container.
- **`s` has no literal escape** — terms containing `|` or `+` (`operator+`, regex
  snippets) are unsearchable (§5.5).
- **§4.1 vs §8.3 escaping inconsistency** — §4.1 says "the only escaped character
  is the line separator," but §8.3 requires `\\` for a literal trailing backslash;
  the stdin grammar section should own that rule too.
- **Forward compatibility unstated** — no rule that an old binary must
  preserve-and-ignore unknown record types in the union; one team member
  upgrading first shouldn't require a flag day.

---

## Recommendation

Fold in the cheap fixes (A2 guard, B4, B5, C8) before build. Promote A1 to a
third validation gate — **Q3: landing-detection accuracy on real squash-merge
history** — since Q1 as written measures rule (a) churn but never measures
rule (b) misclassification.

---

## Annex — test scenarios for the rule (b) redesign

Integration-test style per §11.1 (real git repo, dm over stdin, assertions on
stdout + `dm dump`), destined for §11.4 when the proposal lands. They **replace**
the current §11.4 "Verdicts" line (landed-beats-abandoned and wrong-verdict
repair are obsolete under the redesign). Notation: `main` at M0, `feature` =
F1→F3, notes N1 (origin F1), N2 (origin F2 — intermediate), squash commit S.
Every minted `VD` is asserted **with its matcher id** via `dm dump`.

### m1 — local reflog succession

- **m1-rebase-clean** — notes N1/N2 on feature; `git rebase main` (clean); next
  read: N1+N2 visible on rebased branch; dump shows `VD F1 landed F3' m1`,
  `VD F2 landed F3' m1` in *pending* (minted at read, before any sync).
- **m1-rebase-conflict** — rebase with conflict resolution changing noted file G;
  m2/m3 would fail (patch-ids differ) — m1 still binds; G's note surfaces
  `⚠stale` via rule (a) (§2.1 row 8 composition).
- **m1-amend** — commit F1, note, `git commit --amend`; next read binds
  `VD F1 landed F1' m1`; note visible.
- **m1-reset-no-bind** *(FP guard)* — note at F3; `git reset --hard main`
  (reflog action `reset:`): **no** VD; F3 unreachable → derived abandoned on
  worklist.
- **m1-branch-reuse-no-bind** *(FP guard)* — `checkout -B feature <unrelated>`:
  action filter rejects; no binding.
- **m1-drop-accepted-fp** *(accepted FP, pinned)* — interactive rebase drops F2;
  segment still binds; N2 (on an *unchanged* file) lands with the line; a
  sibling note on the dropped *content* stays invisible via rule (a).

### m1r + m2 — rewrites performed elsewhere

- **m2-remote-rebase-clean** — replica B rebases cleanly + force-pushes; A
  fetches (forced-update = m1r candidate only); A's read pairs the **full
  segment in order** → per-origin VDs stamped `m2`.
- **m1r-alone-no-bind** *(FP guard)* — forced-update to an unrelated new
  occupant (branch reused from scratch): no pairing → no bind; old origins →
  derived abandoned.
- **m2-tip-only-no-bind** *(FP guard)* — new occupant's tip patch-id matches old
  tip (identical trailing "fix lint" commit) but earlier commits don't pair →
  no bind. Pins the full-segment, in-order requirement.
- **m2-conflict-heals** *(FN + self-heal)* — B's rebase had conflicts; A derives
  abandoned (worklisted, **no record** — dump asserts); B's next read mints m1
  VDs, B syncs, A syncs → A's next read: landed, worklist entry gone.
- **m2-floor-no-bind** *(FP guard)* — single-commit trivial segment under branch
  reuse with an identical commit: total bound diff below the min-diff floor →
  no bind.

### m3 — cumulative squash

- **m3-squash-multi-commit** *(the A1 flagship)* — N2's origin F2 is
  intermediate; simulate forge: `git merge --squash feature` → S on main,
  remote branch ref deleted, author keeps local `feature`; **before** author
  syncs: teammate reads show nothing (pins the eventual-consistency window);
  author `dm sync` → mint pass binds F3→S, bulk-mints `VD F1 landed S m3` +
  `VD F2 landed S m3`, pushes them in the same sync; teammate syncs → N1+N2
  visible on main.
- **m3-update-branch-flow** — merge main into feature (T2), then squash;
  binding at T2; segment enumeration excludes main-side commits (no VDs for
  main-origin notes).
- **m3-conflicted-squash-FN** — squash content diverges (conflict resolution):
  no bind; `dm worklist` groups the line (tip · branch hint · note count);
  resolved by m5 below.
- **m3-two-candidates-no-bind** *(FP guard)* — apply → revert → re-apply on
  main: two patch-id-identical candidates → no auto-bind, worklist.
- **m3-floor-FN** — one-line branch squashed: below floor → no auto-bind,
  worklist with hint.
- **m3-duplicate-work-accepted-fp** *(accepted FP, pinned)* — an independently
  authored, byte-identical cumulative diff landed on main; the dead PR branch
  binds to it; its notes land.

### m5 — manual disposition & override

- **m5-disposition** — from *m3-conflicted-squash-FN*: one disposition
  (`landed S`) → **all** notes on the line visible; VD stamped `m5`.
- **m5-unlanded-override** — a wrong landed binding is voided by an `unlanded`
  record with larger rec-id (injected clock); the note falls back to
  reachability classification.
- **vd-lww-determinism** — two replicas mint conflicting VDs for one origin;
  both sync orders fold to the same winner (largest rec-id).

### Qualified-clone guard

- **shallow-no-mint** — `clone --depth 1`: unresolved origins read as
  *unknown* (hidden, not worklisted); dump: zero VDs minted; the same state on
  a full clone shows worklist abandoned. Pins A2's poisoning fix.
- **single-branch-no-mint** — `--single-branch`: other-branch origins unknown,
  never abandoned; existing landed VDs are still consumed normally.

### Derived abandoned & the unreachability cache

- **abandoned-derived-not-stored** — delete unmerged branch: worklist shows
  abandoned; dump shows **no** record of it.
- **abandoned-resurrect** — recreate the branch ref at the old tip: next read
  flips back to pending-elsewhere, no repair verb involved (pins *why*
  abandoned is derived).
- **cache-ff-safe / cache-nonff-invalidate** — after abandoned classification,
  ordinary commits + ff pulls keep it (cheap path); creating a ref at the
  origin flips it on the next read — the cache must never wrongly persist
  across a non-ff ref appearance.

### Sync mechanics

- **mint-pass-order** — a single post-squash `dm sync` both mints and pushes
  (teammate needs no extra step): pins mint-after-fetch-before-push.
- **push-fail-fetch-only** — read-only remote: sync's fetch + union merge apply
  (teammate notes become visible), pending stays intact, error in footer; the
  next successful sync clears pending exactly once.
- **evidence-destroyed-window** — writer clone gone after record-sync, branch
  ref deleted, never fetched elsewhere: all qualified clones derive abandoned;
  worklist groups by line; m5 repairs in bulk.

### Chains & accepted gaps (A3)

- **vd-chain-transitive** — feature lands on `dev` as S1 (m3); `dev` is later
  rebased onto main (S1→S1', m1 on the performing clone); fold resolves
  F1 → S1 → S1' → visible on main. Requires segment enumeration to include
  **landed-as commits of existing VDs**, not just note origins.
- **cherry-pick-no-overbroad** *(pinned fix)* — pick F2 onto main, delete
  feature: **no** line binding; no F-origin notes appear on main; origins
  classify derived-abandoned.
- **backport-gap** *(accepted FN, pinned)* — fix + note on main, commit picked
  to `release-1.x`: note never visible on the release line.

**Fixture additions** (§11.2): one branch per matcher state — clean-rebased,
conflict-rebased, force-push-rewritten (clean + conflicted), squash-landed
(clean · update-branch · conflicted · sub-floor · twin-candidate), reused-name,
dropped-commit interactive rebase, cherry-picked, and a two-hop landing chain —
plus a shallow and a single-branch clone of the base repo.

**Spec details this annex surfaced** (fold into the design with the proposal):

1. Bulk-mint segment enumeration must include *landed-as commits of existing
   VDs* alongside store ∪ pending origins, or transitive chains break
   (*vd-chain-transitive*).
2. The mint-moment boundary must be normative: m1 at any read; m2/m3 only at
   the sync mint pass / first-read-after-fetch, with failure memos keyed
   `(tip, target-tip)` (*m3-squash-multi-commit* asserts the window).
3. The worklist line-group needs a golden output format (tip · branch hint ·
   note count · matcher hint), or the m3-FN scenarios can't be asserted.
