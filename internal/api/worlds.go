package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	"sigs.k8s.io/controller-runtime/pkg/client"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
	"github.com/GT-Drehmal/avSYS/internal/backup"
	"github.com/GT-Drehmal/avSYS/internal/controller"
	"github.com/GT-Drehmal/avSYS/internal/store"
)

// An organization's library of worlds: maps kept to start servers from, or
// to put in place of a server's world. Each is kept as a snapshot of its own
// in the organization's repository, as the world's own files, laid out as a
// server has them (backup.FormFolders): the world at /data/world, and the
// Nether and the End beside it where they were kept apart. restic keeps a
// chunk of a file once, whichever snapshot has it: a server made from a
// world of the library, and each snapshot of that server, share all of the
// world they have not changed with it.
//
// One uploaded is unpacked on the panel and kept (worldunpack.go). One saved
// from a server or a snapshot is a copy of that snapshot with only its
// world's folders, which copies none of their files: there it is in the
// folder the server had it in, which is the world's Root. Putting one in a
// server is the import that puts any world in, with its folders put back
// where an archive would be unpacked. It is downloaded as a zip of a
// single-player save, made as it is sent.
//
// The library kept each world as an archive before: one uploaded as it was
// sent, one saved as a tar of its world's folders. Those still work as they
// did, and are kept as their own files instead as the panel starts
// (worldsunpack.go).

// libraryHost is what a world in the library is kept under.
func libraryHost(id string) string { return "world-" + id }

// keptRoot is the world's folder in a world's snapshot: in its archive, for
// one kept as one, and under /data, for one kept as its own files. One
// uploaded, or kept as an archive before, is at /data/world: it was
// unpacked there, whatever the folder in its archive.
func keptRoot(w *store.World, snap *backup.Snapshot) string {
	if snap != nil && snap.Form == backup.FormFolders && w.Source == "upload" {
		return keptWorld
	}
	return w.Root
}

// worldFoldersIn are the folders of a world kept as its own files in its
// snapshot, its Nether and End beside it included, whichever it has.
func worldFoldersIn(root string) []string {
	var out []string
	for _, dir := range worldFolders(root) {
		out = append(out, "/"+keptData+dir)
	}
	return out
}

// worldsSaving holds, by organization ID and world ID, the worlds being
// saved or deleted, one thing at a time.
var worldsSaving sync.Map

// claimWorld holds a world while it is saved or deleted.
func claimWorld(a *access, id string) (release func(), err error) {
	key := a.Org.ID + "/" + id
	if _, busy := worldsSaving.LoadOrStore(key, true); busy {
		return nil, &Error{Status: http.StatusConflict, Code: "busy", Message: "This world is being saved or deleted. Try again when that has finished."}
	}
	return func() { worldsSaving.Delete(key) }, nil
}

func worldBusy(a *access, id string) bool {
	_, busy := worldsSaving.Load(a.Org.ID + "/" + id)
	return busy
}

// worldsBusy are an organization's worlds being saved or deleted now. It is
// asked before the worlds are read: one whose work ends in between is then
// seen as it was left, never as neither being saved nor saved.
func worldsBusy(a *access) map[string]bool {
	out := map[string]bool{}
	worldsSaving.Range(func(key, _ any) bool {
		if id, ok := strings.CutPrefix(key.(string), a.Org.ID+"/"); ok {
			out[id] = true
		}
		return true
	})
	return out
}

// worldView is a world as it is shown.
type worldView struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	CreatedBy   string    `json:"createdBy"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
	// Root is its folder: under /data in its snapshot, for one kept as its
	// own files, and in its archive, "" for the top, for one kept as one.
	Root string `json:"root"`
	// Version is the game version it was last opened in, where known.
	Version string `json:"version,omitempty"`
	// File is the archive's name, for one uploaded.
	File string `json:"file,omitempty"`
	// Size is how large what is kept of it is, and Unpacked how large the
	// world is, where known: the same for one kept as its own files.
	Size     int64 `json:"size"`
	Unpacked int64 `json:"unpacked,omitempty"`
	// Kept is how it is kept, once it is: "folders", as its own files, or
	// "archive", as the library kept worlds before.
	Kept string `json:"kept,omitempty"`
	// Unpacking is for one kept as an archive that is being kept as its own
	// files now: it is used as it is meanwhile. UnpackError says why it
	// could not be, for one that could not.
	Unpacking   bool   `json:"unpacking,omitempty"`
	UnpackError string `json:"unpackError,omitempty"`
	// Source is where it came from: "upload", "server:<name>" or
	// "snapshot:<server>".
	Source string `json:"source"`
	// Saving is for one whose archive is still being made.
	Saving bool `json:"saving,omitempty"`
	// Ready is for one whose archive is there to be used.
	Ready bool `json:"ready"`
	// Error says why its archive could not be made, for one that was not.
	Error string `json:"error,omitempty"`
	// ResourcePack is the pack it is played with, given to each server it
	// is given to: one from an address, or one uploaded, with no address.
	ResourcePack *avsysv1.ResourcePackSpec `json:"resourcePack,omitempty"`
	// Datapacks is how many datapacks it has, once they have been read;
	// ReadingDatapacks says they are being read now.
	Datapacks        *int `json:"datapacks,omitempty"`
	ReadingDatapacks bool `json:"readingDatapacks,omitempty"`
}

// librarySnapshots are the snapshots of the library's worlds, by world.
func librarySnapshots(list []backup.Snapshot) map[string]backup.Snapshot {
	out := map[string]backup.Snapshot{}
	for _, snap := range list {
		if id, ok := strings.CutPrefix(snap.Server, "world-"); ok && snap.Trigger == backup.TriggerTemplate {
			// Newest first: the first is the one.
			if _, seen := out[id]; !seen {
				out[id] = snap
			}
		}
	}
	return out
}

func worldOf(w *store.World, snaps map[string]backup.Snapshot, busy bool) worldView {
	// One being saved has none yet, or one still being written.
	snap, kept := snaps[w.ID]
	v := worldView{
		ID: w.ID, Name: w.Name, Description: w.Description, CreatedBy: w.CreatedBy, CreatedAt: w.CreatedAt, UpdatedAt: w.UpdatedAt,
		Root: w.Root, Version: w.Version, File: w.File, Size: w.Size, Unpacked: w.Unpacked, Source: w.Source,
		Saving: busy && w.Failed == "", Ready: kept && !busy && w.Failed == "", Error: w.Failed,
		ResourcePack: worldPackOf(w),
	}
	if list, read := datapacksOf(w); read {
		n := len(list)
		v.Datapacks = &n
	}
	if kept {
		v.Root = keptRoot(w, &snap)
		v.Kept = "archive"
		if snap.Form == backup.FormFolders {
			v.Kept = backup.FormFolders
		} else {
			v.Unpacking = worldUnpacking(w.OrgID, w.ID)
			v.UnpackError = w.UnpackFailed
		}
	}
	return v
}

// libraryWorld is one of the library's worlds, and the snapshot of its
// archive: nil while there is none.
func (s *Server) libraryWorld(ctx context.Context, a *access, id string) (*store.World, *backup.Snapshot, error) {
	w, err := s.Store.World(ctx, a.Org.ID, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil, &Error{Status: http.StatusNotFound, Code: "not_found", Message: "There is no such world."}
	} else if err != nil {
		return nil, nil, err
	}
	list, err := s.templateSnapshots(ctx, a.Org)
	if err != nil {
		return nil, nil, err
	}
	if snap, ok := librarySnapshots(list)[w.ID]; ok {
		return w, &snap, nil
	}
	return w, nil, nil
}

// readyWorld is one of the library's worlds that can be used now.
func (s *Server) readyWorld(ctx context.Context, a *access, id string) (*store.World, *backup.Snapshot, error) {
	busy := worldBusy(a, id)
	w, snap, err := s.libraryWorld(ctx, a, id)
	if err != nil {
		return nil, nil, err
	}
	if w.Failed != "" {
		return nil, nil, &Error{Status: http.StatusConflict, Code: "not_saved", Message: "This world was not saved: " + w.Failed}
	}
	if snap == nil || busy {
		return nil, nil, &Error{Status: http.StatusConflict, Code: "not_ready", Message: "This world is still being saved."}
	}
	return w, snap, nil
}

func (s *Server) listWorlds(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, err := s.org(r, u, store.RoleViewer)
	if err != nil {
		return err
	}
	busy := worldsBusy(a)
	worlds, err := s.Store.Worlds(r.Context(), a.Org.ID)
	if err != nil {
		return err
	}
	list, err := s.templateSnapshots(r.Context(), a.Org)
	if err != nil {
		return err
	}
	snaps := librarySnapshots(list)
	out := make([]worldView, len(worlds))
	for i := range worlds {
		out[i] = worldOf(&worlds[i], snaps, busy[worlds[i].ID])
		// Those of a world kept before they were read are read now, in the
		// background, and are in the list once they have been.
		if snap, ok := snaps[worlds[i].ID]; ok {
			out[i].ReadingDatapacks = s.readDatapacksSoon(a.Org, &worlds[i], &snap, busy[worlds[i].ID])
		}
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (s *Server) getWorld(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, err := s.org(r, u, store.RoleViewer)
	if err != nil {
		return err
	}
	busy := worldBusy(a, r.PathValue("world"))
	world, snap, err := s.libraryWorld(r.Context(), a, r.PathValue("world"))
	if err != nil {
		return err
	}
	v := worldOfSnap(world, snap, busy)
	v.ReadingDatapacks = s.readDatapacksSoon(a.Org, world, snap, busy)
	writeJSON(w, http.StatusOK, v)
	return nil
}

func worldOfSnap(w *store.World, snap *backup.Snapshot, busy bool) worldView {
	snaps := map[string]backup.Snapshot{}
	if snap != nil {
		snaps[w.ID] = *snap
	}
	return worldOf(w, snaps, busy)
}

// worldWords checks a world's name and description, and gives them back
// tidied.
func worldWords(name, description string) (string, string, error) {
	name, description = strings.TrimSpace(name), strings.TrimSpace(description)
	if name == "" || len([]rune(name)) > 60 || strings.Contains(name, "\n") {
		return "", "", errBadRequest("Give the world a name of one line, up to 60 characters.")
	}
	if len([]rune(description)) > 500 {
		return "", "", errBadRequest("A description can be at most 500 characters.")
	}
	for _, c := range name + description {
		if unicode.IsControl(c) && c != '\n' {
			return "", "", errBadRequest("A name or description cannot have control characters.")
		}
	}
	return name, description, nil
}

func worldNameTaken(name string) error {
	return &Error{Status: http.StatusConflict, Code: "name_taken", Message: "There is already a world called " + name + "."}
}

// uploadWorld puts an uploaded archive of a world in the library, unpacked:
// the body, or what was sent in parts as ?upload= of ?size=, with ?name=,
// ?description=, ?root= its world's folder in it, and what the page read of
// it, ?version= and ?file=. How large the world is, is as it is unpacked.
func (s *Server) uploadWorld(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, err := s.org(r, u, store.RoleOperator)
	if err != nil {
		return err
	}
	if !s.Backups.Available() {
		return errNoBackups
	}
	q := r.URL.Query()
	defer s.dropStaged(a, q.Get("upload"))
	name, description, err := worldWords(q.Get("name"), q.Get("description"))
	if err != nil {
		return err
	}
	root, err := archiveRoot(q.Get("root"))
	if err != nil {
		return err
	}
	world := &store.World{OrgID: a.Org.ID, Name: name, Description: description, CreatedBy: u.Username, Root: keptWorld, Version: q.Get("version"), File: q.Get("file"), Source: "upload"}
	if err := s.Store.CreateWorld(r.Context(), world); errors.Is(err, store.ErrWorldNameTaken) {
		return worldNameTaken(name)
	} else if err != nil {
		return err
	}
	release, err := claimWorld(a, world.ID)
	if err != nil {
		return err
	}
	defer release()
	ctx := context.WithoutCancel(r.Context())
	kept, unpacked, err := s.keepUploadedWorld(ctx, w, r, a, world, root, u.Username)
	if err == nil {
		err = s.noteTemplateSnapshot(ctx, a.Org, kept)
	}
	if err == nil {
		err = s.Store.SetWorldArchive(ctx, a.Org.ID, world.ID, world.Root, world.Version, world.Size, world.Unpacked)
	}
	if err == nil {
		s.keepUnpackedDatapacks(ctx, unpacked, world)
	}
	if err != nil {
		s.forgetWorld(ctx, a, world.ID, kept)
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return errBadRequest("Over 2 GB in one request: send it in parts.")
		}
		return snapshotError(err)
	}
	s.audit(ctx, u, a.Org, "world.create", world.Name, kept.ID)
	writeJSON(w, http.StatusCreated, worldOfSnap(world, kept, false))
	return nil
}

// forgetWorld takes back a world whose archive could not be kept, and the
// snapshot of it, if one was made.
func (s *Server) forgetWorld(ctx context.Context, a *access, id string, kept *backup.Snapshot) {
	if kept != nil {
		if err := s.Backups.Delete(ctx, a.Org.Slug, kept.ID); err != nil {
			s.Log.Error("could not remove a world's archive kept for nothing", "world", id, "snapshot", kept.ID, "error", err)
		} else {
			s.forgetTemplateSnapshots(ctx, a.Org, kept.ID)
		}
	}
	if err := s.Store.DeleteWorld(ctx, a.Org.ID, id); err != nil && !errors.Is(err, store.ErrNotFound) {
		s.Log.Warn("could not forget a world whose archive was not kept", "world", id, "error", err)
	}
}

// keepUploadedWorld unpacks the archive a request sends, the file sent in
// parts that ?upload= names or else its body, and keeps the world in it,
// whose folder in it is root, as world's, which is told how large it is.
func (s *Server) keepUploadedWorld(ctx context.Context, w http.ResponseWriter, r *http.Request, a *access, world *store.World, root, by string) (*backup.Snapshot, *unpackedWorld, error) {
	dir, done, err := s.worldWork(a.Org.Slug)
	if err != nil {
		return nil, nil, err
	}
	defer done()
	var file string
	var drop func()
	if q := r.URL.Query(); q.Get("upload") != "" {
		f, closeIt, err := s.takeStaged(a, q)
		if err != nil {
			return nil, nil, err
		}
		file = f.Name()
		closeIt()
		drop = func() { s.dropStaged(a, q.Get("upload")) }
	} else {
		file = filepath.Join(dir, "archive")
		f, err := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return nil, nil, err
		}
		_, err = io.Copy(f, http.MaxBytesReader(w, r.Body, maxUpload))
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return nil, nil, err
		}
		drop = func() { _ = os.Remove(file) }
	}
	kept, unpacked, err := s.keepUnpacked(ctx, a.Org.Slug, dir, file, root, libraryHost(world.ID), by, drop)
	if err != nil {
		return nil, nil, err
	}
	world.Size, world.Unpacked = unpacked.Bytes, unpacked.Bytes
	return kept, unpacked, nil
}

// saveWorld puts a world in the library from a snapshot, or from a server
// as it is now, of which a snapshot is taken first. It answers at once;
// the world says it is saving until its archive is made. With ?wait=true it
// answers once it is.
func (s *Server) saveWorld(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, err := s.org(r, u, store.RoleOperator)
	if err != nil {
		return err
	}
	if !s.Backups.Available() {
		return errNoBackups
	}
	var in struct{ Name, Description, Server, Snapshot string }
	if err := decode(r, &in); err != nil {
		return err
	}
	name, description, err := worldWords(in.Name, in.Description)
	if err != nil {
		return err
	}
	if (in.Server == "") == (in.Snapshot == "") {
		return errBadRequest("A world is saved from a server or from a snapshot.")
	}
	// Where its world is, what it was opened in last, and the resource pack
	// it was played with.
	var level, version, source string
	var pack *avsysv1.ResourcePackSpec
	var snap *backup.Snapshot
	var take func(ctx context.Context) (*backup.Snapshot, error)
	if in.Snapshot != "" {
		if snap, err = s.Backups.Get(r.Context(), a.Org.Slug, in.Snapshot); err != nil {
			return snapshotError(err)
		}
		if isTemplate(snap) {
			return errBadRequest("That snapshot is a template's. Make a server from the template, then save that server's world.")
		}
		record, err := s.recordOf(r.Context(), a.Org.Slug, snap.ID)
		if err != nil {
			return errNoRecord(err)
		}
		level, version, source = levelOf(&record.Spec), snap.Version, "snapshot:"+snap.Server
		pack = record.Spec.ResourcePack.DeepCopy()
	} else {
		mc := &avsysv1.MinecraftServer{}
		if err := s.Client.Get(r.Context(), client.ObjectKey{Namespace: a.Namespace, Name: in.Server}, mc); err != nil {
			return err
		}
		if err := unfinished(mc); err != nil {
			return err
		}
		if err := settled(mc); err != nil {
			return err
		}
		level, version, source = levelOf(&mc.Spec), mc.Spec.Version, "server:"+mc.Name
		pack = mc.Spec.ResourcePack.DeepCopy()
		// "Latest" is whichever it ran last: the game says, after its
		// software's name.
		if ran := controller.RanVersion(mc.Status.Version); version == "LATEST" && ran != "" {
			version = ran
		}
		take = func(ctx context.Context) (*backup.Snapshot, error) {
			op, ok := s.ops.begin(a.Namespace, a.Org.Slug, mc.Name, kindSnapshot, "")
			if !ok {
				if running := s.ops.current(a.Namespace, mc.Name); running != nil {
					return nil, heldBy(running.Kind)
				}
				return nil, errBusy
			}
			snap, err := s.takeSnapshot(ctx, a.Org.Slug, mc, op, backup.TriggerManual, u.Username)
			s.finishOperation(ctx, a.Namespace, mc.Name, kindSnapshot, snap, snap, err)
			if err == nil {
				s.audit(ctx, u, a.Org, "snapshot.create", mc.Name, snap.ID)
			}
			return snap, err
		}
	}
	if level == "" {
		return errBadRequest("Which folder the world is in could not be told from the server's settings, so the world cannot be saved here.")
	}
	if version == "LATEST" {
		version = ""
	}
	world := &store.World{OrgID: a.Org.ID, Name: name, Description: description, CreatedBy: u.Username, Root: level, Version: version, Source: source}
	if err := s.Store.CreateWorld(r.Context(), world); errors.Is(err, store.ErrWorldNameTaken) {
		return worldNameTaken(name)
	} else if err != nil {
		return err
	}
	release, err := claimWorld(a, world.ID)
	if err != nil {
		s.forgetWorld(context.WithoutCancel(r.Context()), a, world.ID, nil)
		return err
	}
	// Said before the work starts, which changes the world as it goes.
	saving := worldOfSnap(world, nil, true)
	saved := *world
	work := func(ctx context.Context) (kept *backup.Snapshot, err error) {
		defer release()
		ctx, cancel := context.WithTimeout(ctx, operationTimeout)
		defer cancel()
		defer func() {
			if err == nil {
				return
			}
			// Kept, with why, until it is deleted: whoever asked for it
			// may not be there to be told.
			ctx := context.WithoutCancel(ctx)
			if kept != nil {
				if derr := s.Backups.Delete(ctx, a.Org.Slug, kept.ID); derr != nil {
					s.Log.Error("could not remove a world's archive kept for nothing", "world", saved.ID, "snapshot", kept.ID, "error", derr)
				} else {
					s.forgetTemplateSnapshots(ctx, a.Org, kept.ID)
				}
				kept = nil
			}
			if ferr := s.Store.SetWorldFailed(ctx, a.Org.ID, saved.ID, sentence(plainly(err))); ferr != nil && !errors.Is(ferr, store.ErrNotFound) {
				s.Log.Warn("could not say why a world was not saved", "world", saved.ID, "error", ferr)
			}
			s.Log.Warn("a world was not saved to the library", "world", saved.Name, "error", err)
		}()
		from := snap
		if take != nil {
			if from, err = take(ctx); err != nil {
				return nil, err
			}
			// The pack its files have is the one its snapshot's record says.
			if record, err := s.recordOf(ctx, a.Org.Slug, from.ID); err == nil {
				pack = record.Spec.ResourcePack.DeepCopy()
			}
		}
		// Its world's folders, where the server had them: a copy of the
		// snapshot with nothing else, which copies none of their files.
		kept, err = s.Backups.CopyFolders(ctx, a.Org.Slug, from.ID, worldFoldersIn(level), libraryHost(saved.ID), u.Username)
		if errors.Is(err, backup.ErrNotFound) {
			return nil, errBadRequest("The world is not among the server's files.")
		} else if err != nil {
			return nil, err
		}
		saved.Size, saved.Unpacked = kept.Size, kept.Size
		if err := s.noteTemplateSnapshot(ctx, a.Org, kept); err != nil {
			return kept, err
		}
		if err := s.Store.SetWorldArchive(ctx, a.Org.ID, saved.ID, saved.Root, saved.Version, saved.Size, saved.Unpacked); err != nil {
			return kept, err
		}
		// Its datapacks, read from what was kept. Not read, they are read
		// when the library is next looked at.
		if err := s.readDatapacksOf(ctx, a.Org, &saved, kept); err != nil {
			s.Log.Warn("could not read the datapacks of a world saved to the library", "world", saved.Name, "error", err)
		}
		// Its resource pack, as the server had one. Without it, the world is
		// still the world: the pack can be given to it again.
		if pack != nil {
			if raw, err := s.carryPack(ctx, a, saved.ID, from.ID, pack, u.Username); err != nil {
				s.Log.Warn("a world was saved to the library without its server's resource pack", "world", saved.Name, "error", err)
			} else {
				saved.Pack = raw
			}
		}
		// Its picture, as the server had one.
		if raw, err := s.Backups.File(ctx, a.Org.Slug, from.ID, serverIconPath, snapshotIconLimit); err == nil && raw != nil {
			if data, err := squarePNG(bytes.NewReader(raw), serverIconSide); err == nil {
				_ = s.Store.SetImage(ctx, store.WorldImage(saved.ID), data)
			}
		}
		s.audit(ctx, u, a.Org, "world.create", saved.Name, kept.ID)
		return kept, nil
	}
	if r.URL.Query().Get("wait") == "true" {
		kept, err := work(context.WithoutCancel(r.Context()))
		if err != nil {
			// Asked for and waited on, it is told here, and not kept.
			s.forgetWorld(context.WithoutCancel(r.Context()), a, saved.ID, nil)
			return operationFailed(err)
		}
		writeJSON(w, http.StatusCreated, worldOfSnap(&saved, kept, false))
		return nil
	}
	go func() { _, _ = work(context.Background()) }()
	writeJSON(w, http.StatusAccepted, saving)
	return nil
}

// updateWorld renames a world, changes its description, or changes the
// resource pack it names: resourcePack null for none, one with a url for
// one from that address, or one with only enforce or prompt for whether
// players must take the one it has and what they are asked.
func (s *Server) updateWorld(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, err := s.org(r, u, store.RoleOperator)
	if err != nil {
		return err
	}
	var in struct {
		Name, Description *string
		ResourcePack      json.RawMessage `json:"resourcePack"`
	}
	if err := decode(r, &in); err != nil {
		return err
	}
	// Its pack is not changed while another is uploaded to it.
	if in.ResourcePack != nil {
		release, err := claimWorld(a, r.PathValue("world"))
		if err != nil {
			return err
		}
		defer release()
	}
	busy := in.ResourcePack == nil && worldBusy(a, r.PathValue("world"))
	world, snap, err := s.libraryWorld(r.Context(), a, r.PathValue("world"))
	if err != nil {
		return err
	}
	was := worldPackOf(world)
	var pack *avsysv1.ResourcePackSpec
	if in.ResourcePack != nil {
		if pack, err = changedPack(was, in.ResourcePack); err != nil {
			return err
		}
	}
	name, description := world.Name, world.Description
	if in.Name != nil {
		name = *in.Name
	}
	if in.Description != nil {
		description = *in.Description
	}
	if world.Name, world.Description, err = worldWords(name, description); err != nil {
		return err
	}
	if err := s.Store.UpdateWorld(r.Context(), world); errors.Is(err, store.ErrWorldNameTaken) {
		return worldNameTaken(world.Name)
	} else if err != nil {
		return err
	}
	if in.ResourcePack != nil {
		ctx := context.WithoutCancel(r.Context())
		world.Pack = packJSON(pack)
		if err := s.Store.SetWorldPack(ctx, a.Org.ID, world.ID, world.Pack); err != nil {
			return err
		}
		// An uploaded one it no longer names goes.
		if was != nil && was.Uploaded != nil && (pack == nil || pack.Uploaded == nil) {
			s.dropKeptPacks(ctx, a, libraryHost(world.ID), "")
		}
		if packJSON(was) != world.Pack {
			s.audit(ctx, u, a.Org, "world.resource-pack", world.Name, packAudit(pack))
		}
	}
	s.audit(r.Context(), u, a.Org, "world.update", world.Name, "")
	writeJSON(w, http.StatusOK, worldOfSnap(world, snap, busy))
	return nil
}

// deleteWorld takes a world out of the library. Servers that were given it
// keep theirs.
func (s *Server) deleteWorld(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, err := s.org(r, u, store.RoleAdmin)
	if err != nil {
		return err
	}
	world, _, err := s.libraryWorld(r.Context(), a, r.PathValue("world"))
	if err != nil {
		return err
	}
	release, err := claimWorld(a, world.ID)
	if err != nil {
		return err
	}
	defer release()
	ctx := context.WithoutCancel(r.Context())
	list, err := s.repoSnapshots(ctx, a.Org.Slug)
	if err != nil {
		return err
	}
	hosts := []string{libraryHost(world.ID), packHost(libraryHost(world.ID)), unpackingHost(world.ID)}
	var ids []string
	for _, snap := range list {
		if slices.Contains(hosts, snap.Server) && isTemplate(&snap) {
			ids = append(ids, snap.ID)
		}
	}
	if err := s.Backups.DeleteMany(ctx, a.Org.Slug, ids); err != nil {
		return snapshotError(err)
	}
	s.forgetTemplateSnapshotsOf(ctx, a.Org, hosts...)
	if err := s.Store.DeleteWorld(ctx, a.Org.ID, world.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	s.audit(ctx, u, a.Org, "world.delete", world.Name, "")
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// downloadWorld sends a world as a zip of a single-player save, made as it
// is sent. One kept as an archive that was uploaded is sent as it was.
func (s *Server) downloadWorld(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, err := s.org(r, u, store.RoleOperator)
	if err != nil {
		return err
	}
	world, snap, err := s.readyWorld(r.Context(), a, r.PathValue("world"))
	if err != nil {
		return err
	}
	if world.Source != "upload" || snap.Form == backup.FormFolders {
		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(saveName(world.Name)+".zip"))
		sent := &countWriter{}
		if err := s.writeWorldZip(r.Context(), a, world, snap, io.MultiWriter(w, sent)); err != nil {
			s.Log.Warn("a world's download did not finish", "world", world.ID, "error", err)
			// Nothing sent yet: said so. Once it has begun, the download is
			// cut off, so that the browser says it failed rather than keep a
			// zip without some of the world.
			if sent.n == 0 {
				w.Header().Del("Content-Disposition")
				w.Header().Del("Content-Type")
				return snapshotError(err)
			}
			panic(http.ErrAbortHandler)
		}
		return nil
	}
	file := world.File
	if file == "" {
		file = world.Name
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(file))
	if err := s.Backups.Stream(r.Context(), a.Org.Slug, snap.ID, "/"+ownWorldFile, w); err != nil {
		s.Log.Warn("a world's download did not finish", "world", world.ID, "error", err)
	}
	return nil
}

func (s *Server) getWorldIcon(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, err := s.org(r, u, store.RoleViewer)
	if err != nil {
		return err
	}
	if _, err := s.Store.World(r.Context(), a.Org.ID, r.PathValue("world")); errors.Is(err, store.ErrNotFound) {
		return errNotFound
	} else if err != nil {
		return err
	}
	return s.serveStored(w, r, store.WorldImage(r.PathValue("world")))
}

// putWorldIcon sets a world's picture, as a server's is.
func (s *Server) putWorldIcon(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, err := s.org(r, u, store.RoleOperator)
	if err != nil {
		return err
	}
	world, err := s.Store.World(r.Context(), a.Org.ID, r.PathValue("world"))
	if errors.Is(err, store.ErrNotFound) {
		return errNotFound
	} else if err != nil {
		return err
	}
	data, err := squarePNG(r.Body, serverIconSide)
	if err != nil {
		return err
	}
	if err := s.Store.SetImage(r.Context(), store.WorldImage(world.ID), data); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Server) deleteWorldIcon(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, err := s.org(r, u, store.RoleOperator)
	if err != nil {
		return err
	}
	if _, err := s.Store.World(r.Context(), a.Org.ID, r.PathValue("world")); errors.Is(err, store.ErrNotFound) {
		return errNotFound
	} else if err != nil {
		return err
	}
	if err := s.Store.DeleteImage(r.Context(), store.WorldImage(r.PathValue("world"))); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
