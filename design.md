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

> **TBD:** subject enum is fixed for v1. Whether to allow an extensible/`general`
> fallback is deferred (see §11).

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

> **TBD:** exact id scheme (ULID vs content-hash) and the base length. 6 base32 chars is
> the working assumption.

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

> **TBD:** multi-line note bodies. The original sketch used trailing `\` line
> continuation; the exact escaping rule (how `:` and newlines inside a body are
> delimited from the grammar) needs to be pinned down.

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

> **TBD:** final choice of visible glyphs used *inside* content (match markers `⟪…⟫`,
> the `→ ↑ ← ★` glyphs) — pick for minimal tokenization cost at implementation time.

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
folder notes + linked concepts) for any term (`|` = OR), returning matches as handles +
snippets. This is the escape hatch when a file sits under many arch notes: grep the
assembled context instead of expanding every parent.

```
▸s:db/schema.rb:tenant|isolation
#a3f9c1 c db/schema.rb …single-table inheritance for ⟪tenant⟫s… (+1 more)
#f22e90 a api/ …enforces ⟪tenant⟫ scoping…
2 matches · searched 5 entries in context
```

> **TBD:** `AND` search (space/`+`) and whole-store search (`s::term`) are deferred (§11).

### 4.6 Ranking
When more fits than the budget allows, order by: **proximity** (closer ancestors first)
→ **staleness** (fresh over drifted) → **subject priority** (arch first for orientation)
→ **recency**. Once usage stats exist (§6), **expansion rate** and **usefulness ratio**
join the ranking.

> **TBD:** the exact scoring function / weights.

---

## 5. CRUD & the Append-Only Log

### 5.1 create / supersede / tombstone
To keep merge = deterministic union, `u`/`d` never mutate in place. They append log
records against the logical entry; current state is a fold of that entry's log:
- `a` → append a **create** (`CR`) record — mints a new id/handle,
- `u:#handle:body` → append a **supersede** (`SU`) record — same handle, new revision,
- `d:#handle` → append a **tombstone** (`TB`) record — handle resolves to deleted.

### 5.2 Handle stability
The handle addresses the **logical entry**, not a revision or a position, so it is
stable through supersedes, tombstones, and merges. `#a3f9c1` always means the same note.

### 5.3 Cleanup via CRUD
There is **no dedicated dedup / duplicate signal**. If the agent finds redundant notes,
it collapses them with plain CRUD (`u` to fold content into one, `d` to tombstone the
other).

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
- `CR <entry-id> <subj> <content-hash> <body>` — create,
- `SU <entry-id> <subj> <content-hash> <body>` — supersede,
- `TB <entry-id>` — tombstone.
Links are carried on the entry.

> **TBD:** where links live in the record (inline field vs separate `LN` records), and
> the `SU` conflict rule when two branches supersede the same entry concurrently
> (keep-both fork vs logical-clock last-writer).

### 7.4 Replica identity
G-Counters need a stable per-clone **replica id**, kept in **local config**
(`.dm/replica`, gitignored) so it never merges. Short (3–4 base32 chars, derived from
`git config user.email` + a local salt) to keep header cells tiny.

> **TBD:** derivation + collision handling across a team at that length.

### 7.5 Staleness hashing
Each entry records the **content-hash of the file (or region)** it describes at write
time. On read, if the current file has drifted, the entry is flagged `⚠stale`. Cheap,
high trust value.

> **TBD:** hash granularity — whole-file only (robust) vs region/line (richer but rots).
> Working assumption: whole-file for v1, matching file-level note granularity.

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

> **TBD:** the `SU`-vs-`SU` concurrent-supersede resolution rule (see §7.3).
>
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

---

## 9. Testing

### 9.1 E2E harness
Tests set up a **real temporary git repo**, drive `dm` via stdin, and assert on both
stdout and the resulting shadow-tree state. This is the primary test strategy.

### 9.2 Scenarios
- **CRUD** — create/supersede/tombstone; handle stability across revisions.
- **Discovery** — surface previews, cost hints, drill-by-handle, depth modifier, search.
- **Two-phase** — syntactically bad batch writes nothing; per-command errors isolated.
- **Branch** — companion `-llm` branches off correctly; empty bootstrap.
- **Merge** — deterministic merge produces no conflict; body union + header counter merge.
- **Determinism** — same inputs, different orders/replicas → identical merged result.
- **Stats merge** — G-Counters sum across replicas; LWW timestamp picks the max.

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
  coalesce counters; must be coordinated to not lose concurrent increments.
- **team-shared vs per-clone stats** — already merged via CRDT header; revisit if
  per-clone granularity turns out to matter.
- **duplicate signal** — none for v1; agent cleans up via CRUD.
- **AND-search / whole-store search** — `s` is OR + context-scoped for v1.
- **oversized-entry pagination** — expansion returns full body for v1; paginate huge
  entries later with a continuation handle.
- **extensible subjects** — fixed c/a/d/o enum for v1.
