# Dark Matter — Implementation Plan

> **Status:** agreed ordering for v1 implementation. Companion to
> [design.md](design.md) (bare §N references) and
> [architecture.md](architecture.md) (A§N references).
>
> **Ordering principle:** strict dependency/infrastructure layering — each
> milestone builds only on what already exists, and the share loop (store +
> sync) completes before read-time intelligence (resolution, lineage).
> Risk-first ordering (pulling the Q1 gate ahead of store/sync) was
> considered and deliberately not taken; the cost, on the record: the Q1
> verdict — the one result that could invalidate the content-anchor model
> (§14) — arrives after roughly half the system is built instead of a
> third. The gates themselves are unchanged, only later.
>
> Sizes: S / M / L (relative). Every milestone's acceptance bar is its
> §11.4 scenario group; the E2E harness and fixture grow *with* the
> milestones, not at the end.

## M0 — Scaffold (S)

Module + package skeletons per A§9, port interfaces stubbed, CI, and — from
day one — the determinism plumbing: injectable clock, seeded id source,
pinned replica id as env overrides (§11.2; indicative: `DM_CLOCK`,
`DM_ID_SEED`, `DM_REPLICA_ID`). Every later golden test depends on these
existing first.

**Exit:** `go build ./...`; one trivial E2E test drives the empty binary.

## M1 — `core/record` (M)

Types, ULID minting (monotonic rec-ids, fresh-random entry-ids — §8.3),
handle derivation + collision extension (§3.5), the canonical codec +
encoding canon (§8.3, A§8), `ValidateBody` (§4.3). The package everything
imports; the CRDT byte-identity invariant lives here — full test saturation
before anything consumes it.

**Exit:** codec round-trip goldens incl. the §11.4 path-escaping and
body-bytes rows; handle-collision cases pinned.

## M2 — Walking skeleton (M)

Thinnest vertical slice of the §8.6 worked example: minimal `gitio`
(hash-object, rev-parse, ancestry), minimal `local` (pending append/scan,
lock, replica), minimal `cli` (parse + render for `a`/`r`), app Session
(A§7), `dm init`, `dm dump`. Rule (a) = exact-blob layer 1 only; rule (b) =
ancestor-of-HEAD only. No store branch yet — pending alone carries the
slice.

**Exit:** §8.6's write→read example runs end-to-end against a real repo;
E2E harness bootstrapped (inline-built repos, no fixture yet); §2.1 rows 2
and 7 pass.

## M3 — Fold + full CRUD (M)

`core/fold` complete (LWW, tombstone-absorbing, rescue fold, per-record
rule-(b) consumption as data-in — heavily unit-tested with pinned clocks,
zero fakes; A§6), all write verbs (`u`/`d`/`k`/`f`/`al`/`dl`, `mv`/`rm`
macros — §6), semantic handle resolution, `$N`, per-command errors,
read-your-writes, per-command durability (A§7).

**Exit:** §11.4 CRUD + two-phase + macro scenario groups;
concurrent-supersede and tombstone-terminal folds golden.

## M4 — Store + sync (M)

`internal/store` layout (§8.1: record shards, stats files, blobs, epoch
marker), blobs staging, `core/union` merge + epoch rule, `dm sync`
(fetch → fold → push → retry, push-clears-pending, fetch-only degradation —
§8.4). No mint pass yet — that arrives with the ladder (M6), which needs
this milestone's two-replica infrastructure to be testable at all.

**Exit:** §11.4 sync scenarios: two-replica convergence, non-ff retry,
`push-fail-fetch-only`, stat-counter merge (counters may stub as zero until
M7).

## M5 — Resolution rule (a) (L)

Full `Tree`/`Match` evidence in `gitio`, `evcache` with the match memos
(A§2, A§5), `core/resolve` layers 1–5 (§9.1), move-with-edit bands (§9.2),
folder path-key + follow vote (§9.3), staleness/pinned/orphaned states,
bands as constants. The **Q1 replay rig** lands here (`e2e/`): replay a real
repo's history against seeded notes, measure the layer-1–3 fraction,
calibrate the §9.2/§9.3 bands (§14 Q1).

Placed after store deliberately: resolution develops against the finished
substrate — the store index (`index-<store-tip>`, §8.2) keys `evcache` on
real tips, and anchored-blob lookups exercise the store-tree `blobs/` path
(§8.1, uncommitted-content case) instead of stubbing it.

**Exit:** the §2 visibility rows that don't need lineage; resolution +
split/absorbed scenario groups; Q1 verdict on record.

## M6 — Rule (b): lineage + ladder (L)

`core/lineage` classification (VD chains, unlanded voiding,
qualified/degraded guard — §9.4), unreachability cache in `evcache` (§8.2),
pending-elsewhere/abandoned/unknown states, then the matcher ladder — m1 at
read, m2/m3 at the sync mint pass with nomatch memos (A§7 decision 5),
`vd`, worklist line-grouping. Fixture gains its per-matcher branches
(§11.2). The **Q3 rig**: precision/recall replay on squash- and
force-push-heavy histories; m3 floor calibration (§14 Q3).

**Exit:** the full §11.4 landing-inference block (largest scenario group —
every VD asserted with matcher id via `dm dump`); Q3 false-binding rate ≈ 0
on record.

## M7 — Retrieval polish + stats (M)

`core/view` ranking (§5.6), `s` search (§5.5), depth modifier (§5.4),
surface budgets/cost hints, stats end-to-end (G-counters, `t`, FB tallies →
`⚠disputed` — §7), crowding nudge, `dm worklist` with session scoping, sync
health line (§9.6). Main slack in the plan: touches disjoint packages, can
interleave with M5–M6 if running parallel tracks.

**Exit:** discovery + hygiene scenario groups; golden surface formats
pinned.

## M8 — Compaction (S/M)

`dm gc`: sweep rules in `core/union` (rescue pins, VD retention —
needs M6's VDs — TB-forever; §8.7), orphan commit + `--force-with-lease`
CAS loop, dangling-id read semantics, epoch bump.

**Exit:** §11.4 compaction block incl. gc+sync race and epoch
no-resurrection.

## M9 — Hardening + Q2 + packaging (M)

`cmd/dm-fixture` manifest builder finalized (A§10) and the full fixture
assembled (much of it accreted in M4–M8); complete §11.4 matrix green; the
**Q2 profiling matrix** (repo size × cold/warm cache × checkout age ×
unresolved-note count × dead-origin count — §14 Q2), index upgrade paths
only if profiling demands them (§8.2 pre-paid); agents.md block (§10.3),
docs, release.

**Exit:** all scenarios green; Q2 numbers on record; shippable binary.

---

**Gate summary:** Q1 fires at M5, Q3 at M6, Q2 at M9 (it needs a full
system to profile). All three run on the §11.4 harness.
