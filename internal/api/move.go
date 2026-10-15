package api

import (
	"context"
	"errors"
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
		return errBadRequest("This server is kept to the nodes labelled %s, set outside the panel. Change its node selector to move it.", labelsOf(mc.Spec.NodeSelector))
	}
	if in.Node == "" {
		return errBadRequest("Say which node to move it to.")
	}
	if in.Node == nodeOf(mc) {
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
	if from != "" && !s.Backups.Available() {
		return &Error{Status: http.StatusServiceUnavailable, Code: "backups_unavailable", Message: "Its files are on its node's own disk, and are carried over in a snapshot, which needs restic where the panel runs."}
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
		s.finishOperation(ctx, a.Namespace, mc.Name, kindMove, snap, snap, err)
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
	wasRunning := mc.Spec.State != avsysv1.StateStopped

	if from == "" {
		// Nothing to carry: placed on the new node, and its pod made again
		// there.
		s.setOperation(mc.Namespace, op, "Starting it on "+target, 0)
		if err := s.place(ctx, key, target, false); err != nil {
			return nil, err
		}
		return nil, s.waitPodOn(ctx, key, target, 10*time.Minute)
	}

	// Stopped first, so that the snapshot has everything and nothing is
	// written after it.
	s.setOperation(mc.Namespace, op, "Stopping the server", 0)
	if err := s.patchSpec(ctx, key, func(spec *avsysv1.MinecraftServerSpec) { spec.State = avsysv1.StateStopped }); err != nil {
		return nil, err
	}
	// Whatever goes wrong before its volume is let go of, it is as it was.
	restart := func() {
		if wasRunning {
			_ = s.patchSpec(context.WithoutCancel(ctx), key, func(spec *avsysv1.MinecraftServerSpec) { spec.State = avsysv1.StateRunning })
		}
	}
	if err := s.waitPhase(ctx, key, avsysv1.PhaseStopped, 5*time.Minute); err != nil {
		restart()
		return nil, fmt.Errorf("the server did not stop, so it was not moved: %w", err)
	}
	s.setOperation(mc.Namespace, op, "Saving its files", 0)
	snap, err = s.takeSnapshot(ctx, org, mc, op, backup.TriggerMove, by)
	if errors.Is(err, backup.ErrUnchanged) {
		// Its newest snapshot has everything already.
		snap, err = s.newestOf(ctx, org, mc.Name)
	}
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
	// stopped until they are back, by the same mark as a restore.
	if err := s.markRestoring(ctx, key, snap.ID); err != nil {
		restart()
		return snap, err
	}
	if err := s.place(ctx, key, target, true); err != nil {
		_ = s.markRestoring(context.WithoutCancel(ctx), key, "")
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
	err = s.Backups.Restore(ctx, org, ws, snap.ID, func(p backup.Progress) {
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
	if wasRunning {
		now.Spec.State = avsysv1.StateRunning
	}
	return snap, s.Client.Patch(ctx, now, client.MergeFrom(orig))
}

// place records a server on a node, checked to have room for it, one at a
// time with every other placement. With carry, the controller then takes its
// volume off the node it leaves; without, its pod is only made again.
func (s *Server) place(ctx context.Context, key client.ObjectKey, node string, carry bool) error {
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
	if carry {
		mc.Annotations[avsysv1.AnnotationMoving] = node
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
