package search

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCaseSensitiveTextAndBinaryDiscovery(t *testing.T) {
	root := t.TempDir()
	fixtures := map[string]string{
		"upper.txt": "API",
		"lower.txt": "api",
		"upper.eml": "Subject: Upper\r\n\r\nAPI",
		"lower.eml": "Subject: Lower\r\n\r\napi",
	}
	for name, content := range fixtures {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	engine := NewSearchEngine([]string{"API"}, nil, []string{"-g", "*.txt", "-g", "*.eml"}, false, 1, 1000)
	engine.StartDir = root
	engine.Silent = true
	engine.PathOnly = true
	engine.CaseSensitive = true
	results, err := engine.Execute()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, result := range results {
		got[filepath.Base(result.FilePath)] = true
	}
	if len(got) != 2 || !got["upper.txt"] || !got["upper.eml"] {
		t.Fatalf("case-sensitive results = %v", got)
	}
}

func TestCaseSensitiveExcludeWordsAndExtensionExclusions(t *testing.T) {
	root := t.TempDir()
	for name, content := range map[string]string{
		"upper.txt": "Needle BLOCK",
		"lower.txt": "Needle block",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	engine := NewSearchEngine([]string{"Needle"}, []string{"BLOCK"}, []string{"-g", "*.txt"}, false, 1, 1000)
	engine.StartDir = root
	engine.Silent = true
	engine.PathOnly = true
	engine.CaseSensitive = true
	results, err := engine.Execute()
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || filepath.Base(results[0].FilePath) != "lower.txt" {
		t.Fatalf("word-exclusion results = %#v", results)
	}

	engine.ExcludeWords = []string{".TXT"}
	results, err = engine.Execute()
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Fatalf("case-insensitive extension exclusion retained %#v", results)
	}
}

func TestCaseSensitiveExclusionsStayLiteralWholeWords(t *testing.T) {
	sensitiveSmart := QueryOptions{CaseSensitive: true, SmartForms: true}
	for text, want := range map[string]bool{
		"BLOCK":  true,
		"BLOCKs": false,
		"BLOCKS": false,
	} {
		if got := CheckTextContainsExcludeWordsWithOptions(text, []string{"BLOCK"}, sensitiveSmart); got != want {
			t.Errorf("case-sensitive exclusion for %q = %t, want %t", text, got, want)
		}
	}
	if !CheckTextContainsExcludeWordsWithOptions("Needle BLOCK", []string{"BLOCK"}, sensitiveSmart) {
		t.Fatal("exact-case exclusion did not match")
	}
	if CheckTextContainsExcludeWordsWithOptions("Needle wild*", []string{"wild*"}, sensitiveSmart) {
		t.Fatal("legacy word boundaries unexpectedly matched a trailing-punctuation exclusion")
	}
	for name, text := range map[string]string{
		"case mismatch":        "Needle block",
		"wildcard expansion":   "Needle wildcard",
		"smart form expansion": "Needle BLOCKED",
		"embedded literal":     "Needle xwild*y",
	} {
		if CheckTextContainsExcludeWordsWithOptions(text, []string{"BLOCK", "wild*"}, sensitiveSmart) {
			t.Errorf("%s unexpectedly matched: %q", name, text)
		}
	}

	insensitive := QueryOptions{SmartForms: true}
	for _, text := range []string{"BLOCK", "BLOCKs", "BLOCKS"} {
		if !CheckTextContainsExcludeWordsWithOptions(text, []string{"BLOCK"}, insensitive) {
			t.Errorf("case-insensitive compatibility exclusion rejected %q", text)
		}
	}
	for name, test := range map[string]struct {
		text    string
		exclude string
	}{
		"legacy trailing punctuation": {text: "Needle WILD*", exclude: "wild*"},
		"wildcard expansion":          {text: "Needle wildcard", exclude: "wild*"},
		"smart form expansion":        {text: "Needle blocked", exclude: "BLOCK"},
	} {
		if CheckTextContainsExcludeWordsWithOptions(test.text, []string{test.exclude}, insensitive) {
			t.Errorf("%s unexpectedly matched %q with %q", name, test.text, test.exclude)
		}
	}
}

func TestCaseInsensitiveExclusionsPreserveLegacyWordBoundaries(t *testing.T) {
	for _, tt := range []struct {
		text    string
		exclude string
	}{
		{text: "(test), passed", exclude: "test"},
		{text: "bob@spam.com", exclude: "@spam.com"},
		{text: "message from bob@spam.com!", exclude: "@SPAM.COM"},
	} {
		if !CheckTextContainsExcludeWords(tt.text, []string{tt.exclude}) {
			t.Errorf("legacy exclusion %q did not match %q", tt.exclude, tt.text)
		}
	}
	for _, text := range []string{"contest", "testament"} {
		if CheckTextContainsExcludeWords(text, []string{"test"}) {
			t.Errorf("whole-word exclusion matched %q", text)
		}
	}
}

func TestCaseSensitiveSmartFormsAndRankingMetadata(t *testing.T) {
	root := t.TempDir()
	for name, content := range map[string]string{
		"exact.txt":  "DeployED",
		"folded.txt": "deployED",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	engine := NewSearchEngine([]string{"Deploy"}, nil, []string{"-g", "*.txt"}, false, 1, 1000)
	engine.StartDir = root
	engine.Silent = true
	engine.CaseSensitive = true
	engine.SmartForms = true
	results, err := engine.Execute()
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || filepath.Base(results[0].FilePath) != "exact.txt" {
		t.Fatalf("smart-form results = %#v", results)
	}
	if results[0].TermCount != 1 || len(results[0].MatchedTerms) != 1 || results[0].MatchedTerms[0] != "Deploy" {
		t.Fatalf("ranking metadata = %#v", results[0])
	}
}
