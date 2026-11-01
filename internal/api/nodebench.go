package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
	"github.com/GT-Drehmal/avSYS/internal/controller"
	"github.com/GT-Drehmal/avSYS/internal/store"
)

// A node's speed is measured as hack/rank-nodes.sh does it: one short pod on
// each node runs the 7-Zip benchmark on one thread, twice, and the better
// run is its score. Scores only mean something next to each other.

// benchScript is what each pod runs. It prints one rating per run.
const benchScript = `apk add --no-cache 7zip >/dev/null 2>&1 || exit 1
for run in 1 2; do 7z b 1 -mmt1 -md24 | awk '/^Tot:/ {print $4}'; done`

// benchTimeout is how long a measurement may take in all, pulling the image
// included.
const benchTimeout = 10 * time.Minute

// nodeBench is the measuring of nodes' speed: at most one at a time.
type nodeBench struct {
	mu      sync.Mutex
	running bool
	last    *benchResult
}

type benchResult struct {
	At time.Time `json:"at"`
	// Error says why nothing could be measured.
	Error string `json:"error,omitempty"`
	// Failed are the nodes that could not be measured, which keep the score
	// they had.
	Failed []string `json:"failed,omitempty"`
}

func (b *nodeBench) state() (running bool, last *benchResult) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.running, b.last
}

// setNode switches automatic placement on a node on or off.
func (s *Server) setNode(w http.ResponseWriter, r *http.Request, u *store.User) error {
	if !u.IsAdmin {
		return errForbidden
	}
	var in struct {
		Automatic *bool `json:"automatic"`
	}
	if err := decode(r, &in); err != nil {
		return err
	}
	if in.Automatic == nil {
		return errBadRequest("Say whether the node takes new servers.")
	}
	node := &corev1.Node{}
	if err := s.nodeReader().Get(r.Context(), client.ObjectKey{Name: r.PathValue("node")}, node); apierrors.IsNotFound(err) {
		return errNotFound
	} else if err != nil {
		return err
	}
	orig := node.DeepCopy()
	if *in.Automatic {
		delete(node.Labels, avsysv1.LabelNodeHosts)
	} else {
		if node.Labels == nil {
			node.Labels = map[string]string{}
		}
		node.Labels[avsysv1.LabelNodeHosts] = "false"
	}
	if err := s.Client.Patch(r.Context(), node, client.MergeFrom(orig)); err != nil {
		return err
	}
	s.audit(r.Context(), u, nil, "node.automatic", node.Name, strconv.FormatBool(*in.Automatic))
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// measureNodes starts measuring the speed of every node that is up. It goes
// on after the answer; the list of nodes says when it is done.
func (s *Server) measureNodes(w http.ResponseWriter, r *http.Request, u *store.User) error {
	if !u.IsAdmin {
		return errForbidden
	}
	s.bench.mu.Lock()
	if s.bench.running {
		s.bench.mu.Unlock()
		return &Error{Status: http.StatusConflict, Code: "busy", Message: "Already measuring."}
	}
	s.bench.running = true
	s.bench.mu.Unlock()
	s.audit(r.Context(), u, nil, "node.measure", "", "")
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), benchTimeout+time.Minute)
		defer cancel()
		failed, err := s.benchNodes(ctx)
		result := &benchResult{At: time.Now().UTC(), Failed: failed}
		if err != nil {
			s.Log.Warn("could not measure nodes", "error", err)
			result.Error = err.Error()
		}
		s.bench.mu.Lock()
		s.bench.running = false
		s.bench.last = result
		s.bench.mu.Unlock()
	}()
	writeJSON(w, http.StatusAccepted, map[string]any{"measuring": true})
	return nil
}

// benchNodes measures every node that is up, in a namespace of its own that
// is removed afterwards, and labels each with its score. It returns those it
// could not measure.
func (s *Server) benchNodes(ctx context.Context) (failed []string, err error) {
	var list corev1.NodeList
	if err := s.nodeReader().List(ctx, &list); err != nil {
		return nil, err
	}
	var nodes []corev1.Node
	for _, n := range list.Items {
		if controller.NodeReady(&n) {
			nodes = append(nodes, n)
		}
	}
	// What a measuring cut short by a restart left is removed first.
	s.ReapBench(ctx)
	id := make([]byte, 3)
	_, _ = rand.Read(id)
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "avsys-bench-" + hex.EncodeToString(id), Labels: map[string]string{benchLabel: "true"}}}
	if err := s.Client.Create(ctx, ns); err != nil {
		return names(nodes), err
	}
	defer func() {
		if err := s.Client.Delete(context.WithoutCancel(ctx), ns); err != nil {
			s.Log.Warn("could not remove the namespace nodes were measured in", "namespace", ns.Name, "error", err)
		}
	}()

	pods := map[string]string{}
	for i, n := range nodes {
		pod := benchPod(ns.Name, "bench-"+strconv.Itoa(i), n.Name)
		if err := s.createBenchPod(ctx, pod); err != nil {
			s.Log.Warn("could not start measuring a node", "node", n.Name, "error", err)
			failed = append(failed, n.Name)
			continue
		}
		pods[n.Name] = pod.Name
	}

	// Each is read once it is done; what is still going when time is up
	// counts as failed.
	deadline := time.Now().Add(benchTimeout)
	left := map[string]string{}
	for node, pod := range pods {
		left[node] = pod
	}
	for len(left) > 0 && time.Now().Before(deadline) {
		for node, name := range left {
			pod := &corev1.Pod{}
			if err := s.nodeReader().Get(ctx, client.ObjectKey{Namespace: ns.Name, Name: name}, pod); err != nil {
				continue
			}
			switch pod.Status.Phase {
			case corev1.PodSucceeded:
				delete(left, node)
				score, ok := s.benchScore(ctx, ns.Name, name)
				if !ok || s.labelScore(ctx, node, score) != nil {
					failed = append(failed, node)
				}
			case corev1.PodFailed:
				delete(left, node)
				failed = append(failed, node)
			}
		}
		select {
		case <-ctx.Done():
			return append(failed, nodeNames(left)...), ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
	return append(failed, nodeNames(left)...), nil
}

// benchLabel marks the namespaces nodes are measured in.
const benchLabel = "avsys.dev/bench"

// ReapBench removes the namespaces of measurings that are not running: ones
// a panel that was stopped meanwhile left behind.
func (s *Server) ReapBench(ctx context.Context) {
	var list corev1.NamespaceList
	if err := s.Client.List(ctx, &list, client.MatchingLabels{benchLabel: "true"}); err != nil {
		s.Log.Warn("could not look for namespaces left from measuring nodes", "error", err)
		return
	}
	for i := range list.Items {
		if ns := &list.Items[i]; ns.DeletionTimestamp.IsZero() {
			if err := s.Client.Delete(ctx, ns); err != nil && !apierrors.IsNotFound(err) {
				s.Log.Warn("could not remove a namespace left from measuring nodes", "namespace", ns.Name, "error", err)
			}
		}
	}
}

// createBenchPod creates a pod in a namespace made a moment ago, whose
// default service account the cluster may not have made yet: it is tried
// again for a little while.
func (s *Server) createBenchPod(ctx context.Context, pod *corev1.Pod) error {
	deadline := time.Now().Add(30 * time.Second)
	for {
		err := s.Client.Create(ctx, pod)
		if err == nil || time.Now().After(deadline) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

func benchPod(namespace, name, node string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, Labels: map[string]string{"app.kubernetes.io/managed-by": "avsys"}},
		Spec: corev1.PodSpec{
			// Put there directly: a node is measured whatever it is set to
			// take.
			NodeName:      node,
			RestartPolicy: corev1.RestartPolicyNever,
			Containers: []corev1.Container{{
				Name:    "bench",
				Image:   "alpine:3",
				Command: []string{"sh", "-c", benchScript},
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m"), corev1.ResourceMemory: resource.MustParse("64Mi")},
					Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("1Gi")},
				},
			}},
		},
	}
}

// benchScore is the better of the runs a pod printed.
func (s *Server) benchScore(ctx context.Context, namespace, pod string) (int, bool) {
	out, err := s.Kube.CoreV1().Pods(namespace).GetLogs(pod, &corev1.PodLogOptions{}).DoRaw(ctx)
	if err != nil {
		return 0, false
	}
	return bestRun(string(out))
}

// bestRun is the highest rating among the lines, the slower run being the
// one more likely to have been disturbed.
func bestRun(out string) (int, bool) {
	best, ok := 0, false
	for _, line := range strings.Fields(out) {
		if n, err := strconv.Atoi(line); err == nil && n > 0 && n > best {
			best, ok = n, true
		}
	}
	return best, ok
}

func (s *Server) labelScore(ctx context.Context, name string, score int) error {
	node := &corev1.Node{}
	if err := s.nodeReader().Get(ctx, client.ObjectKey{Name: name}, node); err != nil {
		return err
	}
	orig := node.DeepCopy()
	if node.Labels == nil {
		node.Labels = map[string]string{}
	}
	node.Labels[avsysv1.LabelNodeScore] = strconv.Itoa(score)
	return s.Client.Patch(ctx, node, client.MergeFrom(orig))
}

func names(nodes []corev1.Node) []string {
	out := make([]string, len(nodes))
	for i, n := range nodes {
		out[i] = n.Name
	}
	return out
}

func nodeNames(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
