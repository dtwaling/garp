package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"garp/eval/internal/corpus"
	"garp/eval/internal/metrics"
	"garp/eval/internal/queryset"
	"garp/eval/internal/report"
	"garp/eval/internal/systems"
)

// Tests lower only the query-count minimums, never the content checks.
var minimums = queryset.DesignMinimums()

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(argv []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("eval", flag.ContinueOnError)
	flags.SetOutput(stderr)
	cache := flags.String("cache", "", "verified corpus cache (required)")
	candidate := flags.String("candidate", "", "candidate garp binary (required)")
	previous := flags.String("previous", "", "previous garp binary (required)")
	previousRef := flags.String("previous-ref", "", "previous release ref (required except with -pool)")
	baselinePath := flags.String("baseline", "", "baseline results JSON")
	out := flags.String("out", "results/latest", "output prefix for JSON and markdown")
	noGate := flags.Bool("no-gate", false, "record results without regression gating")
	pool := flags.String("pool", "", "blind pool for comma-separated query IDs; writes no results")
	queriesDir := flags.String("queries", "queries", "query-set directory")
	manifestsDir := flags.String("manifests", "manifests", "manifest directory")
	if err := flags.Parse(argv); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	fail := func(err error) int {
		// Even diagnostics in blind mode must not disclose judgments or producers.
		if *pool != "" {
			fmt.Fprintln(stderr, "pool: validation or execution failed")
		} else {
			fmt.Fprintln(stderr, err)
		}
		return 2
	}
	if flags.NArg() != 0 {
		return fail(fmt.Errorf("unexpected positional arguments"))
	}
	if _, err := exec.LookPath("rg"); err != nil {
		return fail(fmt.Errorf("rg is required: %w", err))
	}
	for _, f := range []struct{ name, value string }{{"-cache", *cache}, {"-candidate", *candidate}, {"-previous", *previous}} {
		if f.value == "" {
			return fail(fmt.Errorf("%s is required", f.name))
		}
	}
	if *pool == "" && *previousRef == "" {
		return fail(fmt.Errorf("-previous-ref is required"))
	}
	type input struct {
		manifest corpus.Manifest
		set      queryset.Set
		paths    []string
		indexes  map[string]*systems.Index
	}
	inputs := make(map[string]*input)
	result := report.Results{Schema: 1, Harness: report.Harness{
		QuerySHA256: make(map[string]string), ManifestSHA256: make(map[string]string), TokenBudget: make(map[string]int), Tokenizer: "o200k_base tiktoken-go/tokenizer v0.10.0",
	}, Corpora: make(map[string]report.Corpus)}
	names := []string{"docs", "code"}
	ids := make(map[string]bool)
	for _, name := range names {
		m, mhash, err := corpus.Load(filepath.Join(*manifestsDir, name+".json"))
		if err != nil {
			return fail(err)
		}
		s, qhash, err := queryset.Load(filepath.Join(*queriesDir, name+".json"))
		if err != nil {
			return fail(err)
		}
		if m.Corpus != name || s.Corpus != name {
			return fail(fmt.Errorf("%s: corpus name mismatch", name))
		}
		in := &input{manifest: m, set: s, indexes: make(map[string]*systems.Index)}
		for _, f := range m.Files {
			if err := corpus.Eligible(name, f.Path); err != nil {
				return fail(err)
			}
			in.paths = append(in.paths, f.Path)
		}
		if errs := queryset.Validate(s, in.paths, minimums); len(errs) > 0 {
			return fail(errs[0])
		}
		for _, q := range s.Queries {
			if ids[q.ID] {
				return fail(fmt.Errorf("query %s: duplicate id across corpora", q.ID))
			}
			ids[q.ID] = true
		}
		inputs[name] = in
		result.Harness.QuerySHA256[name] = qhash
		result.Harness.ManifestSHA256[name] = mhash
		result.Harness.TokenBudget[name] = s.TokenBudget
	}
	selected := make(map[string]bool)
	if *pool != "" {
		for _, id := range strings.Split(*pool, ",") {
			if !ids[id] {
				return fail(fmt.Errorf("unknown pool query %q", id))
			}
			selected[id] = true
		}
	}
	// Verify both corpora before extracting text or starting any query process.
	for _, name := range names {
		if err := verifyCorpus(*cache, inputs[name].manifest); err != nil {
			return fail(err)
		}
	}
	for _, name := range names {
		in := inputs[name]
		c := report.Corpus{Files: len(in.paths), Timing: make(map[string][]time.Duration)}
		for _, q := range in.set.Queries {
			if in.indexes[q.Root] == nil {
				start := time.Now()
				idx, err := systems.BuildIndex(filepath.Join(*cache, name), q.Root, in.paths)
				c.IndexWall += time.Since(start)
				if err != nil {
					return fail(err)
				}
				in.indexes[q.Root] = idx
			}
			if err := checkKind(q, in.indexes[q.Root]); err != nil {
				return fail(err)
			}
		}
		result.Corpora[name] = c
	}
	var baseline *report.Results
	if *pool == "" {
		if *baselinePath != "" {
			data, err := os.ReadFile(*baselinePath)
			if err != nil {
				if !*noGate && os.IsNotExist(err) {
					return fail(fmt.Errorf("baseline missing: run make eval-baseline"))
				}
				return fail(err)
			}
			baseline = &report.Results{}
			if err := json.Unmarshal(data, baseline); err != nil {
				return fail(err)
			}
		} else if !*noGate {
			return fail(fmt.Errorf("baseline missing: run make eval-baseline"))
		}
		pv, err := systems.GarpVersion(*previous)
		if err != nil {
			return fail(err)
		}
		result.Systems.Previous = report.Previous{Version: pv, Ref: *previousRef}
		if !*noGate {
			if err := report.Stale(*baseline, result); err != nil {
				return fail(err)
			}
			for _, name := range names {
				if baseline.Corpora[name].Aggregate["candidate"].HitN == nil {
					return fail(fmt.Errorf("baseline missing candidate metrics: %s", name))
				}
			}
		}
		cv, err := systems.GarpVersion(*candidate)
		if err != nil {
			return fail(err)
		}
		rv, err := systems.RGVersion()
		if err != nil {
			return fail(err)
		}
		result.Harness.RGVersion = rv
		result.Systems = report.Systems{Candidate: report.Version{Version: cv}, Previous: report.Previous{Version: pv, Ref: *previousRef}, BM25: report.BM25{K1: 1.2, B: 0.75}}
	}
	for _, name := range names {
		in := inputs[name]
		c := result.Corpora[name]
		for _, q := range in.set.Queries {
			if *pool != "" && !selected[q.ID] {
				continue
			}
			rootAbs, err := filepath.Abs(filepath.Join(*cache, name, filepath.FromSlash(q.Root)))
			if err != nil {
				return fail(err)
			}
			qr := report.Query{ID: q.ID, Kind: q.Kind, PerSystem: make(map[string]report.QueryMetrics)}
			union := make(map[string]bool)
			for _, system := range []string{"candidate", "previous", "bm25", "rg"} {
				if *pool == "" {
					fmt.Fprintf(stderr, "%s %s %s\n", name, q.ID, system)
				}
				var r systems.Run
				switch system {
				case "candidate":
					r, err = systems.RunGarp(*candidate, name, rootAbs, q.Args)
				case "previous":
					r, err = systems.RunGarp(*previous, name, rootAbs, q.Args)
				case "rg":
					r, err = systems.RunRG(rootAbs, q.Args)
				case "bm25":
					start := time.Now()
					r.Ranking = in.indexes[q.Root].Search(systems.Terms(q.Args))
					r.Wall = time.Since(start)
				}
				if err != nil {
					return fail(fmt.Errorf("query %s %s: %w", q.ID, system, err))
				}
				if *pool != "" {
					for _, p := range r.Ranking[:min(10, len(r.Ranking))] {
						union[p] = true
					}
					continue
				}
				scored, err := score(q, system, r, in.set.TokenBudget)
				if err != nil {
					return fail(fmt.Errorf("query %s: %w", q.ID, err))
				}
				qr.PerSystem[system] = scored
				c.Timing[system] = append(c.Timing[system], r.Wall)
			}
			if *pool != "" {
				paths := make([]string, 0, len(union))
				for p := range union {
					paths = append(paths, p)
				}
				sort.Strings(paths)
				if _, err := fmt.Fprintf(stdout, "id: %s\nneed: %s\nargs: %q\n%s\n\n", q.ID, q.Need, q.Args, strings.Join(paths, "\n")); err != nil {
					return fail(err)
				}
			} else {
				c.Queries = append(c.Queries, qr)
			}
		}
		c.Aggregate = make(map[string]report.Summary)
		c.ByKind = make(map[string]map[string]report.Summary)
		for _, system := range []string{"candidate", "previous", "bm25", "rg"} {
			c.Aggregate[system] = report.Summarize(c.Queries, system)
		}
		for _, kind := range []string{"proximity", "synonym", "single"} {
			var group []report.Query
			for _, q := range c.Queries {
				if q.Kind == kind {
					group = append(group, q)
				}
			}
			if len(group) > 0 {
				c.ByKind[kind] = make(map[string]report.Summary)
				for _, system := range []string{"candidate", "previous", "bm25", "rg"} {
					c.ByKind[kind][system] = report.Summarize(group, system)
				}
			}
		}
		result.Corpora[name] = c
	}
	if *pool != "" {
		return 0
	}
	var jsonOut, md bytes.Buffer
	if err := report.WriteJSON(&jsonOut, result); err != nil {
		return fail(err)
	}
	if err := report.WriteMarkdown(&md, result, baseline); err != nil {
		return fail(err)
	}
	status := 0
	if !*noGate {
		// Compare exactly the representations persisted on disk, not a rounded
		// baseline against full-precision macro means (e.g. 2/3 rounds upward).
		var current report.Results
		if err := json.Unmarshal(jsonOut.Bytes(), &current); err != nil {
			return fail(err)
		}
		regressions := report.Gate(*baseline, current)
		fmt.Fprintf(&md, "### regressions vs %s:", *baselinePath)
		if len(regressions) == 0 {
			md.WriteString(" none\n")
		} else {
			md.WriteByte('\n')
			for _, r := range regressions {
				fmt.Fprintf(&md, "%s %s: %.6f -> %.6f\n", r.Corpus, r.Metric, r.Baseline, r.Candidate)
			}
			status = 1
		}
	}
	if err := os.MkdirAll(filepath.Dir(*out), 0755); err != nil {
		return fail(err)
	}
	if err := os.WriteFile(*out+".json", jsonOut.Bytes(), 0644); err != nil {
		return fail(err)
	}
	if err := os.WriteFile(*out+".md", md.Bytes(), 0644); err != nil {
		return fail(err)
	}
	if _, err := stdout.Write(md.Bytes()); err != nil {
		return fail(err)
	}
	return status
}

// verifyCorpus checks the entire manifest and refuses unmanifested files, so
// black-box directory walkers and the manifest-only BM25 index see one universe.
// As with corpus.Fetch, the cache must not have concurrent/untrusted writers.
func verifyCorpus(cache string, m corpus.Manifest) error {
	root := filepath.Join(cache, m.Corpus)
	expected := make(map[string]bool)
	for _, item := range m.Files {
		if expected[item.Path] {
			return fmt.Errorf("duplicate path: %s/%s", m.Corpus, item.Path)
		}
		expected[item.Path] = true
		filename := filepath.Join(root, filepath.FromSlash(item.Path))
		f, err := os.Open(filename)
		if err != nil {
			return err
		}
		st, err := f.Stat()
		if err != nil {
			f.Close()
			return err
		}
		if !st.Mode().IsRegular() {
			f.Close()
			return fmt.Errorf("not a regular file: %s/%s", m.Corpus, item.Path)
		}
		h := sha256.New()
		_, err = io.Copy(h, f)
		closeErr := f.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		if st.Size() != item.Size || fmt.Sprintf("%x", h.Sum(nil)) != item.SHA256 {
			return fmt.Errorf("sha256 mismatch: %s/%s", m.Corpus, item.Path)
		}
	}
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 || !expected[filepath.ToSlash(rel)] {
			return fmt.Errorf("unexpected corpus file: %s/%s", m.Corpus, filepath.ToSlash(rel))
		}
		return nil
	})
}

// Kind checks count query terms (not split tokens), cleaned with each gold
// file's extraction cleaner. A term is present when all its BM25-tokenized
// parts occur, with prefix matching for a final '*'. Single requires its term
// in every gold file with extracted tokens; zero-token gold (extraction failure)
// is exempt so it stays an honest miss. Synonym requires a common missing term
// across every gold file, not different missing terms in different files.
func checkKind(q queryset.Query, idx *systems.Index) error {
	terms := systems.Terms(q.Args)
	bad := func() error { return fmt.Errorf("query %s: kind %s contradicts gold tokens", q.ID, q.Kind) }
	if q.Kind == "single" {
		if len(terms) != 1 {
			return bad()
		}
		for _, g := range q.Gold {
			tokens := idx.Tokens(g.File)
			if len(tokens) > 0 && !termPresent(systems.CleanTerm(g.File, terms[0]), tokens) {
				return bad()
			}
		}
		return nil
	}
	if len(terms) < 2 {
		return bad()
	}
	absentEverywhere := make([]bool, len(terms))
	for i := range absentEverywhere {
		absentEverywhere[i] = true
	}
	for _, g := range q.Gold {
		tokens := idx.Tokens(g.File)
		for i, term := range terms {
			present := termPresent(systems.CleanTerm(g.File, term), tokens)
			if q.Kind == "proximity" && !present {
				return bad()
			}
			if present {
				absentEverywhere[i] = false
			}
		}
	}
	if q.Kind == "synonym" {
		for _, absent := range absentEverywhere {
			if absent {
				return nil
			}
		}
		return bad()
	}
	return nil
}
func termPresent(term string, tokens map[string]bool) bool {
	stem, wild := strings.CutSuffix(strings.ToLower(term), "*")
	parts := strings.FieldsFunc(stem, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	if len(parts) == 0 {
		return false
	}
	for i, p := range parts {
		found := tokens[p]
		if wild && i == len(parts)-1 {
			for token := range tokens {
				if strings.HasPrefix(token, p) {
					found = true
					break
				}
			}
		}
		if !found {
			return false
		}
	}
	return true
}
func score(q queryset.Query, system string, r systems.Run, budget int) (report.QueryMetrics, error) {
	gold := make(map[string]bool)
	judged := make(map[string]bool)
	var items []metrics.Gold
	m := report.QueryMetrics{GoldRanks: []int{}, Returned: len(r.Ranking), Error: r.Err}
	ranks := make(map[string]int)
	for i, p := range r.Ranking {
		ranks[p] = i + 1
	}
	for _, g := range q.Gold {
		if !gold[g.File] {
			gold[g.File] = true
			m.GoldRanks = append(m.GoldRanks, ranks[g.File])
		}
		item := metrics.Gold{File: g.File}
		if g.Lines != nil {
			item.Lines = &[2]int{g.Lines[0], g.Lines[1]}
		}
		items = append(items, item)
	}
	for _, p := range q.JudgedNonrelevant {
		judged[p] = true
	}
	m.Unjudged = metrics.Unjudged10(gold, judged, r.Ranking)
	m.Unjudged10 = len(m.Unjudged)
	if system != "bm25" {
		n := metrics.Count(r.Stdout)
		m.Tokens = &n
	}
	if system == "candidate" || system == "previous" {
		var hit float64
		var err error
		if r.Err == "" {
			hit, err = metrics.HitN(r.Stdout, items, budget)
			if err != nil {
				return m, err
			}
		}
		value := report.Number(hit)
		m.HitN = &value
	}
	return m, nil
}
