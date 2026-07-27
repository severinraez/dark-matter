# Q3 — landing-detection precision and recall (first verdict on record)

> Gate definition: design.md §14 Q3 · rig: `e2e/q3rig.go` (M6). The §9.4
> matcher ladder is precision-first by construction; what needed measuring
> is whether the false-binding rate is ~zero and whether recall is high
> enough that the worklist residue stays small. The m3 min-diff floor is
> calibrated on the same runs.

## Rig

`RunQ3` generates a squash- and force-push-heavy history with **known
ground truth** — every seeded note's origin, and whether/where its line
genuinely landed, is created by the rig itself — then drives the real `dm`
binary through its §9.4 mint moments (reads and syncs, two replicas) and
audits every minted `VD` against the truth. Seven categories per round,
commit counts and contents rng-varied under a fixed seed:

| category | ground truth | expectation |
|---|---|---|
| squash-clean | forge squash `S`, remote branch deleted, local ref kept | bind whole segment → `S` via **m3** |
| squash-conflicted | squash result content-adjusted before commit | refuse (FN by design) |
| local-rebase | branch rebased onto advanced main | bind → new tip via **m1** |
| remote-force-push | second replica rebases + force-pushes; author's stale ref deleted | bind → new tip via **m2** |
| abandoned | branch deleted unmerged | refuse — **any binding is a false positive** |
| sub-floor | one-line branch squashed | refuse (floor FN, worklist hint) |
| duplicate-work | byte-identical diff independently landed on main | binding allowed (accepted FP, §9.4) |

Run it at any scale:

```
DM_Q3_ROUNDS=8 go test -run TestQ3PrecisionRecall -v ./e2e/
```

`TestQ3PrecisionRecall` is the always-on gate (3 rounds in CI): false
bindings must be zero, the clean categories fully recovered, and every
refusal category exercised.

## Measurements (seed 42, 2026-07-27)

| rounds | branches | notes | m1 recall | m2 recall | m3 recall | correct refusals | accepted FP | **false bindings** |
|---|---|---|---|---|---|---|---|---|
| 3 | 21 | 34 | 6/6 | 6/6 | 6/6 | 13 | 3 | **0** |
| 8 | 56 | 92 | 16/16 | 16/16 | 16/16 | 36 | 8 | **0** |

Every refusal fell exactly where the guards intend: conflict-adjusted
squashes (patch identity broken — worklisted with the `m3: no squash
candidate` hint), sub-floor one-liners (`m3: below floor (1 < 3 changed
lines)`), and abandoned lines (no evidence, grouped by line for one-`vd`
repair). The duplicate-work bindings are the documented accepted FP — the
identical content did land.

## m3 min-diff floor — calibrated at 3 changed lines (kept)

The floor exists because trivial diffs collide on patch identity (branch
reuse, boilerplate). At 3 changed lines:

- no observed false binding above the floor at either scale — including
  the deliberate collision-shaped categories (branch reuse via forced
  update, apply→revert→re-apply twins, both also pinned as §11.4 guards);
- the cost is the documented FN: genuine one-and-two-line branches refuse
  and fall to the worklist with the floor hint, repairable by one `vd`.

No data pushes the floor off its starting guess in either direction, so
**3 stands**, revisitable when a pilot shows real sub-floor branch traffic.

## Verdict — provisional PASS

- **False-binding rate: 0** across both runs and in every §11.4 FP-guard
  scenario (reset, branch reuse, tip-only pairing, twin candidates,
  cherry-pick, shallow/degraded clones).
- **Recall 1.000 per matcher** on the clean rewrite shapes; the misses the
  design accepts (conflicted squash, sub-floor) are exactly the worklist
  residue, grouped by line so repair is O(lines).

**Caveats, on the record:** synthetic histories with rig-controlled
conflict shapes; patch-id collisions in the wild (generated code,
vendored churn) are represented only by the deliberate categories. The
gate stays *provisional* until the pilot replays a large real
squash-merge-heavy history; the `DM_Q3_ROUNDS` knob and the rig's
category structure are the hooks for extending that run.
