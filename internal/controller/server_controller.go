// Package controller reconciles MinecraftServer resources into the pod,
// volume, service and secrets that run them.
package controller

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
)

type Config struct {
	// WorkspaceImage is the SSH sidecar that runs next to every server.
	WorkspaceImage           string
	WorkspaceImagePullPolicy corev1.PullPolicy
	// WorkspaceAuthorizedKeys is the authorized_keys content workspaces trust:
	// the gateway's own public key.
	WorkspaceAuthorizedKeys string
	// RouterPublicPort is the port players reach the router on, when not 25565.
	RouterPublicPort int
	CurseForgeAPIKey string
	// PortRange is the public ports given out to the extra ports of
	// servers that share the router's address.
	PortRange PortRange
	// PortsServiceAnnotations go on those servers' Services of extra
	// ports, as the load balancer's way of sharing the router's address.
	PortsServiceAnnotations map[string]string
	// RouterService is the panel's own Service, whose address extra ports
	// are compared with: where theirs is another, it is the one shown.
	RouterService types.NamespacedName
	// PackURL is where the panel serves uploaded resource packs, at
	// /packs/<sha1>.zip under it.
	PackURL string
}

type Reconciler struct {
	client.Client
	// Reader reads straight from the API server, for the occasional look at
	// things that are not worth caching (every pod on every node).
	Reader client.Reader
	Scheme *runtime.Scheme
	Config Config
}

func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&avsysv1.MinecraftServer{}).
		Owns(&corev1.Pod{}).
		Owns(&corev1.Service{}).
		Complete(r)
}

func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	mc := &avsysv1.MinecraftServer{}
	if err := r.Get(ctx, req.NamespacedName, mc); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !mc.DeletionTimestamp.IsZero() {
		// Everything is owned by the server and garbage collected with it.
		return ctrl.Result{}, nil
	}
	// A server made under the name of one that was just deleted finds that
	// one's pod, volume and secret still there, on their way out. They are
	// not adopted: whatever went into such a volume would be lost when its
	// removal completed. Nothing is made for the new server until they are gone.
	if left, err := r.leftovers(ctx, mc); err != nil {
		return ctrl.Result{}, fmt.Errorf("leftovers: %w", err)
	} else if left != "" {
		return ctrl.Result{RequeueAfter: 2 * time.Second}, r.waitForLeftovers(ctx, mc, left)
	}

	if err := r.ensureWorkspaceSecret(ctx, mc.Namespace); err != nil {
		return ctrl.Result{}, fmt.Errorf("workspace secret: %w", err)
	}
	if err := r.ensureRCONSecret(ctx, mc); err != nil {
		return ctrl.Result{}, fmt.Errorf("rcon secret: %w", err)
	}
	if target := mc.Annotations[avsysv1.AnnotationMoving]; target != "" {
		waiting, err := r.moveVolume(ctx, mc, target)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("moving: %w", err)
		}
		if waiting {
			return ctrl.Result{RequeueAfter: 2 * time.Second}, r.waitForMove(ctx, mc, target)
		}
	}
	if err := r.ensurePVC(ctx, mc); err != nil {
		return ctrl.Result{}, fmt.Errorf("volume: %w", err)
	}
	svc, err := r.ensureService(ctx, mc)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("service: %w", err)
	}
	if err := r.ensurePlacement(ctx, mc); err != nil {
		return ctrl.Result{}, fmt.Errorf("placement: %w", err)
	}
	ports, waiting, err := r.ensurePorts(ctx, mc, svc)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("ports: %w", err)
	}
	pod, pending, wait, err := r.ensurePod(ctx, mc)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("pod: %w", err)
	}
	// Ports waiting for a public port are looked at again: another server
	// giving one back says nothing to this one.
	result := ctrl.Result{RequeueAfter: wait}
	if waiting && (wait == 0 || wait > time.Minute) {
		result.RequeueAfter = time.Minute
	}
	return result, r.updateStatus(ctx, mc, pod, svc, ports, pending)
}

// leftOf reports whether an object belongs to another server than this one,
// which for an object of this server's name means one that was deleted.
// Objects that belong to no server at all, such as a volume someone made by
// hand for a server to use, are not leftovers.
func leftOf(obj metav1.Object, mc *avsysv1.MinecraftServer) bool {
	owner := metav1.GetControllerOf(obj)
	return owner != nil && owner.UID != mc.UID
}

// leftovers names the first thing found under this server's name that
// belongs to a deleted server, or returns "" when there is none.
func (r *Reconciler) leftovers(ctx context.Context, mc *avsysv1.MinecraftServer) (string, error) {
	for _, candidate := range []struct {
		what string
		name string
		obj  client.Object
	}{
		{"pod", mc.Name, &corev1.Pod{}},
		{"volume", DataPVCName(mc.Name), &corev1.PersistentVolumeClaim{}},
		{"console secret", RCONSecretName(mc.Name), &corev1.Secret{}},
		{"service", mc.Name, &corev1.Service{}},
	} {
		err := r.Get(ctx, types.NamespacedName{Namespace: mc.Namespace, Name: candidate.name}, candidate.obj)
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return "", err
		}
		if leftOf(candidate.obj, mc) {
			return candidate.what, nil
		}
	}
	return "", nil
}

// waitForLeftovers says on the server why nothing is happening yet. The phase
// is one of the in-between ones, so that nothing treats the server as settled
// while a pod of its name is still someone else's.
func (r *Reconciler) waitForLeftovers(ctx context.Context, mc *avsysv1.MinecraftServer, what string) error {
	orig := mc.DeepCopy()
	mc.Status.Phase = avsysv1.PhaseStopping
	if wantsMinecraft(mc) {
		mc.Status.Phase = avsysv1.PhaseStarting
	}
	mc.Status.Message = fmt.Sprintf("Waiting for the %s of a deleted server of this name to be removed.", what)
	mc.Status.Players = nil
	if equality.Semantic.DeepEqual(orig.Status, mc.Status) {
		return nil
	}
	return r.Status().Patch(ctx, mc, client.MergeFrom(orig))
}

// moveVolume takes a server's volume off the node it is leaving: the pod
// that has it, then the volume, and nothing is made in their place until
// the volume is gone. waiting is true until then.
func (r *Reconciler) moveVolume(ctx context.Context, mc *avsysv1.MinecraftServer, target string) (waiting bool, err error) {
	pvc := &corev1.PersistentVolumeClaim{}
	err = r.Get(ctx, types.NamespacedName{Namespace: mc.Namespace, Name: DataPVCName(mc.Name)}, pvc)
	if apierrors.IsNotFound(err) {
		// Made again by ensurePVC, and tied to target when its pod, kept
		// there, first uses it.
		return false, nil
	}
	if err != nil {
		return false, err
	}
	node, err := r.nodeOfVolume(ctx, mc)
	if err != nil {
		return false, err
	}
	// Not yet tied anywhere, as a new one is until its pod, kept to target,
	// first uses it; or not node-local; or on target already.
	if pvc.DeletionTimestamp.IsZero() && (pvc.Spec.VolumeName == "" || node == "" || node == target) {
		return false, nil
	}
	// Nothing is deleted on a mark read from a cache, which a move given
	// up a moment ago may have taken off: it is read again as it is now.
	live := &avsysv1.MinecraftServer{}
	if err := r.Reader.Get(ctx, client.ObjectKeyFromObject(mc), live); err != nil {
		return true, client.IgnoreNotFound(err)
	}
	if live.Annotations[avsysv1.AnnotationMoving] != target {
		return false, nil
	}
	pod := &corev1.Pod{}
	switch err := r.Get(ctx, client.ObjectKeyFromObject(mc), pod); {
	case err == nil && pod.DeletionTimestamp.IsZero():
		if err := r.Delete(ctx, pod); client.IgnoreNotFound(err) != nil {
			return true, err
		}
	case err != nil && !apierrors.IsNotFound(err):
		return true, err
	}
	if pvc.DeletionTimestamp.IsZero() {
		log.FromContext(ctx).Info("deleting the volume on the node moved from", "node", node, "to", target)
		if err := r.Delete(ctx, pvc); client.IgnoreNotFound(err) != nil {
			return true, err
		}
	}
	return true, nil
}

// waitForMove says on the server what it is waiting for.
func (r *Reconciler) waitForMove(ctx context.Context, mc *avsysv1.MinecraftServer, target string) error {
	orig := mc.DeepCopy()
	mc.Status.Phase = avsysv1.PhaseStopped
	mc.Status.Message = fmt.Sprintf("Moving to %s.", target)
	mc.Status.Players = nil
	if equality.Semantic.DeepEqual(orig.Status, mc.Status) {
		return nil
	}
	return r.Status().Patch(ctx, mc, client.MergeFrom(orig))
}

func (r *Reconciler) ensureWorkspaceSecret(ctx context.Context, namespace string) error {
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: WorkspaceSecret, Namespace: namespace}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, secret, func() error {
		secret.Data = map[string][]byte{"authorized_keys": []byte(r.Config.WorkspaceAuthorizedKeys)}
		return nil
	})
	return err
}

func (r *Reconciler) ensureRCONSecret(ctx context.Context, mc *avsysv1.MinecraftServer) error {
	key := types.NamespacedName{Namespace: mc.Namespace, Name: RCONSecretName(mc.Name)}
	if err := r.Get(ctx, key, &corev1.Secret{}); !apierrors.IsNotFound(err) {
		return err
	}
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return err
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace, Labels: labelsFor(mc)},
		StringData: map[string]string{"password": hex.EncodeToString(buf)},
	}
	if err := controllerutil.SetControllerReference(mc, secret, r.Scheme); err != nil {
		return err
	}
	return r.Create(ctx, secret)
}

func (r *Reconciler) ensurePVC(ctx context.Context, mc *avsysv1.MinecraftServer) error {
	existing := &corev1.PersistentVolumeClaim{}
	err := r.Get(ctx, types.NamespacedName{Namespace: mc.Namespace, Name: DataPVCName(mc.Name)}, existing)
	if apierrors.IsNotFound(err) {
		pvc := buildPVC(mc)
		if err := controllerutil.SetControllerReference(mc, pvc, r.Scheme); err != nil {
			return err
		}
		return r.Create(ctx, pvc)
	}
	if err != nil {
		return err
	}
	// Volumes can grow but never shrink. Whether growing works is up to the
	// storage class, so a refusal is logged rather than failing the reconcile.
	want := mc.Spec.Storage.Size
	if have := existing.Spec.Resources.Requests[corev1.ResourceStorage]; want.Cmp(have) > 0 {
		orig := existing.DeepCopy()
		existing.Spec.Resources.Requests[corev1.ResourceStorage] = want
		if err := r.Patch(ctx, existing, client.MergeFrom(orig)); err != nil {
			log.FromContext(ctx).Info("volume could not be expanded", "error", err.Error())
		}
	}
	return nil
}

func (r *Reconciler) ensureService(ctx context.Context, mc *avsysv1.MinecraftServer) (*corev1.Service, error) {
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: mc.Name, Namespace: mc.Namespace}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, svc, func() error {
		applyService(svc, mc)
		return controllerutil.SetControllerReference(mc, svc, r.Scheme)
	})
	return svc, err
}

// ensurePod creates the server's pod, or replaces it when it no longer matches
// what is wanted. A healthy running game is never restarted just because the
// spec changed: that is reported as pending and applied on the next restart.
//
// A game that is loading is let finish before it is replaced: told to stop
// then, it stops only once loaded, and the image's way of telling it fails
// at that, so it is killed a minute later, unsaved. wait is how long until
// it is looked at again, if nothing changes before; its pod becoming ready
// is a change.
func (r *Reconciler) ensurePod(ctx context.Context, mc *avsysv1.MinecraftServer) (pod *corev1.Pod, pending bool, wait time.Duration, err error) {
	desired := buildPod(mc, r.Config)
	existing := &corev1.Pod{}
	err = r.Get(ctx, types.NamespacedName{Namespace: mc.Namespace, Name: mc.Name}, existing)
	if apierrors.IsNotFound(err) {
		if err := controllerutil.SetControllerReference(mc, desired, r.Scheme); err != nil {
			return nil, false, 0, err
		}
		if err := r.Create(ctx, desired); err != nil {
			return nil, false, 0, err
		}
		return desired, false, 0, nil
	}
	if err != nil {
		return nil, false, 0, err
	}
	if !existing.DeletionTimestamp.IsZero() {
		// Its removal triggers another reconcile, which creates the replacement.
		return existing, false, 0, nil
	}

	finished := existing.Status.Phase == corev1.PodFailed || existing.Status.Phase == corev1.PodSucceeded
	stateChanged := hasContainer(existing, MinecraftContainer) != wantsMinecraft(mc)
	restartAsked := existing.Annotations[avsysv1.AnnotationRestart] != desired.Annotations[avsysv1.AnnotationRestart]
	specChanged := existing.Annotations[annotationSpecHash] != desired.Annotations[annotationSpecHash]
	serving := hasContainer(existing, MinecraftContainer) && containerReady(existing, MinecraftContainer)

	if finished || stateChanged || restartAsked || (specChanged && !serving) {
		if !finished {
			if wait := letLoad(existing, time.Now()); wait > 0 {
				// Only new settings for a game that goes on running are
				// pending: a stop or a restart asked for is not.
				return existing, specChanged && !stateChanged && !restartAsked, wait, nil
			}
		}
		if err := r.Delete(ctx, existing); client.IgnoreNotFound(err) != nil {
			return nil, false, 0, err
		}
		now := metav1.Now()
		existing.DeletionTimestamp = &now
		return existing, false, 0, nil
	}
	return existing, specChanged, 0, nil
}

func containerStatus(pod *corev1.Pod, name string) *corev1.ContainerStatus {
	for i := range pod.Status.ContainerStatuses {
		if pod.Status.ContainerStatuses[i].Name == name {
			return &pod.Status.ContainerStatuses[i]
		}
	}
	return nil
}

// loading says whether a pod's game is on its way up and not yet ready:
// its container being made, the image setting it up, or the game loading
// its world. Told to stop then, it does not until it is up, and then
// fails to, so it is let finish first.
func loading(pod *corev1.Pod) bool {
	cs := containerStatus(pod, MinecraftContainer)
	if cs == nil || cs.Ready {
		return false
	}
	if w := cs.State.Waiting; w != nil {
		return w.Reason == "ContainerCreating" || w.Reason == "PodInitializing"
	}
	return cs.State.Running != nil
}

// LoadGrace is how long a game is let load before it is stopped anyway: one
// that takes longer is taken as stuck. Whatever waits for a server to stop
// waits at least this long, and the time a game takes to stop, besides.
const LoadGrace = 5 * time.Minute

// letLoad is how much longer a pod's game that is loading is let finish
// before it is stopped, or zero for one that is not loading or has had
// LoadGrace.
func letLoad(pod *corev1.Pod, now time.Time) time.Duration {
	if !loading(pod) {
		return 0
	}
	since := pod.CreationTimestamp.Time
	if r := containerStatus(pod, MinecraftContainer).State.Running; r != nil {
		since = r.StartedAt.Time
	}
	return max(since.Add(LoadGrace).Sub(now), 0)
}

func containerReady(pod *corev1.Pod, name string) bool {
	cs := containerStatus(pod, name)
	return cs != nil && cs.Ready
}

// phaseOf derives the user-facing phase from what is wanted and what the pod is doing.
func phaseOf(mc *avsysv1.MinecraftServer, pod *corev1.Pod) (avsysv1.Phase, string) {
	gameInPod := pod != nil && hasContainer(pod, MinecraftContainer)
	if !wantsMinecraft(mc) {
		switch {
		case gameInPod:
			if loading(pod) {
				return avsysv1.PhaseStopping, "The game is still loading. The server stops once the game has loaded."
			}
			return avsysv1.PhaseStopping, ""
		// Before its stopped state, which a move sets too.
		case mc.Annotations[avsysv1.AnnotationRestoring] != "" && mc.Annotations[avsysv1.AnnotationMoving] != "":
			return avsysv1.PhaseStopped, fmt.Sprintf("Moving to %s.", mc.Annotations[avsysv1.AnnotationMoving])
		case mc.Annotations[avsysv1.AnnotationCreating] != "":
			return avsysv1.PhaseStopped, "Copying files from a snapshot."
		case mc.Annotations[avsysv1.AnnotationImporting] != "":
			return avsysv1.PhaseStopped, "Importing files."
		case mc.Spec.State == avsysv1.StateStopped:
			return avsysv1.PhaseStopped, ""
		case mc.Annotations[avsysv1.AnnotationRestoring] != "":
			return avsysv1.PhaseStopped, "Restoring files."
		default:
			return avsysv1.PhaseSleeping, ""
		}
	}
	// Asked to restart while it loads: it is let finish first.
	if gameInPod && pod.DeletionTimestamp.IsZero() && loading(pod) && pod.Annotations[avsysv1.AnnotationRestart] != mc.Annotations[avsysv1.AnnotationRestart] {
		return avsysv1.PhaseStarting, "Restarting. The game is still loading."
	}
	if gameInPod && !pod.DeletionTimestamp.IsZero() {
		// Restarted: the game it had stops before the next one starts.
		if loading(pod) {
			return avsysv1.PhaseStarting, "Restarting. The game is still loading."
		}
		return avsysv1.PhaseStarting, "Restarting. Saving the world first."
	}
	if !gameInPod {
		return avsysv1.PhaseStarting, ""
	}
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodScheduled && c.Status == corev1.ConditionFalse {
			return avsysv1.PhaseStarting, c.Message
		}
	}
	cs := containerStatus(pod, MinecraftContainer)
	switch {
	case cs == nil:
		return avsysv1.PhaseStarting, ""
	case cs.Ready:
		return avsysv1.PhaseRunning, ""
	case cs.State.Waiting != nil:
		switch w := cs.State.Waiting; w.Reason {
		case "ContainerCreating", "PodInitializing":
			return avsysv1.PhaseStarting, ""
		case "CrashLoopBackOff":
			restarts := "1 restart"
			if cs.RestartCount != 1 {
				restarts = fmt.Sprintf("%d restarts", cs.RestartCount)
			}
			if phase, message, ok := startFailure(pod, cs.LastTerminationState.Terminated, cs.RestartCount); ok {
				return phase, message
			}
			return avsysv1.PhaseError, fmt.Sprintf("The server keeps crashing (%s). Check the console for the cause.", restarts)
		default:
			return avsysv1.PhaseError, fmt.Sprintf("%s: %s", w.Reason, w.Message)
		}
	case cs.State.Terminated != nil && cs.State.Terminated.ExitCode != 0:
		if phase, message, ok := startFailure(pod, cs.State.Terminated, cs.RestartCount); ok {
			return phase, message
		}
		return avsysv1.PhaseError, fmt.Sprintf("The server exited with code %d.", cs.State.Terminated.ExitCode)
	}
	return avsysv1.PhaseStarting, ""
}

// A game that could not look up a name is reported as starting again until
// it has been restarted lookupRetries times and its game has not been ready
// for lookupGrace. Both, because the restart count is the pod's whole life,
// and a game can be not ready for long while it installs a large modpack.
// The cluster waits longer between each restart, so five is about five
// minutes.
const (
	lookupRetries = 5
	lookupGrace   = 5 * time.Minute
)

// Lines in a game's log that say a name could not be looked up, with the name.
// The Java ones are the game's own downloads and the image's helper; the
// others are curl and Go, which the image also uses to fetch things.
var lookupFailed = []*regexp.Regexp{
	regexp.MustCompile(`UnknownHostException: ([A-Za-z0-9.-]+)`),
	regexp.MustCompile(`DnsNameResolver\w*Exception: .*\(([A-Za-z0-9.-]+?)\.? IN `),
	regexp.MustCompile(`Could not resolve host: ([A-Za-z0-9.-]+)`),
	regexp.MustCompile(`dial tcp: lookup ([A-Za-z0-9.-]+?)\.? on `),
}

var ansiColor = regexp.MustCompile("\x1b\\[[0-9;]*m")

// startFailure says whether a game stopped because a name it downloads from
// could not be looked up, from the end of its log that the cluster kept. That
// is nearly always the network or the cluster's DNS for a moment, and the
// game is started again by itself, so it is reported as starting, with why,
// rather than as a crash. If it goes on, it becomes an error.
func startFailure(pod *corev1.Pod, t *corev1.ContainerStateTerminated, restarts int32) (avsysv1.Phase, string, bool) {
	if t == nil || t.ExitCode == 0 {
		return "", "", false
	}
	host := lookupAtEnd(ansiColor.ReplaceAllString(t.Message, ""))
	if host == "" {
		return "", "", false
	}
	if waited := notReadyFor(pod); restarts >= lookupRetries && waited >= lookupGrace {
		return avsysv1.PhaseError, fmt.Sprintf("The game could not look up %s for %d minutes. Check that the cluster can reach the internet.", host, int(waited.Minutes())), true
	}
	return avsysv1.PhaseStarting, fmt.Sprintf("The game could not look up %s to download the server's files. Trying again.", host), true
}

// lookupAtEnd is the name in the last failed lookup in a log, if that is how
// the log ends: nothing after it but the rest of its stack trace and the
// image saying the game stopped. A lookup a plugin logged earlier, before
// some other failure, is not why the game stopped.
func lookupAtEnd(log string) string {
patterns:
	for _, re := range lookupFailed {
		m := re.FindAllStringSubmatchIndex(log, -1)
		if m == nil {
			continue
		}
		last := m[len(m)-1]
		rest := log[last[1]:]
		if i := strings.IndexByte(rest, '\n'); i >= 0 {
			rest = rest[i+1:]
		} else {
			rest = ""
		}
		for _, line := range strings.Split(rest, "\n") {
			line = strings.TrimSpace(line)
			switch {
			case line == "",
				strings.HasPrefix(line, "at "),
				strings.HasPrefix(line, "..."),
				strings.HasPrefix(line, "Caused by: "),
				strings.HasPrefix(line, "[init] "),
				strings.Contains(line, "mc-server-runner"):
			default:
				continue patterns
			}
		}
		return log[last[2]:last[3]]
	}
	return ""
}

// notReadyFor is how long the pod's containers have not been ready: since the
// pod was made, or since the game last stopped being ready.
func notReadyFor(pod *corev1.Pod) time.Duration {
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.ContainersReady && c.Status != corev1.ConditionTrue && !c.LastTransitionTime.IsZero() {
			return time.Since(c.LastTransitionTime.Time)
		}
	}
	return 0
}

func (r *Reconciler) updateStatus(ctx context.Context, mc *avsysv1.MinecraftServer, pod *corev1.Pod, svc *corev1.Service, ports []avsysv1.PortStatus, pending bool) error {
	orig := mc.DeepCopy()
	st := &mc.Status

	phase, message := phaseOf(mc, pod)
	if phase == avsysv1.PhaseRunning && st.Phase != avsysv1.PhaseRunning {
		// The idle clock starts when the server becomes joinable.
		now := metav1.Now()
		st.LastActive = &now
	}
	if phase != avsysv1.PhaseRunning {
		st.Players = nil
	}
	st.Phase, st.Message = phase, message
	st.Address = addressFor(mc, svc, r.Config)
	st.Ports = ports
	st.PendingRestart = pending
	st.ObservedGeneration = mc.Generation
	st.Restarts = 0
	st.Node = mc.Annotations[avsysv1.AnnotationNode]
	if pod != nil && pod.Spec.NodeName != "" {
		st.Node = pod.Spec.NodeName
	}
	if pod != nil {
		if cs := containerStatus(pod, MinecraftContainer); cs != nil {
			st.Restarts = cs.RestartCount
		}
	}

	if equality.Semantic.DeepEqual(orig.Status, mc.Status) {
		return nil
	}
	return r.Status().Patch(ctx, mc, client.MergeFrom(orig))
}
