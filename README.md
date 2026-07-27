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
  **Current state: M4 (store + sync)** — every write verb, reads against
  store ∪ pending, and `dm sync` (shared `refs/dm/store` branch, CRDT
  union merge, push retry, fetch-only degradation); no mint pass or
  resolution layers beyond exact-blob yet.

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
retire whole paths; `$N` references the entry created by the batch's Nth
command. Use a quoted heredoc (`<<'EOF'`) so the shell doesn't eat `\`
continuations or `$N`. Notes live in `.git/.dm/pending` until `dm sync`
folds them into the shared `refs/dm/store` branch (design.md §8);
`worklist` and `gc` arrive with later milestones (plan.md M6+).

## Develop

```sh
go test ./...                  # unit tests + E2E (builds dm, drives real repos)
go test ./e2e/ -run TestWorkedExample -v
go vet ./...
gofmt -l cmd internal e2e      # must print nothing
```

The E2E harness (`e2e/`) builds the binary once per test process and drives it
over stdin against inline-built git repos, pinning all nondeterminism via env
overrides on the production binary (design.md §11.2):

| env | effect |
| --- | --- |
| `DM_CLOCK` | pins the clock (unix milliseconds) |
| `DM_ID_SEED` | seeds rec-/entry-id entropy (int64) |
| `DM_REPLICA_ID` | pins the replica id minted at `dm init` |

When pinning both clock and seed across *multiple* invocations, vary them per
invocation (the harness does) — identical seeded entropy replays identical
entry-ids.

Layout follows architecture.md §9: pure core under `internal/core/`, adapters
(`gitio`, `store`, `local`, `evcache`) beside it, use-case choreography in
`internal/app`, grammar/rendering in `internal/cli`.
