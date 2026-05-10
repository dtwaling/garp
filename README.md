<div align="center">

# :: garp ::

![Version](https://img.shields.io/badge/version-0.7-blue?labelColor=0052cc)
![License](https://img.shields.io/github/license/dtwaling/garp?color=4338ca&labelColor=3730a3)
![Platform](https://img.shields.io/badge/platform-linux-4338ca?logo=linux&logoColor=white&labelColor=3730a3)
![Platform](https://img.shields.io/badge/platform-macos-4338ca?logo=apple&logoColor=white&labelColor=3730a3)

![Last Commit](https://img.shields.io/github/last-commit/dtwaling/garp?color=5b21b6&labelColor=4c1d95)
![Code Size](https://img.shields.io/github/languages/code-size/dtwaling/garp?color=4338ca&labelColor=3730a3)
![Language](https://img.shields.io/badge/language-Go-4338ca?logo=go&logoColor=c7d2fe&labelColor=3730a3)
![Build](https://img.shields.io/badge/build-makefile-4c1d95?labelColor=1e1b4b)

</div>

A high-performance, pure-Go document search tool. garp finds files containing ALL
specified terms within a proximity window and supports common document formats --
text, email, Office, and PDF -- with pure-Go extractors and a clean TUI.

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

# Scope search to specific directories (prunes the walk -- never descends outside scope)
garp pitch frequency --startdir ~/projects/audio2midi --pathscope 'audio2midi/,docs/,tests/' --code

# Machine-readable output for scripts and MCP/agent callers
garp pitch frequency --startdir ~/projects/audio2midi --pathscope 'audio2midi/,docs/' --code --json
```

## Key features

- Pure Go -- zero external tool dependencies
- Multi-word AND logic, unordered, within a proximity window (default 5000 chars)
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
make install-pdfcpu   # recommended -- builds with PDF support, installs to ~/.local/bin/garp
# or: make install    # without PDF tag
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
| `--not <excl...>` | (none) | Tokens after this flag are exclusions. Dot-prefixed = extension exclude (`.pdf`); others = word exclude |
| `--json` | off | Skip TUI; emit structured JSON to stdout. Preferred for MCP/agent callers |
| `--plain` | off | Skip TUI; emit plain line-oriented text to stdout. Useful for shell scripts |
| `--workers N` | 4 | Stage 2 filter worker count |
| `--heavy-concurrency N` | auto | Concurrent heavy (binary) extractions |
| `--file-timeout-binary N` | 1000 | Timeout in ms for binary extraction |
| `--help`, `-h` | | Show help |
| `--version`, `-v` | | Show version |

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
      "excerpts": ["...clean text snippet around matched terms..."]
    }
  ]
}
```

`matches` is always present as a top-level integer so callers can zero-check without
iterating `results`. Errors go to stderr; check exit code before parsing stdout as JSON.

## Supported formats

**Documents (default)**

- Text: `.txt`, `.md`, `.log`, `.rtf`
- Web: `.html`, `.xml`
- Data/Config: `.csv`, `.yaml`, `.yml`, `.cfg`, `.conf`, `.ini`, `.sh`, `.bat`
- Email: `.eml` (MIME parsing), `.mbox` (message collections), `.msg`
- Office: `.pdf` (guardrailed), `.doc`, `.docx`, `.odt`

**Code (with `--code`)**

`.go`, `.py`, `.js`, `.ts`, `.java`, `.cpp`, `.c`, `.rs`, `.rb`, `.cs`, `.swift`,
`.kt`, `.scala`, `.sql`, `.php`, `.json`

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
make install-pdfcpu   # build with PDF support tag (recommended) + install
make test             # run tests
make fmt              # format
make tidy             # go mod tidy
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
Unordered AND within a proximity window. All terms must appear within `--distance`
characters of each other (default 5000) somewhere in the file.

**Does `--pathscope` filter results or restrict the walk?**
It restricts the walk. Directories outside the scope are pruned at directory-entry
time -- garp never descends into them and never visits a single file inside them.
This makes it efficient with large trees: pass `--pathscope 'src/,docs/'` and
`.venv`, `node_modules`, or any other noise tree is skipped entirely.

**Is it cross-platform?**
Pure Go -- works on Linux, macOS, Windows. The TUI requires an ANSI-compatible terminal.

## Troubleshooting

**`pdfcpu: config problem: EOF`**

The pdfcpu config file is corrupted. Fix:

```bash
rm -rf ~/.config/pdfcpu/*
```

Re-run garp; pdfcpu will regenerate a clean default config.

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
