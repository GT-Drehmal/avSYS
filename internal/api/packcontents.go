package api

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
	"github.com/GT-Drehmal/avSYS/internal/controller"
	"github.com/GT-Drehmal/avSYS/internal/store"
)

// What a modpack brings is read from its own index, modrinth.index.json:
// for one uploaded, from its file on the server's volume; for one from
// Modrinth, from the .mrpack of its pinned version, of which only the index
// is fetched, by ranges, as a zip is read from its end. Modrinth lists a
// pack version's mods only where its author said them, so the pack itself
// is what is read. Each pack is read once: an uploaded one is known by its
// SHA-1 and a version on Modrinth never changes.

// packEntry is a file a modpack lists, as the Content page shows it.
type packEntry struct {
	Path string `json:"path"`
	// Server is false for a file for players only, which the server leaves out.
	Server bool `json:"server"`
	// Project and Version are the Modrinth project and version the file is
	// downloaded from, where it is from Modrinth; Slug, Title and IconURL
	// are the project's, where the catalog knows it.
	Project string `json:"project,omitempty"`
	Version string `json:"version,omitempty"`
	Slug    string `json:"slug,omitempty"`
	Title   string `json:"title,omitempty"`
	IconURL string `json:"iconUrl,omitempty"`
	// Source is "curseforge" for a file of a pack from CurseForge, whose
	// Project and Version are CurseForge's IDs, and Page its project's page.
	Source string `json:"source,omitempty"`
	Page   string `json:"page,omitempty"`
	// Manual is set for a file whose author allows downloads of it only on
	// CurseForge itself: it has to be put in by hand.
	Manual bool `json:"manual,omitempty"`
}

// modpackRead is what is read of a pack: the files it lists and its picture.
type modpackRead struct {
	files []packEntry
	icon  []byte
}

// packIcons are where a pack's picture may be, the one the server shows first.
var packIcons = []string{"server-overrides/server-icon.png", "overrides/server-icon.png", "overrides/icon.png", "icon.png"}

// modrinthCDNFile is a file on Modrinth's CDN; the project it is of is its
// first part, and the version, where it says, the next.
var modrinthCDNFile = regexp.MustCompile(`^https://cdn\.modrinth\.com/data/([A-Za-z0-9]{8})/(?:versions/([A-Za-z0-9]{8})/)?`)

// readPackZip reads a pack's index and picture. A pack that is not one
// lists nothing. A failure to read the file itself, as of the connection it
// is read over, is said, so that what was read is not kept.
func readPackZip(r io.ReaderAt, size int64) (*modpackRead, error) {
	watched := &watchedReader{r: r}
	read := readPackFrom(watched, size)
	if watched.err != nil {
		return nil, watched.err
	}
	return read, nil
}

// watchedReader keeps the first failure of its reader other than its end.
type watchedReader struct {
	r   io.ReaderAt
	err error
}

func (w *watchedReader) ReadAt(p []byte, off int64) (int, error) {
	n, err := w.r.ReadAt(p, off)
	if err != nil && err != io.EOF && w.err == nil {
		w.err = err
	}
	return n, err
}

func readPackFrom(r io.ReaderAt, size int64) *modpackRead {
	out := &modpackRead{files: []packEntry{}}
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return out
	}
	files := map[string]*zip.File{}
	for _, f := range zr.File {
		files[f.Name] = f
	}
	if f := files["modrinth.index.json"]; f != nil {
		if rc, err := f.Open(); err == nil {
			var idx struct {
				Files []struct {
					Path      string            `json:"path"`
					Env       map[string]string `json:"env"`
					Downloads []string          `json:"downloads"`
				} `json:"files"`
			}
			if json.NewDecoder(io.LimitReader(rc, maxIndex)).Decode(&idx) == nil {
				for _, f := range idx.Files {
					p := strings.TrimSpace(strings.ReplaceAll(f.Path, "\\", "/"))
					if p == "" || strings.HasSuffix(p, "/") {
						continue
					}
					e := packEntry{Path: p, Server: f.Env["server"] != "unsupported"}
					for _, d := range f.Downloads {
						if m := modrinthCDNFile.FindStringSubmatch(d); m != nil {
							e.Project, e.Version = m[1], m[2]
							break
						}
					}
					out.files = append(out.files, e)
				}
			}
			rc.Close()
		}
	}
	for _, name := range packIcons {
		f := files[name]
		if f == nil || f.UncompressedSize64 > maxPictureBytes {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			continue
		}
		// The pack's, which could be anything: redrawn like any upload.
		data, err := squarePNG(rc, serverIconSide)
		rc.Close()
		if err == nil {
			out.icon = data
			break
		}
	}
	return out
}

// packReads holds what was read of each pack, by the pack: an uploaded one
// by its server and SHA-1, as anyone with the server's files can change the
// file; one from Modrinth by its version.
type packReads struct {
	mu    sync.Mutex
	packs map[string]*modpackRead
}

// packReadsKept bounds how many packs are remembered.
const packReadsKept = 128

func (c *packReads) get(key string) *modpackRead {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.packs[key]
}

func (c *packReads) set(key string, p *modpackRead) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.packs == nil || len(c.packs) >= packReadsKept {
		c.packs = map[string]*modpackRead{}
	}
	c.packs[key] = p
}

// packReadTime bounds reading a pack.
const packReadTime = 20 * time.Second

// readModpack reads a server's modpack, or says why it cannot just now. A
// pack that is there but is not one reads as listing nothing, and is
// remembered as such.
func (s *Server) readModpack(ctx context.Context, mc *avsysv1.MinecraftServer) (*modpackRead, error) {
	p := mc.Spec.Modpack
	if p == nil {
		return &modpackRead{files: []packEntry{}}, nil
	}
	var key string
	switch {
	case p.UploadedFile() != "":
		key = "upload:" + mc.Namespace + "/" + mc.Name + ":" + p.Version
	case p.Source == avsysv1.SourceModrinth && p.Version != "":
		key = "modrinth:" + p.Version
	case p.Source == avsysv1.SourceCurseForge && p.Version != "":
		key = "curseforge:" + p.Version
	default:
		return &modpackRead{files: []packEntry{}}, nil
	}
	if got := s.packReads.get(key); got != nil {
		return got, nil
	}
	ctx, cancel := context.WithTimeout(ctx, packReadTime)
	defer cancel()
	var read *modpackRead
	var err error
	switch p.Source {
	case avsysv1.SourceUpload:
		read, err = s.readUploadedPack(ctx, mc)
	case avsysv1.SourceCurseForge:
		read, err = s.readCurseForgePack(ctx, p)
	default:
		read, err = s.readModrinthPack(ctx, p.Version)
	}
	if err != nil {
		return nil, err
	}
	s.packReads.set(key, read)
	return read, nil
}

// readUploadedPack reads an uploaded pack from its file on the server's volume.
func (s *Server) readUploadedPack(ctx context.Context, mc *avsysv1.MinecraftServer) (*modpackRead, error) {
	type result struct {
		read *modpackRead
		err  error
	}
	done := make(chan result, 1)
	go func() {
		// A workspace whose pod is not up is not waited for, as for the
		// server's picture: this is asked again on the next visit.
		sc, err := s.workspaces.getNow(ctx, mc.Namespace, mc.Name)
		if err != nil {
			done <- result{err: err}
			return
		}
		f, err := sc.Open(path.Join(controller.ContentDir, mc.Spec.Modpack.UploadedFile()))
		if err != nil {
			done <- result{err: err}
			return
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil {
			done <- result{err: err}
			return
		}
		read, err := readPackZip(f, info.Size())
		done <- result{read: read, err: err}
	}()
	select {
	case r := <-done:
		return r.read, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// readModrinthPack reads the .mrpack of a version of a pack on Modrinth.
func (s *Server) readModrinthPack(ctx context.Context, version string) (*modpackRead, error) {
	v, err := s.modrinthVersion(ctx, version)
	if err != nil {
		return nil, err
	}
	u := v.file()
	// Only Modrinth's own files are fetched.
	if !strings.HasPrefix(u, "https://cdn.modrinth.com/") {
		return &modpackRead{files: []packEntry{}}, nil
	}
	r, size, done, err := openRemoteZip(ctx, s.catalog.http, s.catalog.userAgent, u)
	if err != nil {
		return nil, err
	}
	defer done()
	return readPackZip(r, size)
}

// remoteZipBlock is the least fetched at a time, and remoteZipMost the most
// fetched of one file: an index is at most maxIndex.
const (
	remoteZipBlock = 256 << 10
	remoteZipMost  = maxIndex + 8<<20
	// remoteZipWhole is the largest file fetched whole, from a host that
	// does not send parts of one.
	remoteZipWhole = 512 << 20
)

// remoteZip reads a file over HTTP by ranges, keeping what it fetched.
type remoteZip struct {
	ctx       context.Context
	client    *http.Client
	userAgent string
	url       string
	size      int64
	fetched   int64
	parts     []remotePart
}

type remotePart struct {
	at   int64
	data []byte
}

var errRemoteZip = errors.New("the pack could not be read")

// openRemoteZip opens a file over HTTP to be read as a zip. Its end, where
// a zip's directory is, is fetched first. From a host that does not send
// parts of a file, it is fetched whole, to a temporary file, up to a size.
func openRemoteZip(ctx context.Context, client *http.Client, userAgent, u string) (io.ReaderAt, int64, func(), error) {
	resp, err := rangeGet(ctx, client, userAgent, u, fmt.Sprintf("bytes=-%d", remoteZipBlock))
	if err != nil {
		return nil, 0, nil, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusPartialContent:
		total, ok := totalOf(resp.Header.Get("Content-Range"))
		if !ok {
			return nil, 0, nil, errRemoteZip
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, remoteZipBlock+1))
		if err != nil || int64(len(data)) > total {
			return nil, 0, nil, errRemoteZip
		}
		z := &remoteZip{ctx: ctx, client: client, userAgent: userAgent, url: u, size: total, fetched: int64(len(data))}
		z.parts = append(z.parts, remotePart{at: total - int64(len(data)), data: data})
		return z, total, func() {}, nil
	case http.StatusOK:
		if resp.ContentLength > remoteZipWhole {
			return nil, 0, nil, errRemoteZip
		}
		f, err := os.CreateTemp("", "avsys-mrpack-*")
		if err != nil {
			return nil, 0, nil, err
		}
		done := func() {
			f.Close()
			os.Remove(f.Name())
		}
		n, err := io.Copy(f, io.LimitReader(resp.Body, remoteZipWhole+1))
		if err != nil || n > remoteZipWhole {
			done()
			return nil, 0, nil, errRemoteZip
		}
		return f, n, done, nil
	default:
		return nil, 0, nil, errRemoteZip
	}
}

func rangeGet(ctx context.Context, client *http.Client, userAgent, u, ranges string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Range", ranges)
	return client.Do(req)
}

// totalOf reads the whole size from a Content-Range: "bytes 0-9/100".
func totalOf(contentRange string) (int64, bool) {
	_, total, ok := strings.Cut(contentRange, "/")
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseInt(total, 10, 64)
	return n, err == nil && n > 0
}

func (z *remoteZip) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, errRemoteZip
	}
	if off >= z.size {
		return 0, io.EOF
	}
	want := p
	if rest := z.size - off; int64(len(want)) > rest {
		want = want[:rest]
	}
	for _, part := range z.parts {
		if off >= part.at && off+int64(len(want)) <= part.at+int64(len(part.data)) {
			n := copy(want, part.data[off-part.at:])
			if n < len(p) {
				return n, io.EOF
			}
			return n, nil
		}
	}
	end := min(off+max(int64(len(want)), remoteZipBlock), z.size)
	if z.fetched+(end-off) > remoteZipMost {
		return 0, errRemoteZip
	}
	resp, err := rangeGet(z.ctx, z.client, z.userAgent, z.url, fmt.Sprintf("bytes=%d-%d", off, end-1))
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent {
		return 0, errRemoteZip
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, end-off+1))
	if err != nil || int64(len(data)) != end-off {
		return 0, errRemoteZip
	}
	z.fetched += int64(len(data))
	z.parts = append(z.parts, remotePart{at: off, data: data})
	n := copy(want, data)
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

// modrinthProjectsOf looks up the projects of a pack's files in the
// catalog: their slugs, names and pictures. What cannot be looked up just
// now goes without.
func (s *Server) modrinthProjectsOf(ctx context.Context, files []packEntry) map[string]struct{ slug, title, icon string } {
	seen := map[string]bool{}
	var ids []string
	for _, f := range files {
		if f.Source == "" && f.Project != "" && !seen[f.Project] {
			seen[f.Project] = true
			ids = append(ids, f.Project)
		}
	}
	sort.Strings(ids)
	out := map[string]struct{ slug, title, icon string }{}
	for start := 0; start < len(ids); start += 100 {
		chunk := ids[start:min(start+100, len(ids))]
		encoded, _ := json.Marshal(chunk)
		body, err := s.catalog.get(ctx, "https://api.modrinth.com/v2/projects?"+url.Values{"ids": {string(encoded)}}.Encode(), time.Hour)
		if err != nil {
			continue
		}
		var projects []struct {
			ID      string `json:"id"`
			Slug    string `json:"slug"`
			Title   string `json:"title"`
			IconURL string `json:"icon_url"`
		}
		if json.Unmarshal(body, &projects) != nil {
			continue
		}
		for _, p := range projects {
			if seen[p.ID] {
				out[p.ID] = struct{ slug, title, icon string }{p.Slug, p.Title, p.IconURL}
			}
		}
	}
	return out
}

// getModpackContents lists the files a server's modpack brings, with the
// Modrinth project of each where it is known. A pack that cannot be read
// lists nothing.
func (s *Server) getModpackContents(w http.ResponseWriter, r *http.Request, u *store.User) error {
	_, mc, err := s.server(r, u, store.RoleViewer)
	if err != nil {
		return err
	}
	files := []packEntry{}
	read, err := s.readModpack(r.Context(), mc)
	if err != nil {
		s.Log.Debug("modpack not read", "server", mc.Name, "error", err)
	} else {
		projects := s.modrinthProjectsOf(r.Context(), read.files)
		for _, f := range read.files {
			if p, ok := projects[f.Project]; ok && f.Source == "" {
				f.Slug, f.Title, f.IconURL = p.slug, p.title, p.icon
			}
			files = append(files, f)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"files": files})
	return nil
}

// getModpackIcon is the picture in a server's uploaded modpack.
func (s *Server) getModpackIcon(w http.ResponseWriter, r *http.Request, u *store.User) error {
	_, mc, err := s.server(r, u, store.RoleViewer)
	if err != nil {
		return err
	}
	if mc.Spec.Modpack.UploadedFile() == "" {
		return errNotFound
	}
	read, err := s.readModpack(r.Context(), mc)
	if err != nil || read.icon == nil {
		return errNotFound
	}
	servePicture(w, r, read.icon)
	return nil
}
