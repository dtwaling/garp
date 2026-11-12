---
name: garp-search
description: Use when searching a codebase or docs for terms that must appear near each other (concept co-occurrence, symbol proximity), for prefix or partial identifier matching, or to search PDF/DOCX/RTF/eml content. Ranked multi-term matching with JSON output and line numbers.
---

# garp -- proximity content search

`garp` finds files where search terms co-occur within a character window -- text, code, and binary documents (PDF, DOCX, ODT, RTF, eml/mbox/msg). Single pure-Go binary, identical behavior on Windows and Linux/macOS: run `garp` from PATH, else the repo binary (`bin/garp`; `garp.exe` on Windows). Forward slashes work in paths on every OS. Quote paths containing spaces; `--startdir` paths reject wildcards.

Invariants:
- **Always pass `--json`** (preferred), **`-l`** (when only matching file paths are needed), **or `--plain`** in agent sessions. Without one of these, garp launches the interactive TUI, which blocks a PTY and errors out without one.
- Errors go to stderr: check the exit code before parsing stdout, and zero-check the top-level `matches` integer before iterating `results`.
- Tuning flags (`--workers`, `--heavy-concurrency`, `--file-timeout-binary`) have safe defaults; leave them. Exception: if top-level `timed_out` is above 0, rerun with a higher `--file-timeout-binary` (max 60000) to search the skipped binary files.

## When to use garp vs grep/ripgrep

Default rule: single exact symbol or regex -> grep; anything about WHERE terms or concepts co-occur -> garp.

| Use garp | Use grep/ripgrep |
|---|---|
| 2+ terms that must co-occur within a window (`--distance`) | exact symbol / regex lookup |
| 3+ terms: ranked best-cluster match by default; `--strict` forces AND on all terms | list every literal hit of one token |
| prefix/partial identifier matching (`deploy*`, `--partial`) | full regex syntax needed |
| PDF / DOCX / ODT / RTF / eml / mbox / msg content | quick one-shot in a terminal |
| monster files: ranked excerpt + `start_line` -> one targeted read, no paging | |
| prune the walk to chosen subdirs (`-g` / `--iglob`) | |

## Flags that matter

- `--startdir <dir>`: search roots (default: cwd). Use one comma-separated value for a multi-repository search; specify the flag once. An existing directory name that contains a comma stays intact.
- `--pathscope <patterns>`: comma-separated path patterns in one flag occurrence; a `!pattern` entry excludes matching paths. Anchored prefixes such as `src/` prune unrelated directories during the walk. Uses the path pattern rules below and intersects with positive `-g` / `--iglob` globs.
- `-g <pattern>` is case-sensitive and `--iglob <pattern>` is case-insensitive; both are repeatable, positive globs form one union, and commas are literal. A `!pattern` glob (e.g. `!archive/**`) excludes matching paths. Bare `name` matches basenames at any depth. `name/`, `./name`, and patterns that contain `/` are root-anchored. `*` and `?` stay within one segment; `**` crosses segments.
- `--code`: include source files, including Dockerfile/Containerfile names. Only recognized code types skip markup cleaning; HTML is not a code type (even with `--code`), so positive and `--not` terms on `.html` files match cleaned text. Single-line tags (with attributes), comments, entities, and `script`/`style` bodies are removed; an element tag that spans lines stays searchable. `-t <ext>` restricts file types. Repeat the flag or use comma-separated values. A type restriction overrides `--code`.
- `.gitignore`, `.ignore`, and `.git/info/exclude` are respected by default. Use `--no-ignore` to search ignored files; other search filters still apply.
- `--max-filesize <size>`: skip files above the size cap; text/code files default to `5M` when omitted. Requires a positive size, up to `100M`; `0` is invalid.
- `--distance N`: proximity window in characters (default 5000). Same call/block in code ~100-300; same paragraph/section in docs ~1000-2000.
- `-m N`: proximity excerpts per file (default 1, max 50), not raw matching lines.
- `--max-results N`: maximum ranked matching files returned (default `50` in `--json` and `--plain`; `-l`/`--pathonly` defaults to `0`; `0` = unlimited).
- `--partial[=prefix|contains]`: boundary-aware prefix (default) or raw substring. Per-term inline wildcards `*` and `?` work at any position: leading, mid-token, or trailing (`deploy*`, `ms-*-api`, `*OptionApi*`, `mark?systems`; quote terms in shells). `--smart-forms`: match word forms (plurals, -ing, -ed, -tion).
- `-i` and `-s`: select case-insensitive or case-sensitive matching; the last case flag wins.
- `--strict`: require every term in the window (3+ term searches are ranked by default).
- `-l`: skip excerpt extraction and emit one distinct matching file path per line in ranked order. Zero matches produce empty stdout.
- `--not <tokens...>`: exclusions after the flag; `.ext` excludes a file type, bare words exclude whole words.

Value flags accept both `--flag value` and `--flag=value`. Use `--` to stop
flag parsing. These short flags follow rg conventions, but garp is not a
drop-in rg replacement. Context flags such as `-C`, `-A`, and `-B` are not
supported; use `--distance` and `-m`.

Both `--partial` modes (`prefix` and `contains`) require terms of at least
3 characters for partial matching. The short-term guardrail (`len(term) <= 2`)
keeps shorter terms whole-word to prevent noisy substring explosion; the
implementation measures length in bytes. Explicit inline wildcards such as
`al*` still work for short terms (quote them in shells).

On all platforms, an absolute `--pathscope`, `-g`, or `--iglob` pattern must overlap an
effective start root. Examples are `/repo/src/**` on POSIX and `C:/repo/src/**`
on Windows. A pattern can select inside the root, or an ancestor pattern can
cover the root. If no root intersects it, validation fails before search. Use
`./src` for explicit root-relative matching. On Windows, a non-absolute leading
slash is root-relative.

## Output shape (`--json`)

One-shot find -> pinpoint -> targeted read: each result carries the file, a ranked excerpt, and the line of the earliest matched term.

- Top-level `matches` counts all matching files; `returned` counts files in `results`. `truncated: true` means only the top `N` ranked files were returned under `--max-results N`; use `--max-results 0` for all matches. `timed_out` counts binary files skipped because extraction exceeded `--file-timeout-binary`; they are absent from `matches`, and a one-line `Warning:` goes to stderr.
- Sort order: score, matched-term count, path-term count, boundary quality, tighter span, top-quality chunk count, ranking-root-relative path, supplied root order, then canonical path. `matched_terms` lists the terms that actually hit.
- Each excerpt: `text` (context-padded, newlines collapsed) plus 1-based `start_line` = line of the earliest matched term in that chunk, and per-chunk `score`/`term_count`. Present for text/code; omitted for extracted formats (PDF, DOCX, email).
- When matched terms are further apart than the excerpt display budget (`span > budget`), multipart excerpts join per-term context windows with `" ... "` (space, three ASCII dots, space). Windows are in source order and overlapping windows are merged; the joiner marks omitted text, not a contiguous source passage.
- Chunks within a file are a ranked subset, best-first: with `-m N` where `N > 1`, every quality tier can seed a chunk, so a strong cluster no longer hides the file's other clusters. Every chunk is >=90% new content (bounded overlap).
- Act on results by reading the file at `start_line` (with margin) instead of re-searching or paging through reads.
- `query.start_dir` is the first reported root. `query.start_dirs` appears only for two or more reported roots. A nested supplied root can become traversal-only to prevent data loss; results remain under its reported ancestor. Both fields are omitted when `--startdir` is absent.
- `query.only_type` remains the singular compatibility field. `query.only_types` appears only for two or more distinct types. `query.case_sensitive` appears only when enabled.

```jsonc
"results": [ { "file": "...", "score": 14, "term_count": 2, "matched_terms": ["a", "b"],
  "excerpts": [ { "text": "...", "start_line": 42, "score": 14, "term_count": 2 } ] } ]
```

Example: locate deployment base-image notes -> `garp AMI parent base --startdir <repo> -t md --distance 200 --json`. Note: extracted PDF text can normalize tokens (e.g. `RHC03-2516` -> `RHC 03-2516`); if an expected hit misses, retry with looser or split terms.
