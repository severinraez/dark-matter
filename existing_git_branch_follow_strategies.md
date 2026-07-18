# Existing strategies for keeping metadata in sync with git

Survey of how existing systems store metadata beside code and/or track what a git
repository is doing. Collected during the git-workflow discussion of 2026-07-14;
referenced from [spike_content_adressable.md](spike_content_adressable.md) §2.
Findings referenced as A3/A4 are in [review.md](review.md).

## Metadata-beside-code systems

| System | How it syncs | Transferable lesson |
| --- | --- | --- |
| **git-annex** | One global `git-annex` branch of timestamped log lines; union merge; keyed by *content hash*, not path | The 15-year-proven version of dm's CRDT branch — and it never mirrors code branches. Metadata keyed by blob identity survives renames and branches for free |
| **git-bug** | Each bug = append-only DAG of operation commits under `refs/bugs/*`; ordering from DAG causality + Lamport clocks | Causal ordering instead of wall-clock ULIDs kills the clock-skew caveat; fetch is union-of-DAGs, inherently non-clobbering (A3) |
| **git-appraise** | Review data as JSON in `refs/notes/devtools/*`, merged `cat_sort_uniq` (union) | Union-merged metadata refs push/fetch fine through GitHub — design.md §8.1's "only `refs/heads` is reliable" applies to *PR-able* branches, not metadata refs |
| **Gerrit NoteDb** | All review metadata as append-only meta commits, single-writer server | One folding authority (server/CI) makes the concurrent-merge problem disappear entirely |
| **Fossil SCM** | Wiki/tickets/code are one global set of immutable artifacts; sync = set union; state is a computed view | The purest form of dm's idea: *which notes apply where* is a **query**, not a ref topology |

## Git-observing / wrapping systems

| System | How it stays in sync | Transferable lesson |
| --- | --- | --- |
| **Jujutsu (jj)** | No hooks — every invocation imports git refs, **diffs them against its own last-known snapshot**, reconciles; plus an operation log of its own mutations | **State reconciliation beats event observation**: nothing to install, nothing missed, `--no-verify` can't bypass it |
| **jj working-copy-as-commit** | The working copy is always a commit; "dirty" doesn't exist | dm's content-dirt deadlock (A4) exists only because uncommitted note state exists |
| **GitButler** | Daemon + own `refs/gitbutler/*`, reconciles with real refs | Same pattern, daemon flavor |
| **etckeeper / gitwatch** | Hook/watcher auto-commit | Hooks work but carry the known failure modes: not installed, bypassed, races |
| **`reference-transaction` hook** | Fires on every ref update | If hooks at all: this one subsumes post-checkout/commit/merge/rewrite — still opt-in per clone |

## Named and rejected

- **Submodule/gitlink binding** — atomic code↔notes pinning, survives squashes — but
  every PR touches the same gitlink and conflicts with every other PR.
- **Forge-native storage** (PR comments/Discussions) — abandons git-native and offline.
