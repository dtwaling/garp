package search

import (
	"strings"
	"testing"
)

func TestBuildTermRegexLowerModes(t *testing.T) {
	tests := []struct {
		name    string
		word    string
		mode    PartialMode
		matches []string
		rejects []string
	}{
		{
			name:    "off preserves legacy whole word inflections",
			word:    "deploy",
			mode:    PartialModeOff,
			matches: []string{"deploy", "deploys", "deployes"},
			rejects: []string{"deployed", "deployment", "redeploy"},
		},
		{
			name:    "prefix expands suffixes without internal substrings",
			word:    "deploy",
			mode:    PartialModePrefix,
			matches: []string{"deploy", "deployed", "deployment", "deploy_hook"},
			rejects: []string{"redeploy", "autodeploy"},
		},
		{
			name:    "contains matches raw substrings",
			word:    "deploy",
			mode:    PartialModeContains,
			matches: []string{"deploy", "deployed", "redeploy", "autodeploy"},
		},
		{
			name:    "short term prefix falls back to whole word",
			word:    "in",
			mode:    PartialModePrefix,
			matches: []string{"in", "ins"},
			rejects: []string{"inside"},
		},
		{
			name:    "short term contains falls back to whole word",
			word:    "in",
			mode:    PartialModeContains,
			matches: []string{"in", "ins"},
			rejects: []string{"inside"},
		},
		{
			name:    "three character contains remains partial",
			word:    "cat",
			mode:    PartialModeContains,
			matches: []string{"application"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			re := buildTermRegexLower(tt.word, tt.mode)
			for _, text := range tt.matches {
				if !re.MatchString(text) {
					t.Errorf("%q did not match %q", re.String(), text)
				}
			}
			for _, text := range tt.rejects {
				if re.MatchString(text) {
					t.Errorf("%q unexpectedly matched %q", re.String(), text)
				}
			}
		})
	}
}

func TestBuildTermRegexCapturesTokenWithoutBoundary(t *testing.T) {
	for _, text := range []string{"_deploy_hook", ".deploy_hook"} {
		re := buildTermRegexLower("deploy", PartialModePrefix)
		indexes := re.FindStringSubmatchIndex(text)
		if len(indexes) < 4 || indexes[2] < 0 {
			t.Fatalf("%q did not return capture group 1 indexes: %v", text, indexes)
		}
		if got := text[indexes[2]:indexes[3]]; got != "deploy_hook" {
			t.Errorf("group 1 = %q, want deploy_hook", got)
		}
		if indexes[2] != 1 {
			t.Errorf("group 1 started at %d, want 1", indexes[2])
		}
	}
}

func TestBuildTermRegexCIIsCaseInsensitive(t *testing.T) {
	for _, word := range []string{"Deploy", "DEPLOY", "deploy"} {
		re := buildTermRegexCI(word, PartialModePrefix)
		for _, text := range []string{"deploy", "DEPLOYMENT", "Deploy_Hook"} {
			if !re.MatchString(text) {
				t.Errorf("buildTermRegexCI(%q) %q did not match %q", word, re.String(), text)
			}
		}
	}
}

func TestBuildTermRegexPrefixRejectsInternalSubstring(t *testing.T) {
	re := buildTermRegexLower("cat", PartialModePrefix)
	if re.MatchString("application") {
		t.Errorf("%q unexpectedly matched internal cat in application", re.String())
	}
	if !strings.Contains(re.String(), "(") {
		t.Errorf("%q does not contain a capture group", re.String())
	}
}
