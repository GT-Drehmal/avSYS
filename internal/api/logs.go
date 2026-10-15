package api

import (
	"bufio"
	"compress/gzip"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/GT-Drehmal/avSYS/internal/store"
)

// The game keeps its earlier logs itself: logs/latest.log for the run going
// on, logs/YYYY-MM-DD-N.log.gz for each one before, and crash-reports/ for
// the runs that crashed. These are read here for anyone who may see the
// console, from those two folders only.

// logName is a log the panel will read: a plain name in one of the two
// folders, which no path can be made of.
var logName = regexp.MustCompile(`^(logs/[A-Za-z0-9._-]+\.log(\.gz)?|crash-reports/[A-Za-z0-9._-]+\.txt)$`)

// maxLogBytes bounds how much of one log is read, unpacked. A day's log of a
// busy server is a few megabytes.
const maxLogBytes = 64 << 20

// logPage is how many lines one answer holds.
const logPage = 2000

type logEntry struct {
	Name    string    `json:"name"`
	Kind    string    `json:"kind"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"modTime"`
}

// logOrder puts the run going on first, then the rest newest first: by date
// and run number, which the names carry, and by time for crash reports.
func logOrder(entries []logEntry) {
	sort.SliceStable(entries, func(i, j int) bool {
		a, b := entries[i], entries[j]
		if (a.Kind == "current") != (b.Kind == "current") {
			return a.Kind == "current"
		}
		return a.ModTime.After(b.ModTime) || (a.ModTime.Equal(b.ModTime) && a.Name > b.Name)
	})
}

func (s *Server) listLogs(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, mc, err := s.server(r, u, store.RoleViewer)
	if err != nil {
		return err
	}
	sc, err := s.workspaces.get(r.Context(), a.Namespace, mc.Name)
	if err != nil {
		return &Error{Status: http.StatusBadGateway, Code: "workspace", Message: "The server's files are not reachable right now. It may be restarting."}
	}
	entries := []logEntry{}
	for _, dir := range []string{"logs", "crash-reports"} {
		infos, err := sc.ReadDir(path.Join(dataRoot, dir))
		if err != nil {
			continue
		}
		for _, fi := range infos {
			name := dir + "/" + fi.Name()
			if !fi.Mode().IsRegular() || !logName.MatchString(name) {
				continue
			}
			kind := "log"
			switch {
			case dir == "crash-reports":
				kind = "crash"
			case fi.Name() == "latest.log":
				kind = "current"
			}
			entries = append(entries, logEntry{Name: name, Kind: kind, Size: fi.Size(), ModTime: fi.ModTime().UTC()})
		}
	}
	logOrder(entries)
	writeJSON(w, http.StatusOK, map[string]any{"logs": entries})
	return nil
}

type logLine struct {
	N     int    `json:"n"`
	Text  string `json:"text"`
	Match bool   `json:"match,omitempty"`
}

// maxLineBytes is as much of one line as is shown. A longer one, as a
// plugin may write, is cut, and the rest of the log still read.
const maxLineBytes = 64 << 10

// maxMatches is as many matching lines as one search answers with: the last
// ones, nearest the end, where reading starts.
const maxMatches = 1000

// readLog streams a log's lines, unpacked, up to maxLogBytes.
func readLog(f io.Reader, gzipped bool) ([]string, bool, error) {
	if gzipped {
		z, err := gzip.NewReader(f)
		if err != nil {
			return nil, false, err
		}
		defer z.Close()
		f = z
	}
	limited := &io.LimitedReader{R: f, N: maxLogBytes + 1}
	rd := bufio.NewReaderSize(limited, 64<<10)
	var lines []string
	var line []byte
	cut := 0
	for {
		part, more, err := rd.ReadLine()
		if len(part) > 0 || (err == nil && !more) {
			if room := maxLineBytes - len(line); room > 0 {
				line = append(line, part[:min(room, len(part))]...)
				cut += len(part) - min(room, len(part))
			} else {
				cut += len(part)
			}
			if !more {
				text := string(line)
				if cut > 0 {
					text += fmt.Sprintf(" … (%d more bytes on this line)", cut)
				}
				lines = append(lines, text)
				line, cut = line[:0], 0
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, false, err
		}
	}
	return lines, limited.N <= 0, nil
}

// errorLine is a line that tells of something going wrong.
var errorLine = regexp.MustCompile(`\b(ERROR|FATAL)\b|Exception`)

// firstError is the number of the first line that tells of an error, or 0.
func firstError(lines []string) int {
	for i, l := range lines {
		if errorLine.MatchString(l) {
			return i + 1
		}
	}
	return 0
}

// searchLog keeps the lines that hold q, ignoring case, with context lines
// around each. Line numbers start at 1. Of more than maxMatches matches,
// the last are kept, and capped says so.
func searchLog(lines []string, q string, context int) (out []logLine, capped bool) {
	q = strings.ToLower(q)
	var matches []int
	for i, l := range lines {
		if strings.Contains(strings.ToLower(l), q) {
			matches = append(matches, i)
		}
	}
	if len(matches) > maxMatches {
		matches, capped = matches[len(matches)-maxMatches:], true
	}
	keep := make([]bool, len(lines))
	match := make([]bool, len(lines))
	for _, i := range matches {
		match[i] = true
		for j := max(0, i-context); j <= min(len(lines)-1, i+context); j++ {
			keep[j] = true
		}
	}
	out = []logLine{}
	for i, l := range lines {
		if keep[i] {
			out = append(out, logLine{N: i + 1, Text: l, Match: match[i]})
		}
	}
	return out, capped
}

// pageOf is the page of lines ending just before line before (1-based, 0 for
// the end): logs are read from their end, where what happened last is.
func pageOf(lines []string, before int) ([]logLine, int) {
	end := len(lines)
	if before > 0 && before-1 < end {
		end = before - 1
	}
	start := max(0, end-logPage)
	return linesOf(lines, start, end), start
}

// pageFrom is the page of lines starting at line from (1-based), for going
// forward from somewhere in the middle, as from an error.
func pageFrom(lines []string, from int) []logLine {
	start := min(max(0, from-1), len(lines))
	return linesOf(lines, start, min(len(lines), start+logPage))
}

func linesOf(lines []string, start, end int) []logLine {
	out := make([]logLine, 0, end-start)
	for i := start; i < end; i++ {
		out = append(out, logLine{N: i + 1, Text: lines[i]})
	}
	return out
}

func (s *Server) readLogFile(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, mc, err := s.server(r, u, store.RoleViewer)
	if err != nil {
		return err
	}
	q := r.URL.Query()
	name := q.Get("name")
	if !logName.MatchString(name) {
		return errBadRequest("%q is not a log: logs/*.log, logs/*.log.gz or crash-reports/*.txt.", name)
	}
	sc, err := s.workspaces.get(r.Context(), a.Namespace, mc.Name)
	if err != nil {
		return &Error{Status: http.StatusBadGateway, Code: "workspace", Message: "The server's files are not reachable right now. It may be restarting."}
	}
	full := path.Join(dataRoot, name)
	// The folders are the server's, which anyone with a shell there can fill.
	// A link out of them is not followed.
	fi, err := sc.Lstat(full)
	if err != nil {
		return fileError(err)
	}
	if fi.Mode()&fs.ModeSymlink != 0 || !fi.Mode().IsRegular() {
		return errBadRequest("%s is not a plain file.", name)
	}
	f, err := sc.Open(full)
	if err != nil {
		return fileError(err)
	}
	defer f.Close()
	gzipped := strings.HasSuffix(name, ".gz")

	if q.Has("download") {
		file := path.Base(name)
		var body io.Reader = f
		// As it is kept, or unpacked to read.
		if gzipped && q.Get("download") == "text" {
			z, err := gzip.NewReader(f)
			if err != nil {
				return errBadRequest("%s is not a gzip file.", name)
			}
			defer z.Close()
			// All of it: a download is how a log too long to show is read.
			body, file = z, strings.TrimSuffix(file, ".gz")
		}
		w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(file))
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if _, err := io.Copy(w, body); err != nil {
			// The download has begun, and an error said now would be taken
			// as part of the file. Breaking it off makes the browser say it
			// failed instead.
			s.Log.Warn("a log's download broke off", "server", mc.Name, "log", name, "error", err)
			panic(http.ErrAbortHandler)
		}
		return nil
	}

	lines, truncated, err := readLog(f, gzipped)
	if err != nil {
		return errBadRequest("%s could not be read: %v", name, err)
	}
	out := map[string]any{"name": name, "total": len(lines), "truncated": truncated, "firstError": firstError(lines)}
	if search := strings.TrimSpace(q.Get("q")); search != "" {
		found, capped := searchLog(lines, search, 2)
		out["lines"], out["search"], out["capped"] = found, search, capped
	} else if q.Has("from") {
		from, _ := strconv.Atoi(q.Get("from"))
		out["lines"] = pageFrom(lines, from)
	} else {
		before, _ := strconv.Atoi(q.Get("before"))
		page, start := pageOf(lines, before)
		out["lines"], out["more"] = page, start > 0
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}
