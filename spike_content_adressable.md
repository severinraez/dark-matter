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

Surveyed separately in
[existing_git_branch_follow_strategies.md](existing_git_branch_follow_strategies.md):
metadata-beside-code systems (git-annex, git-bug, git-appraise, Gerrit NoteDb, Fossil),
git-observing/wrapping systems (jj, GitButler, hook-based watchers), and the named-and-
rejected options (submodule binding, forge-native storage).

dm ends up needing none of the sync mechanisms surveyed there: with content + origin
anchored on every note (§3), there is no dm-side sync state to maintain — reads compare
the anchors against the current tree and refs directly (§5). Nothing observes git;
queries just look.

## 3. The two ideas

### Idea 1 — Anchor notes to content + origin, not to a branch-mirrored path

Replace (branch, path) as a note's home with two facts stamped at write time:

- **content key** = blob SHA of the described bytes (`git hash-object` of the
  working-tree file — no `@` placeholder, no stamp-at-commit; A5 disappears by
  construction: the note is bound to the exact bytes it described),
- **origin** = the HEAD commit at write time (branch name kept as a display hint only).

One metadata branch for the whole repo (no `llm/<B>` companions). The two anchors are
exactly what §0's visibility rule consumes: the content key answers **(a)**, the origin
commit answers **(b)**.

### Idea 2 — Visibility is a query, not maintained state (from Fossil/git-annex)

Where a note applies is never stored — it is computed per read by comparing the
anchors against the current tree and refs. A note written on `topic/x` is invisible on
`main` because its origin hasn't landed there, and appears the moment it does,
regardless of merge strategy. No companions, no `dm checkout`, no fold, no orphan
gate — and no dm-side snapshot to keep in sync with git either: nothing observes git
events. The only stored derivations are **verdict records** (§5), memoizing the one
thing git structurally forgets — what became of rewritten origin lines.

## 4. Model sketch

- **Store**: one metadata branch (or `refs/dm/*` DAGs, git-bug-style), append-only
  records, CRDT union merge — the entire §5/§7 record machinery of design.md carries
  over unchanged (CR/SU/TB/RA/FB/LN/UL, rec-id ULIDs, G-counter headers, handles).
- **Anchors**: file notes → content blob + origin commit, path as display hint (§3).
  Folder notes → path key + origin commit, resolved by the same read-time diff (§6).
- **Inner loop**: unchanged — `r:` / `s:` / `a` / `u` / `d` / `k` / `f` / `al` / `dl`.
- **Outer loop collapses to one verb**: `dm sync` = commit store → fetch → CRDT union →
  push (with non-ff retry). Replaces checkout/merge/unmerged/prune/push/fetch.
- **§8.4's hard gate** becomes an optional hygiene worklist ("notes not resolving in
  this tree"), never destructive: an unresolved note stops *surfacing*; nothing forces
  tombstones.

## 5. Mechanics — what is persisted where

Derived requirements-first from §0: persist only what the visibility rule forces, compute everything else at
read time. A note must durably carry three facts — its text, a content fingerprint for
rule (a), an origin commit for rule (b) — and row 14 forces a shared, conflict-free,
append-only note set. Everything beyond that floor is either a memoized query result or
a disposable cache.

### Terms

| term | meaning |
|---|---|
| **store** | the single shared metadata branch (`refs/dm/store`): append-only record files, one ULID-named file per record, CRDT union merge. Its commit tree also carries the **anchored blobs**, keeping described content fetchable forever (no reflog dependence). The only replicated persistence. |
| **pending** | `.git/.dm/pending/` — records not yet committed to the store; clone-local; folded into a store commit by `dm sync`. Reads always see **store ∪ pending**. |
| **cache** | `.git/.dm/cache/*` — memoized read-time query results (blob→records, similarity matches). Disposable; never load-bearing. |
| **note record** | text · content key = blob SHA of described bytes at write time · path hint · **origin** = HEAD commit SHA at write time (branch name as display hint only). |
| **verdict record** | memoized answer about an origin line git can no longer answer itself: **landed** (origin O landed as commit M — squash/rebase) or **abandoned** (origin unreachable from all refs, unlanded). Appended lazily by whichever clone first computes it; union-synced. |
| **derived** | no write — computed at read time from CR + store ∪ pending. |

Both halves of the visibility rule are two-point git queries at read time:
**(a) content present?** — exact blob in the current tree, else similarity-match the
anchored blob against the tree (git rename detection between origin commit and
checkout): match ⇒ visible (stale if edited), none ⇒ orphaned. **(b) origin landed?** —
one ancestry check, falling back to verdict records where history rewriting broke
ancestry. The five states of §0 are never stored — they are classifications.

### Persistence table

Rows mirror the requirements table in §0.

| # | action in code repo (CR) | persistence dm |
|---|---|---|
| 1 | repo created, commits exist, dm initialized | create `.git/.dm/` (replica id, empty pending); fetch or create the empty store branch. Nothing in the working tree, nothing on code branches |
| 2 | note added on file F while on main | append **note record** to **pending** (F's blob anchored, origin = main's HEAD) |
| 3 | branch B from main; note added on G | same as row 2, origin = B's HEAD. Visibility split main/B: **derived** (ancestry check on origin) |
| 4 | main progresses; note on H added on main | same as row 2. B's non-seeing of H: **derived** |
| 5 | B rebased onto main (clean) | **nothing at rebase.** First read on B that finds origin not-ancestor-but-present computes the patch-id match and appends a **landed verdict** (old origin → new commit); every replica inherits |
| 6 | branch C created; CR still on B | **nothing.** Mutual invisibility: **derived** |
| 7 | note N added on B, no commit/sync yet | append to **pending** only — dirty blob anchored via the store tree. Immediate visibility falls out of reads consulting store ∪ pending |
| 8 | B rebased with conflict resolution changing G | **nothing at rebase.** (b): landed verdict as row 5. (a): G's drift is **derived** — anchored blob vs. current tree similarity ⇒ stale flag, recomputed per read, never stored |
| 9 | B merged into main | true merge: **derived** — origin now ancestor of main, zero writes. Squash: first read appends a **landed verdict** (patch-id/tree containment). G's stale flag persists because it keeps being derived, until `k` re-anchors the note to the current blob |
| 10 | noted file edited in working tree, uncommitted | **derived** — working blob ≠ anchor ⇒ stale. No write at any point |
| 11 | noted file renamed/moved | **derived** — anchored blob found at new path (similarity match if also edited ⇒ stale). No re-key record; cache may memoize the match, disposably |
| 12 | noted file deleted on the current line | **derived** — no match anywhere in tree ⇒ orphaned, worklist is a query. Writes only when the agent acts (tombstone, re-anchor) |
| 13 | branch B deleted without merging | first read that finds origin unreachable from every local/remote ref and unlanded appends an **abandoned verdict** — persisted so the classification survives GC and replicates |
| 14 | teammate clones/pulls the same state | `dm sync`: fold pending → store commit → fetch → union merge (distinct ULID filenames ⇒ conflict-free) → push with non-ff retry. Teammate fetches store; identical view is **derived** — same store + same refs ⇒ same answers (asterisk: transient divergence until a needed verdict record exists) |
| 15 | detached HEAD / bisect at old commit | **derived** — same queries against the detached tree and its ancestry. Writes (if any) persist as row 2, origin = the detached commit |

Durable writes happen at exactly three moments: **note records at write time**
(rows 2/3/4/7/15), **verdict records at first-read-after-history-rewrite**
(rows 5/8-b/9-squash/13), **store commits at sync** (row 14). Rows 6, 10, 11, 12 are
pure queries. There is no per-clone mutable state that could make two clones disagree
except pending, which is by definition not yet shared.

### What the model pays

- **Read-time cost moves up**: similarity matching per unresolved note at read instead
  of once at reconciliation; mitigated by the disposable cache, but Q2 (large-repo
  performance) gets sharper.
- **Convergence is eventual, not immediate**: two clones can transiently disagree on
  (b) when one has fetched an origin commit the other lacks — until the first memoized
  verdict lands in the store (the row-14 asterisk).
- **Store growth**: anchored blobs ride in the store tree so dirty-written content
  stays fetchable; git dedups blobs that any commit already contains, so the overhead
  is only truly uncommitted content.
- **No forcing function for hygiene**: worklists/ranking/decay must replace design.md's
  hard gate — review finding **D1** (deferred hygiene loop) becomes *more* important,
  not less.

## 6. Folder notes — path as key

(§0 specifies file notes only; folder-note behavioral requirements are still open.)

Folder note = text · **path key** · origin commit. A folder has no stable blob to key
on — hashing the path is still a path key, just unreadable, and the tree SHA is
globally volatile by construction (any edit to any file under the folder changes it).
So the path itself is the key, and the **origin commit supplies the matching
fingerprint** that the anchored blob supplies for files: the origin's tree at the
noted path — its tree SHA and member set at write time.

Resolution is the same two-point comparison as §7, origin → checkout:

1. **Exact** — the path exists in the checkout → visible, fresh. Member churn doesn't
   matter: a folder note describes the place, not the contents.
2. **Moved, pure** — path absent, but the origin's tree SHA at that path appears
   elsewhere in the checkout → follow there, fresh (`git mv api/ svc/` with no member
   edits preserves the tree SHA by construction).
3. **Moved with churn** — no identical tree; aggregate the file-level rename detection
   already computed for file notes (§8): where did the folder's member files go?
   Majority prefix mapping (`api/* → svc/*`) → follow, flagged below full agreement.
   Files pair blobs by similarity; folders aggregate those pairs and vote by prefix.
4. **Unresolved** — members appear in the diff as deletions (no rename pairs) →
   folder deleted → orphaned, worklist. Scattered with no majority prefix →
   ambiguity, agent decides (same policy as multi-candidate file matches).

**Follow heuristic**: the rank-3 vote auto-follows only
when three checks pass — a **majority of paired members** agrees on one target
prefix; the winner beats the runner-up prefix by a clear **margin** (6/10 to `svc/`
with the other 4 deleted is a rename; 6/10 to `api-core/` with 4 in `api-http/` is a
split); and the pairs **cover** a minimum fraction of the original member set (a
2-member folder with one pair and one deletion is 100% of pairs but 50% coverage —
too thin to auto-follow). Unanimous with full coverage → fresh; passing below that →
flagged; no winner → ambiguity. Exact fractions ride the same Q1 calibration harness
as the file-level bands (§8).

**Rule (b) is unchanged** — origin ancestry and verdict records apply to folder notes
verbatim; nothing folder-specific.

**Nothing new persisted**: matches land in the disposable cache; `k` re-anchors the
path key to the resolved location exactly as it re-anchors a file note to the current
blob — same verb, same RA record, no separate re-path record type. Cost piggybacks on
the same batched origin→checkout diff file notes already need (Q2 unaffected).

Edge cases: **split** (`api/` → `api-core/` + `api-http/`) — two prefixes, no
majority margin → ambiguity, agent picks (duplicating the note is out of scope for
v1); **absorbed** (contents moved into a pre-existing `lib/` that is mostly foreign
content) — the vote may be unanimous, but the target may no longer be what the note
described → follow with an unconfirmed flag regardless of vote strength; **empty
folders** — git cannot represent them, so a folder containing no files has nothing
to diff (inherent limit).

## 7. Resolving drift — read-time resolution

With both anchors stored, resolution is a **two-point comparison**: the note's
write-time state (anchored blob, origin commit) against the current checkout. No
history walk is load-bearing — git's diff machinery between origin and checkout
supplies moves, copies, and similarity pairing on demand.

### Rule (a) — is the content present?

1. **Exact** — anchored blob in the tree at the path hint → visible, fresh.
2. **Moved** — anchored blob in the tree at another path → visible there, fresh
   (pure rename/extract — the case path-keying can never handle: extract-file
   refactors carry their notes).
3. **Edited** — blob absent; similarity-match it against the checkout (§8) → visible
   at the matched path, flagged `⚠stale`. Staleness is gradable (similarity score) —
   keep the binary flag for v1.
4. **Unresolved** — no match → orphaned; hygiene worklist. A *visibility* fact, never
   a gate demanding tombstones.

Ambiguity policy (dm detects, agent decides): multiple candidates → prefer the path
hint, flag the rest; `k`/`u` settles it.

### Rule (b) — has the origin landed?

Origin ancestor of HEAD → landed here · else a **landed verdict** exists → landed ·
else origin reachable from another local/remote ref → pending-elsewhere · else
abandoned (verdict appended on first computation — §5).

**Verdict correction**: wrong verdicts are repaired at the note level
in v1, with verbs that already exist. Wrong *abandoned* (notes wrongly worklisted —
e.g. the origin lived on a remote dm couldn't see): `k` from the worklist on the
right checkout — re-anchoring to current blob + HEAD gives the note a fresh origin
that resolves by plain ancestry, no verdict consulted. Wrong *landed* (notes wrongly
surfacing on a line their origin never reached): `k` would bless them there —
instead `d` here and re-add on the correct branch. Accepted v1 caveats: a verdict is
per-*origin*, so repair is O(notes sharing that origin), and the stale verdict
lingers in the store. LWW-overridable verdict records stay a later optimization for
the many-notes-per-origin case, not a v1 requirement.

### Hygiene

`k` = confirm + re-anchor to the current blob and HEAD — design.md's RA record and its
LWW fold carry over unchanged. `u` rewrites and re-anchors; `d` tombstones.

Explicitly out for v1: **hunk-level following** (blame-style line attribution) —
max fidelity, but requires line ranges on notes, which design.md §7.5 already rejected
as rot-prone. Whole-file blob identity is the 90% solution.

## 8. Move-with-edit — git rename detection

Git stores no renames; `git mv` + edit and `rm`/`add` + edit are identical in history.
Rename-ness is inferred at diff time by content similarity (`-M`, ~50% default
threshold). This powers resolution layer 3:

- A move-with-edit leaves the anchored blob `B` at `old/path` in the origin tree and
  `B' ≠ B` at `new/path` in the checkout; one similarity diff origin→checkout pairs
  them. The note surfaces at `new/path` flagged `⚠stale` — correct, since it described
  the pre-edit content; `k` re-blesses and re-anchors to `B'`.
- **dm can beat stock git detection**: it only cares about noted paths, so it can
  afford a lower similarity threshold plus copy detection (`-C`) without whole-tree
  false-positive risk, and can score candidates against the note's anchored blob
  directly.
- **Acceptance is banded, with a uniqueness margin**: ≥ ~80% similarity
  to the anchored blob *and* the best candidate beating the runner-up by ≥ ~20 points
  → auto-accept (`⚠stale`); ~50–80%, or a high score with a close runner-up → follow
  flagged "resolved-by-inference, unconfirmed" for bulk blessing; below ~50% →
  unresolved. The margin is the load-bearing part: false pairings come from clusters
  of near-identical candidates (boilerplate, generated code, fixtures), not from a
  lone wrong file scoring high in isolation. Thin margins tie-break on path-hint
  affinity (same basename, directory distance). The band numbers are starting
  guesses — Q1's replay harness doubles as the calibration rig.
- **Nothing persisted**: matches land in the disposable cache; re-running the diff
  reproduces the answer on any clone, so there is no resolved-state to replicate,
  corrupt, or migrate.

**Residue handed to the agent** (worklist, never a hard gate): move + heavy rewrite
below threshold (indistinguishable from delete-and-create — `mv` survives as a verb
for exactly this); file splits with both sides heavily edited (copy detection catches
at most one side; ambiguity policy applies); true deletions.

Net effect: rename detection shrinks design.md's §8.4 from a mandatory, every-branch,
hard-gated manual reconciliation to an automated common case with an agent-reviewed
remainder.

## 9. Open questions — what the spike must validate

1. **Does resolution follow ordinary edit churn?** If everyday editing routinely drops
   notes to layer 4 (unresolved) of §7's rule (a), the model fails regardless of the
   merge story. Metric: fraction of notes resolving via layers 1–3
   (exact/moved/edited, §7–§8) across a real repo's history replay — which doubles
   as the calibration rig for the acceptance-band fractions (§6, §8).
2. **Read-path performance on large repos** (§5 "what the model pays"): all resolution
   cost now sits on reads; per-note similarity queries must amortize through the one
   batched origin→checkout diff that file (§8) and folder (§6) resolution share, plus
   the disposable cache (§5 terms).
3. **Store transport** (§4, §5 terms): single `refs/dm/store` branch with a union
   merge driver (git-annex-style) vs `refs/dm/*` operation DAGs (git-bug-style) —
   causal ordering would also retire the wall-clock LWW caveat and the review B-items
   touching rec-id ordering. Decide.
4. **Folder-note behavioral requirements** (§6): §0 covers file notes only; the
   agent-facing UX for split/absorbed cases is also unspecified.
5. **Hygiene without a hard gate** (§4, §7): design.md §8.4's gate became an optional
   worklist and stale/orphaned are mere classifications — what supplies the forcing
   function (ranking, decay, review cadence)? Review finding D1 grows more important
   under this model (§5 "what the model pays").

## 10. Relationship to design.md

**Carries over unchanged**: the record/CRDT layer (§5, §7: CR/SU/TB/RA/FB/LN/UL,
rec-id ULIDs, G-counter headers), handles, the batch CLI and inner loop (§3, §4),
testing strategy (§9). Review findings A2/B5/C1–C3 (handles, id storage, link homes,
locking, backreferences) apply to *both* architectures and still need resolving.

**Dissolves under this spike**: most of §8 — companions and worktrees (§8.1), the fold
choreography and merge detection (§8.2, §8.5 — patch-id matching survives only in
shrunken form, inside landed verdicts, §5), commit cadence gating (§8.3), the hard
orphan gate (§8.4). Staleness (§7.5) is *promoted*: the write-time anchor becomes half
of every note's identity (content + origin).

**Superseded if adopted** (fallback package if the spike fails): the `Dm-Code` binding
trailer + recorder-hooks package on the existing companion model.

**Decision pending**: run the spike (validate §9, especially Q1 — which also
calibrates the §6/§8 acceptance bands) before investing further in companion-model
patches, since a positive result obsoletes them.
