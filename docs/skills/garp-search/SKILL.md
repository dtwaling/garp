---
name: garp-search
description: Use when searching unindexed mixed-format docs (PDF, DOC/DOCX/ODT/RTF, eml/mbox/msg, text) via ranked lexical proximity, a|b|c OR-groups, and --json excerpts; --code search is secondary.
---

# garp -- document-first lexical proximity search

`garp` runs ranked lexical proximity search over unindexed, mixed-format document trees, built for agent callers. One static pure-Go binary extracts and searches PDF, DOC, DOCX, ODT, RTF, eml, mbox, and msg alongside Markdown, plain text, HTML/XML, and config files on a live filesystem -- no external tools and no index (`--code` adds source files as a secondary mode). Identical behavior on Windows and Linux/macOS: run `garp` from PATH, else the repo binary (`bin/garp`; `garp.exe` on Windows). Forward slashes work in paths on every OS. Quote paths containing spaces; `--startdir` paths reject wildcards.

Invariants:
- **Always pass `--json`** (preferred), **`-l`** (when only matching file paths are needed), **or `--plain`** in agent sessions. Without one of these, garp launches the interactive TUI, which blocks a PTY and errors out without one.
- Errors go to stderr: check the exit code before parsing stdout, and zero-check the top-level `matches` integer before iterating `results`.
- Tuning flags (`--workers`, `--heavy-concurrency`, `--file-timeout-binary`) have safe defaults; leave them. Exception: if top-level `timed_out` is above 0, rerun with a higher `--file-timeout-binary` (max 60000) to search the skipped binary files.

## When to use garp vs rg (and what garp is not)

Default rule: unindexed PDF/Office/email/mixed-document trees or multi-term lexical proximity -> `garp`; exact symbol, literal string, regex, or exhaustive line scan in plain text/code -> `rg`.

- **Not semantic search:** `garp` is purely lexical and index-free. Natural-language questions (`"how are ignored directories skipped"`) return 0 matches unless those literal words co-occur; the caller supplies vocabulary.
- **Not a `ripgrep` (`rg`) replacement:** `rg` scans raw plain-text and source bytes directly without document extraction, supports full regexes and `-C`/`-A`/`-B` line context, and lists every matching line.
- **Not a code-structure tool:** `garp` has no AST, symbol table, call graph, or type/import resolution; use LSP, `ast-grep`, `ctags`, or `rg` for definitions, callers, and structural queries.

| Scenario | Use | Avoid |
|---|---|---|
| Unindexed PDF, DOC, DOCX, ODT, RTF, eml, mbox, or msg trees | `garp` (pure-Go extraction + ranked `--json` excerpts) | `rg` alone (no Office/PDF/MSG text extraction) |
| Mixed document + Markdown/HTML/text folders where 2+ domain terms co-occur within `--distance` (`1000..5000`) | `garp` (walk-derived BM25F + proximity ranking; primary hits need both slots on 2-slot or any `2+` on `3+`-slot queries, `--strict` requires all slots) | Ad-hoc multi-pass `rg` intersection loops |
| Uncertain document/domain wording where the caller can enumerate synonyms (`'amendment\|addendum\|rider' lease`) | `garp` (`a\|b\|c` satisfies one query slot in one walk) | Passing natural-language questions (no synonym or embedding inference) |
| Large prose, Markdown, or log files needing one ranked excerpt + `start_line` instead of result paging | `garp` (bounded-overlap `-m` excerpts + targeted read at `start_line`) | Avoid `garp` when every matching line or `-C`/`-A`/`-B` context is needed (use `rg` there) |
| Single exact symbol, identifier, string literal, or full regex in plain text or code | `rg` | `garp` |
| Symbol definitions, callers, references, imports, or AST patterns | LSP, `ast-grep`, `ctags`, or `rg` | `garp` |
| Secondary code search (`--code`): finding where 2+ domain terms or `a\|b\|c` synonyms co-occur near each other (`--distance 100..400`, `deploy*`, `--partial`) when exact identifiers are unknown | `garp --code` | Keeping `garp` once you know the exact symbol or regex (switch to `rg`) |

## Query formulation & zero-match recovery

- **Caller owns vocabulary & query expansion:**
  - Strip question words and filler (`how`, `where`, `is`, `handled`, `what`) -- every positional argument is a lexical query slot: `--strict` requires every slot, non-strict `2`-slot primary matches require both slots (admitting single-slot hits only via scarce rare-anchor fallback when `N >= 2` and primary matches `< 5`), and on `3+` slots filler dilutes slot coverage or surfaces noisy rare-word fallbacks on small trees.
  - Expand uncertain terms **in one call** with `a|b|c` OR-group slots (quote pipes in shells) rather than running sequential single-word retries; combine per alternative with `--partial` (`>= 3`-byte prefix stems), inline `*`/`?` wildcards, or `--smart-forms` (appends `s`, `es`, `ed`, `ing`, `al`, `tion`, `ation` directly to the literal stem without silent-`e` stripping, e.g. `renew` -> `renewal`/`renewed`; to bridge silent-`e` stems such as `approve` -> `approval`, use `'approve|approved|approval'`, inline wildcard `approv*`, or the shortened stem `approv` with `--partial` -- bare `approve --partial` still requires the trailing `e` and misses `approval`).
- **How to react when `matches == 0` (check in order instead of repeating the query):**
  1. **`timed_out > 0`:** heavy binary extraction hit `--file-timeout-binary` (default `1000` ms) -- rerun with `--file-timeout-binary 10000` (max `60000`).
  2. **File-type or ignore scope:** source files require `--code` (or `-t <ext>`); `.xls`/`.xlsx`/`.ppt`/`.pptx`/`.ods`/`.odp` are not extracted; `.gitignore`/`.ignore` rules skip ignored trees unless targeted via direct-child `--pathscope child/` or `--no-ignore`.
  3. **Document/markup cleaning:** in `.pdf`, `.doc`, `.docx`, `.odt`, `.rtf`, `.eml`, `.mbox`, `.msg`, `.html`, and `.xml`, letter/digit identifiers stay intact; the only spacing change is a space added after a comma when one is missing. `>`-quoted lines are removed. Identifiers match as whole words with `_` as a word character, so bare `Olk10SideProps` does not match `Olk10SideProps_0001` -- search the full identifier, or use `Olk10SideProps*` or `--partial` for a prefix.
  4. **Strictness, rarity boundary, or window size:** `--strict` requires every slot in `--distance` bytes and disables fallback. Without `--strict`, primary matches require both slots on `2`-slot queries (any `2+` slots on `3+` slots), while single-slot fallback applies to `2+`-slot queries only when `N >= 2` readable files exist, fewer than `5` primary files match, and the matched slot has `1 <= df <= max(1, floor(0.05*N))` (so single-file scopes `N < 2` or common words above `max(1, floor(0.05*N))` return `0` matches when no `2`-slot window hits). Inspect `term_count`/`matched_terms` on non-strict hits to spot `1`-slot fallbacks; to recover missed co-occurrence, widen uncertain slots with `a|b|c` synonyms, drop `--strict`, shorten stems with `--partial` or `*`, add `--smart-forms`, or raise `--distance`.
  5. **Wrong tool for the query:** if searching plain text or code for a literal substring, punctuation-heavy token, or regex, switch to `rg`.

## Flags that matter

- `--startdir <dir>`: search roots (default: cwd). Use one comma-separated value for a multi-repository search; specify the flag once. An existing directory name that contains a comma stays intact.
- `--pathscope <patterns>`: comma-separated path patterns in one flag occurrence; a `!pattern` entry excludes matching paths. Anchored prefixes such as `src/` prune unrelated directories during the walk. Uses the path pattern rules below and intersects with positive `-g` / `--iglob` globs. A positive pattern whose first segment names a direct child of the root (`child/`, `child/src/`) searches that child even when a root ignore file ignores it; the child's own ignore files still apply.
- `-g <pattern>` is case-sensitive and `--iglob <pattern>` is case-insensitive; both are repeatable, positive globs form one union, and commas are literal. A `!pattern` glob (e.g. `!archive/**`) excludes matching paths. Bare `name` matches basenames at any depth. `name/`, `./name`, and patterns that contain `/` are root-anchored. `*` and `?` stay within one segment; `**` crosses segments.
- `--code`: include source files, including Dockerfile/Containerfile names. Markup cleaning applies only to HTML/XML, email (eml/mbox/msg), and extracted documents (pdf/doc/docx/odt/rtf); code and plain-text/config files (txt, md, csv, yaml, log, ...) match literally, including `<...>`, `>` lines, and tokens like `SHA256`. HTML stays markup even with `--code`, so positive and `--not` terms on `.html` files match cleaned text. Single-line tags (with attributes), comments, entities, and `script`/`style` bodies are removed; an element tag that spans lines stays searchable. `-t <ext>` restricts file types. Repeat the flag or use comma-separated values. A type restriction overrides `--code`.
- `.gitignore`, `.ignore`, and `.git/info/exclude` are respected by default. A directory those rules ignore is skipped even when it holds its own Git repository (as `git` and `rg` do); a nested repository that is not ignored uses only its own ignore files. To search an ignored nested repository, pass it as `--startdir`, or scope to it with `--pathscope <child>/` when it is a direct child of the root. Use `--no-ignore` to search ignored files; other search filters still apply.
- `--max-filesize <size>`: skip files above the size cap; text/code files default to `5M` when omitted. Requires a positive size, up to `100M`; `0` is invalid.
- `--distance N`: proximity window in UTF-8 bytes (default 5000; non-ASCII characters take 2-4 bytes). Same call/block in code ~100-300; same paragraph/section in docs ~1000-2000.
- `-m N`: proximity excerpts per file (default 1, max 50), not raw matching lines.
- `--max-results N`: maximum ranked matching files returned (default `50` in `--json` and `--plain`; `-l`/`--pathonly` defaults to `0`; `0` = unlimited).
- `--partial[=prefix|contains]`: boundary-aware prefix (default) or raw substring. Per-term inline wildcards `*` and `?` work at any position: leading, mid-token, or trailing (`deploy*`, `ms-*-api`, `*OptionApi*`, `mark?systems`; quote terms in shells). `--smart-forms`: match word forms (plurals, -ing, -ed, -tion).
- `-i` and `-s`: select case-insensitive or case-sensitive matching; the last case flag wins.
- `a|b|c`: one query slot satisfied by any alternative. Quote pipes in shells; escape a literal pipe as `\|` (`garp 'a\|b' --json`). `\\|` means one literal backslash plus an OR separator; tokens without `|` preserve backslashes verbatim. Wildcards (`deploy*|rollout`), partial matching, and smart forms apply per positive alternative. garp does not infer synonyms.
- `--strict`: require every slot, not every alternative, in the window; disable both partial multi-term admission and single-slot fallback. Otherwise two-slot primary matches require both slots and 3+ slots require any 2+, with no first-term privilege. If fewer than five primary files match, append at most `min(3, 5 - len(primary))` single-slot files whose matched slot has `1 <= df <= max(1, floor(0.05*N))` across `N >= 2` readable files. Common-word-only matches are intentionally withheld; no lexical overlap means no match.
- `-l`: skip excerpt extraction and emit one distinct matching file path per line in ranked order. Zero matches produce empty stdout.
- `--not <tokens...>`: exclusions after the flag; `.ext` excludes a file type, bare words exclude whole words. OR-groups work here too: `--not 'mock|stub'` or `--not '.pdf|.docx'`. Any alternative excludes; wildcards/partial expansion do not apply to exclusions.

OR-specific bounds: 16 raw alternatives per group, 32 normalized positive
slots, 64 normalized positive alternatives total; 64 expanded exclusions
(without deduplication). Positive and exclusion bounds activate independently
when their list uses an unescaped OR separator, even if duplicates collapse.
Malformed groups (`|`, `a||b`, `a|`) or over-limit input exits **1** before
searching, never truncates. Non-OR inputs retain their uncapped behavior.
`--startdir` and `--pathscope` still reject `|`.

Value flags accept both `--flag value` and `--flag=value`. Use `--` to stop
flag parsing. These short flags follow rg conventions, but garp is not a
drop-in rg replacement. Context flags such as `-C`, `-A`, and `-B` are not
supported; use `--distance` and `-m`.

Both `--partial` modes (`prefix` and `contains`) require terms of at least
3 characters for partial matching. The short-term guardrail (`len(term) <= 2`)
keeps shorter terms whole-word to prevent noisy substring explosion; the
implementation measures length in bytes, per alternative (`id|identifier`
keeps `id` whole-word with `--partial`). Explicit inline wildcards such as
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
- Sort order: primary matches before single-slot fallbacks, then score, matched-term count, path-term count, boundary quality, tighter span, top-quality chunk count, ranking-root-relative path, supplied root order, then canonical path. File `score` is walk-derived BM25F times proximity and slot coverage, rounded to `1e-4` (also in plain output and the TUI), not an integer term-length sum. Inspect `term_count` and `matched_terms` to distinguish full, partial, and single-slot match tiers.
- Each excerpt: `text` (context-padded, newlines collapsed), per-chunk IDF-sum `score` and `term_count`, plus 1-based `start_line` = line of the earliest matched term. Only `start_line` is omitted for extracted formats (PDF, DOCX, email) without stable source lines.
- `query.terms` echoes normalized canonical slots (including OR labels and literal-pipe escapes). `term_count` counts covered slots; `matched_terms` names the selected matched alternative per covered slot, in slot order. `--plain` stays ANSI-free and reports matched/total slots, not alternatives.
- When matched terms are further apart than the excerpt display budget (`span > budget`), multipart excerpts join per-term context windows with `" ... "` (space, three ASCII dots, space). Windows are in source order and overlapping windows are merged; the joiner marks omitted text, not a contiguous source passage.
- Chunks within a file are a ranked subset, best-first: with `-m N` where `N > 1`, every quality tier can seed a chunk, so a strong cluster no longer hides the file's other clusters. Every chunk is >=90% new content (bounded overlap).
- Act on results by reading the file at `start_line` (with margin) instead of re-searching or paging through reads.
- `query.start_dir` is the first reported root. `query.start_dirs` appears only for two or more reported roots. A nested supplied root can become traversal-only to prevent data loss; results remain under its reported ancestor. Both fields are omitted when `--startdir` is absent.
- `query.only_type` remains the singular compatibility field. `query.only_types` appears only for two or more distinct types. `query.case_sensitive` appears only when enabled.

```jsonc
"results": [ { "file": "...", "score": 14.1509, "term_count": 2, "matched_terms": ["a", "b"],
  "excerpts": [ { "text": "...", "start_line": 42, "score": 4.0328, "term_count": 2 } ] } ]
```

Example (mixed-format document & email tree):

```sh
garp 'amendment|addendum|rider' lease termination --startdir <docs-dir> --distance 1500 --json
```

Locate Markdown/PDF deployment notes (`garp AMI parent base --startdir <repo> -t md --distance 200 --json`).

Secondary `--code` single-call expansion in the `garp` repository:

```sh
garp 'ignore|skip|exclude|prune' walk gitignore --code --distance 400 --json
```

This replaces four separate synonym probes. `query.terms` is
`["ignore|skip|exclude|prune", "walk", "gitignore"]`; `search/ignore.go`
returns as a 2-of-3 slot hit with `matched_terms: ["ignore", "gitignore"]`
(it lacks whole-word `walk`). Reordering slots or alternatives does not change
ranked file order; matched alternatives are still reported in query-slot order.
