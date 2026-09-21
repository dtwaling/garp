<div align="center">

# :: garp ::

![Version](https://img.shields.io/badge/version-0.8-blue?labelColor=0052cc)
![License](https://img.shields.io/github/license/dtwaling/garp?color=4338ca&labelColor=3730a3)
![Platform](https://img.shields.io/badge/platform-linux-4338ca?logo=linux&logoColor=white&labelColor=3730a3)
![Platform](https://img.shields.io/badge/platform-macos-4338ca?logo=apple&logoColor=white&labelColor=3730a3)
![Platform](https://img.shields.io/badge/platform-windows-4338ca?logo=windows&logoColor=white&labelColor=3730a3)

![Last Commit](https://img.shields.io/github/last-commit/dtwaling/garp?color=5b21b6&labelColor=4c1d95)
![Code Size](https://img.shields.io/github/languages/code-size/dtwaling/garp?color=4338ca&labelColor=3730a3)
![Language](https://img.shields.io/badge/language-Go-4338ca?logo=go&logoColor=c7d2fe&labelColor=3730a3)
![Build](https://img.shields.io/badge/build-makefile-4c1d95?labelColor=1e1b4b)

</div>

An agent-friendly, high-performance, pure-Go document search tool. garp finds files containing ALL
specified terms within a proximity window and supports common document formats --
text, email, Office, and PDF -- with pure-Go extractors and a clean JSON output.

No relation to the [John Irving novel](https://en.wikipedia.org/wiki/The_World_According_to_Garp).
More like a mispronounced "grep" that's easy to remember.

Forked from [CyphrRiot/garp](https://github.com/CyphrRiot/garp). Original concept,
core search engine, TUI, and document extraction are the work of
[CyphrRiot](https://github.com/CyphrRiot).

![garp TUI](garp.png)

## Quick start

```bash
garp contract payment agreement
garp contract payment agreement --distance 200 --not .pdf
garp mutex changed --code
garp bank wire update --not .txt test
garp approval crypto gemini --smart-forms
garp report earnings --only pdf

# Partial word matching
garp deploy service --partial             # boundary-aware prefix: deployment, deploy_service
garp deploy* service                      # inline glob: prefix on deploy only; service stays whole-word
garp deploy service --partial=contains    # raw substring: redeploy, autodeploy

# Three or more terms use Strategy A ranked matching by default
garp pitch onset midi --distance 500  # ranked by term-length rarity score
garp pitch onset midi --strict        # enforce all terms with strict AND

# Scope search to specific directories (prunes the walk -- never descends outside scope)
garp pitch frequency --startdir ~/projects/audio2midi --pathscope 'audio2midi/,docs/,tests/' --code

# Machine-readable output for scripts and MCP/agent callers
garp pitch frequency --startdir ~/projects/audio2midi --pathscope 'audio2midi/,docs/' --code --json
```

## Key features

- Pure Go -- zero external tool dependencies
- Unordered multi-word proximity matching within a window (default 5000 chars)
- Strategy A ranked matching for three or more terms: finds the best proximity
  cluster containing the first query term and at least one secondary term
- Term-length rarity weighting: chunks matching longer domain terms rank above
  chunks that match only short, common words
- One- and two-term searches remain strict AND; `--strict` enforces strict AND
  for searches with three or more terms
- Directory-scoped search: `--startdir` sets the root, `--pathscope` prunes the walk
- Machine-readable output: `--json` (structured envelope) and `--plain` (line-oriented)
  designed for scripting and MCP tool callers
- Smart content cleaning: strips HTML/CSS/JS, email headers, control chars
- Binary document support: .eml, .mbox, .pdf, .doc/.docx/.odt, .rtf, .msg
- Code file search with `--code`
- Advanced exclusion with `--not` for extensions and words
- Beautiful TUI with live progress, paging, and highlighted excerpts
- Safe large-file handling with size-aware reads

## Install

**Option 1: Build from source**

```bash
git clone https://github.com/dtwaling/garp
cd garp
make install          # builds with pure-Go PDF support, installs to ~/.local/bin/garp
```

Ensure `~/.local/bin` is on your `PATH`.

**Option 2: Copy the prebuilt binary**

The latest binary lives at `bin/garp`. Copy it to any directory on your `PATH`:

```bash
cp bin/garp ~/.local/bin/garp
chmod +x ~/.local/bin/garp
```

## Flags

```
garp <word1> <word2> ... [flags] [--not <excl1> <excl2> ...]
```

| Flag | Default | Description |
|------|---------|-------------|
| `--code` | off | Include code files (.go, .py, .js, .ts, ...) |
| `--distance N` | 5000 | Proximity window in characters |
| `--startdir <path>` | cwd | Base directory to search from |
| `--pathscope <patterns>` | (all) | Comma-separated directory patterns. Prunes the walk -- directories outside scope are never descended into. Trailing slash optional: `audio2midi/` and `audio2midi` both work. |
| `--only <type>` | (all) | Search only one file type, e.g. `--only pdf` |
| `--smart-forms` | off | Match word forms (plurals, -ing, -ed, -tion) |
| `--partial[=MODE]` | off | Enable partial word matching. Bare `--partial` defaults to `prefix`; modes: `prefix`, `contains`, `off` |
| `--not <excl...>` | (none) | Tokens after this flag are exclusions. Dot-prefixed = extension exclude (`.pdf`); others = word exclude |
| `--strict` | off | Require every search term in the proximity window, including searches with three or more terms |
| `--json` | off | Skip TUI; emit structured JSON to stdout. Preferred for MCP/agent callers |
| `--plain` | off | Skip TUI; emit plain line-oriented text to stdout. Useful for shell scripts |
| `--max-excerpts N` | 1 | Maximum excerpts per matching file (maximum 50) |
| `--workers N` | 4 | Stage 2 filter worker count |
| `--heavy-concurrency N` | auto | Concurrent heavy (binary) extractions |
| `--file-timeout-binary N` | 1000 | Timeout in ms for binary extraction |
| `--help`, `-h` | | Show help |
| `--version`, `-v` | | Show version |

### Partial word matching and inline globs

By default, garp uses whole-word matching with plural support. `--partial` enables
boundary-aware `prefix` matching, so `deploy` matches `deployment`, `deployed`,
and `deploy_service` without matching internal substrings such as `redeploy`.
`--partial=prefix` is equivalent to bare `--partial`.

Use `--partial=contains` for raw substring matching when internal matches are
useful: `deploy` also matches `redeploy` and `autodeploy`. Contains mode is broad,
so prefer prefix mode for technical terms and code identifiers when possible.

Append `*` to an individual query term for an inline glob. For example,
`garp deploy* service` applies prefix matching to `deploy` while `service` remains
whole-word matching, without enabling a global partial mode. This is useful when
only one term needs expansion.

Prefix matching supports standard punctuation plus `snake_case` and `kebab-case`
identifier boundaries. Go's RE2 regex engine does not support zero-width
lookbehinds, so camelCase transitions without separators, such as `autoDeploy`,
are not recognized as prefix boundaries. Supporting those transitions requires a
future custom scanner.

Terms supplied after `--not` are always whole-word exclusions, regardless of
`--partial` or inline globs. This prevents a broad partial query from accidentally
filtering unrelated results.

### --json output shape

```json
{
  "query": {
    "terms": ["pitch", "frequency"],
    "start_dir": "/path/to/dir",
    "path_scope": ["audio2midi/", "docs/"],
    "include_code": true
  },
  "matches": 2,
  "results": [
    {
      "file": "/path/to/audio2midi/dsp.py",
      "size_bytes": 15649,
      "score": 14,
      "term_count": 2,
      "matched_terms": ["pitch", "frequency"],
      "excerpts": [
        {
          "text": "...clean text snippet around matched terms...",
          "start_line": 42
        }
      ]
    }
  ]
}
```

Each result includes ranked-match metadata. `score` is the sum of the lengths
of the distinct matched query terms, `term_count` is their count, and
`matched_terms` lists them in query order. Results sort by score, then term
count, then tighter match span, then file path.

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
Each plain result begins with `MATCH X/Y [Score: S (T/N terms)]`, where `X/Y`
is the result position and total, `S` is the score, `T` is the matched-term
count, and `N` is the total query-term count.

`matches` is always present as a top-level integer so callers can zero-check without
iterating `results`. Errors go to stderr; check exit code before parsing stdout as JSON.

### Bounded-overlap excerpts

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

`.go`, `.py`, `.js`, `.ts`, `.java`, `.cpp`, `.c`, `.rs`, `.rb`, `.cs`, `.swift`,
`.kt`, `.scala`, `.sql`, `.php`, `.phtml`, `.json`

Delphi / Object Pascal: `.pas`, `.dpr`, `.dpk`, `.inc`, `.dfm`, `.fmx`, `.dproj`, `.groupproj`

Container build files (matched by name, since they're usually extensionless): `Dockerfile`,
`Dockerfile.*` (e.g. `Dockerfile.dev`), `Containerfile`, and `*.dockerfile`. `--only dockerfile`
restricts to just this family.

> Code files use a minimal, code-safe content cleaner: angle brackets, operators, and tokens
> like `<?php`, `$obj->prop`, and `TList<T>` are preserved (the document cleaner would strip
> them as markup). This keeps both matching and excerpts faithful to the source.

**Binary extraction (pure Go)**

| Format | Library |
|--------|---------|
| EML | `enmime` |
| MBOX | `emersion/go-mbox` |
| PDF | `ledongthuc/pdf` |
| DOCX/ODT | `archive/zip` + XML |
| RTF | regex/control-word stripping |
| MSG | raw content fallback |

> PDF note: strict guardrails apply -- concurrency=2, 250ms per-PDF, max 200 pages,
> max 128 KiB/page, max 100 PDFs per search.

## How it works

1. **Discovery** -- walks the directory tree (respecting `--startdir` and `--pathscope`
   to prune irrelevant subtrees at directory-entry time), filtering by extension.
2. **Filter** -- parallel workers check each candidate for all search terms within
   the proximity window.
3. **Extract** -- pure-Go extractors pull text from binary formats.
4. **Clean** -- strips markup, control chars, CSS/JS blocks, email headers.
5. **Output** -- TUI with highlighted excerpts, or `--json`/`--plain` for programmatic use.

## Architecture

```
garp/
├── main.go              # Entry point
├── app/
│   ├── cli.go           # Arg parsing, flags, --json/--plain dispatch
│   └── tui.go           # TUI, progress streaming, results display
├── search/
│   ├── engine.go        # Search orchestration
│   ├── filter.go        # File walking, scope pruning, matching
│   ├── cleaner.go       # Content cleaning, excerpt extraction
│   ├── extractor.go     # Pure-Go binary format extractors
│   └── scope.go         # --startdir / --pathscope validation
├── config/
│   └── types.go         # Supported types, globs, skip-dir list
├── bin/                 # Prebuilt binary
├── Makefile
└── README.md
```

## Building

```bash
make                  # build to bin/garp
make install          # build + copy to ~/.local/bin/garp
make test             # run tests
make fmt              # format
make tidy             # go mod tidy
```

### Windows

A stock Windows install has no `make`, so drive the Go toolchain directly. This
produces `bin\garp.exe` using the same release flags as the Makefile (`-trimpath`,
stripped symbol table + DWARF, embedded version):

```powershell
go build -trimpath -ldflags "-s -w -X garp/app.version=0.8" -o bin\garp.exe .
go test ./...                                              # run tests
go vet ./...                                               # vet
```

The `.exe` is git-ignored -- it's a local artifact, not committed. The tracked
`bin/garp` is the Linux build. To cross-compile a Windows binary from Linux/macOS:

```bash
GOOS=windows GOARCH=amd64 go build -trimpath \
  -ldflags "-s -w -X garp/app.version=0.8" -o bin/garp.exe .
```

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

## FAQ

**How does multi-word matching work?**
One- and two-term searches use unordered strict AND within a proximity window:
all terms must appear within `--distance` characters of each other (default
5000) somewhere in the file. Searches with three or more terms use Strategy A
ranked matching by default: a result needs the first query term plus at least
one secondary term, and the best clusters rank by term-length rarity score.
Pass `--strict` to require all terms for any search size.

**Does `--pathscope` filter results or restrict the walk?**
It restricts the walk. Directories outside the scope are pruned at directory-entry
time -- garp never descends into them and never visits a single file inside them.
This makes it efficient with large trees: pass `--pathscope 'src/,docs/'` and
`.venv`, `node_modules`, or any other noise tree is skipped entirely.

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
