package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
	"github.com/GT-Drehmal/avSYS/internal/backup"
	"github.com/GT-Drehmal/avSYS/internal/store"
)

// A template can have a world of its own: an archive of one, as it was
// uploaded, kept as a snapshot of its own beside the template's versions,
// or a copy of a world of the library, kept as the library keeps it: as its
// own files, or as an archive, for one copied when the library kept them
// so. Each server made from the template is given it in place of the world
// its version has, by the same import that puts any uploaded world in place.

// ownWorldFile is what the archive is called in its snapshot.
const ownWorldFile = "world.archive"

// worldHost is what a template's own world is kept under.
func worldHost(id string) string { return templateHost(id) + "-world" }

// ownWorld is what is known of a template's own world.
type ownWorld struct {
	// Root is the world's folder in the archive, "" for its top, or under
	// /data in its snapshot, for one kept as its own files.
	Root string `json:"root"`
	// Name and Version are what the world says it is called and was last
	// opened in, as read where it was chosen.
	Name    string `json:"name,omitempty"`
	Version string `json:"version,omitempty"`
	// File is the archive's name as it was sent.
	File string `json:"file,omitempty"`
	Size int64  `json:"size"`
	// Pack is the resource pack uploaded to the world of the library it was
	// given, kept with it, for each server made from the template. One
	// from an address is in the template's settings instead.
	Pack *avsysv1.ResourcePackSpec `json:"resourcePack,omitempty"`
	// Library is the world of the library it was given, by its ID and name
	// then, or another template's that was.
	Library *avsysv1.WorldRef `json:"library,omitempty"`
}

// worldsOf are a template's own world's snapshots, newest first: one, but
// for a moment while another takes its place.
func worldsOf(list []backup.Snapshot, id string) []backup.Snapshot {
	var out []backup.Snapshot
	for _, snap := range list {
		if snap.Trigger == backup.TriggerTemplate && snap.Server == worldHost(id) {
			out = append(out, snap)
		}
	}
	return out
}

// ownWorldOf is a template's own world, and the snapshot it is in.
func (s *Server) ownWorldOf(ctx context.Context, a *access, id string) (*backup.Snapshot, ownWorld, *store.Template, error) {
	var info ownWorld
	t, err := s.Store.Template(ctx, a.Org.ID, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, info, nil, &Error{Status: http.StatusNotFound, Code: "not_found", Message: "There is no such template."}
	} else if err != nil {
		return nil, info, nil, err
	}
	list, err := s.templateSnapshots(ctx, a.Org)
	if err != nil {
		return nil, info, nil, err
	}
	worlds := worldsOf(list, t.ID)
	if t.World == "" || len(worlds) == 0 || json.Unmarshal([]byte(t.World), &info) != nil {
		return nil, info, nil, &Error{Status: http.StatusNotFound, Code: "no_world", Message: "That template has no world of its own."}
	}
	return &worlds[0], info, t, nil
}

// putTemplateWorld gives a template a world of its own: the archive sent,
// or sent in parts as ?upload= of ?size=, with ?root= its world's folder in it, with ?from= another template's, or
// with ?library= one of the organization's library. With ?pack=true, the
// resource pack uploaded to the world given that way comes with it.
// The one it had before goes.
func (s *Server) putTemplateWorld(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, err := s.org(r, u, store.RoleAdmin)
	if err != nil {
		return err
	}
	defer s.dropStaged(a, r.URL.Query().Get("upload"))
	t, _, err := s.template(r.Context(), a, r.PathValue("template"))
	if err != nil {
		return err
	}
	if !s.Backups.Available() {
		return errNoBackups
	}
	// One world at a time: each would take the other's away.
	unclaim, err := claimTemplate(a, worldHost(t.ID))
	if err != nil {
		return err
	}
	defer unclaim()
	ctx := context.WithoutCancel(r.Context())
	q := r.URL.Query()
	var info ownWorld
	var kept, keptPack *backup.Snapshot
	// The pack uploaded to the world it is given, kept under host, if it
	// is to come too.
	var pack *avsysv1.ResourcePackSpec
	var packFrom string
	if from := q.Get("from"); from != "" {
		snap, theirs, _, err := s.ownWorldOf(r.Context(), a, from)
		if err != nil {
			return err
		}
		if kept, err = s.Backups.CopyAs(ctx, a.Org.Slug, snap.ID, worldHost(t.ID), u.Username); err != nil {
			return snapshotError(err)
		}
		info = theirs
		info.Pack = nil
		pack, packFrom = theirs.Pack, worldHost(from)
	} else if lib := q.Get("library"); lib != "" {
		// One of the library's, copied: changing or deleting it there
		// leaves the template's as it is.
		world, snap, err := s.readyWorld(r.Context(), a, lib)
		if err != nil {
			return err
		}
		if kept, err = s.Backups.CopyAs(ctx, a.Org.Slug, snap.ID, worldHost(t.ID), u.Username); err != nil {
			return snapshotError(err)
		}
		info = ownWorld{Root: keptRoot(world, snap), Name: world.Name, Version: world.Version, File: world.File, Library: &avsysv1.WorldRef{ID: world.ID, Name: world.Name}}
		pack, packFrom = worldPackOf(world), libraryHost(world.ID)
	} else {
		root, err := archiveRoot(q.Get("root"))
		if err != nil {
			return err
		}
		info = ownWorld{Root: root, Name: q.Get("name"), Version: q.Get("version"), File: q.Get("file")}
		body, done, err := s.uploadBody(w, r, a)
		if err != nil {
			return err
		}
		defer done()
		if kept, err = s.Backups.KeepFile(ctx, a.Org.Slug, worldHost(t.ID), ownWorldFile, u.Username, body); err != nil {
			// Not taken for its world: what was kept of it goes.
			if kept != nil {
				if derr := s.Backups.Delete(ctx, a.Org.Slug, kept.ID); derr != nil {
					s.Log.Error("could not remove a template's world sent in part", "template", t.ID, "snapshot", kept.ID, "error", derr)
				}
			}
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				return errBadRequest("Over 2 GB in one request: send it in parts.")
			}
			return snapshotError(err)
		}
	}
	info.Size = kept.Size
	// Only an uploaded one: one from an address is in its settings.
	if q.Get("pack") == "true" && pack != nil && pack.Uploaded != nil {
		src, err := s.keptPack(ctx, a.Org, packFrom)
		if err == nil {
			keptPack, err = s.Backups.CopyAs(ctx, a.Org.Slug, src.ID, packHost(worldHost(t.ID)), u.Username)
		}
		if err != nil {
			if derr := s.Backups.Delete(ctx, a.Org.Slug, kept.ID); derr != nil {
				s.Log.Error("could not remove a template's world kept for nothing", "template", t.ID, "snapshot", kept.ID, "error", derr)
			}
			return snapshotError(err)
		}
		info.Pack = pack.DeepCopy()
	}
	raw, err := json.Marshal(info)
	if err != nil {
		return err
	}
	err = s.noteTemplateSnapshot(ctx, a.Org, kept)
	if err == nil && keptPack != nil {
		err = s.noteTemplateSnapshot(ctx, a.Org, keptPack)
	}
	if err == nil {
		err = s.Store.SetTemplateWorld(ctx, a.Org.ID, t.ID, string(raw))
	}
	if err != nil {
		// Kept for a template that is not there: not left behind.
		ids := []string{kept.ID}
		if keptPack != nil {
			ids = append(ids, keptPack.ID)
		}
		if derr := s.Backups.DeleteMany(ctx, a.Org.Slug, ids); derr != nil {
			s.Log.Error("could not remove a template's world kept for nothing", "template", t.ID, "snapshot", kept.ID, "error", derr)
		} else {
			s.forgetTemplateSnapshots(ctx, a.Org, ids...)
		}
		if errors.Is(err, store.ErrNotFound) {
			return &Error{Status: http.StatusNotFound, Code: "not_found", Message: "There is no such template."}
		}
		return err
	}
	keepPack := ""
	if keptPack != nil {
		keepPack = keptPack.ID
	}
	s.dropOtherWorlds(ctx, a, t.ID, kept.ID, keepPack)
	s.audit(ctx, u, a.Org, "template.world", t.Name, kept.ID)
	_, v, err := s.template(ctx, a, t.ID)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, templateFor(a.Role, v))
	return nil
}

// dropOtherWorlds removes a template's own worlds but the one it has now,
// and the packs kept with them but its own (keepPack), as the repository
// has them: any it has that were not noted go too.
func (s *Server) dropOtherWorlds(ctx context.Context, a *access, id, keep, keepPack string) {
	list, err := s.repoSnapshots(ctx, a.Org.Slug)
	if err != nil {
		s.Log.Warn("could not list a template's worlds to remove the one before", "template", id, "error", err)
		return
	}
	var old []string
	for _, snap := range worldsOf(list, id) {
		if snap.ID != keep {
			old = append(old, snap.ID)
		}
	}
	for _, snap := range keptPackSnapshots(list, worldHost(id)) {
		if snap.ID != keepPack {
			old = append(old, snap.ID)
		}
	}
	if err := s.Backups.DeleteMany(ctx, a.Org.Slug, old); err != nil {
		s.Log.Warn("could not remove a template's world before", "template", id, "error", err)
		return
	}
	s.forgetTemplateSnapshots(ctx, a.Org, old...)
}

// deleteTemplateWorld takes a template's own world away: servers made from it
// have its version's world, or a new one, as its changes say.
func (s *Server) deleteTemplateWorld(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, err := s.org(r, u, store.RoleAdmin)
	if err != nil {
		return err
	}
	t, _, err := s.template(r.Context(), a, r.PathValue("template"))
	if err != nil {
		return err
	}
	unclaim, err := claimTemplate(a, worldHost(t.ID))
	if err != nil {
		return err
	}
	defer unclaim()
	ctx := context.WithoutCancel(r.Context())
	if err := s.Store.SetTemplateWorld(ctx, a.Org.ID, t.ID, ""); err != nil {
		return err
	}
	s.dropOtherWorlds(ctx, a, t.ID, "", "")
	s.audit(ctx, u, a.Org, "template.world-delete", t.Name, "")
	_, v, err := s.template(ctx, a, t.ID)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, templateFor(a.Role, v))
	return nil
}

// patchTemplateWorld changes the resource pack kept with a template's own
// world: resourcePack null takes it away, and one with enforce or prompt
// says whether players must take it and what they are asked.
func (s *Server) patchTemplateWorld(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, err := s.org(r, u, store.RoleAdmin)
	if err != nil {
		return err
	}
	t, _, err := s.template(r.Context(), a, r.PathValue("template"))
	if err != nil {
		return err
	}
	var in struct {
		ResourcePack json.RawMessage `json:"resourcePack"`
	}
	if err := decode(r, &in); err != nil {
		return err
	}
	if in.ResourcePack == nil {
		return errBadRequest("Say what to change: resourcePack.")
	}
	unclaim, err := claimTemplate(a, worldHost(t.ID))
	if err != nil {
		return err
	}
	defer unclaim()
	_, info, _, err := s.ownWorldOf(r.Context(), a, t.ID)
	if err != nil {
		return err
	}
	var in2 packChange
	if string(in.ResourcePack) != "null" {
		if err := json.Unmarshal(in.ResourcePack, &in2); err != nil || in2.URL != "" {
			return errBadRequest("A template's world keeps only a pack uploaded to the world it was given. One from an address goes in its settings.")
		}
	}
	if info.Pack, err = changedPack(info.Pack, in.ResourcePack); err != nil {
		return err
	}
	raw, err := json.Marshal(info)
	if err != nil {
		return err
	}
	ctx := context.WithoutCancel(r.Context())
	if err := s.Store.SetTemplateWorld(ctx, a.Org.ID, t.ID, string(raw)); err != nil {
		return err
	}
	if info.Pack == nil {
		s.dropKeptPacks(ctx, a, worldHost(t.ID), "")
	}
	s.audit(ctx, u, a.Org, "template.world-pack", t.Name, packAudit(info.Pack))
	_, v, err := s.template(ctx, a, t.ID)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, templateFor(a.Role, v))
	return nil
}
