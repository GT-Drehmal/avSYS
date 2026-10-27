package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/GT-Drehmal/avSYS/internal/backup"
	"github.com/GT-Drehmal/avSYS/internal/store"
)

// A template can have a world of its own: an archive of one, as it was
// uploaded, kept as a snapshot of its own beside the template's versions.
// Each server made from the template is given it in place of the world its
// version has, by the same import that puts any uploaded world in place.

// ownWorldFile is what the archive is called in its snapshot.
const ownWorldFile = "world.archive"

// worldHost is what a template's own world is kept under.
func worldHost(id string) string { return templateHost(id) + "-world" }

// ownWorld is what is known of a template's own world.
type ownWorld struct {
	// Root is the world's folder in the archive, "" for its top.
	Root string `json:"root"`
	// Name and Version are what the world says it is called and was last
	// opened in, as read where it was chosen.
	Name    string `json:"name,omitempty"`
	Version string `json:"version,omitempty"`
	// File is the archive's name as it was sent.
	File string `json:"file,omitempty"`
	Size int64  `json:"size"`
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
func (s *Server) ownWorldOf(ctx context.Context, a *access, id string) (*backup.Snapshot, ownWorld, error) {
	var info ownWorld
	t, err := s.Store.Template(ctx, a.Org.ID, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, info, &Error{Status: http.StatusNotFound, Code: "not_found", Message: "There is no such template."}
	} else if err != nil {
		return nil, info, err
	}
	list, err := s.templateSnapshots(ctx, a.Org.Slug)
	if err != nil {
		return nil, info, err
	}
	worlds := worldsOf(list, t.ID)
	if t.World == "" || len(worlds) == 0 || json.Unmarshal([]byte(t.World), &info) != nil {
		return nil, info, &Error{Status: http.StatusNotFound, Code: "no_world", Message: "That template has no world of its own."}
	}
	return &worlds[0], info, nil
}

// putTemplateWorld gives a template a world of its own: the archive sent,
// with ?root= its world's folder in it, or with ?from= another template's.
// The one it had before goes.
func (s *Server) putTemplateWorld(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, err := s.org(r, u, store.RoleAdmin)
	if err != nil {
		return err
	}
	t, _, err := s.template(r.Context(), a, r.PathValue("template"))
	if err != nil {
		return err
	}
	if !s.Backups.Available() {
		return errNoBackups
	}
	ctx := context.WithoutCancel(r.Context())
	q := r.URL.Query()
	var info ownWorld
	var kept *backup.Snapshot
	if from := q.Get("from"); from != "" {
		snap, theirs, err := s.ownWorldOf(r.Context(), a, from)
		if err != nil {
			return err
		}
		if kept, err = s.Backups.CopyAs(ctx, a.Org.Slug, snap.ID, worldHost(t.ID), u.Username); err != nil {
			return snapshotError(err)
		}
		info = theirs
	} else {
		root, err := archiveRoot(q.Get("root"))
		if err != nil {
			return err
		}
		info = ownWorld{Root: root, Name: q.Get("name"), Version: q.Get("version"), File: q.Get("file")}
		if kept, err = s.Backups.KeepFile(ctx, a.Org.Slug, worldHost(t.ID), ownWorldFile, u.Username, http.MaxBytesReader(w, r.Body, maxUpload)); err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				return errBadRequest("Over 2 GB.")
			}
			return snapshotError(err)
		}
	}
	info.Size = kept.Size
	raw, err := json.Marshal(info)
	if err != nil {
		return err
	}
	if err := s.Store.SetTemplateWorld(ctx, a.Org.ID, t.ID, string(raw)); err != nil {
		// Kept for a template that is not there: not left behind.
		if derr := s.Backups.Delete(ctx, a.Org.Slug, kept.ID); derr != nil {
			s.Log.Error("could not remove a template's world kept for nothing", "template", t.ID, "snapshot", kept.ID, "error", derr)
		}
		if errors.Is(err, store.ErrNotFound) {
			return &Error{Status: http.StatusNotFound, Code: "not_found", Message: "There is no such template."}
		}
		return err
	}
	s.dropOtherWorlds(ctx, a, t.ID, kept.ID)
	s.audit(ctx, u, a.Org, "template.world", t.Name, kept.ID)
	_, v, err := s.template(ctx, a, t.ID)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, templateFor(a.Role, v))
	return nil
}

// dropOtherWorlds removes a template's own worlds but the one it has now.
func (s *Server) dropOtherWorlds(ctx context.Context, a *access, id, keep string) {
	list, err := s.templateSnapshots(ctx, a.Org.Slug)
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
	if err := s.Backups.DeleteMany(ctx, a.Org.Slug, old); err != nil {
		s.Log.Warn("could not remove a template's world before", "template", id, "error", err)
	}
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
	ctx := context.WithoutCancel(r.Context())
	if err := s.Store.SetTemplateWorld(ctx, a.Org.ID, t.ID, ""); err != nil {
		return err
	}
	s.dropOtherWorlds(ctx, a, t.ID, "")
	s.audit(ctx, u, a.Org, "template.world-delete", t.Name, "")
	_, v, err := s.template(ctx, a, t.ID)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, templateFor(a.Role, v))
	return nil
}
