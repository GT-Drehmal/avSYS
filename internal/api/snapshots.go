package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
	"github.com/GT-Drehmal/avSYS/internal/backup"
	"github.com/GT-Drehmal/avSYS/internal/store"
)

// operation is a snapshot or restore under way on one server. There is at
// most one per server at a time.
type operation struct {
	Kind      string    `json:"kind"`
	Org       string    `json:"org"`
	Server    string    `json:"server"`
	Stage     string    `json:"stage"`
	Percent   float64   `json:"percent"`
	StartedAt time.Time `json:"startedAt"`
}

const (
	kindSnapshot = "snapshot"
	kindRestore  = "restore"
)

type operations struct {
	mu      sync.Mutex
	running map[string]*operation
}

// begin claims a server for an operation; ok is false if it already has one.
func (o *operations) begin(namespace, org, server, kind string) (*operation, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.running == nil {
		o.running = map[string]*operation{}
	}
	key := namespace + "/" + server
	if o.running[key] != nil {
		return nil, false
	}
	op := &operation{Kind: kind, Org: org, Server: server, Stage: "Starting", StartedAt: time.Now().UTC()}
	o.running[key] = op
	return op, true
}

func (o *operations) end(namespace, server string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	delete(o.running, namespace+"/"+server)
}

// current returns a copy of the operation running on a server, if any.
func (o *operations) current(namespace, server string) *operation {
	o.mu.Lock()
	defer o.mu.Unlock()
	if op := o.running[namespace+"/"+server]; op != nil {
		copied := *op
		return &copied
	}
	return nil
}

func (o *operations) inNamespace(namespace string) []operation {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := []operation{}
	for key, op := range o.running {
		if len(key) > len(namespace) && key[:len(namespace)+1] == namespace+"/" {
			out = append(out, *op)
		}
	}
	return out
}

// set updates an operation's progress and tells open pages about it.
func (s *Server) setOperation(namespace string, op *operation, stage string, percent float64) {
	s.ops.mu.Lock()
	op.Stage, op.Percent = stage, percent
	copied := *op
	s.ops.mu.Unlock()
	s.Events.Publish(Event{Type: "operation.progress", Namespace: namespace, Data: copied})
}

// operationDone is sent when a snapshot or restore ends, either way.
type operationDone struct {
	Kind     string           `json:"kind"`
	Org      string           `json:"org"`
	Server   string           `json:"server"`
	Error    string           `json:"error,omitempty"`
	Snapshot *backup.Snapshot `json:"snapshot,omitempty"`
}

var (
	errBusy           = &Error{Status: http.StatusConflict, Code: "busy", Message: "A snapshot or restore is already running on this server."}
	errNoBackups      = &Error{Status: http.StatusServiceUnavailable, Code: "backups_unavailable", Message: "Snapshots are not available: restic is not installed where the panel runs."}
	errNoSuchSnapshot = &Error{Status: http.StatusNotFound, Code: "not_found", Message: "No such snapshot."}
)

func snapshotError(err error) error {
	switch {
	case errors.Is(err, backup.ErrNotFound):
		return errNoSuchSnapshot
	case errors.Is(err, backup.ErrUnavailable):
		return errNoBackups
	}
	return err
}

// snapshotRecord is stored inside every snapshot. It is what lets a server be
// recreated from the repository alone.
type snapshotRecord struct {
	Format  int                         `json:"format"`
	Panel   string                      `json:"panel"`
	Org     string                      `json:"org"`
	Server  string                      `json:"server"`
	TakenAt time.Time                   `json:"takenAt"`
	Spec    avsysv1.MinecraftServerSpec `json:"spec"`
}

func softwareOf(spec *avsysv1.MinecraftServerSpec) (version, loader string) {
	if spec.Modpack != nil {
		return spec.Modpack.GameVersion, spec.Modpack.Loader
	}
	return spec.Version, string(spec.Loader)
}

type snapshotList struct {
	// Available is false when the panel cannot run restic at all.
	Available  bool              `json:"available"`
	Snapshots  []backup.Snapshot `json:"snapshots"`
	Operations []operation       `json:"operations"`
	// Usage is only measured for the organization-wide list.
	Usage *backup.Usage `json:"usage,omitempty"`
}

// listOrgSnapshots returns every snapshot in the organization's repository,
// including those of servers that no longer exist.
func (s *Server) listOrgSnapshots(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, err := s.org(r, u, store.RoleViewer)
	if err != nil {
		return err
	}
	out := snapshotList{Available: s.Backups.Available(), Snapshots: []backup.Snapshot{}, Operations: s.ops.inNamespace(a.Namespace)}
	if !out.Available {
		writeJSON(w, http.StatusOK, out)
		return nil
	}
	if out.Snapshots, err = s.Backups.List(r.Context(), a.Org.Slug); err != nil {
		return err
	}
	usage, err := s.Backups.Usage(r.Context(), a.Org.Slug)
	if err != nil {
		return err
	}
	out.Usage = &usage
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (s *Server) listServerSnapshots(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, mc, err := s.server(r, u, store.RoleViewer)
	if err != nil {
		return err
	}
	out := snapshotList{Available: s.Backups.Available(), Snapshots: []backup.Snapshot{}, Operations: []operation{}}
	if op := s.ops.current(a.Namespace, mc.Name); op != nil {
		out.Operations = append(out.Operations, *op)
	}
	if out.Available {
		all, err := s.Backups.List(r.Context(), a.Org.Slug)
		if err != nil {
			return err
		}
		for _, snap := range all {
			if snap.Server == mc.Name {
				out.Snapshots = append(out.Snapshots, snap)
			}
		}
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// createSnapshot takes a snapshot now. It answers as soon as the snapshot has
// started; with ?wait=true it answers when it is done, with the snapshot.
func (s *Server) createSnapshot(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, mc, err := s.server(r, u, store.RoleOperator)
	if err != nil {
		return err
	}
	if !s.Backups.Available() {
		return errNoBackups
	}
	op, ok := s.ops.begin(a.Namespace, a.Org.Slug, mc.Name, kindSnapshot)
	if !ok {
		return errBusy
	}
	work := func(ctx context.Context) (*backup.Snapshot, error) {
		defer s.ops.end(a.Namespace, mc.Name)
		snap, err := s.takeSnapshot(ctx, a.Org.Slug, mc, op, backup.TriggerManual, u.Username)
		s.finishOperation(ctx, a.Namespace, mc.Name, kindSnapshot, snap, err)
		if err == nil {
			s.audit(ctx, u, a.Org, "snapshot.create", mc.Name, snap.ID)
		}
		return snap, err
	}
	if r.URL.Query().Get("wait") == "true" {
		snap, err := work(context.WithoutCancel(r.Context()))
		if err != nil {
			return operationFailed(err)
		}
		writeJSON(w, http.StatusCreated, snap)
		return nil
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), operationTimeout)
		defer cancel()
		_, _ = work(ctx)
	}()
	writeJSON(w, http.StatusAccepted, op)
	return nil
}

const operationTimeout = 6 * time.Hour

func operationFailed(err error) error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	if errors.Is(err, backup.ErrOldWorkspace) {
		return &Error{Status: http.StatusConflict, Code: "workspace_outdated", Message: "This server was started before snapshots existed. Restart it, then try again."}
	}
	return &Error{Status: http.StatusBadGateway, Code: "snapshot_failed", Message: err.Error()}
}

// takeSnapshot saves the server as it is now. A running game is told to
// write everything out and then hold its saves until the files are copied.
func (s *Server) takeSnapshot(ctx context.Context, org string, mc *avsysv1.MinecraftServer, op *operation, trigger, by string) (*backup.Snapshot, error) {
	ws, err := s.Gateway.DialWorkspace(ctx, mc.Namespace, mc.Name)
	if err != nil {
		return nil, fmt.Errorf("the server's files are not reachable: %w", err)
	}
	defer ws.Close()

	if mc.Status.Phase == avsysv1.PhaseRunning {
		s.setOperation(mc.Namespace, op, "Saving the world", 0)
		// Noted on the server before the game is told, so that a panel which
		// dies in the next few minutes leaves word for the one that replaces
		// it. A game left with saving off loses everything at its next stop.
		key := client.ObjectKeyFromObject(mc)
		if err := s.markSavingPaused(ctx, key, true); err != nil {
			return nil, err
		}
		defer func() {
			// Saving is switched back on whatever happens next, including
			// the panel's own request being abandoned.
			ctx := context.WithoutCancel(ctx)
			if _, err := s.rconFor(ctx, mc, "save-on", 15*time.Second); err != nil {
				s.Log.Error("could not switch saving back on; it will be retried when the panel next starts", "server", mc.Name, "namespace", mc.Namespace, "error", err)
				return
			}
			if err := s.markSavingPaused(ctx, key, false); err != nil {
				s.Log.Warn("could not clear the saving-paused note", "server", mc.Name, "error", err)
			}
		}()
		if _, err := s.rconFor(ctx, mc, "save-off", 15*time.Second); err == nil {
			if _, err := s.rconFor(ctx, mc, "save-all flush", 5*time.Minute); err != nil {
				s.Log.Warn("could not flush the world before a snapshot", "server", mc.Name, "error", err)
			}
		} else {
			// A game that is still starting has no console yet. Its files are
			// copied as they are.
			s.Log.Warn("could not pause saving before a snapshot", "server", mc.Name, "error", err)
		}
	}

	version, loader := softwareOf(&mc.Spec)
	meta := backup.Meta{
		Server: mc.Name, Trigger: trigger, By: by, Version: version, Loader: loader,
		Record: snapshotRecord{Format: 1, Panel: s.Config.Version, Org: org, Server: mc.Name, TakenAt: time.Now().UTC(), Spec: mc.Spec},
	}
	s.setOperation(mc.Namespace, op, "Copying files", 0)
	last := time.Time{}
	return s.Backups.Snapshot(ctx, org, ws, meta, func(p backup.Progress) {
		// restic reports many times a second; pages do not need that.
		if time.Since(last) < 500*time.Millisecond {
			return
		}
		last = time.Now()
		s.setOperation(mc.Namespace, op, "Copying files", p.Percent)
	})
}

// markSavingPaused sets or clears the note that a game has been told to hold
// its saves.
func (s *Server) markSavingPaused(ctx context.Context, key client.ObjectKey, paused bool) error {
	mc := &avsysv1.MinecraftServer{}
	if err := s.Client.Get(ctx, key, mc); err != nil {
		return err
	}
	orig := mc.DeepCopy()
	if paused {
		if mc.Annotations == nil {
			mc.Annotations = map[string]string{}
		}
		mc.Annotations[avsysv1.AnnotationSavingPaused] = time.Now().UTC().Format(time.RFC3339)
	} else {
		delete(mc.Annotations, avsysv1.AnnotationSavingPaused)
	}
	return s.Client.Patch(ctx, mc, client.MergeFrom(orig))
}

// ResumeSaving switches saving back on for every game that a previous panel
// left holding its saves, having been stopped in the middle of a snapshot.
// It is run once when the panel starts.
func (s *Server) ResumeSaving(ctx context.Context) {
	var all avsysv1.MinecraftServerList
	if err := s.Client.List(ctx, &all); err != nil {
		s.Log.Error("looking for games left with saving paused", "error", err)
		return
	}
	for i := range all.Items {
		mc := &all.Items[i]
		if _, paused := mc.Annotations[avsysv1.AnnotationSavingPaused]; !paused {
			continue
		}
		key := client.ObjectKeyFromObject(mc)
		// A game that is not running any more starts with saving on.
		if mc.Status.Phase == avsysv1.PhaseRunning {
			if _, err := s.rconFor(ctx, mc, "save-on", 15*time.Second); err != nil {
				s.Log.Error("could not switch saving back on after an interrupted snapshot", "server", mc.Name, "namespace", mc.Namespace, "error", err)
				continue
			}
		}
		if err := s.markSavingPaused(ctx, key, false); err != nil {
			s.Log.Warn("could not clear the saving-paused note", "server", mc.Name, "error", err)
			continue
		}
		s.Log.Info("switched saving back on after an interrupted snapshot", "server", mc.Name, "namespace", mc.Namespace)
	}
}

// finishOperation records how a snapshot or restore ended on the server and
// tells open pages.
func (s *Server) finishOperation(ctx context.Context, namespace, server, kind string, snap *backup.Snapshot, opErr error) {
	ctx = context.WithoutCancel(ctx)
	org, _ := s.orgOfNamespace(namespace)
	done := operationDone{Kind: kind, Org: org, Server: server, Snapshot: snap}
	if opErr != nil {
		done.Error = opErr.Error()
		s.Log.Error(kind+" failed", "namespace", namespace, "server", server, "error", opErr)
	}
	mc := &avsysv1.MinecraftServer{}
	if err := s.Client.Get(ctx, client.ObjectKey{Namespace: namespace, Name: server}, mc); err == nil {
		orig := mc.DeepCopy()
		if mc.Status.Snapshots == nil {
			mc.Status.Snapshots = &avsysv1.SnapshotStatus{}
		}
		mc.Status.Snapshots.Error = done.Error
		if snap != nil && kind == kindSnapshot {
			at := metav1.NewTime(snap.Time)
			mc.Status.Snapshots.LastTime, mc.Status.Snapshots.LastID = &at, snap.ID
		}
		if err := s.Client.Status().Patch(ctx, mc, client.MergeFrom(orig)); err != nil {
			s.Log.Error("recording a "+kind+" on the server", "server", server, "error", err)
		}
	}
	s.Events.Publish(Event{Type: "operation.done", Namespace: namespace, Data: done})
}

// restoreSoftware copies the parts of a server's settings that a snapshot's
// files depend on. A restore puts them back with the files; what the server
// is given to run on (memory, storage, address, sleep) stays as it is now.
//
// The image and extra environment are left as they are as well. The record
// comes out of a repository that the server's own workspace can add to, and
// those two fields decide what code runs with what settings, which is an
// admin's call and not something to take from a file.
func restoreSoftware(dst, src *avsysv1.MinecraftServerSpec) {
	dst.Version, dst.Loader, dst.LoaderVersion = src.Version, src.Loader, src.LoaderVersion
	dst.Modpack, dst.Mods, dst.ResourcePack = src.Modpack, src.Mods, src.ResourcePack
	dst.Properties, dst.Ops, dst.Whitelist = src.Properties, src.Ops, src.Whitelist
}

// restoreSnapshot puts a server back to one of its own snapshots. The game is
// stopped for it, and what is there now is snapshotted first so that the
// restore can itself be undone.
func (s *Server) restoreSnapshot(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, mc, err := s.server(r, u, store.RoleAdmin)
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
	if snap.Server != mc.Name {
		return errBadRequest("That snapshot is of %s. A server can only be restored to its own snapshots.", snap.Server)
	}
	op, ok := s.ops.begin(a.Namespace, a.Org.Slug, mc.Name, kindRestore)
	if !ok {
		return errBusy
	}
	work := func(ctx context.Context) error {
		defer s.ops.end(a.Namespace, mc.Name)
		err := s.restore(ctx, a.Org.Slug, mc, snap, op, u.Username)
		s.finishOperation(ctx, a.Namespace, mc.Name, kindRestore, snap, err)
		if err == nil {
			s.audit(ctx, u, a.Org, "snapshot.restore", mc.Name, snap.ID)
		}
		return err
	}
	if r.URL.Query().Get("wait") == "true" {
		if err := work(context.WithoutCancel(r.Context())); err != nil {
			return operationFailed(err)
		}
		writeJSON(w, http.StatusOK, snap)
		return nil
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), operationTimeout)
		defer cancel()
		_ = work(ctx)
	}()
	writeJSON(w, http.StatusAccepted, op)
	return nil
}

func (s *Server) restore(ctx context.Context, org string, mc *avsysv1.MinecraftServer, snap *backup.Snapshot, op *operation, by string) error {
	key := client.ObjectKeyFromObject(mc)
	wasRunning := mc.Spec.State != avsysv1.StateStopped

	// What a snapshot holds of the server's settings is read before anything
	// is touched, so that an unreadable snapshot stops here.
	raw, err := s.Backups.Record(ctx, org, snap.ID)
	if err != nil {
		return fmt.Errorf("reading the snapshot's record of the server: %w", err)
	}
	var record snapshotRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return fmt.Errorf("the snapshot's record of the server is unreadable: %w", err)
	}
	// The settings a restore applies get the same checks as settings typed
	// into the panel, before anything is touched.
	restored := mc.Spec.DeepCopy()
	restoreSoftware(restored, &record.Spec)
	if err := s.validateSpec(ctx, mc.Namespace, mc.Name, restored); err != nil {
		return fmt.Errorf("the snapshot's settings are not valid here: %w", err)
	}

	s.setOperation(mc.Namespace, op, "Saving the current state first", 0)
	if _, err := s.takeSnapshot(ctx, org, mc, op, backup.TriggerPreRestore, by); err != nil {
		return fmt.Errorf("the current state could not be saved first, so nothing was changed: %w", err)
	}

	s.setOperation(mc.Namespace, op, "Stopping the server", 0)
	if err := s.patchSpec(ctx, key, func(spec *avsysv1.MinecraftServerSpec) { spec.State = avsysv1.StateStopped }); err != nil {
		return err
	}
	if err := s.waitPhase(ctx, key, avsysv1.PhaseStopped, 5*time.Minute); err != nil {
		return fmt.Errorf("the server did not stop: %w", err)
	}

	ws, err := s.Gateway.DialWorkspace(ctx, mc.Namespace, mc.Name)
	if err != nil {
		return fmt.Errorf("the server's files are not reachable: %w", err)
	}
	defer ws.Close()
	s.setOperation(mc.Namespace, op, "Restoring files", 0)
	last := time.Time{}
	err = s.Backups.Restore(ctx, org, ws, snap.ID, func(p backup.Progress) {
		if time.Since(last) < 500*time.Millisecond {
			return
		}
		last = time.Now()
		s.setOperation(mc.Namespace, op, "Restoring files", p.Percent)
	})
	if err != nil {
		// The server is left stopped: its files are in an unknown state and
		// starting the game on them could make it worse.
		return fmt.Errorf("restoring files failed and the server was left stopped; the state from before is in the newest snapshot: %w", err)
	}

	s.setOperation(mc.Namespace, op, "Applying settings", 100)
	return s.patchSpec(ctx, key, func(spec *avsysv1.MinecraftServerSpec) {
		restoreSoftware(spec, &record.Spec)
		if wasRunning {
			spec.State = avsysv1.StateRunning
		}
	})
}

// patchSpec applies a change to a server's spec on top of its current state.
func (s *Server) patchSpec(ctx context.Context, key client.ObjectKey, change func(*avsysv1.MinecraftServerSpec)) error {
	mc := &avsysv1.MinecraftServer{}
	if err := s.Client.Get(ctx, key, mc); err != nil {
		return err
	}
	orig := mc.DeepCopy()
	change(&mc.Spec)
	return s.Client.Patch(ctx, mc, client.MergeFrom(orig))
}

func (s *Server) waitPhase(ctx context.Context, key client.ObjectKey, want avsysv1.Phase, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		mc := &avsysv1.MinecraftServer{}
		if err := s.Client.Get(ctx, key, mc); err != nil {
			return err
		}
		if mc.Status.Phase == want {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("still %s after %s", mc.Status.Phase, timeout)
		case <-time.After(time.Second):
		}
	}
}

// deleteSnapshot removes one snapshot for good. Nothing else ever removes a
// snapshot of a server that has been deleted.
func (s *Server) deleteSnapshot(w http.ResponseWriter, r *http.Request, u *store.User) error {
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
	if err := s.Backups.Delete(context.WithoutCancel(r.Context()), a.Org.Slug, snap.ID); err != nil {
		return snapshotError(err)
	}
	s.audit(r.Context(), u, a.Org, "snapshot.delete", snap.Server, snap.ID)
	s.Events.Publish(Event{Type: "operation.done", Namespace: a.Namespace, Data: operationDone{Kind: "delete", Org: a.Org.Slug, Server: snap.Server}})
	w.WriteHeader(http.StatusNoContent)
	return nil
}
