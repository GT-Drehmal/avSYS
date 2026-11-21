package api

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/pkg/sftp"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
	"github.com/GT-Drehmal/avSYS/internal/controller"
	"github.com/GT-Drehmal/avSYS/internal/store"
)

// Content a server has that the panel did not add: mods and plugins put in
// their folders by hand, over SFTP or the terminal, or brought in with a
// server's files, and datapacks in its world. The panel looks in those
// folders now and then and keeps what it found on the server, in the
// annotation avsys.dev/found-content, so its Content page lists everything
// that is there. Files are never changed by being found: one Modrinth knows
// by its hash is shown as that project at that very version, and the rest
// by what the file says of itself, or by its name.

// FoundFile is a file of content found in a server's folders.
type FoundFile struct {
	// Path is where it is, under the server's folder: mods/x.jar.
	Path string              `json:"path"`
	Kind avsysv1.ContentKind `json:"kind"`
	// Size and Modified (Unix seconds) tell whether it changed since it was
	// read, without reading it again.
	Size     int64  `json:"size"`
	Modified int64  `json:"modified"`
	SHA1     string `json:"sha1,omitempty"`
	// Title and Version are its name and version, from Modrinth or the file.
	Title   string `json:"title,omitempty"`
	Version string `json:"version,omitempty"`
	// About is what a datapack says of itself.
	About string `json:"about,omitempty"`
	// Project, Slug and VersionID are Modrinth's, when Modrinth has the file.
	Project   string `json:"project,omitempty"`
	Slug      string `json:"slug,omitempty"`
	VersionID string `json:"versionId,omitempty"`
	IconURL   string `json:"iconUrl,omitempty"`
	// Picture says the file has a picture of its own, served at
	// .../content/found/icon.
	Picture bool `json:"picture,omitempty"`
	// Folder is a datapack that is a folder rather than a .zip.
	Folder bool `json:"folder,omitempty"`
	// Looked is when Modrinth was last asked about it, in Unix seconds; 0
	// until it has answered.
	Looked int64 `json:"looked,omitempty"`
	// Java is the Java release a mod's or plugin's classes were compiled for.
	Java int32 `json:"java,omitempty"`
	// Filename is the name Modrinth gives the file of its version, which the
	// image saves it under, and Requires the Modrinth projects that version
	// needs, which the image installs with it.
	Filename string   `json:"filename,omitempty"`
	Requires []string `json:"requires,omitempty"`
}

// ServerFiles are a server's files, as the panel reads the content in them.
// Paths are absolute: /data/mods.
type ServerFiles interface {
	ReadDir(path string) ([]os.FileInfo, error)
	Stat(path string) (os.FileInfo, error)
	Open(path string) (ServerFile, error)
	Remove(path string) error
	RemoveAll(path string) error
	MkdirAll(path string) error
	// Create makes a file, or empties the one there, to write.
	Create(path string) (io.WriteCloser, error)
}

// ServerFile is one of them, open to read.
type ServerFile interface {
	io.Reader
	io.ReaderAt
	io.Closer
}

// sftpFiles are a server's files through its workspace.
type sftpFiles struct{ *sftp.Client }

func (f sftpFiles) Open(p string) (ServerFile, error) { return f.Client.Open(p) }

func (f sftpFiles) Create(p string) (io.WriteCloser, error) { return f.Client.Create(p) }

// contentFiles reaches a server's files to find content in, without waiting
// for a workspace that is not up.
func (s *Server) contentFiles(ctx context.Context, namespace, name string) (ServerFiles, error) {
	if s.ContentFiles != nil {
		return s.ContentFiles(ctx, namespace, name)
	}
	sc, err := s.workspaces.getNow(ctx, namespace, name)
	if err != nil {
		return nil, err
	}
	return sftpFiles{sc}, nil
}

// foundOf reads what was found on a server.
func foundOf(mc *avsysv1.MinecraftServer) []FoundFile {
	raw := mc.Annotations[avsysv1.AnnotationFoundContent]
	if raw == "" {
		return []FoundFile{}
	}
	var out []FoundFile
	if json.Unmarshal([]byte(raw), &out) != nil || out == nil {
		return []FoundFile{}
	}
	return out
}

// foundRefs are the found files Modrinth knows, as entries of a server's
// content, for what looks at which mods it has: the ports opened for them.
func foundRefs(found []FoundFile) []avsysv1.ContentRef {
	var out []avsysv1.ContentRef
	for _, f := range found {
		if f.Project != "" {
			out = append(out, avsysv1.ContentRef{Source: avsysv1.SourceModrinth, Kind: f.Kind, Project: f.Project, Version: f.VersionID, Title: f.Title})
		}
	}
	return out
}

// readsAtOnce is how many found files are read at a time.
const readsAtOnce = 4

// maxFound is the most found files kept on a server: the annotation holding
// them stays well under what Kubernetes allows an object.
var maxFound = 400

// The folders content is found in, under /data.
const (
	modsDir    = "mods"
	pluginsDir = "plugins"
)

// foundState is what the panel knows of its looks at one server's folders.
type foundState struct {
	mu sync.Mutex
	// looking is held while a look runs: one at a time per server.
	looking sync.Mutex
	last    time.Time
	phase   avsysv1.Phase
	busy    bool
	dirty   bool
	// removed is when each file was removed through the panel, so that a
	// look that read the folder before then does not list it again.
	removed map[string]time.Time
}

func (s *Server) foundStateOf(key client.ObjectKey) *foundState {
	st, _ := s.found.LoadOrStore(key.String(), &foundState{removed: map[string]time.Time{}})
	return st.(*foundState)
}

// lookSoon has a server's folders looked at again with the next look round,
// as after its files were changed through the panel.
func (s *Server) lookSoon(namespace, name string) {
	st := s.foundStateOf(client.ObjectKey{Namespace: namespace, Name: name})
	st.mu.Lock()
	st.dirty = true
	st.mu.Unlock()
	select {
	case s.lookNow <- struct{}{}:
	default:
	}
}

// lookTime is how long a look at a server's folders may take: each file new
// to it is read whole, which over a slow connection can take minutes.
var lookTime = 15 * time.Minute

// lookEvery is how often a server's folders are looked at when nothing says
// they changed; lookRound is how often the panel goes over its servers to
// see which are due.
var (
	lookEvery = 5 * time.Minute
	lookRound = 15 * time.Second
)

// RunContentLook looks at servers' folders for content the panel did not
// add, until ctx ends: each one now and then, and sooner once it starts or
// stops, an import, restore or move has finished, or its files were changed
// through the panel.
func (s *Server) RunContentLook(ctx context.Context) {
	for {
		s.lookRoundOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(lookRound):
		case <-s.lookNow:
		}
	}
}

func (s *Server) lookRoundOnce(ctx context.Context) {
	var all avsysv1.MinecraftServerList
	var reader client.Reader = s.Client
	if s.Reader != nil {
		reader = s.Reader
	}
	if err := reader.List(ctx, &all); err != nil {
		return
	}
	for i := range all.Items {
		mc := &all.Items[i]
		st := s.foundStateOf(client.ObjectKeyFromObject(mc))
		busy := s.filesInFlux(mc)
		st.mu.Lock()
		due := !busy && (st.last.IsZero() || st.busy || st.dirty || st.phase != mc.Status.Phase || time.Since(st.last) > lookEvery)
		st.busy = busy
		st.mu.Unlock()
		if !due || ctx.Err() != nil {
			continue
		}
		look, cancel := context.WithTimeout(ctx, lookTime)
		_ = s.lookAtContent(look, mc)
		cancel()
	}
}

// filesInFlux says a server's files are being put in place or taken away,
// by a restore, import, move or its making: what is there now is not what
// will be.
func (s *Server) filesInFlux(mc *avsysv1.MinecraftServer) bool {
	for _, a := range []string{avsysv1.AnnotationRestoring, avsysv1.AnnotationImporting, avsysv1.AnnotationMoving, avsysv1.AnnotationCreating} {
		if _, ok := mc.Annotations[a]; ok {
			return true
		}
	}
	if !mc.DeletionTimestamp.IsZero() {
		return true
	}
	if op := s.ops.current(mc.Namespace, mc.Name); op != nil && op.Kind != kindSnapshot && op.Kind != kindTemplate {
		return true
	}
	return false
}

// lookAtContent looks at a server's folders and keeps what it finds on it.
func (s *Server) lookAtContent(ctx context.Context, mc *avsysv1.MinecraftServer) error {
	key := client.ObjectKeyFromObject(mc)
	st := s.foundStateOf(key)
	st.looking.Lock()
	defer st.looking.Unlock()
	started := time.Now()
	st.mu.Lock()
	st.last, st.phase, st.dirty = started, mc.Status.Phase, false
	st.mu.Unlock()

	files, err := s.contentFiles(ctx, mc.Namespace, mc.Name)
	if err != nil {
		return err
	}
	// What someone took off the list through the panel goes first; what is
	// off it any other way is forgotten, and found again.
	s.finishRemovals(ctx, files, key)
	s.settleImported(ctx, files, key)
	if err := s.Client.Get(ctx, key, mc); err != nil {
		return err
	}
	found, err := s.findContent(ctx, key.String(), files, &mc.Spec, foundOf(mc))
	if err != nil {
		return err
	}
	found = st.without(found, started)
	// Not one still to be deleted.
	found = slices.DeleteFunc(found, func(f FoundFile) bool {
		return slices.ContainsFunc(removalsOf(mc), func(r contentRemoval) bool { return r.Path == f.Path })
	})
	// What is found is taken into the list: what is left is what cannot be.
	if found, err = s.importFound(ctx, key, files, found); err != nil {
		s.Log.Warn("could not take content found in a server's folders into its list", "server", key.Name, "error", err)
	}
	if err := s.keepFound(ctx, key, func([]FoundFile) []FoundFile { return st.without(found, started) }); err != nil {
		return err
	}
	// Pages open on it are told, as where the cluster's own events are not
	// heard. Twice is no harm.
	if now := (&avsysv1.MinecraftServer{}); s.Client.Get(ctx, key, now) == nil {
		s.Events.Publish(Event{Type: "server.updated", Namespace: now.Namespace, Data: s.view(store.RoleOwner, now)})
	}
	return nil
}

// without is what a look that read a server's folders at started found,
// without what was removed through the panel after then, which the folders
// had when they were read.
func (st *foundState) without(found []FoundFile, started time.Time) []FoundFile {
	st.mu.Lock()
	defer st.mu.Unlock()
	var out []FoundFile
	for _, f := range found {
		if at, ok := st.removed[f.Path]; ok && at.After(started) {
			continue
		}
		out = append(out, f)
	}
	for p, at := range st.removed {
		if time.Since(at) > time.Hour {
			delete(st.removed, p)
		}
	}
	return out
}

// lookAtImported looks at the folders of a server whose files were just
// put in place, so that its Content page lists them as soon as it is shown.
func (s *Server) lookAtImported(ctx context.Context, key client.ObjectKey) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), lookTime)
	defer cancel()
	mc := &avsysv1.MinecraftServer{}
	if err := s.Client.Get(ctx, key, mc); err != nil {
		return
	}
	if err := s.lookAtContent(ctx, mc); err != nil {
		s.Log.Debug("could not look at an imported server's folders", "server", key.Name, "error", err)
		s.lookSoon(key.Namespace, key.Name)
	}
}

// keepFound changes what a server is noted to have in its folders.
func (s *Server) keepFound(ctx context.Context, key client.ObjectKey, change func([]FoundFile) []FoundFile) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		mc := &avsysv1.MinecraftServer{}
		if err := s.Client.Get(ctx, key, mc); err != nil {
			return client.IgnoreNotFound(err)
		}
		was := mc.Annotations[avsysv1.AnnotationFoundContent]
		now := ""
		if list := change(foundOf(mc)); len(list) > 0 {
			raw, err := json.Marshal(list)
			if err != nil {
				return err
			}
			now = string(raw)
		}
		if now == was {
			return nil
		}
		if mc.Annotations == nil {
			mc.Annotations = map[string]string{}
		}
		if now == "" {
			delete(mc.Annotations, avsysv1.AnnotationFoundContent)
		} else {
			mc.Annotations[avsysv1.AnnotationFoundContent] = now
		}
		return s.Client.Update(ctx, mc)
	})
}

// findContent lists the content in a server's folders that the panel did
// not put there. before is what was found the last time: a file that has
// not changed since is not read again.
func (s *Server) findContent(ctx context.Context, owner string, files ServerFiles, spec *avsysv1.MinecraftServerSpec, before []FoundFile) ([]FoundFile, error) {
	managed := managedFiles(files, spec)
	known := map[string]FoundFile{}
	for _, f := range before {
		known[f.Path] = f
	}
	level := levelOf(spec)
	if level == "" {
		level = propertiesLevel(files)
	}
	type place struct {
		dir  string
		kind avsysv1.ContentKind
	}
	places := []place{{modsDir, avsysv1.KindMod}, {pluginsDir, avsysv1.KindPlugin}}
	if level != "" {
		places = append(places, place{path.Join(level, "datapacks"), avsysv1.KindDatapack})
	}
	var out []FoundFile
	var toRead []int
	reached := false
folders:
	for _, p := range places {
		entries, err := files.ReadDir(path.Join(dataRoot, p.dir))
		if errors.Is(err, fs.ErrNotExist) {
			reached = true
			continue
		}
		if err != nil {
			continue
		}
		reached = true
		for _, e := range entries {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			// No more are kept, read before or not, in any folder.
			if len(out) >= maxFound {
				break folders
			}
			name := e.Name()
			rel := path.Join(p.dir, name)
			if strings.HasPrefix(name, ".") || managed[path.Join(dataRoot, rel)] || !plainName(name) {
				continue
			}
			folder := e.IsDir()
			switch {
			case p.kind == avsysv1.KindDatapack && isDatapack(files, path.Join(dataRoot, p.dir), e):
			case p.kind != avsysv1.KindDatapack && !folder && e.Mode().IsRegular() && strings.HasSuffix(strings.ToLower(name), ".jar"):
			default:
				continue
			}
			f := FoundFile{Path: rel, Kind: p.kind, Size: e.Size(), Modified: e.ModTime().Unix(), Folder: folder}
			if folder {
				f.Size = 0
			}
			if was, ok := known[rel]; ok && was.Size == f.Size && was.Modified == f.Modified && was.Folder == folder && was.read() {
				out = append(out, was)
				continue
			}
			out = append(out, f)
			toRead = append(toRead, len(out)-1)
		}
	}
	// The files read are read a few at a time: each is read whole, for its
	// hash, which over a slow connection to the workspace takes a while.
	var wg sync.WaitGroup
	slots := make(chan struct{}, readsAtOnce)
	for _, i := range toRead {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		slots <- struct{}{}
		go func(f *FoundFile) {
			defer wg.Done()
			defer func() { <-slots }()
			s.readFound(owner, files, f)
		}(&out[i])
	}
	wg.Wait()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if !reached {
		return nil, errors.New("the server's folders could not be read")
	}
	s.recognise(ctx, out)
	// The panel's own, where the image has not said so yet: a file of
	// exactly the version of a project the server lists.
	pinned := map[string]bool{}
	uploads := map[string]bool{}
	for _, m := range spec.Mods {
		if m.Source == avsysv1.SourceModrinth && m.Version != "" {
			pinned[m.Version] = true
		}
		if m.Source == avsysv1.SourceUpload {
			uploads[string(m.Kind)+"/"+path.Base(m.Project)] = true
		}
	}
	kept := out[:0]
	for _, f := range out {
		if (f.VersionID != "" && pinned[f.VersionID]) || (f.Kind != avsysv1.KindDatapack && uploads[string(f.Kind)+"/"+path.Base(f.Path)]) {
			continue
		}
		kept = append(kept, f)
	}
	sort.Slice(kept, func(i, j int) bool { return kept[i].Path < kept[j].Path })
	return kept, nil
}

// madeBySoftware are datapack folders a server's software makes in its
// world by itself.
var madeBySoftware = map[string]bool{"bukkit": true}

// isDatapack says an entry of a world's datapacks folder, dir, is a
// datapack: a .zip file, or a folder with a pack.mcmeta. What the server's
// software makes there itself is not: Bukkit's datapack, which Spigot,
// Paper, Purpur and Folia keep in each world for their plugins.
func isDatapack(files ServerFiles, dir string, e os.FileInfo) bool {
	if e.IsDir() {
		return !madeBySoftware[e.Name()] && hasPackMeta(files, path.Join(dir, e.Name()))
	}
	return strings.HasSuffix(strings.ToLower(e.Name()), ".zip")
}

// hasPackMeta says a folder is a datapack: it has a pack.mcmeta.
func hasPackMeta(files ServerFiles, dir string) bool {
	info, err := files.Stat(path.Join(dir, "pack.mcmeta"))
	return err == nil && !info.IsDir()
}

// propertiesLevel is the world's folder as the server's properties name
// it, for a server whose settings do not say: "world" where they do not
// either, or "" where they name no plain folder.
func propertiesLevel(files ServerFiles) string {
	f, err := files.Open(path.Join(dataRoot, "server.properties"))
	if err != nil {
		return "world"
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, 1<<20))
	if err != nil {
		return "world"
	}
	if !bytes.Contains(raw, []byte("level-name=")) {
		return "world"
	}
	return levelNamed(bytes.NewReader(raw))
}

// The records the image keeps of what it installed, under /data: from
// Modrinth, CurseForge and lists of URLs and files, and a modpack's.
var imageRecords = []string{
	".modrinth-manifest.json",
	".curseforge-files-manifest.json",
	".modrinth-modpack-manifest.json",
	".curseforge-manifest.json",
	modsDir + "/.var-list-manifest.json",
	pluginsDir + "/.var-list-manifest.json",
}

// managedFiles are the files in a server's content folders that the panel
// has put there, itself or through the image: they are its content, not
// found. Paths are absolute.
func managedFiles(files ServerFiles, spec *avsysv1.MinecraftServerSpec) map[string]bool {
	out := installedFiles(spec)
	for _, record := range imageRecords {
		f, err := files.Open(path.Join(dataRoot, record))
		if err != nil {
			continue
		}
		raw, err := io.ReadAll(io.LimitReader(f, 8<<20))
		f.Close()
		if err != nil {
			continue
		}
		// A list of URLs and files names them in its own folder; the
		// others, under /data.
		base := dataRoot
		if strings.Contains(record, "/") {
			base = path.Join(dataRoot, path.Dir(record))
		}
		for _, p := range recordedFiles(raw) {
			if !path.IsAbs(p) {
				p = path.Join(base, p)
			}
			out[path.Clean(p)] = true
		}
	}
	return out
}

// recordedFiles are the files one of the image's records lists: as
// "files", or as the "filePath" of each of its "entries".
func recordedFiles(raw []byte) []string {
	var record struct {
		Files   []string `json:"files"`
		Entries []struct {
			FilePath string `json:"filePath"`
		} `json:"entries"`
	}
	if json.Unmarshal(raw, &record) != nil {
		return nil
	}
	out := record.Files
	for _, e := range record.Entries {
		if e.FilePath != "" {
			out = append(out, e.FilePath)
		}
	}
	return out
}

// read says a found file was read whole: a datapack folder, one too large
// to read, or one whose hash was taken.
func (f FoundFile) read() bool {
	return f.Folder || f.Size > maxJar || f.SHA1 != ""
}

// readFound reads what a found file says of itself, and its hash. One that
// cannot be read is known by its name.
func (s *Server) readFound(owner string, files ServerFiles, f *FoundFile) {
	full := path.Join(dataRoot, f.Path)
	if f.Folder {
		meta := packMeta(func(name string) ([]byte, error) { return readSmall(files, path.Join(full, name)) })
		f.About = meta.about
		if meta.icon != nil {
			f.Picture = s.keepFoundIcon(foundIconKey(owner, *f), meta.icon)
		}
		f.Title = path.Base(f.Path)
		return
	}
	f.Title = withoutExt(path.Base(f.Path))
	if f.Size > maxJar {
		return
	}
	in, err := files.Open(full)
	if err != nil {
		return
	}
	defer in.Close()
	tmp, size, err := spool(in, maxJar)
	if err != nil {
		return
	}
	defer tmp.Close()
	// Read whole, or not at all: a read cut short, as over a connection
	// dropped part of the way, ends early without saying so.
	if size != f.Size {
		return
	}
	h := sha1.New()
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return
	}
	if _, err := io.Copy(h, tmp); err != nil {
		return
	}
	f.SHA1 = hex.EncodeToString(h.Sum(nil))
	zr, err := zip.NewReader(tmp, size)
	if err != nil {
		return
	}
	if f.Kind != avsysv1.KindDatapack {
		if java, err := javaOfJar(tmp, size); err == nil {
			f.Java = int32(java)
		}
	}
	meta := contentMeta(zr, f.Kind)
	if meta.title != "" {
		f.Title = meta.title
	}
	f.Version, f.About = meta.version, meta.about
	if meta.icon != nil {
		f.Picture = s.keepFoundIcon(foundIconKey(owner, *f), meta.icon)
	}
}

func withoutExt(name string) string {
	return strings.TrimSuffix(name, path.Ext(name))
}

func readSmall(files ServerFiles, p string) ([]byte, error) {
	f, err := files.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, maxPictureBytes))
}

// recognitions are Modrinth's answers about files, by SHA-1, for every
// server: one file on several servers is asked about once.
type recognition struct {
	project, versionID, version, filename string
	requires                              []string
	at                                    time.Time
}

// askAgain is how long a file Modrinth did not know is left before it is
// asked about again.
const askAgain = 24 * time.Hour

// recognise asks Modrinth, in one go, which of the found files it has, and
// names those as their projects.
func (s *Server) recognise(ctx context.Context, found []FoundFile) {
	now := time.Now()
	var ask []string
	for i := range found {
		f := &found[i]
		if f.SHA1 == "" {
			continue
		}
		if r, ok := s.recognitions.Load(f.SHA1); ok && now.Sub(r.(recognition).at) < askAgain {
			continue
		}
		if f.Looked == 0 || (f.Project == "" && now.Sub(time.Unix(f.Looked, 0)) > askAgain) {
			ask = append(ask, f.SHA1)
		}
	}
	for start := 0; start < len(ask); start += 500 {
		chunk := ask[start:min(start+500, len(ask))]
		body, err := s.catalog.post(ctx, "https://api.modrinth.com/v2/version_files", map[string]any{"hashes": chunk, "algorithm": "sha1"})
		if err != nil {
			s.Log.Debug("could not ask Modrinth about found files", "error", err)
			break
		}
		var versions map[string]struct {
			ID            string `json:"id"`
			ProjectID     string `json:"project_id"`
			VersionNumber string `json:"version_number"`
			Files         []struct {
				Filename string            `json:"filename"`
				Hashes   map[string]string `json:"hashes"`
			} `json:"files"`
			Dependencies []struct {
				VersionID string `json:"version_id"`
				ProjectID string `json:"project_id"`
				Type      string `json:"dependency_type"`
			} `json:"dependencies"`
		}
		if json.Unmarshal(body, &versions) != nil {
			break
		}
		for _, h := range chunk {
			v := versions[h]
			rec := recognition{project: v.ProjectID, versionID: v.ID, version: v.VersionNumber, at: now}
			for _, file := range v.Files {
				if file.Hashes["sha1"] == h {
					rec.filename = file.Filename
				}
			}
			for _, d := range v.Dependencies {
				if d.Type != "required" {
					continue
				}
				// One named by its version alone is that version's project;
				// one that cannot be told is a need nothing meets.
				project := d.ProjectID
				if project == "" && d.VersionID != "" {
					project = "version:" + d.VersionID
					if dv, err := s.modrinthVersion(ctx, d.VersionID); err == nil && dv.ProjectID != "" {
						project = dv.ProjectID
					}
				}
				if project != "" {
					rec.requires = append(rec.requires, project)
				}
			}
			s.recognitions.Store(h, rec)
		}
	}
	var ids []string
	for i := range found {
		f := &found[i]
		if r, ok := s.recognitions.Load(f.SHA1); ok && f.SHA1 != "" {
			rec := r.(recognition)
			if f.Looked == 0 || rec.at.Unix() > f.Looked {
				f.Looked = rec.at.Unix()
				f.Project, f.VersionID, f.Filename, f.Requires = rec.project, rec.versionID, rec.filename, rec.requires
				if rec.project != "" && rec.version != "" {
					f.Version = rec.version
				}
			}
		}
		// Its project's name and picture are looked up, from what Modrinth
		// said now or before: one that could not be had then is had now.
		if f.Project != "" {
			ids = append(ids, f.Project)
		}
	}
	if len(ids) == 0 {
		return
	}
	projects := s.modrinthProjectsByID(ctx, ids)
	for i := range found {
		f := &found[i]
		if p, ok := projects[f.Project]; ok {
			f.Slug, f.IconURL = p.slug, p.icon
			if p.title != "" {
				f.Title = p.title
			}
		}
	}
}

// modrinthProject is what the panel shows of a Modrinth project.
type modrinthProject struct{ slug, title, icon string }

// modrinthProjectsByID looks up Modrinth projects: their slugs, names and
// pictures. What cannot be looked up just now goes without.
func (s *Server) modrinthProjectsByID(ctx context.Context, wanted []string) map[string]modrinthProject {
	seen := map[string]bool{}
	var ids []string
	for _, id := range wanted {
		if id != "" && !seen[id] && modrinthIDPattern.MatchString(id) {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	out := map[string]modrinthProject{}
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
				out[p.ID] = modrinthProject{p.Slug, p.Title, p.IconURL}
			}
		}
	}
	return out
}

// What a mod, plugin or datapack says of itself.
type selfDescribed struct {
	title, version, about string
	// icon is its picture, as it is in the file.
	icon []byte
}

// contentMeta reads what a mod, plugin or datapack file says of itself:
// Fabric's fabric.mod.json, Quilt's quilt.mod.json, Forge's and NeoForge's
// mods.toml, a plugin's plugin.yml or paper-plugin.yml, a datapack's
// pack.mcmeta.
func contentMeta(zr *zip.Reader, kind avsysv1.ContentKind) selfDescribed {
	read := func(name string) ([]byte, error) {
		f, err := zr.Open(name)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		return io.ReadAll(io.LimitReader(f, maxPictureBytes))
	}
	if kind == avsysv1.KindDatapack {
		return packMeta(read)
	}
	var meta selfDescribed
	iconAt := ""
	if raw, err := read("fabric.mod.json"); err == nil {
		var m struct {
			Name        string          `json:"name"`
			Version     string          `json:"version"`
			Description string          `json:"description"`
			Icon        json.RawMessage `json:"icon"`
		}
		if json.Unmarshal(raw, &m) == nil {
			meta.title, meta.version, meta.about, iconAt = m.Name, m.Version, m.Description, iconPath(m.Icon)
		}
	} else if raw, err := read("quilt.mod.json"); err == nil {
		var m struct {
			Loader struct {
				Version  string `json:"version"`
				Metadata struct {
					Name        string          `json:"name"`
					Description string          `json:"description"`
					Icon        json.RawMessage `json:"icon"`
				} `json:"metadata"`
			} `json:"quilt_loader"`
		}
		if json.Unmarshal(raw, &m) == nil {
			meta.title, meta.version, meta.about, iconAt = m.Loader.Metadata.Name, m.Loader.Version, m.Loader.Metadata.Description, iconPath(m.Loader.Metadata.Icon)
		}
	} else if raw, err := firstOf(read, "META-INF/neoforge.mods.toml", "META-INF/mods.toml"); err == nil {
		t := modsToml(raw)
		meta.title, meta.version, iconAt = t["displayName"], t["version"], t["logoFile"]
		if strings.Contains(meta.version, "${file.jarVersion}") {
			meta.version = ""
			if mf, err := read("META-INF/MANIFEST.MF"); err == nil {
				meta.version = manifestValue(mf, "Implementation-Version")
			}
		}
	} else if raw, err := firstOf(read, "paper-plugin.yml", "plugin.yml"); err == nil {
		var m map[string]any
		if yaml.Unmarshal(raw, &m) == nil {
			meta.title, meta.version, meta.about = scalar(m["name"]), scalar(m["version"]), scalar(m["description"])
		}
	}
	if strings.Contains(meta.version, "${") {
		meta.version = ""
	}
	meta.title, meta.version, meta.about = oneLine(meta.title), oneLine(meta.version), oneLine(meta.about)
	if iconAt != "" {
		if data, err := read(strings.TrimPrefix(path.Clean("/"+iconAt), "/")); err == nil {
			meta.icon = data
		}
	}
	return meta
}

func firstOf(read func(string) ([]byte, error), names ...string) ([]byte, error) {
	var err error
	for _, n := range names {
		var raw []byte
		if raw, err = read(n); err == nil {
			return raw, nil
		}
	}
	return nil, err
}

// packMeta reads a datapack's pack.mcmeta and pack.png.
func packMeta(read func(string) ([]byte, error)) selfDescribed {
	var meta selfDescribed
	if raw, err := read("pack.mcmeta"); err == nil {
		var m struct {
			Pack struct {
				Description json.RawMessage `json:"description"`
			} `json:"pack"`
		}
		if json.Unmarshal(bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf")), &m) == nil {
			meta.about = oneLine(plainText(m.Pack.Description))
		}
	}
	if data, err := read("pack.png"); err == nil {
		meta.icon = data
	}
	return meta
}

// iconPath is the picture a mod's metadata names: one path, or the largest
// of several by size.
func iconPath(raw json.RawMessage) string {
	var one string
	if json.Unmarshal(raw, &one) == nil {
		return one
	}
	var sized map[string]string
	if json.Unmarshal(raw, &sized) != nil {
		return ""
	}
	best, at := -1, ""
	for k, v := range sized {
		if n, err := strconv.Atoi(k); err == nil && n > best {
			best, at = n, v
		}
	}
	return at
}

var tomlLine = regexp.MustCompile(`^\s*([A-Za-z_][A-Za-z0-9_]*)\s*=\s*(?:"((?:[^"\\]|\\.)*)"|'([^']*)')`)

// modsToml reads the few plain strings wanted of a Forge or NeoForge
// mods.toml: those at its top, and those of its first [[mods]], which win.
func modsToml(raw []byte) map[string]string {
	top, first := map[string]string{}, map[string]string{}
	in, mods := top, 0
	for _, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") {
			in = nil
			if trimmed == "[[mods]]" {
				mods++
				if mods == 1 {
					in = first
				}
			}
			continue
		}
		if in == nil {
			continue
		}
		if m := tomlLine.FindStringSubmatch(line); m != nil {
			v := m[3]
			if m[2] != "" || m[3] == "" {
				v = strings.ReplaceAll(strings.ReplaceAll(m[2], `\"`, `"`), `\\`, `\`)
			}
			if _, had := in[m[1]]; !had {
				in[m[1]] = v
			}
		}
	}
	for k, v := range first {
		top[k] = v
	}
	return top
}

// manifestValue is one value of a jar's META-INF/MANIFEST.MF.
func manifestValue(raw []byte, name string) string {
	for _, line := range strings.Split(string(raw), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimRight(line, "\r"), name+":"); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// scalar is a YAML value that is a string or a number, as text.
func scalar(v any) string {
	switch v := v.(type) {
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	}
	return ""
}

var formatCodes = regexp.MustCompile(`§.?`)

// oneLine is what a file says of itself, as one line of plain text: without
// the game's formatting codes, its line breaks, tabs and other control
// characters made spaces, and spaces run together.
func oneLine(s string) string {
	s = formatCodes.ReplaceAllString(s, "")
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' {
			return ' '
		}
		return r
	}, s)
	return strings.Join(strings.Fields(s), " ")
}

// plainText is the text of a game text component: a string, an object with
// text and extra, or a list of them, without formatting codes.
func plainText(raw json.RawMessage) string {
	var b strings.Builder
	var walk func(v any)
	walk = func(v any) {
		switch v := v.(type) {
		case string:
			b.WriteString(v)
		case []any:
			for _, x := range v {
				walk(x)
			}
		case map[string]any:
			walk(v["text"])
			walk(v["extra"])
		}
	}
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return ""
	}
	walk(v)
	return formatCodes.ReplaceAllString(b.String(), "")
}

// foundIcons holds the pictures found files have of their own, drawn again
// by the panel, by foundIconKey.
type foundIcons struct {
	mu   sync.Mutex
	pngs map[string][]byte
}

// foundIconSide is how large the panel draws found files' pictures.
const foundIconSide = 64

// foundIconKey is what a found file's picture is kept by: the server it is
// on, owner (namespace/name), and the file as it is. One server's picture
// is never another's, though their files have the same name and time.
func foundIconKey(owner string, f FoundFile) string {
	if f.SHA1 != "" {
		return owner + "\x00" + f.SHA1
	}
	return owner + "\x00" + f.Path + "@" + strconv.FormatInt(f.Modified, 10)
}

// keepFoundIcon draws a found file's picture again and keeps it, and says
// whether it is one.
func (s *Server) keepFoundIcon(key string, raw []byte) bool {
	png, err := squarePNG(bytes.NewReader(raw), foundIconSide)
	if err != nil {
		return false
	}
	s.foundIcons.mu.Lock()
	defer s.foundIcons.mu.Unlock()
	if s.foundIcons.pngs == nil || len(s.foundIcons.pngs) >= 2048 {
		s.foundIcons.pngs = map[string][]byte{}
	}
	s.foundIcons.pngs[key] = png
	return true
}

// keepFoundPNG keeps a picture drawn already.
func (s *Server) keepFoundPNG(key string, png []byte) {
	s.foundIcons.mu.Lock()
	defer s.foundIcons.mu.Unlock()
	if s.foundIcons.pngs == nil || len(s.foundIcons.pngs) >= 2048 {
		s.foundIcons.pngs = map[string][]byte{}
	}
	s.foundIcons.pngs[key] = png
}

func (s *Server) foundIcon(key string) ([]byte, bool) {
	s.foundIcons.mu.Lock()
	defer s.foundIcons.mu.Unlock()
	png, ok := s.foundIcons.pngs[key]
	return png, ok
}

// lookAgainAfter is how soon after one look someone opening a server's
// Content page has its folders looked at again.
const lookAgainAfter = 10 * time.Second

// lookAtFound has a server's folders looked at now, unless that was done a
// moment ago, and answers at once: what the look finds comes as the
// server's next event, as any change of it does. With ?wait=true it answers
// once the look is done. Anyone who sees the server may ask: it changes
// nothing but what the panel knows.
func (s *Server) lookAtFound(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, mc, err := s.server(r, u, store.RoleViewer)
	if err != nil {
		return err
	}
	key := client.ObjectKeyFromObject(mc)
	st := s.foundStateOf(key)
	st.mu.Lock()
	due := time.Since(st.last) >= lookAgainAfter
	if due {
		// Asked for again while this one runs, it is not run twice.
		st.last = time.Now()
	}
	st.mu.Unlock()
	if !due || s.filesInFlux(mc) {
		writeJSON(w, http.StatusOK, s.view(a.Role, mc))
		return nil
	}
	// Not the request's: a page left or reloaded does not cut it short.
	done := make(chan struct{})
	go func() {
		defer close(done)
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), lookTime)
		defer cancel()
		if err := s.lookAtContent(ctx, mc); err != nil {
			s.Log.Debug("could not look at a server's folders", "server", mc.Name, "error", err)
		}
	}()
	if r.URL.Query().Get("wait") != "true" {
		writeJSON(w, http.StatusAccepted, s.view(a.Role, mc))
		return nil
	}
	select {
	case <-done:
	case <-r.Context().Done():
		return r.Context().Err()
	}
	if err := s.Client.Get(r.Context(), key, mc); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, s.view(a.Role, mc))
	return nil
}

// removeFound deletes a file of content found in a server's folders. Like
// content taken off its list, it is gone from the game once it restarts.
func (s *Server) removeFound(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, mc, err := s.server(r, u, store.RoleAdmin)
	if err != nil {
		return err
	}
	release, err := s.changing(mc, false)
	if err != nil {
		return err
	}
	defer release()
	key := client.ObjectKeyFromObject(mc)
	defer s.lockContent(key)()
	want := r.URL.Query().Get("path")
	i := slices.IndexFunc(foundOf(mc), func(f FoundFile) bool { return f.Path == want })
	if i < 0 {
		return &Error{Status: http.StatusNotFound, Code: "not_found", Message: "That file is not on the server any more."}
	}
	f := foundOf(mc)[i]
	if !foundPlace(f) {
		return errBadRequest("That file is not one the panel removes.")
	}
	files, err := s.contentFiles(r.Context(), mc.Namespace, mc.Name)
	if err != nil {
		s.Log.Warn("workspace unreachable", "server", mc.Name, "error", err)
		return &Error{Status: http.StatusBadGateway, Code: "workspace", Message: "The server's files are not reachable right now. The server may be restarting."}
	}
	full := path.Join(dataRoot, f.Path)
	// Only the file that was found: one put there since stays.
	if f.SHA1 != "" {
		if sum, err := contentHash(files, full); err == nil && sum != f.SHA1 {
			return &Error{Status: http.StatusConflict, Code: "changed", Message: "That file changed since it was found. Open the page again."}
		}
	}
	if f.Folder {
		err = files.RemoveAll(full)
	} else {
		err = files.Remove(full)
	}
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fileError(err)
	}
	st := s.foundStateOf(key)
	st.mu.Lock()
	st.removed[f.Path] = time.Now()
	st.mu.Unlock()
	if err := s.keepFound(r.Context(), key, func(was []FoundFile) []FoundFile {
		return slices.DeleteFunc(was, func(x FoundFile) bool { return x.Path == f.Path })
	}); err != nil {
		return err
	}
	if err := s.Client.Get(r.Context(), key, mc); err != nil {
		return err
	}
	s.audit(r.Context(), u, a.Org, "content.remove", mc.Name, f.Path)
	writeJSON(w, http.StatusOK, s.view(a.Role, mc))
	return nil
}

// foundPlace says a found file is where content is found: right in mods/ or
// plugins/, or in a world's datapacks folder.
func foundPlace(f FoundFile) bool {
	if f.Path != path.Clean(f.Path) || path.IsAbs(f.Path) || strings.HasPrefix(f.Path, "..") {
		return false
	}
	dir, name := path.Split(f.Path)
	dir = strings.TrimSuffix(dir, "/")
	if !plainName(name) || strings.HasPrefix(name, ".") {
		return false
	}
	switch f.Kind {
	case avsysv1.KindMod:
		return dir == modsDir
	case avsysv1.KindPlugin:
		return dir == pluginsDir
	case avsysv1.KindDatapack:
		world, ok := strings.CutSuffix(dir, "/datapacks")
		return ok && plainName(world) && !strings.HasPrefix(world, ".")
	}
	return false
}

// getContentIcon is the picture a file of a server's content has of its
// own: one found in its folders, or one listed as an upload, by its kind
// and name.
func (s *Server) getContentIcon(w http.ResponseWriter, r *http.Request, u *store.User) error {
	_, mc, err := s.server(r, u, store.RoleViewer)
	if err != nil {
		return err
	}
	kind, name := avsysv1.ContentKind(r.URL.Query().Get("kind")), r.URL.Query().Get("name")
	f, ok := contentFileOf(mc, kind, name)
	if !ok {
		return errNotFound
	}
	files, err := s.contentFiles(r.Context(), mc.Namespace, mc.Name)
	if err != nil {
		return errNotFound
	}
	info, err := files.Stat(path.Join(dataRoot, f.Path))
	if err != nil {
		return errNotFound
	}
	owner := client.ObjectKeyFromObject(mc).String()
	key := owner + "\x00" + f.Path + "@" + strconv.FormatInt(info.Size(), 10) + "@" + strconv.FormatInt(info.ModTime().UnixNano(), 10)
	png, ok := s.foundIcon(key)
	if !ok {
		f.Size, f.Modified = info.Size(), info.ModTime().Unix()
		s.readFound(owner, files, &f)
		if png, ok = s.foundIcon(foundIconKey(owner, f)); !ok {
			return errNotFound
		}
		s.keepFoundPNG(key, png)
	}
	servePicture(w, r, png)
	return nil
}

// contentFileOf is the file of a server's content of this kind and name:
// one found in its folders, or an upload, kept with the uploads or, for a
// datapack that is a folder, in its world.
func contentFileOf(mc *avsysv1.MinecraftServer, kind avsysv1.ContentKind, name string) (FoundFile, bool) {
	if !plainName(name) {
		return FoundFile{}, false
	}
	for _, f := range foundOf(mc) {
		if f.Kind == kind && path.Base(f.Path) == name && foundPlace(f) {
			return FoundFile{Path: f.Path, Kind: f.Kind, Folder: f.Folder}, true
		}
	}
	for _, m := range mc.Spec.Mods {
		if m.Source != avsysv1.SourceUpload || m.Kind != kind || m.Project != name {
			continue
		}
		if keptInPlace(m) {
			if !inPlaceFile(m) {
				return FoundFile{}, false
			}
			return FoundFile{Path: m.File, Kind: kind, Folder: kind == avsysv1.KindDatapack && !strings.HasSuffix(strings.ToLower(name), ".zip")}, true
		}
		return FoundFile{Path: strings.TrimPrefix(path.Join(controller.ContentDir, name), dataRoot+"/"), Kind: kind}, true
	}
	return FoundFile{}, false
}

// DirFiles are a server's files kept in a folder of this machine, root
// standing for /data: for tests, which have no workspace to reach.
func DirFiles(root string) ServerFiles { return dirFiles(root) }

type dirFiles string

func (d dirFiles) at(p string) (string, error) {
	rel, ok := strings.CutPrefix(path.Clean(p), dataRoot)
	if !ok || (rel != "" && !strings.HasPrefix(rel, "/")) {
		return "", fs.ErrNotExist
	}
	return string(d) + rel, nil
}

func (d dirFiles) ReadDir(p string) ([]os.FileInfo, error) {
	full, err := d.at(p)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(full)
	if err != nil {
		return nil, err
	}
	out := make([]os.FileInfo, 0, len(entries))
	for _, e := range entries {
		if info, err := e.Info(); err == nil {
			out = append(out, info)
		}
	}
	return out, nil
}

func (d dirFiles) Stat(p string) (os.FileInfo, error) {
	full, err := d.at(p)
	if err != nil {
		return nil, err
	}
	return os.Stat(full)
}

func (d dirFiles) Open(p string) (ServerFile, error) {
	full, err := d.at(p)
	if err != nil {
		return nil, err
	}
	return os.Open(full)
}

func (d dirFiles) Remove(p string) error {
	full, err := d.at(p)
	if err != nil {
		return err
	}
	return os.Remove(full)
}

func (d dirFiles) MkdirAll(p string) error {
	full, err := d.at(p)
	if err != nil {
		return err
	}
	return os.MkdirAll(full, 0o755)
}

func (d dirFiles) Create(p string) (io.WriteCloser, error) {
	full, err := d.at(p)
	if err != nil {
		return nil, err
	}
	return os.Create(full)
}

func (d dirFiles) RemoveAll(p string) error {
	full, err := d.at(p)
	if err != nil {
		return err
	}
	return os.RemoveAll(full)
}
