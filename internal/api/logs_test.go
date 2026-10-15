package api

import (
	"bytes"
	"compress/gzip"
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
	got := searchLog(lines, "error", 2)
	if len(got) != 5 || got[0].N != 8 || got[2].N != 10 || !got[2].Match || got[0].Match {
		t.Errorf("got %+v", got)
	}
	if got := searchLog(lines, "nothing like this", 2); len(got) != 0 {
		t.Errorf("no match: %+v", got)
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
