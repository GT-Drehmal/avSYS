package api

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
	"github.com/GT-Drehmal/avSYS/internal/backup"
	"github.com/GT-Drehmal/avSYS/internal/store"
)

// A world's datapacks are in its datapacks folder, and go wherever it goes:
// the library shows them before the world is put in a server, where they
// are then found as the server's content (found.go), as regular entries.
// They are told as a server's are: .zip files, and folders with a
// pack.mcmeta, but Bukkit's own; each by its name, with what its
// pack.mcmeta says of it and its pack.png.
//
// They are read as a world is kept, and kept with it in the store, so that
// listing the library reads no snapshot: one uploaded, or kept as its own
// files in place of an archive, from the folder it was unpacked in; one
// saved from a server or a snapshot, from its snapshot, of which only the
// list of its datapacks folder is read, and only the pack.mcmeta and
// pack.png of each folder and each .zip are taken out. A world kept before
// they were read is read once, as the panel starts, and before then as soon
// as the library is looked at, in the background either way.

// worldDatapack is one of a world's datapacks, as it is kept and shown.
type worldDatapack struct {
	// Name is its name in the world's datapacks folder: x.zip, or a
	// folder's name.
	Name string `json:"name"`
	// Title is what it is called: its name, without .zip for a file.
	Title string `json:"title"`
	// About is what its pack.mcmeta says of it, on one line.
	About string `json:"about,omitempty"`
	// Folder is one that is a folder rather than a .zip.
	Folder bool `json:"folder,omitempty"`
	// Picture says it has a pack.png, served at .../datapacks/icon.
	Picture bool `json:"picture,omitempty"`
}

// readDatapacks reads the datapacks of the world whose folder is level,
// under /data among files, and draws their pictures as found ones are, by
// name. A world without a datapacks folder has none.
func readDatapacks(ctx context.Context, files ServerFiles, level string) ([]worldDatapack, map[string][]byte, error) {
	dir := path.Join(dataRoot, level, "datapacks")
	entries, err := files.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return []worldDatapack{}, nil, nil
	} else if err != nil {
		return nil, nil, err
	}
	out := []worldDatapack{}
	pictures := map[string][]byte{}
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		if len(out) >= maxFound {
			break
		}
		name := e.Name()
		if strings.HasPrefix(name, ".") || !plainName(name) || !isDatapack(files, dir, e) {
			continue
		}
		full := path.Join(dir, name)
		d := worldDatapack{Name: name, Title: name, Folder: e.IsDir()}
		var meta selfDescribed
		if d.Folder {
			meta = packMeta(func(n string) ([]byte, error) { return readSmall(files, path.Join(full, n)) })
		} else {
			d.Title = withoutExt(name)
			meta = zipPackMeta(files, full, e.Size())
		}
		d.About = meta.about
		if meta.icon != nil {
			if png, err := squarePNG(bytes.NewReader(meta.icon), foundIconSide); err == nil {
				pictures[name], d.Picture = png, true
			}
		}
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := strings.ToLower(out[i].Title), strings.ToLower(out[j].Title)
		if a != b {
			return a < b
		}
		return out[i].Name < out[j].Name
	})
	return out, pictures, nil
}

// zipPackMeta reads what a datapack that is a .zip says of itself, from its
// pack.mcmeta and pack.png alone. One too large, or not a zip, says nothing.
func zipPackMeta(files ServerFiles, full string, size int64) selfDescribed {
	if size > maxJar {
		return selfDescribed{}
	}
	f, err := files.Open(full)
	if err != nil {
		return selfDescribed{}
	}
	defer f.Close()
	zr, err := zip.NewReader(f, size)
	if err != nil {
		return selfDescribed{}
	}
	return contentMeta(zr, avsysv1.KindDatapack)
}

// snapshotFolder is a world's datapacks folder in its snapshot, and the
// folders in it, as listed there, with the files taken out of it under a
// folder here: what is listed is there, as the snapshot has it, whether or
// not it was taken out.
type snapshotFolder struct {
	ServerFiles
	dir     string
	entries []os.FileInfo
	listed  map[string]os.FileInfo
}

func (s snapshotFolder) ReadDir(p string) ([]os.FileInfo, error) {
	if path.Clean(p) == s.dir {
		return s.entries, nil
	}
	return s.ServerFiles.ReadDir(p)
}

func (s snapshotFolder) Stat(p string) (os.FileInfo, error) {
	if info, ok := s.listed[path.Clean(p)]; ok {
		return info, nil
	}
	return s.ServerFiles.Stat(p)
}

// What is taken out of a world's snapshot to read its datapacks is held to
// so much: a datapack that is a .zip larger than maxKeptDatapackZip, or one
// past maxKeptDatapackBytes in all, is listed by its name alone, without
// what its pack.mcmeta says or its picture; and a pack.mcmeta or pack.png
// of a folder larger than a picture may be is left where it is.
var (
	maxKeptDatapackZip   int64 = 64 << 20
	maxKeptDatapackBytes int64 = 512 << 20
	// datapackRoom is how much room there is to take them out into, of
	// which the room kept free while a world is unpacked stays free.
	datapackRoom = freeSpace
)

// snapshotEntry is an entry of a snapshot's folder, as a file's is.
type snapshotEntry struct{ backup.Entry }

func (e snapshotEntry) Name() string       { return e.Entry.Name }
func (e snapshotEntry) Size() int64        { return e.Entry.Size }
func (e snapshotEntry) ModTime() time.Time { return e.Modified }
func (e snapshotEntry) IsDir() bool        { return e.Dir }
func (e snapshotEntry) Sys() any           { return nil }
func (e snapshotEntry) Mode() fs.FileMode {
	if e.Dir {
		return fs.ModeDir | 0o755
	}
	return 0o644
}

// readKeptDatapacks reads the datapacks of a world kept as its own files,
// whose folder is level, from its snapshot. Its datapacks folder, and the
// folders in it, are listed, with how large each file is; then only what is
// needed of the datapacks read is taken out (keptDatapacksPlan), and only
// with room for it on the panel.
func (s *Server) readKeptDatapacks(ctx context.Context, org string, snap *backup.Snapshot, level string) ([]worldDatapack, map[string][]byte, error) {
	dir := path.Join(dataRoot, level, "datapacks")
	top, err := s.Backups.Entries(ctx, org, snap.ID, dir)
	if err != nil {
		return nil, nil, err
	}
	// The folders that may be datapacks, to as many as could be read.
	var folders []string
	for _, e := range top {
		if e.Dir && visibleEntry(e) && !madeBySoftware[e.Name] && len(folders) < 4*maxFound {
			folders = append(folders, e.Path)
		}
	}
	inner, err := s.Backups.Entries(ctx, org, snap.ID, folders...)
	if err != nil {
		return nil, nil, err
	}
	plan := keptDatapacksPlan(top, inner)
	work, done, err := s.worldWork(org)
	if err != nil {
		return nil, nil, err
	}
	defer done()
	if len(plan.take) > 0 {
		if free, err := datapackRoom(work); err != nil {
			return nil, nil, err
		} else if free < plan.bytes+worldRoomReserve {
			return nil, nil, errNoRoomFor(free)
		}
	}
	if err := s.Backups.Extract(ctx, org, snap.ID, plan.take, work); err != nil {
		return nil, nil, err
	}
	files := snapshotFolder{ServerFiles: DirFiles(filepath.Join(work, keptData)), dir: dir, entries: plan.entries, listed: plan.listed}
	return readDatapacks(ctx, files, level)
}

func visibleEntry(e backup.Entry) bool { return !strings.HasPrefix(e.Name, ".") && plainName(e.Name) }

// datapacksPlan is what is taken out of a world's snapshot to read its
// datapacks, and how large that is in all; and what its datapacks folder,
// and the folders in it, were listed as having.
type datapacksPlan struct {
	take    []string
	bytes   int64
	entries []os.FileInfo
	listed  map[string]os.FileInfo
}

// keptDatapacksPlan is what is taken out of a world's snapshot to read its
// datapacks, from what its datapacks folder has (top) and the folders in it
// have (inner): for the datapacks readDatapacks reads, in its order, to as
// many as it reads, each .zip and each folder's pack.mcmeta and pack.png,
// within the bounds above.
func keptDatapacksPlan(top, inner []backup.Entry) datapacksPlan {
	plan := datapacksPlan{entries: make([]os.FileInfo, 0, len(top)), listed: map[string]os.FileInfo{}}
	for _, e := range inner {
		plan.listed[e.Path] = snapshotEntry{e}
	}
	take := func(p string, size int64) {
		if plan.bytes+size <= maxKeptDatapackBytes {
			plan.take, plan.bytes = append(plan.take, p), plan.bytes+size
		}
	}
	n := 0
	for _, e := range top {
		plan.entries = append(plan.entries, snapshotEntry{e})
		plan.listed[e.Path] = snapshotEntry{e}
		if !visibleEntry(e) || n >= maxFound {
			continue
		}
		if e.Dir {
			meta, ok := plan.listed[e.Path+"/pack.mcmeta"]
			if madeBySoftware[e.Name] || !ok || meta.IsDir() {
				continue
			}
			n++
			for _, name := range []string{"pack.mcmeta", "pack.png"} {
				if f, ok := plan.listed[e.Path+"/"+name]; ok && !f.IsDir() && f.Size() <= maxPictureBytes {
					take(e.Path+"/"+name, f.Size())
				}
			}
		} else if strings.HasSuffix(strings.ToLower(e.Name), ".zip") {
			n++
			if e.Size <= maxKeptDatapackZip {
				take(e.Path, e.Size)
			}
		}
	}
	return plan
}

// keepDatapacks keeps what was read of a world's datapacks with it, and
// says it on w.
func (s *Server) keepDatapacks(ctx context.Context, w *store.World, list []worldDatapack, pictures map[string][]byte) error {
	raw, err := json.Marshal(list)
	if err != nil {
		return err
	}
	if err := s.Store.SetWorldDatapacks(ctx, w.OrgID, w.ID, string(raw), pictures); err != nil {
		return err
	}
	w.Datapacks = string(raw)
	return nil
}

// keepUnpackedDatapacks keeps with a world the datapacks read where it was
// unpacked, as it is kept. Those that could not be read there are read
// later, from its snapshot.
func (s *Server) keepUnpackedDatapacks(ctx context.Context, unpacked *unpackedWorld, w *store.World) {
	if unpacked == nil || unpacked.datapacks == nil {
		return
	}
	if err := s.keepDatapacks(ctx, w, unpacked.datapacks, unpacked.pictures); err != nil && !errors.Is(err, store.ErrNotFound) {
		s.Log.Warn("could not keep a world's datapacks", "world", w.ID, "error", err)
	}
}

// NoteWorldDatapacks reads the datapacks of a library world from files, its
// folder level under /data there, and keeps them with it, as those of a
// world unpacked are: for the browser tests' panel, which keeps no worlds.
func (s *Server) NoteWorldDatapacks(ctx context.Context, orgID, id string, files ServerFiles, level string) error {
	w, err := s.Store.World(ctx, orgID, id)
	if err != nil {
		return err
	}
	list, pictures, err := readDatapacks(ctx, files, level)
	if err != nil {
		return err
	}
	return s.keepDatapacks(ctx, w, list, pictures)
}

// datapacksOf are a world's datapacks, and whether they have been read.
func datapacksOf(w *store.World) ([]worldDatapack, bool) {
	if w.Datapacks == "" {
		return nil, false
	}
	var list []worldDatapack
	if json.Unmarshal([]byte(w.Datapacks), &list) != nil {
		return nil, false
	}
	return list, true
}

// worldsReading holds, by organization ID and world ID, the worlds whose
// datapacks are being read from their snapshot now; worldsReadFailed, when
// that last failed, for it not to be tried again each time the library is
// looked at.
var (
	worldsReading    sync.Map
	worldsReadFailed sync.Map
)

// readAgainAfter is how long a world whose datapacks could not be read is
// left before they are tried again.
const readAgainAfter = 10 * time.Minute

// datapackReads holds the reads of worlds' datapacks from their snapshots
// to a couple at a time.
var datapackReads = make(chan struct{}, 2)

func datapacksReading(orgID, id string) bool {
	_, ok := worldsReading.Load(orgID + "/" + id)
	return ok
}

// readDatapacksOf reads a world's datapacks from its snapshot, and keeps
// them with it, unless that is being done already.
func (s *Server) readDatapacksOf(ctx context.Context, org *store.Org, w *store.World, snap *backup.Snapshot) error {
	key := org.ID + "/" + w.ID
	if _, busy := worldsReading.LoadOrStore(key, true); busy {
		return nil
	}
	defer worldsReading.Delete(key)
	select {
	case datapackReads <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-datapackReads }()
	list, pictures, err := s.readKeptDatapacks(ctx, org.Slug, snap, keptRoot(w, snap))
	if err == nil {
		err = s.keepDatapacks(ctx, w, list, pictures)
	}
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		worldsReadFailed.Store(key, time.Now())
		return err
	}
	worldsReadFailed.Delete(key)
	return nil
}

// toRead says a world's datapacks are to be read from its snapshot: one kept
// as its own files, there to be used, whose datapacks have not been.
func toRead(w *store.World, snap *backup.Snapshot, busy bool) bool {
	return w.Datapacks == "" && w.Failed == "" && !busy && snap != nil && snap.Form == backup.FormFolders
}

// readDatapacksSoon has a world's datapacks read from its snapshot in the
// background, if they are to be and were not tried a moment ago, and says
// whether they are being read.
func (s *Server) readDatapacksSoon(org *store.Org, w *store.World, snap *backup.Snapshot, busy bool) bool {
	if !toRead(w, snap, busy) {
		return false
	}
	key := org.ID + "/" + w.ID
	if datapacksReading(org.ID, w.ID) {
		return true
	}
	if at, ok := worldsReadFailed.Load(key); ok && time.Since(at.(time.Time)) < readAgainAfter {
		return false
	}
	world, kept := *w, *snap
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), operationTimeout)
		defer cancel()
		if err := s.readDatapacksOf(ctx, org, &world, &kept); err != nil {
			s.Log.Warn("could not read a world's datapacks", "org", org.Slug, "world", world.ID, "error", err)
		}
	}()
	return true
}

// ReadLibraryDatapacks reads the datapacks of every world of every
// organization's library that was kept before they were read, one at a
// time, as the panel starts.
func (s *Server) ReadLibraryDatapacks(ctx context.Context) {
	if !s.Backups.Available() {
		return
	}
	for _, slug := range s.Backups.Organizations() {
		if ctx.Err() != nil {
			return
		}
		org, err := s.Store.OrgBySlug(ctx, slug)
		if err != nil {
			continue
		}
		a := &access{Org: org}
		busy := worldsBusy(a)
		worlds, err := s.Store.Worlds(ctx, org.ID)
		if err != nil {
			s.Log.Warn("could not list an organization's worlds to read their datapacks", "org", slug, "error", err)
			continue
		}
		list, err := s.templateSnapshots(ctx, org)
		if err != nil {
			s.Log.Warn("could not list an organization's worlds to read their datapacks", "org", slug, "error", err)
			continue
		}
		snaps := librarySnapshots(list)
		for i := range worlds {
			if ctx.Err() != nil {
				return
			}
			snap, ok := snaps[worlds[i].ID]
			if !ok || !toRead(&worlds[i], &snap, busy[worlds[i].ID]) {
				continue
			}
			if err := s.readDatapacksOf(ctx, org, &worlds[i], &snap); err != nil && ctx.Err() == nil {
				s.Log.Warn("could not read a world's datapacks", "org", slug, "world", worlds[i].ID, "error", err)
			}
		}
	}
}

// getWorldDatapacks lists a world's datapacks: none while they have not
// been read.
func (s *Server) getWorldDatapacks(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, err := s.org(r, u, store.RoleViewer)
	if err != nil {
		return err
	}
	world, err := s.Store.World(r.Context(), a.Org.ID, r.PathValue("world"))
	if errors.Is(err, store.ErrNotFound) {
		return errNotFound
	} else if err != nil {
		return err
	}
	list, _ := datapacksOf(world)
	if list == nil {
		list = []worldDatapack{}
	}
	writeJSON(w, http.StatusOK, list)
	return nil
}

// getWorldDatapackIcon is the picture of one of a world's datapacks,
// ?name= by its name: 404 for one with none.
func (s *Server) getWorldDatapackIcon(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, err := s.org(r, u, store.RoleViewer)
	if err != nil {
		return err
	}
	world, err := s.Store.World(r.Context(), a.Org.ID, r.PathValue("world"))
	if errors.Is(err, store.ErrNotFound) {
		return errNotFound
	} else if err != nil {
		return err
	}
	name := r.URL.Query().Get("name")
	if !plainName(name) {
		return errNotFound
	}
	return s.serveStored(w, r, store.WorldDatapackImage(world.ID, name))
}
