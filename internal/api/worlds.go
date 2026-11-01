package api

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"sigs.k8s.io/controller-runtime/pkg/client"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
	"github.com/GT-Drehmal/avSYS/internal/backup"
	"github.com/GT-Drehmal/avSYS/internal/store"
)

// An organization's library of worlds: maps kept to start servers from, or
// to put in place of a server's world. Each is an archive of the world,
// kept as a snapshot of its own in the organization's repository, the way a
// template's own world is. One uploaded is kept as it was sent; one saved
// from a server or a snapshot is a tar of its world's folders, the world as
// "world" and the Nether and the End beside it where the server kept them
// apart. Putting one in a server is the import that puts any world in.

// libraryHost is what a world in the library is kept under.
func libraryHost(id string) string { return "world-" + id }

// libraryRoot is the folder a world saved from a server or a snapshot is
// in, in its archive.
const libraryRoot = "world"

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
	// Root is its folder in its archive, "" for the top.
	Root string `json:"root"`
	// Version is the game version it was last opened in, where known.
	Version string `json:"version,omitempty"`
	// File is the archive's name, for one uploaded.
	File string `json:"file,omitempty"`
	// Size is how large its archive is, and Unpacked how large the world is
	// out of it, where known.
	Size     int64 `json:"size"`
	Unpacked int64 `json:"unpacked,omitempty"`
	// Source is where it came from: "upload", "server:<name>" or
	// "snapshot:<server>".
	Source string `json:"source"`
	// Saving is for one whose archive is still being made.
	Saving bool `json:"saving,omitempty"`
	// Ready is for one whose archive is there to be used.
	Ready bool `json:"ready"`
	// Error says why its archive could not be made, for one that was not.
	Error string `json:"error,omitempty"`
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
	_, kept := snaps[w.ID]
	return worldView{
		ID: w.ID, Name: w.Name, Description: w.Description, CreatedBy: w.CreatedBy, CreatedAt: w.CreatedAt, UpdatedAt: w.UpdatedAt,
		Root: w.Root, Version: w.Version, File: w.File, Size: w.Size, Unpacked: w.Unpacked, Source: w.Source,
		Saving: busy && w.Failed == "", Ready: kept && !busy && w.Failed == "", Error: w.Failed,
	}
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
	list, err := s.templateSnapshots(ctx, a.Org.Slug)
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
	list, err := s.templateSnapshots(r.Context(), a.Org.Slug)
	if err != nil {
		return err
	}
	snaps := librarySnapshots(list)
	out := make([]worldView, len(worlds))
	for i := range worlds {
		out[i] = worldOf(&worlds[i], snaps, busy[worlds[i].ID])
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
	writeJSON(w, http.StatusOK, worldOfSnap(world, snap, busy))
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

// uploadWorld puts an uploaded archive of a world in the library: the body,
// or what was sent in parts as ?upload= of ?size=, with ?name=,
// ?description=, ?root= its world's folder in it, and what the page read of
// it, ?version=, ?file= and ?unpacked=, how large the world is out of it.
func (s *Server) uploadWorld(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, err := s.org(r, u, store.RoleOperator)
	if err != nil {
		return err
	}
	if !s.Backups.Available() {
		return errNoBackups
	}
	q := r.URL.Query()
	name, description, err := worldWords(q.Get("name"), q.Get("description"))
	if err != nil {
		return err
	}
	root, err := archiveRoot(q.Get("root"))
	if err != nil {
		return err
	}
	unpacked, _ := strconv.ParseInt(q.Get("unpacked"), 10, 64)
	world := &store.World{OrgID: a.Org.ID, Name: name, Description: description, CreatedBy: u.Username, Root: root, Version: q.Get("version"), File: q.Get("file"), Unpacked: max(unpacked, 0), Source: "upload"}
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
	body, done, err := s.uploadBody(w, r, a)
	if err != nil {
		s.forgetWorld(ctx, a, world.ID, nil)
		return err
	}
	defer done()
	kept, err := s.Backups.KeepFile(ctx, a.Org.Slug, libraryHost(world.ID), ownWorldFile, u.Username, body)
	if err == nil {
		world.Size = kept.Size
		err = s.Store.SetWorldArchive(ctx, a.Org.ID, world.ID, world.Root, world.Version, kept.Size, world.Unpacked)
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
		}
	}
	if err := s.Store.DeleteWorld(ctx, a.Org.ID, id); err != nil && !errors.Is(err, store.ErrNotFound) {
		s.Log.Warn("could not forget a world whose archive was not kept", "world", id, "error", err)
	}
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
	// Where its world is, and what it was opened in last.
	var level, version, source string
	var snap *backup.Snapshot
	var take func(ctx context.Context) (*backup.Snapshot, error)
	if in.Snapshot != "" {
		if snap, err = s.Backups.Get(r.Context(), a.Org.Slug, in.Snapshot); err != nil {
			return snapshotError(err)
		}
		if isTemplate(snap) {
			return errBadRequest("That snapshot is a template's. Make a server from the template, then save its world.")
		}
		record, err := s.recordOf(r.Context(), a.Org.Slug, snap.ID)
		if err != nil {
			return errNoRecord(err)
		}
		level, version, source = levelOf(&record.Spec), snap.Version, "snapshot:"+snap.Server
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
		// "Latest" is whichever it ran last: the game says, after its
		// software's name.
		if fields := strings.Fields(mc.Status.Version); version == "LATEST" && len(fields) > 0 {
			version = fields[len(fields)-1]
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
		return errBadRequest("Which folder its world is in could not be told from its settings, so it cannot be saved here.")
	}
	if version == "LATEST" {
		version = ""
	}
	world := &store.World{OrgID: a.Org.ID, Name: name, Description: description, CreatedBy: u.Username, Root: libraryRoot, Version: version, Source: source}
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
		}
		folders := []backup.Folder{{Path: dataRoot + "/" + level, As: libraryRoot}}
		for _, dim := range []string{"_nether", "_the_end"} {
			folders = append(folders, backup.Folder{Path: dataRoot + "/" + level + dim, As: libraryRoot + dim})
		}
		kept, err = s.Backups.KeepArchive(ctx, a.Org.Slug, from.ID, folders, libraryHost(saved.ID), ownWorldFile, u.Username)
		if errors.Is(err, backup.ErrNotFound) {
			return kept, errBadRequest("Its world is not among its files.")
		} else if err != nil {
			return kept, err
		}
		// A tar is as large as what is in it.
		saved.Size, saved.Unpacked = kept.Size, kept.Size
		if err := s.Store.SetWorldArchive(ctx, a.Org.ID, saved.ID, saved.Root, saved.Version, saved.Size, saved.Unpacked); err != nil {
			return kept, err
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

// updateWorld renames a world or changes its description.
func (s *Server) updateWorld(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, err := s.org(r, u, store.RoleOperator)
	if err != nil {
		return err
	}
	busy := worldBusy(a, r.PathValue("world"))
	world, snap, err := s.libraryWorld(r.Context(), a, r.PathValue("world"))
	if err != nil {
		return err
	}
	var in struct{ Name, Description *string }
	if err := decode(r, &in); err != nil {
		return err
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
	list, err := s.templateSnapshots(ctx, a.Org.Slug)
	if err != nil {
		return err
	}
	var ids []string
	for _, snap := range list {
		if snap.Server == libraryHost(world.ID) && isTemplate(&snap) {
			ids = append(ids, snap.ID)
		}
	}
	if err := s.Backups.DeleteMany(ctx, a.Org.Slug, ids); err != nil {
		return snapshotError(err)
	}
	if err := s.Store.DeleteWorld(ctx, a.Org.ID, world.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	s.audit(ctx, u, a.Org, "world.delete", world.Name, "")
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// downloadWorld sends a world's archive, as it was uploaded or as it was
// saved.
func (s *Server) downloadWorld(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, err := s.org(r, u, store.RoleOperator)
	if err != nil {
		return err
	}
	world, snap, err := s.readyWorld(r.Context(), a, r.PathValue("world"))
	if err != nil {
		return err
	}
	file := world.File
	if file == "" {
		file = world.Name + ".tar"
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
