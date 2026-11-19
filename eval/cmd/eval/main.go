package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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
	synonymOR := flags.String("synonym-or", "", "paired synonym OR overlay (incompatible with -baseline)")
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
	if *synonymOR != "" && *baselinePath != "" {
		return fail(fmt.Errorf("-baseline cannot be combined with -synonym-or"))
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
		origSet  queryset.Set
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
		in := &input{manifest: m, set: s, origSet: s, indexes: make(map[string]*systems.Index)}
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
	// Kind checks above always see the frozen single-wording sets. Only now
	// apply expansions; previous executes original args but shares judgments.
	var keyGaps map[string][]string
	if *synonymOR != "" {
		sets, paths := make(map[string]queryset.Set), make(map[string][]string)
		for _, name := range names {
			sets[name], paths[name] = inputs[name].set, inputs[name].paths
		}
		overlaid, hash, err := loadSynonymOROverlay(*synonymOR, sets, paths)
		if err != nil {
			return fail(err)
		}
		for _, name := range names {
			inputs[name].set = overlaid[name]
		}
		result.Harness.QuerySHA256["synonym_or"] = hash
		gapFile := strings.TrimSuffix(*synonymOR, ".json") + ".key_gaps.json"
		keyGaps, hash, err = loadSynonymORKeyGaps(gapFile, overlaid, paths)
		if err != nil {
			return fail(err)
		}
		result.Harness.QuerySHA256["synonym_or_key_gaps"] = hash
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
		} else if !*noGate && *synonymOR == "" {
			return fail(fmt.Errorf("baseline missing: run make eval-baseline"))
		}
		pv, err := systems.GarpVersion(*previous)
		if err != nil {
			return fail(err)
		}
		result.Systems.Previous = report.Previous{Version: pv, Ref: *previousRef}
		if !*noGate && *synonymOR == "" {
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
		for qi, q := range in.set.Queries {
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
					r, err = systems.RunGarp(*previous, name, rootAbs, in.origSet.Queries[qi].Args)
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
				scored, err := score(q, system, r, in.set.TokenBudget, keyGaps[q.ID]...)
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
		for _, kind := range []string{"proximity", "synonym", "single", "known_key_gap"} {
			var group []report.Query
			for _, q := range c.Queries {
				groupKind := q.Kind
				if len(keyGaps[q.ID]) > 0 {
					groupKind = "known_key_gap"
				}
				if groupKind == kind {
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
	if len(keyGaps) > 0 {
		md.WriteString("### known key gaps (excluded from strict synonym lift)\n\nMetrics below and corpus aggregates use the incomplete frozen gold; registered paths are judged relevant but receive no gold credit.\n\n| query | paths | system | R@10 | hit@N |\n|---|---|---|---|---|\n")
		for _, name := range names {
			for _, q := range result.Corpora[name].Queries {
				if len(keyGaps[q.ID]) == 0 {
					continue
				}
				for _, system := range []string{"candidate", "previous", "bm25", "rg"} {
					s := report.Summarize([]report.Query{q}, system)
					hit := "n/a"
					if s.HitN != nil {
						hit = fmt.Sprintf("%.6f", *s.HitN)
					}
					fmt.Fprintf(&md, "| %s | %s | %s | %.6f | %s |\n", q.ID, strings.Join(keyGaps[q.ID], ", "), system, s.R10, hit)
				}
			}
		}
		md.WriteByte('\n')
	}
	status := 0
	if !*noGate {
		// Compare exactly the representations persisted on disk, not a rounded
		// baseline against full-precision macro means (e.g. 2/3 rounds upward).
		var current report.Results
		if err := json.Unmarshal(jsonOut.Bytes(), &current); err != nil {
			return fail(err)
		}
		var regressions []string
		if *synonymOR != "" {
			regressions = synonymORGate(current)
			fmt.Fprintf(&md, "### synonym-or gate vs previous (%s):", *previousRef)
		} else {
			for _, r := range report.Gate(*baseline, current) {
				regressions = append(regressions, fmt.Sprintf("%s %s: %.6f -> %.6f", r.Corpus, r.Metric, r.Baseline, r.Candidate))
			}
			fmt.Fprintf(&md, "### regressions vs %s:", *baselinePath)
		}
		if len(regressions) == 0 {
			md.WriteString(" none\n")
		} else {
			md.WriteByte('\n')
			for _, r := range regressions {
				fmt.Fprintln(&md, r)
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

// synonymOREntry cannot change needs, roots, kinds, or gold. Unknown JSON
// fields are rejected, not silently ignored, to protect the frozen key.
type synonymOREntry struct {
	Args                   []string `json:"args"`
	ExtraJudgedNonrelevant []string `json:"extra_judged_nonrelevant"`
}

func loadSynonymOROverlay(filename string, originals map[string]queryset.Set, paths map[string][]string) (map[string]queryset.Set, string, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, "", err
	}
	var overlay map[string]map[string]synonymOREntry
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&overlay); err != nil {
		return nil, "", err
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return nil, "", fmt.Errorf("overlay: expected one JSON object")
	}
	sets := make(map[string]queryset.Set)
	for name := range overlay {
		if _, ok := originals[name]; !ok {
			return nil, "", fmt.Errorf("overlay: unknown corpus %q", name)
		}
	}
	for name, orig := range originals {
		s := orig
		s.Queries = slices.Clone(orig.Queries)
		byID := make(map[string]int)
		for i, q := range s.Queries {
			byID[q.ID] = i
		}
		for id, entry := range overlay[name] {
			i, ok := byID[id]
			if !ok {
				return nil, "", fmt.Errorf("overlay: unknown query %q in %s", id, name)
			}
			q := &s.Queries[i]
			if q.Kind != "synonym" {
				return nil, "", fmt.Errorf("overlay query %s: not synonym", id)
			}
			terms, origTerms := systems.Terms(entry.Args), systems.Terms(q.Args)
			if len(terms) != len(origTerms) || !slices.Equal(queryFlags(entry.Args), queryFlags(q.Args)) {
				return nil, "", fmt.Errorf("overlay query %s: changed flags or slot count", id)
			}
			expanded := false
			for j, term := range terms {
				alts := strings.Split(term, "|")
				if alts[0] != origTerms[j] || strings.Contains(term, "*") {
					return nil, "", fmt.Errorf("overlay query %s: changed first alternative or wildcard", id)
				}
				expanded = expanded || len(alts) > 1
			}
			if !expanded {
				return nil, "", fmt.Errorf("overlay query %s: no OR expansion", id)
			}
			seen := make(map[string]bool)
			for _, g := range q.Gold {
				seen[g.File] = true
			}
			for _, p := range q.JudgedNonrelevant {
				seen[p] = true
			}
			for j, p := range entry.ExtraJudgedNonrelevant {
				if seen[p] || (j > 0 && entry.ExtraJudgedNonrelevant[j-1] >= p) {
					return nil, "", fmt.Errorf("overlay query %s: unsorted, duplicate or colliding judgment", id)
				}
				seen[p] = true
			}
			q.Args = slices.Clone(entry.Args)
			q.JudgedNonrelevant = append(slices.Clone(q.JudgedNonrelevant), entry.ExtraJudgedNonrelevant...)
			sort.Strings(q.JudgedNonrelevant)
		}
		if errs := queryset.Validate(s, paths[name], minimums); len(errs) > 0 {
			return nil, "", errs[0]
		}
		sets[name] = s
	}
	return sets, fmt.Sprintf("%x", sha256.Sum256(data)), nil
}

type synonymORKeyGap struct {
	ID       string `json:"id"`
	Path     string `json:"path"`
	Need     string `json:"need"`
	Evidence string `json:"evidence"`
}

// The required sibling register makes exclusions explicit and hash-bound, even
// when empty. It records relevance without mutating either frozen judgment set.
func loadSynonymORKeyGaps(filename string, sets map[string]queryset.Set, paths map[string][]string) (map[string][]string, string, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, "", err
	}
	var entries []synonymORKeyGap
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&entries); err != nil {
		return nil, "", err
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return nil, "", fmt.Errorf("key gaps: expected one JSON array")
	}
	queries, corpora := make(map[string]queryset.Query), make(map[string]string)
	for name, s := range sets {
		for _, q := range s.Queries {
			queries[q.ID], corpora[q.ID] = q, name
		}
	}
	gaps := make(map[string][]string)
	for _, g := range entries {
		q, ok := queries[g.ID]
		if !ok || q.Kind != "synonym" || !strings.Contains(strings.Join(systems.Terms(q.Args), " "), "|") || g.Need != q.Need || strings.TrimSpace(g.Evidence) == "" {
			return nil, "", fmt.Errorf("key gap %s: unknown/unexpanded synonym, changed need or missing evidence", g.ID)
		}
		if !fs.ValidPath(g.Path) || g.Path == "." || strings.Contains(g.Path, `\`) || !slices.Contains(paths[corpora[g.ID]], filepath.ToSlash(filepath.Join(q.Root, g.Path))) {
			return nil, "", fmt.Errorf("key gap %s: invalid or unmanifested path %q", g.ID, g.Path)
		}
		collision := slices.Contains(q.JudgedNonrelevant, g.Path) || slices.Contains(gaps[g.ID], g.Path)
		for _, gold := range q.Gold {
			collision = collision || gold.File == g.Path
		}
		if collision {
			return nil, "", fmt.Errorf("key gap %s: duplicate or already judged path %q", g.ID, g.Path)
		}
		gaps[g.ID] = append(gaps[g.ID], g.Path)
	}
	for id := range gaps {
		sort.Strings(gaps[id])
	}
	return gaps, fmt.Sprintf("%x", sha256.Sum256(data)), nil
}

// queryFlags retains ordered flag/value pairs, independently of term placement.
// Unrecognized tokens are rejected by queryset.Validate before any execution.
func queryFlags(args []string) []string {
	var flags []string
	for i := 0; i < len(args); i++ {
		if strings.HasPrefix(args[i], "-") {
			flags = append(flags, args[i])
			if (args[i] == "--distance" || args[i] == "-m") && i+1 < len(args) {
				i++
				flags = append(flags, args[i])
			}
		}
	}
	return flags
}

// synonymORGate compares persisted, rounded metrics within a paired run.
// Non-synonym equality intentionally requires the same binary on both sides.
func synonymORGate(current report.Results) []string {
	var failures []string
	for _, name := range []string{"docs", "code"} {
		c := current.Corpora[name]
		// The authorized top-rank tradeoff is fixture-specific, not a general
		// tolerance. Verify its exact rank transition and guard every other
		// query so improvements elsewhere cannot hide another regression.
		boundedR1 := false
		for _, q := range c.Queries {
			before, after := q.PerSystem["previous"], q.PerSystem["candidate"]
			allowed := name == "code" && q.ID == "code-g-008" && q.Kind == "synonym" &&
				slices.Equal(before.GoldRanks, []int{1}) && slices.Equal(after.GoldRanks, []int{2})
			boundedR1 = boundedR1 || allowed
			b, a := report.Summarize([]report.Query{q}, "previous"), report.Summarize([]report.Query{q}, "candidate")
			for _, m := range []struct {
				label         string
				before, after report.Number
			}{
				{"r1", b.R1, a.R1}, {"r5", b.R5, a.R5}, {"mrr10", b.MRR10, a.MRR10},
			} {
				// Rank 1 -> 2 necessarily halves this query's reciprocal rank;
				// cohort and aggregate MRR still must not regress below.
				if allowed && (m.label == "r1" || m.label == "mrr10") {
					continue
				}
				if m.after < m.before-1e-9 {
					failures = append(failures, fmt.Sprintf("%s query %s %s: %.6f -> %.6f", name, q.ID, m.label, m.before, m.after))
				}
			}
		}
		for _, system := range []string{"candidate", "previous", "bm25", "rg"} {
			s, ok := c.Aggregate[system]
			if !ok || s.Unjudged10 != 0 {
				failures = append(failures, name+" "+system+": unjudged10 must be zero")
			}
		}
		for _, kind := range []string{"proximity", "single", "synonym", "aggregate"} {
			group := c.ByKind[kind]
			if kind == "aggregate" {
				group = c.Aggregate
			}
			before, after := group["previous"], group["candidate"]
			if before.HitN == nil || after.HitN == nil {
				failures = append(failures, name+" "+kind+": missing paired metrics")
				continue
			}
			for _, m := range []struct {
				label         string
				before, after report.Number
			}{
				{"r1", before.R1, after.R1}, {"r5", before.R5, after.R5}, {"r10", before.R10, after.R10}, {"mrr10", before.MRR10, after.MRR10}, {"hit_n", *before.HitN, *after.HitN},
			} {
				delta := float64(m.after - m.before)
				bad := false
				switch {
				case kind == "single" || kind == "proximity":
					bad = math.Abs(delta) > 1e-9
				case kind == "synonym" && (m.label == "r10" || m.label == "hit_n"):
					bad = delta <= 1e-9
				case boundedR1 && m.label == "r1" && kind == "synonym":
					bad = delta < -.10-1e-9
				case boundedR1 && m.label == "r1" && kind == "aggregate":
					bad = delta < -.03-1e-9
				case m.label == "r1" || m.label == "r5" || m.label == "mrr10":
					bad = delta < -1e-9
				}
				if bad {
					failures = append(failures, fmt.Sprintf("%s %s %s: %.6f -> %.6f", name, kind, m.label, m.before, m.after))
				}
			}
		}
	}
	return failures
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

// Kind checks count query terms (not split tokens). A raw or extraction-cleaned
// term is present when all its BM25-tokenized parts occur, with prefix matching
// for a final '*'. Cleaning uses each gold file's cleaner; accepting the raw form
// also preserves case-insensitive matches when cleaning depends on letter case.
// Single requires its term in every gold file with extracted tokens; zero-token
// gold (extraction failure) is exempt so it stays an honest miss.
// Synonym requires a common missing term
// across every gold file, not different missing terms in different files.
// --partial, --partial=prefix, --partial=contains, and --smart-forms exempt
// all kinds from presence checks because exact-token presence cannot establish
// broadened matches. Term-count requirements still apply.
func checkKind(q queryset.Query, idx *systems.Index) error {
	terms := systems.Terms(q.Args)
	for _, term := range terms {
		if strings.Contains(term, "|") {
			return fmt.Errorf("query %s: base queries must not contain OR groups", q.ID)
		}
	}
	bad := func() error { return fmt.Errorf("query %s: kind %s contradicts gold tokens", q.ID, q.Kind) }
	if q.Kind == "single" {
		if len(terms) != 1 {
			return bad()
		}
		if broadensMatching(q.Args) {
			return nil
		}
		for _, g := range q.Gold {
			tokens := idx.Tokens(g.File)
			if len(tokens) > 0 && !termPresentIn(g.File, terms[0], tokens) {
				return bad()
			}
		}
		return nil
	}
	if len(terms) < 2 {
		return bad()
	}
	if broadensMatching(q.Args) {
		return nil
	}
	absentEverywhere := make([]bool, len(terms))
	for i := range absentEverywhere {
		absentEverywhere[i] = true
	}
	for _, g := range q.Gold {
		tokens := idx.Tokens(g.File)
		for i, term := range terms {
			present := termPresentIn(g.File, term, tokens)
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
func broadensMatching(args []string) bool {
	for _, arg := range args {
		switch arg {
		case "--partial", "--partial=prefix", "--partial=contains", "--smart-forms":
			return true
		}
	}
	return false
}

func termPresentIn(file, term string, tokens map[string]bool) bool {
	return termPresent(term, tokens) || termPresent(systems.CleanTerm(file, term), tokens)
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

// Known key gaps are adjudicated relevant, but never added to the frozen gold
// or nonrelevant sets. They affect coverage only; recall/hit remain incomplete.
func score(q queryset.Query, system string, r systems.Run, budget int, keyGaps ...string) (report.QueryMetrics, error) {
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
	for _, p := range keyGaps {
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
