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

func (s *Server) modrinthSearch(w http.ResponseWriter, r *http.Request, _ *store.User) error {
	q := r.URL.Query()
	kind := q.Get("type")
	switch kind {
	case "mod", "plugin", "datapack", "modpack", "resourcepack":
	default:
		return errBadRequest("Unknown content type.")
	}
	facets := [][]string{facet("project_type", kind)}
	// A datapack is loader-independent, and a resource pack is not installed
	// on the server at all, so neither is filtered by loader or side.
	if kind == "mod" || kind == "plugin" || kind == "modpack" {
		if loaders := modrinthLoaders[q.Get("loader")]; len(loaders) > 0 {
			facets = append(facets, facet("categories", loaders...))
		}
	}
	if kind == "mod" || kind == "modpack" {
		facets = append(facets, facet("server_side", "required", "optional"))
	}
	if v := q.Get("gameVersion"); v != "" && v != "LATEST" {
		facets = append(facets, facet("versions", v))
	}
	encoded, err := json.Marshal(facets)
	if err != nil {
		return err
	}
	params := url.Values{
		"query":  {q.Get("q")},
		"facets": {string(encoded)},
		"limit":  {"24"},
		"offset": {strconv.Itoa(max(0, atoi(q.Get("offset"))))},
		"index":  {"relevance"},
	}
	body, err := s.catalog.get(r.Context(), "https://api.modrinth.com/v2/search?"+params.Encode(), 5*time.Minute)
	if err != nil {
		return err
	}
	writeRawJSON(w, body, time.Minute)
	return nil
}

func atoi(v string) int {
	n, _ := strconv.Atoi(v)
	return n
}

func (s *Server) modrinthVersions(w http.ResponseWriter, r *http.Request, _ *store.User) error {
	q := r.URL.Query()
	params := url.Values{}
	if loaders := modrinthLoaders[q.Get("loader")]; len(loaders) > 0 {
		encoded, _ := json.Marshal(loaders)
		params.Set("loaders", string(encoded))
	} else if q.Get("loader") == "datapack" {
		params.Set("loaders", `["datapack"]`)
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
	h := sha1.New()
	n, err := io.Copy(h, io.LimitReader(resp.Body, maxResourcePack+1))
	if err != nil {
		return errBadRequest("The download was interrupted.")
	}
	if n > maxResourcePack {
		return errBadRequest("That file is larger than 512 MB.")
	}
	writeJSON(w, http.StatusOK, map[string]any{"sha1": hex.EncodeToString(h.Sum(nil)), "size": n})
	return nil
}
