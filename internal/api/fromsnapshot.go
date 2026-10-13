package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
	"github.com/GT-Drehmal/avSYS/internal/backup"
	"github.com/GT-Drehmal/avSYS/internal/store"
)

// snapshotDetail is one snapshot with the settings its server had when it
// was taken.
type snapshotDetail struct {
	backup.Snapshot
	Spec avsysv1.MinecraftServerSpec `json:"spec"`
}

// getSnapshot returns a snapshot and what it recorded of its server. That
// record includes the server's extra environment, which may hold secrets, so
// it is for those who could make a server out of it.
func (s *Server) getSnapshot(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, err := s.org(r, u, store.RoleAdmin)
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
	writeJSON(w, http.StatusOK, snapshotDetail{Snapshot: *snap, Spec: record.Spec})
	return nil
}

func errNoRecord(err error) error {
	return &Error{Status: http.StatusUnprocessableEntity, Code: "no_record", Message: fmt.Sprintf("This snapshot does not say what server it is of, so none can be made from it: %v", err)}
}

// softwareGiven reports whether a request names any of the settings that a
// snapshot's files depend on.
func softwareGiven(spec *avsysv1.MinecraftServerSpec) bool {
	return spec.Version != "" || spec.Loader != "" || spec.LoaderVersion != "" ||
		spec.Modpack != nil || len(spec.Mods) > 0 || spec.ResourcePack != nil ||
		len(spec.Properties) > 0 || len(spec.Ops) > 0 || len(spec.Whitelist) > 0
}

// fromSnapshot builds the spec of a server made from a snapshot.
//
// What the files depend on comes from the snapshot: game version, loader,
// modpack, mods, resource pack, game properties, operators and whitelist.
// How much memory and storage it gets, and whether it sleeps, come from the
// snapshot too unless the request says otherwise. given holds the settings
// the request named, which is how sleep switched off is told from sleep not
// mentioned.
//
// Its address is its own. The image and extra environment are taken from
// the request alone, for the reason given at restoreSoftware: the record is
// read out of a repository the server itself could add to, and those two
// decide what runs. Whoever asks can read them from the snapshot and send
// them back, which makes it their decision.
func fromSnapshot(requested, recorded *avsysv1.MinecraftServerSpec, given map[string]json.RawMessage) avsysv1.MinecraftServerSpec {
	spec := *requested.DeepCopy()
	restoreSoftware(&spec, recorded.DeepCopy())
	spec.EULA = spec.EULA || recorded.EULA
	if spec.DisplayName == "" {
		spec.DisplayName = recorded.DisplayName
	}
	if spec.Resources.Memory.IsZero() {
		spec.Resources.Memory = recorded.Resources.Memory
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
	if _, said := given["sleep"]; !said {
		spec.Sleep = recorded.Sleep
	}
	return spec
}

// createFromSnapshot makes a new server out of a snapshot: of a server that
// was deleted, to bring it back, or of one that still exists, to copy it.
// The server exists as soon as this answers, stopped and held, and its files
// follow; with ?wait=true the answer waits for them.
func (s *Server) createFromSnapshot(w http.ResponseWriter, r *http.Request, u *store.User, a *access, name string, requested avsysv1.MinecraftServerSpec, given map[string]json.RawMessage, id string) error {
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
	if softwareGiven(&requested) {
		return errBadRequest("A server made from a snapshot takes its game version, loader, mods and game settings from it. Leave those out, and change them once the server exists.")
	}
	record, err := s.recordOf(ctx, a.Org.Slug, snap.ID)
	if err != nil {
		return errNoRecord(err)
	}
	spec := fromSnapshot(&requested, &record.Spec, given)
	s.applyDefaults(a.Org.Slug, name, &spec)
	if err := s.validateSpec(ctx, a.Namespace, name, &spec); err != nil {
		return err
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
		err := s.populate(ctx, a.Org.Slug, mc, snap, op, final)
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
func (s *Server) createFromServer(w http.ResponseWriter, r *http.Request, u *store.User, a *access, name string, requested avsysv1.MinecraftServerSpec, given map[string]json.RawMessage, source string) error {
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
	// nothing. They are checked again when the server is made.
	if err := s.Client.Get(ctx, client.ObjectKey{Namespace: a.Namespace, Name: name}, &avsysv1.MinecraftServer{}); err == nil {
		return &Error{Status: http.StatusConflict, Code: "conflict", Message: fmt.Sprintf("There is already a server named %s.", name)}
	} else if !apierrors.IsNotFound(err) {
		return err
	}
	if softwareGiven(&requested) {
		return errBadRequest("A copy takes its game version, loader, mods and game settings from the server it is of. Leave those out, and change them once the copy exists.")
	}
	if st := requested.State; st != "" && st != avsysv1.StateRunning && st != avsysv1.StateStopped {
		return errBadRequest("%q is not a state. A server is Running or Stopped.", st)
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
	return s.createFromSnapshot(w, r.WithContext(context.WithoutCancel(ctx)), u, a, name, requested, given, snap.ID)
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
func (s *Server) populate(ctx context.Context, org string, created *avsysv1.MinecraftServer, snap *backup.Snapshot, op *operation, final avsysv1.DesiredState) error {
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
	if err != nil {
		return err
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
