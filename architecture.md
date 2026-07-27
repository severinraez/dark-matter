# Dark Matter — Architecture

> **Status:** architecture pass **complete** — topmost layer (§1–§4),
> Evidence port (§5), core decomposition (§6), app use-cases (§7), cli
> internals (§8), repo layout (§9), fixture/E2E threading (§10). Remaining
> constants (bands, floors, crowding threshold) are design/Q1–Q3 matters
> (design §14), not architecture. Companion to [design.md](design.md); bare
> section references (§N) point there.

## 1. Shape — hybrid: functional core, one port

Pure domain core with a single core-owned port (`Evidence`); an app layer owns
all imperative choreography, calling concrete adapters directly. Hexagonal
inversion exactly where the design structurally demands it (resolution and the
matcher ladder interleave policy with demand-driven git queries — §9.1, §9.4);
plain pure functions everywhere else; no ceremony ports.

```
cmd/dm ─► cli ─► app ──────────────► core   (pure + Evidence port)
                  │                    ▲
        ┌─────────┼─────────┐          │ implements
      store ───► gitio    local ───────┴── evidence.Cached
      (layout,   (plumbing, (pending,       (gitio + local.cache,
       tip⇄recs)  transport) cache,          wired by app)
                             replica, lock)
```

Rejected at this altitude (rationale in the planning session, kept short here):

- **Classic hexagonal** — core owns ports for *all* effects (Store, Pending,
  Cache, Clock too). Buys deterministic fakes for the sync/gc race loops, at
  the price of ~5 interfaces with one production implementation each; §11.1's
  E2E-primary strategy leaves those fakes few consumers.
- **Pure FCIS** — interface-free core, shell owns all control flow. Splits the
  resolution/ladder control flow (itself precision-critical policy — "m2/m3
  only at mint moments", "layer 4 fires only where 1–3 fail") from its guards
  across the boundary.
- **Git-kernel layered** — one unified git+storage module. Entangles the two
  hardest, most test-pinned pieces (fold semantics, matcher ladder) with git
  calls; the Q1/Q3 calibration rigs (§14) need a pure core to replay policy
  over recorded evidence.

## 2. Modules — topmost layer

| module | owns | explicitly does NOT own |
|---|---|---|
| **cli** | batch grammar (two-phase parse, `$N`, %-decoding — §4.1/§4.2), output rendering (glyphs, blocks, acks, footer — §4.3/§4.4), subcommand dispatch (§10.1) | any semantics — consumes/produces structured values only |
| **app** | one use case per verb (batch, init, sync, worklist, gc, dump); invocation lock (§8.2); the read-transaction (store ∪ pending view + stat deltas); sync's fetch→mint→fold→push retry loop (§8.4); gc's CAS loop (§8.7); all wiring | policy — every decision it enforces is a core call |
| **core** | record model + **canonical codec** (§8.3); fold (LWW, tombstone-absorbing, rescue, per-record rule (b)); state classification (§2); ranking (§5.6); follow bands & heuristics (§9.2/§9.3); **matcher ladder** (§9.4); union merge + epoch rule (§8.4); sweep rules (§8.7); handle derivation (§3.5); defines `Evidence` | I/O of any kind; imports nothing |
| **gitio** | faithful plumbing wrapper (§12): hash-object, ancestry, similarity diff, patch-id, reflog, refs, commit-tree/update-ref, fetch/push; primary `Evidence` implementor | policy — no thresholds, no guards, no interpretation |
| **store** | store tree layout (§8.1: record shards, stats files, blobs, epoch marker); tip ⇄ record-set materialization; candidate commit builds; rides gitio for object/ref plumbing | merge/sweep *decisions* (core's); transport (gitio's) |
| **local** | `.git/.dm/*` (§8.2): pending records/blobs/stats, replica id, cache file mechanics (atomic build+rename), lock | cache *keys/invalidation semantics* (dictated from above) |

**evidence.Cached** — the concrete evidence value app wires and hands to
core: a caching decorator composing gitio (raw queries) with local's cache,
implementing all three role interfaces of the port (§5). Core stays
cache-oblivious, which keeps the cache honestly "disposable, never
load-bearing"; gitio stays policy-free. Lives at `internal/evcache` (§9): an
own adapter-row package, because it composes two other adapters and carries
real logic of its own — the ff-aware, delta-scoped unreachability
revalidation (§8.2) — with dedicated tests. (Named `evcache`, not
`evidence`: that package name is taken by `core/evidence`.)

## 3. Dependency rules

Import direction is the enforcement mechanism:

- **core imports nothing** (stdlib only).
- Adapters (**gitio**, **store**, **local**) import core's types; **store**
  additionally imports **gitio**. Never the reverse.
- **app** imports everything below it; **cli** *calls* only **app**, but may
  import core's pure data types read-only (the §6 read-model: `Surface`,
  `WorklistReport`, acks — no mirror DTOs); **cmd/dm** imports cli.
- At compile time the `Evidence` dependency points upward (implementor →
  interface); at runtime, core calls downward through the interface.

Guards that are review rules, not compiler-checked (the hybrid's known rot
mode is policy leaking into app):

1. **App sequences, core decides** — app may loop, retry, lock, and order
   effects; any decision comparing domain values belongs in core.
2. **gitio interprets nothing** — it answers questions git can answer;
   thresholds, bands, and guards live in core.
3. **Deleting `.git/.dm/cache/` must be unobservable** to core-level results
   (§8.2's disposability, restated as an architectural invariant).

## 4. Fixed boundary decisions

1. **Core is pure and owns the codec.** Byte-identity is a CRDT invariant
   (union dedup, canonical path encoding — §8.3), so serialization is domain
   policy, not adapter plumbing. Golden fold/codec tests target core directly.
2. **Resolution + matcher ladder live whole in core** behind the single
   `Evidence` seam. Their control flow is inseparable from their guards; the
   port exists precisely so neither smears across a boundary.
3. **Policy/mechanics split for the store.** Union merge, the epoch rule, and
   sweep droppability are core; tree layout and commit builds are the store
   adapter; transport is gitio. Store is its own module so gitio remains
   describable as policy-free (it is also the Evidence implementor).
4. **Semantics/rendering split.** cli holds grammar + rendering only; app
   returns structured results. Golden-output tests (§11.4) pin cli; semantics
   tests pin app/core.
5. **Determinism hooks are injected values, not ports.** Clock, id source,
   replica id are already env-var overrides by design (§11.2) — config, not
   architecture.
6. **Imperative loops are tested E2E.** Sync's retry and gc's CAS race
   coverage rides the §11 harness (real clones, deterministic interleaving),
   accepted in exchange for not maintaining fake Store/Pending ports.

## 5. The Evidence port — contract

Fixed in the second planning iteration. The port is **three core-owned role
interfaces**, not one — signatures then document what each core function
actually consumes, and the Q1/Q3 calibration rigs fake only `Match`+`Tree`.
`evidence.Cached` implements all three. **Role-edge rule:** a method belongs
to the role of the *question it answers*, never the consumer — consumers
freely take several roles (folder resolution takes `Tree` and `Match`).

An evidence value is **bound to one checkout snapshot** (HEAD + working tree)
for one locked invocation (§8.2); mint-pass targets (other lines' tips) are
method parameters, never new snapshots.

```go
// core-owned types throughout; adapters translate.

type Lineage interface {
    // rule (b) step 1, batched — one rev-list walk classifies all origins.
    LandedInHead(origins []SHA) (Set[SHA], error)
    // rule (b) step 3, batched; Ref carries the worklist's branch hint.
    // Decorator applies the ff-aware unreachability cache (§8.2).
    ReachableFrom(origins []SHA) (map[SHA][]Ref, error)
    // worklist line-grouping among a small set of dead origins (§9.6).
    IsAncestor(a, b SHA) (bool, error)
}

type Tree interface {
    WorkingBlob(path Path) (*SHA, error)            // rule (a) L1; nil = absent
    PathsOf(blob SHA) ([]Path, error)               // rule (a) L2, incl. working-tree dirt
    TreeAt(commit SHA, path Path) (*TreeFP, error)  // folder fingerprint: tree SHA + members (§9.3)
    PathsOfTree(tree SHA) ([]Path, error)           // folder L2: pure-move detection
}

type Match interface {
    // THE batched origin→checkout diff (-M -C); one call per distinct
    // origin (Q2's diff-multiplicity is visible here). Generous fixed
    // floor; raw scores — core applies the §9.2 bands/margins.
    RenamePairs(origin SHA) ([]RenamePair, error)
    // direct blob-vs-candidate scoring (dirty working tree, §8.3).
    Score(blob SHA, candidates []Path) (map[Path]int, error)
    // rewrite forensics (matcher ladder, §9.4):
    ReflogEntries() ([]ReflogEntry, error)          // {ref, old, new, action}; core filters (m1)
    MergeBase(a, b SHA) (*SHA, error)
    Segment(base, tip SHA) ([]Commit, error)        // (base..tip] in order, empty-diff marked (m2)
    PatchID(commit SHA) (*PatchID, error)           // canonical pinned diff flags
    RangePatchID(base, tip SHA) (*RangeID, error)   // cumulative patch-id + diff size (m3 + floor)
}
```

**Fixed decisions:**

1. **Facts generous, judgment in core.** Every threshold, band, margin,
   floor, and action-filter lives in core; adapters return raw scored/labeled
   facts down to a hardwired generous floor. ("gitio interprets nothing" made
   concrete; lets Q1/Q3 re-run band policy over recorded evidence without
   re-diffing.)
2. **Batch-shaped where evidence is naturally batched** (ancestry and
   reachability take sets; one `RenamePairs` per distinct origin because one
   diff yields all pairs). Q2's read-cost model is contract-visible.
3. **Deliberately outside the port:** records, stats, pending, refs
   snapshot, and HEAD identity are *data*, passed into core as arguments;
   write-time anchoring (`hash-object` at `a`/`u`/`k`) is app→gitio directly;
   transport is app↔gitio, and m1r's forced-update `(old, new)` pairs exist
   only at fetch time, so app passes them into the mint pass as candidate
   data; `vd` dispositions are records.
4. **Cache placement across the port** (refines §2): §8.2's
   `match-<origin>-<tree>` memo ↔ `RenamePairs` and the unreachability cache
   ↔ `ReachableFrom` sit **below** the port, inside `evidence.Cached`. The
   `nomatch-<tip>-<target-tip>` memo caches a *failed ladder attempt* — a
   core conclusion, not a raw query — so it sits **above** the port:
   app-level memoization of a mint-pass result, stored via `local`.

## 6. Core — internal decomposition

Fixed in the third planning iteration: **concern packages along the
dependency DAG**, chosen over one flat package (no enforced internal
boundaries, one giant test namespace) and over a model/engine split by port
contact (lumps unrelated concerns; cuts by *how* code runs, not *what it's
about*).

| package | owns | Evidence roles | imports |
|---|---|---|---|
| `core/record` | record types, canonical codec, ULID minting rules, handle derivation + collision extension (§8.3, §3.5); shared value types (SHA, Path) | — | — |
| `core/evidence` | the three §5 port interfaces + their value types; interfaces only, no logic | defines them | record |
| `core/lineage` | rule-(b) **classification** (VD chains, unlanded voiding, qualified/degraded guard) *and* the **matcher ladder** (m1–m3 guards, mint pass, floors) (§9.4) | Lineage, Match | record, evidence |
| `core/fold` | per-entry fold: LWW, tombstone-absorbing, rescue fold, per-record rule-(b) **consumption**, FB/dispute + churn derivation (§8.3, §7) | — (data-in) | record |
| `core/resolve` | rule-(a) file layers 1–5, folder fingerprint/follow vote, bands + margins, five-state entry classification (§9.1–§9.3) | Tree, Match | record, evidence, fold |
| `core/view` | ranking, search term matching, surface/digest assembly, worklist assembly + line grouping, crowding threshold (§5.5–§5.6, §9.6) | Lineage | record, evidence, fold, resolve |
| `core/union` | store-set policy: union merge, epoch rule, sweep droppability incl. rescue pins + VD retention, stats-file merge (§8.4, §8.7, §7.2) | — | record |

```
record ─► evidence ─► lineage ─┐  lineage feeds fold as DATA:
   │          │                │  map[origin]LandedState
   ├────────► fold ◄───────────┘
   │          │
   ├────────► resolve ─► view
   └────────► union
```

**Fixed decisions:**

1. **`fold` is data-in.** Rule (b) splits into two activities: *classifying*
   origins (consuming VDs + `Lineage`) and *minting* verdicts (the ladder).
   `lineage` does both; `fold` receives the classification as a plain map and
   never touches a port. The DAG stays acyclic and the single most
   test-pinned semantic (the §8.3 per-record fold) unit-tests with zero
   fakes.
2. **Classify + mint cohabit `core/lineage`** — shared VD semantics
   (chains, voiding, guard) outweigh their different call sites (every read
   vs. mint moments).
3. **Role-edge rule in practice:** worklist line-grouping lives in `view`
   and takes `Lineage` — the question is ancestry, the consumer is the
   worklist.
4. **Stats split by nature:** tally/churn *derivation* in `fold` (folds from
   records), stats-file *merge* in `union` (store-set policy).
5. **`view` owns the read-model** (`Surface`, `WorklistReport`, acks) that
   app returns and cli renders under the relaxed import rule (§3).

## 7. App — use-case shapes

Fixed in the fourth planning iteration. Six verbs as thin choreographies over
one shared abstraction — the **Session**, app's per-invocation context
(§8.2's invocation scope made concrete):

```
Open() → Session:
    lock .git/.dm/lock (wait; holder notice after ~1s)     §8.2
    resume monotonic rec-id minting from pending           §8.2
    snapshot: HEAD, refs fingerprint, working tree         §5 E3 binding
    store tip + index (rebuild on miss)                    §8.2
    scan pending → overlay (reads see store ∪ pending)
    wire evidence.Cached(gitio, local.cache)
    empty stat-delta accumulator
Close():
    flush stat deltas → pending/stats (one write)
    release lock
```

| verb | choreography |
|---|---|
| **batch** | Open → m1 mint (cheap reflog scan, §9.4) → execute commands in order → Close |
| **sync** | Open → fetch (collect forced-update pairs = m1r candidates) → mint pass → retry loop: read tip → `union.Merge` (epoch rule) → build commit → push; landed push clears pending → health line via `view` → Close |
| **gc** | Open → fetch → `union.Sweep` → orphan commit, epoch+1 → `--force-with-lease` push; lease rejection re-enters loop → Close |
| **worklist** | Open → whole-store classify/fold/resolve → `view.Worklist` (session-scoped lead from pending + stat accumulator, §9.6) → Close |
| **init** | create `.git/.dm/`, mint replica id, configure refspec, fetch-or-create store — no Session |
| **dump** | Open → raw store ∪ pending, deterministic print — bypasses `view` (§11.3) |

**Fixed decisions:**

1. **Two-phase boundary = cli/app boundary.** Phase 1 (syntax: grammar,
   %-decoding, `$N` validation — batch-rejecting, §4.1) is cli, producing
   typed commands. Phase 2 (execution, per-command `✗`) is app. **Handle
   resolution is semantic**: it resolves against the visible set (§5.3),
   needing the whole read machinery — unknown/ambiguous handles are phase-2
   per-command errors, never batch rejections; `$N` is pure positional
   syntax, phase 1.
2. **Read-your-writes via the pending overlay** — later commands in a batch
   see earlier writes; "store ∪ pending" applied within one invocation.
3. **Per-command durability.** Each write appends its pending file before
   its `✓` ack (per §8.6); a crash mid-batch leaves a valid prefix —
   consistent with §4.1's per-command isolation, and acks never claim
   durability that isn't there. (End-of-batch flush rejected: acks would
   print before durability and a crash would silently drop acknowledged
   writes.)
4. **Lazy materialization** per addressed node/handle (§8.6's read flow,
   Q2); `worklist`/`dump` reuse the same machinery at whole-store scope.
5. **One mint-pass component, three trigger moments** (§9.4): m1 at any
   read-bearing invocation; m2/m3 at sync and first-read-after-fetch —
   *derived*, not detected: attempt iff classification hit non-landed
   origins and no `nomatch-<tip,target-tip>` memo exists. Memo check in app
   (§5 decision 4); ladder in `core/lineage`.
6. **Stat counting policy in core, recording in app.** `view` reports what
   was shown collapsed/expanded/search-matched (§7.1 event definitions =
   policy); Session translates to deltas, one flush at Close.
7. **Sync's degradation ladder is app logic over core decisions**: failed
   push → fetch-only (merge already applied locally, pending intact, error
   in footer); pending clears only on a landed push (§8.4).

## 8. cli — internals

Fixed in the closing iteration. One package, three files — `parse.go`,
`render.go`, `dispatch.go`; a subpackage split buys nothing (both sides share
the canon below, neither has the surface to earn a namespace).

1. **Type ownership follows call direction, symmetrically.** Inputs belong
   to the callee: the typed command vocabulary lives in **app** (cli
   constructs `app.Command` values — cli-owned types would force app to
   import cli, inverting §3). Outputs belong to the producer: render
   consumes `core/view`'s read-model directly (relaxed rule, §3). cli owns
   almost no types.
2. **The encoding canon lives once, in `core/record`.** §4.1/§8.3: one
   percent-encoding canon (`%` `:` space C0) shared by record fields, CLI
   path fields, and all output. Byte-identity of records is a CRDT
   invariant, so the canon is codec territory — parse decodes with it,
   render encodes with it, store serializes with it.
3. **Body-byte validation is phase 2, not parse.** §11.4 pins `▸`/`◾` and
   C0-except-tab rejections as per-command `✗` errors (§4.3: write-time
   rejections). Parse accepts any body bytes; the write command errors via
   `core/record.ValidateBody`. Only grammar, strict %-decoding, and `$N`
   violations reject the batch.
4. Golden-output tests (§11.4 formats) pin `render` given view values;
   grammar tests pin `parse`; both also ride the E2E harness through the
   real binary.

## 9. Repo layout & naming

```
cmd/dm/              main — wires cli.Dispatch
cmd/dm-fixture/      fixture builder (harness-owned, never shipped — §11.2)
internal/cli/        parse.go · render.go · dispatch.go
internal/app/        session.go · batch.go · sync.go · gc.go · worklist.go · initcmd.go · dump.go
internal/core/       record/ · evidence/ · lineage/ · fold/ · resolve/ · view/ · union/
internal/evcache/    evidence.Cached composite (§2)
internal/gitio/
internal/store/
internal/local/
e2e/                 E2E harness, scenarios, testdata/ goldens
```

Unit tests live beside their packages; the §11 harness lives in `e2e/`.
Module path: `meltcloud.io/dm` (placeholder-grade decision, zero
architectural weight).

## 10. Fixture & E2E threading

1. **`cmd/dm-fixture` is exec-only — a true black box.** §11.2: the builder
   "drives the public `dm` stdin interface plus plain git", and the
   determinism overrides are env vars on the production binary (indicative:
   `DM_CLOCK`, `DM_ID_SEED`, `DM_REPLICA_ID`). So dm-fixture shells out to
   the built `dm` and to git, and imports **nothing** from `internal/` — a
   review rule (Go can't enforce it within one module), structurally
   encouraged since everything it needs is reachable by exec. The fixture
   builder is thereby the first genuine consumer of the public interface,
   which is what the §11.2 guard tests lean on.
2. **E2E harness flow:** build `dm` + `dm-fixture` once → guard tests
   validate the fixture build → per scenario: copy the fixture repo dir
   (§11.2 copy-per-test), drive `dm` over stdin, assert stdout against
   goldens and store state via `dm dump` (§11.3).
