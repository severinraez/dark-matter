# Q1 — resolution under ordinary edit churn (first verdict on record)

> Gate definition: design.md §14 Q1 · rig: `e2e/replay.go` (M5). The
> premise to verify: everyday editing rarely drops notes to §9.1 layer 5
> (unresolved); if it routinely does, the content-anchor model fails
> regardless of the merge story.

## Rig

`Replay` clones a repository, seeds a note on every sampled file and
folder of the commit at a chosen fraction of first-parent history (the
anchors `a` would have stamped there: blob SHA / path key + origin), then
resolves every note against every later checkout through the real
`core/resolve` + `gitio` evidence stack, tallying the deciding layer and
state per sample.

Run it against any repo:

```
DM_Q1_REPO=/path/to/repo [DM_Q1_SEED=0.25] [DM_Q1_NOTES=200] \
  go test -run TestQ1ReplayRepo -v ./e2e/
```

`TestQ1ReplaySynthetic` is the always-on guard: a deterministic history of
ordinary churn (edit, rename, folder move, delete, rewrite) must keep the
layer-1–3 fraction ≥ 0.85.

## First measurements — this repository (36 first-parent commits, 2026-07-27)

| seed point | checkouts replayed | file notes | samples | file layers | layer-1–3 fraction |
|---|---|---|---|---|---|
| 25% | 27 | 8 | 270 | L1=149 L3=21 **L5=46** | **0.787** |
| 50% | 18 | 9 | 198 | L1=112 L3=50 | **1.000** |
| 75% | 9 | 12 | 135 | L1=108 | **1.000** |

Reading the one weak row: every layer-5 sample at the 25% seed traces to
early-milestone placeholder files (`doc.go` stubs, scaffolding) that later
milestones **deleted or wholly rewrote**. Orphaning those is the *correct*
answer — the described content genuinely left the tree — not a resolution
miss; §2.1 row 12 wants exactly this (worklist, never silent loss). No
sample orphaned on a surviving file: in-place edits resolved at layer 3
(stale/unconfirmed inside the §9.2 bands), renames and the folder moves at
layers 1–3.

## Verdict — provisional PASS

- Ordinary churn (edits, renames, moves) kept every note resolvable via
  layers 1–3 in these runs; layer-5 drops occurred only where content was
  genuinely deleted.
- The §9.2 (80/20/50) and §9.3 (majority/margin 50 · coverage 60 ·
  candidate 25 · foreign 50) starting bands produced no observed false
  follow and no missed follow at this sample size — **left unchanged**.

**Caveats, on the record:** one small, append-heavy repository; 8–12
seeded file notes per run; no squash/force-push churn (that is Q3's rig,
M6). The gate stays open as *provisional* until the rig has been run
against at least one large external history (the `DM_Q1_REPO` hook exists
for exactly that); hygiene-metric tracking (worklist backlog growth, stale
fraction — §9.6) starts with the pilot.
