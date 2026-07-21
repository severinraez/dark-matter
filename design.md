# Dark Matter — Design

> **Status:** architecture settled 2026-07-19 — the **content-addressable model**
> (notes anchored to content + origin, one shared store, visibility computed per read)
> is adopted, superseding the companion-branch model (rejected; §13). The behavioral
> requirements (§2) and the mechanics (§3–§12) are normative, with design decisions
> recorded as dated decision blocks in their sections. What remains is empirical —
> two build-time validation gates (§14); intentionally punted scope is §15
> (Deferred).

---

## 1. Overview

### 1.1 What it is
Dark Matter (`dm`) is a git-native memory layer for LLM agents: notes *about* a
codebase that live beside the code, surface exactly where and when they apply — right
branch, right commit, any merge strategy — and are queried through a token-efficient
batch CLI with progressive disclosure.

The knowledge is not source and does not belong in the code files. It is the "dark
matter" around the code — architecture rationale, gotchas, dev/ops know-how — that an
agent accumulates and wants back later, in the right context, without re-deriving it.

### 1.2 Design goals
- **Token-efficiency** — the CLI is batch-oriented and reads never dump; they return a
  *map* of what is queryable, with a size on every hidden thing, so the agent spends
  tokens deliberately.
- **Conflict-free sharing** — all notes live in a single append-only store merged by
  CRDT union; no human (or agent) ever resolves a merge conflict, by construction.
  Where a note applies is never stored — it is computed per read from the note's
  anchors and the current checkout (§2, §9) — so branch topology, rebases, and
  squashes need no bookkeeping and cannot corrupt anything.
- **Progressive discovery** — an agent starts shallow and drills into exactly the
  dimensions it needs (long bodies, links, parent notes) over successive batches.

### 1.3 Glossary
- **Store** — the single shared metadata branch (`refs/dm/store`) holding every note
  record; the only replicated persistence (§8.1).
- **Anchor** — the write-time facts binding a note to code. File notes: **content
  key** (blob SHA of the described bytes) + **origin**; folder notes: **path key** +
  origin (§3.1).
- **Origin** — the HEAD commit at write time; the lineage half of the visibility rule
  (§2).
- **Node** — an addressable location notes resolve to: a file or folder of the
  current checkout.
- **Logical entry** — one note. Has a stable id, a subject, a body, links, and a
  revision history. Addressed by a **handle**.
- **Handle** — a short, stable, identity-derived reference to a logical entry
  (`#a3f9c1`), reusable across batches and across syncs (§3.5).
- **Verdict record** — memoized answer about a rewritten origin line: **landed** or
  **abandoned** (§9.4).
- **Pending** — clone-local records not yet folded into the store; reads always see
  store ∪ pending (§8.2).
- **Replica** — a single clone/agent, identified by a local id, for CRDT counters
  (§8.5).
- **Subject** — the kind of knowledge a note carries: code / architecture /
  development / operations (§3.3).
- **LWW register** — a single-valued field merged **last-write-wins**: when two
  replicas set it concurrently, the merge keeps the value with the higher position in
  a **total order** (so every replica picks the same winner) and discards the other.
  Two order sources are used: the `t` stat register merges by **max timestamp** (equal
  timestamps carry the same value, so no tiebreak is needed), while record-log
  resolution (current body, anchor) orders by the winning record's `<rec-id>` ULID
  (§8.3). Everything else in the stats is a set/counter that unions.

---

## 2. Behavioral Requirements

Mechanism-free specification of how dm must behave (file notes agreed 2026-07-18;
folder notes the same day). These tables are **normative**: the storage (§8) and
resolution (§9) machinery exists to satisfy them, and the test scenarios (§11) replay
them row by row.

### 2.1 File notes

**Visibility rule**: a note is visible iff **(a)** the working tree contains the
content it describes **AND (b)** the note's origin line is part of the current
checkout's lineage — written on this line, or on a line that has since merged into it.
(b) is a deliberate isolation choice: a file-specific note may only make sense in the
changed context of its branch, so content presence alone is not sufficient. "On branch
B" below is shorthand for "the checked-out tree has B's content"; branch *names* carry
no visibility semantics.

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

> **Decision — strict lineage stands (2026-07-21, resolves Q5).** Row 15 is read
> strictly: notes written *later on main itself* stay hidden from old-main checkouts.
> One rule, no special-casing — a note is visible iff its content is present and its
> origin line had landed *as of the checkout*. A bisect mode that surfaces later
> knowledge about unchanged files at old checkouts is **deferred** (§15).

### 2.2 Folder notes

Same visibility rule, same shorthand. Rule (b) is identical for folder notes, so the
lineage rows are delegated wholesale (row 3 below); the folder-specific rows are all
about rule (a).

| # | action in code repo (CR) | behaviour dm |
| --- | --- | --- |
| 1 | note added on folder `api/` while on main | visible whenever the checkout contains path `api/` and descends from main |
| 2 | files under `api/` added, edited, deleted — any member churn | note stays visible and **fresh** — a folder note describes the place, not the contents; member churn alone never flags it |
| 3 | all lineage cases: branch isolation, landing by merge/squash/rebase, abandoned branch, teammate clone, detached HEAD | identical to file-note rows 3–9 and 13–15 above with "folder note" substituted — nothing folder-specific in rule (b) |
| 4 | `git mv api/ svc/`, no member edits | note follows to `svc/`, fresh |
| 5 | `git mv api/ svc/` plus member edits | follows to `svc/`; fresh when the whole folder evidently moved together, flagged unconfirmed when the evidence is partial |
| 6 | most members move to `svc/`, the rest deleted | follows to `svc/`, flagged |
| 7 | folder renamed in the working tree, uncommitted | as rows 4–6 — visible at the new path immediately, no commit required first |
| 8 | split: `api/` → `api-core/` + `api-http/` | **not** auto-followed: ambiguity, surfaced on the worklist, agent picks the home (duplicating the note is out of scope for v1); never silently dropped |
| 9 | members absorbed into a pre-existing `lib/` full of foreign content | follows to `lib/`, flagged unconfirmed regardless of how strong the evidence is |
| 10 | members scattered across several folders, none clearly the successor | ambiguity, agent decides — same policy as multi-candidate file matches |
| 11 | folder deleted (members deleted, nothing moved) | orphaned: leaves normal reads, appears on the worklist; never destroyed |
| 12 | note on `api/v1/`; parent renamed via `git mv api/ svc/` | note follows to `svc/v1/` — each note resolves independently; the move of the parent carries the child along |
| 13 | folder moved to `svc/` **and** a new, unrelated `api/` created at the old path | exact path wins: note stays at `api/` on the foreign content, fresh — the deliberate cost of path-as-key; `k`/`u` re-path it |
| 14 | folder deleted (note orphaned), much later a new `api/` created | note **resurrects** at `api/`, fresh — same consequence of path keying |

**Folder states**: the five states carry over with one substitution — folder notes
replace **stale** with **unconfirmed**: a folder note is never content-stale (row 2),
only follow-uncertain (rows 5, 6, 9).

**Rows 13/14 — accepted** (2026-07-18): exact-path-wins shadows a real move (13) and
resurrects long-buried notes (14). Accepted as-is: the behavior is right for
place-notes, and `k`/`u` repair the rest. An "occupant changed" flag (current contents
share nothing with write-time contents) remains a possible later softening, not v1
(§15).

---

## 3. Data Model

### 3.1 Anchors — content + origin, one store
A note's home is not a location in a mirrored tree; it is **two facts stamped at write
time**:

- **content key** = blob SHA of the described bytes (`git hash-object` of the
  working-tree file — the note binds to the exact bytes it described; no placeholder,
  no later stamping pass, §8.3),
- **origin** = the HEAD commit at write time (branch name kept as a display hint
  only).

Folder notes have no stable blob to key on, so their anchor is a **path key** +
origin; the origin commit supplies the matching fingerprint that the anchored blob
supplies for files — the origin's tree SHA and member set at the noted path (§9.3).

There is **one metadata store for the whole repo** (§8.1) — no per-branch companions.
The two anchors are exactly what §2's visibility rule consumes: the content key
answers **(a)**, the origin commit answers **(b)**. Where a note applies is **never
stored** — it is computed per read by comparing the anchors against the current tree
and refs (§9). Nothing observes git events; queries just look.

### 3.2 Note targets — where a note is anchored
Every note has exactly **one home** — one anchor:
- **file note** — anchored to the file's content; applies to that file wherever its
  content resolves (§9.1).
- **folder note** — anchored to the folder's path; applies to the folder **and
  everything under it, recursively**. This is the "parent notes" disclosure dimension:
  reading a file walks up its ancestors and surfaces folder notes along the way.

One home per note is non-negotiable — each note resolves independently through one
anchor, which is what keeps read-time resolution and the union-merged store simple.

### 3.3 Note subjects — what kind of knowledge
Subject is a **facet tagged on each entry**, not a second location; a node can carry
entries of several subjects at once.

| subj | meaning                                                       | typical target |
|------|---------------------------------------------------------------|----------------|
| `c`  | **code** — behavior, edge cases, gotchas                      | file           |
| `a`  | **architecture** — cross-cutting design, many nodes           | folder / root  |
| `d`  | **development** — build, test, codegen, flaky tests           | file or folder |
| `o`  | **operations** — deploy target, logging, where secrets live (never values) | folder |

The two axes (target × subject) are independent; every combination is meaningful.

> **Decision:** the subject enum is closed — no extensible/`general` fallback. Every
> note commits to one of the defined subjects; a forced choice keeps ranking and
> disclosure meaningful and avoids a catch-all bucket that erodes the taxonomy.

### 3.4 Architecture notes
Architecture knowledge is cross-cutting — "referred to by multiple files/folders" —
which fights the one-home rule. Resolution: **architecture is a subject-tagged note
(`a`) homed at the lowest common ancestor folder of the things it describes, carrying
`links[]` to the specific member nodes.** Members surface it via back-links.

Consequences:
- Everything stays a note with a single anchor → resolution + merge unchanged.
- Reuses existing machinery: recursive folder notes + links + parent-note disclosure
  compose to produce architecture notes for free.
- Truly global concepts living at the repo root is *correct*, not a bug.

This keeps the system to **one storage primitive** — a subject-tagged, linkable entry
with one anchor — rather than a separate "concept" object.

### 3.5 Logical entries, revisions & handles
Each note is a **logical entry** with a globally-unique internal id. Its display
**handle** is a fixed-length prefix of that id, shown as `#a3f9c1`. Because it is
identity-derived (not positional):
- it is the **same everywhere** the entry appears (home node *and* back-links),
- it **survives syncs** (union never changes an entry's id),
- the agent can copy the exact string it saw into a later `u`/`d`/`f`.

**Handle collisions:** fixed length (6 chars), **extended on the rare collision at
write time** — dm grabs a couple more chars when minting if the handle already exists
in store ∪ pending. The agent therefore always sees a stable, reusable handle. The one
case write-time extension cannot see — two replicas concurrently minting the same
handle for different entries, discovered only at sync — is handled at resolve time: a
handle that matches several entries errors with the extended candidates
(`✗ #a3f9c1 ambiguous: #a3f9c1k2 · #a3f9c1p9`), and surfaces display the extended
forms. (Resolved 2026-07-20: entry-ids mint with **fresh randomness** — monotonic
mode is scoped to rec-ids, §8.3 — so tail-prefix handles keep their full entropy;
and stored records always hold **full entry-id ULIDs**, never handles, so a
cross-replica handle collision is an ambiguity error, never record corruption —
Q9/Q10.)

> **Decision:** internal ids are **ULIDs** — globally unique without coordination,
> lexicographically sortable by creation time (a natural tiebreak), and independent of
> content so a supersede keeps the entry's id. A ULID is 48 bits of millisecond
> timestamp + 80 bits of randomness (26 base32 chars: 10 time + 16 random). The handle
> is therefore **not** a prefix of the ULID — the leading chars are wall-clock, so
> every id minted in the same ~4-minute window would share them — but the first 6
> chars of the **80-bit random tail** (30 bits of entropy), extended on the rare
> write-time collision as above. Using the whole ULID as the handle was considered and
> rejected: handles are the densest element of every surface line, and 26 chars vs 6
> is a ~4× token tax on exactly the output the design optimizes.

---

## 4. CLI Interface

### 4.1 Batch-over-stdin & two-phase execution
`dm` reads commands from stdin, one per line, `cmd:...` form, to amortize token cost:

```
dm <<EOF
r:file1.rb
r:test/file2.rb
a:test/file3.rb:c:some notes about file3.rb
EOF
```

Execution is **two-phase**:

1. Parse the **entire** input. Any syntax error → reject the whole batch, print the
   error, write **nothing**.
2. Syntactically valid → execute each command in order. Reads never fail the batch;
   individual write failures print their own error inline while other commands still
   apply.

> **Accepted cost:** a single typo rejects the whole batch, reads included, and the
> agent re-sends everything. Answering the valid reads while rejecting the malformed
> line was considered and kept out: it would make a batch's effects depend on which
> lines happened to parse, and "all or nothing at the syntax layer" is a simpler
> contract for the agent than a partially-answered batch. Semantic failures are
> already per-command (phase 2), so the tax is paid only on genuine syntax errors —
> rare, and worth the occasional re-send.
>
> **Decision:** the body is always the **last field** of a command, so once the first
> line is parsed we know exactly where the body starts — everything from there to the
> end of the command is body. Consequences: `:` inside a body needs **no escaping**
> (all preceding separators are consumed positionally before the body begins), and
> the body may span multiple physical lines. The **only** escaped character is the
> line separator that divides commands: a body newline is written as a trailing `\`
> continuation, so a command runs until the first line **not** ending in `\`.

### 4.2 Command grammar

| cmd | form                    | action                                   |
|-----|-------------------------|------------------------------------------|
| `r` | `r:path` \| `r:#handle` | read node surface, or expand one entry   |
| `r` | `r:path:N`              | read with disclosure depth N (§5.4)      |
| `s` | `s:path:t1\|t2`         | search node context for terms (OR)       |
| `a` | `a:path:subj:body`      | create entry (`subj` ∈ c/a/d/o)          |
| `u` | `u:#handle:body`        | supersede an entry's body                |
| `d` | `d:#handle`             | tombstone an entry                       |
| `f` | `f:#handle:sig[:reason]`| feedback (`sig` ∈ `+` `-` `!`)           |
| `k` | `k:#handle[:path]`      | confirm valid; re-anchor (split: §9.3)   |
| `mv`| `mv:old:new`            | relocate notes manually (§6.5); folders: recursive |
| `rm`| `rm:path`               | tombstone all entries at path (recursive)|
| `al`| `al:#a:#b[:note]`       | add link between two entries, opt comment|
| `dl`| `dl:#a:#b`              | delete a link between two entries        |

> **Decision — same-batch backreference (2026-07-20, resolves Q12).** `$N` refers to
> the entry created by the batch's **Nth command** (1-based; commands, not physical
> lines — bodies may span lines via `\`). It is accepted anywhere a `#handle` is
> (`u`, `d`, `f`, `k`, `al`, `dl`, `r`). `$N` must point to an **earlier** command
> that is an `a`; a forward, out-of-range, or non-create reference is a phase-1
> syntax error and rejects the whole batch (§4.1). Acks echo the minted handle
> (`al:$1:$2` acks `✓ #a3f9c1 → #7c22a1 linked`), so the agent still learns the
> stable handles from the same output.

### 4.3 Output format
One block per command, in input order. Blocks are delimited by **printable sentinel
glyphs at line start**, and content is emitted **raw** (no indentation) to save
characters:

- **next-marker** `▸` (U+25B8) prefixes each command echo; it starts a block and ends
  the previous one.
- **end-marker** `◾` (U+25FE) prefixes the terminal footer.

> **Decision — printable glyphs, not control bytes.** ASCII RS/GS (`0x1E`/`0x1D`) were
> considered — near-zero token cost, impossible in text — and rejected: agents read
> `dm` through terminals, shell captures, and API layers that routinely strip or
> mangle C0 control bytes, which would corrupt the framing in exactly the channel the
> tool targets. The glyphs survive every such channel, are debuggable by eye, and cost
> ~1 token each. Unambiguity is enforced at the write end instead: `dm` **rejects `▸`
> and `◾` in note bodies** (`✗` error) — they are pure decoration characters with no
> plausible use in a note — so a body line can never be mistaken for a boundary. (C0
> control bytes are rejected in bodies too, keeping output shell-safe.)

```
▸r:api/handler.rb
c #7c22a1 Validates tenant header before dispatch (+4 lines)
↑ 2 parent notes (1 arch) #f22e90 api/
→ 1 concept #9d8134 event-sourcing
context: 1 own · 2 parent · 1 link · ~6 hidden
▸r:#f22e90
api/ [a] #f22e90 ⚠stale
api reaches db only through repo/. Handlers must never import
models directly — the repository layer owns persistence + tenant scoping.
→ links: #a3f9c1 schema STI · #4c1180 tenancy
← linked-from: api/handler.rb · api/admin.rb · db/schema.rb
▸u:#a3f9c1:Now partitioned per tenant
✓ #a3f9c1 superseded (rev 2)
◾6 ok
```

Parse = split on `▸`; the first line of each block is the verbatim command echo, the
rest is raw content; `◾` closes the stream.

Write acks are one line: `+ #handle created`, `✓ #handle superseded (rev N)`,
`✓ #handle tombstoned`, `✓ #handle feedback +`, or `✗ #handle <error>`. An `a`
landing on a node already carrying many visible notes appends the crowding nudge
(2026-07-21, Q4, §9.6) to its ack — `+ #b41f02 created · node has 9 notes, consider
folding with u` — still one line, never an error; the threshold is an implementation
constant tuned in the pilot.

> **Decision:** use the Unicode structure glyphs (`→` link, `↑` parent, `←`
> linked-from, `★` arch, `⚠` stale) — visually unambiguous and collision-proof against
> content. **Match markers are dropped entirely**: search snippets carry no in-content
> highlighting, so no glyph ever needs escaping inside a body. The agent already has
> the term it searched for.

### 4.4 Error reporting

- **Batch-level** syntax failure → a single `!` line at the head of output, nothing
  applied: `!parse error line 3: expected subject (c|a|d|o) after path`.
- **Per-command** failure in an otherwise-valid batch → that command's block contains
  a `✗ …` line; other commands still apply.
- A final `◾N ok[, M error]` footer tallies the batch and signals end-of-stream.

---

## 5. Retrieval & Progressive Discovery

Guiding idea: **a read never dumps; it returns a map of what is queryable, with a
size on every hidden thing.** Discovery is the agent walking that map over successive
batches. v1 is **strictly lazy** (surface + explicit expansion); budget-fill is
deferred (§15).

### 5.1 The surface (`r:path`)
A budgeted digest of the node plus pointers to every adjacent dimension, each
collapsed:
- own entries → one-line previews (`subj #handle first-line (+N lines) ⚠flag`),
- parents/links → **not expanded**, shown as handles + labels + counts; arch parents
  are called out because they orient the agent,
- an **inventory footer** — the "how much more is there" signal in one line:
  `context: 2 own · 3 parent · 2 links · ~18 hidden`.

### 5.2 Cost hints
Every collapsed item carries its size: `(+12 lines)`, `linked from 3 nodes`,
`2 parent notes`. The agent sees the *shape* of hidden information and decides what is
worth a token.

### 5.3 Drilling by handle
`r` accepts a **path or a handle**, so expansion is always the same move:
- `r:path` → the surface digest for a node,
- `r:#handle` → the **full** body of one entry + its links one level + relations.

See a collapsed line → `r:#handle`. Whether it is a truncated note, a parent arch
note, or a concept, it is one verb.

> **Decision — handles respect visibility (2026-07-19).** Every handle-addressed
> command resolves against the current checkout's **visible set** (visible, stale,
> unconfirmed — §2). A **pending-elsewhere** entry is indistinguishable from a
> nonexistent one: `r:#handle` errors `✗ #a3f9c1 unknown`, `s` never matches it, and
> `al`/`dl` refuse it — its notes appear the moment the origin lands, as if newly
> created. The worklist states (**abandoned**, **orphaned**) likewise never surface
> in reads, search, or link expansion, but they *are* addressable by the repair verbs
> `k`/`u`/`d` (and `mv`/`rm`) from the worklist (§9.6) — that is how an orphan is
> re-homed or retired without ever being silently destroyed.

### 5.4 Depth modifier
For orientation in fewer round-trips, a depth suffix trades tokens for round-trips,
capped by a token budget so it degrades gracefully:

| call        | returns                                                    |
|-------------|------------------------------------------------------------|
| `r:path`    | surface (depth 0)                                          |
| `r:path:1`  | inline parent notes + link **labels** (bodies collapsed)   |
| `r:path:2`  | also expand link/concept bodies one level                  |

Folder reads navigate down: `r:folder/` returns the folder's own notes + a **child
inventory** (which subpaths carry notes, counts by subject), not the child notes.

### 5.5 Context search (`s`)
`s:path:t1|t2` searches the file's **entire read-context** (its own notes + all parent
folder notes + linked concepts), returning matches as handles + snippets. This is the
escape hatch when a file sits under many arch notes: grep the assembled context
instead of expanding every parent. Terms combine with `|` = **OR** and `+` = **AND**;
`+` binds tighter, so `a+b|c` reads as `(a AND b) OR c` — an entry matches if any
OR-group has all its `+`-joined terms present.

```
▸s:db/schema.rb:tenant|isolation
#a3f9c1 c db/schema.rb …single-table inheritance for tenants… (+1 more)
#f22e90 a api/ …enforces tenant scoping…
2 matches · searched 5 entries in context
```

> **Decision:** `AND` (`+`) is in for v1, as above. **Whole-store search** (`s::term`
> across everything, ignoring context scope) was dropped under the companion model;
> the single global store makes it cheap to express, but it is **deferred**
> (2026-07-20, Q14 → §15). For v1, `s` is context-scoped.

### 5.6 Ranking
When more fits than the budget allows, order by **proximity** (closer ancestors first)
→ **flag state** (unflagged before ⚠-flagged; added 2026-07-21, Q4) → **subject
priority** (arch first for orientation). That is the whole v1 ranking — three keys,
fully deterministic, no tuning.

> **Decision:** v1 ranks on **proximity then subject priority only** — amended
> 2026-07-21 by the binary flag demotion below. Recency and the usage-stat signals
> (expansion rate, usefulness ratio) are *collected* (§7) but kept **out of the
> ranking** until there's data to weight them against — a scoring function with
> weights is deferred (§15) rather than guessed now.
>
> **Amendment — flag demotion (2026-07-21, part of Q4).** Flagged entries (⚠stale ·
> ⚠disputed · ⚠unconfirmed · ⚠split) sink below unflagged ones **within their proximity
> group**; subject priority still orders inside each band. A flag is binary, so this
> is a third deterministic sort key, not a scoring function — the weighted-score
> deferral above stands, and usage-signal demotion stays with it (§15). Readers see
> healthy notes first regardless of store debt; part of the Q4 soft-pressure
> package (§9.6).

---

## 6. CRUD & the Append-Only Log

### 6.1 create / supersede / tombstone / keep
To keep the store a deterministic union, `u`/`d`/`k` never mutate in place. They
append records against the logical entry; current state is a fold of that entry's
records:
- `a` → append a **create** (`CR`) record — mints a new id/handle, stamps the anchors
  (content key / path key + origin, §8.3),
- `u:#handle:body` → append a **supersede** (`SU`) record — same handle, new
  revision, re-stamps the anchors,
- `d:#handle` → append a **tombstone** (`TB`) record — handle resolves to deleted,
- `k:#handle[:path]` → append a **re-anchor** (`RA`) record — re-stamps the anchors
  (current blob / resolved path + current HEAD), **no new body revision** (§9.5), so
  churn stats stay clean. The optional path names the new home explicitly when dm
  could not pick one — the split case (§9.3).

### 6.2 Handle stability
The handle addresses the **logical entry**, not a revision or a position, so it is
stable through supersedes, tombstones, moves, and syncs. `#a3f9c1` always means the
same note.

### 6.3 Cleanup via CRUD
There is **no dedicated dedup / duplicate signal**. If the agent finds redundant
notes, it collapses them with plain CRUD (`u` to fold content into one, `d` to
tombstone the other).

### 6.4 Links
Links follow the same append-only discipline and are the **only** way to relate
entries:
- `al:#a:#b[:note]` → append a **link** (`LN`) record — directed `#a → #b`, optional
  comment,
- `dl:#a:#b` → append an **unlink** (`UL`) record — the pair's current state is LWW.
Links are never edited inside a body; back-links are the inverse index, computed on
read (index: the cache store index, §8.2) — so a link surfaces identically at both
endpoints and survives supersede/move/sync.

### 6.5 Manual moves (`mv`) and node removal (`rm`)
Read-time resolution follows most moves automatically (§9.1–§9.3). `mv:old:new` is
the **manual override** for the residue resolution can't infer — move + heavy rewrite
below the similarity threshold, splits, ambiguous folder scatters — and the way to
act on worklisted orphans. Folders are recursive. `rm:path` tombstones every entry
homed at a path (recursive for folders): the node-level counterpart to `d:#handle`,
for paths whose knowledge is genuinely gone. Both remain ordinary batch commands.

> **Decision — `mv`/`rm` are write-time macros (2026-07-21, resolves Q7).** Neither
> verb has a record type of its own; both expand at write time into records that
> already exist. `mv:old:new` appends one `RA` per **visible** entry homed at `old`
> (recursive for folders): file notes get a fresh anchor — `git hash-object` of the
> working-tree file at the mapped destination, origin = HEAD now — so the entry is
> re-stamped to current reality; folder notes get their path-key anchor rewritten by
> prefix substitution. If the destination file is absent from the working tree, that
> expansion fails as a **per-entry error** in batch output (`mv` describes a move
> that happened; it does not invent one). `rm:path` expands to one `TB` per visible
> entry, recursive likewise. Entries not yet visible (e.g. pending on an unmerged
> branch) are untouched — when they land they surface as residue and get their own
> fix; that is the uniform drift path, not a gap. Path-scoped redirect records were
> rejected: redirects are rules that outlive their evidence (chaining, cycles,
> precedence against automatic resolution, landmines for future entries at reused
> paths) — the same redirect-map machinery §13 already discarded.

---

## 7. Usage Statistics & Feedback

**Decision:** stats are **merged**, not local telemetry: counters are CRDTs, so they
union like everything else and travel with the store. Reads therefore mutate state;
the cost is accepted in exchange for stats that can rank *and* are shared. Stat
deltas accumulate in pending and ride the next `dm sync` (§8.4) — a read-only session
creates no obligation and loses nothing by not syncing. Explicit feedback rides the
record log as `FB` records (§7.3). The stats' physical home is **one stats file per
replica** in the store tree (`stats/<replica-id>` — resolved 2026-07-20, Q6, §8.1):
each replica writes only its own file, so union is conflict-free and
max-per-replica is trivial.

### 7.1 What's tracked (per logical entry)
| stat                 | signal                                                  |
|----------------------|---------------------------------------------------------|
| **impressions** (`i`)| times shown *collapsed* in a surface (weak)             |
| **expansions** (`x`) | times *opened* via `r:#handle` (strong — the real click)|
| **last-expanded**(`t`)| recency of genuine use                                 |
| **search-hits** (`h`)| surfaced as a match by `s` (distinct from impressions)  |
| **revisions / churn**| volatile vs settled knowledge                           |
| **feedback** (`+ - !`)| explicit agent signal (§7.3)                           |

The impression-vs-expansion split is core: surfacing is cheap, *opening* is the
click. Two more distinctions: **expansions** (`x`) is a *count* (how often opened)
while **last-expanded** (`t`) is a *timestamp* (how recently) — frequency vs recency,
both needed for ranking. And **revisions / churn** is **derived from the record log**,
not a stored counter: it is the count of `SU` records on the entry (`CR` = rev 1,
each `SU` +1). `RA` and `TB` are *not* revisions — only `SU` — so re-anchoring via
`k` never inflates churn, and because it folds from the unioned log it can't
double-count on sync.

> **Decision:** `search-hits` is a **distinct counter**, bumped when `s` surfaces an
> entry as a match — *not* folded into impressions, and with **no attempt to
> correlate a search with a later expansion**. That correlation would span two
> separate `dm` invocations (search now, `r:#handle` later) with nothing tying them
> together, so it's not tracked. A search match and a digest impression are simply
> different events.

### 7.2 Merge semantics
- One stat row per logical entry, keyed by full entry-id ULID (§8.3).
- Each counter (`i`, `x`, `h`) is a **G-Counter**: `A3:12,F2:4` = replica A3→12,
  F2→4, total 16. Missing field = zero. Merge = union replicas, **max per replica**,
  sum for value.
- `t` is a last-write-wins timestamp register: merge = **max** timestamp.
- Feedback tallies are **not** counters — they fold from `FB` records (§7.3), which
  is what lets a dispute be cleared (a G-Counter could only ever grow).

Physically, the rows live in per-replica stats files (§8.1): one row per entry
inside `stats/<replica-id>`. The two operations act on different axes — **max**
reconciles two *copies of the same replica's file* (cells are monotonic, so a stale
snapshot never double-counts); **sum** aggregates cells *across* replica files to
display the value. Replica A at 2 and replica B at 3 always total 5.

### 7.3 Feedback interface (`f`)
```
f:#handle:<signal>[:reason]
```
| signal | meaning              | effect                                        |
|--------|----------------------|-----------------------------------------------|
| `+`    | useful               | recorded; feeds weighted ranking when it lands (§15) |
| `-`    | not useful here      | recorded; feeds weighted ranking when it lands (§15) |
| `!`    | wrong / outdated     | flips surface flag to `⚠disputed`, queues for housekeeping |

Optional `reason` is a short string stored with the feedback — invaluable for a later
editor (`f:#f22e90:!:repo/ layer was removed in v3`). Feedback is a **log record**
(`FB`, §8.3), not a counter: it carries the reason, unions like every other record,
and rides pending → sync like content. The per-entry `+`/`-`/`!` tallies are
*derived* by folding `FB` records, like churn. `⚠disputed` is defined as an `FB !`
record **newer (rec-id order) than the entry's latest `SU`/`RA`** — so a dispute is
cleared exactly by the housekeeping resolutions, `u` (rewrite) or `k` (re-bless),
each of which outranks the old complaint; a still-standing complaint re-flags by
being re-filed. Implicit signals still count (expansion = weak +, ignore = weak −);
`f` is the explicit, high-weight channel.

### 7.4 How stats feed retrieval & surface flags
- **Ranking** gains usefulness ratio + expansion rate once the weighted scoring
  function lands (§15); v1 collects but does not rank on them (§5.6).
- **Surface flags** stay token-disciplined: only *behavior-changing* signals are
  shown — `⚠disputed` (from `!`), `⚠stale` (content drift, §9.5), and `⚠unconfirmed`
  (uncertain follow, §9.3). Positive signal is expressed silently through ordering,
  not badges.
- **Housekeeping** (data supports it now; the agent-facing report is the worklist,
  §9.6): never-expanded notes past an age, disputed notes, high-churn notes, drifted
  notes → a prune/review worklist.

---

## 8. Storage & Sync

Requirements-first (§2): persist only what the visibility rule forces, compute
everything else at read time. A note must durably carry three facts — its text, a
content fingerprint for rule (a), an origin commit for rule (b) — and row 14 forces a
shared, conflict-free, append-only note set. Everything beyond that floor is either a
memoized query result (verdicts, §9.4) or a disposable cache.

### 8.1 The store — one metadata branch

> **Decision (2026-07-19):** one shared metadata branch, **`refs/dm/store`**, chosen
> over git-bug-style per-note operation DAGs: one ref beats thousands (simpler sync,
> compaction, and forge story), at the price of keeping the wall-clock caveat on
> ULID-ordered LWW folds — mitigable later via Lamport counters in the record headers
> without changing transport (§15).

- **Append-only records**, one ULID-named file per record, merged by **CRDT union**:
  distinct filenames never collide, identical records dedup — a sync can never
  conflict. (Physical layout: the decision block below.)
- The store's commit tree also carries the **anchored blobs**, keeping described
  content fetchable forever (no reflog dependence). Git dedups blobs that any commit
  already contains, so the overhead is only truly uncommitted content that was
  described mid-session.
- `dm init` configures the `refs/dm/*` fetch refspec. Custom refs push/fetch fine
  through GitHub/GitLab (git-appraise / git-annex precedent); a plain
  `refs/heads/dm-store` branch is the fallback if a forge misbehaves (forks copy only
  heads and tags).

> **Decision — store tree layout (2026-07-20, resolves Q6).** Three top-level dirs
> plus one marker file:
>
> ```
> records/<t4>/<rec-id>    # one file per record, sharded by the rec-id's first
>                          # 4 chars (≈12-day ULID-time windows)
> stats/<replica-id>       # one file per replica: "<entry-id> i:<n> x:<n> h:<n> t:<ms>" rows
> blobs/<2ch>/<38ch>       # anchored blobs, content-addressed like git's odb
> epoch                    # compaction generation counter (§8.7; added 2026-07-21, Q8)
> ```
>
> - **Records** shard by time prefix so tree objects stay small and old shards
>   *freeze* — stable trees delta and pack well, where a flat dir would rewrite one
>   giant tree object every sync. Self-contained: the shard is derived from the id.
> - **Stats** — one file per replica, one row per entry that has stats; each replica
>   writes **only its own file**. Merging two copies of the same replica's file is
>   field-wise **max**; the displayed value **sums** across replica files (§7.2).
>   Chosen over per-entry-per-replica files (entries × replicas file explosion) and
>   over stat-increment records (every impression would append forever — the
>   register file *is* the compacted form). Cadence: deltas accumulate in
>   `.git/.dm/pending/stats` under the Q13 lock; `dm sync` folds them in (add
>   counters, max `t`); reads merge store + pending accumulator.
> - **Blobs** — at `a`/`u`/`k` time, an anchored blob the odb doesn't already have
>   (truly uncommitted content) is staged to `.git/.dm/pending/blobs/<sha>` —
>   deliberately *not* `git hash-object -w`, since a loose unreferenced object can
>   be GC'd before the next sync. Sync writes it into the store tree, after which
>   store commits keep it reachable forever. A record's `<anchor>` field is the blob
>   SHA, so record → blob is a direct address; blob → entries is the store index
>   (§8.2).

### 8.2 Clone-local state — `.git/.dm/`
- `.git/.dm/replica` — the per-clone replica id + config (§8.5).
- `.git/.dm/pending/` — records not yet committed to the store; folded into a store
  commit by `dm sync`. Reads always see **store ∪ pending** — which is why a fresh
  note is visible immediately (§2.1 row 7) with no commit or sync step.
- `.git/.dm/cache/` — memoized read-time resolution results (blob→records, similarity
  matches). Disposable; never load-bearing: re-running the diff reproduces every
  answer on any clone, so there is no resolved-state to replicate, corrupt, or
  migrate.

> **Decision — cache mechanism (2026-07-20, resolves Q11).** Content-keyed,
> **immutable** cache files, stdlib-only — no embedded database: the cache is
> write-once/read-many (rebuilt wholesale, never mutated in place), which is exactly
> the case where a KV store's transactional mutation buys nothing. The **store
> index** (`index-<store-tip>`) memoizes the store-wide maps every read needs:
> entry-ids sorted by random tail (handle → entry by prefix range), entry-id → its
> record files, link source/target → `LN`/`UL` records, and anchor lookups
> (blob-sha → entries, path-hint → entries). The filename is the key, so **presence
> = validity** — no invalidation logic: the tip only moves at `dm sync` (which
> rebuilds eagerly as its last step), a read finding no matching file rebuilds with
> one full scan, and non-matching files are GC'd opportunistically. Files are built
> in a temp file and renamed into place: atomic, and race-safe without locks —
> concurrent rebuilds produce byte-identical content, so last-rename-wins is
> harmless (Q13 concerns pending only). A versioned header (magic + format version)
> guards format changes; any mismatch or parse error → delete and rebuild. Format:
> length-prefixed binary (`encoding/binary`; the fields are mostly fixed-width
> ULIDs/SHAs), loaded wholesale into maps per invocation — ~1–2 MB at 10k entries,
> single-digit milliseconds, amortized across the batch. **Pending is never
> indexed**: it is scanned linearly, per-session small by construction. Similarity
> memos follow the same pattern (`match-<origin>-<checkout-tree>`). Pre-paid upgrade
> paths if Q2 profiling demands them: mmap'd sorted fixed-width arrays (git
> pack-`.idx` style) and incremental chaining from the prior index (the store is
> append-only under union merge, so a new tip's record set is a superset).

Living inside the git dir means git never walks into it from any working tree, it is
invisible to `git status` with no exclude entry, and it is per-clone, shared across
all code worktrees. There is no checkout binding and nothing to keep aligned — `dm`
serves whatever HEAD and working tree it finds.

> **Decision — intra-clone locking (2026-07-20, resolves Q13).** One advisory
> exclusive lock, `.git/.dm/lock`, taken **for the duration of every invocation** —
> reads write too (stat deltas, verdicts), so a shared/read mode isn't worth having.
> `flock(2)`-style locking (`LockFileEx` on Windows), not an `O_EXCL` sentinel file:
> the lock dies with the process, so a crash can never leave the git-`index.lock`
> stale-lock failure mode. Contenders **block** briefly (batches run in
> milliseconds), then error: `✗ another dm process holds .git/.dm/lock (pid N)`. The
> lock also restores cross-process rec-id order: on acquisition, monotonic minting
> resumes from the largest rec-id already in pending when the clock hasn't advanced
> past it, so two serialized same-millisecond processes can never fold out of order.
> Nothing else needs locking: the cache is race-safe by construction (above), and
> the store merges by union (§8.4).

### 8.3 Record schema
Every record's first two fields are its **type** and its **record-id**; the rest are
type-specific, body last:
- `CR <rec-id> <entry-id> <subj> <anchor> <origin> <path> <body>` — create.
  `<anchor>` is the content key (file note) or path key (folder note); `<origin>` the
  HEAD commit at write time; `<path>` the display/path hint (for folder notes it
  equals the anchor).
- `SU <rec-id> <entry-id> <subj> <anchor> <origin> <path> <body>` — supersede;
  re-stamps the anchors.
- `TB <rec-id> <entry-id>` — tombstone.
- `RA <rec-id> <entry-id> <anchor> <origin> <path>` — re-anchor only (§9.5), no body.
- `LN <rec-id> <from-id> <to-id> [comment]` — link, with optional comment.
- `UL <rec-id> <from-id> <to-id>` — unlink.
- `FB <rec-id> <entry-id> <sig> [reason]` — feedback: `sig` ∈ `+`/`-`/`!`, optional
  free-text reason (body-last rule as usual, §7.3).
- `VD <rec-id> <origin-commit> <verdict> [<landed-as>]` — memoized rule-(b) verdict
  (§9.4): `landed <commit>` or `abandoned`; fold rule in §9.4.

> **Decision — id fields hold full ULIDs (2026-07-20, resolves Q10).** Every
> id-valued field above (`<entry-id>`, `<from-id>`, `<to-id>`) holds the **full
> 26-char entry-id ULID**, and stat rows key on the same (§7.2). Handles never
> appear in stored records: they are a surface affordance, resolved to entry-ids at
> parse time against the visible set (§5.3). A cross-replica handle collision is
> therefore an input-ambiguity error (§3.5), never record interleaving.

There is **no `MV` record type**: the companion model's redirect-map machinery is
gone — moves are *derived* at read time (§9.1–§9.3), and the manual `mv`/`rm` verbs
are write-time macros over `RA`/`TB` (decision in §6.5, resolves Q7).

> **Decision — serialization.** Fields are separated by a **single space** (never
> cosmetic alignment padding). Every record's fixed fields come first; the parser
> splits on the leading spaces and takes the **rest of the record as the body** — the
> same "body is the trailing field" rule as stdin (§4.1). Therefore `:`, spaces, and
> backticks inside a body need **no escaping**. Multi-line bodies use the same
> trailing-`\` continuation as stdin; the only escape is a literal trailing
> backslash, doubled `\\`. **Path fields (2026-07-20, resolves Q6's encoding half /
> review nit E3):** path-valued fixed fields (`<path>`, folder anchors) are
> **canonically percent-encoded** — exactly `%` → `%25`, space → `%20`, and C0
> bytes → `%XX`; every other byte raw. Canonical (always this set, never more) is
> load-bearing: union dedup is byte-identity, so any path must have exactly one
> encoding. Bodies are untouched (body-last).

**Decision — record identity & ordering.** `<rec-id>` is a **ULID minted per record**
(distinct from `<entry-id>`, the logical entry's ULID). One field does three jobs:
- **union merge** — the ULID is globally unique, so merging two record sets is a
  set-union keyed by `<rec-id>`; identical records dedup, distinct ones all survive.
- **LWW timestamp** — a ULID embeds its 48-bit creation time, so the record *is* its
  own write-time; no separate timestamp field. Superseding an entry or re-anchoring
  resolves to the record with the **largest `<rec-id>`** for that entry.
- **deterministic tiebreak** — two records minted in the same millisecond order by
  the ULID's random tail. Every replica compares the same stored ULIDs and picks the
  same winner regardless of merge order, so no explicit replica-id is needed in the
  records (replica-id lives only in the stat G-Counters, §8.5).

Two refinements make the order trustworthy:

- **Monotonic minting — rec-ids only (2026-07-20, resolves Q9).** `dm` mints
  **rec-ids** in the spec's *monotonic mode* per process: within the same
  millisecond, each successive id increments the previous random tail instead of
  re-rolling it. This guarantees a batch's records order as written — an `a`
  followed by a `u` of the same entry in one batch can never fold backwards.
  **Entry-ids are exempt**: they mint with fresh randomness per id — monotonicity is
  only needed where ordering is load-bearing, and entry-ids play no ordering role —
  so same-millisecond entries share no tail prefix and the §3.5 handle derivation
  keeps its full 30 bits. (Same-batch backreference: resolved via `$N` — §4.2.)
- **Clock skew — accepted.** rec-id order is wall-clock order, so a clone with a fast
  clock wins concurrent LWW resolutions until its skew is corrected. Accepted for v1
  and on the record here: resolution stays deterministic and nothing is destroyed —
  losers remain in the log as sibling revisions — and NTP-sane clocks make the window
  irrelevant in practice. (Lamport counters can retire the caveat later, §15.)

Tests pin the clock (ULID time bits) and the seeded id source (random tail), so every
LWW outcome is reproducible (§11).

**Decision — write-time anchoring** (replaces the companion model's stamp-at-commit).
Anchors are stamped **at write time** from the working tree — `git hash-object` of
the file exactly as the agent sees it. No placeholder, no later stamping pass, no
window in which a note asserts freshness against bytes it never described (this is
what dissolved review finding A5). The current anchor folds as a **LWW register**
from the entry's `CR`/`SU`/`RA` records (largest `<rec-id>`). It is re-stamped
**only** by a deliberate write — `a`, `u`, or `k` — never by sync, which would
re-bless a note as fresh without anyone confirming it still matches the code.

**Decision — concurrent supersede.** When two replicas supersede the same entry
concurrently, the current body resolves **last-write-wins** by the winning `SU`
record's `<rec-id>` (the ULID total order above), consistent with anchor resolution.
Both `SU` records survive in the log — the loser becomes a sibling revision, never a
conflict — so nothing is destroyed and churn stats stay accurate. No keep-both fork
is surfaced; the fold always yields one clean current body.

**Decision — tombstone is terminal.** `TB` is absorbing, not a LWW participant: once
any `TB` record exists for an entry, the entry folds to deleted — a concurrent `SU`
loses regardless of rec-id order (its record survives in the log, as always, but
resolves nothing). There is no resurrection: `u`/`k`/`f`/`al` against a tombstoned
handle fail at write time (`✗ #a3f9c1 deleted`); knowledge worth restoring is
re-created with `a`, minting a fresh entry.

### 8.4 `dm sync` — the one git-facing verb

The companion model's outer loop (checkout / merge / commit / push / fetch / unmerged
/ prune) collapses to a single verb:

> **`dm sync`** = fold pending into a store commit → fetch `refs/dm/store` → CRDT
> union merge (set-union of records; stat counters merge per §7.2) → push, with a
> fetch-merge-retry loop on a non-fast-forward race.

Properties:
- **Never conflicts** — union of ULID-named records plus CRDT counters; the retry
  loop converges because each retry only ever adds.
- **Never required for local work** — reads see store ∪ pending; a session that
  never syncs still has full function. Sync is when knowledge is *shared*, not when
  it becomes real.
- **Convergence is eventual, not immediate** — two clones can transiently disagree on
  rule (b) when one has fetched an origin commit the other lacks, until the first
  memoized verdict record lands in the store (§9.4).
- **Ends with a health line** (added 2026-07-21, Q4) — one summary line of worklist
  counts, session-scoped then global (`3 stale · 1 orphaned near your work · 41
  elsewhere`): the backlog is surfaced at every share point, never demanded (§9.6).

> **Decision — epoch rule and push-clears-pending (2026-07-21, part of Q8).** Naive
> set-union cannot unlearn: a compacted-away record would be re-added by the first
> sync from any replica still holding the old tree. Two amendments make removal
> stick:
>
> 1. **Epoch rule.** The tip tree carries an `epoch` generation counter (§8.1),
>    bumped only by compaction (§8.7). If a fetched tip's epoch is **newer** than the
>    local store's, the replica adopts the fetched tree wholesale and re-contributes
>    only its **own not-yet-pushed records** — it never re-uploads records it merely
>    received, since anything the newer-epoch tip lacks was removed deliberately.
>    Same epoch → plain union as above.
> 2. **Pending clears only on successful push.** Folding pending into a candidate
>    store commit does not consume it; only a push that lands does. So when a retry
>    loses a race (to another sync or to a compaction), the replay always knows
>    exactly what "own unpushed records" means.

**Persistence table.** Rows mirror the requirements table in §2.1; "derived" = no
write, computed at read time from code-repo state + store ∪ pending.

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
| 14 | teammate clones/pulls the same state | `dm sync` on each side; distinct ULID filenames ⇒ conflict-free union. Identical view is **derived** — same store + same refs ⇒ same answers (asterisk: transient divergence until a needed verdict record exists) |
| 15 | detached HEAD / bisect at old commit | **derived** — same queries against the detached tree and its ancestry. Writes (if any) persist as row 2, origin = the detached commit |

Durable writes happen at exactly three moments: **note records at write time**
(rows 2/3/4/7/15), **verdict records at first-read-after-history-rewrite**
(rows 5/8-b/9-squash/13), **store commits at sync** (row 14). Rows 6, 10, 11, 12 are
pure queries. There is no per-clone mutable state that could make two clones disagree
except pending, which is by definition not yet shared.

**What the model pays** (accepted, on the record):
- **Read-time cost moves up**: similarity matching per unresolved note at read
  instead of once at a reconciliation gate; mitigated by the disposable cache, but
  large-repo performance gets sharper — **Q2** (§14).
- **Convergence is eventual** — the row-14 asterisk above.
- **Store growth**: anchored blobs ride in the store tree; overhead is only truly
  uncommitted content (§8.1). Reclaimed by `dm gc` (§8.7).
- **No forcing function for hygiene**: the worklist replaces the companion model's
  hard gate; pressure is supplied by the Q4 soft layers (§9.6), never by a gate.

### 8.5 Replica identity
G-Counters need a stable per-clone **replica id**, kept in `.git/.dm/replica` (one
per clone, never tracked, §8.2) so it never merges. A wide random value (8 base32
chars from a CSPRNG at `dm init`) so collisions never need coordinating.

> **Decision:** spend the bytes rather than fight collisions. The replica id is a
> wide random value (not derived from `user.email`) so collision across any realistic
> team is negligible and needs no registry or coordination. The cost is a few extra
> bytes per populated G-Counter cell, which is cheap next to the note bodies and
> compresses well in git. One caveat: **never copy a clone** — `cp -r` duplicates
> `.git/.dm/replica`, and two live clones sharing a replica id lose increments under
> the max-per-replica merge (§7.2). A copied clone must delete `.git/.dm/replica` (or
> re-run `dm init`) so a fresh id is minted. (Test fixtures copy clones deliberately,
> with pinned replica ids — §11.2.)

### 8.6 Worked example — one note, write to read

On `main` at HEAD `41c09d7`, the working tree contains `api/handler.rb` whose bytes
hash to blob `9f4e2ab`. The agent runs:

```
a:api/handler.rb:c:Validates tenant header before dispatch
```

**Write.** Under the invocation lock, `a` stamps both anchors (blob `9f4e2ab` via
`hash-object`, bytes staged to `pending/blobs/` only if the odb lacks them; origin
`41c09d7` = HEAD), mints entry-id `01K0N3AB4XA3F9C1TQ84MZV2R7` (fresh random tail →
handle `#a3f9c1`) and rec-id `01K0N3AB4X8QF3ZJ4WT2P9K6H1` (monotonic), and appends
exactly one file, `.git/.dm/pending/records/01K0N3AB4X8QF3ZJ4WT2P9K6H1`:

```
CR 01K0N3AB4X8QF3ZJ4WT2P9K6H1 01K0N3AB4XA3F9C1TQ84MZV2R7 c 9f4e2ab 41c09d7 api/handler.rb Validates tenant header before dispatch
```

Ack: `+ #a3f9c1 created`. No store commit, no ref touched, no stat row. Every field
has exactly one later consumer: rec-id → fold/LWW order · entry-id → identity
(handle, links, stats key) · subj → ranking/display · anchor → rule (a) · origin →
rule (b) · path → display + resolution hint · body → what `r` shows. A later
`dm sync` moves the record to `records/01K0/…K6H1`, any staged blob to
`blobs/9f/…`, and rebuilds the index for the new tip.

**Read** — `r:api/handler.rb`, any clone, before or after sync (reads see store ∪
pending). Load the tip's index and scan pending; hash the working file → `9f4e2ab`
→ blob-sha→entries map → this entry (path-hint lookups on the ancestors surface
folder/arch notes). Rule (a): anchored blob present at the hint path → fresh. Rule
(b): `41c09d7` is an ancestor of HEAD → visible. Fold entry-id→records
(`[CR …K6H1]`) → rev 1, that body and anchor current, no dispute. Assemble the
surface and count the impression (`i+1` in `pending/stats`):

```
▸r:api/handler.rb
c #a3f9c1 Validates tenant header before dispatch
context: 1 own · 0 parent · 0 links
◾1 ok
```

A follow-up `r:#a3f9c1` binary-searches the tail-sorted entry array for prefix
`A3F9C1`, returns the full body + links one level, and counts the click (`x+1`,
`t=now`). The five states are never stored — rules (a)/(b) recompute them on every
read.

### 8.7 Compaction — `dm gc`

> **Decision (2026-07-21, resolves Q8).** The store's *history* carries no
> information — records are self-timestamped, only the tip tree is data — so
> compaction is an **orphan re-commit** of a slimmed tip tree with `epoch` bumped
> by one, pushed under the same non-ff fetch-retry loop as sync. Any replica may
> run it; there is no coordinator. The trigger is **manual `dm gc` only** (v1); the
> sweep is a deterministic mark-and-sweep with a canonical tree build, so two
> concurrent compactors over the same tip produce the same tree SHA and the race
> is benign. A normal sync landing mid-compaction is absorbed by the retry loop:
> refetch, re-sweep, re-push.
>
> **What a sweep keeps / drops:**
>
> - **Tombstones**: the `TB` line itself is kept **forever** — one short line, the
>   permanent suppressor that keeps a late resurrection dead. Everything it
>   suppresses (the entry's `CR`/`SU` bodies, `LN`/`UL`/`FB`, stat rows, anchored
>   blobs) is reclaimed immediately at the next sweep.
> - **Shadowed revisions** (`CR`/`SU` hidden behind a later `SU`): dropped when
>   older than **3 months** (rec-id ULID age). Within the window they stay, so the
>   derived churn count (§7.1) becomes **recency-scoped** — deliberate: recent
>   churn is the volatility signal; ancient churn on a since-stable note is noise.
> - **Verdicts**: a `VD` is droppable **iff no surviving record's origin field
>   matches it**. Landed verdicts are irreplaceable evidence once the origin commit
>   is GC'd (patch-ids can no longer be computed), so they live as long as any
>   record needs them. Abandoned verdicts are re-derivable in principle (absence of
>   evidence re-derives), but the same referential rule keeps them as cheap
>   memoization; their lifetime is bounded by their referencing records — see the
>   lifecycle note below.
> - **Blobs**: mark-and-sweep from surviving records' anchor fields; unreferenced
>   `blobs/<sha>` files drop.
> - **Stat rows** keyed to purged entries drop from each `stats/<replica-id>` file;
>   a racing sync may transiently resurrect rows (field-wise max is monotone —
>   harmless), and the next sweep removes them again.
>
> **Safety invariant — dangling ids are ignorable.** The compactor can never see
> other replicas' pending, so a late-landing record may reference a purged entry
> (`SU` on it, `LN` into it, stat rows). Defined semantics: dangling references are
> **ignored at read**, and headless records are themselves garbage for the next
> sweep. Every droppability rule above must keep the store a valid CRDT state under
> late union of any replica's pending — this invariant is what makes uncoordinated
> compaction sound. Reintroduction by stale full copies is prevented separately by
> the epoch rule (§8.4).
>
> **Abandoned-verdict lifecycle.** An abandoned `VD` is minted at first read that
> finds an origin unreachable and unlanded (§8.4 row 13); it pins nothing open
> indefinitely. It lives exactly as long as records carrying that origin: the
> worklisted entry is eventually tombstoned (`d` → records purge at next sweep),
> re-anchored (`k`/`u` → the old origin survives only in shadowed revisions, which
> age out in 3 months), or deliberately kept — in which case keeping its one-line
> memo is correct, since it is what spares every read the unreachability scan.
> `TB` lines kept forever pin no verdicts: `TB` has no origin field.

---

## 9. Read-Time Resolution

With both anchors stored, resolution is a **two-point comparison**: the note's
write-time state (anchored blob / path key, origin commit) against the current
checkout. No history walk is load-bearing — git's diff machinery between origin and
checkout supplies moves, copies, and similarity pairing on demand. Both halves of the
visibility rule are two-point git queries at read time; the five states of §2 are
never stored — they are classifications.

### 9.1 Rule (a) — is the content present? (file notes)

1. **Exact** — anchored blob in the tree at the path hint → visible, fresh.
2. **Moved** — anchored blob in the tree at another path → visible there, fresh
   (pure rename/extract — the case path-keying can never handle: extract-file
   refactors carry their notes).
3. **Edited** — blob absent; similarity-match it against the checkout (§9.2) →
   visible at the matched path, flagged `⚠stale`. Staleness is gradable (similarity
   score) — keep the binary flag for v1.
4. **Unresolved** — no match → orphaned; hygiene worklist (§9.6). A *visibility*
   fact, never a gate demanding tombstones.

Ambiguity policy (dm detects, agent decides): multiple candidates → prefer the path
hint, flag the rest; `k`/`u` settles it.

### 9.2 Move-with-edit — git rename detection

Git stores no renames; `git mv` + edit and `rm`/`add` + edit are identical in
history. Rename-ness is inferred at diff time by content similarity (`-M`, ~50%
default threshold). This powers resolution layer 3:

- A move-with-edit leaves the anchored blob `B` at `old/path` in the origin tree and
  `B' ≠ B` at `new/path` in the checkout; one similarity diff origin→checkout pairs
  them. The note surfaces at `new/path` flagged `⚠stale` — correct, since it
  described the pre-edit content; `k` re-blesses and re-anchors to `B'`.
- **dm can beat stock git detection**: it only cares about noted paths, so it can
  afford a lower similarity threshold plus copy detection (`-C`) without whole-tree
  false-positive risk, and can score candidates against the note's anchored blob
  directly.
- **Acceptance is banded, with a uniqueness margin**: ≥ ~80% similarity to the
  anchored blob *and* the best candidate beating the runner-up by ≥ ~20 points →
  auto-accept (`⚠stale`); ~50–80%, or a high score with a close runner-up → follow
  flagged "resolved-by-inference, unconfirmed" for bulk blessing; below ~50% →
  unresolved. The margin is the load-bearing part: false pairings come from clusters
  of near-identical candidates (boilerplate, generated code, fixtures), not from a
  lone wrong file scoring high in isolation. Thin margins tie-break on path-hint
  affinity (same basename, directory distance). The band numbers are starting
  guesses — Q1's replay harness doubles as the calibration rig (§11.4).
- **Nothing persisted**: matches land in the disposable cache; re-running the diff
  reproduces the answer on any clone.

**Residue handed to the agent** (worklist, never a hard gate): move + heavy rewrite
below threshold (indistinguishable from delete-and-create — `mv` survives as a verb
for exactly this, §6.5); file splits with both sides heavily edited (copy detection
catches at most one side; ambiguity policy applies); true deletions.

Net effect: rename detection turns the companion model's mandatory, every-branch,
hard-gated manual reconciliation into an automated common case with an agent-reviewed
remainder.

### 9.3 Folder notes — path as key

(Behavioral requirements: §2.2.)

Folder note = text · **path key** · origin commit. A folder has no stable blob to
key on — hashing the path is still a path key, just unreadable, and the tree SHA is
globally volatile by construction (any edit to any file under the folder changes
it). So the path itself is the key, and the **origin commit supplies the matching
fingerprint** that the anchored blob supplies for files: the origin's tree at the
noted path — its tree SHA and member set at write time.

Resolution is the same two-point comparison, origin → checkout:

1. **Exact** — the path exists in the checkout → visible, fresh. Member churn
   doesn't matter: a folder note describes the place, not the contents.
2. **Moved, pure** — path absent, but the origin's tree SHA at that path appears
   elsewhere in the checkout → follow there, fresh (`git mv api/ svc/` with no
   member edits preserves the tree SHA by construction).
3. **Moved with churn** — no identical tree; aggregate the file-level rename
   detection already computed for file notes (§9.2): where did the folder's member
   files go? Majority prefix mapping (`api/* → svc/*`) → follow, flagged below full
   agreement. Files pair blobs by similarity; folders aggregate those pairs and vote
   by prefix.
4. **Unresolved** — members appear in the diff as deletions (no rename pairs) →
   folder deleted → orphaned, worklist. Scattered with no majority prefix →
   ambiguity, agent decides (same policy as multi-candidate file matches).

**Follow heuristic**: the rank-3 vote auto-follows only when three checks pass — a
**majority of paired members** agrees on one target prefix; the winner beats the
runner-up prefix by a clear **margin** (6/10 to `svc/` with the other 4 deleted is a
rename; 6/10 to `api-core/` with 4 in `api-http/` is a split); and the pairs
**cover** a minimum fraction of the original member set (a 2-member folder with one
pair and one deletion is 100% of pairs but 50% coverage — too thin to auto-follow).
Unanimous with full coverage → fresh; passing below that → flagged; no winner →
ambiguity. Exact fractions ride the same Q1 calibration harness as the file-level
bands (§9.2).

**Rule (b) is unchanged** — origin ancestry and verdict records apply to folder
notes verbatim; nothing folder-specific.

**Nothing new persisted**: matches land in the disposable cache; `k` re-anchors the
path key to the resolved location exactly as it re-anchors a file note to the
current blob — same verb, same `RA` record, no separate re-path record type. Cost
piggybacks on the same batched origin→checkout diff file notes already need (Q2
unaffected).

Edge cases: **split** (`api/` → `api-core/` + `api-http/`) — two prefixes, no
majority margin → ambiguity, agent picks (duplicating the note is out of scope for
v1); **absorbed** (contents moved into a pre-existing `lib/` that is mostly foreign
content) — the vote may be unanimous, but the target may no longer be what the note
described → follow with an unconfirmed flag regardless of vote strength; **empty
folders** — git cannot represent them, so a folder containing no files has nothing
to diff (inherent limit).

> **Decision — split/absorbed UX (2026-07-21, resolves Q3).** Three parts:
>
> 1. **Splits surface at every candidate, flagged.** While ambiguous, the note
>    appears as a parent note for reads under *each* candidate prefix, marked
>    `⚠split` and demoted like every flag (§5.6). It is wrong at one candidate by
>    construction, but the flag states exactly that uncertainty — and the readers
>    under the candidates are the only agents with the context to resolve it, at
>    the moment they have it. Worklist-only surfacing was rejected: under the Q4
>    soft-pressure model an invisible split could stay unresolved indefinitely.
>    **Scatter** — no candidate at all — is worklist-only by necessity.
> 2. **Evidence rides the worklist and the drill, not the surface.** Surfaces show
>    only the flag; `dm worklist` and `r:#handle` show the vote so judgment is
>    cheap: `#f22e90 a api/ ⚠split → api-core/ 6/10 · api-http/ 4/10 · cov 100%`;
>    absorbed: `→ lib/ ⚠unconfirmed (vote unanimous · target 87% foreign)`. The
>    numbers are the three follow-heuristic checks (majority · margin · coverage) —
>    already computed at resolution, shown instead of re-derived.
> 3. **`k` gains an optional explicit target** — `k:#h:api-core/` re-homes *this
>    entry* to the named candidate: the same `RA` record as plain `k`, which keeps
>    meaning "bless dm's guess" (the absorbed/unconfirmed case). The path is the
>    final field, so `:` in it needs no escaping (Q6 rule). `mv` stays purely
>    path-level (Q7); it cannot express per-entry choices, which mixed splits need.
>    A note that genuinely applies to both halves: `k` it to one home and write a
>    fresh `a` at the other (optionally `al`-linked) — no duplication machinery in
>    v1.

### 9.4 Rule (b) — has the origin landed?

Origin ancestor of HEAD → landed here · else a **landed verdict** exists → landed ·
else origin reachable from another local/remote ref → pending-elsewhere · else
abandoned (verdict appended on first computation).

**Verdict records** memoize the one thing git structurally forgets — what became of
rewritten origin lines: **landed** (origin O landed as commit M — squash/rebase,
matched by patch-id / tree containment) or **abandoned** (origin unreachable from
all refs, unlanded). Appended lazily by whichever clone first computes the answer;
union-synced, so every replica inherits it. Record form: `VD` (§8.3). If one origin
accumulates conflicting verdicts, **landed beats abandoned** — landed is positive
patch-id evidence, abandoned is absence of evidence (exactly the wrong-abandoned
case below) — then largest rec-id among landed.

**Verdict correction**: wrong verdicts are repaired at the note level in v1, with
verbs that already exist. Wrong *abandoned* (notes wrongly worklisted — e.g. the
origin lived on a remote dm couldn't see): `k` from the worklist on the right
checkout — re-anchoring to current blob + HEAD gives the note a fresh origin that
resolves by plain ancestry, no verdict consulted. Wrong *landed* (notes wrongly
surfacing on a line their origin never reached): `k` would bless them there —
instead `d` here and re-add on the correct branch. Accepted v1 caveats: a verdict is
per-*origin*, so repair is O(notes sharing that origin), and the stale verdict
lingers in the store. LWW-overridable verdict records stay a later optimization for
the many-notes-per-origin case (§15), not a v1 requirement.

### 9.5 Staleness

The write-time anchor is half of every note's identity (§3.1), so staleness is not a
bolt-on check but the gap the anchors measure: on read, the anchored blob is
compared against the tree (rule (a)); any mismatch that still resolves surfaces the
note flagged `⚠stale` (files) or `⚠unconfirmed` (folders, §9.3). Flags are
*display*, not ranking, in v1 (§5.6).

**Decision — granularity.** Whole-file, matching file-level note granularity. A
region/line anchor would be more precise but rots (line ranges shift under edits);
whole-file is robust and cheap. The cost — any edit anywhere in a file flags all its
notes — is absorbed by `k` being a one-liner. **Hunk-level following** (blame-style
line attribution) is explicitly out, not deferred: max fidelity, but it requires
line ranges on notes, which are rot-prone by construction; whole-file blob identity
is the 90% solution.

**Decision — treatment.** A stale note is reconciled by the agent, never by dm, one
of three ways: `k` (still valid → re-anchor), `u` (partly wrong → rewrite,
re-anchors), or `d` (obsolete → tombstone). The worklist (§9.6) surfaces candidates;
nothing gates.

**Accepted limitation.** Blob drift detects that *the file's bytes changed*, not
that *the note became wrong* — the two overlap but differ. So false positives (a
reformat or unrelated edit flags a still-valid note) and false negatives (a note
rots while its file is untouched) both exist. This is accepted for v1: `k` is the
escape valve for the false positive, and no cheaper signal with better fidelity is
on offer. Not revisited further.

### 9.6 Hygiene — worklist, not a gate

`dm worklist` lists everything that wants agent judgment: **orphaned** notes (rule
(a) layer 4), **abandoned** notes (rule (b)), **ambiguous** follows (splits,
scatters, multi-candidate matches), **disputed** notes (`FB !`), and — on request —
stale/unconfirmed notes. It is a pure query over store ∪ pending and the current
checkout; running it changes nothing.

Unlike the companion model's pre-commit gate, the worklist is **never a forcing
function and never destructive**: an unresolved note stops *surfacing* in normal
reads; nothing demands tombstones, so skew or an odd checkout can never pressure the
agent into destroying live knowledge. What supplies the pressure instead is the
layered soft-pressure package below.

> **Decision — layered soft pressure, no gate (2026-07-21, resolves Q4).** Nothing
> ever *forces* hygiene: a hard gate is a destructive-compliance machine and stays
> rejected, as does auto-decay/TTL (destructive expiry violates
> never-silently-destroyed; non-destructive expiry silently hides cold-but-valid
> knowledge — the Q14 recall case). Pressure instead rides moments where the agent
> already has the context to act, in four layers:
>
> 1. **Flag demotion in ranking** (§5.6) — ⚠-flagged entries sink below unflagged
>    ones within their proximity group. Readers stay protected from debt before
>    anyone cleans it.
> 2. **Crowding nudge** (§4.3) — an `a` ack on a node already carrying many visible
>    notes appends a one-line hint to fold with `u`/`d`. Slop is caught at write
>    time, when the writer has just read the node's surface and knows what overlaps.
> 3. **Scoped worklist** — the default listing is bounded and context-scoped: it
>    leads with items homed on or near paths this replica's session has read or
>    written (derived from the pending records and the pending stat accumulator — no
>    new state) and gives the global remainder as a count; a flag lists everything.
>    Each item lands in front of the session best equipped to judge it, while the
>    context is fresh.
> 4. **Sync health line** (§8.4) — every sync ends with one line of worklist counts,
>    session-scoped then global. The backlog is never invisible.
>
> Every layer converts a global, someday chore into a local, now, in-context one —
> but none guarantees the global backlog shrinks. That residual is review finding D1
> (agent discipline alone preventing a slop spiral is the design's biggest
> unvalidated premise), so it is instrumented rather than assumed: the Q1 pilot
> additionally tracks **worklist backlog growth, stale fraction, and usefulness
> ratio**; if the backlog grows without bound under these layers, Q4 reopens with
> data (§14).

Worklist entries are resolved with the ordinary verbs: `k` (re-anchor/re-bless), `u`
(rewrite), `d` (tombstone), `mv` (manual re-home), `rm` (retire a dead path).
Worklist-state notes are addressable by exactly these repair verbs and are invisible
everywhere else (§5.3).

---

## 10. Agent Integration

### 10.1 Subcommand surface

The batch stdin of §4 handles all reads and writes. The subcommand surface shrinks
to:

| subcommand | action |
| --- | --- |
| `dm init` | create `.git/.dm/` (replica id, pending, cache), configure the `refs/dm/*` refspec, fetch or create the empty store (once per clone) |
| `dm sync` | share: fold pending → fetch → union merge → push with retry (§8.4) |
| `dm worklist` | the hygiene query (§9.6) |
| `dm gc` | compact the store: sweep garbage, orphan re-commit, epoch bump (§8.7) |
| `dm dump [path]` | full raw store state, for tests and debugging (§11.3) |

There is no checkout, no merge, no push/fetch pair, no prune, no gate: visibility
follows the checkout automatically, and sharing is one idempotent verb.

### 10.2 The two loops

- **Inner loop** — per-file, continuous, git-unaware. Read-before-touch,
  write-on-learn. Notes are visible the moment they are written (store ∪ pending).
- **Outer loop** — per-session. `dm sync` to share and receive; skim `dm worklist`
  when convenient.

| Lifecycle moment | Code side | dm side (agent runs) |
| --- | --- | --- |
| clone setup | `git clone` | `dm init` (once) |
| branch / rebase / squash / merge / switch | plain git, any strategy | **nothing** — visibility follows automatically (§2) |
| working | edit + `git commit` | **inner loop**: `r:` before touching, `a`/`u`/`d`/`f`/`al` on learning |
| wrap up / share | `git push` | `dm sync`; skim `dm worklist`, resolve what has fresh context (`k`/`u`/`d`/`mv`/`rm`) |

**Inner-loop discipline.**
1. **Read before touching** a file: `r:path` for the surface, `r:path:1` to orient
   with parent/arch notes inline, or `s:path:t1|t2` to grep the assembled context
   when a file sits under many arch notes.
2. **Write when you learn something durable** you'd want back on this branch later —
   a gotcha, a non-obvious constraint, a why-it's-this-way — not a restatement of
   code.
3. **Correct what you find wrong**: `u` to fix, `d` to remove, `f:#h:!:reason` to
   flag when you can't fix now, `k` to re-bless a stale-flagged note you've verified.

### 10.3 The `agents.md` prompt

This is the canonical block a consuming repo copies into its own `AGENTS.md` so the
agent reads `dm` output correctly and follows the lifecycle. It is deliberately
concise.

> #### Dark Matter (`dm`) — your memory about this codebase
>
> `dm` stores notes *about* the code (gotchas, architecture rationale, dev/ops
> know-how) that automatically surface on the branches and commits where they apply.
> Read before you touch a file; write when you learn something you'd want back later.
>
> **Read first.** Before editing `foo/bar.rb`: `r:foo/bar.rb` returns a *surface* —
> own notes (one-line previews), parent/arch notes, links, and an inventory footer.
> It's a map, not a dump. Drill with `r:#handle` (full body of one entry) or
> `s:foo/bar.rb:t1|t2` (grep the file's whole note-context; `|`=OR, `+`=AND).
>
> **Write on learning.** `a:path:subj:body` (subj: `c` code/behavior · `a`
> architecture · `d` dev/build/test · `o` ops). `u:#handle:body` supersedes ·
> `d:#handle` removes · `k:#handle[:path]` re-blesses a stale note (the path picks a
> split's home) · `f:#handle:!:reason` flags one wrong.
>
> **Link related notes.** Links are the only way notes relate — `al:#a:#b[:why]`
> connects two entries (directed `a→b`, optional comment); `dl:#a:#b` removes one.
> Link a note to the ones it depends on, contradicts, or elaborates: an architecture
> note (`a`) links to the files it governs so they surface it as `← linked-from`; a
> gotcha links to the note explaining the underlying cause. Follow any `→`/`←` you
> see with `r:#handle`.
>
> **Output.** Blocks split on `▸` (command echo, then raw content); `◾` ends the
> stream. Glyphs: `→` link · `↑` parent note · `←` linked-from · `⚠stale` (file
> changed since the note) · `⚠unconfirmed` (followed a move by inference) ·
> `⚠disputed` (flagged wrong) · `⚠split` (folder went two-plus places — pick the
> home with `k:#handle:path`). `(+N lines)` = hidden size. Handles `#a3f9c1` are
> stable — copy them into later commands.
>
> **Batch over stdin**, one command per line, body always last (`:` in a body needs
> no escaping; trailing `\` continues to the next line; `$N` references the entry
> created by command N of the same batch, so create-then-link is one round trip):
> ```
> dm <<EOF
> r:api/handler.rb
> a:api/handler.rb:c:Validates tenant header before dispatch
> EOF
> ```
>
> **Session lifecycle** — there is nothing to mirror: notes follow your branch
> through rebases, squashes, and merges automatically, and your own notes are usable
> the moment you write them. Run **`dm sync`** at session end (and whenever you want
> teammates' notes) to share both notes and read-stats; its footer reports how much
> wants judgment. Skim **`dm worklist`** at wrap-up — it leads with items near your
> session's work — and resolve what you have context for: `k` (still valid), `u`
> (rewrite), `d` (obsolete), `mv`/`rm` (re-home / retire a path).

---

## 11. Testing

### 11.1 E2E harness
Tests set up a **real git repo**, drive `dm` via stdin, and assert on both stdout and
the resulting store state. This is the primary test strategy.

### 11.2 Fixture repository

> **Decision (2026-07-19) — the fixture is a whole repository, copied per test.**
> The well-known-base idea is kept: every scenario starts from an identical,
> pre-seeded state so it inherits known handles, paths, and stats, and per-scenario
> setup is one copy, not a long command script. Under the single-store model that
> base is no longer a branch pair but an **entire local repository**: code history
> with branches in each §2 state, the seeded store (`refs/dm/store`), and pending.
> Each scenario **copies the repo directory** — cheap and local. Copy-per-test
> replaces the companion-era worktree-per-test: worktrees share refs, and scenarios
> must mutate `refs/dm/store` and branch refs independently. Copying a clone
> duplicates the replica id (§8.5's caveat) — deliberate and harmless here, since
> fixture replica ids are pinned anyway.

The fixture is **built by a command** (`dm fixture build`) from a single declarative
manifest — code history + notes + seeded stats — so a storage-format change is
absorbed in one place. It exercises every disclosure dimension: a file with
mixed-subject own notes; a nested folder tree (≥2 ancestor levels); an architecture
note at an LCA folder linking members (back-links); an entry with pre-seeded stats
(ranking + `⚠disputed`); a superseded entry (rev ≥2) and a tombstoned one; a
two-replica stat row (G-Counter merge). And the resolution surface: a note in each
§2 visibility state (an unmerged topic branch, a rebased branch, a squash-landed
branch with its landed verdict, an abandoned branch, a drifted file → `⚠stale`); a
pure rename; a move-with-edit inside and below the §9.2 bands; a folder split
ambiguity. Where possible it reuses the worked examples from §4–§7
(`api/handler.rb`, the `api/` arch note `#f22e90`, `db/schema.rb #a3f9c1`) so the
doc's illustrative output is literal golden output.

**Guard tests.** Because the fixture is built *through* `dm` (`a` etc.) rather than
seeded at the storage layer, a small set of guard tests asserts that the build
commands themselves work. A regression in `a` then surfaces as a focused guard
failure, not as every scenario mysteriously breaking.

**Determinism.** The harness pins every source of nondeterminism — fixed replica
ids, a seeded/deterministic id source, an injectable clock (so LWW outcomes are
assertable), fixed commit timestamps/authors (so every blob/commit SHA and patch-id
in the fixture is stable), and stable sort order — otherwise none of the golden
output is reproducible.

### 11.3 `dm dump`
`dm dump [path]` prints the **full raw state** of store ∪ pending — every record
(with resolved handles) and every stat row (per-replica counters, `t`) — in a
deterministic, un-budgeted, unranked, human/test-readable form. It is the opposite
of the agent-facing read (§4.3): no progressive disclosure, no size hints, no
ranking, just the ground truth. Tests use it to assert store state structurally and
to snapshot sync results; devs use it to inspect what actually landed.

### 11.4 Scenarios
- **CRUD** — create/supersede/tombstone; handle stability across revisions.
- **Discovery** — surface previews, cost hints, drill-by-handle, depth modifier,
  search.
- **Two-phase** — syntactically bad batch writes nothing; per-command errors
  isolated; `$N` backref violations (forward / out-of-range / non-create) reject
  at parse.
- **Visibility** — replay the §2 file and folder tables **row by row**; the tables
  are the acceptance spec.
- **Resolution** — rename bands (§9.2), folder follow heuristic and edge cases
  (§9.3), ambiguity policy, path-hint preference.
- **Macros** — `mv` expands to per-entry `RA` (rehashed anchors, prefix-rewritten
  folder keys), `rm` to per-entry `TB`; missing destination file → per-entry error,
  rest of the batch unaffected; recursive folder cases (Q7, §6.5).
- **Hygiene** (Q4) — flagged entries sink within their proximity group, unflagged
  order untouched; the crowding nudge appears exactly at the threshold and never
  errors; worklist leads with session-scoped items and counts the remainder; the
  sync health line matches worklist counts (§9.6).
- **Split/absorbed UX** (Q3) — a split note surfaces `⚠split` at each candidate and
  demoted; worklist/drill lines carry vote · margin · coverage; `k:#h:path` re-homes
  one entry while sibling entries stay ambiguous; plain `k` blesses an unconfirmed
  follow; scatter appears only on the worklist (§9.3).
- **Verdicts** — squash and rebase landings, abandoned branches, the
  landed-beats-abandoned fold, wrong-verdict repair (§9.4).
- **Sync** — two replicas, union convergence, non-ff retry, stat counter merge
  (G-Counters sum across replicas; LWW `t` picks the max).
- **Compaction** — `dm gc` drops what the §8.7 rules say and nothing else; epoch
  rule: a stale replica syncing across a compaction re-contributes only its own
  unpushed records (no resurrection); late pending referencing a purged entry lands
  dangling, is ignored at read, and is swept next pass; TB stays dead forever;
  landed `VD` survives while any record carries its origin; concurrent gc + sync
  race converges; pending survives a lost push race.
- **Locking** — concurrent invocations serialize on `.git/.dm/lock`; rec-id order
  survives a same-millisecond process handoff (Q13, §8.2).
- **Determinism** — same records, different sync orders/replicas → identical folded
  state.
- **Guard** — the fixture build commands (`a`, seed path) produce the expected base.
- **Replay / calibration** (Q1) — replay a real repo's history against seeded notes;
  measure the fraction resolving via §9.1 layers 1–3; calibrate the §9.2/§9.3 bands.
  Doubles as the validation gate for the architecture itself (§14).

---

## 12. Tech Stack
`dm` is a single **Go** binary. No runtime dependencies beyond git.

> **Decision — minimum git 2.15.** Everything used is ancient, stable plumbing:
> `hash-object`, `rev-parse`/ancestry checks, `diff` rename/copy detection
> (`-M`/`-C`), `patch-id`, custom refs push/fetch, `commit-tree`/`update-ref` for
> store commits. The companion model's `git worktree` dependency — which originally
> set the floor — is gone; 2.15 is retained as a conservative floor since nothing
> requires newer.

---

## 13. Rejected — Companion-Branch Mirroring

Until 2026-07-19 this document specified a different architecture: every code branch
`B` had a **companion branch** `llm/<B>` whose tree mirrored the code's directory
structure and carried the notes, held in dm-managed worktrees under `.git/.dm/`,
driven by an explicit verb choreography (`dm checkout` / `merge` / `commit` / `push`
/ `fetch` / `unmerged` / `prune`) that the agent ran alongside each git operation —
plus a hard `dm pre-commit` gate forcing orphan/staleness reconciliation before
every shadow commit, and patch-id detection to recognize branches landed by hosted
rebase/squash buttons.

**Why it was rejected.** Adversarial review ([review.md](review.md)) found the
storage core sound but produced four critical findings — **A1** (the orphan hard
gate destroys live knowledge under tree skew), **A3** (same-companion divergence
across clones has no merge path), **A4** (alignment-check/content-dirt deadlock),
**A5** (within-session anchor drift silently blessed at commit-stamping time) — that
all trace to one architectural stance: **dm sitting outside git, mirroring branch
topology and trying to observe git events after the fact.** A patch package (a
`Dm-Code` binding trailer plus recorder hooks) was designed and judged to be
stabilizing machinery for an unstable stance. The adopted model deletes the problems
instead of patching them: content + origin anchoring makes visibility a pure
read-time query (§2, §9), so there is no topology to mirror, no fold to choreograph,
no gate that can destroy data (A1 → a never-destructive worklist, §9.6), one CRDT
store with a sync retry loop (A3 → §8.4), no checkout binding to misalign (A4 →
nothing to align), and write-time anchoring (A5 → no stamping window, §8.3). It also
needs none of the sync mechanisms surveyed in
[existing_git_branch_follow_strategies.md](existing_git_branch_follow_strategies.md)
— nothing observes git; queries just look.

**What survived** is everything the review found sound: the append-only record/CRDT
layer (§8.3), handles (§3.5), the batch CLI (§4), and progressive disclosure (§5).
The spike that developed the adopted model is preserved in
[spike_content_adressable.md](archive/spike_content_adressable.md); the fallback, had it
failed validation, was the trailer + recorder-hooks package on companions.

---

## 14. Open Questions — build-time validation gates

The questions once tracked here (Q3–Q14) are settled; each decision lives as a
dated decision block in its home section — Q3 §9.3 · Q4 §9.6 · Q5 §2.1 ·
Q6 §8.1/§8.3 · Q7 §6.5 · Q8 §8.7 · Q9 §3.5 · Q10 §8.3 · Q11 §8.2 · Q12 §4.2 ·
Q13 §8.2 · Q14 §15. What remains is empirical: two gates that can only be verified
with v1 built, both running on the §11.4 harness.

- **Q1 — does resolution follow ordinary edit churn?** The premise to verify: that
  everyday editing rarely drops notes to layer 4 (unresolved) of §9.1 — if it
  routinely does, the model fails regardless of the merge story. Rig: replay a real
  repo's history against seeded notes (§11.4) and measure the fraction resolving
  via layers 1–3. The same runs calibrate the acceptance-band fractions (§9.2,
  §9.3) and track the Q4 hygiene metrics — worklist backlog growth, stale
  fraction, usefulness ratio (§9.6).
- **Q2 — read-path performance on large repos.** The caches (§8.2) and verdict
  records (§9.4) reduce steady-state reads to an index load plus tree lookups;
  what needs profiling is the residue: the **cold path** (first read after a
  checkout switch or sync — index rebuild plus the batched diffs), **diff
  multiplicity** (one batched origin→checkout diff per *distinct origin* among
  unresolved notes, and origins accumulate over a store's life), **checkout-side
  lookup** (anchored blob → current tree plus working-tree dirt, recomputed per
  invocation — the working tree has no stable key to memoize under), and
  **pending-elsewhere re-checks** (a live state, never memoized: all-refs
  reachability reruns until the origin lands or is abandoned). Profiling matrix:
  repo size × cold/warm cache × checkout age × unresolved-note count. If the index
  itself bottlenecks, the upgrade paths are pre-paid (§8.2: mmap'd arrays,
  incremental chaining).

---

## 15. Deferred

- **budget-fill retrieval** — `r:path` auto-expands top-ranked items to fill ~N
  tokens; wait until usage-stat ranking makes "dm decides" trustworthy.
- **weighted scoring function** — v1 ranks on proximity + subject priority only
  (§5.6); folding staleness, recency, and usage signals (expansion rate, usefulness
  ratio, feedback) into a weighted score waits until there's real usage data to tune
  the weights against.
- **bisect mode** (Q5) — an opt-in read mode (flag or config) relaxing rule (b) at
  detached old checkouts so later-on-main knowledge about *unchanged* files (rule
  (a) still holds) surfaces during bisect; v1 stays strict (§2.1).
- **auto-triggered compaction** — v1 compaction is manual `dm gc` only (§8.7);
  running it automatically at sync past a size/garbage threshold is a later knob.
- **Lamport counters in record headers** — retire the wall-clock caveat on
  ULID-ordered LWW folds (§8.3) without changing transport.
- **LWW-overridable verdict records** — batch repair for the many-notes-per-origin
  case (§9.4); v1 repairs at the note level.
- **team-shared vs per-clone stats** — already merged via CRDT counters; revisit if
  per-clone granularity turns out to matter.
- **duplicate signal** — none for v1; agent cleans up via CRUD (§6.3).
- **whole-store search** (`s::term`, Q14) — "what do I know about deploys?" has no
  verb in v1; `s` stays context-scoped (§5.5). The single global store makes it
  cheap to add when the need is demonstrated.
- **oversized-entry pagination** — expansion returns full body for v1; paginate huge
  entries later with a continuation handle.
- **extensible subjects** — fixed c/a/d/o enum for v1 (§3.3).
- **"occupant changed" flag for folder notes** — a possible softening of the
  exact-path-wins rows 13/14 (§2.2); accepted as-is for v1.
- **hunk-level following** — out, not deferred (§9.5): line ranges rot; whole-file
  blob identity is the 90% solution. Listed so the known ceiling is on the record.
- **staleness fidelity** — *not* deferred; settled as an **accepted limitation**
  (§9.5): blob drift detects byte-change, not wrongness, so false
  positives/negatives exist and are lived with. Listed here only so the known gap is
  on the record.
