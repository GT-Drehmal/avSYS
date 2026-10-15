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
		spec.Image, spec.Env = "", nil
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
	return &Error{Status: http.StatusUnprocessableEntity, Code: "no_record", Message: fmt.Sprintf("This snapshot does not say what server it is of, so none can be made from it: %v", err)}
}

// checkCopiedContent says whether what a request asks a server made from
// another to have can be given to it. Its modpack stays as the original has
// it: the files were made by that pack. Its mods, plugins and datapacks can
// be chosen again, but a file uploaded to the original exists only among its
// files, and one not uploaded there has to wait until the server exists.
func checkCopiedContent(requested, recorded *avsysv1.MinecraftServerSpec, given map[string]json.RawMessage) error {
	if requested.Modpack != nil {
		return errBadRequest("A server made from another keeps its modpack. Change the modpack's version once the server exists.")
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
			return errBadRequest("%s was not uploaded to the original. Upload it once the server exists.", m.Project)
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

	spec.EULA = spec.EULA || recorded.EULA
	if spec.DisplayName == "" {
		spec.DisplayName = recorded.DisplayName
	}
	if spec.Resources.Memory.IsZero() {
		spec.Resources.Memory = recorded.Resources.Memory
	}
	if spec.Resources.CPU == nil {
		spec.Resources.CPU = recorded.Resources.CPU
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
	changed := spec.Version != recorded.Version || spec.Loader != recorded.Loader || spec.LoaderVersion != recorded.LoaderVersion
	if changed && spec.Modpack != nil {
		return spec, errBadRequest("This server runs a modpack, which decides its game version and loader. Change the modpack's version once the server exists.")
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
func (s *Server) createFromSnapshot(w http.ResponseWriter, r *http.Request, u *store.User, a *access, name string, requested avsysv1.MinecraftServerSpec, given map[string]json.RawMessage, id string, picture newPicture) error {
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
	// Mods were picked to fit one game version and loader, and are picked
	// again for another, as when a server's own version is changed. Any the
	// request added are pinned, as for any new server.
	if _, said := given["mods"]; said || targetOf(&spec) != targetOf(&record.Spec) {
		if err := s.resolveContent(ctx, &spec, &record.Spec); err != nil {
			return err
		}
	}
	if spec.Storage.Size.Value() < snap.Size {
		return errBadRequest("The snapshot holds %s of files. Give the server more storage than that.", sizeOf(snap.Size))
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
		return &Error{Status: http.StatusConflict, Code: "busy", Message: "That snapshot is being deleted, or a server of this name is being made already."}
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
	// Its files come with operators in them, some of whom the panel made
	// for their role in the organization as it was then.
	for note, recorded := range record.roleListNotes() {
		if made := splitNames(recorded); len(made) > 0 {
			mc.Annotations[note] = strings.Join(made, ",")
		}
	}
	if err := s.Client.Create(ctx, mc); err != nil {
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
		err := s.populate(ctx, a.Org.Slug, mc, snap, op, final, record.Spec.Mods, picture)
		if err != nil {
			err = fmt.Errorf("making this server from the snapshot did not finish, so its files are incomplete; delete it and make it again: %w", err)
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

// createFromServer makes a copy of a server as it is now: a snapshot of it
// is taken, unless its newest one already has everything, and the new server
// is made from that. The original keeps running and is not touched.
//
// The request waits for the snapshot. What follows is as for any server made
// from a snapshot: it exists once this answers, and its files follow.
func (s *Server) createFromServer(w http.ResponseWriter, r *http.Request, u *store.User, a *access, name string, requested avsysv1.MinecraftServerSpec, given map[string]json.RawMessage, source string, picture newPicture) error {
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
	// Said before the snapshot, which is the slow part and would be for
	// nothing. They are checked again when the server is made. A copy is
	// never the server that a name's snapshots are of.
	if err := s.nameIsFree(ctx, a, name); err != nil {
		return err
	}
	if err := checkCopiedContent(&requested, &mc.Spec, given); err != nil {
		return err
	}
	if st := requested.State; st != "" && st != avsysv1.StateRunning && st != avsysv1.StateStopped {
		return errBadRequest("%q is not a state. A server is Running or Stopped.", st)
	}
	fromLive(&requested, given, &mc.Spec)
	// What the request asks for is held against the server as it is now,
	// before the snapshot: a copy that could not be made is not worth one.
	// That includes its mods having a build for the game version asked for.
	wanted, err := s.specFromSnapshot(ctx, a, name, &requested, &mc.Spec, given)
	if err != nil {
		return err
	}
	if _, said := given["mods"]; said || targetOf(&wanted) != targetOf(&mc.Spec) {
		if err := s.resolveContent(ctx, &wanted, &mc.Spec); err != nil {
			return err
		}
	}
	if err := unfinished(mc); err != nil {
		return err
	}
	if err := settled(mc); err != nil {
		return err
	}
	op, ok := s.ops.begin(a.Namespace, a.Org.Slug, mc.Name, kindSnapshot, "")
	if !ok {
		return errBusy
	}
	// The caller hanging up does not stop the snapshot once it has begun.
	working, cancel := context.WithTimeout(context.WithoutCancel(ctx), operationTimeout)
	defer cancel()
	snap, err := s.takeSnapshot(working, a.Org.Slug, mc, op, backup.TriggerCopy, u.Username)
	if errors.Is(err, backup.ErrUnchanged) {
		// Nothing has changed since its newest snapshot, which is then what
		// the copy is made from.
		snap, err = s.newestOf(working, a.Org.Slug, mc.Name)
	}
	s.finishOperation(working, a.Namespace, mc.Name, kindSnapshot, snap, snap, err)
	if err != nil {
		return operationFailed(err)
	}
	s.audit(ctx, u, a.Org, "server.copy", mc.Name, "to "+name)
	// The snapshot was seen through although the caller may have hung up
	// meanwhile, and so is making the server from it: otherwise a dropped
	// connection would leave the snapshot and nothing to show for it.
	return s.createFromSnapshot(w, r.WithContext(context.WithoutCancel(ctx)), u, a, name, requested, given, snap.ID, picture)
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
// copied is the content the original had. A file uploaded to it that the new
// server was not given is deleted once the files are in. A picture of its
// own goes in then too, over the one the files brought, before the game
// first starts.
func (s *Server) populate(ctx context.Context, org string, created *avsysv1.MinecraftServer, snap *backup.Snapshot, op *operation, final avsysv1.DesiredState, copied []avsysv1.ContentRef, picture newPicture) error {
	key := client.ObjectKeyFromObject(created)
	// A new server's volume has to be provisioned and the workspace image may
	// have to be pulled, which on a node that has never run one takes a while.
	s.setOperation(key.Namespace, op, "Waiting for storage", 0)
	waiting, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	if err := s.ownPod(waiting, created); err != nil {
		return fmt.Errorf("the new server's storage was not ready within 10 minutes: %w", err)
	}
	ws, err := s.dialWorkspaceWithin(waiting, key.Namespace, key.Name, 10*time.Minute)
	if err != nil {
		return fmt.Errorf("the new server's storage was not ready within 10 minutes: %w", err)
	}
	defer ws.Close()

	s.setOperation(key.Namespace, op, "Copying files", 0)
	last := time.Time{}
	err = s.Backups.Restore(ctx, org, ws, snap.ID, func(p backup.Progress) {
		if time.Since(last) < 500*time.Millisecond {
			return
		}
		last = time.Now()
		s.setOperation(key.Namespace, op, "Copying files", p.Percent)
	})
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
	} else {
		s.removeDroppedUploadsWith(ctx, files, key.Namespace, key.Name, copied, created.Spec.Mods)
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
