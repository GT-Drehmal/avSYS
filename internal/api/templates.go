package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
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
//
// A template can also be made of settings alone, with no server to save:
// those are kept by the panel, and a server made from one is a new server
// with them, its world made when it first starts. Saving a server into one
// gives it versions, which it is made from from then on.

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
	// SettingsOnly is for a template of settings alone, with no versions.
	SettingsOnly bool `json:"settingsOnly"`
	// Spec is what a server made from it is: for one template asked for,
	// and for every template of settings alone.
	Spec *avsysv1.MinecraftServerSpec `json:"spec,omitempty"`
	// World is "new" for one whose servers start a world of their own
	// rather than take its version's, and "own" for one whose servers are
	// given the world it has of its own.
	World string `json:"world,omitempty"`
	// OwnWorld is what is known of that world.
	OwnWorld *ownWorld `json:"ownWorld,omitempty"`
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
	if newWorldIn(t.Changes) {
		v.World = "new"
	}
	var own ownWorld
	if t.World != "" && len(worldsOf(list, t.ID)) > 0 && json.Unmarshal([]byte(t.World), &own) == nil {
		v.World, v.OwnWorld = "own", &own
	}
	// Versions win over settings: a server saved into it is what it is now.
	if t.Settings != "" && len(v.Versions) == 0 {
		var spec avsysv1.MinecraftServerSpec
		if err := json.Unmarshal([]byte(t.Settings), &spec); err == nil {
			v.SettingsOnly, v.Spec = true, &spec
		}
	}
	return v
}

// templateFor is a template as someone of the given role is shown it.
func templateFor(role store.Role, v templateView) templateView {
	if v.Spec != nil {
		spec := recordFor(role, *v.Spec)
		v.Spec = &spec
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
		out[i] = templateFor(a.Role, templateOf(&all[i], list))
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
	t, v, err := s.template(r.Context(), a, r.PathValue("template"))
	if err != nil {
		return err
	}
	if v.Current != nil {
		record, err := s.recordOf(r.Context(), a.Org.Slug, v.Current.ID)
		if err != nil {
			return err
		}
		v.Spec = &record.Spec
		// As a server made from it is: its version, with what was changed.
		if spec, ok := withChanges(&record.Spec, t.Changes); ok {
			v.Spec = &spec
		}
	}
	writeJSON(w, http.StatusOK, templateFor(a.Role, v))
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
// not kept. Given settings ("spec") instead of a server, it keeps a
// template of those alone.
func (s *Server) createTemplate(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, err := s.org(r, u, store.RoleAdmin)
	if err != nil {
		return err
	}
	var in struct {
		Name, Description, Server string
		// Snapshot makes it from a snapshot, a template's version among them.
		Snapshot string
		// Template makes it from another template's version, with what that
		// one has changed.
		Template string
		Spec     json.RawMessage
		// Changes are what it has other than the server, snapshot or
		// template it is made from, as a copy's request asks for them.
		Changes json.RawMessage
	}
	if err := decode(r, &in); err != nil {
		return err
	}
	name, description, err := templateWords(in.Name, in.Description)
	if err != nil {
		return err
	}
	sources := 0
	for _, given := range []bool{len(in.Spec) > 0, in.Server != "", in.Snapshot != "", in.Template != ""} {
		if given {
			sources++
		}
	}
	if sources > 1 {
		return errBadRequest("A template is made from one server, snapshot, template or set of settings.")
	}
	if len(in.Changes) > 0 && in.Server == "" && in.Snapshot == "" && in.Template == "" {
		return errBadRequest("Changes are made to a server, snapshot or template it is made from.")
	}
	if in.Snapshot != "" || in.Template != "" {
		if !s.Backups.Available() {
			return errNoBackups
		}
		id, older := in.Snapshot, json.RawMessage(nil)
		if in.Template != "" {
			if id, older, err = s.templateVersionToMake(r.Context(), a, in.Template); err != nil {
				return err
			}
		}
		// Checked against what the version has, when there is anything to check.
		changes := ""
		if len(older) > 0 || len(in.Changes) > 0 {
			record, err := s.recordOf(r.Context(), a.Org.Slug, id)
			if err != nil {
				if errors.Is(err, backup.ErrNotFound) {
					return errNoSuchSnapshot
				}
				return errNoRecord(err)
			}
			if changes, err = s.templateChanges(r.Context(), a, &record.Spec, older, in.Changes); err != nil {
				return err
			}
		}
		t := &store.Template{OrgID: a.Org.ID, Name: name, Description: description, CreatedBy: u.Username}
		if err := s.Store.CreateTemplate(r.Context(), t); errors.Is(err, store.ErrTemplateNameTaken) {
			return templateNameTaken(name)
		} else if err != nil {
			return err
		}
		return s.copyTemplateVersion(w, r, u, a, t, id, changes, true)
	}
	if len(in.Spec) > 0 {
		settings, err := s.templateSettings(r.Context(), a, in.Spec)
		if err != nil {
			return err
		}
		t := &store.Template{OrgID: a.Org.ID, Name: name, Description: description, CreatedBy: u.Username, Settings: settings}
		if err := s.Store.CreateTemplate(r.Context(), t); errors.Is(err, store.ErrTemplateNameTaken) {
			return templateNameTaken(name)
		} else if err != nil {
			return err
		}
		s.audit(r.Context(), u, a.Org, "template.create", t.Name, "")
		writeJSON(w, http.StatusCreated, templateOf(t, nil))
		return nil
	}
	mc, err := s.templateSource(r.Context(), a, in.Server)
	if err != nil {
		return err
	}
	changes, err := s.templateChanges(r.Context(), a, &mc.Spec, nil, in.Changes)
	if err != nil {
		return err
	}
	t := &store.Template{OrgID: a.Org.ID, Name: name, Description: description, CreatedBy: u.Username}
	if err := s.Store.CreateTemplate(r.Context(), t); errors.Is(err, store.ErrTemplateNameTaken) {
		return templateNameTaken(name)
	} else if err != nil {
		return err
	}
	return s.saveTemplateVersion(w, r, u, a, t, mc, changes, true)
}

// templateSettings checks the settings a template of settings alone is
// made of, and gives them back as they are kept. What is a server's own is
// left out: its name, address, node and state, the EULA, which is agreed to
// for each server made, and its Java, which is found for the version it
// gets. "Latest" stays: each server made gets the newest there is then.
// Files cannot be part of it, as there is nowhere to keep them.
func (s *Server) templateSettings(ctx context.Context, a *access, raw json.RawMessage) (string, error) {
	var spec avsysv1.MinecraftServerSpec
	if err := json.Unmarshal(raw, &spec); err != nil {
		return "", errBadRequest("Invalid spec: %v", err)
	}
	for _, m := range spec.Mods {
		if m.Source == avsysv1.SourceUpload {
			return "", errBadRequest("A template of settings alone cannot have uploaded files. Save a server that has them instead.")
		}
	}
	if rp := spec.ResourcePack; rp != nil && rp.Uploaded != nil {
		return "", errBadRequest("A template of settings alone cannot have an uploaded resource pack. Save a server that has it instead.")
	}
	if rp := spec.ResourcePack; rp != nil {
		rp.Stack = nil
	}
	spec.DisplayName, spec.State, spec.EULA, spec.GameJava = "", "", false, 0
	spec.Image, spec.Env, spec.NodeSelector, spec.Ports = "", nil, nil, nil
	spec.Expose = avsysv1.ExposeSpec{Type: spec.Expose.Type}
	if spec.Modpack == nil && spec.Loader == "" {
		return "", errBadRequest("Choose a server type, or a modpack.")
	}
	// Checked as a server's are, but for the EULA, agreed to later.
	spec.EULA = true
	err := s.validateSpec(ctx, a.Namespace, "", &spec)
	spec.EULA = false
	if err != nil {
		return "", err
	}
	if err := checkPackSHA1(spec.ResourcePack, nil); err != nil {
		return "", err
	}
	if err := checkPackFormats(spec.ResourcePack, nil); err != nil {
		return "", err
	}
	out, err := json.Marshal(spec)
	return string(out), err
}

// templateChanges checks what a template is to have other than its version,
// newer laid over older, as base is the version's settings, and gives it
// back as it is kept: without what is a server's own, as for a template of
// settings alone. "" is no changes.
func (s *Server) templateChanges(ctx context.Context, a *access, base *avsysv1.MinecraftServerSpec, older, newer json.RawMessage) (string, error) {
	raw, err := layered(older, newer)
	if err != nil {
		return "", errBadRequest("Invalid changes: %v", err)
	}
	var fields map[string]json.RawMessage
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &fields); err != nil {
			return "", errBadRequest("Invalid changes: %v", err)
		}
	}
	for _, own := range []string{"displayName", "state", "eula", "nodeSelector", "image", "env", "gameJava", "ports"} {
		delete(fields, own)
	}
	// Whether its servers start a world of their own: "new", or not at all.
	if w, ok := fields["world"]; ok && string(w) != `"new"` {
		delete(fields, "world")
	}
	if e, ok := fields["expose"]; ok {
		var expose struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(e, &expose)
		if expose.Type == "" {
			delete(fields, "expose")
		} else {
			fields["expose"], _ = json.Marshal(map[string]string{"type": expose.Type})
		}
	}
	if len(fields) == 0 {
		return "", nil
	}
	if raw, err = json.Marshal(fields); err != nil {
		return "", err
	}
	var requested avsysv1.MinecraftServerSpec
	if err := json.Unmarshal(raw, &requested); err != nil {
		return "", errBadRequest("Invalid changes: %v", err)
	}
	if err := checkCopiedContent(&requested, base, fields); err != nil {
		return "", err
	}
	// Checked as a copy with them would be, but for the address, which is
	// each server's own.
	spec := fromSnapshot(&requested, base, fields)
	spec.Expose = avsysv1.ExposeSpec{Type: spec.Expose.Type}
	if spec.Expose.Type == "" {
		spec.Expose.Type = base.Expose.Type
	}
	spec.EULA = true
	if err := s.validateSpec(ctx, a.Namespace, "", &spec); err != nil {
		return "", err
	}
	if err := worldLeavable(&spec, newWorldIn(string(raw))); err != nil {
		return "", err
	}
	if err := checkPackSHA1(spec.ResourcePack, base.ResourcePack); err != nil {
		return "", err
	}
	if err := checkPackFormats(spec.ResourcePack, base.ResourcePack); err != nil {
		return "", err
	}
	if spec.Version != base.Version || spec.Loader != base.Loader {
		if err := s.checkVersion(ctx, &spec); err != nil {
			return "", err
		}
	}
	return string(raw), nil
}

// newWorldIn says whether a template's changes give its servers a new world.
func newWorldIn(changes string) bool {
	var fields struct {
		World string `json:"world"`
	}
	return changes != "" && json.Unmarshal([]byte(changes), &fields) == nil && fields.World == "new"
}

// worldOnly is what of a template's changes outlasts a new version: whether
// its servers start a new world.
func worldOnly(changes string) string {
	if newWorldIn(changes) {
		return `{"world":"new"}`
	}
	return ""
}

// withChanges is a template's version's settings with its changes, as a
// server made from it has them before it is given its own; false for none.
func withChanges(base *avsysv1.MinecraftServerSpec, changes string) (avsysv1.MinecraftServerSpec, bool) {
	var fields map[string]json.RawMessage
	var requested avsysv1.MinecraftServerSpec
	if changes == "" || json.Unmarshal([]byte(changes), &fields) != nil || json.Unmarshal([]byte(changes), &requested) != nil {
		return avsysv1.MinecraftServerSpec{}, false
	}
	spec := fromSnapshot(&requested, base, fields)
	// What a copy is not given unless it asks.
	if _, said := fields["expose"]; !said {
		spec.Expose = base.Expose
	}
	if spec.JVM.ExtraFlags == "" && !namedInJVM(fields, "extraFlags") {
		spec.JVM.ExtraFlags = base.JVM.ExtraFlags
	}
	return spec, true
}

// layered lays newer changes over older ones, as one request asking for both
// would: what newer names replaces what older does, but for its game
// settings and launch flags, which are added to older's one by one.
func layered(older, newer json.RawMessage) (json.RawMessage, error) {
	fields := map[string]json.RawMessage{}
	if len(older) > 0 {
		if err := json.Unmarshal(older, &fields); err != nil {
			return nil, err
		}
	}
	var over map[string]json.RawMessage
	if len(newer) > 0 {
		if err := json.Unmarshal(newer, &over); err != nil {
			return nil, err
		}
	}
	for key, value := range over {
		if key == "properties" || key == "jvm" {
			var was, now map[string]json.RawMessage
			if json.Unmarshal(fields[key], &was) == nil && json.Unmarshal(value, &now) == nil && was != nil && now != nil {
				for k, v := range now {
					was[k] = v
				}
				merged, err := json.Marshal(was)
				if err != nil {
					return nil, err
				}
				fields[key] = merged
				continue
			}
		}
		fields[key] = value
	}
	if len(fields) == 0 {
		return nil, nil
	}
	return json.Marshal(fields)
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
func (s *Server) saveTemplateVersion(w http.ResponseWriter, r *http.Request, u *store.User, a *access, t *store.Template, mc *avsysv1.MinecraftServer, changes string, first bool) error {
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
			t.Current, t.Changes = "", changes
			if err = s.Store.SetTemplateCurrent(ctx, a.Org.ID, t.ID, "", changes); err != nil {
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

// copyTemplateVersion makes a copy of a snapshot the newest version of t. No
// files are copied, so it is done before answering. first says t was made
// for it, and is not kept if it fails.
func (s *Server) copyTemplateVersion(w http.ResponseWriter, r *http.Request, u *store.User, a *access, t *store.Template, id, changes string, first bool) error {
	ctx := context.WithoutCancel(r.Context())
	err := func() error {
		unclaim, err := claimTemplate(a, t.ID)
		if err != nil {
			return err
		}
		defer unclaim()
		snap, err := s.Backups.CopyAs(ctx, a.Org.Slug, id, templateHost(t.ID), u.Username)
		if err != nil {
			return snapshotError(err)
		}
		t.Current, t.Changes = "", changes
		if err := s.Store.SetTemplateCurrent(ctx, a.Org.ID, t.ID, "", changes); err != nil {
			// Made for a template that is not there: not left behind.
			if derr := s.Backups.Delete(ctx, a.Org.Slug, snap.ID); derr != nil {
				s.Log.Error("could not remove a template's version copied for nothing", "template", t.ID, "snapshot", snap.ID, "error", derr)
			}
			return err
		}
		if first {
			s.audit(ctx, u, a.Org, "template.create", t.Name, snap.ID)
		} else {
			s.audit(ctx, u, a.Org, "template.update", t.Name, snap.ID)
		}
		return nil
	}()
	if err != nil {
		if first {
			if derr := s.Store.DeleteTemplate(ctx, a.Org.ID, t.ID); derr != nil && !errors.Is(derr, store.ErrNotFound) {
				s.Log.Warn("could not forget a template whose first version failed", "template", t.ID, "error", derr)
			}
		}
		return err
	}
	_, v, err := s.template(ctx, a, t.ID)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, templateFor(a.Role, v))
	return nil
}

// addTemplateVersion saves a server, or a copy of a snapshot, as the newest
// version of a template.
func (s *Server) addTemplateVersion(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, err := s.org(r, u, store.RoleAdmin)
	if err != nil {
		return err
	}
	t, _, err := s.template(r.Context(), a, r.PathValue("template"))
	if err != nil {
		return err
	}
	var in struct{ Server, Snapshot string }
	if err := decode(r, &in); err != nil {
		return err
	}
	if in.Snapshot != "" {
		if in.Server != "" {
			return errBadRequest("A version is saved from a server or a snapshot, not both.")
		}
		if !s.Backups.Available() {
			return errNoBackups
		}
		// A new version is as it was saved: what was changed of the one
		// before is not carried over, but for whether servers made from it
		// start a new world.
		return s.copyTemplateVersion(w, r, u, a, t, in.Snapshot, worldOnly(t.Changes), false)
	}
	mc, err := s.templateSource(r.Context(), a, in.Server)
	if err != nil {
		return err
	}
	return s.saveTemplateVersion(w, r, u, a, t, mc, worldOnly(t.Changes), false)
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
		// Spec is the settings of a template of settings alone.
		Spec json.RawMessage
		// Changes are more for one saved from a server, laid over those it
		// has, as a copy's request asks for them.
		Changes json.RawMessage
	}
	if err := decode(r, &in); err != nil {
		return err
	}
	if len(in.Changes) > 0 {
		if v.SettingsOnly || v.Current == nil {
			return errBadRequest("This template has settings alone. Change them as its spec.")
		}
		record, err := s.recordOf(r.Context(), a.Org.Slug, v.Current.ID)
		if err != nil {
			return errNoRecord(err)
		}
		var older json.RawMessage
		if t.Changes != "" {
			older = json.RawMessage(t.Changes)
		}
		if t.Changes, err = s.templateChanges(r.Context(), a, &record.Spec, older, in.Changes); err != nil {
			return err
		}
	}
	if len(in.Spec) > 0 {
		if !v.SettingsOnly {
			return errBadRequest("This template was saved from a server. Change its settings as changes, or give it a new version.")
		}
		if t.Settings, err = s.templateSettings(r.Context(), a, in.Spec); err != nil {
			return err
		}
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
	// Its own world goes with it.
	list, err := s.templateSnapshots(r.Context(), a.Org.Slug)
	if err != nil {
		return err
	}
	all := append(slices.Clone(v.Versions), worldsOf(list, t.ID)...)
	if len(all) > 0 {
		release, err := s.claimToDelete(r.Context(), a, all)
		if err != nil {
			return err
		}
		defer release()
		ids := make([]string, len(all))
		for i, snap := range all {
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
		if err := s.Store.SetTemplateCurrent(r.Context(), a.Org.ID, t.ID, "", t.Changes); err != nil {
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
func (s *Server) templateVersionToMake(ctx context.Context, a *access, id string) (version string, changes json.RawMessage, err error) {
	t, v, err := s.template(ctx, a, id)
	if err != nil {
		return "", nil, err
	}
	if v.SettingsOnly {
		return "", nil, &Error{Status: http.StatusConflict, Code: "settings_only", Message: "This template has settings alone, with no files to copy: make a new server with its settings."}
	}
	if v.Current == nil {
		return "", nil, &Error{Status: http.StatusConflict, Code: "no_version", Message: "This template has no version yet. Wait for its first to be saved."}
	}
	if t.Changes != "" {
		changes = json.RawMessage(t.Changes)
	}
	// Its servers are given its own world, put in once its files are: the
	// version's is left out.
	if v.World == "own" {
		if changes, err = layered(changes, json.RawMessage(`{"world":"new"}`)); err != nil {
			return "", nil, err
		}
	}
	return v.Current.ID, changes, nil
}

// isTemplate is whether a snapshot is a version of a template, which is not
// part of any server's history.
func isTemplate(snap *backup.Snapshot) bool { return snap.Trigger == backup.TriggerTemplate }
