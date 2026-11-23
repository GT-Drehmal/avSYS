package api

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
	"github.com/GT-Drehmal/avSYS/internal/controller"
	"github.com/GT-Drehmal/avSYS/internal/store"
)

// CurseForge is a second catalog beside Modrinth. Its API needs a key of the
// install's own: entered by the instance administrator, or given to the
// panel when it was installed. Without one nothing of CurseForge is offered
// anywhere. Every call to it is the panel's, so the key never reaches a
// browser, and its answers are put in the shape Modrinth's have, which the
// pages already know how to show.
//
// Content from CurseForge is named by the project's ID, which the image
// takes for any kind; a modpack by its slug, which is how the image is told
// of one.

const (
	curseForgeAPI = "https://api.curseforge.com"
	// curseForgeGame is Minecraft's ID at CurseForge.
	curseForgeGame = 432
	// curseForgeKeySetting is where a key entered in the panel is kept.
	curseForgeKeySetting = "curseforge.api-key"
)

// curseForgeClasses are CurseForge's kinds of project, by the panel's names.
var curseForgeClasses = map[string]int{"mod": 6, "plugin": 5, "datapack": 6945, "resourcepack": 12, "modpack": 4471}

// curseForgeFolders are where the files of each kind go in a modpack.
var curseForgeFolders = map[int]string{6: "mods", 5: "plugins", 6945: "datapacks", 12: "resourcepacks", 6552: "shaderpacks"}

// curseForgeLoaders are CurseForge's numbers for the loaders it knows, and
// its names for them, which a file lists among its game versions.
var curseForgeLoaders = map[string]struct {
	id   int
	name string
}{
	"forge": {1, "Forge"}, "fabric": {4, "Fabric"}, "quilt": {5, "Quilt"}, "neoforge": {6, "NeoForge"},
}

// curseForgeOrder maps the orders the panel offers to CurseForge's.
var curseForgeOrder = map[string]int{
	"": 1, "relevance": 1, "follows": 2, "updated": 3, "downloads": 6, "newest": 11,
}

var errNoCurseForge = &Error{Status: http.StatusNotFound, Code: "no_curseforge", Message: "CurseForge is not set up on this panel."}

// CurseForgeKey is the CurseForge API key in use: one entered in the panel,
// or else the one the install gave.
func CurseForgeKey(ctx context.Context, st *store.Store, install string) string {
	if st == nil {
		return install
	}
	if v, err := st.Setting(ctx, curseForgeKeySetting); err == nil && v != "" {
		return v
	}
	return install
}

func (s *Server) curseForgeKey(ctx context.Context) string {
	return CurseForgeKey(ctx, s.Store, s.Config.CurseForgeKey)
}

// curseForgeKeyFrom says where the key in use comes from: "panel", "install" or "".
func (s *Server) curseForgeKeyFrom(ctx context.Context) string {
	if v, err := s.Store.Setting(ctx, curseForgeKeySetting); err == nil && v != "" {
		return "panel"
	}
	if s.Config.CurseForgeKey != "" {
		return "install"
	}
	return ""
}

// curseForge asks CurseForge's API, serving repeats from memory for ttl.
// in, if not nil, is sent as JSON.
func (s *Server) curseForge(ctx context.Context, path string, in any, ttl time.Duration) ([]byte, error) {
	key := s.curseForgeKey(ctx)
	if key == "" {
		return nil, errNoCurseForge
	}
	return s.catalog.curseForge(ctx, key, curseForgeAPI+path, in, ttl)
}

func (c *catalog) curseForge(ctx context.Context, key, u string, in any, ttl time.Duration) ([]byte, error) {
	method, body := http.MethodGet, []byte(nil)
	if in != nil {
		method = http.MethodPost
		var err error
		if body, err = json.Marshal(in); err != nil {
			return nil, err
		}
	}
	// The key is not part of what is remembered: what CurseForge answers
	// does not depend on whose key asked.
	cacheKey := method + " " + u + " " + string(body)
	c.mu.Lock()
	if e, ok := c.entries[cacheKey]; ok && time.Now().Before(e.expires) {
		c.mu.Unlock()
		return e.body, nil
	}
	c.mu.Unlock()

	req, err := http.NewRequestWithContext(ctx, method, u, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("x-api-key", key)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, &Error{Status: http.StatusBadGateway, Code: "upstream", Message: "Could not reach CurseForge."}
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, errNotFound
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, &Error{Status: http.StatusBadGateway, Code: "upstream", Message: "CurseForge did not accept the panel's API key."}
	case resp.StatusCode != http.StatusOK:
		return nil, &Error{Status: http.StatusBadGateway, Code: "upstream", Message: fmt.Sprintf("CurseForge answered %s.", resp.Status)}
	}
	out, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	if len(c.entries) >= catalogCacheSize {
		clear(c.entries)
	}
	c.entries[cacheKey] = cacheEntry{body: out, expires: time.Now().Add(ttl)}
	c.mu.Unlock()
	return out, nil
}

// checkCurseForgeKey asks CurseForge whether it takes a key.
func (s *Server) checkCurseForgeKey(ctx context.Context, key string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, curseForgeAPI+"/v1/games/"+strconv.Itoa(curseForgeGame), nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", s.catalog.userAgent)
	req.Header.Set("x-api-key", key)
	resp, err := s.catalog.http.Do(req)
	if err != nil {
		return errBadRequest("Could not reach CurseForge to check the key.")
	}
	resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		return nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return errBadRequest("CurseForge did not accept this key.")
	default:
		return errBadRequest("CurseForge answered %s. Try again later.", resp.Status)
	}
}

// setCurseForgeKey keeps a key entered in the panel, or with "" forgets it,
// and gives servers that install from CurseForge the key now in use.
func (s *Server) setCurseForgeKey(ctx context.Context, u *store.User, key string) error {
	key = strings.TrimSpace(key)
	if key != "" {
		if len(key) > 512 || strings.ContainsAny(key, " \t\r\n") {
			return errBadRequest("That is not a CurseForge API key.")
		}
		if err := s.checkCurseForgeKey(ctx, key); err != nil {
			return err
		}
		if err := s.Store.SetSetting(ctx, curseForgeKeySetting, key); err != nil {
			return err
		}
		s.audit(ctx, u, nil, "panel.curseforge", "", "set")
	} else {
		if err := s.Store.DeleteSetting(ctx, curseForgeKeySetting); err != nil {
			return err
		}
		s.audit(ctx, u, nil, "panel.curseforge", "", "removed")
	}
	s.spreadCurseForgeKey(ctx)
	return nil
}

// spreadCurseForgeKey puts the key in use in each namespace with a server
// that installs from CurseForge, for its next start.
func (s *Server) spreadCurseForgeKey(ctx context.Context) {
	var list avsysv1.MinecraftServerList
	if err := s.Client.List(ctx, &list); err != nil {
		s.Log.Warn("could not list servers for the CurseForge key", "error", err)
		return
	}
	key := s.curseForgeKey(ctx)
	done := map[string]bool{}
	for i := range list.Items {
		mc := &list.Items[i]
		if done[mc.Namespace] || !controller.UsesCurseForge(&mc.Spec) {
			continue
		}
		done[mc.Namespace] = true
		if err := controller.EnsureCurseForgeSecret(ctx, s.Client, mc.Namespace, key); err != nil {
			s.Log.Warn("could not give servers the CurseForge key", "namespace", mc.Namespace, "error", err)
		}
	}
}

// What the panel reads of CurseForge's answers.

type curseForgeMod struct {
	ID      int    `json:"id"`
	Name    string `json:"name"`
	Slug    string `json:"slug"`
	Summary string `json:"summary"`
	Links   struct {
		WebsiteURL string `json:"websiteUrl"`
	} `json:"links"`
	DownloadCount float64 `json:"downloadCount"`
	ClassID       int     `json:"classId"`
	Categories    []struct {
		Slug string `json:"slug"`
	} `json:"categories"`
	Authors []struct {
		Name string `json:"name"`
	} `json:"authors"`
	Logo *struct {
		ThumbnailURL string `json:"thumbnailUrl"`
		URL          string `json:"url"`
	} `json:"logo"`
	DateModified string `json:"dateModified"`
	// AllowModDistribution is false where the author allows downloads
	// only on CurseForge itself.
	AllowModDistribution *bool `json:"allowModDistribution"`
}

func (m *curseForgeMod) icon() string {
	if m.Logo == nil {
		return ""
	}
	if m.Logo.ThumbnailURL != "" {
		return m.Logo.ThumbnailURL
	}
	return m.Logo.URL
}

// page is the project's page on CurseForge.
func (m *curseForgeMod) page() string {
	if m.Links.WebsiteURL != "" {
		return m.Links.WebsiteURL
	}
	return curseForgeProjectPage(strconv.Itoa(m.ID))
}

// curseForgeProjectPage is a project's page by its ID, which CurseForge
// sends on to the project's own.
func curseForgeProjectPage(id string) string {
	return "https://www.curseforge.com/projects/" + url.PathEscape(id)
}

func (m *curseForgeMod) downloadable() bool {
	return m.AllowModDistribution == nil || *m.AllowModDistribution
}

type curseForgeFile struct {
	ID           int      `json:"id"`
	ModID        int      `json:"modId"`
	DisplayName  string   `json:"displayName"`
	FileName     string   `json:"fileName"`
	ReleaseType  int      `json:"releaseType"`
	FileDate     string   `json:"fileDate"`
	FileLength   int64    `json:"fileLength"`
	DownloadURL  string   `json:"downloadUrl"`
	GameVersions []string `json:"gameVersions"`
	Hashes       []struct {
		Value string `json:"value"`
		Algo  int    `json:"algo"`
	} `json:"hashes"`
	Dependencies []struct {
		ModID        int `json:"modId"`
		RelationType int `json:"relationType"`
	} `json:"dependencies"`
	IsServerPack bool `json:"isServerPack"`
}

// gameVersionPattern is a game version as CurseForge lists one among a
// file's, where loaders and sides are listed too.
var gameVersionPattern = regexp.MustCompile(`^\d+\.\d+(\.\d+)?(-(pre|rc|snapshot)-?\d+)?$|^\d\dw\d\d[a-z]$`)

// games are the game versions a file is for, oldest first, as Modrinth lists them.
func (f *curseForgeFile) games() []string {
	out := []string{}
	for _, v := range f.GameVersions {
		if gameVersionPattern.MatchString(v) {
			out = append(out, v)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return versionLess(out[i], out[j]) })
	return out
}

// versionLess orders game versions by their numbers: 1.9 before 1.20.
func versionLess(a, b string) bool {
	pa, pb := strings.FieldsFunc(a, notDigit), strings.FieldsFunc(b, notDigit)
	for i := 0; i < len(pa) && i < len(pb); i++ {
		x, _ := strconv.Atoi(pa[i])
		y, _ := strconv.Atoi(pb[i])
		if x != y {
			return x < y
		}
	}
	return len(pa) < len(pb)
}

func notDigit(r rune) bool { return r < '0' || r > '9' }

// loaders are the loaders a file is for, by the panel's names.
func (f *curseForgeFile) loaders() []string {
	out := []string{}
	for _, v := range f.GameVersions {
		for name, l := range curseForgeLoaders {
			if strings.EqualFold(v, l.name) {
				out = append(out, name)
			}
		}
	}
	sort.Strings(out)
	return out
}

func (f *curseForgeFile) sha1() string {
	for _, h := range f.Hashes {
		if h.Algo == 1 {
			return h.Value
		}
	}
	return ""
}

func (f *curseForgeFile) releaseType() string {
	switch f.ReleaseType {
	case 2:
		return "beta"
	case 3:
		return "alpha"
	}
	return "release"
}

// version is a file as a Modrinth version, as the pages show one.
func (f *curseForgeFile) version() map[string]any {
	deps := []map[string]any{}
	for _, d := range f.Dependencies {
		kind := map[int]string{1: "embedded", 2: "optional", 3: "required", 5: "incompatible"}[d.RelationType]
		if kind == "" || d.ModID == 0 {
			continue
		}
		deps = append(deps, map[string]any{"project_id": strconv.Itoa(d.ModID), "dependency_type": kind})
	}
	files := []map[string]any{}
	if f.DownloadURL != "" {
		files = append(files, map[string]any{
			"url": f.DownloadURL, "filename": f.FileName, "primary": true, "size": f.FileLength,
			"hashes": map[string]string{"sha1": f.sha1()},
		})
	}
	return map[string]any{
		"id":             strconv.Itoa(f.ID),
		"name":           f.DisplayName,
		"version_number": f.DisplayName,
		"version_type":   f.releaseType(),
		"game_versions":  f.games(),
		"loaders":        f.loaders(),
		"date_published": f.FileDate,
		"files":          files,
		"dependencies":   deps,
		// The author allows downloads of it only on CurseForge itself.
		"downloadable": f.DownloadURL != "",
	}
}

type curseForgeList[T any] struct {
	Data       []T `json:"data"`
	Pagination struct {
		Index      int `json:"index"`
		PageSize   int `json:"pageSize"`
		TotalCount int `json:"totalCount"`
	} `json:"pagination"`
}

type curseForgeOne[T any] struct {
	Data T `json:"data"`
}

func readCurseForge[T any](body []byte, err error) (T, error) {
	var out T
	if err != nil {
		return out, err
	}
	if json.Unmarshal(body, &out) != nil {
		return out, &Error{Status: http.StatusBadGateway, Code: "upstream", Message: "Could not read CurseForge's answer."}
	}
	return out, nil
}

// curseForgeLast is past where CurseForge answers: a search is followed as
// far as its first ten thousand results.
const curseForgeLast = 10_000

type curseForgeCategory struct {
	ID      int    `json:"id"`
	Slug    string `json:"slug"`
	ClassID int    `json:"classId"`
	IsClass bool   `json:"isClass"`
}

func (s *Server) curseForgeCategories(ctx context.Context, classID int) ([]curseForgeCategory, error) {
	list, err := readCurseForge[curseForgeList[curseForgeCategory]](s.curseForge(ctx, fmt.Sprintf("/v1/categories?gameId=%d&classId=%d", curseForgeGame, classID), nil, time.Hour))
	if err != nil {
		return nil, err
	}
	out := []curseForgeCategory{}
	for _, c := range list.Data {
		if !c.IsClass && c.ClassID == classID && tagPattern.MatchString(c.Slug) {
			out = append(out, c)
		}
	}
	return out, nil
}

// curseForgeLoaderParams narrows a search or a list of files to loaders,
// where CurseForge can be asked to: one by its number, several by name.
func curseForgeLoaderParams(params url.Values, loaders []string) {
	var ids, names []string
	for _, l := range loaders {
		if cf, ok := curseForgeLoaders[l]; ok {
			ids, names = append(ids, strconv.Itoa(cf.id)), append(names, cf.name)
		}
	}
	switch {
	case len(ids) == 1:
		params.Set("modLoaderType", ids[0])
	case len(ids) > 1:
		params.Set("modLoaderTypes", "["+strings.Join(names, ",")+"]")
	}
}

// curseForgeSearchParams turns a search as the panel is asked for it into
// one CurseForge understands. categories are the kind's, by slug.
func curseForgeSearchParams(q url.Values, categories []curseForgeCategory) (url.Values, error) {
	kind := q.Get("type")
	class, ok := curseForgeClasses[kind]
	if !ok {
		return nil, errBadRequest("Unknown content type.")
	}
	order, ok := curseForgeOrder[q.Get("sort")]
	if !ok {
		return nil, errBadRequest("Results are sorted by relevance, downloads, follows, newest or updated.")
	}
	offset := max(0, atoi(q.Get("offset")))
	if offset > modrinthLastOffset {
		return nil, errBadRequest("Only the first %d results can be shown. Narrow the search.", modrinthLastOffset+modrinthPage)
	}
	params := url.Values{
		"gameId":    {strconv.Itoa(curseForgeGame)},
		"classId":   {strconv.Itoa(class)},
		"sortField": {strconv.Itoa(order)},
		"sortOrder": {"desc"},
		"index":     {strconv.Itoa(offset)},
		"pageSize":  {strconv.Itoa(min(modrinthPage, curseForgeLast-offset))},
	}
	if text := strings.TrimSpace(q.Get("q")); text != "" {
		params.Set("searchFilter", text)
	}
	gameVersion := q.Get("gameVersion")
	if gameVersion != "" && gameVersion != "LATEST" {
		params.Set("gameVersion", gameVersion)
	}
	if kind == "mod" || kind == "modpack" {
		curseForgeLoaderParams(params, loadersOf(q.Get("loader"), gameVersion))
	}
	var ids []string
	for _, slug := range strings.Split(q.Get("categories"), ",") {
		if slug == "" {
			continue
		}
		found := false
		for _, c := range categories {
			if c.Slug == slug {
				ids, found = append(ids, strconv.Itoa(c.ID)), true
				break
			}
		}
		if !found {
			return nil, errBadRequest("%q is not a category.", slug)
		}
	}
	if len(ids) > 10 {
		return nil, errBadRequest("At most 10 categories at a time.")
	}
	if len(ids) > 0 {
		params.Set("categoryIds", "["+strings.Join(ids, ",")+"]")
	}
	return params, nil
}

// hit is a project as a Modrinth search result, as the pages show one.
func (m *curseForgeMod) hit(kind string) map[string]any {
	var icon *string
	if i := m.icon(); i != "" {
		icon = &i
	}
	author := ""
	if len(m.Authors) > 0 {
		author = m.Authors[0].Name
	}
	categories := []string{}
	for _, c := range m.Categories {
		categories = append(categories, c.Slug)
	}
	return map[string]any{
		"project_id":         strconv.Itoa(m.ID),
		"slug":               m.Slug,
		"title":              m.Name,
		"description":        m.Summary,
		"icon_url":           icon,
		"downloads":          int64(m.DownloadCount),
		"author":             author,
		"project_type":       kind,
		"categories":         categories,
		"display_categories": categories,
		"date_modified":      m.DateModified,
		"page_url":           m.page(),
		"downloadable":       m.downloadable(),
	}
}

func (s *Server) curseForgeSearch(w http.ResponseWriter, r *http.Request, _ *store.User) error {
	q := r.URL.Query()
	class, ok := curseForgeClasses[q.Get("type")]
	if !ok {
		return errBadRequest("Unknown content type.")
	}
	var categories []curseForgeCategory
	if q.Get("categories") != "" {
		var err error
		if categories, err = s.curseForgeCategories(r.Context(), class); err != nil {
			return err
		}
	}
	params, err := curseForgeSearchParams(q, categories)
	if err != nil {
		return err
	}
	found, err := readCurseForge[curseForgeList[curseForgeMod]](s.curseForge(r.Context(), "/v1/mods/search?"+params.Encode(), nil, 5*time.Minute))
	if err != nil {
		return err
	}
	hits := []map[string]any{}
	for i := range found.Data {
		hits = append(hits, found.Data[i].hit(q.Get("type")))
	}
	offset := atoi(params.Get("index"))
	w.Header().Set("Cache-Control", "private, max-age=60")
	writeJSON(w, http.StatusOK, map[string]any{
		"hits": hits, "offset": offset, "limit": atoi(params.Get("pageSize")),
		"total_hits": min(found.Pagination.TotalCount, curseForgeLast),
	})
	return nil
}

func (s *Server) curseForgeCategoryList(w http.ResponseWriter, r *http.Request, _ *store.User) error {
	class, ok := curseForgeClasses[r.URL.Query().Get("type")]
	if !ok {
		return errBadRequest("Unknown content type.")
	}
	categories, err := s.curseForgeCategories(r.Context(), class)
	if err != nil {
		return err
	}
	names := []string{}
	for _, c := range categories {
		names = append(names, c.Slug)
	}
	sort.Strings(names)
	names = slices.Compact(names)
	w.Header().Set("Cache-Control", "private, max-age=3600")
	writeJSON(w, http.StatusOK, names)
	return nil
}

// curseForgeMods looks up several projects at once, by their IDs.
func (s *Server) curseForgeMods(ctx context.Context, ids []int) ([]curseForgeMod, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	sort.Ints(ids)
	list, err := readCurseForge[curseForgeList[curseForgeMod]](s.curseForge(ctx, "/v1/mods", map[string]any{"modIds": ids, "filterPcOnly": true}, 5*time.Minute))
	return list.Data, err
}

// curseForgeProjects names several projects at once, which is how what a
// mod depends on is shown by name.
func (s *Server) curseForgeProjects(w http.ResponseWriter, r *http.Request, _ *store.User) error {
	parts := strings.Split(r.URL.Query().Get("ids"), ",")
	if len(parts) > 50 {
		return errBadRequest("At most 50 projects at a time.")
	}
	var ids []int
	for _, p := range parts {
		id, err := strconv.Atoi(p)
		if err != nil || id <= 0 {
			return errBadRequest("%q is not a project.", p)
		}
		ids = append(ids, id)
	}
	mods, err := s.curseForgeMods(r.Context(), ids)
	if err != nil {
		return err
	}
	out := []map[string]any{}
	for i := range mods {
		m := &mods[i]
		if !slices.Contains(ids, m.ID) {
			continue
		}
		var icon *string
		if i := m.icon(); i != "" {
			icon = &i
		}
		out = append(out, map[string]any{"id": strconv.Itoa(m.ID), "slug": m.Slug, "title": m.Name, "icon_url": icon, "project_type": curseForgeKindOf(m.ClassID)})
	}
	w.Header().Set("Cache-Control", "private, max-age=60")
	writeJSON(w, http.StatusOK, out)
	return nil
}

func curseForgeKindOf(class int) string {
	for kind, c := range curseForgeClasses {
		if c == class {
			return kind
		}
	}
	return "mod"
}

// curseForgeSlugPattern is what a project's slug looks like.
var curseForgeSlugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,127}$`)

// curseForgeProjectID is the ID of a project named by its ID, or by its
// slug, as a modpack is and content added outside the panel may be. A slug
// is only unique within a kind of project: it is looked for among the kinds
// given, in order, or else modpacks.
func (s *Server) curseForgeProjectID(ctx context.Context, ref string, kinds ...string) (int, error) {
	if id, err := strconv.Atoi(ref); err == nil && id > 0 {
		return id, nil
	}
	if !curseForgeSlugPattern.MatchString(ref) {
		return 0, errNotFound
	}
	if len(kinds) == 0 {
		kinds = []string{"modpack"}
	}
	for _, kind := range kinds {
		params := url.Values{"gameId": {strconv.Itoa(curseForgeGame)}, "classId": {strconv.Itoa(curseForgeClasses[kind])}, "slug": {ref}}
		found, err := readCurseForge[curseForgeList[curseForgeMod]](s.curseForge(ctx, "/v1/mods/search?"+params.Encode(), nil, time.Hour))
		if err != nil {
			return 0, err
		}
		for _, m := range found.Data {
			if m.Slug == ref {
				return m.ID, nil
			}
		}
	}
	return 0, errNotFound
}

// curseForgeKinds are the kinds a project linked to may be, most likely first.
var curseForgeKinds = []string{"modpack", "mod", "plugin", "resourcepack", "datapack"}

func (s *Server) curseForgeMod(ctx context.Context, id int) (*curseForgeMod, error) {
	one, err := readCurseForge[curseForgeOne[curseForgeMod]](s.curseForge(ctx, fmt.Sprintf("/v1/mods/%d", id), nil, time.Hour))
	return &one.Data, err
}

func (s *Server) curseForgeFile(ctx context.Context, mod, file int) (*curseForgeFile, error) {
	one, err := readCurseForge[curseForgeOne[curseForgeFile]](s.curseForge(ctx, fmt.Sprintf("/v1/mods/%d/files/%d", mod, file), nil, time.Hour))
	return &one.Data, err
}

// curseForgeFilePage is how many files CurseForge lists at a time, and
// curseForgeFilePages how many pages of them are read at most.
const (
	curseForgeFilePage  = 50
	curseForgeFilePages = 20
)

// curseForgeFiles lists a project's files for a game version and the
// loaders given, if any, newest first. Server packs, which a modpack has
// beside its own files, are left out.
func (s *Server) curseForgeFiles(ctx context.Context, mod int, loaders []string, gameVersion string) ([]curseForgeFile, error) {
	params := url.Values{"pageSize": {strconv.Itoa(curseForgeFilePage)}}
	if gameVersion != "" && gameVersion != "LATEST" {
		params.Set("gameVersion", gameVersion)
	}
	// One loader is asked for by CurseForge; several are picked out here.
	known := []string{}
	for _, l := range loaders {
		if _, ok := curseForgeLoaders[l]; ok {
			known = append(known, l)
		}
	}
	if len(known) == 1 {
		curseForgeLoaderParams(params, known)
	}
	// Every page, as far as curseForgeFilePages: those that fit may be past
	// the first, where several loaders are picked out here.
	out := []curseForgeFile{}
	for page := 0; page < curseForgeFilePages; page++ {
		params.Set("index", strconv.Itoa(page*curseForgeFilePage))
		list, err := readCurseForge[curseForgeList[curseForgeFile]](s.curseForge(ctx, fmt.Sprintf("/v1/mods/%d/files?%s", mod, params.Encode()), nil, 5*time.Minute))
		if err != nil {
			return nil, err
		}
		for _, f := range list.Data {
			if f.IsServerPack {
				continue
			}
			if len(known) > 1 && !anyIn(f.loaders(), known) {
				continue
			}
			out = append(out, f)
		}
		if len(list.Data) < curseForgeFilePage || (page+1)*curseForgeFilePage >= list.Pagination.TotalCount {
			break
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].FileDate > out[j].FileDate })
	return out, nil
}

func anyIn(have, want []string) bool {
	for _, h := range have {
		for _, w := range want {
			if h == w {
				return true
			}
		}
	}
	return false
}

func (s *Server) curseForgeVersions(w http.ResponseWriter, r *http.Request, _ *store.User) error {
	q := r.URL.Query()
	id, err := s.curseForgeProjectID(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	files, err := s.curseForgeFiles(r.Context(), id, loadersOf(q.Get("loader"), q.Get("gameVersion")), q.Get("gameVersion"))
	if err != nil {
		return err
	}
	out := []map[string]any{}
	for i := range files {
		out = append(out, files[i].version())
	}
	w.Header().Set("Cache-Control", "private, max-age=60")
	writeJSON(w, http.StatusOK, out)
	return nil
}

// curseForgePage sends the browser on to a project's page on CurseForge,
// which content from there is known by the ID of.
func (s *Server) curseForgePage(w http.ResponseWriter, r *http.Request, _ *store.User) error {
	ref := r.PathValue("id")
	page := curseForgeProjectPage(ref)
	if id, err := s.curseForgeProjectID(r.Context(), ref, curseForgeKinds...); err == nil {
		if m, err := s.curseForgeMod(r.Context(), id); err == nil && m.Links.WebsiteURL != "" {
			page = m.Links.WebsiteURL
		}
	} else if !curseForgeSlugPattern.MatchString(ref) {
		return errNotFound
	}
	http.Redirect(w, r, page, http.StatusFound)
	return nil
}

// packSource says whether a resource pack picked from a project names one
// the panel knows: on Modrinth, or CurseForge.
func packSource(p *avsysv1.PackProject) bool {
	return (p.Source == string(avsysv1.SourceModrinth) || p.Source == string(avsysv1.SourceCurseForge)) && p.ID != ""
}

// errOnlyOnCurseForge is said of a file whose author allows downloads of it
// only on CurseForge itself.
func errOnlyOnCurseForge(name, page string) error {
	return errBadRequest("%s can only be downloaded on CurseForge. Download it from %s and upload it here.", name, page)
}

// curseForgeLoadersFor are the loaders a kind of content from CurseForge
// has to be for: plugins, datapacks and resource packs are not sorted by
// loader there.
func (t target) curseForgeLoadersFor(kind avsysv1.ContentKind) []string {
	if kind != avsysv1.KindMod {
		return nil
	}
	return loadersOf(t.loader, t.gameVersion)
}

// pinCurseForge resolves one CurseForge entry to a file and records the
// Java it needs, as pin does for Modrinth.
func (s *Server) pinCurseForge(ctx context.Context, m *avsysv1.ContentRef, t target) error {
	// Named by its slug, as content added outside the panel may be, it is
	// looked for among its kind's projects; the image takes either.
	kind := string(m.Kind)
	if kind == "" {
		kind = "mod"
	}
	id, err := s.curseForgeProjectID(ctx, m.Project, kind)
	if errors.Is(err, errNotFound) {
		return errBadRequest("%s was not found on CurseForge.", cmpOr(m.Title, m.Project))
	}
	if err != nil {
		return err
	}
	mod, err := s.curseForgeMod(ctx, id)
	if errors.Is(err, errNotFound) {
		return errBadRequest("%s was not found on CurseForge.", cmpOr(m.Title, m.Project))
	}
	if err != nil {
		return err
	}
	if m.Title == "" {
		m.Title = mod.Name
	}
	if m.IconURL == "" {
		m.IconURL = mod.icon()
	}
	name := cmpOr(m.Title, m.Project)
	inspect := func(f *curseForgeFile) int {
		if m.Kind == avsysv1.KindDatapack || f.DownloadURL == "" {
			return 0
		}
		java, err := javaOfURL(ctx, f.DownloadURL)
		if err != nil {
			s.Log.Warn("could not inspect mod file", "project", m.Project, "file", f.ID, "error", err)
		}
		return java
	}
	var chosen *curseForgeFile
	java := 0
	if m.Version != "" {
		fileID, err := strconv.Atoi(m.Version)
		if err != nil {
			return errBadRequest("%s has no file %q on CurseForge.", name, m.Version)
		}
		chosen, err = s.curseForgeFile(ctx, id, fileID)
		if errors.Is(err, errNotFound) {
			return errBadRequest("%s has no file %q on CurseForge.", name, m.Version)
		}
		if err != nil {
			return err
		}
		if chosen.DownloadURL == "" {
			return errOnlyOnCurseForge(name, mod.page())
		}
		java = inspect(chosen)
	} else {
		files, err := s.curseForgeFiles(ctx, id, t.curseForgeLoadersFor(m.Kind), t.gameVersion)
		if err != nil {
			return err
		}
		files = preferReleaseFiles(files)
		if len(files) == 0 {
			return errBadRequest("%s has no file for %s.", name, t)
		}
		if files[0].DownloadURL == "" {
			return errOnlyOnCurseForge(name, mod.page())
		}
		// As for Modrinth: the newest that runs on the game's own Java,
		// else the newest, and the server's Java raised to it.
		chosen, java = &files[0], -1
		for i := range min(len(files), candidatesToInspect) {
			if files[i].DownloadURL == "" {
				continue
			}
			needs := inspect(&files[i])
			if java == -1 {
				java = needs
			}
			if t.java == 0 || needs <= t.java {
				chosen, java = &files[i], needs
				break
			}
		}
		java = max(java, 0)
	}
	m.Version, m.VersionName, m.Java = strconv.Itoa(chosen.ID), chosen.DisplayName, int32(java)
	m.Provides = idsOfURL(chosen.DownloadURL)
	if m.Kind == avsysv1.KindDatapack {
		m.File = chosen.FileName
	}
	return nil
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// preferReleaseFiles drops betas and alphas when a release is there.
func preferReleaseFiles(files []curseForgeFile) []curseForgeFile {
	var releases []curseForgeFile
	for _, f := range files {
		if f.ReleaseType == 1 {
			releases = append(releases, f)
		}
	}
	if len(releases) > 0 {
		return releases
	}
	return files
}

// pinCurseForgeModpack records the file of a CurseForge modpack and what it
// is built for.
func (s *Server) pinCurseForgeModpack(ctx context.Context, pack *avsysv1.ModpackSpec) error {
	name := cmpOr(pack.Title, pack.Project)
	id, err := s.curseForgeProjectID(ctx, pack.Project)
	if errors.Is(err, errNotFound) {
		return errBadRequest("%s was not found on CurseForge.", name)
	}
	if err != nil {
		return err
	}
	mod, err := s.curseForgeMod(ctx, id)
	if err != nil {
		return err
	}
	// The image is told of a pack by its slug.
	pack.Project = mod.Slug
	if pack.Title == "" {
		pack.Title = mod.Name
	}
	if pack.IconURL == "" {
		pack.IconURL = mod.icon()
	}
	var chosen *curseForgeFile
	if pack.Version != "" {
		fileID, err := strconv.Atoi(pack.Version)
		if err != nil {
			return errBadRequest("%s has no file %q on CurseForge.", name, pack.Version)
		}
		if chosen, err = s.curseForgeFile(ctx, id, fileID); errors.Is(err, errNotFound) {
			return errBadRequest("%s has no file %q on CurseForge.", name, pack.Version)
		} else if err != nil {
			return err
		}
	} else {
		files, err := s.curseForgeFiles(ctx, id, nil, "")
		if err != nil {
			return err
		}
		if files = preferReleaseFiles(files); len(files) == 0 {
			return errBadRequest("%s has no published versions.", name)
		}
		chosen = &files[0]
	}
	if chosen.DownloadURL == "" {
		return errOnlyOnCurseForge(name, mod.page())
	}
	pack.Version, pack.VersionName = strconv.Itoa(chosen.ID), chosen.DisplayName
	pack.GameVersion, pack.Loader = "", ""
	if games := chosen.games(); len(games) > 0 {
		pack.GameVersion = games[len(games)-1]
	}
	if loaders := chosen.loaders(); len(loaders) > 0 {
		pack.Loader = loaders[0]
	}
	return nil
}

// readCurseForgePack reads what a version of a CurseForge modpack brings
// from its manifest.json, with each file's project looked up: its name,
// picture and page, and whether its author allows downloads of it only on
// CurseForge itself, in which case it is to be put in by hand.
func (s *Server) readCurseForgePack(ctx context.Context, pack *avsysv1.ModpackSpec) (*modpackRead, error) {
	id, err := s.curseForgeProjectID(ctx, pack.Project)
	if err != nil {
		return nil, err
	}
	fileID, err := strconv.Atoi(pack.Version)
	if err != nil {
		return &modpackRead{files: []packEntry{}}, nil
	}
	file, err := s.curseForgeFile(ctx, id, fileID)
	if err != nil {
		return nil, err
	}
	if file.DownloadURL == "" {
		return &modpackRead{files: []packEntry{}}, nil
	}
	r, size, done, err := openRemoteZip(ctx, s.catalog.http, s.catalog.userAgent, file.DownloadURL)
	if err != nil {
		return nil, err
	}
	defer done()
	watched := &watchedReader{r: r}
	manifest := readCurseForgeManifest(watched, size)
	if watched.err != nil {
		return nil, watched.err
	}
	out := &modpackRead{files: []packEntry{}}
	if len(manifest) == 0 {
		return out, nil
	}
	var fileIDs, modIDs []int
	for _, f := range manifest {
		fileIDs, modIDs = append(fileIDs, f.FileID), append(modIDs, f.ProjectID)
	}
	files := map[int]curseForgeFile{}
	mods := map[int]curseForgeMod{}
	for start := 0; start < len(fileIDs); start += 500 {
		end := min(start+500, len(fileIDs))
		list, err := readCurseForge[curseForgeList[curseForgeFile]](s.curseForge(ctx, "/v1/mods/files", map[string]any{"fileIds": fileIDs[start:end]}, time.Hour))
		if err != nil {
			return nil, err
		}
		for _, f := range list.Data {
			files[f.ID] = f
		}
		found, err := s.curseForgeMods(ctx, append([]int(nil), modIDs[start:end]...))
		if err != nil {
			return nil, err
		}
		for _, m := range found {
			mods[m.ID] = m
		}
	}
	for _, ref := range manifest {
		mod, known := mods[ref.ProjectID]
		f := files[ref.FileID]
		name := f.FileName
		if name == "" {
			name = fmt.Sprintf("%d.jar", ref.FileID)
		}
		folder := "mods"
		if known {
			if dir, ok := curseForgeFolders[mod.ClassID]; ok {
				folder = dir
			}
		}
		e := packEntry{
			Path: folder + "/" + name, Server: true, Source: string(avsysv1.SourceCurseForge),
			Project: strconv.Itoa(ref.ProjectID), Version: strconv.Itoa(ref.FileID),
		}
		if known {
			e.Slug, e.Title, e.IconURL, e.Page = mod.Slug, mod.Name, mod.icon(), mod.page()
		} else {
			e.Page = curseForgeProjectPage(e.Project)
		}
		// Without an address of its own the image cannot fetch it either.
		e.Manual = (f.ID != 0 && f.DownloadURL == "") || (known && !mod.downloadable())
		out.files = append(out.files, e)
	}
	return out, nil
}

type curseForgeManifestFile struct {
	ProjectID int  `json:"projectID"`
	FileID    int  `json:"fileID"`
	Required  bool `json:"required"`
}

// readCurseForgeManifest reads the files a CurseForge modpack's
// manifest.json lists. A pack that is not one lists nothing.
func readCurseForgeManifest(r io.ReaderAt, size int64) []curseForgeManifestFile {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return nil
	}
	for _, f := range zr.File {
		if f.Name != "manifest.json" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil
		}
		defer rc.Close()
		var manifest struct {
			Files []curseForgeManifestFile `json:"files"`
		}
		if json.NewDecoder(io.LimitReader(rc, maxIndex)).Decode(&manifest) != nil {
			return nil
		}
		var out []curseForgeManifestFile
		for _, file := range manifest.Files {
			if file.ProjectID > 0 && file.FileID > 0 {
				out = append(out, file)
			}
		}
		return out
	}
	return nil
}
