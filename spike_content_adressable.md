# Spike — Content-Addressable Notes

> **Status: exploration, not adopted.** This documents the git-workflow discussion of
> 2026-07-14 as a candidate *alternative architecture* to the branch-mirroring model in
> [design.md](design.md). It exists so design work can continue from here. Findings
> referenced as A1/A3/… are in [review.md](review.md).

---

## 0. Behavioral requirements — file notes

Mechanism-free specification of how dm must behave, agreed 2026-07-18. Folder notes
deliberately excluded for now.

**Visibility rule**: a note is visible iff **(a)** the working tree contains the content
it describes **AND (b)** the note's origin line is part of the current checkout's
lineage — written on this line, or on a line that has since merged into it. (b) is a
deliberate isolation choice: a file-specific note may only make sense in the changed
context of its branch, so content presence alone is not sufficient. "On branch B" below
is shorthand for "the checked-out tree has B's content"; branch *names* carry no
visibility semantics.

| # | action in code repo (CR) | behaviour dm |
|---|---|---|
| 1 | repo created, commits exist, then dm initialized | dm ready; no notes; full history usable from here on |
| 2 | note added on file F while on main | note visible whenever the tree contains F as described and the checkout descends from main — right now that means main |
| 3 | branch B from main; note added on file G | on B: G-note **and** F-note visible (B contains main's content and descends from it); on main: F-note only — B-origin notes stay invisible there until B lands, even for files B didn't change |
| 4 | main progresses; note added on file H on main | on main: F+H; on B: F+G — H's content isn't in B's tree (and had B already contained identical content, main's later note would still surface on B only once B rebases onto/merges in that main state) |
| 5 | B rebased onto main (clean) | on B: F+G+H all visible; main unchanged |
| 6 | branch C created; CR still on B | notes written on C are invisible on B, and vice versa — unmerged lines never see each other's notes |
| 7 | note N added on B; CR on B | visible immediately on B, no commit/sync step required first |
| 8 | B rebased onto main, conflict resolution changes G | G's note is **flagged, not hidden**: still surfaced, marked "content has drifted — needs re-confirmation"; all other notes unaffected |
| 9 | B merged into main (any strategy — merge, squash, rebase) | on main: B's notes appear (origin line has now landed), including G's note *still carrying its flag* until someone re-confirms it |
| 10 | noted file edited in working tree, uncommitted | note still visible, flagged stale (same state as row 8) |
| 11 | noted file renamed/moved (with or without edits) | note follows to the new path; flagged stale only if content also changed |
| 12 | noted file deleted on the current line | note orphaned: leaves normal reads, appears on a review worklist; never silently destroyed |
| 13 | branch B deleted *without* merging | B-only notes stop being visible anywhere; they appear on the worklist as "abandoned," distinguishable from "pending on an unmerged branch" |
| 14 | teammate clones/pulls the same state | sees exactly the same notes with the same flags — visibility is a pure function of tree state + lineage, not of who wrote what where |
| 15 | detached HEAD / bisect at an old commit | notes whose content is present *and* whose origin line had landed as of that commit are visible; later notes are not — same rule, no special-casing |

**Note states**: every note is in exactly one of five states per checkout —
**visible** · **stale** (visible, flagged: content drifted, needs re-confirmation) ·
**pending-elsewhere** (content and origin exist, but the origin line hasn't landed
here) · **abandoned** (origin line deleted without landing) · **orphaned** (described
content no longer exists anywhere dm can place it). Stale and orphaned are distinct by
design: drift dm can locate is flagged and stays usable; only notes dm cannot place
leave normal reads for the worklist. Nothing is ever silently destroyed.

**Open point (row 15)**: strict lineage hides notes written *later on main itself* from
old-main checkouts (main-as-of-then hadn't received them) — during bisect one might
*want* later knowledge about unchanged files. Strict reading stands until decided.

---

## 1. Why this spike

The critical review findings against design.md's git mechanics — A1 (orphan-gate data
destruction under tree skew), A3 (same-companion divergence across clones), A4
(alignment/dirt deadlock), A5 (anchor drift silently blessed) — all trace to one
architectural stance: **dm sits outside git, mirroring branch topology and trying to
observe git events after the fact.**

A first patch package was designed on top of the existing model (binding trailer,
recorder hooks, patch-id merge detection — the last already adopted into design.md
§8.5). It works, but it is *stabilizing machinery for an unstable stance*. This spike
asks whether a different stance deletes the problems instead of patching them.

## 2. Prior art

### Metadata-beside-code systems

| System | How it syncs | Transferable lesson |
|---|---|---|
| **git-annex** | One global `git-annex` branch of timestamped log lines; union merge; keyed by *content hash*, not path | The 15-year-proven version of dm's CRDT branch — and it never mirrors code branches. Metadata keyed by blob identity survives renames and branches for free |
| **git-bug** | Each bug = append-only DAG of operation commits under `refs/bugs/*`; ordering from DAG causality + Lamport clocks | Causal ordering instead of wall-clock ULIDs kills the clock-skew caveat; fetch is union-of-DAGs, inherently non-clobbering (A3) |
| **git-appraise** | Review data as JSON in `refs/notes/devtools/*`, merged `cat_sort_uniq` (union) | Union-merged metadata refs push/fetch fine through GitHub — design.md §8.1's "only `refs/heads` is reliable" applies to *PR-able* branches, not metadata refs |
| **Gerrit NoteDb** | All review metadata as append-only meta commits, single-writer server | One folding authority (server/CI) makes the concurrent-merge problem disappear entirely |
| **Fossil SCM** | Wiki/tickets/code are one global set of immutable artifacts; sync = set union; state is a computed view | The purest form of dm's idea: *which notes apply where* is a **query**, not a ref topology |

### Git-observing / wrapping systems

| System | How it stays in sync | Transferable lesson |
|---|---|---|
| **Jujutsu (jj)** | No hooks — every invocation imports git refs, **diffs them against its own last-known snapshot**, reconciles; plus an operation log of its own mutations | **State reconciliation beats event observation**: nothing to install, nothing missed, `--no-verify` can't bypass it |
| **jj working-copy-as-commit** | The working copy is always a commit; "dirty" doesn't exist | dm's content-dirt deadlock (A4) exists only because uncommitted note state exists |
| **GitButler** | Daemon + own `refs/gitbutler/*`, reconciles with real refs | Same pattern, daemon flavor |
| **etckeeper / gitwatch** | Hook/watcher auto-commit | Hooks work but carry the known failure modes: not installed, bypassed, races |
| **`reference-transaction` hook** | Fires on every ref update | If hooks at all: this one subsumes post-checkout/commit/merge/rewrite — still opt-in per clone |

Named and rejected: **submodule/gitlink binding** (atomic code↔notes pinning, survives
squashes — but every PR touches the same gitlink and conflicts with every other PR);
**forge-native storage** (PR comments/Discussions — abandons git-native and offline).

## 3. The two ideas

### Idea 1 — Reconcile, don't observe (from jj)

dm keeps a snapshot of all refs + HEAD in `.dm/ref-snapshot`. **Every invocation starts
by diffing current refs against the snapshot** and deriving what happened since:

- branch advanced → new commits to resolve notes against,
- same branch name, old tip unreachable from new tip → rebase; translate any bindings,
- new merge commit on `main` → mirror/fold context,
- HEAD on a different branch → the "alignment" case, handled with full context instead
  of a refusal.

Properties: idempotent catch-up (works after a week of un-dm'd git activity), zero
installation, immune to bypassed/missing hooks. Hooks demote to optional *freshness*
accelerators, never correctness mechanisms. **This idea is worth adopting regardless of
Idea 2.**

### Idea 2 — Key notes by content, not by branch-mirrored path (from git-annex/Fossil)

Replace (branch, path) as a note's home with:

- **one** metadata branch for the whole repo (no `llm/<B>` companions),
- a file note's primary key = the **blob SHA it describes** (which §7.5's staleness
  anchor already stores), path demoted to a display hint.

Branch visibility becomes **emergent**: a note surfaces where its blob (or a descendant
of it) exists in the checked-out working tree. A note written on `topic/x` about new
code is invisible on `main` until the PR lands — because the blobs aren't there —
and appears on `main` the moment the *tree* lands, regardless of merge strategy
(true/squash/rebase all land the tree). No companions, no `dm checkout`, no fold, no
merge detection, no orphan gate.

## 4. Model sketch

- **Store**: one metadata branch (or `refs/dm/*` DAGs, git-bug-style), append-only
  records, CRDT union merge — the entire §5/§7 record machinery of design.md carries
  over unchanged (CR/SU/TB/RA/FB/LN/UL, rec-id ULIDs, G-counter headers, handles).
- **Keys**: file notes → blob SHA + path hint. Folder/arch notes → hybrid (§6 below).
- **Inner loop**: unchanged — `r:` / `s:` / `a` / `u` / `d` / `k` / `f` / `al` / `dl`.
- **Outer loop collapses to one verb**: `dm sync` = commit store → fetch → CRDT union →
  push (with non-ff retry). Replaces checkout/merge/unmerged/prune/push/fetch.
- **Anchor at write time**: key = `git hash-object` of the working-tree file *at write
  time*. No `@` placeholder, no stamp-at-commit — A5 disappears by construction: the
  note is bound to the exact bytes it described.
- **§8.4's hard gate** becomes an optional hygiene worklist ("notes not resolving in
  this tree"), never destructive: an unresolved note stops *surfacing*; nothing forces
  tombstones.

## 5. Lifecycle comparison — design.md vs this spike

Actors: **human** (developer), **agent** (LLM driving dm), **forge** (hosted button).

| # | Step in code repo | dm step currently in design.md | dm step under reconciliation + content-keyed store |
|---|---|---|---|
| 1 | `git clone` (human) | `dm init` — replica id, empty `llm/main`, worktree | `dm init` — replica id, fetch/create the single metadata branch |
| 2 | `git checkout -b topic/x` from main tip (human/agent) | `dm checkout` — create/bind companion from `llm/main` tip (agent) | **nothing** — notes resolve against the blobs in the checked-out worktree |
| 3 | branch from an old commit — hotfix off `v1.0` | same as #2 → shadow/code skew → orphan storm, forced `rm` (**A1**) | **nothing** — old worktree contains old blobs; era-appropriate notes surface automatically |
| 4 | plain `git checkout other` mid-session with unsaved notes (human) | next dm call refuses on mismatch; `dm commit` ↔ `dm checkout` deadlock (**A4**) | **nothing to desync** — no alignment check; uncommitted store state is branch-independent; reconciliation notes the HEAD move |
| 5 | editing, learning (agent) | inner loop; writes accumulate uncommitted | same verbs, same loop — note keyed to the blob it describes at write time; path = display hint |
| 6 | `git commit` on code branch (agent/human) | nothing; `@` anchors pending until `dm commit` → whole-session drift window (**A5**) | **nothing needed** — anchor *is* the key, stamped from the exact bytes described at write time |
| 7 | `git rebase -i` / `--amend`, incl. dropped commits | nothing fires; staleness gate catches fallout later | **nothing** — notes bind to content, not commits; dropped work's blobs leave the tree, its notes go invisible (not destroyed) |
| 8 | wrap up, `git push`, open PR (agent) | `dm pre-commit` (hard gate) → `dm commit` → `dm push` | `dm sync`; the gate becomes an optional hygiene worklist, never destructive |
| 9 | `git merge topic/x` into main locally | `dm merge llm/topic/x` → `dm commit` → `dm push` → `dm prune` | **nothing** — merged tree carries the blobs; notes surface on `main` immediately |
| 10 | PR merged via hosted button — true/squash/rebase (forge) | `dm unmerged` (ancestry + patch-id detection, §8.5) → `dm merge` each → commit/push/prune | **nothing** — every merge strategy lands the tree, and the tree is all that matters |
| 11 | `git pull` while teammates also write notes | `dm fetch` "moves the companion ref" — clobber/wedge on divergence (**A3**) | `dm sync` — fetch + CRDT union of the one store branch (git-annex-proven) + push retry |
| 12 | teammate force-pushes the shared code branch | invisible; bindings/ancestry silently wrong | **immune** — nothing binds to code commits; reconciliation refreshes its snapshot |
| 13 | `git branch -d topic/x` after landing (human) | `dm prune topic/x` | **nothing** — no companion exists |
| 14 | recreate the *name* `topic/x` for unrelated work | `dm checkout` silently binds the stale old companion | **nothing** — branch names carry no note state |
| 15 | `git bisect` / detached HEAD debugging (agent) | unspecified — alignment check has no branch | **works normally** — blobs in the detached worktree resolve; writes keyed by content as usual |
| 16 | PR from a fork (contributor) | uncovered — companion lives on the fork, never fetched | reduced, not solved: fetch the fork's store branch and union (trivially mergeable, CI-able) — someone must still fetch |

Rows 2–4, 6–7, 9–10, 12–14 collapse to **nothing** — precisely the set where A1, A3,
A4, A5 and the merge-detection machinery lived.

### What the new model pays instead

- **A follow algorithm** (§7): every edit creates a new blob, so surfacing notes for a
  modified file means resolving descendants of the keyed blob. This replaces all the
  ref choreography with one retrieval problem — the load-bearing unknown of the spike.
- **Hybrid keying**: folder/place notes have no blob (§6); a residue stays path-keyed
  with a (much smaller) rename story.
- **Weaker branch privacy**: notes sync on one shared branch before code lands; a
  teammate who has your blobs (pulled your branch) sees your notes. Isolation is
  "no blob, no note" — usually equivalent to companion isolation, not always.
- **No forcing function for hygiene**: design.md's hard gate guaranteed reconciliation;
  here hygiene must come from worklists/ranking/decay — review finding **D1** (deferred
  hygiene loop) becomes *more* important, not less.

## 6. Folder notes (no blob to key on)

Options examined:

- **Hash of the folder path — rejected.** Still a path key, just unreadable; inherits
  every rename problem (`api/` → `svc/` changes the hash exactly as much as the path).
- **Tree SHA — rejected.** Git's real content address for a folder hashes the entire
  subtree recursively: any edit to any file under it changes the tree SHA. A folder
  note keyed this way detaches on virtually every commit. (Blobs anchor well because a
  file's bytes are *locally* stable; trees are *globally* volatile by construction.)
- **Derived home — adopt for member-bearing notes.** An architecture note already
  carries `links[]` to blob-keyed member entries, so it needs no key of its own: its
  home is **computed** — the LCA of wherever its members currently resolve. Rename
  `api/` → `svc/` and the note follows, because its members' blobs now live under
  `svc/`. Location is a query, not a stored fact (the Fossil move).
- **Path key + rename inference — keep for member-less "place" notes** (e.g.
  "everything under `ops/` deploys to k8s" — describes a *place*, not contents).
  A folder rename is trivially visible in a reconciliation tree diff (same blob set,
  new prefix) and is fixed with one appended re-key record — the MV mechanism shrunk to
  a maintenance detail.

**Hybrid**: file notes → blob key · member-bearing arch notes → derived LCA home ·
place notes → path key with inferred renames. Only the last category retains rename
exposure, and it is the smallest.

## 7. Resolving blob drift (edits)

Key realization: **git already stores the lineage** — a path's blob history *is* the
follow chain (`git log --follow`, rename detection included). dm mostly queries it and
patches two gaps.

### When each part happens

| Moment | What happens |
|---|---|
| **Write** (`a`/`u`) | Key = `git hash-object` of the working-tree file now. Immediate; no placeholder; A5 gone by construction |
| **Read** (`r:path`) | Resolution (below). Pure lookup, no lineage writes |
| **Reconciliation** (start of every dm invocation) | Diff refs/worktree since last snapshot. Patches the two gaps: (1) a note keyed to a *dirty* blob never committed as-is → re-key to the nearest committed blob at its path hint (one append record); (2) folder-rename inference for path-keyed notes. Each hop is recorded while it is one small commit range — cheap and high-confidence |
| **Hygiene** (`k`/`u`/`d`) | Agent deliberately re-keys — today's re-anchor. `k` = confirm + re-key to current blob. The RA record and its LWW fold carry over unchanged |

### Read-time resolution order

1. **Exact** — current blob at path equals the key → fresh.
2. **Ancestor** — key appears in the path's blob history (cached git log walk) or in
   dm re-key records → note applies, surfaced `⚠stale`. "Stale" now measurably means
   *N revisions of drift* rather than "bytes differ" — a gradable signal (keep the
   binary flag for v1).
3. **Moved content** — key not in this path's lineage but the blob exists/existed at
   another path → note followed its content through a move or split; surface at the
   new location. The case path-keying can never handle: extract-file refactors carry
   their notes.
4. **Unresolved** — nowhere in tree or history → hygiene worklist. A *visibility*
   fact, never a gate demanding tombstones.

Ambiguity policy (dm detects, agent decides): multiple candidates → prefer the path
hint, flag the rest, `k`/`u` settles it.

Explicitly out for v1: **hunk-level following** (blame-style line attribution) —
max fidelity, but requires line ranges on notes, which design.md §7.5 already rejected
as rot-prone. Whole-file blob identity + git history is the 90% solution; the only
novel dm state is the occasional re-key record (the existing RA record with a new job).

## 8. Move-with-edit — git rename detection

Git stores no renames; `git mv` + edit and `rm`/`add` + edit are identical in history.
Rename-ness is inferred at diff time by content similarity (`-M`, ~50% default
threshold). This slots in as resolution layer 3:

- A move-with-edit leaves blob `B` at `old/path` (parent) and `B' ≠ B` at `new/path`
  (child); similarity pairs them, extending the lineage chain `B → B'` across the path
  change. The note surfaces at `new/path` flagged `⚠stale` — correct, since it
  described the pre-edit content; `k` re-blesses and re-keys to `B'`.
- **Run detection eagerly at reconciliation, not lazily at read**: the since-snapshot
  diff is one commit range, so the candidate set is tiny and pairing is
  high-confidence. Append a re-key record per pair touching a noted file.
- **Resolved once, ever**: re-key records union across replicas via the store branch —
  whoever reconciles first pays; everyone inherits, including replicas that never saw
  the intermediate commits. (Contrast design.md §8.4: every branch's agent re-homes
  manually at the gate.) Read-time `git log --follow -M` remains the fallback for
  moves made by someone who never runs dm, before any replica reconciled that range.
- **dm can beat stock git detection**: it only cares about noted paths, so it can
  afford a lower similarity threshold plus copy detection (`-C`) without whole-tree
  false-positive risk, and can score candidates against the note's *anchor* blob
  directly.

**Residue handed to the agent** (worklist, never a hard gate): move + heavy rewrite
below threshold (indistinguishable from delete-and-create — `mv` survives as a verb
for exactly this); file splits with both sides heavily edited (copy detection catches
at most one side; ambiguity policy applies); true deletions.

Net effect: rename detection is what shrinks design.md's §8.4 from a mandatory,
every-branch, hard-gated manual reconciliation to an automated common case with an
agent-reviewed remainder.

## 9. Open questions — what the spike must validate

1. **Does resolution follow ordinary edit churn?** If everyday editing routinely lands
   notes in "unresolved", the model fails regardless of the merge story. Metric:
   fraction of notes resolving via layers 1–3 across a real repo's history replay.
2. **False-pairing rate at lowered similarity thresholds.** A note silently following
   the *wrong* file is worse than one going unresolved → conservative auto-accept
   threshold + a "resolved-by-inference, unconfirmed" flag the agent can bless in bulk.
3. **Read-path performance on large repos**: the per-noted-file history walk must be
   amortizable (blob→entry index built at reconciliation).
4. **Store transport**: single branch with union merge driver (git-annex-style) vs
   `refs/dm/*` operation DAGs (git-bug-style; causal ordering would also retire the
   wall-clock LWW caveat and review B-items touching rec-id ordering). Decide.
5. **Derived-LCA home**: cost of computing member resolution per read; caching.
6. **Branch privacy semantics**: is "no blob, no note" acceptable isolation, or does
   any use case need notes hidden even from someone holding the blobs?
7. **Hygiene without a hard gate**: what replaces the forcing function (D1 interacts).

## 10. Relationship to design.md

**Carries over unchanged**: the record/CRDT layer (§5, §7: CR/SU/TB/RA/FB/LN/UL,
rec-id ULIDs, G-counter headers), handles, the batch CLI and inner loop (§3, §4),
testing strategy (§9). Review findings A2/B5/C1–C3 (handles, id storage, link homes,
locking, backreferences) apply to *both* architectures and still need resolving.

**Dissolves under this spike**: most of §8 — companions and worktrees (§8.1), the fold
choreography and merge detection (§8.2, §8.5 — including the patch-id detection adopted
2026-07-14), commit cadence gating (§8.3), the hard orphan gate (§8.4). Staleness
(§7.5) is *promoted*: the anchor becomes the primary key.

**Superseded if adopted** (fallback package if the spike fails): the `Dm-Code` binding
trailer + recorder-hooks package on the existing companion model. Idea 1
(reconciliation) should be adopted in either outcome.

**Decision pending**: run the spike (validate §9, especially Q1/Q2) before investing
further in companion-model patches, since a positive result obsoletes them.
