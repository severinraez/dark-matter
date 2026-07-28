<!-- The canonical dm block (design.md §10.3). Copy everything below this
     line into your repo's AGENTS.md (or CLAUDE.md) so agents read dm
     output correctly and follow the lifecycle. -->

#### Dark Matter (`dm`) — your memory about this codebase

`dm` stores notes *about* the code (gotchas, architecture rationale, dev/ops
know-how) that automatically surface on the branches and commits where they apply.
Read before you touch a file; write when you learn something you'd want back later.

**Read first.** Before editing `foo/bar.rb`: `r:foo/bar.rb` returns a *surface* —
own notes (one-line previews), parent/arch notes, links, and an inventory footer.
It's a map, not a dump. Drill with `r:#handle` (full body of one entry) or
`s:foo/bar.rb:t1|t2` (grep the file's whole note-context; `|`=OR, `+`=AND).

**Write on learning.** `a:path:subj:body` (subj: `c` code/behavior · `a`
architecture · `d` dev/build/test · `o` ops). `u:#handle:body` supersedes ·
`d:#handle` removes · `k:#handle[:path]` re-blesses a stale note (the path picks a
split's home) · `f:#handle:!:reason` flags one wrong.

**Link related notes.** Links are the only way notes relate — `al:#a:#b[:why]`
connects two entries (directed `a→b`, optional comment); `dl:#a:#b` removes one.
Link a note to the ones it depends on, contradicts, or elaborates: an architecture
note (`a`) links to the files it governs so they surface it as `← linked-from`; a
gotcha links to the note explaining the underlying cause. Follow any `→`/`←` you
see with `r:#handle`.

**Output.** Blocks split on `▸` (command echo, then raw content); `◾` ends the
stream. Glyphs: `→` link · `↑` parent note · `←` linked-from · `⚠stale` (file
changed since the note) · `⚠unconfirmed` (followed a move by inference) ·
`⚠disputed` (flagged wrong) · `⚠split` (folder went two-plus places — pick the
home with `k:#handle:path`). `(+N lines)` = hidden size. Handles `#a3f9c1` are
stable — copy them into later commands.

**Batch over stdin**, one command per line, body always last (`:` in a body needs
no escaping; trailing `\` continues to the next line; `$N` references the entry
created by command N of the same batch, so create-then-link is one round trip):
```
dm <<'EOF'
r:api/handler.rb
a:api/handler.rb:c:Validates tenant header before dispatch
EOF
```

**Session lifecycle** — there is nothing to mirror: notes follow your branch
through rebases, squashes, and merges automatically, and your own notes are usable
the moment you write them. Run **`dm sync`** at session end (and whenever you want
teammates' notes) to share both notes and read-stats; its footer reports how much
wants judgment. Skim **`dm worklist`** at wrap-up — it leads with items near your
session's work — and resolve what you have context for: `k` (still valid), `u`
(rewrite), `d` (obsolete), `mv`/`rm` (re-home / retire a path), `vd` (say where
a rewritten branch landed).
