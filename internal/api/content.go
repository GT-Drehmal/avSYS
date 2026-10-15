package api

import (
	"archive/zip"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/pkg/sftp"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
	"github.com/GT-Drehmal/avSYS/internal/controller"
	"github.com/GT-Drehmal/avSYS/internal/store"
)

// Content is resolved when it is added, not when the server starts. Each
// Modrinth entry is pinned to one version, so restarts never change what is
// installed, and the file is read to learn which Java it was compiled for, so
// the server can be given a Java that runs it. A plugin can list a game
// version as supported and still need a newer Java than that version ships
// with; without this it would download fine and then fail to load.

type modrinthFile struct {
	URL      string `json:"url"`
	Filename string `json:"filename"`
	Primary  bool   `json:"primary"`
}

type modrinthVersion struct {
	ID            string         `json:"id"`
	VersionNumber string         `json:"version_number"`
	VersionType   string         `json:"version_type"`
	GameVersions  []string       `json:"game_versions"`
	Loaders       []string       `json:"loaders"`
	Files         []modrinthFile `json:"files"`
}

func (v modrinthVersion) file() string {
	for _, f := range v.Files {
		if f.Primary {
			return f.URL
		}
	}
	if len(v.Files) > 0 {
		return v.Files[0].URL
	}
	return ""
}

// modrinthVersionList returns a project's versions, newest first, optionally
// narrowed to loaders and a game version.
func (s *Server) modrinthVersionList(ctx context.Context, project string, loaders []string, gameVersion string) ([]modrinthVersion, error) {
	// The changelogs are not used, and on a project with years of versions
	// they are most of the answer.
	params := url.Values{"include_changelog": {"false"}}
	if len(loaders) > 0 {
		encoded, _ := json.Marshal(loaders)
		params.Set("loaders", string(encoded))
	}
	if gameVersion != "" {
		encoded, _ := json.Marshal([]string{gameVersion})
		params.Set("game_versions", string(encoded))
	}
	body, err := s.catalog.get(ctx, "https://api.modrinth.com/v2/project/"+url.PathEscape(project)+"/version?"+params.Encode(), 5*time.Minute)
	if err != nil {
		return nil, err
	}
	var versions []modrinthVersion
	return versions, json.Unmarshal(body, &versions)
}

func (s *Server) modrinthVersion(ctx context.Context, id string) (*modrinthVersion, error) {
	body, err := s.catalog.get(ctx, "https://api.modrinth.com/v2/version/"+url.PathEscape(id), time.Hour)
	if err != nil {
		return nil, err
	}
	var v modrinthVersion
	return &v, json.Unmarshal(body, &v)
}

// preferReleases drops betas and alphas when a release exists.
func preferReleases(versions []modrinthVersion) []modrinthVersion {
	var releases []modrinthVersion
	for _, v := range versions {
		if v.VersionType == "release" {
			releases = append(releases, v)
		}
	}
	if len(releases) > 0 {
		return releases
	}
	return versions
}

// maxJar bounds how much of a mod file is downloaded to inspect it.
const maxJar = 256 << 20

// javaOfJar reports the Java release a jar's classes were compiled for, or 0
// if it holds no classes (a datapack, say).
func javaOfJar(file io.ReaderAt, size int64) (int, error) {
	zr, err := zip.NewReader(file, size)
	if err != nil {
		return 0, err
	}
	major := uint16(0)
	for _, f := range zr.File {
		// META-INF/versions holds alternatives that only newer JVMs load, and
		// module descriptors are ignored by older ones; neither says what the
		// jar needs.
		if !strings.HasSuffix(f.Name, ".class") || strings.HasPrefix(f.Name, "META-INF/") || strings.HasSuffix(f.Name, "module-info.class") {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			continue
		}
		var head [8]byte
		_, err = io.ReadFull(rc, head[:])
		rc.Close()
		if err != nil || binary.BigEndian.Uint32(head[:4]) != 0xCAFEBABE {
			continue
		}
		major = max(major, binary.BigEndian.Uint16(head[6:8]))
	}
	if major < 45 {
		return 0, nil
	}
	// Class file version 52 is Java 8, 65 is Java 21, and so on.
	return int(major) - 44, nil
}

// spool copies r to a temporary file so it can be read as a zip, and returns
// it positioned at the start.
func spool(r io.Reader, limit int64) (*os.File, int64, error) {
	tmp, err := os.CreateTemp("", "avsys-content-*")
	if err != nil {
		return nil, 0, err
	}
	// The file is only needed while it is open.
	_ = os.Remove(tmp.Name())
	n, err := io.Copy(tmp, io.LimitReader(r, limit+1))
	if err == nil && n > limit {
		err = fmt.Errorf("file is larger than %d MB", limit>>20)
	}
	if err != nil {
		tmp.Close()
		return nil, 0, err
	}
	return tmp, n, nil
}

type javaCache struct {
	mu   sync.Mutex
	byID map[string]int
}

var inspected = javaCache{byID: map[string]int{}}

// javaOfURL downloads a mod file and reports the Java it needs. Published
// files do not change, so answers are remembered.
func javaOfURL(ctx context.Context, fileURL string) (int, error) {
	inspected.mu.Lock()
	java, ok := inspected.byID[fileURL]
	inspected.mu.Unlock()
	if ok {
		return java, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fileURL, nil)
	if err != nil {
		return 0, err
	}
	resp, err := publicHTTP.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("downloading %s: %s", fileURL, resp.Status)
	}
	tmp, size, err := spool(resp.Body, maxJar)
	if err != nil {
		return 0, err
	}
	defer tmp.Close()
	java, err = javaOfJar(tmp, size)
	if err != nil {
		return 0, err
	}
	inspected.mu.Lock()
	inspected.byID[fileURL] = java
	inspected.mu.Unlock()
	return java, nil
}

// target is what content has to fit: the loader and game version the server
// really runs, which for a modpack are the pack's.
type target struct {
	loader      string
	gameVersion string
	// java is what the game version ships with, or 0 when unknown.
	java int
}

func targetOf(spec *avsysv1.MinecraftServerSpec) target {
	t := target{loader: string(spec.Loader), gameVersion: controller.GameVersionOf(spec)}
	if spec.Modpack != nil {
		t.loader = spec.Modpack.Loader
	}
	if t.gameVersion == "LATEST" {
		t.gameVersion = ""
	}
	t.java = controller.GameJavaOf(spec)
	return t
}

var loaderNames = map[string]string{
	"vanilla": "Vanilla", "paper": "Paper", "purpur": "Purpur", "folia": "Folia",
	"fabric": "Fabric", "quilt": "Quilt", "forge": "Forge", "neoforge": "NeoForge",
}

// String names the target for messages: "Paper 1.21.4".
func (t target) String() string {
	name := loaderNames[t.loader]
	if name == "" {
		return "this server"
	}
	return strings.TrimSpace(name + " " + t.gameVersion)
}

func (t target) loadersFor(kind avsysv1.ContentKind) []string {
	if kind == avsysv1.KindDatapack {
		return []string{"datapack"}
	}
	return modrinthLoaders[t.loader]
}

// candidatesToInspect is how far back the panel looks for a version that runs
// on the server's own Java before settling for the newest and raising it.
const candidatesToInspect = 6

// describe fills in the name and icon of a Modrinth entry that came without
// them, as entries added through the API or kubectl do.
func (s *Server) describe(ctx context.Context, m *avsysv1.ContentRef) {
	if m.Title != "" && m.IconURL != "" {
		return
	}
	body, err := s.catalog.get(ctx, "https://api.modrinth.com/v2/project/"+url.PathEscape(m.Project), time.Hour)
	if err != nil {
		return
	}
	var project struct {
		Title   string `json:"title"`
		IconURL string `json:"icon_url"`
	}
	if json.Unmarshal(body, &project) != nil {
		return
	}
	if m.Title == "" {
		m.Title = project.Title
	}
	if m.IconURL == "" {
		m.IconURL = project.IconURL
	}
}

// pin resolves one Modrinth entry to a version and records the Java it needs.
func (s *Server) pin(ctx context.Context, m *avsysv1.ContentRef, t target) error {
	s.describe(ctx, m)
	name := m.Title
	if name == "" {
		name = m.Project
	}
	inspect := func(v *modrinthVersion) int {
		if m.Kind == avsysv1.KindDatapack || v.file() == "" {
			return 0
		}
		java, err := javaOfURL(ctx, v.file())
		if err != nil {
			// Not knowing is not a reason to refuse the mod.
			s.Log.Warn("could not inspect mod file", "project", m.Project, "version", v.VersionNumber, "error", err)
		}
		return java
	}

	if m.Version != "" {
		// Pinned by hand: honour it, and just learn what it needs.
		v, err := s.modrinthVersion(ctx, m.Version)
		if errors.Is(err, errNotFound) {
			return errBadRequest("%s has no version %q on Modrinth.", name, m.Version)
		}
		if err != nil {
			return err
		}
		m.VersionName, m.Java = v.VersionNumber, int32(inspect(v))
		return nil
	}

	versions, err := s.modrinthVersionList(ctx, m.Project, t.loadersFor(m.Kind), t.gameVersion)
	if errors.Is(err, errNotFound) {
		return errBadRequest("%s was not found on Modrinth.", name)
	}
	if err != nil {
		return err
	}
	versions = preferReleases(versions)
	if len(versions) == 0 {
		return errBadRequest("%s has no version for %s.", name, t)
	}
	// Prefer the newest version that runs on the Java this game version
	// ships with. If none does, take the newest and let the server's Java
	// be raised to match.
	chosen, java := &versions[0], -1
	for i := range min(len(versions), candidatesToInspect) {
		needs := inspect(&versions[i])
		if i == 0 {
			java = needs
		}
		if t.java == 0 || needs <= t.java {
			chosen, java = &versions[i], needs
			break
		}
	}
	m.Version, m.VersionName, m.Java = chosen.ID, chosen.VersionNumber, int32(java)
	return nil
}

// pinModpack records the version of a Modrinth modpack and what it is built for.
func (s *Server) pinModpack(ctx context.Context, pack *avsysv1.ModpackSpec) error {
	name := pack.Title
	if name == "" {
		name = pack.Project
	}
	var chosen *modrinthVersion
	if pack.Version != "" {
		v, err := s.modrinthVersion(ctx, pack.Version)
		if errors.Is(err, errNotFound) {
			return errBadRequest("%s has no version %q on Modrinth.", name, pack.Version)
		}
		if err != nil {
			return err
		}
		chosen = v
	} else {
		versions, err := s.modrinthVersionList(ctx, pack.Project, nil, "")
		if errors.Is(err, errNotFound) {
			return errBadRequest("%s was not found on Modrinth.", name)
		}
		if err != nil {
			return err
		}
		if versions = preferReleases(versions); len(versions) == 0 {
			return errBadRequest("%s has no published versions.", name)
		}
		chosen = &versions[0]
	}
	pack.Version, pack.VersionName = chosen.ID, chosen.VersionNumber
	pack.GameVersion, pack.Loader = "", ""
	if n := len(chosen.GameVersions); n > 0 {
		pack.GameVersion = chosen.GameVersions[n-1]
	}
	for _, l := range chosen.Loaders {
		if _, known := modrinthLoaders[l]; known {
			pack.Loader = l
			break
		}
	}
	return nil
}

// resolveContent pins whatever in spec is not pinned yet. previous is the spec
// being replaced, or nil for a new server.
func (s *Server) resolveContent(ctx context.Context, spec, previous *avsysv1.MinecraftServerSpec) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	if pack := spec.Modpack; pack != nil && pack.Source == avsysv1.SourceModrinth {
		unchanged := previous != nil && previous.Modpack != nil && previous.Modpack.Project == pack.Project && previous.Modpack.Version == pack.Version
		if !unchanged || pack.GameVersion == "" || pack.Version == "" {
			if err := s.pinModpack(ctx, pack); err != nil {
				return err
			}
		}
	}

	t := targetOf(spec)
	// Versions were chosen to fit one loader and game version. If either
	// changed, they are chosen again; this is how content follows a server
	// through an upgrade.
	retarget := previous != nil && targetOf(previous) != t

	var (
		wg    sync.WaitGroup
		slots = make(chan struct{}, 4)
		mu    sync.Mutex
		first error
	)
	// Where a datapack from a URL was saved is known from when it was added.
	// What a request says of it is not taken: the panel deletes that file.
	saved := map[string]string{}
	if previous != nil {
		for _, m := range previous.Mods {
			if m.Source == avsysv1.SourceURL && m.Kind == avsysv1.KindDatapack {
				saved[m.Project] = m.File
			}
		}
	}
	for i := range spec.Mods {
		m := &spec.Mods[i]
		if m.Source == avsysv1.SourceURL && m.Kind == avsysv1.KindDatapack {
			file, ok := saved[m.Project]
			if m.File = file; ok {
				continue
			}
		} else {
			m.File = ""
		}
		switch m.Source {
		case avsysv1.SourceModrinth:
			if retarget {
				m.Version, m.VersionName, m.Java = "", "", 0
			}
			if m.Version != "" && (m.Java != 0 || m.Kind == avsysv1.KindDatapack) && m.Title != "" && m.IconURL != "" {
				continue
			}
		case avsysv1.SourceURL:
			if m.Kind == avsysv1.KindDatapack {
				break
			}
			if m.Java != 0 || !strings.HasSuffix(strings.ToLower(strings.SplitN(m.Project, "?", 2)[0]), ".jar") {
				continue
			}
		default:
			continue
		}
		wg.Add(1)
		slots <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-slots }()
			var err error
			if m.Source == avsysv1.SourceURL && m.Kind == avsysv1.KindDatapack {
				// Best effort, as below; without it, the URL's own name is used.
				m.File = savedName(ctx, m.Project)
			} else if m.Source == avsysv1.SourceURL {
				// Best effort: a URL that cannot be read now may still work
				// from inside the cluster.
				if java, ierr := javaOfURL(ctx, m.Project); ierr == nil {
					m.Java = int32(java)
				}
			} else {
				err = s.pin(ctx, m, t)
			}
			if err != nil {
				mu.Lock()
				if first == nil {
					first = err
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return first
}

// savedName is the name the image's downloader saves a file from fileURL
// under: the one its response gives, or else the last part of the path it
// was redirected to. "" if the URL cannot be read now.
func savedName(ctx context.Context, fileURL string) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fileURL, nil)
	if err != nil {
		return ""
	}
	resp, err := publicHTTP.Do(req)
	if err != nil {
		return ""
	}
	// Only the headers are wanted.
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	if _, params, err := mime.ParseMediaType(resp.Header.Get("Content-Disposition")); err == nil {
		if name := path.Base(strings.ReplaceAll(params["filename"], "\\", "/")); plainName(name) {
			return name
		}
	}
	if name := path.Base(resp.Request.URL.Path); plainName(name) {
		return name
	}
	return ""
}

var unsafeFilenameChars = regexp.MustCompile(`[^A-Za-z0-9._+()\[\] -]+`)

// contentFilename makes an uploaded file's name safe to store and to list in
// the server image's newline- and comma-separated variables.
func contentFilename(name string, kind avsysv1.ContentKind) (string, error) {
	name = strings.TrimSpace(unsafeFilenameChars.ReplaceAllString(path.Base(strings.ReplaceAll(name, "\\", "/")), "_"))
	want := ".jar"
	if kind == avsysv1.KindDatapack {
		want = ".zip"
	}
	if !strings.HasSuffix(strings.ToLower(name), want) || len(name) <= len(want) || len(name) > 200 || strings.HasPrefix(name, ".") {
		return "", errBadRequest("Upload a %s file.", want)
	}
	return name, nil
}

// uploadContent stores a mod, plugin or datapack sent from someone's computer
// on the server's volume and lists it in the server's content.
func (s *Server) uploadContent(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, mc, err := s.server(r, u, store.RoleAdmin)
	if err != nil {
		return err
	}
	release, err := s.changing(mc, false)
	if err != nil {
		return err
	}
	defer release()
	kind := avsysv1.ContentKind(r.URL.Query().Get("kind"))
	switch kind {
	case avsysv1.KindMod, avsysv1.KindPlugin, avsysv1.KindDatapack:
	default:
		return errBadRequest("Unknown content type.")
	}
	name, err := contentFilename(r.URL.Query().Get("name"), kind)
	if err != nil {
		return err
	}

	tmp, size, err := spool(http.MaxBytesReader(w, r.Body, maxJar), maxJar)
	if err != nil {
		return errBadRequest("The upload did not complete: %v.", err)
	}
	defer tmp.Close()
	java, err := javaOfJar(tmp, size)
	if err != nil {
		return errBadRequest("%s is not a valid %s file.", name, path.Ext(name))
	}

	sc, err := s.workspaces.get(r.Context(), a.Namespace, mc.Name)
	if err != nil {
		s.Log.Warn("workspace unreachable", "server", mc.Name, "error", err)
		return &Error{Status: http.StatusBadGateway, Code: "workspace", Message: "The server's files are not reachable right now. It may be restarting."}
	}
	if err := sc.MkdirAll(controller.ContentDir); err != nil {
		return fileError(err)
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return err
	}
	dst, err := sc.Create(path.Join(controller.ContentDir, name))
	if err != nil {
		return fileError(err)
	}
	_, err = io.Copy(dst, tmp)
	if cerr := dst.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fileError(err)
	}

	entry := avsysv1.ContentRef{Source: avsysv1.SourceUpload, Kind: kind, Project: name, Title: name, Java: int32(java)}
	// The file is in place; now list it. Someone may be editing the server at
	// the same moment, so a conflicting write is retried on fresh state.
	for attempt := 0; ; attempt++ {
		mods := mc.Spec.Mods[:0:0]
		for _, m := range mc.Spec.Mods {
			if m.Source != avsysv1.SourceUpload || m.Project != name {
				mods = append(mods, m)
			}
		}
		mc.Spec.Mods = append(mods, entry)
		err := s.Client.Update(r.Context(), mc)
		if err == nil {
			break
		}
		if !apierrors.IsConflict(err) || attempt == 3 {
			return err
		}
		time.Sleep(100 * time.Millisecond)
		if err := s.Client.Get(r.Context(), client.ObjectKeyFromObject(mc), mc); err != nil {
			return err
		}
	}
	s.audit(r.Context(), u, a.Org, "content.upload", mc.Name, name)
	writeJSON(w, http.StatusOK, s.view(mc))
	return nil
}

// removeDroppedUploads deletes what content taken out of a server's list left
// behind that nothing else removes: the stored file of an upload, and the
// installed file of a datapack from a URL or an upload.
//
// The image removes what it installed from Modrinth, CurseForge, or as mods
// and plugins from a URL or an upload, once it is no longer asked for. A
// datapack from a URL or an upload it only ever adds: it can be told to clear
// the whole datapacks folder, which would take Modrinth's and any put there
// by hand with it. So the panel removes exactly the file it had put there.
func (s *Server) removeDroppedUploads(ctx context.Context, namespace, server string, spec, before *avsysv1.MinecraftServerSpec) {
	s.removeDroppedUploadsWith(ctx, nil, namespace, server, spec, before)
}

// removeDroppedUploadsWith is removeDroppedUploads through a connection to the
// server's files that the caller has made sure of, or the shared one if nil.
func (s *Server) removeDroppedUploadsWith(ctx context.Context, through *sftp.Client, namespace, server string, spec, before *avsysv1.MinecraftServerSpec) {
	gone := leftBehind(spec, before)
	if len(gone) == 0 {
		return
	}
	sc, err := through, error(nil)
	if sc == nil {
		if sc, err = s.workspaces.get(ctx, namespace, server); err != nil {
			s.Log.Warn("could not reach a server's files to remove content taken off it", "server", server, "error", err)
			return
		}
	}
	for _, file := range gone {
		if err := sc.Remove(file); err != nil && !errors.Is(err, fs.ErrNotExist) {
			s.Log.Warn("could not delete content taken off a server", "server", server, "file", file, "error", err)
		}
	}
}

// leftBehind lists the files to delete for content that was in before and is
// not in spec's list.
func leftBehind(spec, before *avsysv1.MinecraftServerSpec) []string {
	kept := map[string]bool{}
	for _, m := range spec.Mods {
		kept[string(m.Source)+":"+m.Project] = true
	}
	// A datapack is in the world's folder, which LEVEL can move. Where it was
	// put is where the world was then; and a datapack still listed may be
	// in the same file, which then stays. If either folder is not known,
	// as when LEVEL is read from elsewhere, no datapack is deleted.
	was, now := levelOf(before), levelOf(spec)
	inUse := map[string]bool{}
	for _, m := range spec.Mods {
		if name := installedDatapack(m); name != "" && now != "" {
			inUse[path.Join(dataRoot, now, "datapacks", name)] = true
		}
	}
	var gone []string
	for _, m := range before.Mods {
		if kept[string(m.Source)+":"+m.Project] {
			continue
		}
		if m.Source == avsysv1.SourceUpload {
			gone = append(gone, path.Join(controller.ContentDir, path.Base(m.Project)))
		}
		if name := installedDatapack(m); name != "" && was != "" && now != "" {
			if file := path.Join(dataRoot, was, "datapacks", name); !inUse[file] {
				gone = append(gone, file)
			}
		}
	}
	return gone
}

// installedDatapack is the name the image gives a datapack from a URL or an
// upload in the world's datapacks folder: the upload's own name, or the name
// its download gave it, recorded when it was added. Without that, as for one
// added before the panel recorded it, the last part of the URL's path.
func installedDatapack(m avsysv1.ContentRef) string {
	if m.Kind != avsysv1.KindDatapack {
		return ""
	}
	switch m.Source {
	case avsysv1.SourceUpload:
		return path.Base(m.Project)
	case avsysv1.SourceURL:
		if plainName(m.File) {
			return m.File
		}
		u, err := url.Parse(m.Project)
		if err != nil {
			return ""
		}
		if name := path.Base(u.Path); plainName(name) && strings.HasSuffix(strings.ToLower(name), ".zip") {
			return name
		}
	}
	return ""
}

// plainName is a file's name with no folder in it.
func plainName(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, "/\\\x00")
}

// levelOf is the world's folder, where the image puts datapacks: what LEVEL
// says in a server's own environment, or "world". It is "" when that cannot
// be told: LEVEL read from a secret or the like, or naming a folder that is
// not one in /data.
func levelOf(spec *avsysv1.MinecraftServerSpec) string {
	for _, e := range spec.Env {
		if e.Name != "LEVEL" {
			continue
		}
		if e.ValueFrom != nil || !plainName(e.Value) {
			return ""
		}
		return e.Value
	}
	return "world"
}
