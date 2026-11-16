package corpus

import (
	"testing"
)

func TestSelectEntries(t *testing.T) {
	for _, tc := range []struct{ source, want string }{
		{"govdocs-thread0", "a.pdf"}, {"govdocs-docx", "b.docx"},
		{"govdocs-rtf", "c.rtf"}, {"poi-hsmf", "d.msg"},
	} {
		got := selectEntries("docs", []entry{{name: "d.msg"}, {name: "c.rtf"}, {name: "b.docx"}, {name: "a.pdf"}}, "docs", selectionCounts(tc.source))
		if len(got) != 1 || got[0].name != tc.want {
			t.Fatalf("%s selection: %+v", tc.source, got)
		}
	}
	entries := []entry{{name: "c.txt", size: 3}, {name: "a.txt", size: 512 << 10}, {name: "b.txt", size: (512 << 10) + 1}, {name: "d.txt", size: 2}, {name: "target/e.txt", size: 1}}
	got := selectEntries("docs", entries, "", map[string]int{".txt": 2})
	if len(got) != 2 || got[0].name != "a.txt" || got[1].name != "c.txt" {
		t.Fatalf("selection: %+v", got)
	}
	code := selectEntries("code", []entry{{name: "src/a.rs", size: 600 << 10}, {name: "target/b.rs", size: 1}, {name: "image.png", size: 1}}, "ripgrep", nil)
	if len(code) != 1 || code[0].name != "src/a.rs" {
		t.Fatalf("code selection: %+v", code)
	}
	for _, tc := range []struct {
		corpus         string
		count          int
		size, download int64
		valid          bool
	}{
		{"docs", 450, 50_000_000, 90_000_000, true}, {"docs", 451, 1, 1, false},
		{"docs", 1, 50_000_001, 1, false}, {"docs", 1, 1, 90_000_001, false},
		{"code", 1, 10_000_000, 0, true}, {"code", 1, 10_000_001, 0, false},
	} {
		if err := checkBudget(tc.corpus, tc.count, tc.size, tc.download); (err == nil) != tc.valid {
			t.Fatalf("%+v: %v", tc, err)
		}
	}
}
