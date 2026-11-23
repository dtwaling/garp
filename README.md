<div align="center">

# :: garp ::

![Version](https://img.shields.io/badge/version-0.11.0-blue?labelColor=0052cc)
![License](https://img.shields.io/github/license/dtwaling/garp?color=4338ca&labelColor=3730a3)
![Platform](https://img.shields.io/badge/platform-linux-4338ca?logo=linux&logoColor=white&labelColor=3730a3)
![Platform](https://img.shields.io/badge/platform-macos-4338ca?logo=apple&logoColor=white&labelColor=3730a3)
![Platform](https://img.shields.io/badge/platform-windows-4338ca?logo=windows&logoColor=white&labelColor=3730a3)

![Last Commit](https://img.shields.io/github/last-commit/dtwaling/garp?color=5b21b6&labelColor=4c1d95)
![Code Size](https://img.shields.io/github/languages/code-size/dtwaling/garp?color=4338ca&labelColor=3730a3)
![Language](https://img.shields.io/badge/language-Go-4338ca?logo=go&logoColor=c7d2fe&labelColor=3730a3)
![Build](https://img.shields.io/badge/build-makefile-4c1d95?labelColor=1e1b4b)

</div>

Ranked lexical proximity search over unindexed, mixed-format document trees,
built for agent callers and interactive TUI triage. One static pure-Go binary
extracts and searches PDF, DOC, DOCX, ODT, RTF, eml, mbox, and msg alongside
Markdown, plain text, HTML/XML, and config files on a live filesystem -- no
external tools and no persistent index. `garp` ranks order-invariant multi-term
co-occurrence (with single-call `a|b|c` OR-group slots, scarce rare-anchor
fallback, and structured `--json` output); `--code` extends the same proximity
search to source trees as a secondary mode.

No relation to the [John Irving novel](https://en.wikipedia.org/wiki/The_World_According_to_Garp).
More like a mispronounced "grep" that's easy to remember.

Forked from [CyphrRiot/garp](https://github.com/CyphrRiot/garp). Original concept,
core search engine, TUI, and document extraction are the work of
[CyphrRiot](https://github.com/CyphrRiot).

![garp TUI](garp.png)

## What garp is not

- **Not semantic search:** `garp` is purely lexical and index-free -- no embeddings, vector index, or synonym inference. A natural-language query such as `garp "how are ignored directories skipped"` returns zero matches unless those literal words co-occur; the caller supplies vocabulary via domain terms, `a|b|c` OR-groups, `--partial`, `*`/`?`, or `--smart-forms`.
- **Not a ripgrep (`rg`) replacement:** Use `rg` instead for single-token or exact symbol/string lookup, full regular expressions, `-C`/`-A`/`-B` line context, or exhaustive line-by-line matches in plain text and source code. `rg` scans raw bytes directly without document extraction or corpus-counting passes (for example, informational single-pass wall time across the pinned 42-query, 206-file `code` corpus in [eval/results/v0.10.0.md](eval/results/v0.10.0.md) is `0.662515 s` total for `rg` versus `1.481326 s` for `garp v0.10.0`; see [eval/README.md#known-limitations](eval/README.md#known-limitations) -- one timed pass is not a statistical performance benchmark).
- **Not a code-structure tool:** `garp` has no AST, symbol table, call graph, or type/import resolution. Use LSP, `ast-grep`, `ctags`, or `rg` for definitions, callers, references, and structural code queries; use `garp --code` only when locating where 2+ domain words or `a|b|c` alternatives co-occur within a function or module.
- **Measured retrieval scope (no unverified claims):** On the [retrieval-quality eval harness](eval/README.md) (`make eval` after `make eval-fetch`; pinned 42-query, 430-file mixed `docs` corpus and 42-query, 206-file `code` corpus; see [eval/README.md#readable-corpus-denominator-and-bm25-returned-counts](eval/README.md#readable-corpus-denominator-and-bm25-returned-counts) and [eval/results/v0.10.0.md](eval/results/v0.10.0.md)), corpus-aware `garp v0.11.0` reaches `MRR@10` `0.785714` on `docs` (`409` readable files in `v0.11.0`, prior to `v0.11.1` PDF framing recovery) and `0.663520` on `code`, versus `garp v0.10.0` (`0.591270` / `0.504195`), whole-document `BM25` sharing `garp`'s extractors (`0.754195` / `0.635185`), and a raw-byte `rg` slot-conjunction pipeline (`0.269841` / `0.341138`, where `rg` uses default binary handling without Office/PDF/MSG extraction). In paired synonym evaluation (`make eval-or`, [eval/README.md#paired-synonym-or-evaluation-and-known-key-gaps](eval/README.md#paired-synonym-or-evaluation-and-known-key-gaps)), caller-authored `a|b|c` expansions lift `R@10` from `0.750000` to `0.916667` on eligible `docs` synonym queries (`12` queries) and from `0.642857` to `0.892857` on eligible `code` synonym queries (`14` queries, excluding registered key gap `code-g-011`; see [Known limitations](#known-limitations) and [eval/README.md](eval/README.md) for frozen-key, top-rank precision, and extractor caveats).

## Quick start

```bash
# Mixed-format document & email trees (PDF, DOC/DOCX/ODT/RTF, eml/mbox/msg, md, txt)
garp contract payment agreement --json
garp report earnings --only pdf --json
garp 'amendment|addendum|rider' lease termination --distance 1500 --json
garp wire remittance --not '.txt|.csv' 'draft|sample' --json
garp auth migration -s -t md -t pdf -g '!archive/**' -l

# Multi-term ranking, strict conjunction, word forms, and partial matching
garp indemnity liability ceiling --distance 1000 --json   # any 2+ slots in window, BM25F + proximity ranked
garp indemnity liability ceiling --strict --json          # every slot required; no partial or single-slot fallback
garp renew notice --smart-forms --json                    # inflections: renews, renewed, renewing, renewal
garp deploy liability --partial --json                    # boundary-aware prefix: deployment, deploy_plan
garp deploy* liability --json                             # inline wildcard on deploy* only; liability stays whole-word
garp deploy liability --partial=contains --json           # raw substring: redeploy, autodeploy

# Scope walks to specific folders or roots (prunes unrelated branches; -l emits ranked paths only)
garp renewal notice --startdir ~/deals --pathscope 'contracts/,correspondence/' --json
garp deployment runbook --startdir 'ops-docs,service-a' -g 'docs/**' -l

# Code search (supported, secondary -- opt in with --code when exact symbols are unknown)
garp 'ignore|skip|exclude|prune' walk gitignore --code --distance 400 --json
garp pitch frequency --startdir ~/projects/audio2midi --pathscope 'audio2midi/,docs/' --code --json
```

## Key features

- Pure-Go mixed-document extraction on unindexed live trees -- `.pdf`, `.doc`, `.docx`, `.odt`, `.rtf`, `.eml`, `.mbox`, and `.msg` alongside `.md`, `.txt`, `.html`, `.xml`, `.csv`, and config files in one static binary with zero external tool dependencies and no persistent index
- Agent-ready machine output and interactive TUI -- `--json` (structured envelope with ranked excerpts, `matched_terms`, `truncated`, `timed_out`, and 1-based `start_line` on text/Markdown/code), `-l`/`--pathonly` (ranked file paths), `--plain` (ANSI-free line-oriented output), plus an interactive TUI with live progress, paging, and highlighted excerpts
- Unordered lexical proximity matching within `--distance` UTF-8 bytes (default 5000): two-slot primary matches require both slots; three or more slots admit any two or more distinct slots in a window with no first-term privilege
- Walk-derived corpus-aware BM25F ranking with proximity and slot coverage (not term-length weighting or a persistent index), plus non-strict scarce rare-anchor fallback across `2+`-slot queries: when `N >= 2` readable files exist and fewer than five primary files match, appends at most `min(3, 5 - len(primary))` single-slot files with `1 <= df <= max(1, floor(0.05*N))`, always after primary matches
- Caller-supplied vocabulary expansion -- single-call OR-group slots (`a|b|c`, with per-alternative wildcards, `--partial`, and `--smart-forms`), `--strict` full-slot conjunction, and `--not` extension/word exclusions (including OR-groups)
- Format-aware content cleaning and bounded large-file/binary guardrails -- strips tags, entities, `script`/`style`, and quote noise from HTML/XML, email, and extracted documents while matching literal text/Markdown/config/code verbatim, with size caps and `--file-timeout-binary` deadlines (see [Supported formats](#supported-formats) and [Content cleaning](#content-cleaning))
- Directory-scoped walk pruning via `--startdir`, `--pathscope`, `-g`/`--iglob`, and `.gitignore`/`.ignore` rules
- Secondary code search with `--code` (including `Dockerfile`/`Containerfile` builds) when searching source trees by multi-term proximity

## Install

**Option 1: Build from source**

```bash
git clone https://github.com/dtwaling/garp
cd garp
make install          # builds with pure-Go PDF support, installs to ~/.local/bin/garp
```

Ensure `~/.local/bin` is on your `PATH`.

**Option 2: Copy the prebuilt binary**

Both release binaries are committed to the repo: `bin/garp` (Linux) and
`bin/garp.exe` (Windows, cross-compiled). Both are pure Go; `bin/garp` is
statically linked, with no glibc dependency. Copy the one for your OS to any
directory on your `PATH`:

```bash
cp bin/garp ~/.local/bin/garp
chmod +x ~/.local/bin/garp
```

**Option 3: Download from GitHub Releases**

No checkout needed -- the latest release ships binaries for linux, macOS
(darwin), and Windows, in amd64 and arm64 flavors (`garp-<os>-<arch>[.exe]`):

```bash
# Linux (amd64)
curl -LO https://github.com/dtwaling/garp/releases/latest/download/garp-linux-amd64
chmod +x garp-linux-amd64 && mkdir -p ~/.local/bin && mv garp-linux-amd64 ~/.local/bin/garp
```

```powershell
# Windows (PowerShell)
curl -LO https://github.com/dtwaling/garp/releases/latest/download/garp-windows-amd64.exe
```

Or with the GitHub CLI:

```bash
gh release download --repo dtwaling/garp --pattern 'garp-*' --output ~/.local/bin/garp
```

```powershell
gh release download --repo dtwaling/garp --pattern 'garp-*' --output 'c:/cmd/garp.exe'
```

- note: use flag `--clobber` if replacing an earlier version.

## Agent / AI assistant use

A ready-made skill definition ships at `docs/skills/garp-search/SKILL.md`.
Copy the `garp-search/` directory into your harness skills location (e.g.
`.hermes/skills/`, `.claude/skills/`, or `.cursor/skills/`) for a guide to
when to use `garp` (unindexed mixed-format documents and multi-term proximity)
versus `rg` (exact code or plain-text symbol/regex lookup), caller-owned
`a|b|c` query expansion, zero-match recovery, and one-shot `--json` consumption
(find -> pinpoint -> targeted read, no result paging). One skill covers
Windows and Linux/macOS.

Repo-local agent skills live in `.agents/skills/` and load automatically for
sessions started inside this checkout (trust it once with
`hermes skills trust`). The validation playbook at
`.agents/skills/garp-perf-benchmarking/SKILL.md` holds the gauntlet / SQA /
performance-benchmark process used to validate changes to this project. It is
repo-owned, so keep it in sync with the code it validates.

## Flags

```
garp <word1|alt2> <word2> ... [flags] [--not <excl1|excl2> ...]
```

| Flag | Default | Description |
|------|---------|-------------|
| `--code` | off | Include code files (.go, .py, .js, .ts, ...) |
| `--distance N` | 5000 | Proximity window in UTF-8 bytes |
| `--startdir <path>` | cwd | Search roots as one comma-separated value. Specify the flag once. An existing directory name that contains a comma stays intact. |
| `--pathscope <patterns>` | (all) | Comma-separated path patterns in one flag occurrence. Prefix with `!` to exclude. Use repeatable `-g` for additional path globs. The scope intersects with positive globs. See path pattern rules below. |
| `-g`, `--glob <pattern>` | (all) | Repeatable case-sensitive path glob. Prefix with `!` to exclude. |
| `--iglob <pattern>` | (all) | Repeatable case-insensitive path glob. Prefix with `!` to exclude. |
| `--no-ignore` | off | Search files ignored by repository ignore files (`.gitignore`, `.ignore`, or Git `exclude`). |
| `--max-filesize <size>` | `5M` for text/code | Skip files larger than the given positive size; an explicit limit also applies to binary formats. Accepts B, K/KB/KiB, M/MB/MiB (binary units), up to `100M`. Omit for the default; `0` is invalid. |
| `-t`, `--type`, `--only <type>` | (all) | Restrict file types. Repeat the flag or use comma-separated values. |
| `--smart-forms` | off | Match word forms (plurals, -ing, -ed, -tion) |
| `--partial[=MODE]` | off | Enable partial matching. Bare form selects `prefix`; modes are `prefix`, `contains`, `off`, and `none`. |
| `-i`, `--ignore-case` | on | Match terms without case sensitivity. The last case flag wins. |
| `-s`, `--case-sensitive` | off | Match terms with case sensitivity through discovery, exclusions, ranking, and excerpts. |
| `--not <excl...>` | (none) | Tokens after this flag are exclusions, including OR-groups (`mock\|stub`). Dot-prefixed alternatives exclude extensions (`.pdf\|.docx`); others exclude whole words |
| `--strict` | off | Require every search slot in a proximity window; an `a\|b` slot is satisfied by any alternative. Disable both partial multi-term admission and single-slot fallback |
| `-l`, `--files-with-matches`, `--pathonly` | off | Skip TUI; emit only distinct matching file paths in ranked order. |
| `--json` | off | Skip TUI; emit structured JSON to stdout. Preferred for MCP/agent callers |
| `--plain` | off | Skip TUI; emit plain line-oriented text to stdout. Useful for shell scripts |
| `-m`, `--max-count`, `--max-excerpts N` | 1 | Maximum proximity excerpts per file, not raw matching lines. Maximum 50. |
| `--max-results N` | JSON/plain: 50; path-only/TUI: 0 | Maximum matching files to return. An explicit `0` means unlimited |
| `--workers N` | 4 | Stage 2 filter worker count |
| `--heavy-concurrency N` | auto | Concurrent heavy (binary) extractions |
| `--file-timeout-binary N` | 1000 | Timeout in ms per binary extraction; timed-out files are skipped and counted in `timed_out` |
| `--help`, `-h` | | Show help |
| `--version`, `-v` | | Show version |

Value flags accept space and equals forms, such as `--startdir repo` and
`--startdir=repo`. Use `--` to stop flag parsing. Context flags such as `-C`,
`-A`, and `-B` are not supported; use `--distance` and `--max-excerpts`.
The listed aliases are compatible with rg conventions. garp is not a drop-in
rg replacement.

### Path pattern rules

A bare `name` matches a basename at any depth. `name/`, `./name`, and any
pattern that contains `/` are anchored to the search root. `*` and `?` stay
within one path segment; `**` crosses segments.

On all platforms, an absolute pathscope or user-glob pattern must overlap an
effective `--startdir`. Examples are `/repo/src/**` on POSIX and
`C:/repo/src/**` on Windows. A pattern can select a path inside the root, or an
ancestor pattern can cover the root. If no root intersects it, validation fails
before search. Use `./src` for explicit root-relative matching. On Windows, a
leading slash that is not an absolute path is root-relative.

A positive `--pathscope` pattern whose first segment names a direct child of
`--startdir` (for example `child/`, `child/**`, `child/src/`, or bare `child`)
searches that child even when an ignore file above it (`.gitignore`, `.ignore`,
or Git `exclude`) ignores the directory itself. This lets you target an ignored
workspace child directory without `--no-ignore`. For an ordinary child,
inherited file rules such as `*.log` and the child's own ignore files still
apply. If the child is a nested Git repository, its contents use that
repository's own ignore rules; parent rules do not carry in. Ignored directories
deeper than the direct child stay skipped. `!` patterns, patterns that start
with `**`, bare patterns that contain `.`, and `-g` / `--iglob` globs never
exempt a directory.

### Partial word matching and inline globs

By default, garp uses whole-word matching with plural support. `--partial` enables
boundary-aware `prefix` matching, so `deploy` matches `deployment`, `deployed`,
and `deploy_service` without matching internal substrings such as `redeploy`.
`--partial=prefix` is equivalent to bare `--partial`.

Use `--partial=contains` for raw substring matching when internal matches are
useful: `deploy` also matches `redeploy` and `autodeploy`. Contains mode is broad,
so prefer prefix mode for technical terms and code identifiers when possible.

Both `prefix` and `contains` require terms of at least 3 characters for partial
matching: the short-term guardrail (`len(term) <= 2`) keeps shorter terms in
whole-word mode to prevent noisy substring explosion. The implementation measures
length in bytes. Explicit inline wildcards such as `al*` still work for short terms.

Use `*` and `?` inside an individual query term for inline wildcard matching.
For example, `ms-*-api` matches `ms-option-api`, `ms-?-api` matches
`ms-a-api`, and `*OptionApi*` matches `defaultMsOptionApiOrigin`. `?` matches
exactly one byte. An internal `*` matches zero or more bytes. An edge wildcard,
`*` or `?`, removes the matcher boundary on that side. An edge `?` still
consumes one byte. An edge `*` consumes no bytes in the selected exact range.
Inline wildcards work with every partial mode.

Wildcard expansion accepts only ASCII letters, digits, `_`, `.`, and `-`.
It cannot consume spaces, tabs, newlines, or other punctuation. Consecutive
stars collapse before matching and output, so `ms-**-api` becomes `ms-*-api`.

A wildcard term is valid only when it has 2 to 128 literal bytes, at most four
`*` characters, and at most 16 `?` characters after star collapse. Each
internal `*` can consume at most `floor(128 / canonical star count)` bytes.
The divisor includes all canonical `*` characters, including leading and
trailing stars. If a term exceeds any bound, garp treats `*` and `?` as literal
characters and applies whole-token matching. This fallback applies in `off`,
`prefix`, and `contains` modes.

Prefix matching supports standard punctuation plus `snake_case` and `kebab-case`
identifier boundaries. Go's RE2 regex engine does not support zero-width
lookbehinds, so camelCase transitions without separators, such as `autoDeploy`,
are not recognized as prefix boundaries. Supporting those transitions requires a
future custom scanner.

Terms supplied after `--not` are always whole-word exclusions, regardless of
`--partial` or inline globs. This prevents a broad partial query from accidentally
filtering unrelated results. Word exclusions check the same cleaned Stage 2 text
as positive search terms.

### OR-groups (a|b|c) and literal pipe escaping

An unescaped `|` joins alternatives within **one concept slot**, not extra
required terms. Quote the argument so the shell does not interpret the pipe:

```sh
garp 'ignore|skip|exclude|prune' walk gitignore --code --distance 400 --json
garp 'deploy*|rollout' service --strict --not 'mock|stub'
garp 'id|identifier' lookup --partial --not '.pdf|.docx'
garp 'a\|b' --json                  # search for literal a|b
```

Two-slot primary matches require both slots; three or more slots admit any
two or more in a window. `--strict` requires every slot, **not every
alternative**, and disables scarce single-slot fallback. Wildcards, partial
matching, and `--smart-forms` apply independently to each positive alternative.
For `id|identifier --partial`, the `len(term) <= 2` byte guardrail keeps `id`
whole-word while `identifier` gets prefix matching. Exclusion alternatives
remain whole-word literals (or dot-prefixed extensions), not wildcard or
partial expansions; any excluded alternative is sufficient to reject a file.

Backslash escaping is contextual: `\|` encodes a literal pipe; `\\|` encodes
one literal backslash followed by an OR separator. More generally, pairs of
backslashes immediately before `|` become literal backslashes, and an odd
remaining backslash escapes the pipe. Other backslashes, including all those
in tokens without `|`, remain verbatim. `--startdir` and `--pathscope` still
reject `|`; query OR syntax does not change path parsing.

Alternatives are normalized and deduplicated within each slot; equivalent
slots also deduplicate regardless of alternative order. Display preserves
first-seen slot and alternative order, while ranking is permutation-invariant.
Bounds activate only when the corresponding positive query or `--not` list
uses an unescaped OR separator, even if duplicates later collapse:

- At most **16 raw alternatives per group**, before deduplication.
- Positive queries: at most **32 normalized slots** and **64 normalized
  alternatives across those slots**.
- Exclusions: at most **64 expanded alternatives**, without deduplication.

Malformed groups (`|`, `a||b`, `a|`) or over-limit groups fail with CLI exit
code **1** before searching; nothing is silently truncated. Non-OR queries
and exclusion lists remain uncapped by these OR-specific limits.

### --json output shape

```json
{
  "query": {
    "terms": ["pitch", "frequency"],
    "start_dir": "/path/to/dir",
    "start_dirs": ["/path/to/dir", "/other/repo"],
    "path_scope": ["audio2midi/", "docs/"],
    "include_code": true
  },
  "matches": 1,
  "returned": 1,
  "truncated": false,
  "timed_out": 0,
  "results": [
    {
      "file": "/path/to/audio2midi/dsp.py",
      "size_bytes": 15649,
      "score": 14.1509,
      "term_count": 2,
      "matched_terms": ["pitch", "frequency"],
      "excerpts": [
        {
          "text": "...clean text snippet around matched terms...",
          "start_line": 42,
          "score": 4.0328,
          "term_count": 2
        }
      ]
    }
  ]
}
```

`query.start_dir` contains the first reported root. `query.start_dirs` appears
only when at least two reported roots remain. A nested supplied root can become
traversal-only to prevent data loss; results remain under its reported ancestor.
Both fields are omitted when `--startdir` is not supplied. `query.only_type`
remains present for one or more selected types. `query.only_types` appears only
for two or more distinct types. `query.case_sensitive` appears only when
case-sensitive mode is active.

Each result includes ranked-match metadata. File `score` is a floating-point
BM25F score using walk-derived document frequency, cleaned document length,
document-wide term frequency and a relative-path bonus, multiplied by proximity
and the fraction of query slots covered by the representative window. Scores
are rounded to `1e-4` in JSON, plain output, and the TUI; insignificant trailing
zeroes need not be printed. Excerpt scores instead sum the matched slots' IDF
weights. The numbers in the example illustrate the shape, not fixed scores for
those terms. Scores depend on the searched corpus, not just the matching file.
`query.terms` echoes normalized canonical slots, for example
`["ignore|skip|exclude|prune", "walk", "gitignore"]`, retaining syntax escapes
for literal pipes. `term_count` counts slots in the representative window;
`matched_terms` reports the selected matched alternative for each covered slot
in slot order, not every alternative or the entire OR-group label. For example,
`search/ignore.go` is a 2-of-3 slot hit with `["ignore", "gitignore"]` in the
query above. Inspect both fields to distinguish partial and single-slot matches.

Every output mode uses the same ten ranking tiers:

1. `!IsFallback` first: primary matches always precede single-slot fallbacks
2. rounded `score`, descending
3. `term_count`, descending
4. `PathMatchCount`, descending
5. `BoundaryQuality`, descending
6. `SpanLength` (match span length), ascending
7. `TopQualityChunks`, descending
8. ranking-root-relative path, ascending: slash-normalized, case-insensitive
   byte order, then exact byte order
9. supplied `rootOrder`, ascending
10. canonical path, ascending

`PathMatchCount` is the number of distinct query terms represented in the
path relative to the search root. `BoundaryQuality` rewards terms that match
on both identifier boundaries. `TopQualityChunks` counts non-overlapping
windows tied for the file's best slot coverage and IDF weight. These fields set
file order before excerpt extraction. `--max-excerpts` cannot change file order.

Chunks within a file form a per-file ranked subset: with `--max-excerpts N`
every match quality tier from the query's minimum requirement up to the full
term set can seed a chunk, emitted best-first (most matched slots, then highest
IDF sum). Single-slot fallback seeds must pass the rarity gate. A file's best
cluster no longer hides its separate lower-quality clusters. Each `--json`
excerpt carries its own `score` and
`term_count` (omitted for chunks that did not frame a ranked window), and
`--plain` excerpt headers include them: `EXCERPT 1 [L42, Score: 4.0328 (2 terms)]`.
The bounded-overlap guarantee is unchanged: every chunk is >= 90% new content
relative to earlier chunks from the same file.

When partial matching is active, the JSON query object includes
`query.partial` with the selected mode, for example `"partial": "prefix"` or
`"partial": "contains"`. The field is omitted when partial matching is off,
preserving compatibility for callers that use the legacy whole-word default.

Each excerpt is an object: `text` plus a 1-based `start_line`. `start_line` points at the line
of the **earliest matched search term** in that chunk -- not necessarily the first line of the
rendered `text` (the excerpt window pads a little context around the match and collapses newlines
into one line). In practice this is what you want: it lands you on the match. Line numbers are
present for text/code files and omitted for binary/extracted formats (PDF, DOCX, email) that have
no stable source lines. The plain (`--plain`) output prefixes each excerpt with `[L<start>]` when known.
Each plain result begins with `MATCH X/Y [Score: S (T/N terms)]`, where `Y`
is the number of returned files, `S` is the score, `T` is the matched-slot
count, and `N` is the total query-slot count (alternatives do not inflate it).
`--plain` remains ANSI-free. When output is truncated, plain
output ends with this exact footer:

```text
# truncated: showing <returned> of <matches> matching files (use --max-results 0 for all)
```

The footer is omitted when all matching files are returned.

`--max-results` limits returned files after ranking. JSON and plain output
default to 50. Path-only output and the TUI default to unlimited. An explicit
`--max-results 0` selects unlimited output in every mode.

JSON always includes `matches`, `returned`, `truncated`, and `timed_out`.
`matches` is the deduplicated matching-file count, adjusted only for file-open,
extraction, or zero-excerpt failures observed while garp fills the result limit.
`returned` is the number of entries in `results`. `truncated` is true when
`returned` is less than the adjusted `matches`. Errors go to stderr; check the
exit code before parsing stdout as JSON.

`timed_out` counts binary files whose extraction exceeded `--file-timeout-binary`,
including the wait for a heavy-extraction slot. Those files are not in `matches`:
a Stage 2 timeout drops a file before garp knows whether it matches, while a
Stage 3 timeout drops a file that had already matched. When `timed_out` is above
0, `--json`, `--plain`, and `-l` print one `Warning:` line to stderr after stdout.
stdout is unchanged and the exit code stays 0. The TUI header shows `Timed out N`.
Rerun with a higher `--file-timeout-binary` (max 60000) to search the skipped files.

### Bounded-overlap excerpts

When matched terms are further apart than the excerpt display budget
(`span > budget`), garp builds a multipart excerpt from per-term context windows.
It orders these windows by source position, merges overlapping windows, and joins
the remaining parts with `" ... "` (space, three ASCII dots, space). The joiner
marks omitted text; the excerpt need not be one contiguous source passage.

`--max-excerpts` controls how many excerpts garp can return for each matching file.
The default is one and the maximum is 50. When more than one excerpt is requested,
garp uses skip-ahead selection: after capturing a chunk, it evaluates the next chunk
outside the prior captured chunk rather than repeatedly returning the same nearby
match.

Excerpt windows may overlap by up to 10% when a small boundary-context overlap makes
the result more useful. Every emitted chunk is therefore at least 90% new content
relative to earlier chunks from the same file. A cluster whose candidate window would
contain more than 10% old content is skipped as redundant; separate clusters remain
eligible for their own excerpts.

This per-file guarantee applies consistently to the `excerpts` array in `--json`,
the excerpt blocks in `--plain`, and the excerpt lists shown in the TUI.

## Supported formats

**Documents (default)**

- Text: `.txt`, `.md`, `.log`, `.rtf`
- Web: `.html`, `.xml`
- Data/Config: `.csv`, `.yaml`, `.yml`, `.cfg`, `.conf`, `.ini`, `.sh`, `.bat`
- Email: `.eml` (MIME parsing), `.mbox` (message collections), `.msg`
- Office: `.pdf` (guardrailed), `.doc`, `.docx`, `.odt`

**Code (with `--code`)**

`.go`, `.py`, `.js`, `.ts`, `.java`, `.c`, `.h`, `.cpp`, `.cc`, `.cxx`, `.hpp`,
`.m`, `.mm`, `.cs`, `.rs`, `.rb`, `.swift`, `.kt`, `.scala`, `.clj`, `.pl`, `.r`,
`.sql`, `.php`, `.phtml`, `.json`, `.pas`, `.dpr`, `.dpk`, `.inc`, `.dfm`, `.fmx`,
`.dproj`, `.groupproj`

Container build files (matched by name, since they're usually extensionless): `Dockerfile`,
`Dockerfile.*` (e.g. `Dockerfile.dev`), `Containerfile`, and `*.dockerfile`. `--only dockerfile`
restricts to just this family.

**Binary extraction (pure Go)**

| Format | Library |
|--------|---------|
| EML | `enmime` |
| MBOX | `emersion/go-mbox` |
| PDF | `ledongthuc/pdf` |
| DOC | `richardlehane/mscfb` + `WordDocument`/`1Table`/`0Table` streams (2 MiB total): Word 97-2003 text from the FIB piece table (Windows-1252 or UTF-16LE pieces); otherwise UTF-16 when a BOM or dominant NUL lane proves it, else printable ASCII, plus a UTF-16LE reading only with sufficient odd-lane NUL density and dominance or aligned UTF-16LE markup; non-CFB or textless files fall back to printable-ASCII salvage |
| DOCX/ODT | `archive/zip` + XML |
| RTF | regex/control-word stripping |
| MSG | `richardlehane/mscfb` + MAPI property-typed Subject/Body: Unicode `001F` (UTF-16LE), ANSI `001E` (UTF-8, else Windows-1252); HTML body (`001F`/`001E`/`0102`) when no plain body, with a `0102` UTF-16LE reading only with sufficient odd-lane NUL density and dominance or aligned UTF-16LE markup; requires an Outlook OLE/CFB container -- other .msg files are not searched, and a message without Subject/Body/HTML text yields no text |

> PDF note: extractions share `--heavy-concurrency` slots -- auto: half of
> `--workers`, at most 1 per 4 GiB of available RAM, clamped to 1..8.
> Each extraction has a `--file-timeout-binary` deadline (default 1000 ms, max 60000 ms);
> a file that exceeds it is skipped and counted in `timed_out`.
> Extracted text is capped at 8 MiB per PDF, cumulative across pages. A PDF over
> the cap is skipped, not partially searched; its unextracted pages are reported
> as `Truncated`. Standard-encrypted PDFs with an empty user password (40- to
> 128-bit RC4 `V=1`/`2`, or 128-bit AES `V=4` with `/EncryptMetadata` true or
> absent) are decrypted automatically; files that require a non-empty user
> password or use unsupported encryption (such as AES-256 `V=5`, `V=4` RC4
> crypt filters, or 128-bit AES `V=4` with `/EncryptMetadata false`) are skipped.
> Parser panics
> on malformed PDFs or unsupported page operators (such as raw inline-image
> `BI`/`ID`/`EI` data) are recovered per page -- unreadable pages yield no text
> while remaining pages still extract. A `%PDF-1.0`..`%PDF-1.7` header whose
> 8-byte token lies within the first 1024 bytes (followed by at most 256
> spaces or tabs before `\r` or `\n`) is accepted with `xref` offsets relative
> to `%PDF-`, and `%%EOF` is located within the final 1024 bytes; files outside
> those bounds or otherwise unopenable are skipped. There is no page-count or
> per-search PDF limit.
>
> PDF text comes from each page's own content stream. Text drawn inside a Form
> XObject (a `/Do` sub-stream, common for embedded figures and their labels) is
> not extracted. Positioned glyphs are ordered by unrotated horizontal page
> coordinates (`Y` then `X`), so 90/270-degree rotated text matrices can
> interleave characters across adjacent rotated lines. A word break is emitted
> after each `TJ` text run as a newline glyph decoded through the run's font.
> When that font's encoding maps code 10 to a glyph (TeX OT1 fonts map it to
> `/Omega`), an Ohm sign (U+2126) follows the run in excerpts, for example
> `alpha` followed by U+2126. The run's own words stay searchable.

Spreadsheet and presentation formats (`.xls`, `.xlsx`, `.ppt`, `.pptx`, `.ods`,
`.odp`) are not extracted in v0.11.0 and are not searched by default; extractor
support is planned for v1.0. An explicit `-t xlsx` (or another of these types)
searches the raw file bytes as literal text, which rarely matches compressed
content.

### Content cleaning

- **Literal (control characters and whitespace normalized only):** code files plus
  `.txt .md .csv .yaml .yml .log .cfg .conf .ini .sh .bat .cmd .tex .rst .asciidoc`,
  and any other extension selected with `-t` that is not listed below.
  Angle-bracket tokens (`<user@example.com>`, `<kind>-<topic>.md`, `TList<T>`),
  `>` blockquote/redirect lines, divider lines, entities, and tokens like `SHA256`
  are searchable and appear verbatim in excerpts with source `start_line`.
  Excerpts also preserve embedded divider runs (`|-----|-----|`, `snake_____case`),
  including in code files; these are no longer collapsed for display.
- **Markup / email / extracted:**
  `.html .htm .xhtml .shtml .xml .eml .mbox .msg .pdf .doc .docx .odt .rtf`.
  Tags, comments, CDATA, declarations, processing instructions, entities,
  `script`/`style` bodies, `>`-quoted lines, and `-----` dividers are removed.
  Uppercase/digit run-ons are split (`PDF32` -> `PDF 32`) and a space is added
  after commas. Positive terms and `--not` exclusions match this cleaned text
  in every stage.

## How it works

1. **Discover and count** -- walk eligible files under the active scope and ignore
   rules, deduplicate physical candidates, and derive readable-file counts,
   cleaned word lengths, and per-slot document frequencies. No index is persisted.
2. **Extract and clean** -- pure-Go readers provide authoritative searchable text.
   Markup/email/extracted formats are cleaned; literal formats retain source tokens.
3. **Filter and rank** -- workers collect bounded, text-free window summaries.
   Corpus-aware BM25F, proximity, and slot coverage rank primary matches; scarce,
   rare single-slot candidates can then fill the tail. `--strict` requires all slots.
4. **Output** -- extract ranked excerpts for the TUI, JSON, or plain text; path-only
   output uses the same file order without excerpt extraction.

## Architecture

```
garp/
|-- main.go              # Entry point delegating to app.Run
|-- app/
|   |-- cli.go           # Arg parsing, flags, --json/--plain/--pathonly dispatch
|   `-- tui.go           # TUI, progress streaming, results display
|-- search/
|   |-- engine.go        # Search orchestration
|   |-- filter.go        # Candidate discovery, streaming and proximity matching
|   |-- walk.go          # Shared traversal, candidate eligibility, size limits
|   |-- ignore.go        # Repository ignore rules and inherited ignore chains
|   |-- path_policy.go   # Pathscope and user-glob matching, directory pruning
|   |-- root_set.go      # Search-root validation, deduplication, nested-root policy
|   |-- ranking.go       # Proximity-window analysis and file-ranking metadata
|   |-- term_matcher.go  # Query normalization, whole-word, partial and wildcard matching
|   |-- lines.go         # Source line indexing and excerpt line anchors
|   |-- cleaner.go       # Content cleaning, excerpt extraction
|   |-- extractor.go     # Pure-Go binary format extractors
|   `-- scope.go         # --startdir / --pathscope / --max-filesize validation
|-- config/
|   `-- types.go         # Supported types, globs, skip-dir list
|-- eval/                # Retrieval-quality eval harness (own go.mod; see eval/README.md)
|-- bin/                 # Prebuilt binary
|-- Makefile
`-- README.md
```

## Building

```bash
make                  # build to bin/garp-local (static, gitignored)
make install          # build + copy to ~/.local/bin/garp
make release          # rebuild tracked bin/garp + bin/garp.exe (clean tree required)
make dist             # all release assets -> dist/garp-<os>-<arch>[.exe]
make test             # run tests
make fmt              # format
make tidy             # go mod tidy
```

### Windows

A stock Windows install has no `make`, so drive the Go toolchain directly. This
produces `bin\garp-local.exe` (gitignored) using the same release flags as the Makefile (`-trimpath`,
stripped symbol table + DWARF, embedded version):

```powershell
go build -trimpath -ldflags "-s -w -X garp/app.version=0.11.0" -o bin\garp-local.exe .
go test ./...                                              # run tests
go vet ./...                                               # vet
```

The committed `bin/garp` and `bin/garp.exe` are produced only by `make release`
(`CGO_ENABLED=0`, `-trimpath`, stripped symbol table + DWARF, embedded version),
built from a clean tree and committed on their own.

### Dependency pin: temporary `ledongthuc/pdf` fork

Both `go.mod` files temporarily redirect `github.com/ledongthuc/pdf` to the
maintained single-patch fork `github.com/dtwaling/pdf` with an exact
pseudo-version `replace` directive
(`v0.0.0-20261123044633-dc44d404eb72`, one commit above the previously pinned
upstream revision; import paths unchanged). The patch constructs PDF page
content-stream readers lazily, so at most one member reader is retained per
concatenated content stream instead of one per member for the whole
interpretation. One observable contract change: construction errors of later
content-stream members surface only when that member is reached, and an
earlier interpreter exit never triggers them; encountered errors still
propagate and per-page panic recovery is unchanged.

- **Removal condition:** the pin is temporary. Both `replace` directives are
  removed once upstream merges and releases the equivalent fix
  ([ledongthuc/pdf#106](https://github.com/ledongthuc/pdf/pull/106));
  tracking, provenance, and release-by-release review live in
  [dtwaling/garp#102](https://github.com/dtwaling/garp/issues/102).
- **`go install module@version` limitation:** Go rejects version-suffixed
  installs for modules whose `replace` directives change module
  interpretation. This install mode was never documented for garp -- use a
  source build (`make install`), the committed `bin/` binaries, or a GitHub
  Release asset -- so the limitation affects no supported distribution path.

## Development

The [retrieval-quality eval harness](eval/README.md) compares the working tree
against the previous release, BM25, and an rg pipeline on pinned, judged corpora.
Use `make eval-fetch` to fetch corpora and build the previous release,
`make eval` to run the offline regression gate, `make eval-or` for paired
single-wording versus OR-expanded synonym evaluation, `make eval-baseline` to record
a release baseline, and `make eval-test` for harness unit tests. `make eval`
is not part of `make test`; the nested module keeps its dependencies and tests
separate from the root module.

## TUI navigation

| Key | Action |
|-----|--------|
| `enter`, `y`, `space` | Next result |
| `n` | Next (no confirm) |
| `p` | Previous |
| `up` / `down`, `k` / `j` | Scroll excerpt |
| `home` / `end` | First / last result |
| `pgup` / `pgdown` | Scroll 5 lines |
| `q`, `Ctrl+C` | Quit |

## Known limitations

- **High-frequency single-slot precision boundary:** single-slot fallback is
  intentionally withheld when at least five primary proximity matches exist,
  or the only matched word occurs in more than `max(1, floor(0.05*N))` readable
  files. Common words such as `option` or `signature` must not flood the tail
  or outrank genuine co-occurrence. For example, the `option agreement`
  benchmark need targets messages saying `Option 2` and `contract`; nine real
  co-occurrence hits and high `option` frequency both prevent fallback.
- **Lexical overlap:** garp is lexical and index-free (see [What garp is not](#what-garp-is-not)).
  `signature authenticated` cannot find a document that says only `signed`.
  With no matching vocabulary, that document yields zero matches unless
  `--smart-forms`, `--partial`, or explicit OR-groups bridge the lexical gap
  (for example `garp 'signature|signed' authenticated`). Alternatives are
  supplied by the caller; garp does not infer synonyms.
- **Pipe escaping & OR-group bounds:** unescaped `|` in query terms and
  `--not` tokens parses as OR. Quote pipes in shells; search a literal pipe
  with `\|` (`garp 'a\|b'`). OR-expanded queries have the limits documented
  above and fail rather than truncate. `--startdir` and `--pathscope` reject `|`.
- **Deferred v0.11.1 extraction/cleaning limits:** `/LZWDecode`
  PDF streams (#92) and uppercase-digit splitting of markup/extracted mixed
  tokens such as `Olk10SideProps_0001` (#94) remain known limits. Evaluation
  retains the affected misses rather than attributing them to ranking.
- Markdown and other literal formats are not rendered: text inside inline HTML
  and HTML comments in `.md` (for example `<!-- note -->`) is part of the
  searchable text. A search term can match there, and a word you exclude on the
  command line with `--not <word>` excludes the file if it appears there.
- In markup/extracted formats, quoted `>` lines are dropped and `SHA256`-style
  tokens are indexed as `SHA 256`. Search the split form, or a literal format,
  to find them.
- Classification is by extension only. A markup file with a literal extension
  (for example, HTML saved as `.txt`) is searched with its tags.
- `.msg` files are searched only as Outlook OLE/CFB messages. Other files that
  use the extension (for example Tcl msgcat or Firebird message catalogs) are skipped.
- Literal formats keep commas exactly as written, so a query must use the file's
  comma form: `name,email` matches `name,email`, but `name, email` does not.
  Before this release, every non-code document padded commas, so only
  `name, email` matched. Markup/email/extracted formats still add a space after
  commas.
- For content exclusions, a `--not` term that starts or ends with an ASCII
  non-word character (for example `<user@example.com>`, `&amp;`, `>`, or `(note)`)
  excludes a file only where that edge touches an adjacent ASCII letter, digit,
  or `_`: `--not '&amp;'` excludes `x&amp;y` but not `a &amp; b`.
  Exclude a word inside the token instead
  (`--not user@example.com`, `--not amp`). A term that starts with `-` must
  follow `--` (`--not -- -----`).
- Line numbers count `\n` only. In a file with lone-CR (classic Mac) line
  endings, every match reports `start_line` 1.

## FAQ

**How does multi-word matching work?**
Query argument order gives no term priority. A single-term query requires that
term; two-term primary matches require both; three or more distinct terms
require any two or more slots within `--distance` UTF-8 bytes (default 5000,
measured between term starts in cleaned text; non-ASCII characters take 2-4 bytes).

For multi-term queries with fewer than five primary matching files, a single-slot
fallback is eligible only when `N >= 2` and its slot occurs in
`1 <= df <= max(1, floor(0.05*N))` readable files. At most
`min(3, 5 - len(primary))` fallback files are appended, strictly after primary
matches. `N` includes readable empty files but excludes read/extraction errors;
scope and ignore rules therefore affect rarity. `--strict` enforces full
conjunction and disables both partial multi-term admission and single-slot fallback.

**Does `--pathscope` filter results or restrict the walk?**
It filters results and can restrict the walk. Anchored positive prefixes such as
`src/` and `docs/` support direct pruning of unrelated branches. A bare basename
pattern such as `docs` can match at any depth, so it can require full traversal.

**Is it cross-platform?**
Pure Go -- works on Linux, macOS, Windows. The TUI requires an ANSI-compatible terminal.

## Credits

garp was created by [CyphrRiot](https://github.com/CyphrRiot). The core search
engine, TUI, document extraction pipeline, and original architecture are entirely
upstream work. This fork adds:

- `--startdir` -- set an arbitrary base directory for the search walk
- `--pathscope` -- prune the walk to specific subdirectories at directory-entry time
- `--json` -- structured JSON output for MCP tools and agent callers
- `--plain` -- line-oriented plain-text output for shell scripts
- Excerpt quality fixes (pre-highlight interception, float32/identifier preservation,
  ASCII ellipsis joiner)

## License

MIT
