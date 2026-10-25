package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
	"github.com/GT-Drehmal/avSYS/internal/backup"
	"github.com/GT-Drehmal/avSYS/internal/store"
)

// A template is a server kept to make others from, files and settings and
// all. Each version of it is a snapshot in the organization's repository,
// taken of a server the way any snapshot is, under a name of the template's
// own rather than the server's: it is not part of that server's history,
// does not keep its name taken, and is never expired, as only scheduled
// snapshots are. What it is called, and which version servers are made
// from, is kept by the panel. Making a server from one is making it from
// that version's snapshot.

// kindTemplate is a server being saved as a version of a template.
const kindTemplate = "template"

// errTemplateVersion is for a template's snapshot asked to be deleted as if
// it were a server's.
var errTemplateVersion = &Error{Status: http.StatusConflict, Code: "template", Message: "That snapshot is a version of a template. Delete it on the Templates page."}

// errTemplateBusy is for a template a version is being saved to or deleted
// from, which is let finish first.
var errTemplateBusy = &Error{Status: http.StatusConflict, Code: "busy", Message: "This template is being saved or deleted. Try again when that has finished."}

// templatesBusy holds, by organization ID and template ID, the templates a
// version is being saved to or deleted from, one at a time: a version saved
// is never left without its template, nor deleted as it is saved.
var templatesBusy sync.Map

// claimTemplate holds a template for one save or deletion.
func claimTemplate(a *access, id string) (release func(), err error) {
	key := a.Org.ID + "/" + id
	if _, busy := templatesBusy.LoadOrStore(key, true); busy {
		return nil, errTemplateBusy
	}
	return func() { templatesBusy.Delete(key) }, nil
}

// templateHost is what a template's snapshots are taken under.
func templateHost(id string) string { return "template-" + id }

type templateView struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	CreatedBy   string    `json:"createdBy"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
	// Current is the version servers are made from: nil while it has none,
	// as while its first is being saved.
	Current *backup.Snapshot `json:"current"`
	// Versions are newest first.
	Versions []backup.Snapshot `json:"versions"`
	// Spec is what a server made from it is, for one template asked for.
	Spec *avsysv1.MinecraftServerSpec `json:"spec,omitempty"`
}

// templateOf is a template as it is shown, with its versions found among
// the organization's snapshots.
func templateOf(t *store.Template, list []backup.Snapshot) templateView {
	v := templateView{ID: t.ID, Name: t.Name, Description: t.Description, CreatedBy: t.CreatedBy, CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt, Versions: []backup.Snapshot{}}
	for _, snap := range list {
		if snap.Trigger == backup.TriggerTemplate && snap.Server == templateHost(t.ID) {
			v.Versions = append(v.Versions, snap)
		}
	}
	for i := range v.Versions {
		if v.Versions[i].Lineage == t.Current {
			v.Current = &v.Versions[i]
		}
	}
	if v.Current == nil && len(v.Versions) > 0 {
		v.Current = &v.Versions[0]
	}
	return v
}

// templateSnapshots are the organization's snapshots, or none where there
// are none to have.
func (s *Server) templateSnapshots(ctx context.Context, org string) ([]backup.Snapshot, error) {
	if !s.Backups.Available() {
		return nil, nil
	}
	list, err := s.Backups.List(ctx, org)
	return list, snapshotErrorOrNil(err)
}

func snapshotErrorOrNil(err error) error {
	if err == nil {
		return nil
	}
	return snapshotError(err)
}

func (s *Server) listTemplates(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, err := s.org(r, u, store.RoleViewer)
	if err != nil {
		return err
	}
	all, err := s.Store.Templates(r.Context(), a.Org.ID)
	if err != nil {
		return err
	}
	list, err := s.templateSnapshots(r.Context(), a.Org.Slug)
	if err != nil {
		return err
	}
	out := make([]templateView, len(all))
	for i := range all {
		out[i] = templateOf(&all[i], list)
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// template is the template the request names, as it is shown.
func (s *Server) template(ctx context.Context, a *access, id string) (*store.Template, templateView, error) {
	t, err := s.Store.Template(ctx, a.Org.ID, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, templateView{}, &Error{Status: http.StatusNotFound, Code: "not_found", Message: "There is no such template."}
	} else if err != nil {
		return nil, templateView{}, err
	}
	list, err := s.templateSnapshots(ctx, a.Org.Slug)
	if err != nil {
		return nil, templateView{}, err
	}
	return t, templateOf(t, list), nil
}

func (s *Server) getTemplate(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, err := s.org(r, u, store.RoleViewer)
	if err != nil {
		return err
	}
	_, v, err := s.template(r.Context(), a, r.PathValue("template"))
	if err != nil {
		return err
	}
	if v.Current != nil {
		record, err := s.recordOf(r.Context(), a.Org.Slug, v.Current.ID)
		if err != nil {
			return err
		}
		spec := recordFor(a.Role, record.Spec)
		v.Spec = &spec
	}
	writeJSON(w, http.StatusOK, v)
	return nil
}

// templateWords checks a template's name and description, and gives them
// back tidied.
func templateWords(name, description string) (string, string, error) {
	name, description = strings.TrimSpace(name), strings.TrimSpace(description)
	if name == "" || len([]rune(name)) > 60 {
		return "", "", errBadRequest("Give the template a name of up to 60 characters.")
	}
	if len([]rune(description)) > 500 {
		return "", "", errBadRequest("A description can be at most 500 characters.")
	}
	for _, c := range name + description {
		if unicode.IsControl(c) && c != '\n' {
			return "", "", errBadRequest("A name or description cannot have control characters.")
		}
	}
	if strings.Contains(name, "\n") {
		return "", "", errBadRequest("A name is one line.")
	}
	return name, description, nil
}

func templateNameTaken(name string) error {
	return &Error{Status: http.StatusConflict, Code: "conflict", Message: fmt.Sprintf("There is already a template named %s.", name)}
}

// createTemplate saves a server as a new template: its first version is a
// snapshot of the server as it is now. It answers as the snapshot starts,
// with the template, which has a version once it is done; with ?wait=true
// once it is done. A template whose first version could not be saved is
// not kept.
func (s *Server) createTemplate(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, err := s.org(r, u, store.RoleAdmin)
	if err != nil {
		return err
	}
	var in struct{ Name, Description, Server string }
	if err := decode(r, &in); err != nil {
		return err
	}
	name, description, err := templateWords(in.Name, in.Description)
	if err != nil {
		return err
	}
	mc, err := s.templateSource(r.Context(), a, in.Server)
	if err != nil {
		return err
	}
	t := &store.Template{OrgID: a.Org.ID, Name: name, Description: description, CreatedBy: u.Username}
	if err := s.Store.CreateTemplate(r.Context(), t); errors.Is(err, store.ErrTemplateNameTaken) {
		return templateNameTaken(name)
	} else if err != nil {
		return err
	}
	return s.saveTemplateVersion(w, r, u, a, t, mc, true)
}

// templateSource is the server a version of a template is to be saved from.
func (s *Server) templateSource(ctx context.Context, a *access, name string) (*avsysv1.MinecraftServer, error) {
	if !s.Backups.Available() {
		return nil, errNoBackups
	}
	if name == "" {
		return nil, errBadRequest("Say which server to save, as \"server\".")
	}
	mc := &avsysv1.MinecraftServer{}
	if err := s.Client.Get(ctx, client.ObjectKey{Namespace: a.Namespace, Name: name}, mc); apierrors.IsNotFound(err) {
		return nil, &Error{Status: http.StatusNotFound, Code: "not_found", Message: fmt.Sprintf("There is no server named %s.", name)}
	} else if err != nil {
		return nil, err
	}
	// What an interrupted restore or import left is not a server to make
	// others from.
	if err := unfinished(mc); err != nil {
		return nil, err
	}
	if err := settled(mc); err != nil {
		return nil, err
	}
	return mc, nil
}

// saveTemplateVersion takes a snapshot of mc as the newest version of t,
// which servers are then made from. first says t was made for it, and is
// not kept if it fails.
func (s *Server) saveTemplateVersion(w http.ResponseWriter, r *http.Request, u *store.User, a *access, t *store.Template, mc *avsysv1.MinecraftServer, first bool) error {
	forget := func(ctx context.Context) {
		if first {
			if err := s.Store.DeleteTemplate(ctx, a.Org.ID, t.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
				s.Log.Warn("could not forget a template whose first version failed", "template", t.ID, "error", err)
			}
		}
	}
	unclaim, err := claimTemplate(a, t.ID)
	if err != nil {
		forget(r.Context())
		return err
	}
	op, ok := s.ops.begin(a.Namespace, a.Org.Slug, mc.Name, kindTemplate, "")
	if !ok {
		unclaim()
		forget(r.Context())
		if running := s.ops.current(a.Namespace, mc.Name); running != nil {
			return heldBy(running.Kind)
		}
		return errBusy
	}
	started := *op
	work := func(ctx context.Context) error {
		defer unclaim()
		ctx, cancel := context.WithTimeout(ctx, operationTimeout)
		defer cancel()
		snap, err := s.takeSnapshotAs(ctx, a.Org.Slug, mc, op, backup.TriggerTemplate, u.Username, templateHost(t.ID))
		if err == nil {
			// The newest is the one servers are made from. Only that is
			// changed: a name given meanwhile stays.
			t.Current = ""
			if err = s.Store.SetTemplateCurrent(ctx, a.Org.ID, t.ID, ""); err != nil {
				// Saved for a template that is not there: not left behind.
				if derr := s.Backups.Delete(context.WithoutCancel(ctx), a.Org.Slug, snap.ID); derr != nil {
					s.Log.Error("could not remove a template's version saved for nothing", "template", t.ID, "snapshot", snap.ID, "error", derr)
				}
			}
		}
		if err != nil {
			forget(context.WithoutCancel(ctx))
		} else if first {
			s.audit(ctx, u, a.Org, "template.create", t.Name, snap.ID)
		} else {
			s.audit(ctx, u, a.Org, "template.update", t.Name, snap.ID)
		}
		// Not a snapshot of the server's own: what it says of its
		// snapshots stays as it was.
		s.finishUnsaved(ctx, a.Namespace, mc.Name, kindTemplate, err)
		return err
	}
	if r.URL.Query().Get("wait") == "true" {
		if err := work(context.WithoutCancel(r.Context())); err != nil {
			return operationFailed(err)
		}
		_, v, err := s.template(r.Context(), a, t.ID)
		if err != nil {
			return err
		}
		writeJSON(w, http.StatusCreated, v)
		return nil
	}
	go func() { _ = work(context.Background()) }()
	writeJSON(w, http.StatusAccepted, struct {
		templateView
		Operation operation `json:"operation"`
	}{templateOf(t, nil), started})
	return nil
}

// addTemplateVersion saves a server as the newest version of a template.
func (s *Server) addTemplateVersion(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, err := s.org(r, u, store.RoleAdmin)
	if err != nil {
		return err
	}
	t, _, err := s.template(r.Context(), a, r.PathValue("template"))
	if err != nil {
		return err
	}
	var in struct{ Server string }
	if err := decode(r, &in); err != nil {
		return err
	}
	mc, err := s.templateSource(r.Context(), a, in.Server)
	if err != nil {
		return err
	}
	return s.saveTemplateVersion(w, r, u, a, t, mc, false)
}

// updateTemplate renames a template, describes it again, or says which of
// its versions servers are made from, by that version's snapshot.
func (s *Server) updateTemplate(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, err := s.org(r, u, store.RoleAdmin)
	if err != nil {
		return err
	}
	t, v, err := s.template(r.Context(), a, r.PathValue("template"))
	if err != nil {
		return err
	}
	var in struct {
		Name, Description *string
		Current           *string
	}
	if err := decode(r, &in); err != nil {
		return err
	}
	name, description := t.Name, t.Description
	if in.Name != nil {
		name = *in.Name
	}
	if in.Description != nil {
		description = *in.Description
	}
	if t.Name, t.Description, err = templateWords(name, description); err != nil {
		return err
	}
	if in.Current != nil {
		snap := snapshotByID(v.Versions, *in.Current)
		if snap == nil {
			return errBadRequest("That is not a version of this template.")
		}
		t.Current = snap.Lineage
		// The newest is the one used anyway, and stays so as others come.
		if snap.ID == v.Versions[0].ID {
			t.Current = ""
		}
	}
	if err := s.Store.UpdateTemplate(r.Context(), t); errors.Is(err, store.ErrTemplateNameTaken) {
		return templateNameTaken(t.Name)
	} else if err != nil {
		return err
	}
	s.audit(r.Context(), u, a.Org, "template.edit", t.Name, "")
	_, v, err = s.template(r.Context(), a, t.ID)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, v)
	return nil
}

// deleteTemplate deletes a template and every version of it. A version a
// server is being made from keeps it, and all of it, until that is done.
func (s *Server) deleteTemplate(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, err := s.org(r, u, store.RoleAdmin)
	if err != nil {
		return err
	}
	t, v, err := s.template(r.Context(), a, r.PathValue("template"))
	if err != nil {
		return err
	}
	unclaim, err := claimTemplate(a, t.ID)
	if err != nil {
		return err
	}
	defer unclaim()
	if len(v.Versions) > 0 {
		release, err := s.claimToDelete(r.Context(), a, v.Versions)
		if err != nil {
			return err
		}
		defer release()
		ids := make([]string, len(v.Versions))
		for i, snap := range v.Versions {
			ids[i] = snap.ID
		}
		if err := s.Backups.DeleteMany(context.WithoutCancel(r.Context()), a.Org.Slug, ids); err != nil {
			return snapshotError(err)
		}
	}
	if err := s.Store.DeleteTemplate(r.Context(), a.Org.ID, t.ID); err != nil {
		return err
	}
	s.audit(r.Context(), u, a.Org, "template.delete", t.Name, "")
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// deleteTemplateVersion deletes one version of a template. Its only one is
// deleted with the template.
func (s *Server) deleteTemplateVersion(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, err := s.org(r, u, store.RoleAdmin)
	if err != nil {
		return err
	}
	t, v, err := s.template(r.Context(), a, r.PathValue("template"))
	if err != nil {
		return err
	}
	snap := snapshotByID(v.Versions, r.PathValue("id"))
	if snap == nil {
		return &Error{Status: http.StatusNotFound, Code: "not_found", Message: "That is not a version of this template."}
	}
	if len(v.Versions) == 1 {
		return &Error{Status: http.StatusConflict, Code: "last_version", Message: "This is the template's only version. Delete the template instead."}
	}
	unclaim, err := claimTemplate(a, t.ID)
	if err != nil {
		return err
	}
	defer unclaim()
	release, err := s.claimToDelete(r.Context(), a, []backup.Snapshot{*snap})
	if err != nil {
		return err
	}
	defer release()
	if err := s.Backups.Delete(context.WithoutCancel(r.Context()), a.Org.Slug, snap.ID); err != nil {
		return snapshotError(err)
	}
	// The one used is gone: the newest is used.
	if t.Current == snap.Lineage {
		if err := s.Store.SetTemplateCurrent(r.Context(), a.Org.ID, t.ID, ""); err != nil {
			return err
		}
	}
	s.audit(r.Context(), u, a.Org, "template.delete-version", t.Name, snap.ID)
	_, v, err = s.template(r.Context(), a, t.ID)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, v)
	return nil
}

// claimToDelete holds snapshots off from being restored or made into a
// server while they are deleted. None is held if any is in use.
func (s *Server) claimToDelete(ctx context.Context, a *access, snaps []backup.Snapshot) (release func(), err error) {
	var held []func()
	release = func() {
		for _, h := range held {
			h()
		}
	}
	for _, snap := range snaps {
		r, ok := s.ops.remove(a.Namespace, snap.ID)
		if !ok {
			release()
			return nil, &Error{Status: http.StatusConflict, Code: "in_use", Message: "A server is being made from this template. Wait for that to finish."}
		}
		held = append(held, r)
		if by, err := s.heldIn(ctx, a.Namespace, snap.ID); err != nil {
			release()
			return nil, err
		} else if by != "" {
			release()
			return nil, &Error{Status: http.StatusConflict, Code: "in_use", Message: fmt.Sprintf("%s is still being made from this template. Wait for that to finish.", by)}
		}
	}
	return release, nil
}

// templateVersionToMake is the snapshot a server made from a template is
// made from: the version it says it uses.
func (s *Server) templateVersionToMake(ctx context.Context, a *access, id string) (string, error) {
	_, v, err := s.template(ctx, a, id)
	if err != nil {
		return "", err
	}
	if v.Current == nil {
		return "", &Error{Status: http.StatusConflict, Code: "no_version", Message: "This template has no version yet. Wait for its first to be saved."}
	}
	return v.Current.ID, nil
}

// isTemplate is whether a snapshot is a version of a template, which is not
// part of any server's history.
func isTemplate(snap *backup.Snapshot) bool { return snap.Trigger == backup.TriggerTemplate }
