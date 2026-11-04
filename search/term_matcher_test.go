package search

import (
	"strings"
	"testing"
)

func TestNormalizeQueryTerms(t *testing.T) {
	got := NormalizeQueryTerms([]string{
		"  ms-**-api  ",
		"MS-*-API",
		"",
		"***OptionApi**",
		"*optionapi*",
	})
	want := []string{"ms-*-api", "*optionapi*"}
	if len(got) != len(want) {
		t.Fatalf("NormalizeQueryTerms() = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("NormalizeQueryTerms()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestCaseSensitiveNormalizeQueryTerms(t *testing.T) {
	got := NormalizeQueryTermsWithOptions(
		[]string{" API ", "api", "API", "É*API", "é*api"},
		QueryOptions{CaseSensitive: true},
	)
	want := []string{"API", "api", "É*API", "é*api"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("case-sensitive terms = %q, want %q", got, want)
	}
}

func TestCaseSensitiveTermMatcherSuffixContract(t *testing.T) {
	exact := compileTermMatcherWithOptions("Deploy", PartialModeOff, QueryOptions{CaseSensitive: true})
	for _, rejected := range []string{"deploy", "Deploys", "DeployES", "Deployed"} {
		if exact.matches(rejected) {
			t.Errorf("exact matcher unexpectedly matched %q", rejected)
		}
	}
	if !exact.matches("Deploy") {
		t.Fatal("exact matcher rejected exact-case stem")
	}

	smart := compileTermMatcherWithOptions("Deploy", PartialModeOff, QueryOptions{
		CaseSensitive: true,
		SmartForms:    true,
	})
	for _, accepted := range []string{"DeployED", "DeployIng", "DeployATION"} {
		if !smart.matches(accepted) {
			t.Errorf("smart matcher rejected mixed-case suffix %q", accepted)
		}
	}
	if smart.matches("deployED") {
		t.Fatal("smart matcher folded the exact-case stem")
	}
}

func TestTermMatcherWildcardMatrix(t *testing.T) {
	tests := []struct {
		name    string
		term    string
		matches []string
		rejects []string
	}{
		{
			name: "internal star",
			term: "ms-*-api",
			matches: []string{
				"ms-option-api",
				"dev-ms-option-api-alb",
				"ms-core.v1-api",
				"ms-first-apiX-ms-second-api",
			},
			rejects: []string{
				"MsOptionApiClient",
				"ms_option_api",
				"ms-core.api",
				"ms-option-apis",
				"ms- internal api",
				"ms-\n-api",
			},
		},
		{
			name:    "surrounding stars",
			term:    "*OptionApi*",
			matches: []string{"MsOptionApiClient", "defaultMsOptionApiOrigin", "OptionApi"},
		},
		{
			name:    "leading internal and trailing stars",
			term:    "*ms-*-api*",
			matches: []string{"https://dev-ms-option-api-alb.ihmsweb.com/api/v1"},
		},
		{
			name:    "single character question",
			term:    "ms-?-api",
			matches: []string{"ms-a-api", "ms-1-api", "MS-.-API"},
			rejects: []string{"ms--api", "ms-ab-api", "ms- -api", "ms-\n-api"},
		},
		{
			name:    "question inside word",
			term:    "mark?systems",
			matches: []string{"mark-systems", "mark.systems"},
			rejects: []string{"marksystems", "markXXsystems", "mark systems"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matcher := compileTermMatcher(tt.term, PartialModeOff, true)
			if !matcher.validWildcard {
				t.Fatalf("%q was not qualified as a wildcard", tt.term)
			}
			for _, text := range tt.matches {
				if !matcher.matches(text) {
					t.Errorf("%q did not match %q", tt.term, text)
				}
			}
			for _, text := range tt.rejects {
				if matcher.matches(text) {
					t.Errorf("%q unexpectedly matched %q", tt.term, text)
				}
			}
		})
	}
}

func TestTermMatcherWildcardLimits(t *testing.T) {
	literal128 := strings.Repeat("a", 128)
	tests := []struct {
		name string
		term string
		want bool
	}{
		{name: "two literal bytes", term: "a*b", want: true},
		{name: "128 literal bytes", term: literal128 + "*", want: true},
		{name: "one literal byte", term: "a*", want: false},
		{name: "129 literal bytes", term: literal128 + "a*", want: false},
		{name: "four stars", term: "ab*c*d*e*f", want: true},
		{name: "five stars", term: "ab*c*d*e*f*g", want: false},
		{name: "sixteen questions", term: "ab" + strings.Repeat("?", 16), want: true},
		{name: "seventeen questions", term: "ab" + strings.Repeat("?", 17), want: false},
		{name: "collapsed stars", term: "ms-**-api", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matcher := compileTermMatcher(tt.term, PartialModeContains, true)
			if matcher.validWildcard != tt.want {
				t.Errorf("validWildcard = %t, want %t", matcher.validWildcard, tt.want)
			}
			if strings.Contains(tt.term, "**") && matcher.term != "ms-*-api" {
				t.Errorf("canonical term = %q, want ms-*-api", matcher.term)
			}
		})
	}
}

func TestTermMatcherBoundariesAndExactRanges(t *testing.T) {
	tests := []struct {
		name   string
		term   string
		mode   PartialMode
		text   string
		want   string
		reject bool
	}{
		{name: "wildcard off exact", term: "ms-*-api", text: "(ms-option-api),", want: "ms-option-api"},
		{name: "wildcard off left boundary", term: "ms-*-api", text: "xms-option-api", reject: true},
		{name: "wildcard off right boundary", term: "ms-*-api", text: "ms-option-apis", reject: true},
		{name: "wildcard prefix permits suffix", term: "ms-*-api", mode: PartialModePrefix, text: "ms-option-apis", want: "ms-option-api"},
		{name: "wildcard prefix keeps left boundary", term: "ms-*-api", mode: PartialModePrefix, text: "xms-option-api", reject: true},
		{name: "wildcard contains permits both sides", term: "ms-*-api", mode: PartialModeContains, text: "xms-option-apis", want: "ms-option-api"},
		{name: "hyphen off", term: "foo-bar", text: ".foo-bar.", want: "foo-bar"},
		{name: "hyphen off rejects attached", term: "foo-bar", text: "xfoo-barX", reject: true},
		{name: "hyphen prefix", term: "foo-bar", mode: PartialModePrefix, text: ".foo-barValue", want: "foo-barValue"},
		{name: "hyphen contains", term: "foo-bar", mode: PartialModeContains, text: "xfoo-barX", want: "foo-bar"},
		{name: "dot off", term: "foo.bar", text: "/foo.bar/", want: "foo.bar"},
		{name: "leading dot off rejects package suffix", term: ".json", text: "package.json", reject: true},
		{name: "dot prefix", term: "foo.bar", mode: PartialModePrefix, text: "/foo.barValue", want: "foo.barValue"},
		{name: "dot contains", term: "foo.bar", mode: PartialModeContains, text: "xfoo.barX", want: "foo.bar"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matcher := compileTermMatcher(tt.term, tt.mode, true)
			found, ok := matcher.find(tt.text, 0)
			if tt.reject {
				if ok {
					t.Fatalf("unexpected range [%d:%d] %q", found.start, found.end, tt.text[found.start:found.end])
				}
				return
			}
			if !ok {
				t.Fatal("no match")
			}
			if got := tt.text[found.start:found.end]; got != tt.want {
				t.Errorf("matched %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTermMatcherInvalidWildcardsAreLiteralWholeTokens(t *testing.T) {
	invalidTerms := []string{
		"*",
		"?",
		"a*",
		"*a",
		"ab*c*d*e*f*g",
		strings.Repeat("a", 129) + "*",
	}
	modes := []PartialMode{PartialModeOff, PartialModePrefix, PartialModeContains}

	for _, term := range invalidTerms {
		for _, mode := range modes {
			t.Run(term+"/"+string(mode), func(t *testing.T) {
				matcher := compileTermMatcher(term, mode, true)
				if matcher.validWildcard {
					t.Fatalf("%q unexpectedly qualified as wildcard", term)
				}
				if matcher.mode != PartialModeOff {
					t.Fatalf("effective mode = %q, want off", matcher.mode)
				}
				if !matcher.matches(" " + term + " ") {
					t.Errorf("literal %q did not match itself", term)
				}
				if matcher.matches("x"+term) || matcher.matches(term+"x") {
					t.Errorf("literal %q ignored whole-token boundaries", term)
				}
			})
		}
	}
}

func TestTermMatcherFindAllReturnsExactRanges(t *testing.T) {
	text := "[ms-a-api], (MS-b-API)."
	ranges := compileTermMatcher("ms-?-api", PartialModeOff, true).findAll(text)
	if len(ranges) != 2 {
		t.Fatalf("findAll() returned %d ranges, want 2: %#v", len(ranges), ranges)
	}
	for i, want := range []string{"ms-a-api", "MS-b-API"} {
		if got := text[ranges[i].start:ranges[i].end]; got != want {
			t.Errorf("range %d = %q, want %q", i, got, want)
		}
	}
}

func TestTermMatcherCompatibilityRegexUsesExactRepeatedRanges(t *testing.T) {
	tests := []struct {
		name string
		term string
		text string
		want []string
	}{
		{
			name: "wildcard occurrences separated by one delimiter",
			term: "ms-*-api",
			text: "ms-a-api.ms-b-api",
			want: []string{"ms-a-api", "ms-b-api"},
		},
		{
			name: "plain occurrences separated by one delimiter",
			term: "service",
			text: "service.service",
			want: []string{"service", "service"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matcher := compileTermMatcher(tt.term, PartialModeOff, true)
			ranges := matcher.findAll(tt.text)
			indexes := matcher.compatibilityRegex().FindAllStringSubmatchIndex(tt.text, -1)
			if len(ranges) != len(tt.want) || len(indexes) != len(tt.want) {
				t.Fatalf("range counts = matcher:%d regex:%d, want %d", len(ranges), len(indexes), len(tt.want))
			}
			for i, want := range tt.want {
				start, end := submatchRange(indexes[i])
				if got := tt.text[start:end]; got != want {
					t.Errorf("regex range %d = %q, want %q", i, got, want)
				}
				if ranges[i] != (matchRange{start: start, end: end}) {
					t.Errorf("matcher range %d = %#v, regex range = [%d:%d]", i, ranges[i], start, end)
				}
			}
		})
	}
}

func TestTermMatcherPlainUnderscoreBoundaryModes(t *testing.T) {
	text := "my_service"
	if compileTermMatcher("service", PartialModeOff, true).matches(text) {
		t.Fatal("off mode matched service after a leading underscore")
	}
	if !compileTermMatcher("service", PartialModePrefix, true).matches(text) {
		t.Fatal("prefix mode did not preserve the legacy underscore boundary")
	}
	if buildTermRegexCI("service", PartialModeOff).MatchString(text) {
		t.Fatal("off compatibility regex matched service after a leading underscore")
	}
	if !buildTermRegexCI("service", PartialModePrefix).MatchString(text) {
		t.Fatal("prefix compatibility regex did not preserve the legacy underscore boundary")
	}
}

func TestTermMatcherPrefixStrictBoundaryAgreement(t *testing.T) {
	for _, tt := range []struct {
		term string
		text string
	}{
		{term: "foo-bar", text: "_foo-barValue"},
		{term: "foo.bar", text: "_foo.barValue"},
	} {
		t.Run(tt.term, func(t *testing.T) {
			matcher := compileTermMatcher(tt.term, PartialModePrefix, true)
			if matcher.matches(tt.text) {
				t.Fatal("optimized find accepted underscore as a strict boundary")
			}
			if matcher.compatibilityRegex().MatchString(tt.text) {
				t.Fatal("compatibility regex accepted underscore as a strict boundary")
			}
			boundaries := utf8ByteBoundaries(tt.text)
			if _, ok := matcher.findByteExactWithBoundaries(
				tt.text,
				0,
				boundaries,
				make(map[wildcardByteState]struct{}),
			); ok {
				t.Fatal("byte-exact path accepted underscore as a strict boundary")
			}
		})
	}

	control := compileTermMatcher("deploy", PartialModePrefix, true)
	text := "_deployment"
	found, ok := control.find(text, 0)
	if !ok || text[found.start:found.end] != "deployment" {
		t.Fatalf("optimized non-strict control = %#v, ok = %t", found, ok)
	}
	indexes := control.compatibilityRegex().FindStringSubmatchIndex(text)
	if indexes == nil {
		t.Fatal("compatibility regex rejected non-strict underscore control")
	}
	start, end := submatchRange(indexes)
	if text[start:end] != "deployment" {
		t.Fatalf("compatibility control range = %q", text[start:end])
	}
	boundaries := utf8ByteBoundaries(text)
	byteRange, ok := control.findByteExactWithBoundaries(
		text,
		0,
		boundaries,
		make(map[wildcardByteState]struct{}),
	)
	if !ok || text[byteRange.start:byteRange.end] != "deployment" {
		t.Fatalf("byte-exact non-strict control = %#v, ok = %t", byteRange, ok)
	}
}

func TestTermMatcherEmptyTermsNeverMatch(t *testing.T) {
	for _, term := range []string{"", " ", "\t\r\n"} {
		matcher := compileTermMatcher(term, PartialModeContains, true)
		if matcher.matches("anything") || matcher.compatibilityRegex().MatchString("anything") {
			t.Errorf("empty term %q matched text", term)
		}
		if got := FindTermIndexCI("anything", term, PartialModeContains); got != nil {
			t.Errorf("FindTermIndexCI(%q) = %v, want nil", term, got)
		}
	}
}

func TestTermMatcherWildcardRejectsLeadingUnderscore(t *testing.T) {
	text := "_deployment"
	matcher := compileTermMatcher("deploy*", PartialModeOff, true)
	if matcher.matches(text) {
		t.Fatal("valid wildcard treated underscore as a leading boundary")
	}
	if matcher.compatibilityRegex().MatchString(text) {
		t.Fatal("wildcard compatibility regex treated underscore as a leading boundary")
	}
}

func TestTermMatcherEdgeQuestionSuppressesBoundary(t *testing.T) {
	tests := []struct {
		name string
		term string
		text string
		want string
	}{
		{
			name: "leading question suppresses leading boundary",
			term: "?Option",
			text: "prefixxOption",
			want: "xOption",
		},
		{
			name: "trailing question suppresses trailing boundary",
			term: "Option?",
			text: "OptionXsuffix",
			want: "OptionX",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matcher := compileTermMatcher(tt.term, PartialModeOff, true)
			found, ok := matcher.find(tt.text, 0)
			if !ok || tt.text[found.start:found.end] != tt.want {
				t.Fatalf("matcher range = %#v, ok = %t, want %q", found, ok, tt.want)
			}
			indexes := matcher.compatibilityRegex().FindStringSubmatchIndex(tt.text)
			if indexes == nil {
				t.Fatal("compatibility regex did not match")
			}
			start, end := submatchRange(indexes)
			if got := tt.text[start:end]; got != tt.want {
				t.Fatalf("compatibility range = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTermMatcherUnicodeLiteralDoesNotUndercountMaxBytes(t *testing.T) {
	term := "é*api"
	text := "é" + strings.Repeat("x", maxWildcardExpansion) + "API"
	matcher := compileTermMatcher(term, PartialModeOff, true)
	found, ok := matcher.find(text, 0)
	if !ok {
		t.Fatal("wildcard with a Unicode literal did not match its exact literal bytes")
	}
	if got := found.end - found.start; got > matcher.maxMatchBytes {
		t.Fatalf("matched bytes = %d, maxMatchBytes = %d", got, matcher.maxMatchBytes)
	}
	if matcher.maxMatchBytes != len(text) {
		t.Errorf("maxMatchBytes = %d, want %d", matcher.maxMatchBytes, len(text))
	}
}

func TestTermMatcherCompilationIsCached(t *testing.T) {
	first := compileTermMatcher("ms-*-api", PartialModeOff, true)
	second := compileTermMatcher("ms-*-api", PartialModeOff, true)
	if first.core != second.core {
		t.Fatal("compileTermMatcher did not reuse its compiled expression")
	}

	firstCompatibility := buildTermRegexCI("ms-*-api", PartialModeOff)
	for i := 0; i <= wordCacheMaxSize; i++ {
		getWordRegex("cache_churn_" + decimalString(i))
	}
	secondCompatibility := buildTermRegexCI("ms-*-api", PartialModeOff)
	if firstCompatibility != secondCompatibility {
		t.Fatal("compatibility wrapper did not reuse its compiled expression")
	}
}

func TestTermMatcherEdgeStarsDoNotExpandSelectedRange(t *testing.T) {
	longPrefix := strings.Repeat("x", maxWildcardExpansion+73)
	text := longPrefix + "defaultMsOptionApiOrigin"
	wantStart := strings.Index(text, "OptionApi")
	wantEnd := wantStart + len("OptionApi")

	matcher := compileTermMatcher("*OptionApi*", PartialModeOff, true)
	found, ok := matcher.find(text, 0)
	if !ok {
		t.Fatal("edge-star wildcard did not match after a prefix longer than 128 bytes")
	}
	if found != (matchRange{start: wantStart, end: wantEnd}) {
		t.Fatalf("matcher range = %#v, want [%d:%d]", found, wantStart, wantEnd)
	}

	indexes := matcher.compatibilityRegex().FindStringSubmatchIndex(text)
	start, end := submatchRange(indexes)
	if start != wantStart || end != wantEnd {
		t.Fatalf("compatibility range = [%d:%d], want [%d:%d]", start, end, wantStart, wantEnd)
	}
	if matcher.maxMatchBytes != len("OptionApi") || matcher.detectionBytes != len("OptionApi") {
		t.Fatalf("bounds = match:%d detection:%d, want %d", matcher.maxMatchBytes, matcher.detectionBytes, len("OptionApi"))
	}
}

func TestTermMatcherInternalStarRemainsInSelectedRange(t *testing.T) {
	text := "before ms-option-api after"
	matcher := compileTermMatcher("*ms-*-api*", PartialModeOff, true)
	found, ok := matcher.find(text, 0)
	if !ok {
		t.Fatal("wildcard did not match")
	}
	if got := text[found.start:found.end]; got != "ms-option-api" {
		t.Fatalf("matched %q, want ms-option-api", got)
	}
}

func TestTermMatcherPrefixUsesSeparateDetectionBound(t *testing.T) {
	suffix := strings.Repeat("x", 1024)
	matcher := compileTermMatcher("deploy", PartialModePrefix, true)
	found, ok := matcher.find("deploy"+suffix, 0)
	if !ok || found.end-found.start != len("deploy")+len(suffix) {
		t.Fatalf("prefix range = %#v, want unbounded consumed suffix", found)
	}
	if matcher.maxMatchBytes != -1 {
		t.Errorf("maxMatchBytes = %d, want -1 for unbounded range", matcher.maxMatchBytes)
	}
	if matcher.detectionBytes != len("deploy") {
		t.Errorf("detectionBytes = %d, want %d", matcher.detectionBytes, len("deploy"))
	}

	query := compileQuery([]string{"deploy"}, PartialModePrefix)
	if query.maxMatchBytes != -1 || query.maxDetectionBytes != len("deploy") {
		t.Errorf("query bounds = match:%d detection:%d", query.maxMatchBytes, query.maxDetectionBytes)
	}
}

func TestTermMatcherPrefixSkipsLongInternalWordRun(t *testing.T) {
	internal := strings.Repeat("xabc", 1<<15)
	text := internal + ".abc_tail"
	matcher := compileTermMatcher("abc", PartialModePrefix, true)

	found, ok := matcher.find(text, 0)
	if !ok {
		t.Fatal("prefix matcher did not find the boundary-qualified suffix")
	}
	if got := text[found.start:found.end]; got != "abc_tail" {
		t.Fatalf("prefix matcher selected %q, want abc_tail", got)
	}
	if found.start != len(internal)+1 {
		t.Fatalf("prefix start = %d, want %d", found.start, len(internal)+1)
	}

	underscore := matcher.findAll("_abc_tail")
	if len(underscore) != 1 || underscore[0] != (matchRange{start: 1, end: len("_abc_tail")}) {
		t.Fatalf("underscore prefix ranges = %#v", underscore)
	}
}

func TestNormalizeQueryTermsUsesUnicodeCaseFold(t *testing.T) {
	got := NormalizeQueryTerms([]string{"MS-**-API", "ms-*-api", "É*API", "é*api"})
	want := []string{"ms-*-api", "é*api"}
	if len(got) != len(want) {
		t.Fatalf("NormalizeQueryTerms() = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("term %d = %q, want %q", i, got[i], want[i])
		}
	}
	if !compileTermMatcher("service", PartialModeOff, true).matches("SERVICES") {
		t.Error("case folding did not include a legacy inflection suffix")
	}
}

func TestNormalizeQueryTermsKeepsLowercaseInsideSimpleFoldOrbit(t *testing.T) {
	const dottedCapitalI = "\u0130"
	got := NormalizeQueryTerms([]string{dottedCapitalI, "API"})
	want := []string{dottedCapitalI, "api"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("NormalizeQueryTerms() = %q, want %q", got, want)
	}

	query := compileQuery([]string{dottedCapitalI}, PartialModeOff)
	if len(query.matchers) != 1 || !query.matchers[0].matches(dottedCapitalI) {
		t.Fatal("default matcher lost U+0130 during normalization")
	}
}

func TestTermMatcherUnicodeCaseFoldAcrossPaths(t *testing.T) {
	for _, term := range []string{"service", "skate"} {
		if matcher := compileTermMatcher(term, PartialModeOff, true); matcher.byteExact {
			t.Errorf("common term %q did not remain on the RE2 path", term)
		}
	}

	plain := compileTermMatcher("CAFÉ", PartialModeOff, true)
	if !plain.matches("café") {
		t.Fatal("regex matcher did not fold uppercase non-ASCII literals")
	}
	if !plain.compatibilityRegex().MatchString("café") {
		t.Fatal("compatibility regex did not fold uppercase non-ASCII literals")
	}

	wildcard := compileTermMatcher("É?x", PartialModeOff, true)
	if found, ok := wildcard.find("éax", 0); !ok || found != (matchRange{start: 0, end: len("éax")}) {
		t.Fatalf("Unicode wildcard range = %#v, ok = %t", found, ok)
	}

	kelvin := compileTermMatcher("K?x", PartialModeOff, true)
	kelvinText := "Kax"
	found, ok := kelvin.find(kelvinText, 0)
	if !ok || found != (matchRange{start: 0, end: len(kelvinText)}) {
		t.Fatalf("Kelvin fold range = %#v, ok = %t", found, ok)
	}
	if kelvin.byteExact {
		t.Fatal("valid Kelvin-fold term did not remain on the RE2 path")
	}
	indexes := kelvin.compatibilityRegex().FindStringSubmatchIndex(kelvinText)
	if indexes == nil {
		t.Fatal("Kelvin fold did not match through compatibility RE2")
	}
	start, end := submatchRange(indexes)
	if start != 0 || end != len(kelvinText) {
		t.Fatalf("Kelvin compatibility range = [%d:%d], want [0:%d]", start, end, len(kelvinText))
	}
	if kelvin.maxMatchBytes != len(kelvinText) || kelvin.detectionBytes != len(kelvinText) {
		t.Fatalf("Kelvin bounds = match:%d detection:%d, want %d", kelvin.maxMatchBytes, kelvin.detectionBytes, len(kelvinText))
	}

	if !CheckTextContainsAllWordsPartial("CAFÉ service", []string{"café", "SERVICE"}, 20, PartialModeOff) {
		t.Fatal("Stage 2 helper did not preserve Unicode case-insensitive matching")
	}

	normalized := NormalizeQueryTerms([]string{"K?x", "K?X"})
	if len(normalized) != 1 {
		t.Fatalf("Kelvin simple-fold terms were not deduplicated: %q", normalized)
	}
}

func TestTermMatcherByteExactUnicodeCaseFold(t *testing.T) {
	matcher := compileTermMatcher("\uFFFDÉ", PartialModeOff, true)
	text := "\uFFFDé"
	found, ok := matcher.find(text, 0)
	if !ok || found != (matchRange{start: 0, end: len(text)}) {
		t.Fatalf("byte-exact Unicode fold range = %#v, ok = %t", found, ok)
	}
	if matcher.maxMatchBytes != len(text) || matcher.detectionBytes != len(text) {
		t.Fatalf("byte-exact Unicode bounds = match:%d detection:%d, want %d", matcher.maxMatchBytes, matcher.detectionBytes, len(text))
	}
}

func TestTermMatcherWildcardByteMemoizationPath(t *testing.T) {
	matcher := compileTermMatcher("\uFFFDa*a*a*a*z", PartialModeOff, true)
	text := "\uFFFD" + strings.Repeat("a", 120) + "y"
	if matcher.matches(text) {
		t.Fatal("four-star byte matcher unexpectedly matched adversarial text")
	}
}

func TestTermMatcherPreservesInvalidUTF8Bytes(t *testing.T) {
	term := string([]byte{0xff, 'A', '*', '*', '?', 'b'})
	canonical := string([]byte{0xff, 'A', '*', '?', 'b'})
	matcher := compileTermMatcher(term, PartialModeOff, true)
	if matcher.term != canonical {
		t.Fatalf("canonical bytes = %v, want %v", []byte(matcher.term), []byte(canonical))
	}
	if !matcher.validWildcard {
		t.Fatal("byte-safe term did not qualify as a valid wildcard")
	}
	text := string([]byte{0xff, 'a', 'X', 'Y', 'B'})
	found, ok := matcher.find(text, 0)
	if !ok || found != (matchRange{start: 0, end: len(text)}) {
		t.Fatalf("invalid UTF-8 range = %#v, ok = %t", found, ok)
	}
	for name, rejected := range map[string]string{
		"different invalid byte": string([]byte{0xfe, 'a', 'X', 'Y', 'B'}),
		"literal replacement":    "\uFFFDaXYB",
	} {
		if matcher.matches(rejected) {
			t.Errorf("matched %s bytes %v", name, []byte(rejected))
		}
		if matcher.compatibilityRegex().MatchString(rejected) {
			t.Errorf("compatibility regex produced an inexact %s match", name)
		}
		if matcher.core.MatchString(rejected) {
			t.Errorf("core regex produced an inexact %s match", name)
		}
	}

	maxText := string([]byte{0xff, 'a'}) + strings.Repeat("x", maxWildcardExpansion) + "YB"
	maxRange, ok := matcher.find(maxText, 0)
	if !ok || maxRange.end-maxRange.start != matcher.maxMatchBytes {
		t.Fatalf("maximum range = %#v, bound = %d, ok = %t", maxRange, matcher.maxMatchBytes, ok)
	}

	duplicate := string([]byte{0xff, 'a', '*', '?', 'B'})
	normalized := NormalizeQueryTerms([]string{term, duplicate})
	normalizedCanonical := string([]byte{0xff, 'a', '*', '?', 'b'})
	if len(normalized) != 1 || normalized[0] != normalizedCanonical {
		t.Fatalf("normalized invalid UTF-8 terms = %v", normalized)
	}
}

func TestTermMatcherInvalidByteDoesNotRestartMidRune(t *testing.T) {
	term := string([]byte{0xbf, 'A', '*', '?', 'b'})
	matcher := compileTermMatcher(term, PartialModeOff, true)
	text := "\u00FFaXYb" // U+00FF ends with byte 0xbf.
	if matcher.matches(text) {
		t.Fatalf("invalid byte term restarted inside valid UTF-8 bytes %v", []byte(text))
	}
}

func TestTermMatcherReplacementRuneUsesByteExactPath(t *testing.T) {
	matcher := compileTermMatcher("\uFFFDA*?b", PartialModeOff, true)
	if !matcher.byteExact {
		t.Fatal("literal U+FFFD term did not use byte-exact matching")
	}
	if !matcher.matches("\uFFFDaXYB") {
		t.Fatal("literal U+FFFD term did not match its exact UTF-8 bytes")
	}
	if matcher.matches(string([]byte{0xff, 'a', 'X', 'Y', 'B'})) {
		t.Fatal("literal U+FFFD term aliased an invalid input byte")
	}

	runeOnly := compileTermMatcher("\uFFFD", PartialModeOff, true)
	if _, ok := runeOnly.find("\uFFFD", 1); ok {
		t.Fatal("literal U+FFFD matcher restarted inside its UTF-8 encoding")
	}
}

func TestTermMatcherOffDetectionIncludesInflectionSuffix(t *testing.T) {
	matcher := compileTermMatcher("process", PartialModeOff, true)
	wantDetection := widestFoldedLiteralBytes("process", true, false) + len("es")
	if matcher.detectionBytes != wantDetection {
		t.Fatalf("detectionBytes = %d, want %d", matcher.detectionBytes, wantDetection)
	}

	prefix := strings.Repeat("z", 64)
	text := prefix + ".processes rest"
	split := len(prefix) + len(".proces")
	windowStart := max(0, split-(matcher.detectionBytes+1))
	if !matcher.matches(text[windowStart:]) {
		t.Fatal("suffix match crossing a chunk boundary was not detected")
	}

	smart := compileTermMatcherWithOptions("loc", PartialModeOff, QueryOptions{SmartForms: true})
	if smart.detectionBytes != len("location") {
		t.Fatalf("smart detectionBytes = %d, want %d", smart.detectionBytes, len("location"))
	}
	smartText := prefix + ".location rest"
	smartSplit := len(prefix) + len(".locat")
	smartStart := max(0, smartSplit-(smart.detectionBytes+1))
	if !smart.matches(smartText[smartStart:]) {
		t.Fatal("smart suffix match crossing a chunk boundary was not detected")
	}
}

func TestTermMatcherDetectionRetainsLeadingBoundaryContext(t *testing.T) {
	prefix := strings.Repeat("z", 64)
	matcher := compileTermMatcher("service", PartialModeOff, true)

	rejected := prefix + "xservice rest"
	split := len(prefix) + len("xserv")
	windowStart := max(0, split-(matcher.detectionBytes+1))
	if matcher.matches(rejected[windowStart:]) {
		t.Fatal("cross-boundary window lost the rejecting leading word byte")
	}

	prefixMatcher := compileTermMatcher("deploy", PartialModePrefix, true)
	accepted := prefix + ".deployment"
	prefixSplit := len(prefix) + len(".dep")
	prefixStart := max(0, prefixSplit-(prefixMatcher.detectionBytes+1))
	found, ok := prefixMatcher.find(accepted[prefixStart:], 0)
	if !ok || accepted[prefixStart+found.start:prefixStart+found.end] != "deployment" {
		t.Fatalf("prefix match crossing a chunk boundary = %#v, ok = %t", found, ok)
	}
	if prefixMatcher.detectionBytes != len("deploy") {
		t.Fatalf("prefix detectionBytes = %d, want base length %d", prefixMatcher.detectionBytes, len("deploy"))
	}
}

func TestTermMatcherFindAllReusesByteBoundaries(t *testing.T) {
	term := string([]byte{0xff, 'a'})
	text := string([]byte{0xff, 'a', ' ', 0xff, 'A'})
	ranges := compileTermMatcher(term, PartialModeOff, true).findAll(text)
	if len(ranges) != 2 {
		t.Fatalf("findAll() returned %d ranges, want 2", len(ranges))
	}

	wildcardTerm := string([]byte{0xff, 'A', '*', '?', 'B'})
	wildcardText := string([]byte{
		0xff, 'a', 'X', 'Y', 'b',
		' ',
		0xff, 'A', 'Z', 'Q', 'B',
	})
	wildcardRanges := compileTermMatcher(wildcardTerm, PartialModeOff, true).findAll(wildcardText)
	want := []matchRange{{start: 0, end: 5}, {start: 6, end: 11}}
	if len(wildcardRanges) != len(want) {
		t.Fatalf("wildcard findAll() returned %#v, want %#v", wildcardRanges, want)
	}
	for i := range want {
		if wildcardRanges[i] != want[i] {
			t.Errorf("wildcard range %d = %#v, want %#v", i, wildcardRanges[i], want[i])
		}
	}
}
