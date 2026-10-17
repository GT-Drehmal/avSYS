package api

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
	"github.com/GT-Drehmal/avSYS/internal/store"
)

func TestFormatsOfMeta(t *testing.T) {
	for meta, want := range map[string]string{
		// 1.6.1 to 1.20.1: one number.
		`{"pack":{"pack_format":15,"description":"x"}}`: "15-15",
		// 1.20.2 to 1.21.8: a range besides, in any of three forms.
		`{"pack":{"pack_format":18,"supported_formats":[15,22]}}`:                                 "15-22",
		`{"pack":{"pack_format":18,"supported_formats":{"min_inclusive":16,"max_inclusive":34}}}`: "16-34",
		`{"pack":{"pack_format":22,"supported_formats":18}}`:                                      "18-22",
		// 1.21.9 on: min_format and max_format, as [major, minor] or a number.
		`{"pack":{"min_format":[69,0],"max_format":[97,1]}}`:          "69-97",
		`{"pack":{"pack_format":46,"min_format":46,"max_format":97}}`: "46-97",
		`{"pack":{"min_format":75}}`:                                  "75-75",
		// What is not a format is not read as one.
		`{"pack":{"pack_format":"15"}}`:      "none",
		`{"pack":{"pack_format":1.5}}`:       "none",
		`{"pack":{"pack_format":-3}}`:        "none",
		`{"pack":{"pack_format":1e9}}`:       "none",
		`{"pack":{"supported_formats":[1]}}`: "none",
		`{"pack":{"description":"x"}}`:       "none",
		`not json`:                           "none",
	} {
		got := "none"
		if f := formatsOfMeta([]byte(meta)); f != nil {
			got = fmt.Sprintf("%d-%d", f.Min, f.Max)
		}
		if got != want {
			t.Errorf("%s: %s, want %s", meta, got, want)
		}
	}
}

func TestCheckPackFormats(t *testing.T) {
	f := func(lo, hi int32) *avsysv1.PackFormats { return &avsysv1.PackFormats{Min: lo, Max: hi} }
	for _, bad := range []*avsysv1.PackFormats{f(0, 3), f(5, 4), f(1, 20_000)} {
		if err := checkPackFormats(&avsysv1.ResourcePackSpec{URL: "https://example.org/a.zip", Formats: bad}, nil); err == nil {
			t.Errorf("formats %v were taken", *bad)
		}
	}
	// Another pack, given with the formats of the one before, is not known
	// to be made for them.
	was := &avsysv1.ResourcePackSpec{URL: "https://example.org/a.zip", Formats: f(15, 15)}
	other := &avsysv1.ResourcePackSpec{URL: "https://example.org/b.zip", Formats: f(15, 15)}
	if err := checkPackFormats(other, was); err != nil || other.Formats != nil {
		t.Errorf("another pack kept the formats of the one before: %v, %v", other.Formats, err)
	}
	same := &avsysv1.ResourcePackSpec{URL: "https://example.org/a.zip", Formats: f(15, 15), Enforce: true}
	if err := checkPackFormats(same, was); err != nil || same.Formats == nil {
		t.Errorf("the same pack lost its formats: %v", err)
	}
	given := &avsysv1.ResourcePackSpec{URL: "https://example.org/b.zip", Formats: f(46, 46)}
	if err := checkPackFormats(given, was); err != nil || given.Formats == nil {
		t.Errorf("another pack lost the formats given with it: %v", err)
	}
}

// jarWith is a jar of these files, with filler around them as a server jar
// has, so that reading one file is reading a small part of it.
func jarWith(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	z := zip.NewWriter(&buf)
	noise := make([]byte, 1<<20)
	rng := rand.New(rand.NewPCG(1, 2))
	for i := range noise {
		noise[i] = byte(rng.Uint32())
	}
	for i := range 3 {
		w, _ := z.CreateHeader(&zip.FileHeader{Name: fmt.Sprintf("lib/%d.jar", i), Method: zip.Store})
		w.Write(noise)
	}
	for name, body := range files {
		w, _ := z.Create(name)
		io.WriteString(w, body)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// mojang serves a manifest, version files and server jars as Mojang does,
// jars in ranges, and counts what is asked of it.
type mojang struct {
	t      *testing.T
	jars   map[string][]byte
	ranges bool
	asked  atomic.Int32
	srv    *httptest.Server
}

func newMojang(t *testing.T, jars map[string][]byte) *mojang {
	m := &mojang{t: t, jars: jars, ranges: true}
	m.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.asked.Add(1)
		jar, ok := m.jars[strings.TrimPrefix(r.URL.Path, "/jar/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		if !m.ranges {
			r.Header.Del("Range")
		}
		http.ServeContent(w, r, "server.jar", time.Time{}, bytes.NewReader(jar))
	}))
	t.Cleanup(m.srv.Close)
	return m
}

// RoundTrip answers Mojang's manifest and version files itself, and sends
// the jars to its server.
func (m *mojang) RoundTrip(r *http.Request) (*http.Response, error) {
	if strings.HasPrefix(r.URL.String(), m.srv.URL) {
		return http.DefaultTransport.RoundTrip(r)
	}
	var body string
	switch {
	case r.URL.String() == mojangManifest:
		var entries []string
		for _, id := range []string{"26.3", "1.21.4", "1.20.1", "1.14", "1.13.2", "1.8.9", "1.2.5"} {
			entries = append(entries, fmt.Sprintf(`{"id":%q,"type":"release","url":"https://piston-meta.example/%s.json"}`, id, id))
		}
		body = `{"latest":{"release":"26.3"},"versions":[` + strings.Join(entries, ",") + `]}`
	case strings.HasPrefix(r.URL.String(), "https://piston-meta.example/"):
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/"), ".json")
		jar, ok := m.jars[id]
		if !ok {
			// Too old for a server to have been published: none listed.
			body = `{"downloads":{}}`
			break
		}
		body = fmt.Sprintf(`{"downloads":{"server":{"url":"%s/jar/%s","size":%d}}}`, m.srv.URL, id, len(jar))
	default:
		return nil, fmt.Errorf("offline: %s", r.URL)
	}
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
}

// A game version's pack format is read from its own server, a few parts of
// its jar at a time and once only. For a version too old for its server to
// say, it is what it was.
func TestGameVersionPackFormat(t *testing.T) {
	p := newPanel(t)
	up := newMojang(t, map[string][]byte{
		"26.3":   jarWith(t, map[string]string{"version.json": `{"id":"26.3","pack_version":{"resource_major":97,"resource_minor":1,"data_major":101,"data_minor":0}}`}),
		"1.21.4": jarWith(t, map[string]string{"version.json": `{"id":"1.21.4","pack_version":{"resource":46,"data":61}}`}),
		"1.20.1": jarWith(t, map[string]string{"version.json": `{"id":"1.20.1","pack_version":{"resource":15,"data":15}}`}),
		"1.14":   jarWith(t, map[string]string{"version.json": `{"id":"1.14","pack_version":4}`}),
		"1.13.2": jarWith(t, map[string]string{"net/minecraft/server/Main.class": "x"}),
	})
	p.srv.catalog.http.Transport = up
	for versions, want := range map[string]string{
		"26.3":   "97-97",
		"LATEST": "97-97",
		"1.21.4": "46-46",
		"1.14":   "4-4",
		"1.13.2": "4-4",
		"1.8.9":  "1-1",
		// A pack listed for several: from the oldest to the newest.
		"1.20.1,26.3,1.21.4":   "15-97",
		"1.21.4,not-a-version": "46-46",
	} {
		var got avsysv1.PackFormats
		p.fetch("/catalog/minecraft/pack-formats?versions="+versions, &got)
		if s := fmt.Sprintf("%d-%d", got.Min, got.Max); s != want {
			t.Errorf("%s: %s, want %s", versions, s, want)
		}
	}
	// A jar of over 3 MB was read a few blocks at a time, and each version
	// once: the second time is from memory.
	before := up.asked.Load()
	if before > 6*5 {
		t.Errorf("%d requests for 5 jars", before)
	}
	var again avsysv1.PackFormats
	p.fetch("/catalog/minecraft/pack-formats?versions=26.3,1.20.1", &again)
	if up.asked.Load() != before {
		t.Errorf("formats found out before were asked for again")
	}

	// Where none can be found out, it says so.
	for _, versions := range []string{"1.2.5", "nothing", ""} {
		if code, _ := p.do("owner", "GET", "/catalog/minecraft/pack-formats?versions="+versions, ""); code != http.StatusNotFound {
			t.Errorf("%q: %d, want 404", versions, code)
		}
	}
}

// A host that sends the whole jar when asked for a part is not read from:
// that would be the whole jar each time.
func TestGameVersionPackFormatNeedsRanges(t *testing.T) {
	p := newPanel(t)
	up := newMojang(t, map[string][]byte{"1.21.4": jarWith(t, map[string]string{"version.json": `{"pack_version":{"resource":46}}`})})
	up.ranges = false
	p.srv.catalog.http.Transport = up
	if code, _ := p.do("owner", "GET", "/catalog/minecraft/pack-formats?versions=1.21.4", ""); code != http.StatusNotFound {
		t.Errorf("%d, want 404", code)
	}
	if n := up.asked.Load(); n != 1 {
		t.Errorf("%d requests, want 1", n)
	}
	// Not found out is not remembered as found out.
	up.ranges = true
	var got avsysv1.PackFormats
	p.fetch("/catalog/minecraft/pack-formats?versions=1.21.4", &got)
	if got.Min != 46 {
		t.Errorf("then %d, want 46", got.Min)
	}
}

// Of several versions, one whose format cannot be found out gives way to the
// next one in from its end, rather than leaving that end out.
func TestPackFormatsSkipVersionsNotFoundOut(t *testing.T) {
	p := newPanel(t)
	up := newMojang(t, map[string][]byte{
		// 26.3's jar is not there; 1.13.2 is older than any version.json.
		"1.21.4": jarWith(t, map[string]string{"version.json": `{"pack_version":{"resource":46}}`}),
		"1.20.1": jarWith(t, map[string]string{"version.json": `{"pack_version":{"resource":15}}`}),
	})
	p.srv.catalog.http.Transport = up
	var got avsysv1.PackFormats
	p.fetch("/catalog/minecraft/pack-formats?versions=26.3,1.21.4,1.20.1", &got)
	if got.Min != 15 || got.Max != 46 {
		t.Errorf("%d-%d, want 15-46", got.Min, got.Max)
	}
}

// The copy of a pack kept to read its formats from is not what the hash
// depends on: a copy that cannot be written leaves the hash as it was.
func TestBestEffortKeepsGoing(t *testing.T) {
	failing := &bestEffort{w: failAfter(3)}
	n, err := failing.Write([]byte("hello"))
	if n != 5 || err != nil || failing.err == nil {
		t.Errorf("first write: %d %v, kept err %v", n, err, failing.err)
	}
	if n, err := failing.Write([]byte("more")); n != 4 || err != nil {
		t.Errorf("after the failure: %d %v", n, err)
	}
}

type failAfter int

func (f failAfter) Write(p []byte) (int, error) {
	if len(p) > int(f) {
		return int(f), errors.New("disk full")
	}
	return len(p), nil
}

// A copy given another address for its pack, with the original's formats
// still on it, does not keep them: they are not known to be the new pack's.
// The original's own pack, kept, keeps its formats.
func TestCopyPackFormats(t *testing.T) {
	p := newPanel(t)
	org, err := p.srv.Store.OrgBySlug(context.Background(), "alpha")
	if err != nil {
		t.Fatal(err)
	}
	a := &access{Org: org, Role: store.RoleOwner, Namespace: "mc-alpha"}
	formats := &avsysv1.PackFormats{Min: 15, Max: 15}
	recorded := &avsysv1.MinecraftServerSpec{EULA: true, Version: "1.20.1", Loader: avsysv1.LoaderPaper,
		ResourcePack: &avsysv1.ResourcePackSpec{URL: "https://example.org/old.zip", SHA1: strings.Repeat("ab", 20), Formats: formats}}
	given := map[string]json.RawMessage{"resourcePack": json.RawMessage(`{}`)}
	for url, keep := range map[string]bool{"https://example.org/new.zip": false, "https://example.org/old.zip": true} {
		requested := &avsysv1.MinecraftServerSpec{ResourcePack: &avsysv1.ResourcePackSpec{URL: url, Formats: formats.DeepCopy(), Enforce: true}}
		spec, err := p.srv.specFromSnapshot(context.Background(), a, "copy", requested, recorded.DeepCopy(), given)
		if err != nil {
			t.Fatalf("%s: %v", url, err)
		}
		if got := spec.ResourcePack.Formats != nil; got != keep {
			t.Errorf("%s: formats kept %v, want %v", url, got, keep)
		}
	}
}
