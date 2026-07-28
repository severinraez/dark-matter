# Q2 — read-path performance on large repos (verdict on record)

> Gate definition: design.md §14 Q2 · rig: `e2e/q2rig.go` (M9). The §8.2
> caches and §9.4 verdict records reduce steady-state reads to an index
> load plus tree lookups; what needed profiling is the residue — the cold
> path, diff multiplicity over distinct origins, checkout-side lookups,
> and the live (never-memoized) pending-elsewhere / derived-abandoned
> re-checks.

## Rig

`RunQ2` generates a synthetic repo along the profiling matrix — history
length × cold/warm cache × checkout age × unresolved-note count ×
dead-origin count — and times real `dm` invocations:

- **cold** — first read after a cache wipe (index rebuild + the batched
  origin→checkout diffs);
- **warm** — the steady state agents live in;
- **old** — the same reads at a detached checkout `Age` commits back;
- **worklist** — `dm worklist --all`, the hygiene pass over every debt
  dimension at once.

Unresolved notes are seeded at *distinct commits* spread over the history
and their files then rewritten below the similarity floor — the worst
case for diff multiplicity (§14: one batched diff per distinct origin,
and origins accumulate over a store's life). Dead origins are one deleted
branch each (derived-abandoned re-checks, bounded by the ff-aware
unreachability cache).

`TestQ2ReadPath` is the always-on gate (small cell, generous tripwire);
report cells run via `DM_Q2_SCALE` and the per-axis `DM_Q2_*` overrides:

```
DM_Q2_SCALE=20 go test -run TestQ2ReadPath -v -timeout 60m ./e2e/
```

## Measurements (2026-07-28, warm page cache, single dev machine)

Scale ladder (all axes co-varied):

| commits | unresolved | dead | age | cold | warm | old-cold | old-warm | worklist --all |
|---|---|---|---|---|---|---|---|---|
| 100 | 10 | 5 | 50 | 106ms | 64ms | 68ms | 53ms | 136ms |
| 500 | 50 | 25 | 250 | 805ms | 290ms | 300ms | 258ms | 1.38s |
| 2000 | 200 | 100 | 1000 | 7.8s | **1.1s** | 739ms | 519ms | 29.6s |

Axis isolation (500 commits, age 250):

| unresolved | dead | cold | warm | worklist --all | shows |
|---|---|---|---|---|---|
| 200 | 0 | 4.4s | 1.6s | 4.3s | cold and worklist scale with **distinct-origin diff multiplicity** |
| 0 | 100 | 423ms | 119ms | 8.8s | reads stay cheap under dead origins (unreachability cache works); the cost pools in the **worklist** |

## Reading of the numbers

- **Steady state holds.** Warm reads stay ~1s at the extreme cell (2000
  commits, 200 unresolved, 100 dead) and ~100–300ms at realistic debt
  levels. The §8.2 index (load-whole-file, content-keyed) is nowhere near
  bottlenecking — **the pre-paid upgrade paths (mmap'd arrays,
  incremental chaining) are not needed for v1.**
- **The cold path is diff multiplicity, as predicted.** Cold time tracks
  the unresolved-note count (one batched diff per distinct origin), not
  history length: 200 unresolved origins cost ~4–8s cold whether the
  history is 500 or 2000 commits. It amortizes: the memos live in the
  §8.2 cache, so it is paid once per checkout-switch/sync, not per read.
- **Old checkouts are fine.** Detached reads 1000 commits back are
  *cheaper* than tip reads (fewer noted files present → fewer diff
  targets); no age-related cliff.
- **Worklist debt is a real latency debt.** `worklist --all` walks every
  unresolved origin and dead line: ~90ms per dead line, 29.6s at the
  deliberately pathological cell. This is §14's "worklist debt carries a
  read-latency consequence" made concrete — the cost pools in the
  hygiene surfaces (worklist, sync health tally), not in reads. The
  designed answer stands: worklist hygiene (resolve or `vd` the debt)
  is what bounds it, and the numbers at healthy debt levels (≤ 25 dead
  lines: ≤ 1.4s) are unremarkable. No new mechanism warranted for v1;
  if teams accumulate hundreds of dead lines, the §15 deferrals
  (auto-compaction, shared read locks) are the pressure valves to
  revisit.

## Verdict

**Q2 passes.** Steady-state reads are interactive at sizes well beyond
the design's working assumptions; the residual costs land exactly where
§14 predicted (cold diff multiplicity, hygiene-pass reachability), both
amortized or hygiene-bounded. The §8.2 index upgrade paths stay
pre-paid-but-unbuilt.
