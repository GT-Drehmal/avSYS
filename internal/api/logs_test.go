package api

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// Only the game's own logs and crash reports, by plain name: nothing that
// leads out of the two folders.
func TestLogName(t *testing.T) {
	for _, name := range []string{"logs/latest.log", "logs/2026-10-14-1.log.gz", "crash-reports/crash-2026-10-14_12.00.00-server.txt"} {
		if !logName.MatchString(name) {
			t.Errorf("%s was refused", name)
		}
	}
	for _, name := range []string{"server.properties", "logs/../server.properties", "logs/sub/latest.log", "/logs/latest.log", "crash-reports/x.log", "logs/latest.txt", "logs/.."} {
		if logName.MatchString(name) {
			t.Errorf("%s was taken", name)
		}
	}
}

func TestReadLog(t *testing.T) {
	text := "[12:00:00] [Server thread/INFO]: Starting\n[12:00:05] [Server thread/INFO]: Done (5.0s)!\n"
	lines, cut, err := readLog(strings.NewReader(text), false)
	if err != nil || cut || len(lines) != 2 || lines[1] != "[12:00:05] [Server thread/INFO]: Done (5.0s)!" {
		t.Errorf("plain: %q %v %v", lines, cut, err)
	}
	var packed bytes.Buffer
	z := gzip.NewWriter(&packed)
	z.Write([]byte(text))
	z.Close()
	lines, _, err = readLog(&packed, true)
	if err != nil || len(lines) != 2 {
		t.Errorf("gzipped: %q %v", lines, err)
	}
	if _, _, err := readLog(strings.NewReader("not gzip"), true); err == nil {
		t.Error("a file that is not gzip was read as one")
	}
}

// A search keeps what matches, ignoring case, with lines around it, and says
// which line it is.
func TestSearchLog(t *testing.T) {
	var lines []string
	for i := 1; i <= 20; i++ {
		lines = append(lines, fmt.Sprintf("line %d", i))
	}
	lines[9] = "[Server thread/ERROR]: Something broke"
	got, capped := searchLog(lines, "error", 2)
	if len(got) != 5 || got[0].N != 8 || got[2].N != 10 || !got[2].Match || got[0].Match || capped {
		t.Errorf("got %+v", got)
	}
	// Nothing found is an empty list, not null, which the page would trip on.
	got, _ = searchLog(lines, "nothing like this", 2)
	if b, _ := json.Marshal(got); string(b) != "[]" {
		t.Errorf("no match: %s", b)
	}
	// A search that matches nearly everything answers with the last matches only.
	many := make([]string, maxMatches+500)
	for i := range many {
		many[i] = fmt.Sprintf("[INFO] %d", i+1)
	}
	got, capped = searchLog(many, "info", 2)
	// The kept matches, from line 501, and the two lines before the first.
	if !capped || len(got) != maxMatches+2 || got[2].N != 501 || !got[2].Match || got[1].Match || got[len(got)-1].N != len(many) {
		t.Errorf("many matches: %d lines from %d, capped %v", len(got), got[0].N, capped)
	}
}

// A line too long to show is cut, and what comes after it still read.
func TestReadLogLongLine(t *testing.T) {
	text := "before\n" + strings.Repeat("x", maxLineBytes+5000) + "\n\nafter"
	lines, cut, err := readLog(strings.NewReader(text), false)
	if err != nil || cut || len(lines) != 4 || lines[3] != "after" || lines[2] != "" {
		t.Fatalf("%d lines, cut %v, err %v", len(lines), cut, err)
	}
	if !strings.HasSuffix(lines[1], " … (5000 more bytes on this line)") || !strings.HasPrefix(lines[1], strings.Repeat("x", maxLineBytes)) {
		t.Errorf("the long line ends %q", lines[1][len(lines[1])-60:])
	}
}

// The first error is found in the whole log, not only the page shown, and a
// page can be read going forward from it.
func TestFirstErrorAndPageFrom(t *testing.T) {
	lines := make([]string, 3*logPage)
	for i := range lines {
		lines[i] = fmt.Sprintf("[Server thread/INFO]: %d", i+1)
	}
	lines[41] = "java.lang.IllegalStateException: no"
	lines[2*logPage] = "[Server thread/ERROR]: later"
	if n := firstError(lines); n != 42 {
		t.Errorf("first error at %d", n)
	}
	if n := firstError(lines[:40]); n != 0 {
		t.Errorf("no error, but found one at %d", n)
	}
	page := pageFrom(lines, 37)
	if len(page) != logPage || page[0].N != 37 || page[len(page)-1].N != 37+logPage-1 {
		t.Errorf("from 37: %d lines, %d to %d", len(page), page[0].N, page[len(page)-1].N)
	}
	if page := pageFrom(lines, len(lines)-10); len(page) != 11 || page[10].N != len(lines) {
		t.Errorf("near the end: %d lines", len(page))
	}
}

// Logs are read from their end, a page at a time, going back.
func TestPageOf(t *testing.T) {
	lines := make([]string, logPage+500)
	for i := range lines {
		lines[i] = fmt.Sprint(i + 1)
	}
	page, start := pageOf(lines, 0)
	if len(page) != logPage || page[len(page)-1].N != len(lines) || start != 500 {
		t.Fatalf("last page: %d lines ending at %d, starting after %d", len(page), page[len(page)-1].N, start)
	}
	page, start = pageOf(lines, page[0].N)
	if len(page) != 500 || page[0].N != 1 || page[len(page)-1].N != 500 || start != 0 {
		t.Errorf("the page before: %d lines, %d to %d, start %d", len(page), page[0].N, page[len(page)-1].N, start)
	}
}

func TestLogOrder(t *testing.T) {
	day := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	entries := []logEntry{
		{Name: "logs/2026-10-13-1.log.gz", Kind: "log", ModTime: day.Add(-24 * time.Hour)},
		{Name: "crash-reports/crash-a.txt", Kind: "crash", ModTime: day.Add(2 * time.Hour)},
		{Name: "logs/latest.log", Kind: "current", ModTime: day},
		{Name: "logs/2026-10-14-2.log.gz", Kind: "log", ModTime: day.Add(time.Hour)},
	}
	logOrder(entries)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name)
	}
	want := "logs/latest.log crash-reports/crash-a.txt logs/2026-10-14-2.log.gz logs/2026-10-13-1.log.gz"
	if strings.Join(names, " ") != want {
		t.Errorf("order: %v", names)
	}
}
