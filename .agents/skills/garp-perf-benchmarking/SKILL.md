---
name: garp-perf-benchmarking
description: "Use when measuring garp prefilter perf caps. Harness."
---

# garp prefilter performance benchmarking

## When to Use
A garp card has a before/after perf cap (Stage 1 discovery, prefilters, cleaner readers),
or two agents report conflicting percentages for the same change.

## Harness (scratch only, never in the repo)
1. `git archive <sha> | tar -x -C $TMPDIR/<dir>/<variant>` per variant; never `git worktree` (touches .git).
2. Probe = `cmd/<probe>/main.go` inside each tree (module `garp`), calling
   `search.FindFilesWithFirstWordProgressPartial(terms, {txt,md,html,docx}, workers, nil, root, nil, mode)`.
   Per rep: re-read every corpus file (warm), `runtime.GC()`, time one scan.
3. Interleave variants, alternate order per round, report medians of >= 9 samples + min/max.
4. Corpora: codie's 41 MB prose (600 x 68.3 KB) and 113 MB split-dense recipe (comment on gh-45-A),
   PLUS a small-file corpus (20k x 2 KB) and a 4-worker row. Large-file-only corpora hide per-file costs.
5. Fuzz soundness with an oracle `matcher.matches(cleanSearchContent(raw))` vs Stage 1 admission on a temp
   file; include a mutation negative control.

## Pitfalls
- discovery calls `FADV_DONTNEED` after every file (`search/file_advice_unix.go`): repeated scans without
  re-warming read from disk (~2.5x slower), shrinking CPU-regression percentages. State which regime.
- Per-file heap allocations (e.g. a reader with an inline `[64*1024]byte`) explode on small-file trees
  and with >1 worker (GC); 68 KB corpora barely show them.
- `asciiPlainTermIndexCI` in Contains/Prefix mode has no boundary short-circuit: a Contains scan is slower
  than an Off scan unless a first-byte filter is added.
- `cp -r src dst` into an existing `dst` nests the copy; copy files explicitly.
- In kanban workers, `terminal(background=true)` runs zsh without the snap `go` on PATH; run go
  commands in the foreground (<= 600 s) and only background pure-binary loops.
- `rm -rf` / `find -delete` are blocked unattended: delete files by glob, `rmdir` dirs, leave trees for prune.
