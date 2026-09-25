---
name: garp-search
description: Use when searching a codebase or docs for terms that must appear near each other (concept co-occurrence, symbol proximity), for prefix or partial identifier matching, or to search PDF/DOCX/RTF/eml content. Ranked multi-term matching with JSON output and line numbers.
---

# garp -- proximity content search

`garp` finds files where search terms co-occur within a character window -- text, code, and binary documents (PDF, DOCX, ODT, RTF, eml/mbox/msg). Single pure-Go binary, identical behavior on Windows and Linux/macOS: run `garp` from PATH, else the repo binary (`bin/garp`; `garp.exe` on Windows). Forward slashes work in paths on every OS. Quote paths containing spaces; path wildcards are rejected.

Invariants:
- **Always pass `--json`** (preferred) **or `--plain`** in agent sessions. Without either, garp launches the interactive TUI, which blocks a PTY and errors out without one.
- Errors go to stderr: check the exit code before parsing stdout, and zero-check the top-level `matches` integer before iterating `results`.
- Tuning flags (`--workers`, `--heavy-concurrency`, `--file-timeout-binary`) have safe defaults; leave them.

## When to use garp vs grep/ripgrep

Default rule: single exact symbol or regex -> grep; anything about WHERE terms or concepts co-occur -> garp.

| Use garp | Use grep/ripgrep |
|---|---|
| 2+ terms that must co-occur within a window (`--distance`) | exact symbol / regex lookup |
| 3+ terms: ranked best-cluster match by default; `--strict` forces AND on all terms | list every literal hit of one token |
| prefix/partial identifier matching (`deploy*`, `--partial`) | full regex syntax needed |
| PDF / DOCX / ODT / RTF / eml / mbox / msg content | quick one-shot in a terminal |
| monster files: ranked excerpt + `start_line` -> one targeted read, no paging | |
| prune the walk to chosen subdirs (`--pathscope` never descends elsewhere: .venv, node_modules) | |

## Flags that matter

- `--startdir <dir>`: search root (default: cwd). `--pathscope 'a/,b/'`: comma-separated directory patterns that prune the walk -- directories outside scope are never descended. `*`/`?` wildcards only; no file extensions (use `--only`/`--not`).
- `--code`: include source files, incl. Dockerfile/Containerfile matched by name. `--only <ext>`: restrict to one extension family, no dot (`--only sql`, `--only pdf`, `--only dockerfile`); overrides `--code`. Omit both for the document set (md, txt, html, csv, ini, yaml, pdf, docx, ...).
- `--distance N`: proximity window in characters (default 5000). Same call/block in code ~100-300; same paragraph/section in docs ~1000-2000.
- `--max-excerpts N`: excerpts per file (default 1, max 50), skip-ahead selected so every chunk is >=90% new content.
- `--partial[=prefix|contains]`: boundary-aware prefix (default) or raw substring. Per-term inline glob instead: append `*` to one term (`deploy*`; quote it in shells). `--smart-forms`: match word forms (plurals, -ing, -ed, -tion).
- `--strict`: require every term in the window (3+ term searches are ranked by default).
- `--not <tokens...>`: exclusions after the flag; `.ext` excludes a file type, bare words exclude whole words.

## Output shape (`--json`)

One-shot find -> pinpoint -> targeted read: each result carries the file, a ranked excerpt, and the line of the earliest matched term.

- Sort order: score (sum of matched term lengths), then matched-term count, then top-quality chunk count, then tighter span, then path. `matched_terms` lists the terms that actually hit.
- Each excerpt: `text` (context-padded, newlines collapsed) plus 1-based `start_line` = line of the earliest matched term in that chunk, and per-chunk `score`/`term_count`. Present for text/code; omitted for extracted formats (PDF, DOCX, email).
- Chunks within a file are a ranked subset, best-first: with `--max-excerpts N > 1` every quality tier can seed a chunk, so a strong cluster no longer hides the file's other clusters. Every chunk is >=90% new content (bounded overlap).
- Act on results by reading the file at `start_line` (with margin) instead of re-searching or paging through reads.

```jsonc
"results": [ { "file": "...", "score": 14, "term_count": 2, "matched_terms": ["a", "b"],
  "excerpts": [ { "text": "...", "start_line": 42, "score": 14, "term_count": 2 } ] } ]
```

Example: locate deployment base-image notes -> `garp AMI parent base --startdir <repo> --only md --distance 200 --json`. Note: extracted PDF text can normalize tokens (e.g. `RHC03-2516` -> `RHC 03-2516`); if an expected hit misses, retry with looser or split terms.
