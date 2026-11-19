# Retrieval-quality evaluation

The nested `garp/eval` module compares four systems on frozen, judged docs and
code corpora. It does not change garp's search behavior or root dependencies.
The baseline of record is [v0.10.0.json](results/v0.10.0.json), with human-readable
metrics and informational timing in [v0.10.0.md](results/v0.10.0.md).

## How to run

Run these targets from the repository root on Linux or macOS, with Go (see
`go.mod`), POSIX make, git, tar, and ripgrep (`rg`) on PATH:

```sh
make eval-fetch     # Network allowed: pinned corpora, Go modules, previous binary
make eval           # Offline: build candidate and harness, score and gate
make eval-or        # Offline: paired original wording / synonym OR expansion
make eval-test      # Harness unit tests, no corpus required
make eval-baseline  # Record the previous release as candidate, without gating
```

`EVAL_CACHE` defaults to `$HOME/.cache/garp-eval`; use the same override on every
corpus target, for example `make eval EVAL_CACHE="$TMPDIR/garp-eval"`.
Keep the cache outside the repository and do not add files inside its `docs/`
or `code/` trees: the runner verifies the manifest universe, including hashes,
before searching. `make clean` does not remove this cache.
`EVAL_BASELINE_REF` defaults to `v0.10.0` and selects the archived previous
binary and `results/<ref>.json`. Fetch before evaluating a different ref.

`make eval-fetch` downloads both modules' dependencies and the archived
release's dependencies, verifies cached corpus files, and builds the previous
release from `git archive` with `-buildvcs=false`. Subsequent `eval` and
`eval-baseline` builds use `GOPROXY=off`; the tokenizer vocabulary is embedded.
`make test` remains the root module's test suite; it does not run this nested
module or any corpus evaluation. `make eval-test` may need module downloads on
its first run.

After an interrupted fetch (including SIGINT or SIGKILL), re-run
`make eval-fetch` with the same `EVAL_CACHE` before evaluating. It removes
leftover `.fetch-<digits>` regular files from the corpus and download trees,
including when all manifest files are already present. Other extra files are
not removed and still fail the runner's universe check. Run only one fetch at
a time per cache, and do not evaluate that cache while a fetch is in progress.

The built `bin/eval-harness` returns **0** for a pass, **1** for a quality
regression, and **2** for a harness/setup error (missing rg, bad query, corrupt
corpus, stale/missing baseline, timeout, etc.). On recipe failure, make prints
`eval: harness exit <status> (1 = quality regression, 2 = harness or setup error)`.
GNU make itself returns **2 for either failure**, not the harness's 1/2;
success returns 0. Build failures can precede the harness status line. The
built binary, rather than `go run`, preserves exact harness exit codes.

The gate requires non-decreasing candidate R@1, R@5, R@10, MRR@10, and hit@N
**separately for each corpus** (tolerance `1e-9`). Both sides use persisted
six-decimal metrics. Token costs, timing, unjudged counts, other systems, and
individual query rank changes are informational. A passing run prints
`regressions vs results/v0.10.0.json: none` after the two corpus reports.
Outputs are `results/latest.json` and `results/latest.md`, both gitignored.
JSON has stable ordering, six-decimal metrics, no absolute cache paths, and no
timing; markdown includes machine-dependent timing and unjudged paths.

## How to add a query

1. Fetch the pinned corpus. Independently read a source file as described below;
   write the information `need`, then gold, then query terms, in that order.
2. Add an entry to `queries/docs.json` or `queries/code.json`, with a unique `id`,
   `root`, `kind`, `need`, `args`, `gold`, and `judged_nonrelevant`. Docs use root
   `"."`; code uses `"garp"` or `"ripgrep"`. Gold and nonrelevant paths are
   slash-separated and relative to that root, and must belong to the manifest.
   Gold `lines: [a,b]` is an inclusive, 1-based LF-counted range for text/code;
   omit it for PDF, DOC, DOCX, RTF, EML, and MSG.
3. Each alternative matches `[A-Za-z0-9_.-]+` with an optional final `*`.
   The harness accepts `a|b|c` slots for synonym overlays, with at most 16
   alternatives per group, 32 slots and 64 alternatives total in OR-expanded
   args (counted before deduplication). Empty alternatives are invalid.
   Frozen base queries remain single-wording: put expansions in the paired
   overlay described below, not in `queries/docs.json` or `queries/code.json`.
   Query flags allowed
   are `--distance N`, `--strict`, `--partial`, `--partial=prefix|contains`,
   `--smart-forms`, and `-m N`. Do not put harness-owned flags (`--json`,
   `--startdir`, `--no-ignore`, `--file-timeout-binary`, `--code`, or
   `--max-results`) in `args`.
4. Build with `make eval`. After a query change, its stale-baseline error is
   expected; it still builds the binaries. From `eval/`, generate a blind pool
   using the same absolute cache path (replace the example IDs as needed):

   ```sh
   ../bin/eval-harness -cache "$HOME/.cache/garp-eval" \
     -candidate ../bin/garp-eval \
     -previous "$HOME/.cache/garp-eval/bin/garp-v0.10.0" \
     -pool docs-001,docs-002
   ```

   Pool mode prints ID, need, args, and the path-sorted union of all four
   systems' top 10. It hides system attribution, scores, and gold; it writes
   no result files. Read each pooled source independently, add relevant files
   to gold, and put every remaining pooled file in `judged_nonrelevant`.
   Retain known-item gold even when none of the systems retrieves it.
5. Validate all queries and score without a gate, from `eval/`:

   ```sh
   ../bin/eval-harness -cache "$HOME/.cache/garp-eval" \
     -candidate ../bin/garp-eval \
     -previous "$HOME/.cache/garp-eval/bin/garp-v0.10.0" \
     -previous-ref v0.10.0 -out "$TMPDIR/eval-dry" -no-gate
   ```

   Resolve validation errors and judge any new unjudged results. Do not change
   labels merely to improve scores. Get an independent blind second judgment
   before accepting the changed key.
6. Re-record the release baseline with `make eval-baseline`, then run
   `make eval` and `make eval-test`. Review and commit the query change together
   with the regenerated baseline pair, never hand-editing generated results.

Minimums are 40 queries per corpus, including 12 proximity, 10 synonym, and
10 single; every required extracted format must be gold for at least four docs
queries; code must have at least 15 garp-rooted and 20 ripgrep-rooted queries.
The committed key has **42 docs and 42 code queries**, with `token_budget: 2000`
in both files.

## How gold answers were established

1. **Need first.** The author opens a corpus file with an independent reader,
   not garp extraction, picks a passage, and writes a one-sentence agent need.
2. **Gold from the source.** That file is initial gold; text/code ranges count
   raw `\n` bytes, independently of garp's excerpt selection.
3. **Terms last.** Write args from the need, not by copying the passage.
   Synonym queries deliberately use wording the gold file lacks.
4. **Pooled, blind completion.** Judge the path-sorted union of all four
   systems' top 10 against the need, without scores or system names. Read the
   original files, not garp/BM25 extraction or pool excerpts. Additional
   relevant files become gold; the others become `judged_nonrelevant`.
5. **Unjudged tracking.** A new ranker's top-10 files outside those two sets
   are reported and count as nonrelevant until judged. Judge them before
   trusting comparisons. Extraction/retrieval failures remain honest misses.
6. **Mechanical kind checks.** BM25 tokens check honesty, not relevance:
   a raw or extraction-cleaned term counts as present, using the gold file's
   own cleaner. Accepting either form preserves case-insensitive matches even
   when cleaning depends on letter case. `single`
   has one term, present in every gold file that has extracted tokens; zero-token
   gold is exempt so extraction failures remain honest misses. `proximity` has
   at least two terms, all present in every gold file; `synonym` has at least two,
   at least one common query term absent from every gold file's whole-file
   token set, not merely absent from its cited line range. Queries using `--partial`,
   `--partial=prefix`, `--partial=contains`, or `--smart-forms` skip presence
   checks for all three kinds because exact-token presence cannot establish
   broadened matches; term-count requirements still apply. These checks are
   not substitutes for reading the source. Kind checks run on the frozen
   single-wording queries **before** applying the OR overlay; base queries
   containing OR-groups are rejected. The expanded wording deliberately
   bridges missing vocabulary and is not reclassified by these checks.
7. **Independent second judge.** An analyst reviews a gold-stripped packet
   and blind pool, deriving answers before opening the author's labels. The
   minimum sample is six docs (one per required extracted format) and six
   code (three per root, including a synonym). Resolve every differing gold
   file or non-overlapping range before acceptance. For this key the reviewer
   checked 13 queries (seven docs, six code) and agreed with the final labels.
   This replaces the original design's per-entry human sign-off.
8. **Frozen key.** Query/gold or manifest changes alter recorded SHA-256
   hashes; the gate refuses with `baseline stale: run make eval-baseline`
   until the previous release is re-scored against the changed key.
   The gate also refuses baselines with missing or different previous release ref/version metadata.
   Missing or different tokenizer identity or schema version likewise requires re-running `make eval-baseline`.

Independent readers used for authoring and confirmed by the second judge:

| Format | Reader and version used | Method |
|---|---|---|
| PDF | Poppler `pdftotext` 24.02.0 | `pdftotext -layout <file> <scratch-text>` |
| DOC, DOCX, RTF | LibreOffice 24.2.7.2 420(Build:2) | Headless `--convert-to txt:Text --outdir <scratch-dir>` |
| MSG | Python 3.12.3 with `olefile` 0.46 | Read subject `__substg1.0_0037001F/1E` and body `__substg1.0_1000001F/1E` OLE streams |
| EML | Python 3.12.3 stdlib `email` | `BytesParser(policy=policy.default)` and `get_body` |
| TXT, HTML, XML, CSV, code | Raw-byte file reading | Read original bytes; line anchors count LF, without extraction |

Readers are authoring/judging tools, **not harness dependencies**. Probe every
reader on a cached file before labeling. If missing, a Python dependency may
be installed in a scratch virtual environment; never alter host prefixes.
If no independent reader works for a required format, stop that authoring
work rather than falling back to garp, BM25, or pool excerpts. MuPDF `mutool`
is an allowed PDF fallback, but was not needed for this key.

### Approved authoring divergences

The original design/plan assigned `search/ignore.go` to all three imported
spot checks. Independent source inspection disproved that assignment, so the
approved override keeps their exact terms/distances but derives gold per need:
`gitignore ignore nested --distance 300` targets `search/ignore.go`,
`timeout binary extract --distance 300` targets `search/engine.go`, and
`prune walk gitignore --distance 400` targets `search/walk.go` plus
`config/types.go`. Their kinds are synonym, proximity, and synonym,
respectively; `--code` remains harness-owned.

During pool completion, `code-r-006` was re-authored from its unchanged need
as `memory maps --distance 400` instead of platform-specific `macos`; all
eight relevant gold entries were retained and the pool regenerated/rejudged.
`docs-027` changed a subject-only `RAID1` draft to the need's body-grounded
`dump`/`tar` terms, with the same gold and proximity kind. These corrections
were independently reviewed; other extraction/retrieval misses were retained.
After the #85 key was frozen, `docs-026` dropped `mail/sa-00008.eml`, a
follow-up reply that neither contains `carbonara` nor gives the explanation
the need asks for -- `mail/sa-00006.eml` remains sole gold, and the baseline
was re-recorded.

### Corpus-aware ranking pool completion and frozen-key limitation

For #86, `eval-harness -pool` produced a path-sorted, system-blind pool for
ten affected queries. Independent readers and a second judge reconciled the
117 query/file pairs. The approved update added **13 `judged_nonrelevant`
entries (4 docs, 9 code)** with **zero gold changes**; needs, args, kinds,
roots, budgets, manifests, and existing judgments remained frozen. The
v0.10.0 baseline was regenerated to refresh query hashes, with unchanged
release retrieval metrics. The selected candidate has zero unjudged top-10
files on both corpora; that is key coverage, not proof of semantic unanimity.

**ASSUMED -- human PR-gate confirmation remains open:** `code-r-015`
(`alias include type`) retains `crates/core/flags/defs.rs` as nonrelevant in
the frozen key even though its include-directive documentation answers the
need. That whole file contains every query term, whereas synonym-kind gold
must share a missing term. This is a pre-existing semantic/key exception,
explicitly retained under the approved frozen-key constraint, not a newly
established content judgment or a parser/ranking fix. The mechanical schema
does not justify calling relevant content nonrelevant. Comparisons below
measure the retained key; query/schema correction requires separate approval.
Keep this assumption visible at the human PR gate rather than silently closing it.

### Paired synonym OR evaluation and known key gaps

`make eval-or` runs the same candidate binary twice: original wording as
`previous`, and `queries/synonym_or.json` expansions as `candidate`. BM25 and
ripgrep also use the expansions. The frozen base queries, gold and v0.10.0
baseline are unchanged. `-synonym-or` is incompatible with `-baseline`; its
own paired gate replaces the normal baseline requirement. `-no-gate` records
results without applying either gate.

Outputs are gitignored `results/latest.or.json` and `results/latest.or.md`.
The table below reproduces persisted six-decimal `by_kind["synonym"]`
metrics: **12 docs** and **14 code** queries eligible for strict lift,
excluding registered
`code-g-011`. Here `previous` means original wording on the **same current
binary**, not v0.10.0. Single/proximity controls have zero delta, and all four
systems have `unjudged10 == 0` on both corpora.

| Corpus | Wording/system | R@1 | R@5 | R@10 | MRR@10 | hit@N |
|---|---|---|---|---|---|---|
| docs | previous (single-wording) | 0.416667 | 0.750000 | 0.750000 | 0.541667 | 0.750000 |
| docs | candidate (OR-expanded) | 0.833333 | 0.916667 | 0.916667 | 0.875000 | 0.916667 |
| code | previous (single-wording) | 0.357143 | 0.500000 | 0.642857 | 0.438776 | 0.500000 |
| code | candidate (OR-expanded) | 0.285714 | 0.714286 | 0.892857 | 0.476786 | 0.535714 |

Both eligible cohorts improve R@10 and hit@N, under the bounded precision
exception below. This is **not** a claim of aggregate hit@N non-regression:
the separately reported key-gap query has frozen-key hit@N `1.000000 ->
0.000000`, and aggregate code hit@N is `0.572421 -> 0.560516`. The register
does not award new gold credit. To check determinism, save `latest.or.json`,
run `make eval-or` again and compare bytes; do not compare Markdown timing.

The overlay's 27 expansions (12 docs, 15 code) were locked from needs and
original args only, before inspecting gold or corpus content. The pre-judgment
expansion lock is SHA-256
`dc20fab4c224de445621f2c6b1641eba345ba8cbc2ec5a9dd146832342352b57`.
Only sorted/deduplicated `extra_judged_nonrelevant` labels were added afterward;
args, ordering and other fields remain unchanged. The blind pool introduced
142 query/path pairs: 141 retained negatives and one confirmed key gap.

**ASSUMED -- judgment-quality caveat requiring human PR-gate review:**
129 agreeing negative labels were retained under explicit bounded-scope
approval, not re-certified as complete close reads. Their earlier independent
passes used shallow/head/keyword-context reading and generic rationales.
The remaining 13 uncertain/disputed pairs received bounded, source-specific
primary reads and a fresh isolated third judge: 12 negatives and one relevant
path, with zero unresolved disagreements. Zero `unjudged10` therefore means
coverage under this authorized disposition, not complete independent
source-grounded certification of all 142 pairs. Do not silently close this
limitation or claim a uniformly close-read two-judge pool.

The required sibling `queries/synonym_or.key_gaps.json` records `id`, `path`
(query-root-relative), `need`, and `evidence`. Custom overlays likewise require
`<overlay stem>.key_gaps.json` (use `[]` when empty). The harness validates the
register against expanded synonym queries, needs, manifest paths and existing
judgments, and records both overlay and register SHA-256s in result metadata.
`code-g-011` / `app/tui.go` is confirmed relevant: pinned lines 485-497 and
826-831 sanitize untrusted excerpts before display. It remains absent from
both frozen gold and nonrelevant labels. Registered paths count as adjudicated
for coverage only, never as new gold credit or as nonrelevant.

Query IDs in the register are excluded from `by_kind.synonym` and reported
separately under `by_kind.known_key_gap` and a Markdown per-query table.
Per-query rows retain their original kind; corpus aggregates still include
all queries. Their recall/hit metrics use the incomplete frozen gold and must
not be interpreted as corrected relevance estimates. Only strict synonym
lift excludes the registered IDs; single/proximity equality and aggregate
non-regression checks remain in force, with the bounded exception below.
The gate requires zero unjudged top-10 paths in all four systems and strict
R@10 and hit@N lift in both eligible synonym cohorts. JSON is deterministic;
Markdown timing remains informational.

**ASSUMED -- authorized bounded top-rank precision tradeoff:**
`code-g-008`'s frozen gold `search/engine.go` moves from rank 1 to rank 2:
an OR-expanded third slot admits a judged-nonrelevant competitor above it.
Only this exact single-gold rank transition permits a code synonym R@1 drop
of at most `0.10` and aggregate code R@1 drop of at most `0.03` (with `1e-9`
comparison tolerance), and only while strict R@10/hit@N lift holds on both
eligible cohorts. The observed code R@1 changes are `0.357143 -> 0.285714`
(synonym) and `0.534722 -> 0.510913` (aggregate). That query's reciprocal rank
necessarily falls from 1 to 0.5; synonym and aggregate R@5/MRR@10 must still
not regress. All other code queries and every docs query retain per-query
R@1/R@5/MRR@10 non-regression at `candidate >= previous - 1e-9`, preventing
cohort gains from hiding another query's loss. No expansions or frozen keys
were changed to recover rank.

The bounded precision tradeoff, confirmed key gap, and 129-label caveat
remain explicit `ASSUMED` items for human review in the eventual PR; the key
gap and judgment-quality confirmation remain open, not grounds to tune
expansions after seeing metrics.

## Metric definitions

Rankings are deduplicated in output order. R@k and MRR@10 use distinct gold
files; hit@N uses gold items (including separate ranges in the same file).
Corpus and per-kind scores are macro means over their queries.

| Metric | Definition |
|---|---|
| R@1, R@5, R@10 | Fraction of distinct gold files in the first k results |
| MRR@10 | Reciprocal rank of the first gold file in the top 10, or zero |
| hit@N | Fraction of gold items evidenced by complete garp results within the first N output tokens |
| tokens | Full normalized stdout token count, mean and nearest-rank p50; garp and rg only |
| unjudged@10 | Count of top-10 files in neither gold nor `judged_nonrelevant`, macro-averaged |
| wall time | One timed run per system/query, p50/p95/total; BM25 index build reported separately |

Tokens use `o200k_base` via `github.com/tiktoken-go/tokenizer` v0.10.0. For
hit@N, walk the top-level JSON `results` array with `json.Decoder`, recording
`InputOffset()` after each complete element. Re-tokenize the stdout prefix
through that offset; the element qualifies if its count is at most N (2000).
A line-range gold item is hit only when a qualifying result for its file has
an excerpt `start_line` inside the inclusive range. Without `lines`, a
qualifying file result suffices. Each gold item contributes at most once.

**Intentional per-range tradeoff:** garp does not expose excerpt end lines, so
hit@N measures gold items/ranges, not individual gold lines or range overlap.
BM25 and rg return paths only, so hit@N is n/a for them; BM25 tokens are also
n/a. Token counts are approximate costs for models using other tokenizers.

### Root-path normalization and plan divergence

Each query uses `rootAbs = filepath.Abs(<cache>/<corpus>/<root>)`, retaining
the logical path without resolving symlinks. garp emits absolute paths even
for a relative `--startdir`. Before decoding, token counting, or hit@N:

1. Remove every occurrence of the JSON-encoded `rootAbs + "/"` prefix
   (without its surrounding JSON quotes) from stdout bytes.
2. Replace every **complete JSON string equal to `rootAbs`** with `"."`,
   including garp's echoed `query.start_dir`. This is token-aware, not a
   substring replacement inside quoted excerpt text.

Other whitespace/order is preserved; JSON is not decoded and re-encoded.
The first replacement applies throughout stdout, including excerpts if they
contain that prefix. rg instead strips `rootAbs + "/"` from each output path;
its tokens count the final newline-joined path list.

This intentionally measures **normalized stdout**, not literal process
stdout, so cache-path length cannot change token costs or gated hit@N.
Step 2 extends settled decision 4 of the implementation plan, which described
only removing `rootAbs/`: leaving the echoed root would retain machine-local
path length in token counts. It is an explicit divergence from that plan text.

In ordinary evaluation, candidate and previous run identical args; in paired
OR evaluation, only candidate uses expanded args. Both add `--json --startdir <root>`
`--no-ignore --file-timeout-binary 60000` and, for code, `--code`. Any timeout
is a harness error; a nonzero garp process exit is recorded as an error and an
empty ranking (a scored miss). BM25 uses garp extraction/cleaning, Unicode
lowercase letter/digit tokens, k1=1.2, b=0.75, no stemming or stopwords, and
path tie-breaking for its positive-score top 50. For OR queries, BM25 flattens
the union of alternative tokens (deduplicated after trailing-`*` vocabulary
expansion); it does not enforce slot conjunction. Ordinary queries retain
their original token multiplicity. rg is an AND-across-slots pipeline with
OR-within-slot `-e` patterns, using case-insensitive whole-word literal terms
and default binary handling,
`--no-config --no-ignore --hidden --sort path`; trailing `*` uses prefix
matching. BM25 and rg ignore query flags.

### Readable-corpus denominator and BM25 returned counts

Live corpus-aware garp counts only successfully read/extracted eligible files,
including readable files with zero cleaned words, after physical deduplication.
For the pinned docs corpus, **`N_garp = 409`**, excluding 21 open-error PDFs
tracked in #90. `internal/systems/bm25.go` retains failed extractions as empty
manifest documents, so **`N_bm25 = 430`**. Both use the same 409 successfully
extracted texts, but the IDF and average-length denominators differ. Live garp
must not let unreadable files dilute average length or inflate rarity thresholds.
The code corpus has no extraction errors (67 garp files, 139 ripgrep files).

The current [v0.10.0 baseline](results/v0.10.0.json) per-query
`per_system.bm25.returned` means (**Mean Ret**, not output tokens) are
**15.9 files/query on docs (`666 / 42`)** and
**27.8 files/query on code (`1166 / 42`)**. Values **36.1 / 41.2** in earlier
#85/#86 design notes were historical pre-freeze #85 draft counts, not the
current frozen-key baseline. BM25 returns at most 50 positive-score paths and
does not render excerpt payloads; its hit@N and token costs remain n/a.

The selected corpus-aware candidate's MRR@10 is 0.785714 on docs and 0.663520
on code, versus previous-release 0.591270 / 0.504195 and BM25
0.754195 / 0.635185. These small-corpus results retain the frozen-key
exception above, the lexical/precision boundaries, and the deferred v0.11.1
extractor/cleaner misses (#90, #91, #92, #94); see the repository
[known limitations](../README.md#known-limitations). They do not measure
semantic retrieval or eliminate extraction limitations.

## How to re-baseline at a release

The baseline's candidate and previous columns both run the selected release
binary, not today's working tree. Never edit generated JSON or markdown or
relax the gate to conceal a regression. Accept intentional ranking tradeoffs
explicitly at release time:

```sh
make eval-fetch EVAL_BASELINE_REF=vX.Y.Z
make eval-baseline EVAL_BASELINE_REF=vX.Y.Z
make eval EVAL_BASELINE_REF=vX.Y.Z
```

The release ref must exist locally. Review and commit the generated
`results/vX.Y.Z.json` and `.md` together, and consistently select that ref in
future runs. Changing the ref selects the comparison release, not a new
code corpus: the manifest still pins garp's source corpus to v0.10.0.
Changing queries/gold/manifests similarly requires re-running the selected
release on that new key. Hashes prevent a key change from silently hiding
inside a ranking change. For determinism, save `latest.json`, run `make eval`
again without changes, and compare the bytes; do not compare markdown timing.

## Corpus provenance and selection rule

The committed `manifests/{docs,code}.json` are the source of truth, with source
identities, archive pins, paths, sizes, and per-file SHA-256. Original document
bytes are fetched, never converted or committed. The content-blind selector
in `cmd/fetch -select docs|code` regenerates manifests; it does not inspect
search scores or extraction success.

| Source | Pin and license/provenance | Selection |
|---|---|---|
| [GovDocs1](https://digitalcorpora.org/corpora/files/) | Digital Corpora's freely redistributable .gov documents; S3 `threads/thread0.zip`, `by_type/docx.zip`, `by_type/rtf.zip`, pinned sizes/ETags in manifest | Members in name order, at most 512 KiB each: first 160 PDF, 40 DOC, 40 TXT, 25 HTML, 10 XML, 10 CSV from thread0; first 30 DOCX and 30 RTF from by-type archives |
| [SpamAssassin easy_ham](https://spamassassin.apache.org/old/publiccorpus/) | `20030228_easy_ham.tar.bz2`, SHA-256 pinned; public testing corpus, copyright remains with senders | First 60 eligible lowest-numbered messages, stored byte-unchanged as `mail/sa-<nnnnn>.eml`; fetched, not redistributed |
| [Apache POI](https://github.com/apache/poi/tree/47dcff769bb395034dddfd59c4609659b6dbc6a4/test-data/hsmf) | Commit `47dcff769bb395034dddfd59c4609659b6dbc6a4`, Apache-2.0 | Explicit 25-name MSG list in `internal/corpus/select.go`, from name-sorted files at most 512 KiB; `clusterfuzz-*` excluded |
| [ripgrep](https://github.com/BurntSushi/ripgrep/tree/14.1.1) | Tag 14.1.1, commit `4649aa9700619f94cf9c66876e9549d83420e16c`; tarball tag object `0e8390a66fbcf6eeac1aeb0541b367663a597c79`, SHA-256 pinned; MIT OR Unlicense | All eligible files under `ripgrep/` |
| garp | `git archive v0.10.0`, verified commit `f85a90c24d69e5febf829961c29658d73edfb01c`; MIT | All eligible files under `garp/`; frozen source keeps gold line ranges stable |

Docs eligibility uses `config.DocumentTypes`; code additionally allows code
extensions/filenames. Both exclude `config.ShouldSkipDirectory` segments.
Ineligible binaries and assets are not part of the universe. Ignore handling
is disabled in the systems so ancestor ignore files cannot change it.

The manifests contain **430 docs files (38,749,661 bytes)** and **206 code
files (3,938,077 bytes)**. Docs budgets are 450 files, 50 MB on disk and 90 MB
download; code's eligible files must fit 10 MB. Selective HTTP Range ZIP reads
avoid downloading the entire GovDocs archives. The fetcher verifies already
cached bytes, skips matches, and refetches missing/mismatched files; upstream
pin/hash mismatches fail loudly. Sources are not mirrored.

## Measured runtime and host

A full `make eval EVAL_CACHE=<scratch-cache>` took **254.42 seconds wall time**
with `/usr/bin/time -p`, including builds, integrity verification, indexes,
all 84 queries across four systems, scoring and reporting. It ran after
fetching and recording the baseline on host `trogdor`: Intel Core i7-11850H
2.50 GHz, 8 physical cores / 16 logical CPUs, x86_64 Ubuntu 24.04.5 LTS,
Linux 7.0.0-38-generic, Go 1.27.4, ripgrep 14.1.0. This is a measurement, not
a runtime guarantee; caches, host load, and extraction cost affect it.

There is **one timed pass per system/query**, not the design's original
warm-up plus timed pass. Corpus SHA-256 verification reads every file first;
a full garp warm-up would be largely redundant, and garp's `FADV_DONTNEED`
can evict pages after reading. Process timings are start-to-exit (all rg
stages summed); BM25 query timing excludes its separately reported index
build. Timing is informational and omitted from deterministic JSON.

## Known limitations

- Mechanical kind checks use working-tree extraction; if an extraction change invalidates a gold file's kind label, `make eval` exits 2 until labels are re-judged -- intentional under the frozen-key rule, not a bug.
  Gold files with zero extracted tokens (extraction failures, e.g. `docs-002`)
  are exempt from the `single` presence check and remain honest misses, so the
  check cannot catch a wrong label on such a file.
- Linux/macOS tooling is required; Windows `make eval` support is not provided.
- rg numbers depend on the installed rg version, recorded in JSON, and are
  never gated. rg scans original bytes with its default binary handling,
  whereas BM25 shares garp's extraction and therefore isolates ranking, not
  extraction quality. Independent gold still exposes extraction misses.
- hit@N measures per-gold-range starts, not individual lines or overlap. It
  and tokens use normalized stdout, including normalization of the echoed
  root; see the precise contract above. They are not raw cache-path costs.
- GNU make's failure exit is 2; use the harness status line or invoke the
  built binary when distinguishing regression 1 from setup error 2.
- One timed pass is not a statistical performance benchmark. Timing and token
  costs are reported, not gated.
- Pooled judgments are not exhaustive corpus relevance labels. Unjudged files
  count as nonrelevant; new retrieval strategies require additional judging.
  The second judge samples the key rather than independently relabeling all
  84 queries. Do not generalize these small-corpus scores to every workload.
- Harness query syntax is deliberately narrower than the CLI: OR-groups are
  supported in paired synonym overlays, but `--not`, literal-pipe escaping,
  and mid-term wildcards are not. There is no agent-in-the-loop evaluation,
  CI integration, or mbox/XLSX/PPTX corpus. Upstream availability is needed
  for an uncached fetch.
