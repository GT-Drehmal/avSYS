package api

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
	"github.com/GT-Drehmal/avSYS/internal/controller"
)

// curseForgeStandIn answers as CurseForge's API does, from the answers
// recorded in testdata/curseforge in the shape its documentation gives: the
// projects of each search there, and each project's files. It refuses a
// request without the key it is given, and remembers what it was asked.
type curseForgeStandIn struct {
	t     *testing.T
	key   string
	mods  map[int]json.RawMessage
	files map[int][]json.RawMessage
	// pack is the modpack's own file, served where its file says it is.
	pack []byte

	mu    sync.Mutex
	asked []string
}

func newCurseForgeStandIn(t *testing.T, key string) *curseForgeStandIn {
	t.Helper()
	cf := &curseForgeStandIn{t: t, key: key, mods: map[int]json.RawMessage{}, files: map[int][]json.RawMessage{}}
	paths, _ := filepath.Glob("testdata/curseforge/*.json")
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var list struct{ Data []json.RawMessage }
		if err := json.Unmarshal(data, &list); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		for _, item := range list.Data {
			var ids struct{ ID, ModID int }
			_ = json.Unmarshal(item, &ids)
			switch base := filepath.Base(path); {
			case strings.HasPrefix(base, "search-"):
				cf.mods[ids.ID] = item
			case strings.HasPrefix(base, "files-"):
				cf.files[ids.ModID] = append(cf.files[ids.ModID], item)
			}
		}
	}
	cf.pack = curseForgePack(t, `{"minecraft":{"version":"1.20.1","modLoaders":[{"id":"forge-47.2.0","primary":true}]},"manifestType":"minecraftModpack","manifestVersion":1,"name":"All the Mods 9","version":"0.2.44","author":"ATMTeam",
"files":[{"projectID":238222,"fileID":4593548,"required":true},{"projectID":900001,"fileID":5100001,"required":true}],"overrides":"overrides"}`)
	return cf
}

// curseForgePack is a CurseForge modpack's file: its manifest, and a file of its own.
func curseForgePack(t *testing.T, manifest string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range map[string]string{"manifest.json": manifest, "overrides/config/a.toml": "a = 1\n"} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(body))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func (cf *curseForgeStandIn) count(prefix string) int {
	cf.mu.Lock()
	defer cf.mu.Unlock()
	n := 0
	for _, a := range cf.asked {
		if strings.HasPrefix(a, prefix) {
			n++
		}
	}
	return n
}

func cfAnswer(r *http.Request, status int, body []byte) *http.Response {
	return &http.Response{StatusCode: status, Status: strconv.Itoa(status) + " " + http.StatusText(status), Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(bytes.NewReader(body)), ContentLength: int64(len(body)), Request: r}
}

func (cf *curseForgeStandIn) RoundTrip(r *http.Request) (*http.Response, error) {
	cf.mu.Lock()
	cf.asked = append(cf.asked, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
	cf.mu.Unlock()
	if r.URL.Host == "edge.forgecdn.net" && strings.HasSuffix(r.URL.Path, ".zip") {
		return cfAnswer(r, http.StatusOK, cf.pack), nil
	}
	if r.URL.Host != "api.curseforge.com" {
		return cfAnswer(r, http.StatusNotFound, nil), nil
	}
	if r.Header.Get("x-api-key") != cf.key {
		return cfAnswer(r, http.StatusForbidden, nil), nil
	}
	q := r.URL.Query()
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	data := func(v any) (*http.Response, error) {
		body, _ := json.Marshal(map[string]any{"data": v, "pagination": map[string]int{"index": 0, "pageSize": 50, "totalCount": countOf(v)}})
		return cfAnswer(r, http.StatusOK, body), nil
	}
	switch {
	case r.URL.Path == "/v1/games/432":
		return data(map[string]any{"id": 432, "name": "Minecraft", "slug": "minecraft"})
	case r.URL.Path == "/v1/categories":
		body, _ := os.ReadFile("testdata/curseforge/categories-" + q.Get("classId") + ".json")
		if body == nil {
			body = []byte(`{"data":[]}`)
		}
		return cfAnswer(r, http.StatusOK, body), nil
	case r.URL.Path == "/v1/mods/search":
		class, _ := strconv.Atoi(q.Get("classId"))
		var out []json.RawMessage
		for _, id := range sortedKeys(cf.mods) {
			var m struct {
				Slug    string
				ClassID int
			}
			_ = json.Unmarshal(cf.mods[id], &m)
			if m.ClassID == class && (q.Get("slug") == "" || q.Get("slug") == m.Slug) {
				out = append(out, cf.mods[id])
			}
		}
		return data(out)
	case r.Method == http.MethodPost && r.URL.Path == "/v1/mods":
		var in struct{ ModIDs []int }
		_ = json.NewDecoder(r.Body).Decode(&in)
		var out []json.RawMessage
		for _, id := range in.ModIDs {
			if m, ok := cf.mods[id]; ok {
				out = append(out, m)
			}
		}
		return data(out)
	case r.Method == http.MethodPost && r.URL.Path == "/v1/mods/files":
		var in struct{ FileIDs []int }
		_ = json.NewDecoder(r.Body).Decode(&in)
		var out []json.RawMessage
		for _, files := range cf.files {
			for _, f := range files {
				var ids struct{ ID int }
				_ = json.Unmarshal(f, &ids)
				if slices.Contains(in.FileIDs, ids.ID) {
					out = append(out, f)
				}
			}
		}
		return data(out)
	case len(parts) >= 3 && parts[1] == "mods":
		id, _ := strconv.Atoi(parts[2])
		m, ok := cf.mods[id]
		if !ok {
			return cfAnswer(r, http.StatusNotFound, nil), nil
		}
		if len(parts) == 3 {
			return data(m)
		}
		var out []json.RawMessage
		for _, f := range cf.files[id] {
			var file struct {
				ID           int
				GameVersions []string
			}
			_ = json.Unmarshal(f, &file)
			if len(parts) == 5 {
				if strconv.Itoa(file.ID) == parts[4] {
					return data(f)
				}
				continue
			}
			if v := q.Get("gameVersion"); v != "" && !slices.Contains(file.GameVersions, v) {
				continue
			}
			if l := q.Get("modLoaderType"); l != "" && !slices.Contains(file.GameVersions, map[string]string{"1": "Forge", "4": "Fabric", "5": "Quilt", "6": "NeoForge"}[l]) {
				continue
			}
			out = append(out, f)
		}
		if len(parts) == 5 {
			return cfAnswer(r, http.StatusNotFound, nil), nil
		}
		return data(out)
	}
	return cfAnswer(r, http.StatusNotFound, nil), nil
}

func countOf(v any) int {
	if list, ok := v.([]json.RawMessage); ok {
		return len(list)
	}
	return 1
}

func sortedKeys[V any](m map[int]V) []int {
	var keys []int
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// withCurseForge has the panel installed with a CurseForge key, asking cf.
func (p *panel) withCurseForge(cf *curseForgeStandIn) {
	p.srv.Config.CurseForgeKey = cf.key
	p.srv.catalog.http.Transport = cf
	// What the panel downloads to read, as a mod's Java, is not reached.
	public := publicHTTP
	publicHTTP = &http.Client{Transport: cf}
	p.t.Cleanup(func() { publicHTTP = public })
}

// Without a key, nothing of CurseForge is offered: the panel says so, and
// its catalog of CurseForge answers that it is not set up.
func TestCurseForgeNeedsAKey(t *testing.T) {
	p := newPanel(t)
	if _, meta := p.get("viewer", "GET", "/meta", ""); meta["curseforge"] != false {
		t.Errorf("meta without a key: %v", meta["curseforge"])
	}
	for _, path := range []string{"/catalog/curseforge/search?type=mod", "/catalog/curseforge/categories?type=mod", "/catalog/curseforge/projects?ids=238222", "/catalog/curseforge/projects/238222/versions"} {
		if code, msg := p.do("viewer", "GET", path, ""); code != 404 || msg != "no_curseforge" {
			t.Errorf("%s without a key: %d %s", path, code, msg)
		}
	}
	// Content from CurseForge cannot be added without one either.
	if code, msg := p.do("owner", "POST", "/orgs/alpha/servers", `{"name":"cf","spec":{"eula":true,"version":"1.20.1","loader":"forge","mods":[{"source":"curseforge","kind":"mod","project":"238222"}]}}`); code != 404 || msg != "no_curseforge" {
		t.Errorf("a mod from CurseForge without a key: %d %s", code, msg)
	}

	cf := newCurseForgeStandIn(t, "install-key")
	p.withCurseForge(cf)
	if _, meta := p.get("viewer", "GET", "/meta", ""); meta["curseforge"] != true {
		t.Errorf("meta with the install's key: %v", meta["curseforge"])
	}
}

func TestCurseForgeSearchParams(t *testing.T) {
	categories := []curseForgeCategory{{ID: 423, Slug: "map-information", ClassID: 6}, {ID: 406, Slug: "world-gen", ClassID: 6}}
	got, err := curseForgeSearchParams(url.Values{"type": {"mod"}, "loader": {"forge"}, "gameVersion": {"1.20.1"}, "q": {" jei "}, "categories": {"world-gen,map-information"}}, categories)
	if err != nil {
		t.Fatal(err)
	}
	want := url.Values{
		"gameId": {"432"}, "classId": {"6"}, "sortField": {"1"}, "sortOrder": {"desc"}, "index": {"0"}, "pageSize": {"24"},
		"searchFilter": {"jei"}, "gameVersion": {"1.20.1"}, "modLoaderType": {"1"}, "categoryIds": {"[406,423]"},
	}
	if got.Encode() != want.Encode() {
		t.Errorf("got %v\nwant %v", got, want)
	}

	// Quilt runs Fabric's mods; NeoForge on 1.20.1 runs Forge's.
	for loader, want := range map[string]string{"quilt": "[Quilt,Fabric]", "neoforge": "[NeoForge,Forge]"} {
		got, _ := curseForgeSearchParams(url.Values{"type": {"mod"}, "loader": {loader}, "gameVersion": {"1.20.1"}}, nil)
		if got.Get("modLoaderTypes") != want || got.Has("modLoaderType") {
			t.Errorf("%s: %v", loader, got)
		}
	}
	// Plugins, datapacks and resource packs are not sorted by loader there.
	for kind, class := range map[string]string{"plugin": "5", "datapack": "6945", "resourcepack": "12", "modpack": "4471"} {
		got, _ := curseForgeSearchParams(url.Values{"type": {kind}, "loader": {"paper"}, "sort": {"downloads"}}, nil)
		if got.Get("classId") != class || got.Has("modLoaderType") || got.Has("modLoaderTypes") || got.Get("sortField") != "6" {
			t.Errorf("%s: %v", kind, got)
		}
	}
	// "Latest" is no version to narrow to.
	if got, _ := curseForgeSearchParams(url.Values{"type": {"mod"}, "gameVersion": {"LATEST"}}, nil); got.Has("gameVersion") {
		t.Errorf("LATEST: %v", got)
	}
	// The last page there is, short of CurseForge's ten thousand.
	if got, _ := curseForgeSearchParams(url.Values{"type": {"mod"}, "offset": {"9984"}}, nil); got.Get("pageSize") != "16" {
		t.Errorf("the last page: %v", got)
	}
	for name, q := range map[string]url.Values{
		"no type":             {},
		"an unknown order":    {"type": {"mod"}, "sort": {"random"}},
		"an unknown category": {"type": {"mod"}, "categories": {"magic"}},
		"past the last page":  {"type": {"mod"}, "offset": {"10008"}},
	} {
		if _, err := curseForgeSearchParams(q, categories); err == nil {
			t.Errorf("%s was taken", name)
		}
	}
}

// A search, a project's files and the projects a file needs come back in
// the shape Modrinth's have, which the pages show; the key goes along with
// every one, and none of it is sent on to the browser.
func TestCurseForgeCatalog(t *testing.T) {
	p := newPanel(t)
	cf := newCurseForgeStandIn(t, "install-key")
	p.withCurseForge(cf)

	rec := p.send("viewer", "GET", "/catalog/curseforge/search?type=mod&loader=forge&gameVersion=1.20.1&categories=map-information", nil)
	if rec.Code != 200 {
		t.Fatalf("search: %d %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "install-key") {
		t.Error("the key is in what the browser is sent")
	}
	var results struct {
		Hits      []map[string]any `json:"hits"`
		TotalHits int              `json:"total_hits"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &results)
	if len(results.Hits) != 2 || results.TotalHits != 2 {
		t.Fatalf("hits: %v", results)
	}
	jei, hand := results.Hits[0], results.Hits[1]
	for field, want := range map[string]any{
		"project_id": "238222", "slug": "jei", "title": "Just Enough Items (JEI)", "author": "mezz", "downloads": float64(412345678),
		"project_type": "mod", "page_url": "https://www.curseforge.com/minecraft/mc-mods/jei", "downloadable": true,
		"icon_url": "https://media.forgecdn.net/avatars/thumbnails/29/69/256/256/635838945588716414.jpeg",
	} {
		if jei[field] != want {
			t.Errorf("%s: got %v, want %v", field, jei[field], want)
		}
	}
	if hand["downloadable"] != false || hand["icon_url"] != nil {
		t.Errorf("a mod whose author allows downloads only on CurseForge: %v", hand)
	}
	if n := cf.count("GET /v1/mods/search?"); n != 1 {
		t.Errorf("searched %d times", n)
	}
	asked := cf.asked[len(cf.asked)-1]
	for _, want := range []string{"categoryIds=%5B423%5D", "modLoaderType=1", "gameVersion=1.20.1", "classId=6"} {
		if !strings.Contains(asked, want) {
			t.Errorf("the search asked %s, without %s", asked, want)
		}
	}

	var versions []map[string]any
	p.fetch("/catalog/curseforge/projects/238222/versions?loader=forge&gameVersion=1.20.1", &versions)
	if len(versions) != 2 {
		t.Fatalf("Forge's files for 1.20.1: %v", versions)
	}
	// Newest first, with what each is for.
	if versions[0]["id"] != "4600000" || versions[0]["version_type"] != "beta" || versions[1]["id"] != "4593548" {
		t.Errorf("order: %v", versions)
	}
	v := versions[1]
	if g := v["game_versions"].([]any); len(g) != 2 || g[0] != "1.20" || g[1] != "1.20.1" {
		t.Errorf("game versions: %v", g)
	}
	if l := v["loaders"].([]any); len(l) != 1 || l[0] != "forge" {
		t.Errorf("loaders: %v", l)
	}
	files := v["files"].([]any)
	if len(files) != 1 || files[0].(map[string]any)["hashes"].(map[string]any)["sha1"] != "3333333333333333333333333333333333333333" {
		t.Errorf("files: %v", files)
	}
	if deps := v["dependencies"].([]any); len(deps) != 2 || deps[0].(map[string]any)["dependency_type"] != "required" {
		t.Errorf("dependencies: %v", deps)
	}

	// Quilt is asked for all of the project's files for the version, and
	// those for Quilt or Fabric are kept.
	p.fetch("/catalog/curseforge/projects/238222/versions?loader=quilt&gameVersion=1.20.1", &versions)
	if len(versions) != 1 || versions[0]["id"] != "4590000" {
		t.Errorf("Quilt's: %v", versions)
	}
	// A file the panel cannot fetch says so.
	p.fetch("/catalog/curseforge/projects/900001/versions", &versions)
	if len(versions) != 1 || versions[0]["downloadable"] != false || len(versions[0]["files"].([]any)) != 0 {
		t.Errorf("a file only on CurseForge: %v", versions)
	}
	// A modpack is named by its slug.
	p.fetch("/catalog/curseforge/projects/all-the-mods-9/versions", &versions)
	if len(versions) != 1 || versions[0]["id"] != "5000001" {
		t.Errorf("a modpack's, by its slug: %v", versions)
	}

	var projects []map[string]any
	p.fetch("/catalog/curseforge/projects?ids=238222,900001", &projects)
	if len(projects) != 2 || projects[0]["title"] != "Just Enough Items (JEI)" || projects[0]["id"] != "238222" {
		t.Errorf("projects: %v", projects)
	}
	if code, _ := p.do("viewer", "GET", "/catalog/curseforge/projects?ids=jei", ""); code != 400 {
		t.Errorf("a project named by its slug: %d", code)
	}

	var categories []string
	p.fetch("/catalog/curseforge/categories?type=mod", &categories)
	if !slices.Equal(categories, []string{"map-information", "mc-food", "world-gen"}) {
		t.Errorf("categories: %v", categories)
	}

	// A project's page, which content from CurseForge links to by its ID.
	if rec := p.send("viewer", "GET", "/catalog/curseforge/projects/238222/page", nil); rec.Code != http.StatusFound || rec.Header().Get("Location") != "https://www.curseforge.com/minecraft/mc-mods/jei" {
		t.Errorf("page: %d %s", rec.Code, rec.Header().Get("Location"))
	}

	// A key CurseForge does not take is said plainly.
	cf.key = "another"
	if code, _ := p.do("viewer", "GET", "/catalog/curseforge/search?type=mod&q=new", ""); code != http.StatusBadGateway {
		t.Errorf("a key CurseForge refuses: %d", code)
	}
}

// Content from CurseForge is pinned to a file when it is added, as from
// Modrinth: the newest release for the server's loader and game version.
// One its author allows downloads of only on CurseForge is refused with
// where to get it.
func TestCurseForgeContentIsPinned(t *testing.T) {
	p := newPanel(t)
	p.withCurseForge(newCurseForgeStandIn(t, "install-key"))
	ctx := context.Background()

	if code, msg := p.do("owner", "POST", "/orgs/alpha/servers", `{"name":"cf","spec":{"eula":true,"version":"1.20.1","loader":"forge","mods":[{"source":"curseforge","kind":"mod","project":"238222"}]}}`); code != 201 {
		t.Fatalf("create: %d %s", code, msg)
	}
	mc := &avsysv1.MinecraftServer{}
	if err := p.srv.Client.Get(ctx, client.ObjectKey{Namespace: "mc-alpha", Name: "cf"}, mc); err != nil {
		t.Fatal(err)
	}
	m := mc.Spec.Mods[0]
	if m.Version != "4593548" || m.VersionName != "jei-1.20.1-forge-15.2.0.27.jar" || m.Title != "Just Enough Items (JEI)" || !strings.HasPrefix(m.IconURL, "https://media.forgecdn.net/") {
		t.Errorf("pinned: %+v", m)
	}

	// Moved to a loader with no file, it is said.
	rec := p.send("owner", "PATCH", "/orgs/alpha/servers/cf", []byte(`{"spec":{"loader":"fabric","version":"1.21.4"}}`), "Content-Type", "application/json")
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "no file for Fabric 1.21.4") {
		t.Errorf("no file for the server: %d %s", rec.Code, rec.Body)
	}

	rec = p.send("owner", "POST", "/orgs/alpha/servers", []byte(`{"name":"hand","spec":{"eula":true,"version":"1.20.1","loader":"forge","mods":[{"source":"curseforge","kind":"mod","project":"900001"}]}}`))
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "Hand Only can only be downloaded on CurseForge. Download it from https://www.curseforge.com/minecraft/mc-mods/hand-only and upload it here.") {
		t.Errorf("one only on CurseForge: %d %s", rec.Code, rec.Body)
	}
	// Named by a slug, it is not one.
	if code, _ := p.do("owner", "POST", "/orgs/alpha/servers", `{"name":"slug","spec":{"eula":true,"version":"1.20.1","loader":"forge","mods":[{"source":"curseforge","kind":"mod","project":"jei"}]}}`); code != 400 {
		t.Errorf("a mod named by its slug: %d", code)
	}
}

// A datapack from CurseForge is fetched from where CurseForge keeps its
// file, by the name it was pinned with.
func TestCurseForgeDatapack(t *testing.T) {
	spec := &avsysv1.MinecraftServerSpec{Mods: []avsysv1.ContentRef{{Source: avsysv1.SourceCurseForge, Kind: avsysv1.KindDatapack, Project: "1", Version: "4593548", File: "pack one.zip"}}}
	if got := installedDatapack(spec.Mods[0]); got != "pack one.zip" {
		t.Errorf("installed as %q", got)
	}
	// Taken off, the panel deletes it, as the image does not.
	if gone := leftBehind(&avsysv1.MinecraftServerSpec{}, spec); !slices.Equal(gone, []string{"/data/world/datapacks/pack one.zip"}) {
		t.Errorf("left behind: %v", gone)
	}
	if u := controller.CurseForgeFileURL("4593548", "pack one.zip"); u != "https://mediafilez.forgecdn.net/files/4593/548/pack%20one.zip" {
		t.Errorf("address: %s", u)
	}
}

// A modpack from CurseForge is pinned to its newest file, which says the
// game version and loader; what it lists is read from its manifest, and
// files that have to be put in by hand say so.
func TestCurseForgeModpack(t *testing.T) {
	p := newPanel(t)
	cf := newCurseForgeStandIn(t, "install-key")
	p.withCurseForge(cf)
	ctx := context.Background()

	if code, msg := p.do("owner", "POST", "/orgs/alpha/servers", `{"name":"atm","spec":{"eula":true,"version":"1.21.4","modpack":{"source":"curseforge","project":"all-the-mods-9"}}}`); code != 201 {
		t.Fatalf("create: %d %s", code, msg)
	}
	mc := &avsysv1.MinecraftServer{}
	if err := p.srv.Client.Get(ctx, client.ObjectKey{Namespace: "mc-alpha", Name: "atm"}, mc); err != nil {
		t.Fatal(err)
	}
	pack := mc.Spec.Modpack
	if pack.Version != "5000001" || pack.VersionName != "All the Mods 9-0.2.44" || pack.GameVersion != "1.20.1" || pack.Loader != "forge" || pack.Title != "All the Mods 9 - ATM9" || pack.Project != "all-the-mods-9" {
		t.Errorf("pinned: %+v", pack)
	}

	var contents struct{ Files []packEntry }
	p.fetch("/orgs/alpha/servers/atm/modpack/contents", &contents)
	want := []packEntry{
		{Path: "mods/jei-1.20.1-forge-15.2.0.27.jar", Server: true, Source: "curseforge", Project: "238222", Version: "4593548", Slug: "jei", Title: "Just Enough Items (JEI)",
			IconURL: "https://media.forgecdn.net/avatars/thumbnails/29/69/256/256/635838945588716414.jpeg", Page: "https://www.curseforge.com/minecraft/mc-mods/jei"},
		{Path: "mods/handonly-1.0.jar", Server: true, Source: "curseforge", Project: "900001", Version: "5100001", Slug: "hand-only", Title: "Hand Only",
			Page: "https://www.curseforge.com/minecraft/mc-mods/hand-only", Manual: true},
	}
	if !slices.Equal(contents.Files, want) {
		t.Errorf("contents:\n got %+v\nwant %+v", contents.Files, want)
	}
	// Read once: a version of a pack does not change.
	p.fetch("/orgs/alpha/servers/atm/modpack/contents", &contents)
	if n := cf.count("GET /files/5000/1/"); n != 1 {
		t.Errorf("the pack was fetched %d times", n)
	}
}

// The instance administrator enters the key, which is checked with
// CurseForge, kept, and never shown again; removing it goes back to the
// install's, or to none. Servers that install from CurseForge are given it
// in a Secret of their namespace.
func TestCurseForgeKeySetting(t *testing.T) {
	p := newPanel(t)
	cf := newCurseForgeStandIn(t, "entered-key")
	p.srv.catalog.http.Transport = cf
	ctx := context.Background()

	panelOf := func() map[string]any {
		t.Helper()
		_, nodes := p.get("root", "GET", "/nodes", "")
		panel, _ := nodes["panel"].(map[string]any)
		return panel
	}
	if got := panelOf()["curseforge"]; got != "" {
		t.Errorf("before: %v", got)
	}
	for _, who := range []string{"owner", "admin", "viewer"} {
		if code, _ := p.do(who, "PATCH", "/panel", `{"curseforgeKey":"entered-key"}`); code != 403 {
			t.Errorf("%s set it: %d", who, code)
		}
	}
	if code, _ := p.do("root", "PATCH", "/panel", `{"curseforgeKey":"wrong-key"}`); code != 400 {
		t.Errorf("a key CurseForge does not take: %d", code)
	}
	if p.srv.curseForgeKey(ctx) != "" {
		t.Error("a key CurseForge did not take was kept")
	}

	// A server that installs from CurseForge, made before there was a key.
	mc := &avsysv1.MinecraftServer{}
	if err := p.srv.Client.Get(ctx, client.ObjectKey{Namespace: "mc-alpha", Name: "world"}, mc); err != nil {
		t.Fatal(err)
	}
	mc.Spec.Mods = []avsysv1.ContentRef{{Source: avsysv1.SourceCurseForge, Kind: avsysv1.KindMod, Project: "238222", Version: "4593548"}}
	if err := p.srv.Client.Update(ctx, mc); err != nil {
		t.Fatal(err)
	}

	rec := p.send("root", "PATCH", "/panel", []byte(`{"curseforgeKey":" entered-key "}`))
	if rec.Code != 200 || strings.Contains(rec.Body.String(), "entered-key") {
		t.Fatalf("set: %d %s", rec.Code, rec.Body)
	}
	if got := panelOf()["curseforge"]; got != "panel" {
		t.Errorf("entered: %v", got)
	}
	if _, meta := p.get("viewer", "GET", "/meta", ""); meta["curseforge"] != true {
		t.Errorf("meta: %v", meta)
	}
	secret := &corev1.Secret{}
	if err := p.srv.Client.Get(ctx, client.ObjectKey{Namespace: "mc-alpha", Name: controller.CurseForgeSecret}, secret); err != nil || string(secret.Data[controller.CurseForgeSecretKey]) != "entered-key" {
		t.Errorf("the servers' Secret: %v %v", err, secret.Data)
	}
	// Beta has no server from CurseForge, so it is not given the key.
	if err := p.srv.Client.Get(ctx, client.ObjectKey{Namespace: "mc-beta", Name: controller.CurseForgeSecret}, &corev1.Secret{}); err == nil {
		t.Error("a namespace without a server from CurseForge was given the key")
	}
	if code, _ := p.do("viewer", "GET", "/catalog/curseforge/search?type=mod", ""); code != 200 {
		t.Errorf("search with the key: %d", code)
	}

	// Removed, there is the install's, or none.
	p.srv.Config.CurseForgeKey = "install-key"
	if code, _ := p.do("root", "PATCH", "/panel", `{"curseforgeKey":""}`); code != 200 {
		t.Fatal(code)
	}
	if got := panelOf()["curseforge"]; got != "install" {
		t.Errorf("with the install's: %v", got)
	}
	if err := p.srv.Client.Get(ctx, client.ObjectKey{Namespace: "mc-alpha", Name: controller.CurseForgeSecret}, secret); err != nil || string(secret.Data[controller.CurseForgeSecretKey]) != "install-key" {
		t.Errorf("the servers' Secret after: %v %v", err, secret.Data)
	}
	p.srv.Config.CurseForgeKey = ""
	if code, _ := p.do("root", "PATCH", "/panel", `{"curseforgeKey":""}`); code != 200 {
		t.Fatal(code)
	}
	if err := p.srv.Client.Get(ctx, client.ObjectKey{Namespace: "mc-alpha", Name: controller.CurseForgeSecret}, secret); err != nil || len(secret.Data) != 0 {
		t.Errorf("the servers' Secret with none: %v %v", err, secret.Data)
	}
	if _, meta := p.get("viewer", "GET", "/meta", ""); meta["curseforge"] != false {
		t.Errorf("meta with none: %v", meta)
	}
	// Auto-update is still set on its own.
	if code, _ := p.do("root", "PATCH", "/panel", `{"autoUpdate":true}`); code != 200 {
		t.Errorf("auto-update: %d", code)
	}
}
