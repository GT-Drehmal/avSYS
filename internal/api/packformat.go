package api

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
	"github.com/GT-Drehmal/avSYS/internal/store"
)

// A resource pack says which pack formats it is made for, and each game
// version reads one format. A pack made for another loads all the same, but
// what changed between them, such as shaders and the names of models, may
// not work: the panel says so rather than refusing it.

// formatsOfMeta reads the formats a pack.mcmeta says its pack is made for,
// or nil if it says none it can read. Over the years a pack has said so as
// pack_format, as supported_formats (a number, [min, max], or
// {min_inclusive, max_inclusive}), and from the game's 1.21.9 on as
// min_format and max_format (a number, [major] or [major, minor]). The
// range is of every format it names.
func formatsOfMeta(data []byte) *avsysv1.PackFormats {
	var meta struct {
		Pack struct {
			Format    json.RawMessage `json:"pack_format"`
			Supported json.RawMessage `json:"supported_formats"`
			MinFormat json.RawMessage `json:"min_format"`
			MaxFormat json.RawMessage `json:"max_format"`
		} `json:"pack"`
	}
	if json.Unmarshal(data, &meta) != nil {
		return nil
	}
	var out *avsysv1.PackFormats
	add := func(n int) {
		// Formats are small numbers: anything else is not one.
		if n < 1 || n > maxPackFormat {
			return
		}
		if out == nil {
			out = &avsysv1.PackFormats{Min: int32(n), Max: int32(n)}
		}
		out.Min, out.Max = min(out.Min, int32(n)), max(out.Max, int32(n))
	}
	p := meta.Pack
	if n, ok := formatNumber(p.Format); ok {
		add(n)
	}
	if n, ok := formatNumber(p.Supported); ok {
		add(n)
	} else {
		var pair []json.RawMessage
		var bounds struct {
			Min json.RawMessage `json:"min_inclusive"`
			Max json.RawMessage `json:"max_inclusive"`
		}
		if json.Unmarshal(p.Supported, &pair) == nil && len(pair) == 2 {
			for _, v := range pair {
				if n, ok := formatNumber(v); ok {
					add(n)
				}
			}
		} else if json.Unmarshal(p.Supported, &bounds) == nil {
			for _, v := range []json.RawMessage{bounds.Min, bounds.Max} {
				if n, ok := formatNumber(v); ok {
					add(n)
				}
			}
		}
	}
	for _, v := range []json.RawMessage{p.MinFormat, p.MaxFormat} {
		if n, ok := formatNumber(v); ok {
			add(n)
			continue
		}
		// [major, minor]: formats are told apart here by their major.
		var parts []json.RawMessage
		if json.Unmarshal(v, &parts) == nil && len(parts) > 0 {
			if n, ok := formatNumber(parts[0]); ok {
				add(n)
			}
		}
	}
	return out
}

// maxPackFormat is past any format there will be for a long time: a number
// larger is not one.
const maxPackFormat = 10_000

// formatNumber reads a whole number, as JSON has it.
func formatNumber(v json.RawMessage) (int, bool) {
	var f float64
	if len(v) == 0 || json.Unmarshal(v, &f) != nil || f != float64(int(f)) {
		return 0, false
	}
	return int(f), true
}

// checkPackFormats checks the formats a pack is said to be made for, and
// lets go of those of the one before (was) when it is another pack now.
func checkPackFormats(rp, was *avsysv1.ResourcePackSpec) error {
	if rp == nil || rp.Formats == nil {
		return nil
	}
	if f := rp.Formats; f.Min < 1 || f.Max < f.Min || f.Max > maxPackFormat {
		return errBadRequest("A resource pack's formats are from 1 up, the lowest first.")
	}
	if was != nil && was.Formats != nil && rp.URL != was.URL && *rp.Formats == *was.Formats {
		rp.Formats = nil
	}
	return nil
}

// oldFormats are the resource pack formats of the game versions whose
// server does not say: those before 1.14, the first whose jar has a
// version.json.
var oldFormats = []struct {
	minor  int
	format int32
}{{13, 4}, {11, 3}, {9, 2}, {6, 1}}

// packFormatOf is the pack format a game version reads, from what its own
// server says, or from oldFormats for one too old to say.
func (s *Server) packFormatOf(ctx context.Context, version string) (int32, error) {
	if f, ok := s.gameFormats.Load(version); ok {
		return f.(int32), nil
	}
	// One look at a time: each is a few requests to Mojang, and a version's
	// format never changes once found out.
	s.gameFormatLock.Lock()
	defer s.gameFormatLock.Unlock()
	if f, ok := s.gameFormats.Load(version); ok {
		return f.(int32), nil
	}
	f, err := s.readPackFormat(ctx, version)
	if err != nil {
		return 0, err
	}
	s.gameFormats.Store(version, f)
	return f, nil
}

var errNoFormat = errors.New("the game version does not say its pack format")

func (s *Server) readPackFormat(ctx context.Context, version string) (int32, error) {
	body, err := s.catalog.get(ctx, mojangManifest, time.Hour)
	if err != nil {
		return 0, err
	}
	var list struct {
		Versions []struct {
			ID  string `json:"id"`
			URL string `json:"url"`
		} `json:"versions"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return 0, err
	}
	at := ""
	for _, v := range list.Versions {
		if v.ID == version {
			at = v.URL
		}
	}
	if at == "" {
		return 0, errNotFound
	}
	body, err = s.catalog.get(ctx, at, time.Hour)
	if err != nil {
		return 0, err
	}
	var meta struct {
		Downloads struct {
			Server struct {
				URL  string `json:"url"`
				Size int64  `json:"size"`
			} `json:"server"`
		} `json:"downloads"`
	}
	if err := json.Unmarshal(body, &meta); err != nil {
		return 0, err
	}
	if jar := meta.Downloads.Server; jar.URL != "" && jar.Size > 0 {
		f, err := s.jarPackFormat(ctx, jar.URL, jar.Size)
		if err == nil || !errors.Is(err, errNoFormat) {
			return f, err
		}
	}
	if m := regexp.MustCompile(`^1\.(\d+)(\.|$)`).FindStringSubmatch(version); m != nil {
		minor, _ := strconv.Atoi(m[1])
		for _, o := range oldFormats {
			if minor >= o.minor && minor < 14 {
				return o.format, nil
			}
		}
	}
	return 0, errNoFormat
}

// jarPackFormat reads the version.json in a server jar for its pack format,
// reading only the parts of the jar it needs: its directory at the end and
// that one file. A version says it as a number (1.14 to 1.16), as
// {"resource": n} (1.17 on), or as {"resource_major": n, ...} (1.21.9 on).
func (s *Server) jarPackFormat(ctx context.Context, url string, size int64) (int32, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	z, err := zip.NewReader(&rangeReader{ctx: ctx, http: s.catalog.http, agent: s.catalog.userAgent, url: url, size: size}, size)
	if err != nil {
		return 0, err
	}
	for _, e := range z.File {
		if e.Name != "version.json" {
			continue
		}
		f, err := e.Open()
		if err != nil {
			return 0, err
		}
		data, err := io.ReadAll(io.LimitReader(f, 1<<20))
		f.Close()
		if err != nil {
			return 0, err
		}
		var v struct {
			Pack json.RawMessage `json:"pack_version"`
		}
		if err := json.Unmarshal(data, &v); err != nil {
			return 0, err
		}
		if n, ok := formatNumber(v.Pack); ok && n > 0 {
			return int32(n), nil
		}
		var split struct {
			Resource json.RawMessage `json:"resource"`
			Major    json.RawMessage `json:"resource_major"`
		}
		if json.Unmarshal(v.Pack, &split) == nil {
			for _, raw := range []json.RawMessage{split.Major, split.Resource} {
				if n, ok := formatNumber(raw); ok && n > 0 {
					return int32(n), nil
				}
			}
		}
		return 0, errNoFormat
	}
	return 0, errNoFormat
}

// rangeReader reads a file over HTTP a block at a time, as a zip reader asks
// for parts of it, keeping the blocks it has read.
type rangeReader struct {
	ctx   context.Context
	http  *http.Client
	agent string
	url   string
	size  int64

	mu     sync.Mutex
	blocks map[int64][]byte
	asked  int
}

const rangeBlock = 64 << 10

// rangeAsks is the most requests one read of a jar makes: a jar laid out
// as expected takes a handful.
const rangeAsks = 64

func (r *rangeReader) ReadAt(p []byte, off int64) (int, error) {
	if off >= r.size {
		return 0, io.EOF
	}
	n := 0
	for n < len(p) && off+int64(n) < r.size {
		at := off + int64(n)
		b, err := r.block(at / rangeBlock)
		if err != nil {
			return n, err
		}
		n += copy(p[n:], b[at%rangeBlock:])
	}
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func (r *rangeReader) block(i int64) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if b, ok := r.blocks[i]; ok {
		return b, nil
	}
	if r.asked++; r.asked > rangeAsks {
		return nil, errors.New("too many reads of one jar")
	}
	start := i * rangeBlock
	end := min(start+rangeBlock, r.size) - 1
	req, err := http.NewRequestWithContext(r.ctx, http.MethodGet, r.url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", r.agent)
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))
	resp, err := r.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	// Only the part asked for: a whole jar is not read for this.
	if resp.StatusCode != http.StatusPartialContent {
		return nil, fmt.Errorf("%s answered %s to a request for part of it", req.URL.Host, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, rangeBlock+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) != end-start+1 {
		return nil, fmt.Errorf("%s answered %d bytes for %d", req.URL.Host, len(b), end-start+1)
	}
	if r.blocks == nil {
		r.blocks = map[int64][]byte{}
	}
	r.blocks[i] = b
	return b, nil
}

// packFormats says which resource pack formats game versions read: given
// several, as those a pack on Modrinth is listed for, the range from the
// oldest of them to the newest, which are told apart by where they are in
// Mojang's list. "LATEST" is the newest release. Versions the panel cannot
// find out the format of are left out; with none left it answers 404.
func (s *Server) packFormats(w http.ResponseWriter, r *http.Request, _ *store.User) error {
	asked := strings.Split(r.URL.Query().Get("versions"), ",")
	if len(asked) > 200 {
		return errBadRequest("At most 200 versions at a time.")
	}
	m, err := s.manifest(r.Context())
	if err != nil {
		return err
	}
	want := map[string]bool{}
	for _, v := range asked {
		if v == "LATEST" {
			v = m.Latest["release"]
		}
		want[v] = true
	}
	// Newest first: the first and last in the list asked for are the ends.
	var ends []string
	for _, v := range m.Versions {
		if want[v.ID] {
			ends = append(ends, v.ID)
		}
	}
	if len(ends) > 2 {
		ends = []string{ends[0], ends[len(ends)-1]}
	}
	var out *avsysv1.PackFormats
	for _, v := range ends {
		f, err := s.packFormatOf(r.Context(), v)
		if err != nil {
			s.Log.Info("finding out a game version's resource pack format", "version", v, "error", err)
			continue
		}
		if out == nil {
			out = &avsysv1.PackFormats{Min: f, Max: f}
		}
		out.Min, out.Max = min(out.Min, f), max(out.Max, f)
	}
	if out == nil {
		return errNotFound
	}
	w.Header().Set("Cache-Control", "private, max-age=3600")
	writeJSON(w, http.StatusOK, out)
	return nil
}
