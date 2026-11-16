package metrics

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sync"

	"github.com/tiktoken-go/tokenizer"
)

var (
	codecOnce sync.Once
	codec     tokenizer.Codec
)

// Count counts o200k_base tokens in the supplied bytes, without normalization.
// The pinned tokenizer embeds its vocabulary; no runtime download is needed.
// An unexpected tokenizer error panics rather than silently undercounting.
func Count(b []byte) int {
	codecOnce.Do(func() {
		var err error
		codec, err = tokenizer.Get(tokenizer.O200kBase)
		if err != nil {
			panic(fmt.Errorf("initialize o200k_base: %w", err))
		}
	})
	n, err := codec.Count(string(b))
	if err != nil {
		panic(fmt.Errorf("count o200k_base tokens: %w", err))
	}
	return n
}

// Gold is one judged evidence item. Lines, when present, is an inclusive,
// 1-based range on a text/code file. Callers validate ranges before scoring.
// A nil Lines accepts file-level evidence, including extracted formats.
// Multiple ranges in the same file are separate items for hit@N.
type Gold struct {
	File  string  `json:"file"`
	Lines *[2]int `json:"lines,omitempty"`
}

// ResultEnds records byte offsets immediately after each complete element of
// the top-level results array. Unknown envelope fields are skipped as whole
// JSON values. Null results (like an empty array) represents no matches.
// Missing/duplicate results, malformed JSON, and trailing values are errors.
func ResultEnds(stdout []byte) ([]int64, error) {
	dec := json.NewDecoder(bytes.NewReader(stdout))
	if err := delimiter(dec, '{'); err != nil {
		return nil, err
	}
	var ends []int64
	found := false
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return nil, err
		}
		if key != "results" {
			var ignored json.RawMessage
			if err := dec.Decode(&ignored); err != nil {
				return nil, err
			}
			continue
		}
		if found {
			return nil, fmt.Errorf("duplicate results field")
		}
		found = true
		token, err := dec.Token()
		if err != nil {
			return nil, err
		}
		if token == nil {
			continue
		}
		if token != json.Delim('[') {
			return nil, fmt.Errorf("results must be an array or null")
		}
		for dec.More() {
			var result json.RawMessage
			if err := dec.Decode(&result); err != nil {
				return nil, err
			}
			ends = append(ends, dec.InputOffset())
		}
		if err := delimiter(dec, ']'); err != nil {
			return nil, err
		}
	}
	if err := delimiter(dec, '}'); err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("missing results field")
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, fmt.Errorf("trailing data after JSON envelope")
	}
	return ends, nil
}

func delimiter(dec *json.Decoder, want json.Delim) error {
	token, err := dec.Token()
	if err != nil {
		return err
	}
	if token != want {
		return fmt.Errorf("expected JSON delimiter %q, got %v", want, token)
	}
	return nil
}

// HitN is the fraction of gold items with evidence in complete results whose
// stdout prefix costs at most budget tokens. Callers supply path-normalized
// garp JSON (including its original whitespace); this function never re-encodes
// JSON. It re-tokenizes each prefix because BPE counts are not additive.
// A line range is hit by an excerpt start_line in that range, not by overlap:
// garp does not expose excerpt end lines. Each gold item contributes at most
// once, regardless of repeated results or excerpts. Empty gold yields zero.
func HitN(stdout []byte, gold []Gold, budget int) (float64, error) {
	ends, err := ResultEnds(stdout)
	if err != nil {
		return 0, err
	}
	// Use the exact key walked by ResultEnds, not struct decoding's
	// case-insensitive field matching, so results and offsets stay aligned.
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(stdout, &envelope); err != nil {
		return 0, err
	}
	var results []struct {
		File     string `json:"file"`
		Excerpts []struct {
			StartLine int `json:"start_line"`
		} `json:"excerpts"`
	}
	if err := json.Unmarshal(envelope["results"], &results); err != nil {
		return 0, err
	}
	if len(gold) == 0 {
		return 0, nil
	}
	hit := make([]bool, len(gold))
	for i, result := range results {
		if Count(stdout[:ends[i]]) > budget {
			continue
		}
		for j, item := range gold {
			if hit[j] || item.File != result.File {
				continue
			}
			if item.Lines == nil {
				hit[j] = true
				continue
			}
			for _, excerpt := range result.Excerpts {
				if excerpt.StartLine >= item.Lines[0] && excerpt.StartLine <= item.Lines[1] {
					hit[j] = true
					break
				}
			}
		}
	}
	hits := 0
	for _, yes := range hit {
		if yes {
			hits++
		}
	}
	return float64(hits) / float64(len(gold)), nil
}
