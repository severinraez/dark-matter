# Dark Matter — Design

> **Status:** design draft. Decisions marked **TBD** are not yet settled; everything
> else reflects a made decision. This document is the source of truth for the model,
> the CLI, the storage format, and the git mechanics.

---

## 1. Overview

### 1.1 What it is
Dark Matter (`dm`) is a git-native memory layer for LLM agents: notes *about* a
codebase that live beside the code, branch and merge *with* the code, and are queried
through a token-efficient batch CLI with progressive disclosure.

The knowledge is not source and does not belong in the code files. It is the "dark
matter" around the code — architecture rationale, gotchas, dev/ops know-how — that an
agent accumulates and wants back later, on the right branch, without re-deriving it.

### 1.2 Design goals
- **Token-efficiency** — the CLI is batch-oriented and reads never dump; they return a
  *map* of what is queryable, with a size on every hidden thing, so the agent spends
  tokens deliberately.
- **Deterministic merge** — the LLM companion of a branch merges without human
  conflicts, always, by construction (union / CRDT semantics, not textual patch).
- **Progressive discovery** — an agent starts shallow and drills into exactly the
  dimensions it needs (long bodies, links, parent notes) over successive batches.

### 1.3 Glossary
- **Shadow tree** — the note-bearing directory tree that mirrors the code tree.
- **Companion branch** — for a code branch `B`, the shadow branch `B-llm`.
- **Node** — a single addressable location in the shadow tree: a file or a folder.
- **Logical entry** — one note. Has a stable id, a subject, a body, links, and a
  revision history. Addressed by a **handle**.
- **Handle** — a short, stable, identity-derived reference to a logical entry
  (`#a3f9c1`), reusable across batches and across merges.
- **Replica** — a single clone/agent, identified by a local id, for CRDT counters.
- **Subject** — the kind of knowledge a note carries: code / architecture /
  development / operations.
- **LWW register** — a single-valued field merged **last-write-wins**: when two
  branches set it concurrently, the merge keeps the value with the higher timestamp
  (wall-clock + replica-id tiebreak, a total order so every replica picks the same
  winner) and discards the other. Used for `t`; everything else in the header is a
  set/counter that unions.

---

## 2. Data Model

### 2.1 Shadow tree & the `B` / `B-llm` companion branch
For every code branch `B` there is a companion branch `B-llm` whose tree mirrors `B`'s
directory structure but contains only notes — no source. A code file `foo/bar.rb` maps
to a note file at the same path `foo/bar.rb` in the shadow tree.

Git lifecycle is mirrored:

| Code op                     | Shadow op                          |
|-----------------------------|------------------------------------|
| branch `B'` from `B`        | branch `B'-llm` from `B-llm`       |
| merge `B` into `main`       | merge `B-llm` into `main-llm`      |
| new branch, no companion    | `B-llm` starts empty               |

Merging `-llm` branches never produces a human conflict (see §8.2). This is the
founding invariant and constrains every storage decision below.

Mirroring is **explicit**: the agent/user drives the shadow side through `dm`
subcommands (e.g. `dm merge <source>`) alongside the corresponding git op on the code
branch. See §8.1.

### 2.2 Note targets — where a note is anchored
Every note has exactly **one home node**, which preserves the mirror property and the
deterministic merge:
- **file note** — homed at the file's mirror path; applies to that file.
- **folder note** — homed at the folder's mirror path; applies to the folder **and
  everything under it, recursively**. This is the "parent notes" disclosure dimension:
  reading a file walks up its ancestors and surfaces folder notes along the way.

One home per note is non-negotiable — union of per-node entry sets is what keeps merge
deterministic.

### 2.3 Note subjects — what kind of knowledge
Subject is a **facet tagged on each entry**, not a second location; a node can carry
entries of several subjects at once.

| subj | meaning                                                | typical target |
|------|--------------------------------------------------------|----------------|
| `c`  | **code** — behavior, edge cases, gotchas               | file           |
| `a`  | **architecture** — cross-cutting design, many nodes    | folder / root  |
| `d`  | **development** — build, test, codegen, flaky tests    | file or folder |
| `o`  | **operations** — deploy target, logging, secrets       | folder         |

The two axes (target × subject) are independent; every combination is meaningful.

> **Decision:** the subject enum is closed — no extensible/`general` fallback. Every
> note commits to one of the defined subjects; a forced choice keeps ranking and
> disclosure meaningful and avoids a catch-all bucket that erodes the taxonomy.

### 2.4 Architecture notes (Option 1)
Architecture knowledge is cross-cutting — "referred to by multiple files/folders" —
which fights the one-home rule. Resolution: **architecture is a subject-tagged note
(`a`) homed at the lowest common ancestor folder of the things it describes, carrying
`links[]` to the specific member nodes.** Members surface it via back-links.

Consequences:
- Everything stays a note on a real tree node → mirror + merge unchanged.
- Reuses existing machinery: recursive folder notes + links + parent-note disclosure
  compose to produce architecture notes for free.
- Truly global concepts living at the repo root is *correct*, not a bug.

This keeps the system to **one storage primitive** — a subject-tagged, linkable entry
homed on one tree node — rather than a separate "concept" object.

### 2.5 Logical entries, revisions & handles
Each note is a **logical entry** with a globally-unique internal id. Its display
**handle** is a fixed-length prefix of that id, shown as `#a3f9c1`. Because it is
identity-derived (not positional):
- it is the **same everywhere** the entry appears (home node *and* back-links),
- it **survives merges** (union never changes an entry's id),
- the agent can copy the exact string it saw into a later `u`/`d`/`f`.

**Handle collisions:** fixed length (6 chars), **extended on the rare collision at write
time** — dm grabs a couple more bits when minting if the prefix already exists. The
agent therefore always sees a stable, reusable handle; ambiguity is resolved once, at
write time, never surprising a later command.

> **Decision:** internal ids are **ULIDs** — globally unique without coordination,
> lexicographically sortable by creation time (a natural tiebreak), and independent of
> content so a supersede keeps the entry's id. The `#a3f9c1` handle is a 6-char base32
> prefix of the ULID, extended on the rare write-time collision as above.

---

## 3. CLI Interface

### 3.1 Batch-over-stdin & two-phase execution
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

> **Decision:** the body is always the **last field** of a command, so once the first
> line is parsed we know exactly where the body starts — everything from there to the
> end of the command is body. Consequences: `:` inside a body needs **no escaping** (all
> preceding separators are consumed positionally before the body begins), and the body
> may span multiple physical lines. The **only** escaped character is the line separator
> that divides commands: a body newline is written as a trailing `\` continuation, so a
> command runs until the first line **not** ending in `\`.

### 3.2 Command grammar

| cmd | form                    | action                                   |
|-----|-------------------------|------------------------------------------|
| `r` | `r:path` \| `r:#handle` | read node surface, or expand one entry   |
| `r` | `r:path:N`              | read with disclosure depth N (§4.4)      |
| `s` | `s:path:t1\|t2`         | search node context for terms (OR)       |
| `a` | `a:path:subj:body`      | create entry (`subj` ∈ c/a/d/o)          |
| `u` | `u:#handle:body`        | supersede an entry's body                |
| `d` | `d:#handle`             | tombstone an entry                       |
| `f` | `f:#handle:sig[:reason]`| feedback (`sig` ∈ `+` `-` `!`)           |
| `k` | `k:#handle`             | confirm stale note still valid; re-anchor|
| `mv`| `mv:old:new`            | relocate a node's notes to a new path    |
| `rm`| `rm:path`               | tombstone all entries homed at a path    |
| `al`| `al:#a:#b[:note]`       | add link between two entries, opt comment|
| `dl`| `dl:#a:#b`              | delete a link between two entries        |

### 3.3 Output format
One block per command, in input order. Blocks are delimited by single **sentinel
bytes**, and content is emitted **raw** (no indentation) to save characters:
- **next-marker** `▸` (wire byte ASCII **RS `0x1E`**) prefixes each command echo; it
  starts a block and ends the previous one.
- **end-marker** `◾` (wire byte ASCII **GS `0x1D`**) prefixes the terminal footer.

Control bytes cost ~nothing in tokens and cannot occur in text note bodies, so a body
line can never be mistaken for a boundary.

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
`✓ #handle tombstoned`, `✓ #handle feedback +`, or `✗ #handle <error>`.

> **Decision:** use the Unicode structure glyphs (`→` link, `↑` parent, `←` linked-from,
> `★` arch, `⚠` stale) — visually unambiguous and collision-proof against content. **Match
> markers are dropped entirely**: search snippets carry no in-content highlighting, so no
> glyph ever needs escaping inside a body. The agent already has the term it searched for.

### 3.4 Error reporting
- **Batch-level** syntax failure → a single `◾`… no: a single `!` line at the head of
  output, nothing applied: `!parse error line 3: expected subject (c|a|d|o) after path`.
- **Per-command** failure in an otherwise-valid batch → that command's block contains a
  `✗ …` line; other commands still apply.
- A final `◾N ok[, M error]` footer tallies the batch and signals end-of-stream.

---

## 4. Retrieval & Progressive Discovery

Guiding idea: **a read never dumps; it returns a map of what is queryable, with a size
on every hidden thing.** Discovery is the agent walking that map over successive batches.
v1 is **strictly lazy** (surface + explicit expansion); budget-fill is deferred (§11).

### 4.1 The surface (`r:path`)
A budgeted digest of the node plus pointers to every adjacent dimension, each collapsed:
- own entries → one-line previews (`subj #handle first-line (+N lines) ⚠flag`),
- parents/links → **not expanded**, shown as handles + labels + counts; arch parents
  are called out because they orient the agent,
- an **inventory footer** — the "how much more is there" signal in one line:
  `context: 2 own · 3 parent · 2 links · ~18 hidden`.

### 4.2 Cost hints
Every collapsed item carries its size: `(+12 lines)`, `linked from 3 nodes`,
`2 parent notes`. The agent sees the *shape* of hidden information and decides what is
worth a token.

### 4.3 Drilling by handle
`r` accepts a **path or a handle**, so expansion is always the same move:
- `r:path` → the surface digest for a node,
- `r:#handle` → the **full** body of one entry + its links one level + relations.

See a collapsed line → `r:#handle`. Whether it is a truncated note, a parent arch note,
or a concept, it is one verb.

### 4.4 Depth modifier
For orientation in fewer round-trips, a depth suffix trades tokens for round-trips,
capped by a token budget so it degrades gracefully:

| call        | returns                                                    |
|-------------|------------------------------------------------------------|
| `r:path`    | surface (depth 0)                                          |
| `r:path:1`  | inline parent notes + link **labels** (bodies collapsed)   |
| `r:path:2`  | also expand link/concept bodies one level                  |

Folder reads navigate down: `r:folder/` returns the folder's own notes + a **child
inventory** (which subpaths carry notes, counts by subject), not the child notes.

### 4.5 Context search (`s`)
`s:path:t1|t2` searches the file's **entire read-context** (its own notes + all parent
folder notes + linked concepts), returning matches as handles + snippets. This is the
escape hatch when a file sits under many arch notes: grep the assembled context instead
of expanding every parent. Terms combine with `|` = **OR** and `+` = **AND**; `+` binds
tighter, so `a+b|c` reads as `(a AND b) OR c` — an entry matches if any OR-group has all
its `+`-joined terms present.

```
▸s:db/schema.rb:tenant|isolation
#a3f9c1 c db/schema.rb …single-table inheritance for tenants… (+1 more)
#f22e90 a api/ …enforces tenant scoping…
2 matches · searched 5 entries in context
```

> **Decision:** `AND` (`+`) is in for v1, as above. **Whole-store search** (`s::term`
> across the entire tree, ignoring context scope) is **dropped** — `s` is always
> context-scoped; a global grep is out of scope, not merely deferred.

### 4.6 Ranking
When more fits than the budget allows, order by: **proximity** (closer ancestors first)
→ **staleness** (fresh over drifted) → **subject priority** (arch first for orientation)
→ **recency**. Once usage stats exist (§6), **expansion rate** and **usefulness ratio**
join the ranking.

> **TBD:** the exact scoring function / weights.

---

## 5. CRUD & the Append-Only Log

### 5.1 create / supersede / tombstone / keep
To keep merge = deterministic union, `u`/`d`/`k` never mutate in place. They append log
records against the logical entry; current state is a fold of that entry's log:
- `a` → append a **create** (`CR`) record — mints a new id/handle,
- `u:#handle:body` → append a **supersede** (`SU`) record — same handle, new revision,
- `d:#handle` → append a **tombstone** (`TB`) record — handle resolves to deleted,
- `k:#handle` → append a **re-anchor** (`RA`) record — re-stamps the staleness anchor
  to the current blob SHA, **no new body revision** (§7.5), so churn stats stay clean.

### 5.2 Handle stability
The handle addresses the **logical entry**, not a revision or a position, so it is
stable through supersedes, tombstones, and merges. `#a3f9c1` always means the same note.

### 5.3 Cleanup via CRUD
There is **no dedicated dedup / duplicate signal**. If the agent finds redundant notes,
it collapses them with plain CRUD (`u` to fold content into one, `d` to tombstone the
other).

### 5.4 Links
Links follow the same append-only discipline and are the **only** way to relate entries:
- `al:#a:#b[:note]` → append a **link** (`LN`) record — directed `#a → #b`, optional comment,
- `dl:#a:#b` → append an **unlink** (`UL`) record — the pair's current state is LWW.
Links are never edited inside a body (§7.3); back-links are the inverse index (§7.3),
so a link surfaces identically at both endpoints and survives supersede/move/merge.

---

## 6. Usage Statistics & Feedback

**Decision:** stats are **merged**, not local telemetry, and are stored as a **header on
each note file** (§7.1). This means reads mutate the header; the cost is accepted in
exchange for stats that rank *and* travel with branches.

### 6.1 What's tracked (per logical entry)
| stat                 | signal                                                  |
|----------------------|---------------------------------------------------------|
| **impressions** (`i`)| times shown *collapsed* in a surface (weak)             |
| **expansions** (`x`) | times *opened* via `r:#handle` (strong — the real click)|
| **last-expanded**(`t`)| recency of genuine use                                 |
| **search-hits**      | matched a search and then expanded                      |
| **revisions / churn**| volatile vs settled knowledge                           |
| **feedback** (`+ - !`)| explicit agent signal (§6.3)                           |

The impression-vs-expansion split is core: surfacing is cheap, *opening* is the click.

> **TBD:** whether `search-hits` is a distinct counter or folded into `impressions`.

### 6.2 In-file stat header
See §7.1–7.2. One header row per logical entry; each counter cell is a per-replica
**G-Counter**; `t` is a last-write-wins timestamp. All fields merge deterministically.

### 6.3 Feedback interface (`f`)
```
f:#handle:<signal>[:reason]
```
| signal | meaning              | effect                                        |
|--------|----------------------|-----------------------------------------------|
| `+`    | useful               | boosts ranking                                |
| `-`    | not useful here      | demotes ranking                               |
| `!`    | wrong / outdated     | flips surface flag to `⚠disputed`, queues for housekeeping |

Optional `reason` is a short string stored with the feedback — invaluable for a later
editor (`f:#f22e90:!:repo/ layer was removed in v3`). Implicit signals still count
(expansion = weak +, ignore = weak −); `f` is the explicit, high-weight channel.

### 6.4 How stats feed retrieval & surface flags
- **Ranking** gains usefulness ratio + expansion rate on top of §4.6.
- **Surface flags** stay token-disciplined: only *behavior-changing* signals are shown —
  `⚠disputed` (from `!`) and `⚠stale` (hash drift, §7.5). Positive signal is expressed
  silently through ordering, not badges.
- **Housekeeping** (data supports it now; the agent-facing/CLI report is deferred, §11):
  never-expanded notes past an age, disputed notes, high-churn notes, hash-drifted notes
  → a prune/review worklist.

---

## 7. Storage Format

### 7.1 Note file layout
Each node's note file has two deterministically-mergeable regions split by a marker: a
**header** (mutable CRDT stats) above a **body** (append-only entry log).

```
∎dm1
a3f9c1  i=A3:12,F2:4  x=A3:3  t=2026-07-07T09:12Z  +=A3:2  !=A3:1
b1c0e7  i=A3:5                t=2026-07-01T11:03Z
∎
CR a3f9c1 c 1a2b… Schema uses single-table inheritance for tenants…
SU a3f9c1 c 9f01… …now partitioned per tenant
CR b1c0e7 d 77c3… Regenerate with `rake db:schema` after edits
```

Content edits only append to the **body**; stat updates only touch the **header**. So
the knowledge stays diff-quiet — churn is confined to the header lines.

> **TBD:** exact on-disk syntax (field separators, how multi-line bodies are encoded in
> a `CR`/`SU` record, escaping). Above is illustrative.

### 7.2 Header schema
- One row per logical entry, keyed by id.
- Columns: `i` impressions, `x` expansions, `t` last-expanded, `+` `-` `!` feedback.
  (Add `search-hits` per §6.1 TBD.)
- Each counter cell is a **G-Counter**: `A3:12,F2:4` = replica A3→12, F2→4, total 16.
  Missing field = zero.
- `t` is a last-write-wins timestamp register.

### 7.3 Body schema
Append log of records, each carrying its own record-id (for union merge):
- `CR <entry-id> <subj> <blob-sha> <body>` — create,
- `SU <entry-id> <subj> <blob-sha> <body>` — supersede,
- `TB <entry-id>` — tombstone,
- `RA <entry-id> <blob-sha>` — re-anchor: re-stamp the staleness anchor only (§7.5),
- `MV <from-path> <to-path>` — relocate: entries at `<from-path>` belong at
  `<to-path>` (§8.4). Path-scoped, not entry-scoped; applied as a sweep during the
  merge fold, not a per-entry edit.
- `LN <from-#> <to-#> [comment]` — link, with optional comment,
- `UL <from-#> <to-#>` — unlink.
Links are their own append-only records — never a field edited inside a body — so they
merge by union like everything else. The `al`/`dl` commands are the **only** way to
manage links and the sole writers of `LN`/`UL` records; a link's current state folds
from those records (LWW on the pair key, §8.2). Back-links (`← linked-from`) are the
inverse index, computed on read.

**Decision — staleness anchor.** The `<blob-sha>` on `CR`/`SU` is the git blob SHA of
the described file at write time (`git rev-parse HEAD:path`) — git's own content address,
so no separate hashing pass. The current anchor folds as a **LWW register** from the
entry's `CR`/`SU`/`RA` records. It is re-stamped **only** by a deliberate write —
`a`, `u`, or `k` — never by merge, which would re-bless a note as fresh without anyone
confirming it still matches the code (§7.5).

**Decision — concurrent supersede.** When two branches supersede the same entry
concurrently, the current body resolves **last-write-wins** (timestamp + replica-id
tiebreak, §8.2), consistent with the `t` and move-path registers. Both `SU` records
survive in the log — the loser becomes a sibling revision, never a git conflict — so
nothing is destroyed and churn stats stay accurate. No keep-both fork is surfaced; the
merge always yields one clean current body.

**Decision — `MV` lives on the destination file.** A move carries the whole note-file
log to the new path and appends `MV <from> <to>`, so a file that moved `a→b→c`
accumulates both `MV a b` and `MV b c` in its own log — the full provenance chain
travels with the file. On merge, the union of all `MV` records forms a redirect map
chased to a **fixpoint** (transitively composing `a→b→c`), sweeping any entries left at
an intermediate path — including ones a branch added there *unaware of the move* — to
the terminal path, which then unions by handle. This composes correctly no matter which
branch each hop came from, because the hops are just unioned log records.

**Decision — cycle guard.** Pathological concurrent moves (`a→b` vs `b→a`) could loop
the fixpoint chase. The transitive close tracks visited paths; if the chain revisits a
path, it stops and picks the cycle's terminal by the **same LWW total order** used
everywhere else (highest `t` + replica-id tiebreak among the `MV` records in the cycle).
Every replica computes the identical winner, so it collapses deterministically with no
new rule.

**Decision — `rm` discards the chain (accepted degradation).** `rm`-ing a terminal file
tombstones its accumulated `MV` provenance with it, so a much-later add at a now-orphaned
old path has nothing to redirect. This is caught by post-merge `dm pre-commit` (§8.4),
which already demands the agent resolve orphans — a graceful one-step fallback, and
correct behavior anyway since the path's whole lineage is genuinely gone.

### 7.4 Replica identity
G-Counters need a stable per-clone **replica id**, kept in **local config**
(`.dm/replica`, gitignored) so it never merges. A wide random value (8 base32 chars from
a CSPRNG at `dm init`) so collisions never need coordinating.

> **Decision:** spend the bytes rather than fight collisions. The replica id is a wide
> random value (e.g. 8 base32 chars from a CSPRNG at `dm init`, not derived from
> `user.email`) so collision across any realistic team is negligible and needs no
> registry or coordination. The cost is a few extra bytes per populated G-Counter cell,
> which is cheap next to the note bodies and compresses well in git.

### 7.5 Staleness anchor
Each entry records the **git blob SHA** of the file it describes at write time
(`git rev-parse HEAD:path`). On read, `dm` compares it to the file's current blob SHA;
a mismatch flags the entry `⚠stale`, and ranking (§4.6) sinks stale below fresh. Using
git's own content address means no separate hashing pass, a self-contained per-file
anchor (no cross-branch history dependency), and a trivial merge rule — the anchor rides
`CR`/`SU`/`RA` and folds LWW (§7.3).

**Decision — granularity.** Whole-file, matching file-level note granularity. A region/
line anchor would be more precise but rots (line ranges shift under edits); whole-file is
robust and cheap. The cost — any edit anywhere in a file flags all its notes — is
absorbed by the housekeeping flow (§8.4): re-blessing via `k` is a one-liner, and the
stale worklist is scoped to files the session actually touched.

**Decision — treatment.** A stale note is reconciled by the agent, never by dm, one of
three ways: `k` (still valid → re-anchor), `u` (partly wrong → rewrite, re-anchors), or
`d` (obsolete → tombstone). The `dm pre-commit` gate (§8.4) drives this at session end.

**Accepted limitation.** Blob-SHA drift detects that *the file's bytes changed*, not that
*the note became wrong* — the two overlap but differ. So false positives (a reformat or
unrelated edit flags a still-valid note) and false negatives (a note rots while its file
is untouched) both exist. This is accepted for v1: `k` is the escape valve for the false
positive, and no cheaper signal with better fidelity is on offer. Not revisited further.

---

## 8. Git Mechanics

> The mirroring model is settled (explicit `dm` subcommands). Remaining **TBD**s are the
> ref storage, the merge driver packaging, and the `SU`-vs-`SU` rule.

### 8.1 `-llm` branch mirroring — explicit commands

**Decision:** mirroring is **explicit**. The agent/user drives the shadow branches
through `dm` subcommands, run alongside the corresponding git op on the code branch —
no hooks, no implicit magic. This is the portable, testable choice, and it keeps `dm`
in control of shadow refs and merge semantics.

**`dm merge <source-branch>`** performs the deterministic merge of the companion
branches into the **currently checked-out branch's** companion — i.e. it merges
`<source>-llm` into `<current>-llm`, mirroring `git merge <source-branch>` — and
**writes the merged files into the working tree, uncommitted**. The caller reviews and
commits them, typically in the same commit/PR as the code merge. dm does not auto-commit
the merge result.

Because the merge is deterministic (§8.2) there is never a conflict to resolve; the
uncommitted state is simply the finished merge awaiting the caller's commit.

A missing companion bootstraps empty (a merge/branch targeting a code branch with no
`-llm` yet starts from an empty shadow tree).

> **TBD:** the branch-lifecycle commands beyond merge — the exact surface for
> branching/checkout mirroring (`dm branch <base> <new>`? `dm checkout`?) and whether
> those also leave uncommitted state or commit immediately.

> **TBD:** how `-llm` branches are stored — real branches (double the `git branch`
> list), an orphan/detached ref namespace (`refs/dm/…`), or a subtree. Prefer a ref
> namespace so `git branch` output stays clean. `dm merge` resolving `<branch>` →
> `<branch>-llm` must match whatever storage scheme is chosen here.

### 8.2 Deterministic merge algorithm

`dm merge` computes the merge **in-process** — it reads both branch versions of each
note file, merges each file per-region, and writes the result to the working tree.
Because mirroring is explicit (§8.1), the merge does **not** rely on git invoking a
registered merge driver; `dm` owns the merge logic directly.

Per file, both regions merge with no human conflict:
- **header counters** (`i x + - !`): union replicas, **max per replica**, sum for value,
- **header `t`**: **max** timestamp (LWW),
- **body** (entry log): **union** of records by record-id.

The three-way base is the merge-base of the two `-llm` branch tips (standard git
ancestor), so deletes/tombstones and additions are distinguished correctly.

Single-valued fields that can be concurrently written resolve **last-write-wins** on
the `t`/replica-id total order: the header `t` register and the current body of an entry
superseded on both sides (**`SU`-vs-`SU`**, decided in §7.3 — both `SU` records survive
in the log, the loser becomes a sibling revision). The move-path redirect map composes
by fixpoint (§7.3) rather than LWW.

> **TBD (optional):** also register a `.gitattributes` merge driver as a safety net so a
> *raw* `git merge` of shadow branches stays deterministic too, even when the user
> bypasses `dm merge`. Not required for the primary path.

### 8.3 Commit cadence for stats
Reads mutate the header, so stat writes are **coalesced and deferred**: a `dm`
invocation applies all its deltas to the working tree once at the end, and dm flushes
them into a single stats commit lazily — piggybacked on the next content write, or
forced right before any branch/merge op (so nothing merges uncommitted). Pending deltas
between flushes live in the working tree.

> **TBD:** the exact flush trigger and whether stats get their own commit vs riding
> content commits.

### 8.4 Renames & orphaned notes — agent-driven reconciliation

**Decision:** the **agent tracks renames**, not dm. dm does not watch the code tree
or run git rename detection; instead it provides a **pre-commit gate** that forces
the agent to reconcile the shadow tree against the code tree before every shadow
commit.

**`dm pre-commit`** compares the shadow tree against the current code working tree
and lists every **orphaned node** — a note-bearing path (file or folder) whose mirror
path no longer exists in the code tree. It exits non-zero while any orphan is
unresolved, so the agent must act. Each orphan is resolved one of two ways:

- **`mv:old:new`** — the code moved; relocate the node's notes to the new mirror
  path. Entry handles are preserved (§5.2), so links and back-links survive.
- **`rm:path`** — the code was genuinely deleted; tombstone the node's entries. This
  is the node-level counterpart to `d:#handle` (which tombstones one entry): it
  tombstones every entry homed at a vanished path in one move.

`mv` and `rm` are ordinary **batch-stdin commands** (§3.2) — the agent feeds them into
a `dm` batch like any read or write. `dm pre-commit` is a **subcommand** (like `dm
merge`): the gate itself, run at the end of a work session before committing the `-llm`
branch. Reconciling on every commit keeps the shadow tree consistent with the code tree,
which is what makes the deterministic merge tractable: both sides of a merge are
self-consistent snapshots.

**Decision — the gate also covers staleness (§7.5).** Besides orphans, `dm pre-commit`
emits a **stale worklist**: notes whose blob-SHA anchor no longer matches the current
file. It is **scoped to the working set** — only notes homed on code files this branch
changed (diff vs the merge-base) — so the list is short and every item is one the agent
has fresh context to judge. Notes on untouched files stay flagged on read but never enter
the gate. Each stale note is resolved with `k` (still valid → re-anchor), `u` (rewrite),
or `d` (tombstone). Both worklists are a **hard gate**: `dm pre-commit` exits non-zero
until every orphan *and* every scoped-stale note is resolved. Scoping is what earns the
hard gate — it makes the list actionable rather than noise, so "reconcile the
consequences of what you changed this session" applies uniformly to moves and drift.

**Decision — concurrent moves.** A move writes a mergeable **`MV` record** (§7.3)
naming the prior path; the merge **follows the entry handle, not the path**, and
consolidates all of an entry's records at the **newest path** — the destination of the
most recent move, resolving conflicting destinations (`a→b` vs `a→c`) by the same LWW
rule as `t` (§8.2). The `MV` record is what makes move-vs-edit safe: a branch that
added notes at the old path *unaware of the move* has them swept to the new path on
merge instead of orphaning at a dead location. So a "moved" pointer does survive — but
only as **internal merge metadata**, never an agent-facing redirect: reads never
resolve an old path (§4), and once an `MV` record sinks below the merge-base of all
live branches it is compacted away (§11), like a tombstone.

> **Decided:** `dm pre-commit` reports **orphans and scoped-stale notes** (both a hard
> gate). Detecting *new* code files with no shadow node (to nudge note-taking) is out of
> scope for v1.

---

## 9. Testing

### 9.1 E2E harness
Tests set up a **real git repo**, drive `dm` via stdin, and assert on both stdout and
the resulting shadow-tree state. This is the primary test strategy.

### 9.2 Fixture repo & the base branch
All scenarios branch off a single **well-known base**: `base` (code) + `base-llm`
(shadow, pre-seeded with a known note set). Branching off a shared base means every
scenario inherits identical handles, paths, and stats, so per-scenario setup is one
checkout, not a long command script — and discovery/merge tests stop depending on the
write commands being correct just to reach their starting state.

The fixture is **built by a command** (`dm fixture build`) from a single declarative
manifest — code tree + notes + seeded stats — so a storage-format change is absorbed in
one place. It deliberately exercises every disclosure dimension so discovery tests just
reuse it: a file with mixed-subject own notes; a nested folder tree (≥2 ancestor
levels); an architecture note at an LCA folder linking members (back-links); an entry
with pre-seeded stats (ranking + `⚠disputed`); a hash-drifted entry (`⚠stale`); a
superseded entry (rev ≥2) and a tombstoned one; and a two-replica header (G-Counter
merge). Where possible it reuses the worked examples from §3–§6 (`api/handler.rb`, the
`api/` arch note `#f22e90`, `db/schema.rb #a3f9c1`) so the doc's illustrative output is
literal golden output.

**Guard tests.** Because the fixture is built *through* `dm` (`a` etc.) rather than
seeded at the storage layer, a small set of guard tests asserts that the build commands
themselves work. A regression in `a` then surfaces as a focused guard failure, not as
every scenario mysteriously breaking.

**Isolation: worktree-per-test.** Each scenario runs in its own `git worktree` off the
fixture, so tests cannot mutate the shared base and can run in parallel.

**Determinism.** The harness pins every source of nondeterminism — fixed replica id, a
seeded/deterministic id source, an injectable clock (so LWW `t` is assertable), and
stable sort order — otherwise none of the golden output is reproducible.

### 9.3 `dm dump`
`dm dump [path]` prints the **full raw state** of the shadow tree — every note file's
header (per-replica counters, `t`) and body (the append-log with resolved handles) — in
a deterministic, un-budgeted, unranked, human/test-readable form. It is the opposite of
the agent-facing read (§3.3): no progressive disclosure, no size hints, no ranking, just
the ground truth on disk. Tests use it to assert shadow-tree state structurally and to
snapshot merge results; devs use it to inspect what actually landed.

### 9.4 Scenarios
- **CRUD** — create/supersede/tombstone; handle stability across revisions.
- **Discovery** — surface previews, cost hints, drill-by-handle, depth modifier, search.
- **Two-phase** — syntactically bad batch writes nothing; per-command errors isolated.
- **Branch** — companion `-llm` branches off correctly; empty bootstrap.
- **Merge** — deterministic merge produces no conflict; body union + header counter merge.
- **Determinism** — same inputs, different orders/replicas → identical merged result.
- **Stats merge** — G-Counters sum across replicas; LWW timestamp picks the max.
- **Renames** — `dm pre-commit` lists orphans; `dm mv` relocates notes preserving
  handles/links; `dm rm` tombstones a deleted node's entries.
- **Guard** — the fixture build commands (`a`, seed path) produce the expected base.

---

## 10. Tech Stack
`dm` is a single **Go** binary. No runtime dependencies beyond git.

> **TBD:** minimum git version; whether the merge driver / hooks ship inside the same
> binary (assumed yes).

---

## 11. Deferred / Open Questions
- **budget-fill retrieval** — `r:path` auto-expands top-ranked items to fill ~N tokens;
  wait until usage-stat ranking makes "dm decides" trustworthy.
- **compaction** — rewrite files to fold superseded revisions, drop tombstoned rows,
  prune settled `MV` records (once below the merge-base of all live branches), coalesce
  counters; must be coordinated to not lose concurrent increments.
- **team-shared vs per-clone stats** — already merged via CRDT header; revisit if
  per-clone granularity turns out to matter.
- **duplicate signal** — none for v1; agent cleans up via CRUD.
- **whole-store search** — dropped, not deferred: `s` is always context-scoped
  (`AND`/`OR` both land in v1, §4.5).
- **oversized-entry pagination** — expansion returns full body for v1; paginate huge
  entries later with a continuation handle.
- **extensible subjects** — fixed c/a/d/o enum for v1.
- **staleness fidelity** — *not* deferred; settled as an **accepted limitation** (§7.5):
  blob-SHA drift detects byte-change, not wrongness, so false positives/negatives exist
  and are lived with. Listed here only so the known gap is on the record.
