package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
	"github.com/GT-Drehmal/avSYS/internal/backup"
	"github.com/GT-Drehmal/avSYS/internal/controller"
	"github.com/GT-Drehmal/avSYS/internal/store"
)

// operation is a snapshot or restore under way on one server. There is at
// most one per server at a time.
type operation struct {
	Kind   string `json:"kind"`
	Org    string `json:"org"`
	Server string `json:"server"`
	// Snapshot is the one being restored to, for a restore.
	Snapshot  string    `json:"snapshot,omitempty"`
	Stage     string    `json:"stage"`
	Percent   float64   `json:"percent"`
	StartedAt time.Time `json:"startedAt"`
	// stop calls the operation off, for the kinds that can be.
	stop context.CancelFunc
}

const (
	kindSnapshot = "snapshot"
	kindRestore  = "restore"
	// kindCreate is a new server receiving its files from a snapshot.
	kindCreate = "create"
	// kindDeleteServer is a server being snapshotted one last time and then
	// deleted.
	kindDeleteServer = "delete-server"
)

// rewrites reports whether an operation of this kind replaces the server's
// files, so that nothing else may touch the server while it runs.
//
// A server on its way to being deleted counts: nothing is rewritten, but a
// change made after its last snapshot would be lost with it. So does one
// being moved, whose files are put back on another node.
func rewrites(kind string) bool {
	return kind == kindRestore || kind == kindCreate || kind == kindDeleteServer || kind == kindMove
}

// operations decides who may touch a server or a snapshot at a time. One
// lock covers every question it answers, so that "nothing is running, so I
// may" and "now something is running" cannot both be true.
type operations struct {
	mu      sync.Mutex
	running map[string]*operation
	// changes counts requests that are in the middle of changing a server.
	changes map[string]int
	// deleting holds snapshots that are being removed, by namespace and ID.
	deleting map[string]bool
}

func (o *operations) init() {
	if o.running == nil {
		o.running, o.changes, o.deleting = map[string]*operation{}, map[string]int{}, map[string]bool{}
	}
}

// begin claims a server for an operation. ok is false if it already has one,
// if a change to it is still being applied, or if the snapshot it needs is
// being deleted.
func (o *operations) begin(namespace, org, server, kind, snapshot string) (*operation, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.init()
	key := namespace + "/" + server
	if o.running[key] != nil || o.changes[key] > 0 || (snapshot != "" && o.deleting[namespace+"/"+snapshot]) {
		return nil, false
	}
	op := &operation{Kind: kind, Org: org, Server: server, Snapshot: snapshot, Stage: "Starting", StartedAt: time.Now().UTC()}
	o.running[key] = op
	return op, true
}

func (o *operations) end(namespace, server string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	delete(o.running, namespace+"/"+server)
}

// stoppable gives an operation the means to be called off.
func (o *operations) stoppable(op *operation, stop context.CancelFunc) {
	o.mu.Lock()
	defer o.mu.Unlock()
	op.stop = stop
}

// callOff stops the operation running on a server if it is of the given kind
// and can be stopped. The operation still has to wind down: it is over when
// current no longer returns it.
func (o *operations) callOff(namespace, server, kind string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	op := o.running[namespace+"/"+server]
	if op == nil || op.Kind != kind || op.stop == nil {
		return false
	}
	op.stop()
	return true
}

// change admits a request that is about to change a server, unless an
// operation of one of the given kinds owns it. The caller must call release
// when the change has been made; until then no operation can start.
func (o *operations) change(namespace, server string, blockedBy ...string) (release func(), blocker *operation) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.init()
	key := namespace + "/" + server
	if op := o.running[key]; op != nil && slices.Contains(blockedBy, op.Kind) {
		copied := *op
		return nil, &copied
	}
	o.changes[key]++
	return func() {
		o.mu.Lock()
		defer o.mu.Unlock()
		if o.changes[key]--; o.changes[key] <= 0 {
			delete(o.changes, key)
		}
	}, nil
}

// remove admits the deletion of a snapshot, unless a restore is using it.
// Until release is called no restore to that snapshot can start.
// heldIn names a server whose files are to be put back from this snapshot,
// as one whose restore or move was cut short says on itself, or "". Such a
// snapshot is all there may be of them.
func (s *Server) heldIn(ctx context.Context, namespace, snapshot string) (string, error) {
	var list avsysv1.MinecraftServerList
	// Not knowing is not taken as free: it may be the only copy of a
	// server's files.
	if err := s.Client.List(ctx, &list, client.InNamespace(namespace)); err != nil {
		return "", &Error{Status: http.StatusServiceUnavailable, Code: "unavailable", Message: "Could not see whether a server is waiting for its files from this snapshot, so nothing was deleted. Try again."}
	}
	for _, mc := range list.Items {
		if mc.Annotations[avsysv1.AnnotationRestoring] == snapshot {
			return mc.Name, nil
		}
	}
	return "", nil
}

// carry records the snapshot an operation is carrying a server's files in,
// which may not be deleted until it is over. ok is false if it is being
// deleted already.
func (o *operations) carry(namespace string, op *operation, snapshot string) (ok bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.init()
	if o.deleting[namespace+"/"+snapshot] {
		return false
	}
	op.Snapshot = snapshot
	return true
}

func (o *operations) remove(namespace, snapshot string) (release func(), ok bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.init()
	for key, op := range o.running {
		if strings.HasPrefix(key, namespace+"/") && op.Snapshot == snapshot {
			return nil, false
		}
	}
	key := namespace + "/" + snapshot
	if o.deleting[key] {
		return nil, false
	}
	o.deleting[key] = true
	return func() {
		o.mu.Lock()
		defer o.mu.Unlock()
		delete(o.deleting, key)
	}, true
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
		if strings.HasPrefix(key, namespace+"/") {
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
	Kind   string `json:"kind"`
	Org    string `json:"org"`
	Server string `json:"server"`
	Error  string `json:"error,omitempty"`
	// Scheduled is set for the daily snapshots, which nobody asked for just
	// now and nobody needs telling about unless they fail.
	Scheduled bool `json:"scheduled,omitempty"`
	// CalledOff is set when the operation ended because someone stopped it,
	// which is not a failure anyone needs telling about.
	CalledOff bool             `json:"calledOff,omitempty"`
	Snapshot  *backup.Snapshot `json:"snapshot,omitempty"`
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

var (
	errRestoring    = &Error{Status: http.StatusConflict, Code: "restoring", Message: "This server is being restored. Wait for that to finish."}
	errDeleting     = &Error{Status: http.StatusConflict, Code: "deleting", Message: "This server is being deleted."}
	errMoving       = &Error{Status: http.StatusConflict, Code: "moving", Message: "This server is being moved to another node. Wait for that to finish."}
	errCreating     = &Error{Status: http.StatusConflict, Code: "creating", Message: "This server is still being made from a snapshot. Wait for that to finish."}
	errSnapshotting = &Error{Status: http.StatusConflict, Code: "busy", Message: "A snapshot of this server is being taken. Try again when it has finished."}
	// errRestoreIncomplete is returned to anything that would run the game
	// on files a restore started on and did not finish.
	errRestoreIncomplete = &Error{Status: http.StatusConflict, Code: "restore_incomplete",
		Message: "A restore of this server did not finish, so its files may be incomplete. Restore it again before starting it; the state from before that restore is in its newest snapshot."}
	// errCreateIncomplete is the same for a server that never received all of
	// the files it was to be made from.
	errCreateIncomplete = &Error{Status: http.StatusConflict, Code: "create_incomplete",
		Message: "This server was being made from a snapshot and that did not finish, so its files are incomplete. Delete it and make it again."}
)

// changing admits a request that changes a server. While a restore owns the
// server nothing else may touch it: starting the game would run it on a
// half-restored world, and most other changes replace the pod the restore is
// working through. A snapshot only needs the game not to be started or
// restarted under it, which callers ask for with alsoDuringSnapshot.
//
// The caller must call release once its change has been made.
func (s *Server) changing(mc *avsysv1.MinecraftServer, alsoDuringSnapshot bool) (release func(), err error) {
	release, err = s.admit(mc, alsoDuringSnapshot)
	if err != nil {
		return nil, err
	}
	// A move cut short holds it as one under way does, until it is finished.
	if err := halfMoved(mc); err != nil {
		release()
		return nil, err
	}
	return release, nil
}

// admit is changing without holding off a server whose move did not
// finish, for what such a server may still have done to it: deleting it.
func (s *Server) admit(mc *avsysv1.MinecraftServer, alsoDuringSnapshot bool) (release func(), err error) {
	kinds := []string{kindRestore, kindCreate, kindDeleteServer, kindMove}
	if alsoDuringSnapshot {
		kinds = append(kinds, kindSnapshot)
	}
	release, blocker := s.ops.change(mc.Namespace, mc.Name, kinds...)
	if blocker == nil {
		return release, nil
	}
	return nil, heldBy(blocker.Kind)
}

// heldBy is the refusal given while an operation of that kind owns a server.
func heldBy(kind string) error {
	switch kind {
	case kindRestore:
		return errRestoring
	case kindCreate:
		return errCreating
	case kindDeleteServer:
		return errDeleting
	case kindMove:
		return errMoving
	}
	return errSnapshotting
}

// restoreUnfinished reports whether a restore started on this server's files
// and never completed.
func restoreUnfinished(mc *avsysv1.MinecraftServer) bool {
	_, marked := mc.Annotations[avsysv1.AnnotationRestoring]
	return marked
}

// createUnfinished reports whether the server was being made from a snapshot
// and has not received all of its files.
func createUnfinished(mc *avsysv1.MinecraftServer) bool {
	_, marked := mc.Annotations[avsysv1.AnnotationCreating]
	return marked
}

// unfinished is the reason the game may not be run on this server's files, or
// nil when it may.
func unfinished(mc *avsysv1.MinecraftServer) error {
	switch {
	case createUnfinished(mc):
		return errCreateIncomplete
	case restoreUnfinished(mc):
		return errRestoreIncomplete
	}
	return nil
}

// settled reports whether a server is in a state its files can be copied in:
// running, where the game can be told to hold its saves, or with no game
// running at all. In between, a game is starting or shutting down and
// writing as it pleases.
func settled(mc *avsysv1.MinecraftServer) error {
	switch mc.Status.Phase {
	case avsysv1.PhaseRunning, avsysv1.PhaseStopped, avsysv1.PhaseSleeping:
		return nil
	}
	state := strings.ToLower(string(mc.Status.Phase))
	if state == "" {
		state = "not ready"
	}
	return &Error{Status: http.StatusConflict, Code: "unsettled", Message: fmt.Sprintf("This server is %s. Wait until it is running or stopped, then try again.", state)}
}

// snapshotRecord is stored inside every snapshot. It is what lets a server be
// recreated from the repository alone.
//
// It holds nothing that differs from one snapshot to the next unless the
// server did: no time, no panel version. A scheduled snapshot is skipped
// when the files are unchanged, and this is one of the files.
type snapshotRecord struct {
	Format int                         `json:"format"`
	Org    string                      `json:"org"`
	Server string                      `json:"server"`
	Spec   avsysv1.MinecraftServerSpec `json:"spec"`
	// AutoOps is the note of which operators in the files the panel made
	// for their role, as it stood when the snapshot was taken. It goes
	// back on with the files, so that someone who was an operator then and
	// has no claim to it now is taken off again.
	AutoOps string `json:"autoOps,omitempty"`
	// AutoWhitelist is the same for the whitelist.
	AutoWhitelist string `json:"autoWhitelist,omitempty"`
}

// roleListNotes is the record's notes by the annotation each belongs on.
func (r *snapshotRecord) roleListNotes() map[string]string {
	return map[string]string{avsysv1.AnnotationAutoOps: r.AutoOps, avsysv1.AnnotationAutoWhitelist: r.AutoWhitelist}
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
	// What an interrupted restore left behind is not something to keep as if
	// it were a state of the server. Only another restore gets it out of that.
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
	// What the caller is told has started is a copy: the original is updated
	// by the work as it goes.
	started := *op
	work := func(ctx context.Context) (*backup.Snapshot, error) {
		ctx, cancel := context.WithTimeout(ctx, operationTimeout)
		defer cancel()
		snap, err := s.takeSnapshot(ctx, a.Org.Slug, mc, op, backup.TriggerManual, u.Username)
		s.finishOperation(ctx, a.Namespace, mc.Name, kindSnapshot, snap, snap, err)
		if err == nil {
			s.audit(ctx, u, a.Org, "snapshot.create", mc.Name, snap.ID)
		}
		return snap, err
	}
	if r.URL.Query().Get("wait") == "true" {
		// The caller hanging up does not stop the work, but the work still
		// has a limit of its own.
		snap, err := work(context.WithoutCancel(r.Context()))
		if err != nil {
			return operationFailed(err)
		}
		writeJSON(w, http.StatusCreated, snap)
		return nil
	}
	go func() { _, _ = work(context.Background()) }()
	writeJSON(w, http.StatusAccepted, started)
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
	// The caller read the server before it held the claim, and it may have
	// been started, stopped or changed since. From here it cannot be.
	mc, err := s.fresh(ctx, mc)
	if err != nil {
		return nil, err
	}
	if err := settled(mc); err != nil {
		return nil, err
	}
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
		// Said in the workspace too, where the mc helper turns away the
		// game's commands for saving while it lasts. Taken away again before
		// saving is switched back on (deferred calls run last first).
		if err := runIn(ws, "mkdir -p /tmp/avsys && touch "+savingPausedMark); err != nil {
			s.Log.Warn("could not say in the workspace that saving is held", "server", mc.Name, "error", err)
		}
		defer func() {
			if err := runIn(ws, "rm -f "+savingPausedMark); err != nil {
				s.Log.Warn("could not take away the note in the workspace that saving is held", "server", mc.Name, "error", err)
			}
		}()
		// Without both of these the copy could miss what the game still holds
		// in memory, or catch a file half written. That is not a snapshot
		// anyone should restore from, so there is none.
		if _, err := s.rconFor(ctx, mc, "save-off", 15*time.Second); err != nil {
			return nil, fmt.Errorf("the game did not answer, so its world could not be saved first (stop the server to snapshot it as it is): %w", err)
		}
		if _, err := s.rconFor(ctx, mc, "save-all flush", 5*time.Minute); err != nil {
			return nil, fmt.Errorf("the game did not finish writing its world out: %w", err)
		}
	}

	// The game restarting while its files are copied, on its own or because
	// someone restarted it, would leave a copy that mixes two states. Its
	// container is looked at before and after to tell.
	before := s.gameInstance(ctx, mc)

	version, loader := softwareOf(&mc.Spec)
	// When the server is snapshotted is not part of what it was: changing
	// that alone should not count as the server having changed.
	recorded := *mc.Spec.DeepCopy()
	recorded.Backups = nil
	meta := backup.Meta{
		Server: mc.Name, Trigger: trigger, By: by, Version: version, Loader: loader,
		Record: snapshotRecord{Format: 1, Org: org, Server: mc.Name, Spec: recorded, AutoOps: mc.Annotations[avsysv1.AnnotationAutoOps], AutoWhitelist: mc.Annotations[avsysv1.AnnotationAutoWhitelist]},
		// A scheduled snapshot of a server nothing has happened to is not
		// taken, and neither is a final one or one to copy from: the last
		// one is as good.
		SkipUnchanged: trigger == backup.TriggerScheduled || trigger == backup.TriggerFinal || trigger == backup.TriggerCopy,
		// One that carries a server to another node is all there will be of
		// its files, its workspace home among them, and is always taken.
		Whole: trigger == backup.TriggerMove,
	}
	s.setOperation(mc.Namespace, op, "Copying files", 0)
	last := time.Time{}
	snap, err := s.Backups.Snapshot(ctx, org, ws, meta, func(p backup.Progress) {
		// restic reports many times a second; pages do not need that.
		if time.Since(last) < 500*time.Millisecond {
			return
		}
		last = time.Now()
		s.setOperation(mc.Namespace, op, "Copying files", p.Percent)
	})
	if err != nil {
		if errors.Is(err, io.EOF) || strings.Contains(err.Error(), "EOF") || strings.Contains(err.Error(), "closed") {
			return nil, fmt.Errorf("the connection to the server's files was lost while they were being copied, which is what happens when the server is stopped or started meanwhile; no snapshot was kept: %w", err)
		}
		return nil, err
	}
	if after := s.gameInstance(ctx, mc); after != before {
		// The copy cannot be trusted and must not be offered for a restore.
		if err := s.Backups.Delete(context.WithoutCancel(ctx), org, snap.ID); err != nil {
			s.Log.Error("could not remove a snapshot taken across a restart of the game", "server", mc.Name, "snapshot", snap.ID, "error", err)
		}
		return nil, errors.New("the game restarted while its files were being copied, so the snapshot was discarded; take it again")
	}
	return snap, nil
}

// fresh returns the server as it is now.
func (s *Server) fresh(ctx context.Context, mc *avsysv1.MinecraftServer) (*avsysv1.MinecraftServer, error) {
	now := &avsysv1.MinecraftServer{}
	if err := s.Client.Get(ctx, client.ObjectKeyFromObject(mc), now); err != nil {
		return nil, err
	}
	return now, nil
}

// gameInstance identifies the running instance of a server's game container:
// it changes whenever that container is restarted or its pod replaced. It is
// empty for a server with no game running.
func (s *Server) gameInstance(ctx context.Context, mc *avsysv1.MinecraftServer) string {
	pod := &corev1.Pod{}
	if err := s.Client.Get(ctx, client.ObjectKey{Namespace: mc.Namespace, Name: mc.Name}, pod); err != nil {
		return ""
	}
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.Name == controller.MinecraftContainer {
			return fmt.Sprintf("%s/%d/%s", pod.UID, cs.RestartCount, cs.ContainerID)
		}
	}
	return string(pod.UID)
}

// markRestoring notes on a server that a restore to the given snapshot is
// rewriting its files, or with an empty ID that it no longer is.
func (s *Server) markRestoring(ctx context.Context, key client.ObjectKey, snapshot string) error {
	mc := &avsysv1.MinecraftServer{}
	if err := s.Client.Get(ctx, key, mc); err != nil {
		return err
	}
	orig := mc.DeepCopy()
	if snapshot == "" {
		delete(mc.Annotations, avsysv1.AnnotationRestoring)
		// A move left to be finished by a restore is finished by it.
		delete(mc.Annotations, avsysv1.AnnotationMoving)
		delete(mc.Annotations, avsysv1.AnnotationMoveWas)
	} else {
		if mc.Annotations == nil {
			mc.Annotations = map[string]string{}
		}
		mc.Annotations[avsysv1.AnnotationRestoring] = snapshot
	}
	return s.Client.Patch(ctx, mc, client.MergeFrom(orig))
}

// savingPausedMark is the file in a server's workspace that is there while a
// snapshot holds the game's saves. The mc helper there will not send the
// game's commands for saving while it is.
const savingPausedMark = "/tmp/avsys/saving-paused"

// runIn runs a fixed command in a workspace and waits for it.
func runIn(ws *ssh.Client, command string) error {
	session, err := ws.NewSession()
	if err != nil {
		return err
	}
	defer session.Close()
	return session.Run(command)
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

// ResumeSaving switches saving back on for every game left holding its
// saves by a snapshot that was cut short: by the panel being stopped in the
// middle of one, or by the game not answering when it was over. It keeps
// looking until ctx ends, because a game that could not be reached now may be
// reachable in a minute, and one left with saving off loses everything at its
// next stop.
func (s *Server) ResumeSaving(ctx context.Context) {
	for {
		s.resumeSaving(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Minute):
		}
	}
}

func (s *Server) resumeSaving(ctx context.Context) {
	var all avsysv1.MinecraftServerList
	if err := s.Client.List(ctx, &all); err != nil {
		s.Log.Error("looking for games left with saving paused", "error", err)
		return
	}
	for i := range all.Items {
		mc := &all.Items[i]
		// Whatever is running right now set its own notes and will clear them.
		if s.ops.current(mc.Namespace, mc.Name) != nil {
			continue
		}
		// A restore with nobody running it was cut short, by this panel's
		// predecessor being stopped in the middle of it. The note stays, and
		// keeps the game from being started; the server is made to say why.
		if restoreUnfinished(mc) && (mc.Status.Snapshots == nil || mc.Status.Snapshots.Error == "") {
			orig := mc.DeepCopy()
			if mc.Status.Snapshots == nil {
				mc.Status.Snapshots = &avsysv1.SnapshotStatus{}
			}
			mc.Status.Snapshots.Error = "A restore was interrupted when the panel restarted, so this server's files may be incomplete. Restore it again before starting it; the state from before is in its newest snapshot."
			if createUnfinished(mc) {
				mc.Status.Snapshots.Error = "This server was being made from a snapshot when the panel restarted, so its files are incomplete. Delete it and make it again."
			}
			if err := s.Client.Status().Patch(ctx, mc, client.MergeFrom(orig)); err != nil {
				s.Log.Error("could not report an interrupted restore", "server", mc.Name, "namespace", mc.Namespace, "error", err)
			} else {
				s.Log.Warn("found a restore that was interrupted", "server", mc.Name, "namespace", mc.Namespace)
			}
		}
		// A move cut short before it let go of the server's files has not
		// touched them: the server is put back as it was.
		if was, ok := mc.Annotations[avsysv1.AnnotationMoveWas]; ok && mc.Annotations[avsysv1.AnnotationMoving] == "" && !restoreUnfinished(mc) {
			if err := s.putBack(ctx, client.ObjectKeyFromObject(mc), was == string(avsysv1.StateRunning)); err != nil {
				s.Log.Error("could not put back a server whose move was cut short; trying again in a minute", "server", mc.Name, "namespace", mc.Namespace, "error", err)
			} else {
				s.Log.Warn("put back a server whose move was cut short before its files were let go of", "server", mc.Name, "namespace", mc.Namespace, "state", was)
			}
		}
		if _, paused := mc.Annotations[avsysv1.AnnotationSavingPaused]; !paused {
			continue
		}
		key := client.ObjectKeyFromObject(mc)
		switch mc.Status.Phase {
		case avsysv1.PhaseRunning:
			if _, err := s.rconFor(ctx, mc, "save-on", 15*time.Second); err != nil {
				s.Log.Error("could not switch saving back on after an interrupted snapshot; trying again in a minute", "server", mc.Name, "namespace", mc.Namespace, "error", err)
				continue
			}
		case avsysv1.PhaseStopped, avsysv1.PhaseSleeping:
			// No game is running, and the next one starts with saving on.
		default:
			// Starting or stopping: the game that was told to hold its saves
			// may still be there. Looked at again in a minute.
			continue
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
//
// subject is the snapshot the operation was about. newest is the latest
// snapshot taken of the server along the way, if any: for a restore that is
// the undo point, not the snapshot restored to.
func (s *Server) finishOperation(ctx context.Context, namespace, server, kind string, subject, newest *backup.Snapshot, opErr error) {
	s.finish(ctx, namespace, server, kind, subject, newest, opErr, false)
}

// finishUnsaved ends an operation that took no snapshot and restored none,
// leaving what the server says of its snapshots as it is.
func (s *Server) finishUnsaved(ctx context.Context, namespace, server, kind string, opErr error) {
	org, _ := s.orgOfNamespace(namespace)
	done := operationDone{Kind: kind, Org: org, Server: server}
	if opErr != nil {
		done.Error = opErr.Error()
		s.Log.Error(kind+" failed", "namespace", namespace, "server", server, "error", opErr)
	}
	s.ops.end(namespace, server)
	s.Events.Publish(Event{Type: "operation.done", Namespace: namespace, Data: done})
}

// finish is finishOperation, with scheduled set for a run of the daily
// snapshot. One that completed is noted on the server as such even when it
// found nothing to save.
func (s *Server) finish(ctx context.Context, namespace, server, kind string, subject, newest *backup.Snapshot, opErr error, scheduled bool) {
	ctx = context.WithoutCancel(ctx)
	org, _ := s.orgOfNamespace(namespace)
	done := operationDone{Kind: kind, Org: org, Server: server, Snapshot: subject, Scheduled: scheduled}
	switch {
	case errors.Is(opErr, context.Canceled):
		done.Error, done.CalledOff = opErr.Error(), true
		s.Log.Info(kind+" called off", "namespace", namespace, "server", server)
	case opErr != nil:
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
		if newest != nil {
			at := metav1.NewTime(newest.Time)
			mc.Status.Snapshots.LastTime, mc.Status.Snapshots.LastID = &at, newest.ID
		}
		if scheduled && opErr == nil {
			now := metav1.Now()
			mc.Status.Snapshots.LastRun = &now
		}
		if err := s.Client.Status().Patch(ctx, mc, client.MergeFrom(orig)); err != nil {
			s.Log.Error("recording a "+kind+" on the server", "server", server, "error", err)
		}
	}
	// The operation is over before anyone is told so. A page refetches its
	// list on this event, and must not be handed the operation as running.
	s.ops.end(namespace, server)
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
	// What Java that version needs goes with it.
	dst.GameJava = src.GameJava
	dst.Modpack, dst.Mods, dst.ResourcePack = src.Modpack, src.Mods, src.ResourcePack
	dst.Properties, dst.Ops, dst.Whitelist = src.Properties, src.Ops, src.Whitelist
}

// getOperation says what is being done to a server's snapshots now, as a
// snapshot, restore or copy, or null. It is the panel's own record, quick
// to read, which a page starts from before the event stream tells it more.
func (s *Server) getOperation(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, mc, err := s.server(r, u, store.RoleViewer)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, s.ops.current(a.Namespace, mc.Name))
	return nil
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
	// A server that was never completed has no state of its own to go back
	// to, or to save first.
	if createUnfinished(mc) {
		return errCreateIncomplete
	}
	snap, err := s.Backups.Get(r.Context(), a.Org.Slug, r.PathValue("id"))
	if err != nil {
		return snapshotError(err)
	}
	if snap.Server != mc.Name {
		return errBadRequest("That snapshot is of %s. A server can only be restored to its own snapshots.", snap.Server)
	}
	// A move that did not finish is finished by its own snapshot, which has
	// the workspace home too: any other would leave that out.
	if err := heldForMove(mc, snap.ID); err != nil {
		return err
	}
	op, ok := s.ops.begin(a.Namespace, a.Org.Slug, mc.Name, kindRestore, snap.ID)
	if !ok {
		return errBusy
	}
	started := *op
	work := func(ctx context.Context) error {
		ctx, cancel := context.WithTimeout(ctx, operationTimeout)
		defer cancel()
		undo, err := s.restore(ctx, a.Org.Slug, mc, snap, op, u.Username)
		s.finishOperation(ctx, a.Namespace, mc.Name, kindRestore, snap, undo, err)
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
	go func() { _ = work(context.Background()) }()
	writeJSON(w, http.StatusAccepted, started)
	return nil
}

// finishesMove says whether restoring this snapshot finishes the move it was
// taken for, which was cut short before the files were put back. Only then
// does the workspace home go back too. Any other restore of it, as of any
// snapshot, leaves the home as it is now: tools put there since are not
// taken away.
func finishesMove(mc *avsysv1.MinecraftServer, snap *backup.Snapshot) bool {
	return snap.Trigger == backup.TriggerMove && mc.Annotations[avsysv1.AnnotationMoving] != "" && mc.Annotations[avsysv1.AnnotationRestoring] == snap.ID
}

// restore returns the snapshot it took of the state it replaced, once there
// is one, whether or not the rest then succeeded.
func (s *Server) restore(ctx context.Context, org string, mc *avsysv1.MinecraftServer, snap *backup.Snapshot, op *operation, by string) (undo *backup.Snapshot, err error) {
	key := client.ObjectKeyFromObject(mc)
	// As it is now that the claim is held, not as the request first saw it.
	if mc, err = s.fresh(ctx, mc); err != nil {
		return nil, err
	}
	// Checked again now that the claim is held: it may have changed.
	if err := heldForMove(mc, snap.ID); err != nil {
		return nil, err
	}
	whole := finishesMove(mc, snap)
	// Finishing a move puts it back as it was before the move stopped it.
	wasRunning := mc.Spec.State != avsysv1.StateStopped || (whole && mc.Annotations[avsysv1.AnnotationMoveWas] == string(avsysv1.StateRunning))

	// What a snapshot holds of the server's settings is read before anything
	// is touched, so that an unreadable snapshot stops here.
	record, err := s.recordOf(ctx, org, snap.ID)
	if err != nil {
		return nil, err
	}
	// The settings a restore applies get the same checks as settings typed
	// into the panel, before anything is touched.
	restored := mc.Spec.DeepCopy()
	restoreSoftware(restored, &record.Spec)
	if err := s.validateSpec(ctx, mc.Namespace, mc.Name, restored); err != nil {
		return nil, fmt.Errorf("the snapshot's settings are not valid here: %w", err)
	}

	s.setOperation(mc.Namespace, op, "Saving the current state first", 0)
	undo, err = s.takeSnapshot(ctx, org, mc, op, backup.TriggerPreRestore, by)
	if err != nil {
		return nil, fmt.Errorf("the current state could not be saved first, so nothing was changed: %w", err)
	}

	s.setOperation(mc.Namespace, op, "Stopping the server", 0)
	// Its pod is made again whether or not it was running, so that a shell
	// or file transfer already open to its files ends: what it wrote could
	// land among the restored ones. New ones wait for the restore.
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	if err := s.stopReplacing(ctx, key, stamp, nil); err != nil {
		return undo, err
	}
	if err := s.waitRestarted(ctx, key, stamp, stopWait); err != nil {
		return undo, fmt.Errorf("the server did not stop: %w", err)
	}
	if err := s.waitPhase(ctx, key, avsysv1.PhaseStopped, stopWait); err != nil {
		return undo, fmt.Errorf("the server did not stop: %w", err)
	}

	// Stopping replaces the pod with one that has only the workspace. That
	// pod may take a while to be scheduled and have its volume attached.
	ws, err := s.dialWorkspaceWithin(ctx, mc.Namespace, mc.Name, 5*time.Minute)
	if err != nil {
		return undo, fmt.Errorf("the server was stopped, but its files did not become reachable, so nothing was restored: %w", err)
	}
	defer ws.Close()
	// From here the files are in between two states until the restore is
	// done. That is written on the server itself, where a panel that
	// replaces this one will find it: the game is not started on such files.
	if err := s.markRestoring(ctx, key, snap.ID); err != nil {
		return undo, err
	}
	s.setOperation(mc.Namespace, op, "Restoring files", 0)
	last := time.Time{}
	put := s.Backups.Restore
	if whole {
		put = s.Backups.RestoreWhole
	}
	err = put(ctx, org, ws, snap.ID, func(p backup.Progress) {
		if time.Since(last) < 500*time.Millisecond {
			return
		}
		last = time.Now()
		s.setOperation(mc.Namespace, op, "Restoring files", p.Percent)
	})
	// The picture may have come with the files, or gone with them. What was
	// read before, perhaps from an empty volume, is not kept.
	s.serverIcons.forget(mc.Namespace + "/" + mc.Name)
	if err != nil {
		// The server is left stopped: its files are in an unknown state and
		// starting the game on them could make it worse.
		return undo, fmt.Errorf("restoring files failed and the server was left stopped; the state from before is in the newest snapshot: %w", err)
	}

	// The settings go on before the mark comes off. With the mark in place
	// the game is not started, so a server whose files are back but whose
	// settings could not be applied stays held, and says why, instead of
	// starting on a version or mod list that does not match its files.
	s.setOperation(mc.Namespace, op, "Applying settings", 100)
	err = s.patchSpec(ctx, key, func(spec *avsysv1.MinecraftServerSpec) {
		restoreSoftware(spec, &record.Spec)
		if wasRunning {
			spec.State = avsysv1.StateRunning
		}
	})
	if err != nil {
		return undo, fmt.Errorf("the files were restored, but the settings that go with them could not be applied; restore again: %w", err)
	}
	// The operators in the files are the ones of then, and so is the note
	// of which of them the panel made.
	if err := s.noteRoleLists(ctx, key, record.roleListNotes()); err != nil {
		return undo, fmt.Errorf("the files and settings were restored, but the note of who was made an operator could not be put back; restore again: %w", err)
	}
	if err := s.markRestoring(ctx, key, ""); err != nil {
		return undo, fmt.Errorf("the files and settings were restored, but the server could not be released to start; restore again: %w", err)
	}
	return undo, nil
}

// recordOf reads what a snapshot holds of its server's settings.
func (s *Server) recordOf(ctx context.Context, org, id string) (*snapshotRecord, error) {
	raw, err := s.Backups.Record(ctx, org, id)
	if err != nil {
		return nil, fmt.Errorf("reading the snapshot's record of the server: %w", err)
	}
	record := &snapshotRecord{}
	if err := json.Unmarshal(raw, record); err != nil {
		return nil, fmt.Errorf("the snapshot's record of the server is unreadable: %w", err)
	}
	return record, nil
}

// dialWorkspaceWithin keeps trying to reach a server's workspace for as long
// as it may reasonably take a new pod to start.
func (s *Server) dialWorkspaceWithin(ctx context.Context, namespace, server string, limit time.Duration) (*ssh.Client, error) {
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	for {
		ws, err := s.Gateway.DialWorkspace(ctx, namespace, server)
		if err == nil {
			return ws, nil
		}
		select {
		case <-ctx.Done():
			return nil, err
		case <-time.After(2 * time.Second):
		}
	}
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

// snapshotByID finds the snapshot an ID, or the start of one, names, if it
// names exactly one.
func snapshotByID(list []backup.Snapshot, id string) *backup.Snapshot {
	if len(id) < 8 {
		return nil
	}
	var found *backup.Snapshot
	for i := range list {
		if strings.HasPrefix(list[i].ID, id) {
			if found != nil {
				return nil
			}
			found = &list[i]
		}
	}
	return found
}

// maxSnapshotsAtOnce is how many snapshots one request may delete.
const maxSnapshotsAtOnce = 500

// deleteSnapshots removes several snapshots at once: all of them, or, if any
// is unknown or in use, none. They go in one pass over the repository,
// where deleting them one by one would go through all of it for each.
func (s *Server) deleteSnapshots(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, err := s.org(r, u, store.RoleAdmin)
	if err != nil {
		return err
	}
	if !s.Backups.Available() {
		return errNoBackups
	}
	var in struct{ IDs []string }
	if err := decode(r, &in); err != nil {
		return err
	}
	if len(in.IDs) == 0 {
		return errBadRequest("Name the snapshots to delete: send {\"ids\": [...]}.")
	}
	if len(in.IDs) > maxSnapshotsAtOnce {
		return errBadRequest("At most %d snapshots can be deleted at once.", maxSnapshotsAtOnce)
	}
	// Looked up in one listing, not one for each.
	list, err := s.Backups.List(r.Context(), a.Org.Slug)
	if err != nil {
		return snapshotError(err)
	}
	snaps := make([]*backup.Snapshot, 0, len(in.IDs))
	seen := map[string]bool{}
	for _, id := range in.IDs {
		snap := snapshotByID(list, id)
		if snap == nil {
			return &Error{Status: http.StatusNotFound, Code: "not_found", Message: fmt.Sprintf("There is no snapshot %q. Nothing was deleted.", id)}
		}
		if !seen[snap.ID] {
			seen[snap.ID] = true
			snaps = append(snaps, snap)
		}
	}
	ids := make([]string, len(snaps))
	for i, snap := range snaps {
		release, ok := s.ops.remove(a.Namespace, snap.ID)
		if !ok {
			return &Error{Status: http.StatusConflict, Code: "in_use", Message: fmt.Sprintf("A server is being restored to the snapshot of %s from %s. Nothing was deleted; wait for that to finish.", snap.Server, snap.Time.Format("2 Jan 15:04"))}
		}
		defer release()
		if held, err := s.heldIn(r.Context(), a.Namespace, snap.ID); err != nil {
			return err
		} else if held != "" {
			return &Error{Status: http.StatusConflict, Code: "in_use", Message: fmt.Sprintf("%s is waiting for its files to be put back from the snapshot of %s from %s. Nothing was deleted; restore it first.", held, snap.Server, snap.Time.Format("2 Jan 15:04"))}
		}
		ids[i] = snap.ID
	}
	if err := s.Backups.DeleteMany(context.WithoutCancel(r.Context()), a.Org.Slug, ids); err != nil {
		return snapshotError(err)
	}
	servers := map[string]bool{}
	for _, snap := range snaps {
		s.audit(r.Context(), u, a.Org, "snapshot.delete", snap.Server, snap.ID)
		if !servers[snap.Server] {
			servers[snap.Server] = true
			s.Events.Publish(Event{Type: "operation.done", Namespace: a.Namespace, Data: operationDone{Kind: "delete", Org: a.Org.Slug, Server: snap.Server}})
		}
	}
	writeJSON(w, http.StatusOK, map[string]int{"deleted": len(snaps)})
	return nil
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
	release, ok := s.ops.remove(a.Namespace, snap.ID)
	if !ok {
		return &Error{Status: http.StatusConflict, Code: "in_use", Message: "A server is being restored to this snapshot. Wait for that to finish."}
	}
	defer release()
	if held, err := s.heldIn(r.Context(), a.Namespace, snap.ID); err != nil {
		return err
	} else if held != "" {
		return &Error{Status: http.StatusConflict, Code: "in_use", Message: fmt.Sprintf("%s is waiting for its files to be put back from this snapshot. Restore it first.", held)}
	}
	if err := s.Backups.Delete(context.WithoutCancel(r.Context()), a.Org.Slug, snap.ID); err != nil {
		return snapshotError(err)
	}
	s.audit(r.Context(), u, a.Org, "snapshot.delete", snap.Server, snap.ID)
	s.Events.Publish(Event{Type: "operation.done", Namespace: a.Namespace, Data: operationDone{Kind: "delete", Org: a.Org.Slug, Server: snap.Server}})
	w.WriteHeader(http.StatusNoContent)
	return nil
}
