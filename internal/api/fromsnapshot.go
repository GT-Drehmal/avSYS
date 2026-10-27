package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"strings"
	"time"

	"github.com/pkg/sftp"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
	"github.com/GT-Drehmal/avSYS/internal/backup"
	"github.com/GT-Drehmal/avSYS/internal/store"
)

// snapshotDetail is one snapshot with the settings its server had when it
// was taken, and how long it will be here.
type snapshotDetail struct {
	backup.Snapshot
	Spec      avsysv1.MinecraftServerSpec `json:"spec"`
	Retention snapshotRetention           `json:"retention"`
}

// snapshotRetention says what, if anything, will remove a snapshot.
type snapshotRetention struct {
	// Scheduled is true for a snapshot that its server's schedule looks
	// after. Anything else stays until someone deletes it: one taken by hand
	// or for a restore, a copy or a delete, and any snapshot of a server
	// that is gone or has itself been taken out of scheduled snapshots.
	Scheduled bool `json:"scheduled"`
	// Kept is which rules of the schedule keep it: daily, weekly, monthly,
	// yearly. Empty for one the schedule removes the next time it runs.
	Kept []string `json:"kept,omitempty"`
	// Paused is true while the daily snapshots are switched off for its
	// server's organization. The rules are then only applied when someone
	// runs the schedule by hand, or switches it back on.
	Paused bool `json:"paused,omitempty"`
}

// getSnapshot returns a snapshot and what it recorded of its server. That
// record includes the server's image and extra environment, which decide
// what runs and may hold secrets, so those two are only shown to those who
// could make a server out of it.
func (s *Server) getSnapshot(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, err := s.org(r, u, store.RoleViewer)
	if err != nil {
		return err
	}
	if !s.Backups.Available() {
		return errNoBackups
	}
	snap, err := s.Backups.Get(r.Context(), a.Org.Slug, r.PathValue("id"))
	if err != nil {
		return snapshotError(err)
	}
	record, err := s.recordOf(r.Context(), a.Org.Slug, snap.ID)
	if err != nil {
		return errNoRecord(err)
	}
	writeJSON(w, http.StatusOK, snapshotDetail{Snapshot: *snap, Spec: recordFor(a.Role, record.Spec), Retention: s.retentionOf(r.Context(), a, snap)})
	return nil
}

// recordFor is what a snapshot recorded of its server, as someone with this
// role is shown it: without the image and extra environment unless they
// could make a server out of it.
func recordFor(role store.Role, spec avsysv1.MinecraftServerSpec) avsysv1.MinecraftServerSpec {
	if !role.AtLeast(store.RoleAdmin) {
		spec.Image, spec.Env, spec.JVM.ExtraFlags = "", nil, ""
	}
	return spec
}

// retentionOf works out what will remove a snapshot. Where that cannot be
// found out it is reported as kept by nothing in particular, which is the
// safe way round: nobody is told a snapshot is safe that is not.
func (s *Server) retentionOf(ctx context.Context, a *access, snap *backup.Snapshot) snapshotRetention {
	if snap.Trigger != backup.TriggerScheduled {
		return snapshotRetention{}
	}
	// The schedule is applied server by server, as each one's run completes.
	mc := &avsysv1.MinecraftServer{}
	if err := s.Client.Get(ctx, client.ObjectKey{Namespace: a.Namespace, Name: snap.Server}, mc); err != nil {
		return snapshotRetention{}
	}
	policy, err := s.policyOf(ctx, a.Org)
	if err != nil {
		return snapshotRetention{Scheduled: true}
	}
	// A server taken out of scheduled snapshots is passed over even by a run
	// asked for by hand, so nothing removes what it has.
	if switchedOff(&mc.Spec) {
		return snapshotRetention{}
	}
	// With the organization's daily snapshots off the rules still stand: a
	// run asked for by hand applies them.
	policy = policyFor(policy, &mc.Spec)
	retention := snapshotRetention{Scheduled: true, Paused: !policy.Enabled}
	kept, err := s.Backups.Kept(ctx, a.Org.Slug, snap.Server, policy.Keep)
	if err != nil {
		s.Log.Warn("could not work out which rule keeps a snapshot", "org", a.Org.Slug, "snapshot", snap.ID, "error", err)
		return retention
	}
	retention.Kept = kept[snap.ID]
	return retention
}

func errNoRecord(err error) error {
	return &Error{Status: http.StatusUnprocessableEntity, Code: "no_record", Message: fmt.Sprintf("Cannot create a server from this snapshot: %v", err)}
}

// checkCopiedContent says whether what a request asks a server made from
// another to have can be given to it. Its modpack stays as the original has
// it: the files were made by that pack. Its mods, plugins and datapacks can
// be chosen again, but a file uploaded to the original exists only among its
// files, and one not uploaded there has to wait until the server exists.
func checkCopiedContent(requested, recorded *avsysv1.MinecraftServerSpec, given map[string]json.RawMessage) error {
	if requested.Modpack != nil {
		return errBadRequest("A copy keeps its modpack. Change the modpack's version after it is created.")
	}
	if _, said := given["mods"]; !said {
		return nil
	}
	uploaded := map[string]bool{}
	for _, m := range recorded.Mods {
		if m.Source == avsysv1.SourceUpload {
			uploaded[m.Project] = true
		}
	}
	for _, m := range requested.Mods {
		if m.Source == avsysv1.SourceUpload && !uploaded[m.Project] {
			return errBadRequest("%s was not uploaded to the original. Upload it after the server is created.", m.Project)
		}
	}
	return nil
}

// fromSnapshot builds the spec of a server made from a snapshot. What the
// request names is used, and everything it leaves out is as the snapshot
// recorded it. given holds the settings the request named, which is how
// sleep switched off is told from sleep not mentioned.
//
// Game settings are merged one by one: the request's over the snapshot's.
// One it gives as null is no longer set by the panel, which leaves that
// line of the copied server.properties as the snapshot has it. The modpack
// is always the snapshot's; its mods are unless the request names them.
//
// Three things are the request's alone. Its address is its own. The image
// and extra environment are never taken from the snapshot, for the reason
// given at restoreSoftware: the record is read out of a repository the
// server itself could add to, and those two decide what runs. Whoever asks
// can read them from the snapshot and send them back, which makes it their
// decision. And when it is snapshotted is not in the record at all.
func fromSnapshot(requested, recorded *avsysv1.MinecraftServerSpec, given map[string]json.RawMessage) avsysv1.MinecraftServerSpec {
	spec := *requested.DeepCopy()
	recorded = recorded.DeepCopy()
	said := func(setting string) bool { _, ok := given[setting]; return ok }

	if !said("version") {
		spec.Version = recorded.Version
	}
	if !said("loader") {
		spec.Loader = recorded.Loader
	}
	// A build of the loader goes with the loader and game version it is for.
	if !said("loaderVersion") && spec.Version == recorded.Version && spec.Loader == recorded.Loader {
		spec.LoaderVersion = recorded.LoaderVersion
	}
	// What Java that version needs goes with it.
	spec.GameJava = 0
	if spec.Version == recorded.Version {
		spec.GameJava = recorded.GameJava
	}
	spec.Modpack = recorded.Modpack
	if !said("mods") {
		spec.Mods = recorded.Mods
	}
	if !said("resourcePack") {
		spec.ResourcePack = recorded.ResourcePack
	} else if rp, was := spec.ResourcePack, recorded.ResourcePack; rp != nil && was != nil && was.Uploaded != nil && rp.URL == was.URL {
		// The original's uploaded pack, made required or given a prompt:
		// still the one that comes in its files.
		rp.SHA1, rp.Uploaded, rp.Project, rp.Formats = was.SHA1, was.Uploaded, nil, was.Formats
		rp.Stack = was.DeepCopy().Stack
	}
	// What its mods listen on goes with them. The public ports given out
	// to the original are its own, and the copy gets ports of its own.
	if !said("ports") {
		spec.Ports = recorded.Ports
	}
	spec.Properties = mergeProperties(recorded.Properties, given["properties"])
	if !said("ops") {
		spec.Ops = recorded.Ops
	}
	if !said("whitelist") {
		spec.Whitelist = recorded.Whitelist
	}
	if !said("autoOp") {
		spec.AutoOp = recorded.AutoOp
	}
	if !said("autoWhitelist") {
		spec.AutoWhitelist = recorded.AutoWhitelist
	}
	// Members picked one by one: of the same organization, and those who
	// have left it since are dropped as the copy is checked.
	if !said("opMembers") {
		spec.OpMembers = recorded.OpMembers
	}
	if !said("whitelistMembers") {
		spec.WhitelistMembers = recorded.WhitelistMembers
	}

	spec.EULA = spec.EULA || recorded.EULA
	if spec.DisplayName == "" {
		spec.DisplayName = recorded.DisplayName
	}
	if spec.Resources.Memory.IsZero() {
		spec.Resources.Memory = recorded.Resources.Memory
	}
	// A CPU limit given as null is none, and not the original's.
	if spec.Resources.CPU == nil && !namedIn(given["resources"], "cpu") {
		spec.Resources.CPU = recorded.Resources.CPU
	}
	// Aikar's flags, on or off, as the original has them unless the request
	// says. Its extra flags run code, like an image, so they are not taken
	// from a record the server could have written: fromLive takes them from
	// a server that is there.
	if !namedInJVM(given, "aikarFlags") {
		spec.JVM.AikarFlags = recorded.JVM.AikarFlags
	}
	if spec.Storage.Size.IsZero() {
		spec.Storage.Size = recorded.Storage.Size
	}
	// The kind of storage it was on, too: where there is no default kind, a
	// volume asked for without one is never provided.
	if class := recorded.Storage.StorageClassName; spec.Storage.StorageClassName == nil && class != nil {
		own := *class
		spec.Storage.StorageClassName = &own
	}
	if !said("sleep") {
		spec.Sleep = recorded.Sleep
	}
	return spec
}

// namedIn reports whether a JSON object names a key at all, null included.
func namedIn(object json.RawMessage, key string) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(object, &fields) != nil {
		return false
	}
	_, ok := fields[key]
	return ok
}

// mergeProperties lays the game settings a request names over the recorded
// ones. asked is the request's "properties" as it was sent: a setting given
// as null is taken out of what the panel sets. The server's own
// server.properties, which is copied with its other files, then decides.
func mergeProperties(recorded map[string]string, asked json.RawMessage) map[string]string {
	var changes map[string]*string
	_ = json.Unmarshal(asked, &changes)
	if len(recorded) == 0 && len(changes) == 0 {
		return recorded
	}
	merged := make(map[string]string, len(recorded)+len(changes))
	for name, value := range recorded {
		merged[name] = value
	}
	for name, value := range changes {
		if value == nil {
			delete(merged, name)
			continue
		}
		merged[name] = *value
	}
	return merged
}

// specFromSnapshot is fromSnapshot with what follows from it settled: the
// defaults, whether the result is a server that can exist, and which Java a
// changed game version needs.
func (s *Server) specFromSnapshot(ctx context.Context, a *access, name string, requested, recorded *avsysv1.MinecraftServerSpec, given map[string]json.RawMessage) (avsysv1.MinecraftServerSpec, error) {
	spec := fromSnapshot(requested, recorded, given)
	// A pack given with the copy is checked; the original's, kept or made
	// required, is as it has it.
	if err := checkPackSHA1(spec.ResourcePack, recorded.ResourcePack); err != nil {
		return spec, err
	}
	// And its formats: given another address with the original's, they are
	// not known to be the new pack's.
	if err := checkPackFormats(spec.ResourcePack, recorded.ResourcePack); err != nil {
		return spec, err
	}
	changed := spec.Version != recorded.Version || spec.Loader != recorded.Loader || spec.LoaderVersion != recorded.LoaderVersion
	if changed && spec.Modpack != nil {
		return spec, errBadRequest("This server runs a modpack. Change the modpack's version after it is created.")
	}
	s.applyDefaults(a.Org.Slug, name, &spec)
	// "Latest" is settled here, as for any new server.
	if spec.Version == "LATEST" && spec.Modpack == nil {
		if latest := s.latestRelease(ctx, spec.Loader); latest != "" {
			spec.Version = latest
		}
	}
	if err := s.validateSpec(ctx, a.Namespace, name, &spec); err != nil {
		return spec, err
	}
	// Only a change of software is checked: a snapshot of a version that has
	// since left its software's list can still be made a server of.
	if changed {
		if err := s.checkVersion(ctx, &spec); err != nil {
			return spec, err
		}
	}
	if spec.Version != recorded.Version {
		s.resolveJava(ctx, &spec)
	}
	return spec, nil
}

// createFromSnapshot makes a new server out of a snapshot: of a server that
// was deleted, to bring it back, or of one that still exists, to copy it.
// The server exists as soon as this answers, stopped and held, and its files
// follow; with ?wait=true the answer waits for them.
func (s *Server) createFromSnapshot(w http.ResponseWriter, r *http.Request, u *store.User, a *access, name string, requested avsysv1.MinecraftServerSpec, given map[string]json.RawMessage, id string, picture newPicture, newWorld bool, node string) error {
	ctx := r.Context()
	if !s.Backups.Available() {
		return errNoBackups
	}
	// Said here, before any claim is made on the name: a claim would briefly
	// hold up the server that already has it.
	if err := s.Client.Get(ctx, client.ObjectKey{Namespace: a.Namespace, Name: name}, &avsysv1.MinecraftServer{}); err == nil {
		return &Error{Status: http.StatusConflict, Code: "conflict", Message: fmt.Sprintf("There is already a server named %s.", name)}
	} else if !apierrors.IsNotFound(err) {
		return err
	}
	if err := s.serviceNameIsFree(ctx, a.Namespace, name); err != nil {
		return err
	}
	// The state asked for is only applied once the files are in, which is too
	// late to find out that it is not one.
	if st := requested.State; st != "" && st != avsysv1.StateRunning && st != avsysv1.StateStopped {
		return errBadRequest("%q is not a state. A server is Running or Stopped.", st)
	}
	snap, err := s.Backups.Get(ctx, a.Org.Slug, id)
	if err != nil {
		return snapshotError(err)
	}
	// Under its own name it is that server brought back, and its history
	// goes on. Under another, it is a new server, which cannot take a name
	// that the snapshots of another are under.
	if snap.Server != name {
		if err := s.nameIsFree(ctx, a, name); err != nil {
			return err
		}
	}
	record, err := s.recordOf(ctx, a.Org.Slug, snap.ID)
	if err != nil {
		return errNoRecord(err)
	}
	if err := checkCopiedContent(&requested, &record.Spec, given); err != nil {
		return err
	}
	spec, err := s.specFromSnapshot(ctx, a, name, &requested, &record.Spec, given)
	if err != nil {
		return err
	}
	if err := worldLeavable(&spec, newWorld); err != nil {
		return err
	}
	if err := s.checkHostnameOwner(ctx, a.Org, spec.Expose.Hostname); err != nil {
		return err
	}
	// Mods were picked to fit one game version and loader, and are picked
	// again for another, as when a server's own version is changed. Any the
	// request added are pinned, as for any new server.
	if _, said := given["mods"]; said || targetOf(&spec) != targetOf(&record.Spec) {
		if err := s.resolveContent(ctx, &spec, &record.Spec); err != nil {
			return err
		}
	}
	if node != "" {
		if err := s.checkNode(ctx, node, spec.Resources.Memory); err != nil {
			return err
		}
	}
	if spec.Storage.Size.Value() < snap.Size {
		return errBadRequest("The snapshot has %s of files. Give the server more storage than that.", sizeOf(snap.Size))
	}
	// The server is created stopped and marked, so that nothing starts the
	// game before its files are there, and gets the state asked for last.
	final := spec.State
	spec.State = avsysv1.StateStopped

	if err := s.ensureNamespace(ctx, a.Org.Slug); err != nil {
		return err
	}
	// Claimed before it exists, so that there is no moment at which it is
	// there and anyone else may act on it.
	op, ok := s.ops.begin(a.Namespace, a.Org.Slug, name, kindCreate, snap.ID)
	if !ok {
		return &Error{Status: http.StatusConflict, Code: "busy", Message: "That snapshot is being deleted, or a server with this name is already being created."}
	}
	// It was looked up before the claim, and may have been deleted since.
	// With the claim held it cannot be.
	if _, err := s.Backups.Get(ctx, a.Org.Slug, snap.ID); err != nil {
		s.ops.end(a.Namespace, name)
		return snapshotError(err)
	}
	mc := &avsysv1.MinecraftServer{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: a.Namespace, Name: name,
			Annotations: map[string]string{avsysv1.AnnotationRestoring: snap.ID, avsysv1.AnnotationCreating: snap.ID},
		},
		Spec: spec,
	}
	// Placed where asked rather than where the original was kept.
	if node != "" {
		mc.Annotations[avsysv1.AnnotationNode] = node
		mc.Spec.NodeSelector = nil
	}
	// Its files come with operators in them, some of whom the panel made
	// for their role in the organization as it was then.
	for note, recorded := range record.roleListNotes() {
		if made := splitNames(recorded); len(made) > 0 {
			mc.Annotations[note] = strings.Join(made, ",")
		}
	}
	// Checked again as it is made: a domain may have been claimed while
	// the original was being snapshotted.
	if err := s.withHostname(ctx, a.Org, mc.Spec.Expose.Hostname, func() error { return s.createPlaced(ctx, mc, node) }); err != nil {
		s.ops.end(a.Namespace, name)
		return err
	}
	s.audit(ctx, u, a.Org, "server.create", name, fmt.Sprintf("from snapshot %s of %s", snap.ID, snap.Server))

	// Deleting the server calls this off. Nothing else does: the request
	// that asked for it going away does not.
	working, stop := context.WithCancel(context.WithoutCancel(ctx))
	s.ops.stoppable(op, stop)
	work := func() error {
		defer stop()
		ctx, cancel := context.WithTimeout(working, operationTimeout)
		defer cancel()
		err := s.populate(ctx, a.Org.Slug, mc, snap, op, final, &record.Spec, picture, newWorld)
		if err != nil {
			err = failed(err, "making this server from the snapshot did not finish, so its files are incomplete; delete it and make it again")
		}
		s.finishOperation(ctx, a.Namespace, name, kindCreate, snap, nil, err)
		return err
	}
	if r.URL.Query().Get("wait") == "true" {
		if err := work(); err != nil {
			return operationFailed(err)
		}
		if mc, err = s.fresh(ctx, mc); err != nil {
			return err
		}
	} else {
		go func() { _ = work() }()
	}
	writeJSON(w, http.StatusCreated, s.view(mc))
	return nil
}

// createFromServer makes a copy of a server as it is now. The copy is made
// at once, stopped and held, and answered with; a snapshot of the original
// is then taken, unless its newest one already has everything, and the copy
// is given its files from that, as its create operation. The original keeps
// running and is not touched. With ?wait=true it answers when all is done.
func (s *Server) createFromServer(w http.ResponseWriter, r *http.Request, u *store.User, a *access, name string, requested avsysv1.MinecraftServerSpec, given map[string]json.RawMessage, source string, picture newPicture, newWorld bool, node string) error {
	ctx := r.Context()
	if !s.Backups.Available() {
		return errNoBackups
	}
	mc := &avsysv1.MinecraftServer{}
	if err := s.Client.Get(ctx, client.ObjectKey{Namespace: a.Namespace, Name: source}, mc); apierrors.IsNotFound(err) {
		return &Error{Status: http.StatusNotFound, Code: "not_found", Message: fmt.Sprintf("There is no server named %s to copy.", source)}
	} else if err != nil {
		return err
	}
	// A copy is never the server that a name's snapshots are of.
	if err := s.nameIsFree(ctx, a, name); err != nil {
		return err
	}
	if err := s.serviceNameIsFree(ctx, a.Namespace, name); err != nil {
		return err
	}
	if err := checkCopiedContent(&requested, &mc.Spec, given); err != nil {
		return err
	}
	if st := requested.State; st != "" && st != avsysv1.StateRunning && st != avsysv1.StateStopped {
		return errBadRequest("%q is not a state. A server is Running or Stopped.", st)
	}
	fromLive(&requested, given, &mc.Spec)
	// Made from the server as it is now: what its snapshot records of it is
	// the same, written a moment later.
	spec, err := s.specFromSnapshot(ctx, a, name, &requested, &mc.Spec, given)
	if err != nil {
		return err
	}
	if err := worldLeavable(&spec, newWorld); err != nil {
		return err
	}
	if err := s.checkHostnameOwner(ctx, a.Org, spec.Expose.Hostname); err != nil {
		return err
	}
	if _, said := given["mods"]; said || targetOf(&spec) != targetOf(&mc.Spec) {
		if err := s.resolveContent(ctx, &spec, &mc.Spec); err != nil {
			return err
		}
	}
	if node != "" {
		if err := s.checkNode(ctx, node, spec.Resources.Memory); err != nil {
			return err
		}
	}
	if err := unfinished(mc); err != nil {
		return err
	}
	if err := settled(mc); err != nil {
		return err
	}
	final := spec.State
	spec.State = avsysv1.StateStopped
	if err := s.ensureNamespace(ctx, a.Org.Slug); err != nil {
		return err
	}

	// Both are claimed before the copy exists: the original, so that it is
	// not restored or moved while it is snapshotted, and the copy's name,
	// so that there is no moment at which it is there and anyone else may
	// act on it.
	op, ok := s.ops.begin(a.Namespace, a.Org.Slug, name, kindCreate, "")
	if !ok {
		return &Error{Status: http.StatusConflict, Code: "busy", Message: "A server with this name is already being created."}
	}
	origOp, ok := s.ops.begin(a.Namespace, a.Org.Slug, mc.Name, kindSnapshot, "")
	if !ok {
		s.ops.end(a.Namespace, name)
		if running := s.ops.current(a.Namespace, mc.Name); running != nil {
			return heldBy(running.Kind)
		}
		return errBusy
	}
	created := &avsysv1.MinecraftServer{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: a.Namespace, Name: name,
			// Held, as one whose files are on their way, until the snapshot
			// they come from is known and then they are in.
			Annotations: map[string]string{avsysv1.AnnotationRestoring: snapshotNotYetTaken, avsysv1.AnnotationCreating: snapshotNotYetTaken},
		},
		Spec: spec,
	}
	if node != "" {
		created.Annotations[avsysv1.AnnotationNode] = node
		created.Spec.NodeSelector = nil
	}
	// Its files come with operators in them, some of whom the panel made
	// for their role in the organization.
	for _, note := range []string{avsysv1.AnnotationAutoOps, avsysv1.AnnotationAutoWhitelist} {
		if made := splitNames(mc.Annotations[note]); len(made) > 0 {
			created.Annotations[note] = strings.Join(made, ",")
		}
	}
	if err := s.withHostname(ctx, a.Org, created.Spec.Expose.Hostname, func() error { return s.createPlaced(ctx, created, node) }); err != nil {
		s.ops.end(a.Namespace, mc.Name)
		s.ops.end(a.Namespace, name)
		return err
	}
	s.audit(ctx, u, a.Org, "server.copy", mc.Name, "to "+name)
	s.audit(ctx, u, a.Org, "server.create", name, "copy of "+mc.Name)

	// Deleting the copy calls this off. Nothing else does: the request that
	// asked for it going away does not.
	working, stop := context.WithCancel(context.WithoutCancel(ctx))
	s.ops.stoppable(op, stop)
	original := mc.Spec.DeepCopy()
	work := func() error {
		defer stop()
		ctx, cancel := context.WithTimeout(working, operationTimeout)
		defer cancel()
		snap, err := s.copyFrom(ctx, a, mc, origOp, created, op, u.Username)
		if err == nil {
			err = s.populate(ctx, a.Org.Slug, created, snap, op, final, original, picture, newWorld)
			if err != nil {
				err = failed(err, "making this server from the snapshot did not finish, so its files are incomplete; delete it and make it again")
			}
		}
		s.finishOperation(ctx, a.Namespace, name, kindCreate, snap, nil, err)
		return err
	}
	if r.URL.Query().Get("wait") == "true" {
		if err := work(); err != nil {
			return operationFailed(err)
		}
		if fresh, err := s.fresh(context.WithoutCancel(ctx), created); err == nil {
			created = fresh
		}
	} else {
		go func() { _ = work() }()
	}
	writeJSON(w, http.StatusCreated, s.view(created))
	return nil
}

// snapshotNotYetTaken stands for the snapshot a copy's files are to come
// from, on the copy, until it is taken.
const snapshotNotYetTaken = "pending"

// copyFrom takes the snapshot a copy is made from, ending the original's
// claim, and notes it on the copy, whose claim then carries it.
func (s *Server) copyFrom(ctx context.Context, a *access, mc *avsysv1.MinecraftServer, origOp *operation, created *avsysv1.MinecraftServer, op *operation, by string) (*backup.Snapshot, error) {
	s.setOperation(a.Namespace, op, "Taking a snapshot of "+displayNameOf(mc), 0)
	snap, err := s.takeSnapshot(ctx, a.Org.Slug, mc, origOp, backup.TriggerCopy, by)
	if errors.Is(err, backup.ErrUnchanged) {
		// Nothing has changed since its newest snapshot, which is then what
		// the copy is made from.
		snap, err = s.newestOf(ctx, a.Org.Slug, mc.Name)
	}
	s.finishOperation(ctx, a.Namespace, mc.Name, kindSnapshot, snap, snap, err)
	if err != nil {
		return nil, failed(err, "no snapshot of %s could be taken, so nothing was copied; delete this server and make the copy again", displayNameOf(mc))
	}
	if created.Spec.Storage.Size.Value() < snap.Size {
		return nil, failed(errors.New("too small"), "%s has %s of files, more than this server's storage; delete it and make the copy again with more", displayNameOf(mc), sizeOf(snap.Size))
	}
	// Not to be deleted until the copy has its files.
	if !s.ops.carry(a.Namespace, op, snap.ID) {
		return nil, failed(errors.New("being deleted"), "the snapshot taken of %s is being deleted, so nothing was copied; delete this server and make the copy again", displayNameOf(mc))
	}
	key := client.ObjectKeyFromObject(created)
	fresh := &avsysv1.MinecraftServer{}
	if err := s.Client.Get(ctx, key, fresh); err != nil {
		return nil, err
	}
	orig := fresh.DeepCopy()
	fresh.Annotations[avsysv1.AnnotationRestoring] = snap.ID
	fresh.Annotations[avsysv1.AnnotationCreating] = snap.ID
	if err := s.Client.Patch(ctx, fresh, client.MergeFrom(orig)); err != nil {
		return nil, err
	}
	return snap, nil
}

// fromLive fills in what a copy of a server takes from the server itself
// rather than from the snapshot it is made from, unless the request says.
// When it is snapshotted is not recorded in a snapshot. The nodes it is kept
// to are not taken from a record, which the server could have written; a
// server kept to some nodes on purpose is copied onto them.
func fromLive(requested *avsysv1.MinecraftServerSpec, given map[string]json.RawMessage, live *avsysv1.MinecraftServerSpec) {
	if _, said := given["backups"]; !said {
		requested.Backups = live.Backups.DeepCopy()
	}
	if _, said := given["nodeSelector"]; !said {
		requested.NodeSelector = maps.Clone(live.NodeSelector)
	}
	if !namedInJVM(given, "extraFlags") {
		requested.JVM.ExtraFlags = live.JVM.ExtraFlags
	}
}

// namedInJVM reports whether a request's jvm names a key at all, null
// included.
func namedInJVM(given map[string]json.RawMessage, key string) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(given["jvm"], &fields) != nil {
		return false
	}
	_, ok := fields[key]
	return ok
}

// newestOf returns a server's newest snapshot.
func (s *Server) newestOf(ctx context.Context, org, server string) (*backup.Snapshot, error) {
	list, err := s.Backups.List(ctx, org)
	if err != nil {
		return nil, err
	}
	for i := range list {
		if list[i].Server == server {
			return &list[i], nil
		}
	}
	return nil, errors.New("the server has no snapshot to copy from")
}

// populate copies a snapshot's files into a server that has just been
// created, and then lets it go.
//
// original is the spec of the server the snapshot is of. Content it had that
// the new server was not given, and that only the panel removes, is deleted
// once the files are in. A picture of its
// own goes in then too, over the one the files brought, before the game
// first starts.
func (s *Server) populate(ctx context.Context, org string, created *avsysv1.MinecraftServer, snap *backup.Snapshot, op *operation, final avsysv1.DesiredState, original *avsysv1.MinecraftServerSpec, picture newPicture, newWorld bool) error {
	key := client.ObjectKeyFromObject(created)
	// A new server's volume has to be provisioned and the workspace image may
	// have to be pulled, which on a node that has never run one takes a while.
	s.setOperation(key.Namespace, op, "Waiting for storage", 0)
	waiting, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	if err := s.ownPod(waiting, created); err != nil {
		return failed(err, "the new server's storage was not ready within 10 minutes")
	}
	ws, err := s.dialWorkspaceWithin(waiting, key.Namespace, key.Name, 10*time.Minute)
	if err != nil {
		return failed(err, "the new server's storage was not ready within 10 minutes")
	}
	defer ws.Close()

	s.setOperation(key.Namespace, op, "Copying files", 0)
	last := time.Time{}
	progress := func(p backup.Progress) {
		if time.Since(last) < 500*time.Millisecond {
			return
		}
		last = time.Now()
		s.setOperation(key.Namespace, op, "Copying files", p.Percent)
	}
	if newWorld {
		// All but the world, whose datapacks stay: content the server has.
		// Its seed goes as well, for the game to pick one unless it is
		// given one. The world is made as the server first starts.
		level := levelOf(&created.Spec)
		err = s.Backups.RestoreWithout(ctx, org, ws, snap.ID, worldFolders(level), []string{"/" + level + "/datapacks"}, progress)
		if err == nil {
			err = runIn(ws, "cd "+dataRoot+" && if [ -f server.properties ]; then sed -i '/^level-seed=/d' server.properties; fi")
		}
	} else {
		err = s.Backups.Restore(ctx, org, ws, snap.ID, progress)
	}
	// The picture may have come with the files, or gone with them. What was
	// read before, perhaps from an empty volume, is not kept.
	s.serverIcons.forget(key.Namespace + "/" + key.Name)
	if err != nil {
		return err
	}

	// Through the connection made above, to this server's own volume: a pod
	// of a server of the same name that was deleted may still be going away,
	// and a connection kept from it would reach its volume instead.
	if files, err := sftp.NewClient(ws); err != nil {
		s.Log.Warn("could not reach a new server's files to tidy them", "server", key.String(), "error", err)
		// Noted, for the look once a minute to remove when it can. The
		// picture is given up on: the files brought one, or none.
		gone := leftBehind(&created.Spec, withSavedNames(ctx, &created.Spec, original))
		if file := droppedPackFile(&created.Spec, original); file != "" {
			gone = append(gone, file)
		}
		if len(gone) > 0 {
			if err := s.setContentLeft(ctx, key, func(was []string) []string { return union(was, gone) }); err != nil {
				s.Log.Warn("could not note content left to remove", "server", key.String(), "error", err)
			}
		}
	} else {
		s.removeDroppedUploadsWith(ctx, files, key.Namespace, key.Name, &created.Spec, original)
		// Its original's uploaded pack came with its files: gone, if the
		// new server was given another or none.
		if droppedPackFile(&created.Spec, original) != "" {
			s.dropPacks(ctx, files, nil, key, keptPacks(created.Spec.ResourcePack), original.ResourcePack)
		}
		// As with a new server's picture, the server is never held back over it.
		if err := s.placePicture(files, key.Namespace, key.Name, picture); err != nil {
			s.Log.Warn("a new server's own picture was not put in place", "server", key.String(), "error", err)
		}
		files.Close()
	}

	// The marks come off and the state goes on in one change: there is no
	// moment at which the server is both released and not yet as asked for.
	s.setOperation(key.Namespace, op, "Finishing", 100)
	mc := &avsysv1.MinecraftServer{}
	if err := s.Client.Get(ctx, key, mc); err != nil {
		return err
	}
	orig := mc.DeepCopy()
	mc.Spec.State = final
	delete(mc.Annotations, avsysv1.AnnotationRestoring)
	delete(mc.Annotations, avsysv1.AnnotationCreating)
	return s.Client.Patch(ctx, mc, client.MergeFrom(orig))
}

// ownPod waits until the pod of this name is one that was made for this
// server. A server made under the name of one that was just deleted finds the
// old one's pod still there for a while, with the old volume, which is on
// its way out: files put there would go with it.
func (s *Server) ownPod(ctx context.Context, mc *avsysv1.MinecraftServer) error {
	for {
		pod := &corev1.Pod{}
		err := s.Client.Get(ctx, client.ObjectKeyFromObject(mc), pod)
		if err == nil && pod.DeletionTimestamp.IsZero() && metav1.IsControlledBy(pod, mc) {
			return nil
		}
		if err != nil && !apierrors.IsNotFound(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

// sizeOf writes a number of bytes the way people say it.
func sizeOf(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// displayNameOf is what a server is called on its page.
func displayNameOf(mc *avsysv1.MinecraftServer) string {
	if mc.Spec.DisplayName != "" {
		return mc.Spec.DisplayName
	}
	return mc.Name
}

// worldFolders are a world's folders, relative to the server directory: the
// world, and the Nether and the End where its software keeps them apart.
func worldFolders(level string) []string {
	return []string{"/" + level, "/" + level + "_nether", "/" + level + "_the_end"}
}

// worldLeavable says whether a server copied with a new world can be: it can
// when it is known which folder the world it leaves behind is in.
func worldLeavable(spec *avsysv1.MinecraftServerSpec, newWorld bool) error {
	if newWorld && levelOf(spec) == "" {
		return errBadRequest("Which folder its world is in could not be told from its settings, so it cannot be given a new one.")
	}
	return nil
}
