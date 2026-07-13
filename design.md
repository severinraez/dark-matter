# Dark Matter — Design

> **Status:** design settled. Every decision below is made; there are no open **TBD**s.
> Items intentionally punted to a later version are collected in §11 (Deferred). This
> document is the source of truth for the model, the CLI, the storage format, and the git
> mechanics.

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
- **Companion branch** — for a code branch `B`, the shadow branch `llm/<B>` (§8.1).
- **Node** — a single addressable location in the shadow tree: a file or a folder.
- **Logical entry** — one note. Has a stable id, a subject, a body, links, and a
  revision history. Addressed by a **handle**.
- **Handle** — a short, stable, identity-derived reference to a logical entry
  (`#a3f9c1`), reusable across batches and across merges.
- **Replica** — a single clone/agent, identified by a local id, for CRDT counters.
- **Subject** — the kind of knowledge a note carries: code / architecture /
  development / operations.
- **LWW register** — a single-valued field merged **last-write-wins**: when two
  branches set it concurrently, the merge keeps the value with the higher position in a
  **total order** (so every replica picks the same winner) and discards the other. Two
  order sources are used: the header `t` register merges by **max timestamp** (equal
  timestamps carry the same value, so no tiebreak is needed), while body-log resolution
  (current body, anchor, move destination) orders by the winning record's `<rec-id>`
  ULID (§7.3). Everything else in the header is a set/counter that unions.

---

## 2. Data Model

### 2.1 Shadow tree & the `B` / `llm/<B>` companion branch
For every code branch `B` there is a companion branch `llm/<B>` whose tree mirrors `B`'s
directory structure but contains only notes — no source. A code file `foo/bar.rb` maps
to a note file at the same path `foo/bar.rb` in the shadow tree. (Storage scheme: §8.1.)

Git lifecycle is mirrored:

| Code op                     | Shadow op                          |
|-----------------------------|------------------------------------|
| branch `B'` from `B`        | branch `llm/<B'>` from `llm/<B>`   |
| merge `B` into `main`       | merge `llm/<B>` into `llm/main`    |
| new branch, no companion    | `llm/<B>` starts empty             |

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

> **Decision — folder-note storage.** A folder is a git *tree* in the shadow branch and
> cannot also be a blob, so a folder's own notes live in a **well-known file inside it**:
> notes homed at `api/` are stored at `api/.dm`, repo-root notes at `/.dm`. (Distinct
> from the per-clone `.dm/` home, which lives inside the git dir and is never tracked,
> §8.1.) The name is reserved: a code file literally named `.dm` cannot carry notes —
> `dm` errors — an accepted limitation. The reserved file also gives the blob-vs-tree
> merge case a deterministic rule: when one branch's shadow has a *file* note at `p` and
> the other has a *tree* (the code turned file `p` into directory `p/`), the file's log
> folds into `p/.dm`; entries keep their ids, and the next `dm pre-commit` surfaces them
> for the agent to re-home properly.

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
time** — dm grabs a couple more chars when minting if the handle already exists in the
merged store. The agent therefore always sees a stable, reusable handle. The one case
write-time extension cannot see — two replicas concurrently minting the same handle for
different entries, discovered only at merge — is handled at resolve time: a handle that
matches several entries errors with the extended candidates
(`✗ #a3f9c1 ambiguous: #a3f9c1k2 · #a3f9c1p9`), and surfaces display the extended forms.

> **Decision:** internal ids are **ULIDs** — globally unique without coordination,
> lexicographically sortable by creation time (a natural tiebreak), and independent of
> content so a supersede keeps the entry's id. A ULID is 48 bits of millisecond
> timestamp + 80 bits of randomness (26 base32 chars: 10 time + 16 random). The handle
> is therefore **not** a prefix of the ULID — the leading chars are wall-clock, so every
> id minted in the same ~4-minute window would share them — but the first 6 chars of the
> **80-bit random tail** (30 bits of entropy), extended on the rare write-time collision
> as above. Using the whole ULID as the handle was considered and rejected: handles are
> the densest element of every surface line, and 26 chars vs 6 is a ~4× token tax on
> exactly the output the design optimizes.

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

> **Accepted cost:** a single typo rejects the whole batch, reads included, and the
> agent re-sends everything. Answering the valid reads while rejecting the malformed
> line was considered and kept out: it would make a batch's effects depend on which
> lines happened to parse, and "all or nothing at the syntax layer" is a simpler
> contract for the agent than a partially-answered batch. Semantic failures are already
> per-command (phase 2), so the tax is paid only on genuine syntax errors — rare, and
> worth the occasional re-send.
>
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
| `mv`| `mv:old:new`            | relocate notes; folders: recursive (§7.3)|
| `rm`| `rm:path`               | tombstone all entries at path (recursive)|
| `al`| `al:#a:#b[:note]`       | add link between two entries, opt comment|
| `dl`| `dl:#a:#b`              | delete a link between two entries        |

### 3.3 Output format
One block per command, in input order. Blocks are delimited by **printable sentinel
glyphs at line start**, and content is emitted **raw** (no indentation) to save
characters:

- **next-marker** `▸` (U+25B8) prefixes each command echo; it starts a block and ends
  the previous one.
- **end-marker** `◾` (U+25FE) prefixes the terminal footer.

> **Decision — printable glyphs, not control bytes.** ASCII RS/GS (`0x1E`/`0x1D`) were
> considered — near-zero token cost, impossible in text — and rejected: agents read `dm`
> through terminals, shell captures, and API layers that routinely strip or mangle C0
> control bytes, which would corrupt the framing in exactly the channel the tool
> targets. The glyphs survive every such channel, are debuggable by eye, and cost ~1
> token each. Unambiguity is enforced at the write end instead: `dm` **rejects `▸` and
> `◾` in note bodies** (`✗` error) — they are pure decoration characters with no
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
`✓ #handle tombstoned`, `✓ #handle feedback +`, or `✗ #handle <error>`.

> **Decision:** use the Unicode structure glyphs (`→` link, `↑` parent, `←` linked-from,
> `★` arch, `⚠` stale) — visually unambiguous and collision-proof against content. **Match
> markers are dropped entirely**: search snippets carry no in-content highlighting, so no
> glyph ever needs escaping inside a body. The agent already has the term it searched for.

### 3.4 Error reporting

- **Batch-level** syntax failure → a single `!` line at the head of
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
When more fits than the budget allows, order by **proximity** (closer ancestors first)
→ **subject priority** (arch first for orientation). That is the whole v1 ranking — two
keys, fully deterministic, no tuning.

> **Decision:** v1 ranks on **proximity then subject priority only**. Staleness, recency,
> and the usage-stat signals (expansion rate, usefulness ratio) are *collected* (§6) but
> kept **out of the ranking** until there's data to weight them against — a scoring
> function with weights is deferred (§11) rather than guessed now. Stale entries are
> still *flagged* on read (§7.5); they just don't reorder for it in v1.

---

## 5. CRUD & the Append-Only Log

### 5.1 create / supersede / tombstone / keep
To keep merge = deterministic union, `u`/`d`/`k` never mutate in place. They append log
records against the logical entry; current state is a fold of that entry's log:
- `a` → append a **create** (`CR`) record — mints a new id/handle,
- `u:#handle:body` → append a **supersede** (`SU`) record — same handle, new revision,
- `d:#handle` → append a **tombstone** (`TB`) record — handle resolves to deleted,
- `k:#handle` → append a **re-anchor** (`RA`) record — re-stamps the staleness anchor
  (placeholder `@`, resolved at `dm commit`, §7.3), **no new body revision** (§7.5), so
  churn stats stay clean.

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
each note file** (§7.1) — except explicit feedback, which rides the body log as `FB`
records (§6.3). This means reads mutate the header; the cost is accepted in exchange for
stats that rank *and* travel with branches. Stat-only churn is **second-class dirt**: it
never blocks a git-facing verb and is auto-committed rather than gated (§8.3).

### 6.1 What's tracked (per logical entry)
| stat                 | signal                                                  |
|----------------------|---------------------------------------------------------|
| **impressions** (`i`)| times shown *collapsed* in a surface (weak)             |
| **expansions** (`x`) | times *opened* via `r:#handle` (strong — the real click)|
| **last-expanded**(`t`)| recency of genuine use                                 |
| **search-hits** (`h`)| surfaced as a match by `s` (distinct from impressions)  |
| **revisions / churn**| volatile vs settled knowledge                           |
| **feedback** (`+ - !`)| explicit agent signal (§6.3)                           |

The impression-vs-expansion split is core: surfacing is cheap, *opening* is the click.
Two more distinctions: **expansions** (`x`) is a *count* (how often opened) while
**last-expanded** (`t`) is a *timestamp* (how recently) — frequency vs recency, both
needed for ranking. And **revisions / churn** is **derived from the body log**, not a
stored counter: it is the count of `SU` records on the entry (`CR` = rev 1, each `SU`
+1). `RA` and `TB` are *not* revisions — only `SU` — so re-anchoring via `k` never
inflates churn, and because it folds from the unioned log it can't double-count on merge.

> **Decision:** `search-hits` is a **distinct counter**, bumped when `s` surfaces an
> entry as a match — *not* folded into impressions, and with **no attempt to correlate a
> search with a later expansion**. That correlation would span two separate `dm`
> invocations (search now, `r:#handle` later) with nothing tying them together, so it's
> not tracked. A search match and a digest impression are simply different events.

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
editor (`f:#f22e90:!:repo/ layer was removed in v3`). Feedback is a **body-log record**
(`FB`, §7.3), not a header counter: it carries the reason, unions like every other
record, and is content dirt (persisted by `dm commit`, §8.3). The per-entry `+`/`-`/`!`
tallies are *derived* by folding `FB` records, like churn. `⚠disputed` is defined as an
`FB !` record **newer (rec-id order) than the entry's latest `SU`/`RA`** — so a dispute
is cleared exactly by the housekeeping resolutions, `u` (rewrite) or `k` (re-bless),
each of which outranks the old complaint; a still-standing complaint re-flags by being
re-filed. Implicit signals still count (expansion = weak +, ignore = weak −); `f` is the
explicit, high-weight channel.

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
a3f9c1 i=A3:12,F2:4 x=A3:3 t=2026-07-07T09:12Z
b1c0e7 i=A3:5 t=2026-07-01T11:03Z
∎
CR 01J8Z…rec… a3f9c1 c 1a2b…sha… Schema uses single-table inheritance for tenants; the \
discriminator column is `type`.
SU 01J9A…rec… a3f9c1 c 9f01…sha… …now partitioned per tenant
CR 01J8Z…rec… b1c0e7 d 77c3…sha… Regenerate with `rake db:schema` after edits
FB 01J9B…rec… b1c0e7 + saved a broken schema regen
```

Content edits only append to the **body**; stat updates only touch the **header**. So
the knowledge stays diff-quiet — churn is confined to the header lines.

> **Decision — separators & multi-line.** Fields are separated by a **single space**
> (never cosmetic alignment padding — that would churn the header as values grow). Every
> record's fixed fields come first and are space-free (id, single-char subject, hex SHA,
> …), so the parser splits on the leading spaces and takes the **rest of the record as
> the body** — the same "body is the trailing field" rule as stdin (§3.1). Therefore
> `:`, spaces, and backticks inside a body need **no escaping**. Multi-line bodies use
> the same trailing-`\` continuation as stdin: a record spans physical lines until the
> first line not ending in `\`; the only escape is a literal trailing backslash, doubled
> `\\`. The header uses the same single-space rule between the id and its `key=value`
> cells.
>
> The fixed-field set of a body record is settled in §7.3: `<type> <rec-id> …`, where
> `<rec-id>` is a per-record ULID that supplies union identity, the LWW timestamp, and
> the tiebreak in one field — so there is no separate timestamp or replica column.

### 7.2 Header schema
- One row per logical entry, keyed by id.
- Columns: `i` impressions, `x` expansions, `h` search-hits (§6.1), `t` last-expanded.
  Feedback tallies are **not** header columns — they fold from `FB` body records (§6.3),
  which is what lets a dispute be cleared (a G-Counter could only ever grow).
- Each counter cell is a **G-Counter**: `A3:12,F2:4` = replica A3→12, F2→4, total 16.
  Missing field = zero.
- `t` is a last-write-wins timestamp register.

### 7.3 Body schema
Append log of records. Every record's first two fields are its **type** and its
**record-id** (a per-record ULID, see below); the rest are type-specific, body last:
- `CR <rec-id> <entry-id> <subj> <blob-sha> <body>` — create,
- `SU <rec-id> <entry-id> <subj> <blob-sha> <body>` — supersede,
- `TB <rec-id> <entry-id>` — tombstone,
- `RA <rec-id> <entry-id> <blob-sha>` — re-anchor: re-stamp the staleness anchor only (§7.5),
- `MV <rec-id> <from-path> <to-path>` — relocate: entries at `<from-path>` belong at
  `<to-path>` (§8.4). Path-scoped, not entry-scoped; applied as a sweep during the
  merge fold, not a per-entry edit.
- `LN <rec-id> <from-#> <to-#> [comment]` — link, with optional comment,
- `UL <rec-id> <from-#> <to-#>` — unlink,
- `FB <rec-id> <entry-id> <sig> [reason]` — feedback: `sig` ∈ `+`/`-`/`!`, optional
  free-text reason (body-last rule as usual, §6.3).
Links are their own append-only records — never a field edited inside a body — so they
merge by union like everything else. The `al`/`dl` commands are the **only** way to
manage links and the sole writers of `LN`/`UL` records; a link's current state folds
from those records (LWW on the pair key, §8.2). Back-links (`← linked-from`) are the
inverse index, computed on read.

**Decision — record identity & ordering.** `<rec-id>` is a **ULID minted per record**
(distinct from `<entry-id>`, the logical entry's ULID). One field does three jobs:
- **union merge** — the ULID is globally unique, so merging two logs is a set-union
  keyed by `<rec-id>`; identical records dedup, distinct ones all survive.
- **LWW timestamp** — a ULID embeds its 48-bit creation time, so the record *is* its own
  write-time; no separate timestamp field. Superseding an entry, re-anchoring, or moving
  resolves to the record with the **largest `<rec-id>`** for that entry/path.
- **deterministic tiebreak** — two records minted in the same millisecond order by the
  ULID's random tail. Every replica compares the same stored ULIDs and picks the same
  winner regardless of merge order, so no explicit replica-id is needed in the body log
  (replica-id lives only in the header G-Counters, §7.4).

So the body log carries **no timestamp or replica column** — both are subsumed by
`<rec-id>`. Two refinements make the order trustworthy:

- **Monotonic minting.** `dm` mints ULIDs in the spec's *monotonic mode* per process:
  within the same millisecond, each successive id increments the previous random tail
  instead of re-rolling it. This guarantees a batch's records order as written — an `a`
  followed by a `u` of the same entry in one batch can never fold backwards. Cost:
  essentially none; same-ms ids lose independence of their random bits, which only
  matters to the handle scheme, and handles are collision-checked at mint time anyway
  (§2.5).
- **Clock skew — accepted.** rec-id order is wall-clock order, so a clone with a fast
  clock wins concurrent LWW resolutions until its skew is corrected. Accepted for v1 and
  on the record here: resolution stays deterministic and nothing is destroyed — losers
  remain in the log as sibling revisions — and NTP-sane clocks make the window
  irrelevant in practice.

Tests pin the clock (ULID time bits) and the seeded id source (random tail),
so every LWW outcome is reproducible (§9.2).

**Decision — staleness anchor.** The `<blob-sha>` on `CR`/`SU`/`RA` is the git blob SHA
of the described file — git's own content address, so no separate hashing pass. It is
**not** stamped at write time: mid-session the code file is typically dirty, and
`HEAD:path` would anchor a fresh note to a pre-edit blob, flagging it stale the moment
the code commits. Instead a write records the placeholder `@`, and **`dm commit` stamps
every pending `@`** with `git rev-parse HEAD:path` of the code branch at that moment
(falling back to `git hash-object` of the working-tree file when the path isn't in HEAD
yet, so a placeholder never persists — rewriting one is safe only while the record is
uncommitted worktree state). A pending `@` anchor reads as fresh. Accepted assumption:
edits made *after* `dm commit` (a later rubocop pass, review fixups) flag the note stale
like any other drift, and the agent is expected to reconcile notes it just wrote — §8.4
scopes the gate to exactly these files. The current anchor folds as a **LWW register**
from the entry's `CR`/`SU`/`RA` records. It is re-stamped **only** by a deliberate
write — `a`, `u`, or `k` — never by merge, which would re-bless a note as fresh without
anyone confirming it still matches the code (§7.5).

**Decision — concurrent supersede.** When two branches supersede the same entry
concurrently, the current body resolves **last-write-wins** by the winning `SU` record's
`<rec-id>` (the ULID total order above), consistent with the anchor and move-path
resolution. Both `SU` records
survive in the log — the loser becomes a sibling revision, never a git conflict — so
nothing is destroyed and churn stats stay accurate. No keep-both fork is surfaced; the
merge always yields one clean current body.

**Decision — tombstone is terminal.** `TB` is absorbing, not a LWW participant: once any
`TB` record exists for an entry, the entry folds to deleted — a concurrent `SU` loses
regardless of rec-id order (its record survives in the log, as always, but resolves
nothing). There is no resurrection: `u`/`k`/`f`/`al` against a tombstoned handle fail at
write time (`✗ #a3f9c1 deleted`); knowledge worth restoring is re-created with `a`,
minting a fresh entry.

**Decision — `MV` lives on the destination file.** A move carries the whole note-file
log to the new path and appends `MV <from> <to>`, so a file that moved `a→b→c`
accumulates both `MV a b` and `MV b c` in its own log — the full provenance chain
travels with the file. On merge, the union of all `MV` records forms a redirect map
chased to a **fixpoint** (transitively composing `a→b→c`), sweeping any entries left at
an intermediate path — including ones a branch added there *unaware of the move* — to
the terminal path, which then unions by handle. This composes correctly no matter which
branch each hop came from, because the hops are just unioned log records.

**Decision — `MV` and `rm` are recursive for folders.** `mv:old/:new/` moves the entire
note subtree and appends a **single** folder-scoped record (`MV …rec… old/ new/`, the
trailing slash marks a folder) to the destination folder's `.dm` file (§2.2) — not one
record per descendant. In the redirect map a folder record redirects by
**longest-prefix match**, so it sweeps every entry under `old/` — including notes a
concurrent branch added at paths the mover never saw, which per-file records could not
cover — to the corresponding path under `new/`. A more specific record (a file moved
out of the folder individually) beats the folder prefix at that hop; ties are impossible
(two records for the *same* path resolve LWW by `<rec-id>` as usual). `rm:path` on a
folder likewise tombstones every entry homed at the folder node **and all descendants**
in one command.

**Decision — cycle guard.** Pathological concurrent moves (`a→b` vs `b→a`) could loop
the fixpoint chase. The transitive close tracks visited paths; if the chain revisits a
path, it stops and picks the cycle's terminal by the **same LWW total order** used
everywhere else (largest `<rec-id>` among the `MV` records in the cycle). Every replica
computes the identical winner, so it collapses deterministically with no new rule.

**Decision — `rm` discards the chain (accepted degradation).** `rm`-ing a terminal file
tombstones its accumulated `MV` provenance with it, so a much-later add at a now-orphaned
old path has nothing to redirect. This is caught by post-merge `dm pre-commit` (§8.4),
which already demands the agent resolve orphans — a graceful one-step fallback, and
correct behavior anyway since the path's whole lineage is genuinely gone.

### 7.4 Replica identity
G-Counters need a stable per-clone **replica id**, kept in **local config** under the
`dm`-managed `.dm/` home (`.dm/replica`, one per clone, never tracked, §8.1) so it never
merges. A wide random value (8 base32 chars from a CSPRNG at `dm init`) so collisions
never need coordinating.

> **Decision:** spend the bytes rather than fight collisions. The replica id is a wide
> random value (e.g. 8 base32 chars from a CSPRNG at `dm init`, not derived from
> `user.email`) so collision across any realistic team is negligible and needs no
> registry or coordination. The cost is a few extra bytes per populated G-Counter cell,
> which is cheap next to the note bodies and compresses well in git. One caveat:
> **never copy a clone** — `cp -r` duplicates `.dm/replica`, and two live clones sharing
> a replica id lose increments under the max-per-replica merge (§8.2). A copied clone
> must delete `.dm/replica` (or re-run `dm init`) so a fresh id is minted.

### 7.5 Staleness anchor
Each entry records the **git blob SHA** of the file it describes, stamped at
**`dm commit` time** from the code branch's HEAD (placeholder mechanics: §7.3). On read,
`dm` compares it to the current working-tree blob (`git hash-object`); a mismatch flags
the entry `⚠stale` — a pending `@` anchor reads as fresh — and ranking (§4.6) sinks
stale below fresh. Using git's own content address means no separate hashing pass, a
self-contained per-file anchor (no cross-branch history dependency), and a trivial merge
rule — the anchor rides `CR`/`SU`/`RA` and folds LWW (§7.3).

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

### 8.1 `-llm` branch mirroring — explicit commands

**Decision:** mirroring is **explicit**. The agent/user drives the shadow branches
through `dm` subcommands, run alongside the corresponding git op on the code branch —
no hooks, no implicit magic. This is the portable, testable choice, and it keeps `dm`
in control of shadow refs and merge semantics.

**Decision — ref storage: real branches under an `llm/` prefix.** A code branch `B` has
companion `refs/heads/llm/<B>` (`main` → `llm/main`, `topic/x` → `llm/topic/x`).
Companions are **real branches**, not a custom ref namespace (`refs/dm/…`), because only
`refs/heads/*` and `refs/tags/*` are reliably accepted, fetched, pushed, protected, and
PR-able across hosted providers (GitHub / GitLab / Bitbucket) — a custom namespace is
rejected or made invisible by most of them. The `llm/` prefix keeps companions grouped
and foldable in branch UIs so the list stays readable. `dm init` guards against a real
code branch colliding with the prefix. (CI must filter the `llm/*` ref pattern, or
pushing companions will trigger pipelines.)

**Decision — companions live in a `dm`-managed `.dm/` inside the git common dir.** A
companion is a *different* branch than the code it mirrors and cannot be checked out in
the code working copy. `dm` keeps its own **linked git worktrees** — one per active code
branch, each checked out to that branch's companion — plus its replica id and config, in
a `.dm/` directory located **inside the git common dir** (`git rev-parse
--git-common-dir`, e.g. `.git/.dm/`). Living inside the git dir means git never walks
into it from any working tree, so `.dm/` is invisible to `git status` in every code
worktree with **no exclude entry**, and it is inherently **per-clone, shared across all
code worktrees** — one `.dm/`, one replica id (§7.4). Layout:

```
.dm/replica         per-clone replica id + config
.dm/llm-main/       linked worktree → llm/main
.dm/llm-topic-x/    linked worktree → llm/topic/x   (code worktree on topic/x)
.dm/llm-fix-y/      linked worktree → llm/fix/y      (another code worktree)
```

One worktree per active code branch, directory named after the (slash-sanitised)
companion branch. All note reads, writes, and merges happen in these worktrees; the code
working copy is never touched and note/stat churn never appears in code diffs. Companions
are *not* nested beneath each code worktree: that would duplicate the replica id, inject
`.dm/` into secondary checkouts, and strand worktree registrations when a code worktree
is removed. (`git worktree add` accepts a working dir inside the common dir; the fallback,
if a target git version balks, is a sibling `.dm/` beside the common dir with one local
exclude entry.)

Subcommand surface (the batch stdin of §3 handles reads/writes; these are the git-facing
verbs):

| subcommand | action |
| --- | --- |
| `dm init` | create `.dm/` (replica id, config) and the first, **empty** `llm/main`; add the worktree |
| `dm checkout [B] [--from <llm-branch>]` | point the `.dm/` worktree at `llm/<B>` (current code branch if omitted), creating it from `llm/main` if missing — `--from` names a different parent (§8.5); **requires a content-clean worktree** — run `dm commit` first; pending stat dirt is auto-committed (§8.3) |
| `dm merge <src>` | deterministically merge the **local** `llm/<src>` into the current companion, into the worktree, **uncommitted** (§8.2); does **not** auto-fetch — run `dm fetch` first for a remote source |
| `dm commit` | commit the companion worktree — the **only** thing that persists notes and stat deltas (§8.3) |
| `dm push` / `dm fetch` | move the companion ref to / from `origin` (a plain `git push` never sees `llm/*`) |
| `dm pre-commit` | the orphan + stale reconciliation gate (§8.4) |
| `dm unmerged` | list companions whose code branch has landed in the current one but which aren't yet folded in; follow up with `dm merge` per entry (§8.5) |
| `dm prune <B>` | delete the landed companion `llm/<B>` (local + `origin`) and remove its `.dm/` worktree — shadow counterpart of `git branch -d B` (§8.5) |

`dm init` builds the empty `llm/main` with plumbing (empty-tree → `commit-tree` →
`update-ref`), **not** `git worktree add --orphan` (git 2.42+), to keep the version floor
low (§10).

**Decision — commit is agent-driven, merge is hands-off.** Content never auto-commits:
writes accumulate in the `.dm/` worktree, and the **agent is responsible for running
`dm commit`** to persist them; stat-only deltas are auto-committed as needed (§8.3).
Merging needs **no human**:
`dm merge` is deterministic (§8.2), so the receiving side never resolves a conflict —
after an explicit `dm fetch`, the companion merges hands-off. The merge lands as a
*parallel* commit on `llm/<B>` in the same PR / push event as the code merge — **never the
same commit** as the code, since it is a different branch. `dm merge` does not auto-commit
either; the uncommitted worktree state is the finished merge awaiting `dm commit`.

A missing companion bootstraps empty (a merge or branch targeting a code branch with no
`llm/` companion yet starts from an empty shadow tree).

**Decision — branch-alignment check on every invocation.** `dm checkout` binds the
companion once, but nothing stops a plain `git checkout` afterwards. So **every** `dm`
invocation (batch or subcommand) first verifies that the code worktree's checked-out
branch matches the active companion; on mismatch it refuses with
`✗ code branch is topic/y but companion is llm/topic/x — run dm checkout`. The check is
one ref read, and it closes the one silent-corruption footgun in the explicit-mirroring
model: notes read from — or worse, written to — the wrong branch's memory.

### 8.2 Deterministic merge algorithm

`dm merge` computes the merge **in-process** — it reads both branch versions of each
note file, merges each file per-region, and writes the result to the `.dm/` worktree (§8.1).
Because mirroring is explicit (§8.1), the merge does **not** rely on git invoking a
registered merge driver; `dm` owns the merge logic directly.

Per file, both regions merge with no human conflict:

- **header counters** (`i x h`): union replicas, **max per replica**, sum for value,
- **header `t`**: **max** timestamp (LWW),
- **body** (entry log): **union** of records by record-id.

No three-way base is consulted — every rule is base-free by construction: deletion is an
explicit `TB` record (never a missing file), moves leave an `MV` record at the
destination, counters carry per-replica maxima, and logs union by record-id. Two inputs,
one deterministic output, regardless of history shape.

**Worked example.** Base `llm/main` has `db/schema.rb` with entries `#e1` and `#e2`.
Branch X supersedes `#e1` (`SU` rec `…X5`) and tombstones `#e2` (`TB` rec `…X6`); branch
Y independently supersedes `#e1` (`SU` rec `…Y8`, minted later) and moves the file
(`MV …Y9 db/schema.rb db/core_schema.rb`). Folding X into Y's companion: union the two
logs (all five records survive); the redirect map sends every `db/schema.rb` record —
including X's, written unaware of the move — to `db/core_schema.rb`; `#e1`'s current
body is `…Y8`'s (largest `SU` rec-id; `…X5` remains a sibling revision); `#e2` is
deleted (`TB` is terminal, §7.3). Fold the branches in the opposite order and every
comparison sees the same stored ULIDs — identical result, no base, no conflict.

The merge is nonetheless recorded with **real ancestry**: the `dm commit` that persists
a `dm merge` writes a **two-parent commit** (current companion tip + merged `llm/<src>`
tip). Nothing in the fold needs it, but ancestry is what lets `dm unmerged` corroborate
folds (§8.5) and lets compaction decide when an `MV` or tombstone has sunk below the
merge-base of all live branches (§11).

Single-valued fields that can be concurrently written resolve **last-write-wins**, but
by two different total orders (§1.3): the header `t` register orders by wall-clock +
replica-id, while body-log resolution orders by the winning record's `<rec-id>` ULID —
the current body of an entry superseded on both sides (**`SU`-vs-`SU`**, §7.3 — both
`SU` records survive, the loser becomes a sibling revision) and the current staleness
anchor (`RA`, §7.5). The move-path redirect map composes by fixpoint (§7.3), falling back
to `<rec-id>` order only to break a cycle.

> **Decision — no merge driver.** A `.gitattributes` merge driver (to keep a *raw* `git
> merge` of `llm/*` deterministic) is **dropped for v1**: `dm` mediates every companion
> operation through its own `.dm/` worktree (§8.1), so git's merge machinery is never
> invoked for notes and no one raw-merges an `llm/*` branch by hand. The rule is simply:
> never `git merge` a companion — always `dm merge`. A hosted merge button would not run
> the driver anyway, so it was never protection on the hosted side.

### 8.3 Commit cadence — explicit `dm commit`, auto-committed stats

Both content writes and stat deltas (reads bump the header, §7.1) accumulate in the
`.dm/` worktree; a `dm` invocation applies all its deltas there once at the end but
**commits nothing**. Persisting content is a single explicit act: **`dm commit`**. The
agent runs it to finalize a session and must run it before any branch/merge/push op. It
also stamps pending `@` staleness anchors (§7.3) and, when persisting a `dm merge`,
records the merged companion tip as a second parent (§8.2).

> **Decision — content is gated, stats are not.** Worktree dirt is classified by region
> (§7.1): **content dirt** (body-log changes from `a`/`u`/`d`/`k`/`f`/`al`/`dl`/`mv`/`rm`)
> versus **stat dirt** (header-only counter/`t` bumps from reads). Content never commits
> on its own — it coalesces in the worktree and is persisted only by an explicit
> **`dm commit`**; the agent is responsible for committing before it walks away. To keep
> the invariant "nothing merges or pushes uncommitted" without magic, `dm checkout` /
> `dm merge` / `dm push` / `dm pre-commit` **refuse on content dirt** and tell the agent
> to `dm commit` first, rather than silently committing for it. Stat dirt is exempt:
> reads must not create commit obligations, so a mere `r:` never blocks a git-facing
> verb. Any verb that needs a clean tree first **auto-commits pending stat-only changes**
> as a `dm: stats` commit, and `dm commit` sweeps them in with the content. Header-only
> auto-commits are safe precisely because the header is a CRDT (§8.2) — they can never
> conflict, and they contain nothing an agent would want to review before persisting.
> Commit churn stays on the companion branch (§7.1) and is compacted later (§11).

### 8.4 Renames & orphaned notes — agent-driven reconciliation

**Decision:** the **agent tracks renames**, not dm. dm does not watch the code tree
or run git rename detection; instead it provides a **pre-commit gate** that forces
the agent to reconcile the shadow tree against the code tree before every shadow
commit.

**`dm pre-commit`** compares the shadow tree against the current code working tree
and lists every **orphaned node** — a note-bearing path (file or folder) whose mirror
path no longer exists in the code tree. Orphans are **collapsed to the highest orphaned
ancestor**: a renamed or deleted directory reports one line
(`api/ — 12 note-bearing paths`), not twelve. The gate exits non-zero while any orphan
is unresolved, so the agent must act. Each orphan is resolved one of two ways:

- **`mv:old:new`** — the code moved; relocate the node's notes to the new mirror
  path (recursive for folders, §7.3). Entry handles are preserved (§5.2), so links and
  back-links survive.
- **`rm:path`** — the code was genuinely deleted; tombstone the node's entries
  (recursive for folders). This is the node-level counterpart to `d:#handle` (which
  tombstones one entry): it tombstones every entry homed at a vanished path in one move.

Resolution can be **iterative**: after a recursive `mv:api/:svc/`, a descendant that
actually moved somewhere else (`api/x.rb` → `lib/x.rb`) is still orphaned — now at
`svc/x.rb` — and surfaces when the gate is re-run, to be fixed with its own `mv`. No
extra machinery: the gate already loops until it exits zero.

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
move with the largest `<rec-id>`, resolving conflicting destinations (`a→b` vs `a→c`) by
that same ULID total order (§7.3/§8.2). The `MV` record is what makes move-vs-edit safe: a branch that
added notes at the old path *unaware of the move* has them swept to the new path on
merge instead of orphaning at a dead location. So a "moved" pointer does survive — but
only as **internal merge metadata**, never an agent-facing redirect: reads never
resolve an old path (§4), and once an `MV` record sinks below the merge-base of all
live branches it is compacted away (§11), like a tombstone.

> **Decided:** `dm pre-commit` reports **orphans and scoped-stale notes** (both a hard
> gate). Detecting *new* code files with no shadow node (to nudge note-taking) is out of
> scope for v1.

### 8.5 Branch lifecycle — two loops

The verbs above split into two cadences, and naming that split is the whole operating
model. **`dm` is entirely agent-operated:** the developer runs `dm init` once per clone
and otherwise does normal git; the agent drives every other `dm` op, mirroring each git
op with its shadow counterpart *at the moment it happens* (no hooks, §8.1).

- **Inner loop** — per-file, continuous, git-unaware. Read-before-touch, write-on-learn.
  Writes accumulate uncommitted in the worktree; nothing persists until `dm commit` (§8.3).
- **Outer loop** — per-session / per-branch, git-bound. The `checkout / pre-commit /
  commit / push / merge / unmerged / prune` verbs fire at branch boundaries.

| Lifecycle moment | Code side | Shadow side (agent runs) |
| --- | --- | --- |
| clone setup | `git clone` | `dm init` (once) |
| start work on a branch | `git checkout -b topic/x` | `dm checkout` (infers branch, creates companion from `llm/main` if missing; `--from` overrides) |
| working | edit + `git commit` | **inner loop**: `r:` before touching, `a`/`u`/`d`/`f`/`al` on learning |
| wrap up / open PR | `git push` | `dm pre-commit` → resolve orphans (`mv`/`rm`) + scoped-stale (`k`/`u`/`d`) → `dm commit` → `dm push` |
| land locally | `git merge topic/x` into `main` | `dm merge llm/topic/x` → `dm commit` → `dm push` → `dm prune topic/x` |
| land via hosted button | PR merged remotely | (later, on `llm/main`) `dm unmerged` → `dm merge` each → `dm commit` → `dm push` → `dm prune` |

**Inner-loop discipline.**
1. **Read before touching** a file: `r:path` for the surface, `r:path:1` to orient with
   parent/arch notes inline, or `s:path:t1|t2` to grep the assembled context when a file
   sits under many arch notes.
2. **Write when you learn something durable** you'd want back on this branch later — a
   gotcha, a non-obvious constraint, a why-it's-this-way — not a restatement of code.
3. **Correct what you find wrong**: `u` to fix, `d` to remove, `f:#h:!:reason` to flag
   when you can't fix now, `k` to re-bless a stale-flagged note you've verified.

**Decision — companion parent on `dm checkout`.** When `dm checkout` must create a
missing `llm/<B>`, it branches from **`llm/main`** by default (an empty shadow tree if
even that is missing); an optional parameter names a different parent explicitly —
`dm checkout topic/child --from llm/topic/parent` — for a topic branched off another
topic. Fork-point *discovery* (guessing the parent via `git merge-base`) is dropped: it
cannot distinguish topic-off-topic from topic-off-main without per-repo configuration,
so the default covers the common case and the exception is one explicit flag.

**Decision — `dm unmerged` lists what to fold.** The founding invariant (§2.1) requires
every code merge into `main` to be mirrored by `llm/<B>` → `llm/main`. Local merges are
mirrored directly (`dm merge` right after `git merge`). Merges via a **hosted PR button**
are observed by no local agent, so — run on `llm/main` after pulling `main` — `dm
unmerged` **lists** every companion `llm/<B>` whose notes are not yet in `llm/main`,
marked **landed** (`✓`, detection below) or **undetermined** (`?`). It does **not** fold
anything itself: the agent
follows up with `dm merge llm/<B>` per listed entry, then `dm commit` → `dm push` →
`dm prune <B>`. Keeping detection (`unmerged`) separate from the mutation (`merge`) keeps
`dm` free of the one hands-off write the rest of the model avoids, and lets the agent skip
a companion it recognizes as abandoned.

**Detection — true-merge flows only.** Hosted providers often **rebase or squash**
before merging, so `B`'s commits are rewritten and `B`'s tip is *not* an ancestor of
`main` — `git branch --merged` misses it. v1 auto-detects only what is unambiguous:
`git log --merges --first-parent` over `main` since the companion's last fold names the
source branch in the subject (`Merge branch 'topic/x'`,
`Merge pull request #N from …/topic/x`), and the merge commit's second parent
corroborates by ancestry. A companion matched this way is listed **landed** (`✓`) —
definitive, since a rejected PR never produces a merge commit on `main`. Every other
unfolded companion is listed **undetermined** (`?`): squash/rebase merges leave no merge
commit, and the tempting fallback — "the code branch was deleted on `origin`" — cannot
distinguish a merged PR from one closed without merging, so v1 does not guess.
**Accepted caveat:** on squash/rebase flows the agent must know from outside git (the
PR, the user) that a `?` companion landed before folding it. Folding early is harmless
for a branch that *does* eventually land — the fold is an **idempotent CRDT union**
(§8.2), so a re-fold yields the identical result — but folding a branch that never
lands pollutes `llm/main`, which is exactly why `?` entries are never auto-folded. The
CI end state (§11) sidesteps detection entirely: the merge event names the source
branch, so CI can `dm merge llm/<that>` directly regardless of rebase/squash.

**Decision — `dm prune <B>`.** After `llm/<B>` is folded into `llm/main`, `dm prune <B>`
deletes the companion branch (local + `origin`) and removes its `.dm/` worktree — the
shadow counterpart of `git branch -d B`. Always agent-invoked, one companion at a time.

### 8.6 Agent integration — the `agents.md` prompt

This is the canonical block a consuming repo copies into its own `AGENTS.md` so the agent
reads `dm` output correctly and follows the lifecycle. It is deliberately concise.

> #### Dark Matter (`dm`) — your memory about this codebase
>
> `dm` stores notes *about* the code (gotchas, architecture rationale, dev/ops know-how)
> on a shadow git branch that tracks your current branch. Read before you touch a file;
> write when you learn something you'd want back later.
>
> **Read first.** Before editing `foo/bar.rb`: `r:foo/bar.rb` returns a *surface* — own
> notes (one-line previews), parent/arch notes, links, and an inventory footer. It's a
> map, not a dump. Drill with `r:#handle` (full body of one entry) or `s:foo/bar.rb:t1|t2`
> (grep the file's whole note-context; `|`=OR, `+`=AND).
>
> **Write on learning.** `a:path:subj:body` (subj: `c` code/behavior · `a` architecture ·
> `d` dev/build/test · `o` ops). `u:#handle:body` supersedes · `d:#handle` removes ·
> `k:#handle` re-blesses a stale note · `f:#handle:!:reason` flags one wrong.
>
> **Link related notes.** Links are the only way notes relate — `al:#a:#b[:why]` connects
> two entries (directed `a→b`, optional comment); `dl:#a:#b` removes one. Link a note to
> the ones it depends on, contradicts, or elaborates: an architecture note (`a`) links to
> the files it governs so they surface it as `← linked-from`; a gotcha links to the note
> explaining the underlying cause. Follow any `→`/`←` you see with `r:#handle`.
>
> **Output.** Blocks split on `▸` (command echo, then raw content); `◾` ends the stream.
> Glyphs: `→` link · `↑` parent note · `←` linked-from · `⚠stale` (file changed since the
> note) · `⚠disputed` (flagged wrong). `(+N lines)` = hidden size. Handles `#a3f9c1` are
> stable — copy them into later commands.
>
> **Batch over stdin**, one command per line, body always last (`:` in a body needs no
> escaping; trailing `\` continues to the next line):
> ```
> dm <<EOF
> r:api/handler.rb
> a:api/handler.rb:c:Validates tenant header before dispatch
> EOF
> ```
>
> **Session lifecycle** — mirror your git ops: start / new branch → `dm checkout`; before
> a PR → `dm pre-commit` (resolve orphans with `mv`/`rm`, stale notes with `k`/`u`/`d`)
> then `dm commit` then `dm push`; when your branch merges to `main` → `dm merge
> llm/<branch>` then `dm commit && dm push` (after a hosted merge, run `dm unmerged` on
> `llm/main` and `dm merge` each listed companion). **Nothing persists until you run `dm
> commit`** — it saves both your notes and read-stats, so commit before any merge/push and
> at session end.

---

## 9. Testing

### 9.1 E2E harness
Tests set up a **real git repo**, drive `dm` via stdin, and assert on both stdout and
the resulting shadow-tree state. This is the primary test strategy.

### 9.2 Fixture repo & the base branch
All scenarios branch off a single **well-known base**: `base` (code) + `llm/base`
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
- **Branch** — companion `llm/*` branches off correctly; empty bootstrap.
- **Merge** — deterministic merge produces no conflict; body union + header counter merge.
- **Determinism** — same inputs, different orders/replicas → identical merged result.
- **Stats merge** — G-Counters sum across replicas; LWW timestamp picks the max.
- **Renames** — `dm pre-commit` lists orphans; `dm mv` relocates notes preserving
  handles/links; `dm rm` tombstones a deleted node's entries.
- **Guard** — the fixture build commands (`a`, seed path) produce the expected base.

---

## 10. Tech Stack
`dm` is a single **Go** binary. No runtime dependencies beyond git.

> **Decision — minimum git 2.15.** The merge is custom (CRDT union, §8.2), so `dm` never
> uses git's textual 3-way merge or `git merge-tree --write-tree` (git 2.38+) — it only
> reads blob versions and writes trees, ancient plumbing. The floor is set by `git
> worktree` robustness (§8.1, §9.2) at ~2.15, and `dm init` creates the empty companion
> with plumbing rather than `git worktree add --orphan` (git 2.42+) to stay there. No
> separate merge driver or hooks ship — `dm` mediates everything itself (§8.1 / §8.2).

---

## 11. Deferred / Open Questions
- **budget-fill retrieval** — `r:path` auto-expands top-ranked items to fill ~N tokens;
  wait until usage-stat ranking makes "dm decides" trustworthy.
- **weighted scoring function** — v1 ranks on proximity + subject priority only (§4.6);
  folding staleness, recency, and usage signals (expansion rate, usefulness ratio) into a
  weighted score waits until there's real usage data to tune the weights against.
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
- **CI-driven merge-to-main** — v1 reconciles hosted PR-button merges with the local
  `dm unmerged` + `dm merge` flow (§8.5); the scalable end state is a CI job on code
  `main` that folds the merged branch's companion into `llm/main` headless. CI sidesteps
  rebase/squash detection entirely — the merge event names the source branch, so it runs
  `dm merge llm/<that>` directly. Deferred: it's infra, and the job must be scoped so `llm/*` pushes
  don't themselves trigger pipelines (§8.1).
- **staleness fidelity** — *not* deferred; settled as an **accepted limitation** (§7.5):
  blob-SHA drift detects byte-change, not wrongness, so false positives/negatives exist
  and are lived with. Listed here only so the known gap is on the record.
