package api

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/GT-Drehmal/avSYS/internal/store"
)

// catalog proxies the public content APIs the UI browses: Modrinth and
// Mojang's version list. Going through the panel keeps third-party requests
// off people's browsers and lets responses be cached for everyone.
type catalog struct {
	http      *http.Client
	userAgent string

	mu      sync.Mutex
	entries map[string]cacheEntry
}

type cacheEntry struct {
	body    []byte
	expires time.Time
}

func newCatalog(version string) *catalog {
	return &catalog{
		http: &http.Client{Timeout: 15 * time.Second},
		// Modrinth asks for a User-Agent that identifies the application.
		userAgent: "GT-Drehmal/avSYS/" + version,
		entries:   map[string]cacheEntry{},
	}
}

const catalogCacheSize = 512

// get fetches a URL as JSON, serving repeats from memory for ttl.
func (c *catalog) get(ctx context.Context, u string, ttl time.Duration) ([]byte, error) {
	c.mu.Lock()
	if e, ok := c.entries[u]; ok && time.Now().Before(e.expires) {
		c.mu.Unlock()
		return e.body, nil
	}
	c.mu.Unlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.userAgent)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, &Error{Status: http.StatusBadGateway, Code: "upstream", Message: "Could not reach " + req.URL.Host + "."}
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, errNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &Error{Status: http.StatusBadGateway, Code: "upstream", Message: fmt.Sprintf("%s answered %s.", req.URL.Host, resp.Status)}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	if len(c.entries) >= catalogCacheSize {
		clear(c.entries)
	}
	c.entries[u] = cacheEntry{body: body, expires: time.Now().Add(ttl)}
	c.mu.Unlock()
	return body, nil
}

func writeRawJSON(w http.ResponseWriter, body []byte, maxAge time.Duration) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "private, max-age="+strconv.Itoa(int(maxAge.Seconds())))
	_, _ = w.Write(body)
}

// modrinthLoaders maps what a server runs to the loader names Modrinth files
// are tagged with. Plugin servers accept plugins built for their ancestors.
var modrinthLoaders = map[string][]string{
	"fabric":   {"fabric"},
	"quilt":    {"quilt", "fabric"},
	"forge":    {"forge"},
	"neoforge": {"neoforge"},
	"paper":    {"paper", "spigot", "bukkit"},
	"purpur":   {"purpur", "paper", "spigot", "bukkit"},
	"folia":    {"folia"},
}

func facet(prefix string, values ...string) []string {
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = prefix + ":" + v
	}
	return out
}

// modrinthOrder maps the orders the panel offers to Modrinth's names for them.
var modrinthOrder = map[string]string{
	"":          "relevance",
	"relevance": "relevance",
	"downloads": "downloads",
	"follows":   "follows",
	"newest":    "newest",
	"updated":   "updated",
}

// tagPattern is what a category is named like. They go into a search
// filter, so nothing that could be read as part of one is let through.
var tagPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9+_-]{0,39}$`)

// modrinthSearchParams turns a search as the panel is asked for it into one
// Modrinth understands.
func modrinthSearchParams(q url.Values) (url.Values, error) {
	kind := q.Get("type")
	switch kind {
	case "mod", "plugin", "datapack", "modpack", "resourcepack":
	default:
		return nil, errBadRequest("Unknown content type.")
	}
	order, ok := modrinthOrder[q.Get("sort")]
	if !ok {
		return nil, errBadRequest("Results are sorted by relevance, downloads, follows, newest or updated.")
	}
	facets := [][]string{facet("project_type", kind)}
	// A datapack is loader-independent, and a resource pack is not installed
	// on the server at all, so neither is filtered by loader or side.
	if kind == "mod" || kind == "plugin" || kind == "modpack" {
		if loaders := modrinthLoaders[q.Get("loader")]; len(loaders) > 0 {
			facets = append(facets, facet("categories", loaders...))
		}
	}
	// Mods that only do something on the player's own game are left out,
	// unless they are asked for.
	if (kind == "mod" || kind == "modpack") && q.Get("side") != "any" {
		facets = append(facets, facet("server_side", "required", "optional"))
	}
	if v := q.Get("gameVersion"); v != "" && v != "LATEST" {
		facets = append(facets, facet("versions", v))
	}
	// Each category narrows the search further: a result has all of them.
	for _, category := range strings.Split(q.Get("categories"), ",") {
		if category == "" {
			continue
		}
		if !tagPattern.MatchString(category) {
			return nil, errBadRequest("%q is not a category.", category)
		}
		facets = append(facets, facet("categories", category))
	}
	// Past the last result that can be asked for there is nothing, and saying
	// so is what ends a list that loads as it is scrolled: answering with the
	// last page again would have it grow for ever.
	offset := max(0, atoi(q.Get("offset")))
	if offset > modrinthLastOffset {
		return nil, errBadRequest("A search goes as far as its first %d results. Narrow it down to see others.", modrinthLastOffset+modrinthPage)
	}
	encoded, err := json.Marshal(facets)
	if err != nil {
		return nil, err
	}
	return url.Values{
		"query":  {q.Get("q")},
		"facets": {string(encoded)},
		"limit":  {strconv.Itoa(modrinthPage)},
		"offset": {strconv.Itoa(offset)},
		"index":  {order},
	}, nil
}

const (
	// modrinthPage is how many results a search returns at a time.
	modrinthPage = 24
	// modrinthLastOffset is the furthest into a search's results a page may start.
	modrinthLastOffset = 9_984
)

func (s *Server) modrinthSearch(w http.ResponseWriter, r *http.Request, _ *store.User) error {
	params, err := modrinthSearchParams(r.URL.Query())
	if err != nil {
		return err
	}
	body, err := s.catalog.get(r.Context(), "https://api.modrinth.com/v2/search?"+params.Encode(), 5*time.Minute)
	if err != nil {
		return err
	}
	writeRawJSON(w, body, time.Minute)
	return nil
}

// modrinthTagType is the kind of project Modrinth files a kind of content's
// categories under: mods, plugins and datapacks share one list.
var modrinthTagType = map[string]string{
	"mod": "mod", "plugin": "mod", "datapack": "mod",
	"modpack": "modpack", "resourcepack": "resourcepack",
}

// modrinthCategories lists the categories a kind of content is sorted into,
// as Modrinth has them today.
func (s *Server) modrinthCategories(w http.ResponseWriter, r *http.Request, _ *store.User) error {
	tagType, ok := modrinthTagType[r.URL.Query().Get("type")]
	if !ok {
		return errBadRequest("Unknown content type.")
	}
	body, err := s.catalog.get(r.Context(), "https://api.modrinth.com/v2/tag/category", time.Hour)
	if err != nil {
		return err
	}
	names, err := categoriesOf(body, tagType)
	if err != nil {
		return &Error{Status: http.StatusBadGateway, Code: "upstream", Message: "Modrinth's list of categories could not be read."}
	}
	w.Header().Set("Cache-Control", "private, max-age=3600")
	writeJSON(w, http.StatusOK, names)
	return nil
}

// categoriesOf picks one kind of project's categories out of Modrinth's list
// of all tags. Its other groups, such as a resource pack's resolution, are
// left out, as are the loaders, which Modrinth also counts as categories.
func categoriesOf(body []byte, tagType string) ([]string, error) {
	var tags []struct {
		Name        string `json:"name"`
		ProjectType string `json:"project_type"`
		Header      string `json:"header"`
	}
	if err := json.Unmarshal(body, &tags); err != nil {
		return nil, err
	}
	names := []string{}
	for _, t := range tags {
		if t.ProjectType == tagType && t.Header == "categories" && tagPattern.MatchString(t.Name) {
			names = append(names, t.Name)
		}
	}
	sort.Strings(names)
	return names, nil
}

// modrinthIDPattern is what a Modrinth project ID looks like.
var modrinthIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// modrinthProjects names several projects at once, which is how what a mod
// depends on is shown by name: a version only lists the IDs.
func (s *Server) modrinthProjects(w http.ResponseWriter, r *http.Request, _ *store.User) error {
	ids := strings.Split(r.URL.Query().Get("ids"), ",")
	if len(ids) > 50 {
		return errBadRequest("At most 50 projects at a time.")
	}
	for _, id := range ids {
		if !modrinthIDPattern.MatchString(id) {
			return errBadRequest("%q is not a project.", id)
		}
	}
	sort.Strings(ids)
	encoded, _ := json.Marshal(ids)
	body, err := s.catalog.get(r.Context(), "https://api.modrinth.com/v2/projects?"+url.Values{"ids": {string(encoded)}}.Encode(), 5*time.Minute)
	if err != nil {
		return err
	}
	var projects []struct {
		ID          string  `json:"id"`
		Slug        string  `json:"slug"`
		Title       string  `json:"title"`
		IconURL     *string `json:"icon_url"`
		ProjectType string  `json:"project_type"`
	}
	if err := json.Unmarshal(body, &projects); err != nil {
		return &Error{Status: http.StatusBadGateway, Code: "upstream", Message: "Modrinth's answer could not be read."}
	}
	w.Header().Set("Cache-Control", "private, max-age=60")
	writeJSON(w, http.StatusOK, projects)
	return nil
}

func atoi(v string) int {
	n, _ := strconv.Atoi(v)
	return n
}

func (s *Server) modrinthVersions(w http.ResponseWriter, r *http.Request, _ *store.User) error {
	q := r.URL.Query()
	// Nothing shown from this list is a changelog, and on a project with
	// years of versions they are most of the answer.
	params := url.Values{"include_changelog": {"false"}}
	if loaders := modrinthLoaders[q.Get("loader")]; len(loaders) > 0 {
		encoded, _ := json.Marshal(loaders)
		params.Set("loaders", string(encoded))
	} else if q.Get("loader") == "datapack" {
		params.Set("loaders", `["datapack"]`)
	} else if q.Get("loader") == "resourcepack" {
		// Modrinth files resource packs under the game itself.
		params.Set("loaders", `["minecraft"]`)
	}
	if v := q.Get("gameVersion"); v != "" && v != "LATEST" {
		encoded, _ := json.Marshal([]string{v})
		params.Set("game_versions", string(encoded))
	}
	u := "https://api.modrinth.com/v2/project/" + url.PathEscape(r.PathValue("id")) + "/version?" + params.Encode()
	body, err := s.catalog.get(r.Context(), u, 5*time.Minute)
	if err != nil {
		return err
	}
	writeRawJSON(w, body, time.Minute)
	return nil
}

// publicOnly refuses connections to addresses that are not on the public
// internet. hashURL fetches URLs chosen by users; without this it could be
// pointed at services inside the cluster or on the host's network.
func publicOnly(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if ip == nil || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() {
		return errors.New("address is not public")
	}
	return nil
}

var publicHTTP = &http.Client{
	Timeout: 2 * time.Minute,
	Transport: &http.Transport{
		DialContext: (&net.Dialer{Timeout: 10 * time.Second, Control: publicOnly}).DialContext,
	},
}

const maxResourcePack = 512 << 20

// hashURL downloads a resource pack and returns its SHA-1, which the game
// needs to cache the pack and notice when it changes.
func (s *Server) hashURL(w http.ResponseWriter, r *http.Request, _ *store.User) error {
	var in struct{ URL string }
	if err := decode(r, &in); err != nil {
		return err
	}
	target, err := url.Parse(strings.TrimSpace(in.URL))
	if err != nil || (target.Scheme != "http" && target.Scheme != "https") || target.Host == "" {
		return errBadRequest("Enter an http:// or https:// URL.")
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, target.String(), nil)
	if err != nil {
		return err
	}
	resp, err := publicHTTP.Do(req)
	if err != nil {
		return errBadRequest("Could not download that URL.")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return errBadRequest("That URL answered %s.", resp.Status)
	}
	// Kept a moment where the panel keeps packs, to read which formats it
	// is made for: a zip is read from its end.
	var keep *os.File
	if s.Config.PackDir != "" {
		if err := os.MkdirAll(s.Config.PackDir, 0o750); err == nil {
			keep, _ = os.CreateTemp(s.Config.PackDir, "fetch-*")
		}
	}
	h := sha1.New()
	to := io.Writer(h)
	// The copy is only for reading the formats: one that cannot be written,
	// as with the disk full, is given up, and the hash goes on.
	var copied *bestEffort
	if keep != nil {
		defer os.Remove(keep.Name())
		defer keep.Close()
		copied = &bestEffort{w: keep}
		to = io.MultiWriter(h, copied)
	}
	n, err := io.Copy(to, io.LimitReader(resp.Body, maxResourcePack+1))
	if err != nil {
		return errBadRequest("The download was interrupted.")
	}
	if n > maxResourcePack {
		return errBadRequest("That file is larger than 512 MB.")
	}
	out := map[string]any{"sha1": hex.EncodeToString(h.Sum(nil)), "size": n}
	// Not being a pack is not refused here: the address may be one the
	// game is given anyway.
	if keep != nil && copied.err == nil {
		if formats, err := checkPack(keep, n); err == nil && formats != nil {
			out["formats"] = formats
		}
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// bestEffort writes to w until a write fails, and from then on takes what it
// is given without writing it, remembering the failure.
type bestEffort struct {
	w   io.Writer
	err error
}

func (b *bestEffort) Write(p []byte) (int, error) {
	if b.err == nil {
		_, b.err = b.w.Write(p)
	}
	return len(p), nil
}
