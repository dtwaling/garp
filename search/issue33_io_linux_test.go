//go:build linux

package search_test

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"garp/search"
	"golang.org/x/sys/unix"
)

// Observe only the fixture inode, not unrelated runtime or ignore-file I/O.
// Alternating OPEN/CLOSE events cannot coalesce across sequential scans. These
// metrics count read sessions, not read(2) calls or physical disk/cache misses.
func issue33Watch(tb testing.TB, path string) func() int {
	tb.Helper()
	fd, err := unix.InotifyInit1(unix.IN_NONBLOCK | unix.IN_CLOEXEC)
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { unix.Close(fd) })
	if _, err := unix.InotifyAddWatch(fd, path, unix.IN_OPEN|unix.IN_CLOSE_NOWRITE); err != nil {
		tb.Fatal(err)
	}
	return func() int {
		tb.Helper()
		opens, closes := 0, 0
		var buf [4096]byte
		for {
			n, err := unix.Read(fd, buf[:])
			if err == unix.EAGAIN {
				break
			}
			if err == unix.EINTR {
				continue
			}
			if err != nil {
				tb.Fatal(err)
			}
			for off := 0; off < n; {
				mask := binary.NativeEndian.Uint32(buf[off+4:])
				length := binary.NativeEndian.Uint32(buf[off+12:])
				if mask&unix.IN_Q_OVERFLOW != 0 {
					tb.Fatal("inotify queue overflow")
				}
				if mask&unix.IN_OPEN != 0 {
					opens++
				}
				if mask&unix.IN_CLOSE_NOWRITE != 0 {
					closes++
				}
				off += unix.SizeofInotifyEvent + int(length)
			}
		}
		if opens != closes {
			tb.Fatalf("unbalanced fixture I/O: opens=%d closes=%d", opens, closes)
		}
		return opens
	}
}

func issue33IOFixtures() map[string][]byte {
	fixtures := encodedBinaryFixtures()
	fixtures["plain.eml"] = []byte("Content-Type: text/plain\r\n\r\nordinary text\r\n")
	fixtures["plain.mbox"] = append([]byte("From sender@example.com Mon Jan 1 00:00:00 2024\n"), fixtures["plain.eml"]...)
	fixtures["plain.rtf"] = []byte(`{\rtf1\ansi ordinary text}`)
	// Convert the real CFB fixture's two text properties from PT_UNICODE to
	// PT_STRING8, retaining its valid directory/FAT and attachment stream.
	ascii := readerAtMSG()
	old := []byte{'0', 0, '0', 0, '1', 0, 'F', 0}
	replacement := []byte{'0', 0, '0', 0, '1', 0, 'E', 0}
	copy(ascii[1024:1536], bytes.ReplaceAll(ascii[1024:1536], old, replacement))
	for start, text := range map[int]string{3 * 512: "ordinary", 11 * 512: "text"} {
		// I/O-only miss fixture: ANSI decoding is not asserted. Supported
		// decoded recall is covered by the unchanged UTF-16LE unicode.msg.
		copy(ascii[start:start+4096], bytes.Repeat([]byte{' '}, 4096))
		copy(ascii[start:start+4096], text)
	}
	fixtures["plain.msg"] = ascii
	return fixtures
}

func TestIssue33ReadSessions(t *testing.T) {
	for name, data := range issue33IOFixtures() {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, name)
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			reads := issue33Watch(t, path)
			ext := filepath.Ext(name)
			found, decided := search.BinaryStreamingPrefilterDecided(path, []string{"missing"}, int64(len(data)))
			wantPrefilter := 1
			if ext == ".msg" {
				wantPrefilter = 0
			}
			if got := reads(); got != wantPrefilter {
				t.Fatalf("prefilter reads=%d want=%d", got, wantPrefilter)
			}
			if ext == ".msg" && (found || decided) {
				t.Fatalf("MSG decision=(%v,%v)", found, decided)
			}
			plain := name[:5] == "plain"
			for _, words := range [][]string{{"subject"}, {"subject", "body"}} {
				matches, readerCalls, err := search.Issue33StagesForTest(issue33Engine(root, words), func(n int) {
					wantCandidates := 1
					if plain && ext != ".msg" {
						wantCandidates = 0
					}
					if n != wantCandidates {
						t.Fatalf("candidates=%d want=%d", n, wantCandidates)
					}
					if got := reads(); got != wantPrefilter {
						t.Fatalf("Stage 1 reads=%d want=%d", got, wantPrefilter)
					}
				})
				wantMatches, wantStage2 := 1, 1
				if plain {
					wantMatches = 0
				}
				if plain && ext != ".msg" {
					wantStage2 = 0
				}
				if !plain && ext != ".msg" && len(words) > 1 {
					wantStage2 = 2
				}
				if err != nil || matches != wantMatches {
					t.Fatalf("matches=%d want=%d err=%v", matches, wantMatches, err)
				}
				if got := reads(); got != wantStage2 {
					t.Fatalf("Stage 2 reads=%d want=%d", got, wantStage2)
				}
				if ext == ".msg" && readerCalls != 1 {
					t.Fatalf("MSG ReaderAt calls=%d", readerCalls)
				}
			}
		})
	}
}

func BenchmarkIssue33IO(b *testing.B) {
	for name, data := range issue33IOFixtures() {
		for _, stage := range []string{"prefilter", "stages12"} {
			b.Run(fmt.Sprintf("%s/%s", name, stage), func(b *testing.B) {
				root := b.TempDir()
				path := filepath.Join(root, name)
				if err := os.WriteFile(path, data, 0600); err != nil {
					b.Fatal(err)
				}
				reads := issue33Watch(b, path)
				engine := issue33Engine(root, []string{"subject"})
				sessions := 0
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					if stage == "prefilter" {
						search.BinaryStreamingPrefilterDecided(path, []string{"subject"}, int64(len(data)))
					} else {
						if _, _, err := search.Issue33StagesForTest(engine, nil); err != nil {
							b.Fatal(err)
						}
					}
					// Drain each iteration to avoid overflow; observation overhead is included.
					sessions += reads()
				}
				b.StopTimer()
				b.ReportMetric(float64(sessions)/float64(b.N), "read-sessions/op")
			})
		}
	}
}
