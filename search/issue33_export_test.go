package search

import (
	"io"
	"sync/atomic"
	"time"
)

// Issue33StagesForTest runs the actual discovery/filter pipeline with candidate
// provenance intact, without Stage 3 reads. The callback observes the stage boundary.
func Issue33StagesForTest(se *SearchEngine, afterDiscovery func(int)) (int, int64, error) {
	msg := &issue33MSGExtractor{}
	original := se.Registry.extractors["msg"]
	se.Registry.extractors["msg"] = msg
	defer func() { se.Registry.extractors["msg"] = original }()
	roots, err := se.effectiveStartRoots()
	if err != nil {
		return 0, 0, err
	}
	query := compileQueryWithOptions(se.SearchWords, se.Partial, se.queryOptions())
	candidates, total, err := se.discoverCandidates(query, 1, roots)
	if err != nil {
		return 0, 0, err
	}
	if afterDiscovery != nil {
		afterDiscovery(len(candidates))
	}
	matches, err := se.filterCandidates(query, candidates, total, time.Now())
	return len(matches), msg.calls.Load(), err
}

type issue33MSGExtractor struct {
	MSGExtractor
	calls atomic.Int64
}

func (e *issue33MSGExtractor) ExtractReaderAt(r io.ReaderAt, size, limit int64) (string, error) {
	e.calls.Add(1)
	return e.MSGExtractor.ExtractReaderAt(r, size, limit)
}
