package api

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
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

// The console completes commands from the game's own list of them: every
// command, its subcommands and its arguments, as the game's command tree
// has them. The game writes that tree out when its data generator is asked
// for its reports, so the panel runs the generator once for each game
// version, in a short pod with the version's own server jar from Mojang,
// and keeps what it wrote. The tree is of the game's own commands: those a
// mod or plugin adds are not in it.

// commandTrees are the command trees the panel has, by game version, and
// those it is making.
type commandTrees struct {
	mu     sync.Mutex
	trees  map[string]*commandTree
	making map[string]bool
	// failed is when a version that could not be made may be tried again.
	failed map[string]time.Time
	// turn holds the panel to making one at a time.
	turn sync.Mutex
}

// commandTree is the game's commands.json, compacted and gzipped, as it is
// sent.
type commandTree struct {
	gz   []byte
	etag string
}

const (
	// commandsTimeout is how long making a tree may take, pulling the
	// image included.
	commandsTimeout = 10 * time.Minute
	// commandsRetry is how long after a tree could not be made it is tried
	// again.
	commandsRetry = 10 * time.Minute
	// commandsLabel marks the pods trees are made in.
	commandsLabel = "avsys.dev/commands"
)

// ErrNoCommands is a version there is no tree for, nor will be: one older
// than the game's command tree, or one Mojang has no server for.
var ErrNoCommands = errors.New("the game version has no command tree")

// commandsState is how far the tree of a version is.
type commandsState int

const (
	commandsReady commandsState = iota
	commandsMaking
	commandsNone
)

// getCommands answers with the game's command tree for the version a server
// runs, for the console to complete commands from. While it is being made
// the answer is 202, to ask again shortly; where there is none, 404.
func (s *Server) getCommands(w http.ResponseWriter, r *http.Request, u *store.User) error {
	_, mc, err := s.server(r, u, store.RoleOperator)
	if err != nil {
		return err
	}
	version := s.commandsVersion(r.Context(), mc)
	if version == "" {
		return noCommands
	}
	tree, state := s.commandTree(mc, version)
	switch state {
	case commandsMaking:
		writeJSON(w, http.StatusAccepted, map[string]any{"making": true, "version": version})
		return nil
	case commandsNone:
		return noCommands
	}
	h := w.Header()
	h.Set("ETag", tree.etag)
	h.Set("Cache-Control", "private, no-cache")
	h.Set("Vary", "Accept-Encoding")
	h.Set("X-Game-Version", version)
	if inm := r.Header.Get("If-None-Match"); inm != "" && inm == tree.etag {
		w.WriteHeader(http.StatusNotModified)
		return nil
	}
	h.Set("Content-Type", "application/json")
	if acceptsGzip(r) {
		h.Set("Content-Encoding", "gzip")
		_, _ = w.Write(tree.gz)
		return nil
	}
	z, err := gzip.NewReader(bytes.NewReader(tree.gz))
	if err != nil {
		return err
	}
	_, _ = io.Copy(w, z)
	return nil
}

var noCommands = &Error{Status: http.StatusNotFound, Code: "no_commands", Message: "There is no list of commands for this game version."}

func acceptsGzip(r *http.Request) bool {
	for _, part := range strings.Split(r.Header.Get("Accept-Encoding"), ",") {
		name, q, _ := strings.Cut(strings.TrimSpace(part), ";")
		if strings.EqualFold(strings.TrimSpace(name), "gzip") && strings.ReplaceAll(q, " ", "") != "q=0" {
			return true
		}
	}
	return false
}

// commandsVersion is the game version a server's commands are of: the one
// it is set to, or for LATEST the one it says it runs, or else the newest
// release. "" when none can be told.
func (s *Server) commandsVersion(ctx context.Context, mc *avsysv1.MinecraftServer) string {
	v := controller.GameVersionOf(&mc.Spec)
	if v != "" && !strings.EqualFold(v, "latest") {
		return v
	}
	if ran := controller.RanVersion(mc.Status.Version); ran != "" {
		return ran
	}
	m, err := s.manifest(ctx)
	if err != nil {
		return ""
	}
	return m.Latest["release"]
}

// commandTree is the tree of a version, if the panel has it; otherwise it
// starts making it, unless that is under way or failed a moment ago.
func (s *Server) commandTree(mc *avsysv1.MinecraftServer, version string) (*commandTree, commandsState) {
	c := &s.commands
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.trees == nil {
		c.trees, c.making, c.failed = map[string]*commandTree{}, map[string]bool{}, map[string]time.Time{}
	}
	if t := c.trees[version]; t != nil {
		return t, commandsReady
	}
	if gz, err := os.ReadFile(s.commandsFile(version)); err == nil {
		t := newCommandTree(gz)
		c.trees[version] = t
		return t, commandsReady
	}
	if c.making[version] {
		return nil, commandsMaking
	}
	if until, ok := c.failed[version]; ok && time.Now().Before(until) {
		return nil, commandsNone
	}
	c.making[version] = true
	mc = mc.DeepCopy()
	go s.makeCommandTree(mc, version)
	return nil, commandsMaking
}

func newCommandTree(gz []byte) *commandTree {
	sum := sha256.Sum256(gz)
	return &commandTree{gz: gz, etag: `"` + hex.EncodeToString(sum[:12]) + `"`}
}

// commandsFile is where a version's tree is kept, or "" where the panel
// keeps none.
func (s *Server) commandsFile(version string) string {
	if s.Config.CommandsDir == "" {
		return ""
	}
	return filepath.Join(s.Config.CommandsDir, url.PathEscape(version)+".json.gz")
}

// makeCommandTree makes a version's tree and keeps it, or notes that it
// could not.
func (s *Server) makeCommandTree(mc *avsysv1.MinecraftServer, version string) {
	c := &s.commands
	c.turn.Lock()
	defer c.turn.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), commandsTimeout+time.Minute)
	defer cancel()
	var raw []byte
	var err error
	if s.CommandTree != nil {
		raw, err = s.CommandTree(ctx, version)
	} else {
		raw, err = s.generateCommands(ctx, mc, version)
	}
	var gz []byte
	if err == nil {
		gz, err = packCommands(raw)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.making, version)
	if err != nil {
		retry := commandsRetry
		if errors.Is(err, ErrNoCommands) {
			// It will not be there later either; another start of the
			// panel asks again.
			retry = 100 * 365 * 24 * time.Hour
		} else {
			s.Log.Warn("could not make the list of commands of a game version", "version", version, "error", err)
		}
		c.failed[version] = time.Now().Add(retry)
		return
	}
	c.trees[version] = newCommandTree(gz)
	if f := s.commandsFile(version); f != "" {
		if err := writeFileAtomic(f, gz); err != nil {
			s.Log.Warn("could not keep the list of commands of a game version", "version", version, "error", err)
		}
	}
}

func writeFileAtomic(name string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(name), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), name)
}

// packCommands checks that what was made is a command tree, and compacts and
// gzips it.
func packCommands(raw []byte) ([]byte, error) {
	var root struct {
		Type     string                     `json:"type"`
		Children map[string]json.RawMessage `json:"children"`
	}
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, fmt.Errorf("reading the command tree: %w", err)
	}
	if root.Type != "root" || len(root.Children) == 0 {
		return nil, errors.New("the command tree has no commands")
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	z, _ := gzip.NewWriterLevel(&out, gzip.BestCompression)
	if _, err := z.Write(compact.Bytes()); err != nil {
		return nil, err
	}
	if err := z.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// firstCommandTree is about when the game's command tree and the data
// generator that writes it out came, in the snapshots before 1.13. Older
// versions have neither.
var firstCommandTree = time.Date(2017, 11, 1, 0, 0, 0, 0, time.UTC)

// serverJar is where Mojang has the server of a game version, and the Java
// it ships with.
type serverJar struct {
	URL, SHA1 string
	Java      int
}

// serverJarOf looks up a version's server jar in Mojang's list of versions.
func (s *Server) serverJarOf(ctx context.Context, version string) (*serverJar, error) {
	body, err := s.catalog.get(ctx, mojangManifest, time.Hour)
	if err != nil {
		return nil, err
	}
	var list struct {
		Versions []struct {
			ID          string    `json:"id"`
			URL         string    `json:"url"`
			ReleaseTime time.Time `json:"releaseTime"`
		} `json:"versions"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, err
	}
	at := ""
	for _, v := range list.Versions {
		if v.ID == version {
			if v.ReleaseTime.Before(firstCommandTree) {
				return nil, ErrNoCommands
			}
			at = v.URL
		}
	}
	if at == "" {
		// Perhaps out a moment ago, and not yet in the list as last read:
		// it is asked about again later.
		return nil, fmt.Errorf("mojang does not list %s", version)
	}
	body, err = s.catalog.get(ctx, at, time.Hour)
	if err != nil {
		return nil, err
	}
	var meta struct {
		Downloads struct {
			Server struct {
				URL  string `json:"url"`
				SHA1 string `json:"sha1"`
			} `json:"server"`
		} `json:"downloads"`
		JavaVersion struct {
			Major int `json:"majorVersion"`
		} `json:"javaVersion"`
	}
	if err := json.Unmarshal(body, &meta); err != nil {
		return nil, err
	}
	jar := meta.Downloads.Server
	if jar.URL == "" || jar.SHA1 == "" {
		return nil, ErrNoCommands
	}
	java := meta.JavaVersion.Major
	if java == 0 {
		java = 8
	}
	return &serverJar{URL: jar.URL, SHA1: jar.SHA1, Java: java}, nil
}

// commandsScript is what the pod runs: it fetches the server jar, has the
// game write its reports, and prints the command tree, gzipped and in
// base64, between two marks. From 1.18 the jar is a bundle that runs the
// part of the game it is told to; before, the game is the jar itself.
const commandsScript = `set -eu
cd /work
curl -fsS --retry 3 --connect-timeout 20 -o server.jar "$JAR_URL"
echo "$JAR_SHA1  server.jar" | sha1sum -c - >/dev/null
if grep -aq META-INF/versions.list server.jar; then
  set -- -DbundlerMainClass=net.minecraft.data.Main -jar server.jar
else
  set -- -cp server.jar net.minecraft.data.Main
fi
if ! java -Xmx512m -XX:+UseSerialGC -Djava.io.tmpdir=/work "$@" --reports >generate.log 2>&1; then
  tail -n 20 generate.log
  exit 1
fi
echo ` + commandsBegin + `
gzip -9c generated/reports/commands.json | base64
echo ` + commandsEnd + `
`

const (
	commandsBegin = "avsys-commands-begin"
	commandsEnd   = "avsys-commands-end"
)

// commandsPod is the pod a version's tree is made in, in a server's
// namespace and owned by it. It goes to the server's node if it can, where
// the image is likely there already.
func commandsPod(mc *avsysv1.MinecraftServer, jar *serverJar, name string) *corev1.Pod {
	id := int64(1000)
	yes, no := true, false
	deadline := int64(commandsTimeout / time.Second)
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: mc.Namespace,
			Name:      name,
			Labels:    map[string]string{"app.kubernetes.io/managed-by": "avsys", commandsLabel: "true"},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: avsysv1.GroupVersion.String(), Kind: "MinecraftServer", Name: mc.Name, UID: mc.UID,
			}},
		},
		Spec: corev1.PodSpec{
			RestartPolicy:                corev1.RestartPolicyNever,
			ActiveDeadlineSeconds:        &deadline,
			AutomountServiceAccountToken: &no,
			EnableServiceLinks:           &no,
			SecurityContext:              &corev1.PodSecurityContext{RunAsUser: &id, RunAsGroup: &id, RunAsNonRoot: &yes},
			Volumes:                      []corev1.Volume{{Name: "work", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}},
			Containers: []corev1.Container{{
				Name:         "commands",
				Image:        controller.ImageForJava(jar.Java),
				Command:      []string{"sh", "-c", commandsScript},
				Env:          []corev1.EnvVar{{Name: "JAR_URL", Value: jar.URL}, {Name: "JAR_SHA1", Value: jar.SHA1}, {Name: "HOME", Value: "/work"}},
				WorkingDir:   "/work",
				VolumeMounts: []corev1.VolumeMount{{Name: "work", MountPath: "/work"}},
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m"), corev1.ResourceMemory: resource.MustParse("512Mi")},
					Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("1Gi")},
				},
				SecurityContext: &corev1.SecurityContext{
					AllowPrivilegeEscalation: &no,
					Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
				},
			}},
		},
	}
	if node := mc.Status.Node; node != "" {
		pod.Spec.Affinity = &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{
			PreferredDuringSchedulingIgnoredDuringExecution: []corev1.PreferredSchedulingTerm{{
				Weight: 100,
				Preference: corev1.NodeSelectorTerm{MatchExpressions: []corev1.NodeSelectorRequirement{{
					Key: "kubernetes.io/hostname", Operator: corev1.NodeSelectorOpIn, Values: []string{node},
				}}},
			}},
		}}
	}
	return pod
}

// commandsLogs reads what a pod printed. Tests stand in for it: a fake
// cluster's logs are always the same words.
var commandsLogs = func(ctx context.Context, s *Server, namespace, pod string) ([]byte, error) {
	return s.Kube.CoreV1().Pods(namespace).GetLogs(pod, &corev1.PodLogOptions{}).DoRaw(ctx)
}

// generateCommands makes a version's tree in a pod in a server's namespace,
// and removes the pod.
func (s *Server) generateCommands(ctx context.Context, mc *avsysv1.MinecraftServer, version string) ([]byte, error) {
	jar, err := s.serverJarOf(ctx, version)
	if err != nil {
		return nil, err
	}
	// One is made at a time, so a pod of this kind there is now is one a
	// panel stopped meanwhile left.
	s.reapCommands(ctx)
	id := make([]byte, 4)
	_, _ = rand.Read(id)
	pod := commandsPod(mc, jar, "avsys-commands-"+hex.EncodeToString(id))
	if err := s.Client.Create(ctx, pod); err != nil {
		return nil, err
	}
	defer func() {
		if err := s.Client.Delete(context.WithoutCancel(ctx), pod); err != nil && !apierrors.IsNotFound(err) {
			s.Log.Warn("could not remove the pod a list of commands was made in", "namespace", pod.Namespace, "pod", pod.Name, "error", err)
		}
	}()
	deadline := time.Now().Add(commandsTimeout)
	for {
		got := &corev1.Pod{}
		if err := s.nodeReader().Get(ctx, client.ObjectKeyFromObject(pod), got); err != nil {
			return nil, err
		}
		switch got.Status.Phase {
		case corev1.PodSucceeded:
			logs, err := commandsLogs(ctx, s, pod.Namespace, pod.Name)
			if err != nil {
				return nil, err
			}
			return readCommandsLog(logs)
		case corev1.PodFailed:
			logs, _ := commandsLogs(ctx, s, pod.Namespace, pod.Name)
			return nil, fmt.Errorf("the game could not write its commands: %s", lastLines(logs, 5))
		}
		if time.Now().After(deadline) {
			return nil, errors.New("the game took too long to write its commands")
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(commandsPoll):
		}
	}
}

// commandsPoll is how often the pod is looked at.
var commandsPoll = 2 * time.Second

// readCommandsLog takes the command tree out of what the pod printed.
func readCommandsLog(logs []byte) ([]byte, error) {
	var encoded strings.Builder
	in, found, ended := false, false, false
	sc := bufio.NewScanner(bytes.NewReader(logs))
	sc.Buffer(make([]byte, 64*1024), 16<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case line == commandsBegin:
			in, found = true, true
			encoded.Reset()
		case line == commandsEnd && in:
			in, ended = false, true
		case in:
			encoded.WriteString(line)
		}
	}
	if !found || !ended {
		return nil, errors.New("the game printed no commands")
	}
	gz, err := base64.StdEncoding.DecodeString(encoded.String())
	if err != nil {
		return nil, fmt.Errorf("reading the commands printed: %w", err)
	}
	z, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		return nil, fmt.Errorf("reading the commands printed: %w", err)
	}
	// A tree is a megabyte or so; one far bigger is not one.
	raw, err := io.ReadAll(io.LimitReader(z, 64<<20))
	if err != nil {
		return nil, fmt.Errorf("reading the commands printed: %w", err)
	}
	return raw, nil
}

func lastLines(b []byte, n int) string {
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " | ")
}

// reapCommands removes the pods trees were being made in when the panel
// stopped.
func (s *Server) reapCommands(ctx context.Context) {
	var list corev1.PodList
	if err := s.nodeReader().List(ctx, &list, client.MatchingLabels{commandsLabel: "true"}); err != nil {
		s.Log.Warn("could not look for pods left from making lists of commands", "error", err)
		return
	}
	for i := range list.Items {
		if err := s.Client.Delete(ctx, &list.Items[i]); err != nil && !apierrors.IsNotFound(err) {
			s.Log.Warn("could not remove a pod left from making a list of commands", "namespace", list.Items[i].Namespace, "pod", list.Items[i].Name, "error", err)
		}
	}
}
