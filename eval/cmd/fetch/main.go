package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"garp/eval/internal/corpus"
)

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	flags := flag.NewFlagSet("fetch", flag.ContinueOnError)
	cache := flags.String("cache", "", "required corpus cache directory")
	repo := flags.String("repo", "..", "garp repository")
	manifests := flags.String("manifests", "manifests", "manifest directory")
	selection := flags.String("select", "", "regenerate docs or code manifest")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	fail := func(err error) int { fmt.Fprintln(os.Stderr, err); return 2 }
	if *cache == "" || flags.NArg() != 0 {
		return fail(fmt.Errorf("-cache DIR is required; positional arguments are not accepted"))
	}
	if *selection != "" {
		m, err := corpus.Select(*selection, *cache, *repo)
		if err != nil {
			return fail(err)
		}
		if err := corpus.WriteManifest(*manifests, m); err != nil {
			return fail(err)
		}
		var size int64
		for _, f := range m.Files {
			size += f.Size
		}
		fmt.Fprintf(os.Stderr, "selected %s: %d files, %d bytes\n", m.Corpus, len(m.Files), size)
		return 0
	}
	var total corpus.FetchStats
	for _, name := range []string{"docs", "code"} {
		m, _, err := corpus.Load(filepath.Join(*manifests, name+".json"))
		if err != nil {
			return fail(err)
		}
		if m.Corpus != name {
			return fail(fmt.Errorf("manifest corpus mismatch: %s", name))
		}
		stats, err := corpus.Fetch(m, *cache, *repo)
		if err != nil {
			return fail(err)
		}
		total.Fetched += stats.Fetched
		total.Present += stats.Present
		total.Bytes += stats.Bytes
	}
	fmt.Fprintf(os.Stderr, "fetched %d files, %d bytes; %d already present\n", total.Fetched, total.Bytes, total.Present)
	return 0
}
