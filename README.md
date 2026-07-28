# Dark Matter (`dm`)

A git-native memory layer for LLM agents: notes *about* a codebase that live
beside the code and surface exactly where and when they apply — right branch,
right commit, any merge strategy — queried through a token-efficient batch CLI.

## Documentation

- [design.md](design.md) — the normative design: behavior, data model, CLI
  grammar, storage/sync, read-time resolution (bare §N references everywhere
  point here).
- [architecture.md](architecture.md) — module boundaries, the Evidence port,
  core decomposition, repo layout (A§N references).
- [plan.md](plan.md) — milestone ordering and dependency decisions.
  **Current state: v1 complete (M9)** — the full §10.1 surface (batch
  verbs incl. `vd`, `init`, `sync`, `dump`, `worklist`, `gc`), rule-(a)
  resolution layers 1–5 with folder follow votes, the §9.4 matcher ladder
  (m1 reflog / m2 replay / m3 squash) with verdict records, retrieval
  ranking + search + stats, compaction, and the complete §11.4 scenario
  matrix green. All three validation gates on record:
  [q1-report.md](q1-report.md), [q2-report.md](q2-report.md),
  [q3-report.md](q3-report.md).
- [agents-block.md](agents-block.md) — the canonical §10.3 block a
  consuming repo copies into its own `AGENTS.md`.

## Build

Requires Go (see `go.mod` for the version) and git ≥ 2.15 at runtime.

```sh
go build ./...                 # compile everything
go build -o bin/dm ./cmd/dm    # build the dm binary
```

## Run

`dm` operates on the git repository containing the current directory:

```sh
cd /path/to/some/git/repo
dm init                        # once per clone: creates .git/.dm/, fetches or creates the store

dm <<'EOF'                     # no subcommand = batch over stdin, one command per line
a:api/handler.rb:c:Validates tenant header before dispatch
r:api/handler.rb
EOF

dm sync                        # share: fetch, union merge, push (retries a lost
                               # race; a failed push degrades to fetch-only)
dm dump                        # raw store ∪ pending state (tests/debugging)
dm help                        # subcommand overview
```

Batch grammar is §4 of design.md: `a:path:subj:body` creates a note
(`subj` ∈ `c`ode/`a`rch/`d`ev/`o`ps); `r:path` reads a node's surface and
`r:#handle` expands one entry; `u`/`d`/`k`/`f` supersede/tombstone/re-anchor/
flag by handle; `al`/`dl` link and unlink entries; `mv`/`rm` relocate or
retire whole paths; `vd:sha:landed:sha` / `vd:sha:unlanded` records where a
rewritten line landed (design.md §9.4); `$N` references the entry created
by the batch's Nth command. Use a quoted heredoc (`<<'EOF'`) so the shell
doesn't eat `\` continuations or `$N`. Notes live in `.git/.dm/pending`
until `dm sync` folds them into the shared `refs/dm/store` branch
(design.md §8); `dm worklist` lists what wants judgment (orphans, disputed
notes, abandoned lines grouped for one-`vd` repair — §9.6); `s:path:terms`
greps a node's note-context (§5.5) and `dm gc` compacts the store (§8.7).

## Develop

```sh
go test ./...                  # unit tests + E2E (builds dm, drives real repos)
go test ./e2e/ -run TestWorkedExample -v
go vet ./...
gofmt -l cmd internal e2e      # must print nothing
```

The E2E harness (`e2e/`) builds the binary once per test process and drives it
over stdin against inline-built git repos plus the seeded fixture repository
(design.md §11.2) — assembled once per process by `cmd/dm-fixture`, a
harness-owned, exec-only tool that drives the public `dm` interface from a
declarative manifest (`cmd/dm-fixture/manifest.json`) and is never shipped.
All nondeterminism is pinned via env overrides on the production binary:

| env | effect |
| --- | --- |
| `DM_CLOCK` | pins the clock (unix milliseconds) |
| `DM_ID_SEED` | seeds rec-/entry-id entropy (int64) |
| `DM_REPLICA_ID` | pins the replica id minted at `dm init` |

When pinning both clock and seed across *multiple* invocations, vary them per
invocation (the harness does) — identical seeded entropy replays identical
entry-ids.

The three §14 validation gates run on the same harness and scale via env:
`TestQ1ReplaySynthetic`/`TestQ1ReplayRepo` (resolution under churn),
`TestQ3PrecisionRecall` (`DM_Q3_ROUNDS` — landing precision/recall), and
`TestQ2ReadPath` (`DM_Q2_SCALE`, plus per-axis `DM_Q2_*` overrides —
read-path profiling). Verdicts: [q1-report.md](q1-report.md) ·
[q2-report.md](q2-report.md) · [q3-report.md](q3-report.md).

Layout follows architecture.md §9: pure core under `internal/core/`, adapters
(`gitio`, `store`, `local`, `evcache`) beside it, use-case choreography in
`internal/app`, grammar/rendering in `internal/cli`.
