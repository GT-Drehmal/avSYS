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
	pod, pending, err := r.ensurePod(ctx, mc)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("pod: %w", err)
	}
	return ctrl.Result{}, r.updateStatus(ctx, mc, pod, svc, pending)
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
func (r *Reconciler) ensurePod(ctx context.Context, mc *avsysv1.MinecraftServer) (pod *corev1.Pod, pending bool, err error) {
	desired := buildPod(mc, r.Config)
	existing := &corev1.Pod{}
	err = r.Get(ctx, types.NamespacedName{Namespace: mc.Namespace, Name: mc.Name}, existing)
	if apierrors.IsNotFound(err) {
		if err := controllerutil.SetControllerReference(mc, desired, r.Scheme); err != nil {
			return nil, false, err
		}
		if err := r.Create(ctx, desired); err != nil {
			return nil, false, err
		}
		return desired, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if !existing.DeletionTimestamp.IsZero() {
		// Its removal triggers another reconcile, which creates the replacement.
		return existing, false, nil
	}

	finished := existing.Status.Phase == corev1.PodFailed || existing.Status.Phase == corev1.PodSucceeded
	stateChanged := hasContainer(existing, MinecraftContainer) != wantsMinecraft(mc)
	restartAsked := existing.Annotations[avsysv1.AnnotationRestart] != desired.Annotations[avsysv1.AnnotationRestart]
	specChanged := existing.Annotations[annotationSpecHash] != desired.Annotations[annotationSpecHash]
	serving := hasContainer(existing, MinecraftContainer) && containerReady(existing, MinecraftContainer)

	if finished || stateChanged || restartAsked || (specChanged && !serving) {
		if err := r.Delete(ctx, existing); client.IgnoreNotFound(err) != nil {
			return nil, false, err
		}
		now := metav1.Now()
		existing.DeletionTimestamp = &now
		return existing, false, nil
	}
	return existing, specChanged, nil
}

func containerStatus(pod *corev1.Pod, name string) *corev1.ContainerStatus {
	for i := range pod.Status.ContainerStatuses {
		if pod.Status.ContainerStatuses[i].Name == name {
			return &pod.Status.ContainerStatuses[i]
		}
	}
	return nil
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
			return avsysv1.PhaseStopping, ""
		case mc.Annotations[avsysv1.AnnotationCreating] != "":
			return avsysv1.PhaseStopped, "Made from a snapshot, and held stopped until all of its files are in place."
		case mc.Spec.State == avsysv1.StateStopped:
			return avsysv1.PhaseStopped, ""
		case mc.Annotations[avsysv1.AnnotationRestoring] != "":
			return avsysv1.PhaseStopped, "Held stopped until a restore of its files has completed."
		default:
			return avsysv1.PhaseSleeping, ""
		}
	}
	if !gameInPod || !pod.DeletionTimestamp.IsZero() {
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
		return avsysv1.PhaseError, fmt.Sprintf("The game could not look up %s to download what it runs, and has kept failing for %d minutes. Check that the cluster can reach the internet and resolve names.", host, int(waited.Minutes())), true
	}
	return avsysv1.PhaseStarting, fmt.Sprintf("The game could not look up %s to download what it runs, and is being started again.", host), true
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

func (r *Reconciler) updateStatus(ctx context.Context, mc *avsysv1.MinecraftServer, pod *corev1.Pod, svc *corev1.Service, pending bool) error {
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
