package api

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
	"github.com/GT-Drehmal/avSYS/internal/backup"
	"github.com/GT-Drehmal/avSYS/internal/controller"
	"github.com/GT-Drehmal/avSYS/internal/store"
)

// kindMove is a server being moved to another node.
const kindMove = "move"

// placementView is where a server runs and what moving it involves.
type placementView struct {
	// Node is the node it is on, or is recorded for.
	Node string `json:"node,omitempty"`
	// Local says its files are on that node's own disk, so that moving it
	// carries them over in a snapshot.
	Local bool `json:"local"`
	// Labels are the node labels it is kept to, set outside the panel; such
	// a server is not moved by the panel.
	Labels map[string]string `json:"labels,omitempty"`
}

func nodeOf(mc *avsysv1.MinecraftServer) string {
	if mc.Status.Node != "" {
		return mc.Status.Node
	}
	return mc.Annotations[avsysv1.AnnotationNode]
}

func (s *Server) getPlacement(w http.ResponseWriter, r *http.Request, u *store.User) error {
	_, mc, err := s.server(r, u, store.RoleAdmin)
	if err != nil {
		return err
	}
	local, err := controller.VolumeNode(r.Context(), s.nodeReader(), mc)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, placementView{Node: nodeOf(mc), Local: local != "", Labels: mc.Spec.NodeSelector})
	return nil
}

// moveServer moves a server to another node. Files on the node's own disk
// go with it through a snapshot: it is stopped, snapshotted, its volume is
// made again on the new node and the snapshot put back there, and it is
// started again if it was running. Other files stay where they are, and the
// server is only started again on the new node.
func (s *Server) moveServer(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, mc, err := s.server(r, u, store.RoleAdmin)
	if err != nil {
		return err
	}
	var in struct {
		Node string `json:"node"`
	}
	if err := decode(r, &in); err != nil {
		return err
	}
	if len(mc.Spec.NodeSelector) > 0 {
		return errBadRequest("This server is kept to nodes labelled %s, set outside the panel.", labelsOf(mc.Spec.NodeSelector))
	}
	if in.Node == "" {
		return errBadRequest("Say which node to move it to.")
	}
	// Where it runs, and where it is placed: during a move over shared
	// storage that did not finish its pod may still be where it was, and
	// it can be moved back there.
	if placed := mc.Annotations[avsysv1.AnnotationNode]; in.Node == nodeOf(mc) && (placed == "" || placed == in.Node || mc.Status.Node == "") {
		return errBadRequest("It is on %s already.", in.Node)
	}
	if err := unfinished(mc); err != nil {
		return err
	}
	// Checked here for a quick answer, and again as it is placed.
	if err := s.checkNode(r.Context(), in.Node, mc.Spec.Resources.Memory); err != nil {
		return err
	}
	from, err := controller.VolumeNode(r.Context(), s.nodeReader(), mc)
	if err != nil {
		return err
	}
	// Files on a node's own disk that are on that node already.
	if from == in.Node {
		return errBadRequest("It is on %s already.", in.Node)
	}
	// Files not on the node's own disk stay where they are, and the new node
	// has to be one their volume can be used on.
	if from == "" {
		fits, err := controller.VolumeFits(r.Context(), s.nodeReader(), mc, in.Node)
		if err != nil {
			return err
		}
		if !fits {
			return errBadRequest("%s cannot use this server's storage. Choose another node.", in.Node)
		}
	}
	if from != "" && !s.Backups.Available() {
		return &Error{Status: http.StatusServiceUnavailable, Code: "backups_unavailable", Message: "Moving needs snapshots, which are not set up on this panel."}
	}
	op, ok := s.ops.begin(a.Namespace, a.Org.Slug, mc.Name, kindMove, "")
	if !ok {
		return errBusy
	}
	started := *op
	work := func(ctx context.Context) error {
		ctx, cancel := context.WithTimeout(ctx, operationTimeout)
		defer cancel()
		snap, err := s.move(ctx, a.Org.Slug, mc, in.Node, from, op, u.Username)
		if from == "" {
			// Nothing was snapshotted or restored: what the server says of
			// its snapshots is left as it is.
			s.finishUnsaved(ctx, a.Namespace, mc.Name, kindMove, err)
		} else {
			s.finishOperation(ctx, a.Namespace, mc.Name, kindMove, snap, snap, err)
		}
		if err == nil {
			s.audit(ctx, u, a.Org, "server.move", mc.Name, "to "+in.Node)
		}
		return err
	}
	if r.URL.Query().Get("wait") == "true" {
		if err := work(context.WithoutCancel(r.Context())); err != nil {
			return operationFailed(err)
		}
		now, err := s.fresh(r.Context(), mc)
		if err != nil {
			return err
		}
		writeJSON(w, http.StatusOK, s.view(now))
		return nil
	}
	go func() { _ = work(context.Background()) }()
	writeJSON(w, http.StatusAccepted, started)
	return nil
}

// move does the moving, holding the server's claim. from is the node its
// volume is tied to, if it is. The snapshot it returns, once it has one, has
// the server's files whatever happened after.
func (s *Server) move(ctx context.Context, org string, mc *avsysv1.MinecraftServer, target, from string, op *operation, by string) (snap *backup.Snapshot, err error) {
	key := client.ObjectKeyFromObject(mc)
	if mc, err = s.fresh(ctx, mc); err != nil {
		return nil, err
	}
	// As it is now that the claim is held: a restore that failed since the
	// request first looked leaves its files half put back.
	if err := unfinished(mc); err != nil {
		return nil, err
	}
	wasRunning := mc.Spec.State != avsysv1.StateStopped

	if from == "" {
		// Nothing to carry: placed on the new node, and its pod made again
		// there.
		s.setOperation(mc.Namespace, op, "Starting it on "+target, 0)
		if err := s.place(ctx, key, target, ""); err != nil {
			return nil, err
		}
		return nil, s.waitPodOn(ctx, key, target, 10*time.Minute)
	}

	// Stopped first, so that the snapshot has everything and nothing is
	// written after it.
	// Its pod is made again, so that every session to its files ends: none
	// can write after the snapshot, and new ones wait for the move.
	s.setOperation(mc.Namespace, op, "Stopping the server", 0)
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	if err := s.stopAnew(ctx, key, stamp); err != nil {
		return nil, err
	}
	// Whatever goes wrong before its volume is let go of, it is as it was.
	restart := func() {
		_ = s.putBack(context.WithoutCancel(ctx), key, wasRunning)
	}
	if err := s.waitRestarted(ctx, key, stamp, stopWait); err != nil {
		restart()
		return nil, fmt.Errorf("the server did not stop, so it was not moved: %w", err)
	}
	if err := s.waitPhase(ctx, key, avsysv1.PhaseStopped, stopWait); err != nil {
		restart()
		return nil, fmt.Errorf("the server did not stop, so it was not moved: %w", err)
	}
	s.setOperation(mc.Namespace, op, "Saving its files", 0)
	snap, err = s.takeSnapshot(ctx, org, mc, op, backup.TriggerMove, by)
	if err != nil {
		restart()
		return nil, fmt.Errorf("its files could not be saved, so it was not moved: %w", err)
	}
	// The snapshot is all there is of its files once the old volume goes:
	// it is not to be deleted until the move is over.
	if !s.ops.carry(mc.Namespace, op, snap.ID) {
		restart()
		return nil, fmt.Errorf("its newest snapshot, %s, is being deleted, so it was not moved", shortID(snap.ID))
	}

	// From here its files on the old node are let go of. The game is held
	// stopped until they are back, by the same mark as a restore, put on in
	// the same change: a panel that stopped between the two would leave a
	// server held for a restore that is not one, and not moving either.
	if err := s.place(ctx, key, target, snap.ID); err != nil {
		restart()
		return snap, err
	}
	s.setOperation(mc.Namespace, op, "Making its volume on "+target, 0)
	if err := s.waitPodOn(ctx, key, target, 10*time.Minute); err != nil {
		// While its volume is still on the node it was leaving, nothing is
		// lost: it is put back there as it was.
		live := context.WithoutCancel(ctx)
		if now, gerr := s.fresh(live, mc); gerr == nil {
			if still, verr := controller.VolumeNode(live, s.nodeReader(), now); verr == nil && still == from && !s.claimGoing(live, key) {
				if uerr := s.unplace(live, key, from); uerr == nil {
					// The controller reads the mark live before deleting
					// anything; one already past that look is given a
					// moment to show, and then it is not put back.
					time.Sleep(5 * time.Second)
					if !s.claimGoing(live, key) {
						restart()
						return snap, fmt.Errorf("its volume on %s was not ready, so it was put back on %s as it was: %w", target, from, err)
					}
					// Going after all: held again, to be moved on, so
					// that the game never starts on an empty volume.
					_ = s.remark(live, key, target, snap.ID)
				}
			}
		}
		return snap, fmt.Errorf("its volume on %s was not ready, so it was left stopped; its files are in snapshot %s: %w", target, shortID(snap.ID), err)
	}
	ws, err := s.dialWorkspaceWithin(ctx, mc.Namespace, mc.Name, 5*time.Minute)
	if err != nil {
		return snap, fmt.Errorf("its files on %s could not be reached, so it was left stopped; they are in snapshot %s: %w", target, shortID(snap.ID), err)
	}
	defer ws.Close()
	s.setOperation(mc.Namespace, op, "Putting its files back", 0)
	last := time.Time{}
	err = s.Backups.RestoreWhole(ctx, org, ws, snap.ID, func(p backup.Progress) {
		if time.Since(last) < 500*time.Millisecond {
			return
		}
		last = time.Now()
		s.setOperation(mc.Namespace, op, "Putting its files back", p.Percent)
	})
	s.serverIcons.forget(key.Namespace + "/" + key.Name)
	if err != nil {
		return snap, fmt.Errorf("putting its files back on %s failed, so it was left stopped; restore snapshot %s to finish: %w", target, shortID(snap.ID), err)
	}

	// The marks come off and the state goes back on in one change.
	s.setOperation(mc.Namespace, op, "Finishing", 100)
	now := &avsysv1.MinecraftServer{}
	if err := s.Client.Get(ctx, key, now); err != nil {
		return snap, err
	}
	orig := now.DeepCopy()
	delete(now.Annotations, avsysv1.AnnotationRestoring)
	delete(now.Annotations, avsysv1.AnnotationMoving)
	delete(now.Annotations, avsysv1.AnnotationMoveWas)
	if wasRunning {
		now.Spec.State = avsysv1.StateRunning
	}
	return snap, s.Client.Patch(ctx, now, client.MergeFrom(orig))
}

// place records a server on a node, checked to have room for it, one at a
// time with every other placement. Carrying its files over in a snapshot,
// the controller then takes its volume off the node it leaves, and it is
// held stopped until the snapshot is restored there; without, its pod is
// only made again.
func (s *Server) place(ctx context.Context, key client.ObjectKey, node, carry string) error {
	defer controller.LockPlacement()()
	mc := &avsysv1.MinecraftServer{}
	if err := s.Client.Get(ctx, key, mc); err != nil {
		return err
	}
	if err := s.checkNode(ctx, node, mc.Spec.Resources.Memory); err != nil {
		return err
	}
	orig := mc.DeepCopy()
	if mc.Annotations == nil {
		mc.Annotations = map[string]string{}
	}
	mc.Annotations[avsysv1.AnnotationNode] = node
	if carry != "" {
		mc.Annotations[avsysv1.AnnotationMoving] = node
		mc.Annotations[avsysv1.AnnotationRestoring] = carry
	} else {
		mc.Annotations[avsysv1.AnnotationRestart] = time.Now().UTC().Format(time.RFC3339Nano)
	}
	return s.Client.Patch(ctx, mc, client.MergeFrom(orig))
}

// claimGoing says whether a server's volume claim is being deleted, after
// which its files go however it is placed.
func (s *Server) claimGoing(ctx context.Context, key client.ObjectKey) bool {
	pvc := &corev1.PersistentVolumeClaim{}
	err := s.nodeReader().Get(ctx, client.ObjectKey{Namespace: key.Namespace, Name: controller.DataPVCName(key.Name)}, pvc)
	return err != nil || !pvc.DeletionTimestamp.IsZero()
}

// unplace puts a server whose move was given up back on the node its volume
// is still on, and releases it.
func (s *Server) unplace(ctx context.Context, key client.ObjectKey, node string) error {
	mc := &avsysv1.MinecraftServer{}
	if err := s.Client.Get(ctx, key, mc); err != nil {
		return err
	}
	orig := mc.DeepCopy()
	mc.Annotations[avsysv1.AnnotationNode] = node
	delete(mc.Annotations, avsysv1.AnnotationMoving)
	delete(mc.Annotations, avsysv1.AnnotationRestoring)
	return s.Client.Patch(ctx, mc, client.MergeFrom(orig))
}

// remark holds a server whose old volume is going as one being moved again.
func (s *Server) remark(ctx context.Context, key client.ObjectKey, node, snapshot string) error {
	mc := &avsysv1.MinecraftServer{}
	if err := s.Client.Get(ctx, key, mc); err != nil {
		return err
	}
	orig := mc.DeepCopy()
	mc.Annotations[avsysv1.AnnotationNode] = node
	mc.Annotations[avsysv1.AnnotationMoving] = node
	mc.Annotations[avsysv1.AnnotationRestoring] = snapshot
	return s.Client.Patch(ctx, mc, client.MergeFrom(orig))
}

// putBack ends a move that did not let go of the server's files: the note
// that it was moving comes off, and it is started again if it was running.
func (s *Server) putBack(ctx context.Context, key client.ObjectKey, wasRunning bool) error {
	mc := &avsysv1.MinecraftServer{}
	if err := s.Client.Get(ctx, key, mc); err != nil {
		return err
	}
	orig := mc.DeepCopy()
	delete(mc.Annotations, avsysv1.AnnotationMoveWas)
	if wasRunning {
		mc.Spec.State = avsysv1.StateRunning
	}
	return s.Client.Patch(ctx, mc, client.MergeFrom(orig))
}

// halfMoved refuses a change to a server whose move did not finish: its
// files on the new node are not all there until the move's snapshot is
// restored, which a change made now would be lost to.
func halfMoved(mc *avsysv1.MinecraftServer) error {
	node := mc.Annotations[avsysv1.AnnotationMoving]
	if node == "" {
		return nil
	}
	return &Error{Status: http.StatusConflict, Code: "move_unfinished", Message: fmt.Sprintf("Its move to %s did not finish. Restore snapshot %s from its Backups tab to finish it.", node, shortID(mc.Annotations[avsysv1.AnnotationRestoring]))}
}

// heldForMove refuses restoring a server whose move did not finish to any
// snapshot but the move's own.
func heldForMove(mc *avsysv1.MinecraftServer, snapshot string) error {
	if mc.Annotations[avsysv1.AnnotationMoving] == "" || mc.Annotations[avsysv1.AnnotationRestoring] == snapshot {
		return nil
	}
	return &Error{Status: http.StatusConflict, Code: "move_unfinished", Message: fmt.Sprintf("Its move did not finish. Restore snapshot %s first.", shortID(mc.Annotations[avsysv1.AnnotationRestoring]))}
}

// stopAnew stops a server for a move and has its pod made again, stamped,
// whether or not it was running.
func (s *Server) stopAnew(ctx context.Context, key client.ObjectKey, stamp string) error {
	return s.stopReplacing(ctx, key, stamp, func(mc *avsysv1.MinecraftServer) {
		// In the same change as the stop, so that a panel restarting before
		// the move gets further finds it and puts the server back as it was.
		mc.Annotations[avsysv1.AnnotationMoveWas] = string(mc.Spec.State)
		if mc.Spec.State == "" {
			mc.Annotations[avsysv1.AnnotationMoveWas] = string(avsysv1.StateRunning)
		}
	})
}

// stopReplacing stops a server and has its pod made again, stamped, whether
// or not it was running: every session to its files, a shell or a file
// transfer, ends with the pod it was to. mark, if given, changes the server
// in the same change.
func (s *Server) stopReplacing(ctx context.Context, key client.ObjectKey, stamp string, mark func(*avsysv1.MinecraftServer)) error {
	mc := &avsysv1.MinecraftServer{}
	if err := s.Client.Get(ctx, key, mc); err != nil {
		return err
	}
	orig := mc.DeepCopy()
	if mc.Annotations == nil {
		mc.Annotations = map[string]string{}
	}
	if mark != nil {
		mark(mc)
	}
	mc.Spec.State = avsysv1.StateStopped
	mc.Annotations[avsysv1.AnnotationRestart] = stamp
	return s.Client.Patch(ctx, mc, client.MergeFrom(orig))
}

// stopWait is how long the panel waits for a server it stops to have
// stopped: a game still loading is let finish first, for up to
// controller.LoadGrace, and then saves and stops, which a large world can
// take minutes to.
const stopWait = controller.LoadGrace + 3*time.Minute

// waitRestarted waits for a server's pod made again with this stamp to have
// its workspace up.
func (s *Server) waitRestarted(ctx context.Context, key client.ObjectKey, stamp string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		pod := &corev1.Pod{}
		err := s.nodeReader().Get(ctx, key, pod)
		if err == nil && pod.Annotations[avsysv1.AnnotationRestart] == stamp && pod.DeletionTimestamp.IsZero() && containerIsReady(pod, controller.WorkspaceContainer) {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("its pod was not made again after %s", timeout)
		case <-time.After(2 * time.Second):
		}
	}
}

// waitPodOn waits for a server's pod to be running on a node, with its
// workspace up.
func (s *Server) waitPodOn(ctx context.Context, key client.ObjectKey, node string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		pod := &corev1.Pod{}
		err := s.nodeReader().Get(ctx, key, pod)
		if err == nil && pod.Spec.NodeName == node && pod.DeletionTimestamp.IsZero() && containerIsReady(pod, controller.WorkspaceContainer) {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("its pod was not running on %s after %s", node, timeout)
		case <-time.After(2 * time.Second):
		}
	}
}

func containerIsReady(pod *corev1.Pod, name string) bool {
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.Name == name {
			return cs.Ready
		}
	}
	return false
}

// labelsOf writes node labels as k=v, k=v, in order.
func labelsOf(labels map[string]string) string {
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + "=" + labels[k]
	}
	return strings.Join(parts, ", ")
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}
