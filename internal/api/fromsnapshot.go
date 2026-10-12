package api

import (
	"context"
	"fmt"
	"net/http"
	"time"

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
// snapshot too unless the request says otherwise.
//
// Its address is its own. The image and extra environment are taken from
// the request alone, for the reason given at restoreSoftware: the record is
// read out of a repository the server itself could add to, and those two
// decide what runs. Whoever asks can read them from the snapshot and send
// them back, which makes it their decision.
func fromSnapshot(requested, recorded *avsysv1.MinecraftServerSpec) avsysv1.MinecraftServerSpec {
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
	if spec.Sleep == (avsysv1.SleepSpec{}) {
		spec.Sleep = recorded.Sleep
	}
	return spec
}

// createFromSnapshot makes a new server out of a snapshot: of a server that
// was deleted, to bring it back, or of one that still exists, to copy it.
// The server exists as soon as this answers, stopped and held, and its files
// follow; with ?wait=true the answer waits for them.
func (s *Server) createFromSnapshot(w http.ResponseWriter, r *http.Request, u *store.User, a *access, name string, requested avsysv1.MinecraftServerSpec, id string) error {
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
	spec := fromSnapshot(&requested, &record.Spec)
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

	key := client.ObjectKeyFromObject(mc)
	work := func(ctx context.Context) error {
		ctx, cancel := context.WithTimeout(ctx, operationTimeout)
		defer cancel()
		err := s.populate(ctx, a.Org.Slug, key, snap, op, final)
		if err != nil {
			err = fmt.Errorf("making this server from the snapshot did not finish, so its files are incomplete; delete it and make it again: %w", err)
		}
		s.finishOperation(ctx, a.Namespace, name, kindCreate, snap, nil, err)
		return err
	}
	if r.URL.Query().Get("wait") == "true" {
		if err := work(context.WithoutCancel(ctx)); err != nil {
			return operationFailed(err)
		}
		if mc, err = s.fresh(ctx, mc); err != nil {
			return err
		}
	} else {
		go func() { _ = work(context.Background()) }()
	}
	writeJSON(w, http.StatusCreated, s.view(mc))
	return nil
}

// populate copies a snapshot's files into a server that has just been
// created, and then lets it go.
func (s *Server) populate(ctx context.Context, org string, key client.ObjectKey, snap *backup.Snapshot, op *operation, final avsysv1.DesiredState) error {
	// A new server's volume has to be provisioned and the workspace image may
	// have to be pulled, which on a node that has never run one takes a while.
	s.setOperation(key.Namespace, op, "Waiting for storage", 0)
	ws, err := s.dialWorkspaceWithin(ctx, key.Namespace, key.Name, 10*time.Minute)
	if err != nil {
		return fmt.Errorf("the new server's storage did not become reachable: %w", err)
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
