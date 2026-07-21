# Design Review — Challenges to `design.md`

Adversarial review of [design.md](design.md). Verdict: the storage core — append-only
logs unioned by record-ULID, G-counter headers, LWW by rec-id — is sound. The problems
cluster at two seams: **where the shadow tree meets real git workflows** (skew,
divergence, deadlock) and **identity minting** (handles). Several places contradict the
"design settled, no open TBDs" banner.

Status legend: `[ ]` open · `[x]` resolved (note resolution inline).

---

## A. Critical — breaks stated invariants or destroys data

### A1. Orphan hard gate can destroy live knowledge under tree skew
- [x] *(resolved 2026-07-19)* Dissolved by the content-addressable adoption: the hard
  gate is gone; orphans are a never-destructive worklist (design.md §9.6).

§8.4 defines an orphan as any note-bearing path absent from the *current code working
tree*, **globally**, and hard-gates until every one is resolved by `mv` or `rm`. But
nothing binds a companion's content to the code branch's *commit*: `dm checkout` creates
`llm/<B>` from the **tip** of `llm/main` (§8.5), while the code branch may fork from an
older commit — a hotfix cut from a months-old tag is the extreme case, an un-pulled
local `main` the mundane one. Every file added to `main` in between shows up as an
orphan in this branch; `mv` has no valid destination in this tree, so the gate funnels
the agent into `rm` → tombstone → **terminal, no resurrection** (§7.3) → the merge
faithfully propagates the deletions into `llm/main` for everyone.

The founding invariant holds — no conflict — but determinism isn't safety: the gate
manufactures destructive writes that the merge then launders. The doc already solved
this exact problem for staleness by scoping it to the working set (§8.4, "scoping is
what earns the hard gate") — orphans get the hard gate *without* the scoping.

**Fix directions:** an "exists at merge-base / on main — park it" resolution, or scope
the orphan check to paths the branch actually touched.

### A2. Handle minting degenerates under monotonic ULIDs
- [x] *(resolved 2026-07-20 as Q9)* Monotonic minting is scoped to rec-ids, where
  ordering is load-bearing; entry-ids mint with fresh randomness per id, so
  tail-prefix handles keep their full entropy and stay derived, not stored
  (design.md §3.5, §8.3).

Handles are the first 6 chars of the 80-bit random tail (§2.5), i.e. its top 30 bits.
Monotonic mode (§7.3) makes same-millisecond ULIDs *increments* of each other — only the
lowest bits differ. So every multi-`a` batch (the normal case for a fast CLI) mints
entries whose 6-char handles are **identical by construction**, and the escape hatch —
"grab a couple more chars" — grabs chars that are also identical; disambiguation only
appears in the last one or two chars of the full tail. The doc's own mitigation
("handles are collision-checked at mint time anyway") is exactly what breaks.

**Fix direction:** mint handles from their own randomness, decoupled from the id — which
also concedes that a handle is *stored state*, not identity-derived (extension already
made it stateful; an extended handle can't be recomputed from the ULID alone). Interacts
with B5 (where is the handle/id stored?).

### A3. Same-companion divergence across clones has no merge path
- [x] *(resolved 2026-07-19)* Dissolved by the content-addressable adoption: one store,
  and `dm sync` is exactly the fix direction below (fetch → CRDT union → push with
  retry), design.md §8.4.

Two agents on two clones writing `llm/main` (or the same topic companion) is the
*primary* scenario the CRDT machinery exists for — and the deferred CI end-state (§11)
makes remote commits to `llm/main` routine. But `dm merge <src>` only takes a **local,
different** branch (§8.1), and `dm fetch` "moves the companion ref from origin" — on a
diverged local ref that's either a refusal or a clobber; merging `origin/llm/main` into
`llm/main` is inexpressible in the verb set. `dm push` after a non-fast-forward race has
no specified fetch-merge-retry loop, and stat auto-commits guarantee frequent pushes
from every *reading* clone.

**Fix direction:** remote-tracking staging refs plus a `dm sync`-shaped verb
(fetch → CRDT-merge → commit → push with retry).

### A4. Alignment check and content-dirt refusal deadlock
- [x] *(resolved 2026-07-19)* Dissolved: no checkout binding, no alignment check, no
  content-dirt refusal exists — dm serves whatever HEAD it finds (design.md §8.2).

Every `dm` invocation refuses on code-branch/companion mismatch (§8.1); `dm checkout`
refuses on content dirt; the prescribed remedy is `dm commit` — which is itself an
invocation that fails the alignment check. Sequence: write notes, forget `dm commit`,
plain `git checkout other-branch` → wedged. The escape (switch back) is unstated and
unavailable if the branch was deleted.

`dm commit` can't simply be exempted: commit stamps `@` anchors from the code branch's
HEAD (§7.3), so stamping under mismatch anchors notes to the wrong blobs.

**Fix direction:** stamp against the companion's *bound* code branch ref rather than the
worktree's checked-out branch, or allow a `--no-stamp` commit as the escape hatch.

### A5. Within-session drift is silently blessed; the doc's stated defense doesn't hold
- [x] *(resolved 2026-07-19)* Dissolved by construction: anchors are stamped at write
  time from the working-tree bytes — no `@` placeholder, no commit-time stamping window
  (design.md §8.3).

A note written at 10:00 describes the file as it was at 10:00; the agent keeps editing;
`dm commit` at 17:00 stamps the anchor with the **17:00 blob** — asserting freshness
against code the note never described. §7.3 claims "§8.4 scopes the gate to exactly
these files" — but a pending `@` **reads as fresh**, and the stale worklist only lists
anchor *mismatches*, so these notes are structurally invisible to the gate. The cited
defense is unreachable by the mechanics as specified. (Same gap applies to `k`: verified
against the working tree at `k`-time, stamped later at commit time.)

---

## B. Internal contradictions ("design settled" is false in four+ places)

### B1. Stale-in-ranking, both ways
- [x] *(resolved 2026-07-19)* Settled in the rewrite: staleness flags on read but never
  reorders in v1 (design.md §5.6, §9.5).

§7.5: "ranking (§4.6) sinks stale below fresh." §4.6: staleness signals "just don't
reorder for it in v1." Pick one.

### B2. Feedback in ranking, same disease
- [x] *(resolved 2026-07-19)* Settled in the rewrite: feedback/usage ranking effects are
  worded as deferred to the weighted scoring function (design.md §7.3, §7.4).

§6.3 says `+` "boosts ranking" and `-` "demotes"; §6.4 says ranking "gains usefulness
ratio + expansion rate" — all present tense, all contradicted by §4.6/§11's deferral.

### B3. The `t` tiebreak
- [x] *(resolved 2026-07-19)* The leftover §8.2 text was removed in the rewrite; `t`
  merges by max timestamp, no tiebreak (design.md §1.3, §7.2).

§1.3: max timestamp, "no tiebreak is needed." §8.2: "orders by wall-clock +
**replica-id**." §1.3 is right (the register's value *is* the timestamp); §8.2 is
leftover text.

### B4. "Per-file merge" is false once `MV` exists
- [x] *(resolved 2026-07-19)* Dissolved: `MV` records and the per-file merge are gone —
  the store unions per-record and moves are derived at read time (design.md §8.3, §9);
  the manual `mv` verb's record form is tracked as design.md §14 **Q7**.

§8.2 says the merge "reads both branch versions of each note file, merges each file
per-region" — but the `MV` redirect map is built from records *across* files and sweeps
entries between files. Unaddressed: the swept entries' **header stat rows** must migrate
too. The real algorithm is a whole-tree fold with a global redirect pass; header-row
migration on move is never specified.

### B5. What is actually stored in the entry-id field?
- [x] *(resolved 2026-07-20 as Q10)* Records and stat rows hold **full entry-id
  ULIDs**; handles never appear in stored records — surface-only, resolved at
  parse time against the visible set. A cross-replica handle collision is an
  ambiguity error, never record interleaving (design.md §8.3).

§7.3's schema says `<entry-id>` (a ULID, 26 chars); the §7.1 example stores `a3f9c1` —
the 6-char handle, no ellipsis — in both header rows and body records. If files key on
handles, a cross-replica handle collision (the case §2.5 explicitly anticipates) makes
two different entries' records **interleave under one key at merge** — corruption, not
ambiguity. If files store full ULIDs, the ambiguity-resolution story works but the
examples mislead. Must be pinned; it's the difference between a display bug and data
corruption. Interacts with A2.

---

## C. Underspecified but load-bearing

### C1. Link storage and reverse lookup
- [x] *(resolved 2026-07-20 as Q11)* One-file-per-record answers "which log"; the
  back-link/handle index is a derived, content-keyed immutable cache file
  (`index-<store-tip>`), rebuilt wholesale at sync, pending scanned linearly
  (design.md §8.2).

§7.3 never says *which file's log* an `LN`/`UL` record is appended to. Back-links are
"computed on read" and `r:#handle` resolves a handle to its home file — both require
either a whole-shadow-tree scan per read or an index that appears nowhere in the design.

### C2. No same-batch backreference for freshly minted handles
- [x] *(resolved 2026-07-20 as Q12)* Positional `$N` backrefs — valid wherever a
  handle is, parse-rejected unless pointing at an earlier `a` (design.md §4.2).

`a` mints the handle in *output*, so create-then-link — the canonical arch-note gesture,
per the doc's own agents.md prompt — always costs two round-trips in a design whose
stated purpose is amortizing round-trips. Telling detail: §7.3 justifies monotonic
minting with "an `a` followed by a `u` of the same entry in one batch" — a batch that is
**unwritable** in the grammar as specified, since the agent can't know the handle yet.

**Fix direction:** client-supplied ids, or positional backreferences (`u:$1:...`).

### C3. No intra-clone concurrency control
- [x] *(resolved 2026-07-20 as Q13)* One advisory exclusive `flock` on
  `.git/.dm/lock` for every invocation's duration; dies with the process; minting
  resumes from the largest pending rec-id so serialized same-millisecond processes
  stay rec-id-ordered (design.md §8.2).

Two `dm` processes in one clone (parallel agent sessions are the norm now) share a
replica id and a worktree with zero locking: lost G-counter increments, interleaved
header writes, and "monotonic per process" minting that isn't monotonic across
processes. §7.4 worries about `cp -r` duplicating a replica id but not about two
processes using the same one simultaneously.

**Fix direction:** lockfile in `.dm/` at minimum.

---

## D. Strategic / product-level

### D1. v1 ships unlimited write throughput and defers the entire hygiene loop
- [x] *(resolved 2026-07-21)* Named and instrumented rather than gated: design.md
  §9.6 adopts four soft-pressure layers (flag demotion in ranking, crowding nudge
  on write acks, bounded context-scoped worklist, sync health line); hard gates and
  auto-decay rejected on principle. The premise this finding names is now tracked
  by pilot metrics (worklist backlog growth, stale fraction, usefulness ratio)
  attached to Q1.

Ranking weights, housekeeping report, compaction, dedup — every mechanism that would
keep the store healthy is in §11, while the write path is fully armed (including
recursive `rm` with no undo, under hard-gate pressure). The bet that agent discipline
alone prevents a slop spiral is the biggest unvalidated premise in the document, and it
isn't named as a risk anywhere.

### D2. Dropping whole-store search kills cold recall
- [x] *(resolved 2026-07-20 as Q14)* Deliberately deferred, not dropped: v1 `s`
  stays context-scoped; the single global store keeps `s::term` cheap to add once
  the need is demonstrated (design.md §5.5, §15).

"What do I know about deploys?" has no verb: `s` needs a path, and `o`/`d` knowledge
homed on folders is precisely the knowledge you *don't* discover by reading a code file.
Dropping (not deferring) global search (§4.5) optimizes the read-before-touch flow while
orphaning the recall flow the tool's own pitch ("wants back later, without re-deriving")
leads with.

---

## E. Smaller nits

- [x] **E1.** *(resolved 2026-07-19)* Wording fixed in the rewrite (design.md §3.3:
  "where secrets live (never values)").
- [ ] **E2.** C0 rejection in bodies (§3.3) bans tabs, i.e. many pasted code snippets.
- [ ] **E3.** Paths containing `:` break every non-final field in the grammar
  (`mv:old:new`, `r:path:N` — `r:foo:1` is ambiguous with a file named `foo:1`).
  Path-valued *record* fields have the sibling problem (spaces); both were folded
  into **Q6**. *(2026-07-20: the record half is resolved — canonical
  percent-encoding, design.md §8.3. The grammar half — `:` inside a non-final CLI
  path field — remains open.)*
- [x] **E4.** *(resolved 2026-07-14)* `dm unmerged`'s ✓-detection parsed free-text
  merge-commit subjects. Replaced in design.md §8.5 with purely content-based
  detection: second-parent ancestry (true merge), whole-range `git patch-id` (squash),
  `git cherry` (rebase). Remaining edge cases documented there as accepted: diff
  modified in flight, empty range, deleted local ref (all → `?`), and patch-id
  coincidence (the one false-`✓`, argued harmless via idempotent union).
- [ ] **E5.** `dm fixture build` (§9.2) ships a test-fixture subcommand in the
  production binary.

---

## Theme

The document rigorously verifies its **merge algebra** but assumes its **workflow
topology** — that code tree and shadow tree are always cut from the same instant.
A1, A3, A4, and A5 are all that one assumption failing in different places.
