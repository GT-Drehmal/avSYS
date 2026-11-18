package api

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
)

// commandsFixture is the commands.json the game's data generator wrote for
// 1.21.4, as recorded for the browser tests.
func commandsFixture(t *testing.T) []byte {
	t.Helper()
	f, err := os.Open("../../web/tests/upstream/commands/1.21.4.json.gz")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	z, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(z)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// commandsPrinted is what the pod prints for a tree: the game's own lines
// first, then the tree between the marks, wrapped as base64 wraps it.
func commandsPrinted(t *testing.T, raw []byte) []byte {
	t.Helper()
	var gz bytes.Buffer
	z := gzip.NewWriter(&gz)
	_, _ = z.Write(raw)
	_ = z.Close()
	enc := base64.StdEncoding.EncodeToString(gz.Bytes())
	var out strings.Builder
	out.WriteString("[12:00:00] [main/INFO]: Starting provider: Command Syntax\n")
	out.WriteString(commandsBegin + "\n")
	for len(enc) > 76 {
		out.WriteString(enc[:76] + "\n")
		enc = enc[76:]
	}
	out.WriteString(enc + "\n" + commandsEnd + "\n")
	return []byte(out.String())
}

func TestReadCommandsLog(t *testing.T) {
	raw := commandsFixture(t)
	got, err := readCommandsLog(commandsPrinted(t, raw))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, raw) {
		t.Error("the tree read back is not the one printed")
	}
	// Long lines, as a runtime that does not wrap them keeps them.
	one := strings.ReplaceAll(string(commandsPrinted(t, raw)), "\n", "")
	one = strings.ReplaceAll(one, commandsBegin, "\n"+commandsBegin+"\n")
	one = strings.ReplaceAll(one, commandsEnd, "\n"+commandsEnd+"\n")
	if got, err := readCommandsLog([]byte(one)); err != nil || !bytes.Equal(got, raw) {
		t.Errorf("a tree on one line was not read: %v", err)
	}
	for name, logs := range map[string]string{
		"nothing printed":   "",
		"cut short":         commandsBegin + "\nH4sIAAAA\n",
		"not base64":        commandsBegin + "\n!!!\n" + commandsEnd + "\n",
		"not gzip":          commandsBegin + "\naGVsbG8=\n" + commandsEnd + "\n",
		"only the game log": "[12:00:00] [main/INFO]: All providers took: 1200 ms\n",
	} {
		if _, err := readCommandsLog([]byte(logs)); err == nil {
			t.Errorf("%s: read as a tree", name)
		}
	}
}

func TestPackCommands(t *testing.T) {
	raw := commandsFixture(t)
	gz, err := packCommands(raw)
	if err != nil {
		t.Fatal(err)
	}
	z, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		t.Fatal(err)
	}
	packed, _ := io.ReadAll(z)
	if bytes.Contains(packed, []byte("\n")) || len(packed) >= len(raw) {
		t.Error("the tree is not compacted")
	}
	var a, b any
	_ = json.Unmarshal(raw, &a)
	_ = json.Unmarshal(packed, &b)
	if ja, _ := json.Marshal(a); string(ja) != string(must(json.Marshal(b))) {
		t.Error("compacting changed the tree")
	}
	for _, bad := range []string{``, `[]`, `{"type":"literal"}`, `{"type":"root","children":{}}`, `{"type":"root"`} {
		if _, err := packCommands([]byte(bad)); err == nil {
			t.Errorf("%q taken for a tree", bad)
		}
	}
}

func must(b []byte, err error) []byte {
	if err != nil {
		panic(err)
	}
	return b
}

// getCommandTree asks for a server's command tree as a browser does.
func getCommandTree(p *panel, who string, header http.Header) *httptest.ResponseRecorder {
	p.t.Helper()
	req := httptest.NewRequest("GET", "/api/v1/orgs/alpha/servers/world/console/commands", nil)
	req.Header.Set("Authorization", "Bearer "+p.tokens[who])
	for k, v := range header {
		req.Header[k] = v
	}
	rec := httptest.NewRecorder()
	p.h.ServeHTTP(rec, req)
	return rec
}

// waitCommandTree asks until the tree is no longer being made.
func waitCommandTree(p *panel, header http.Header) *httptest.ResponseRecorder {
	p.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		rec := getCommandTree(p, "operator", header)
		if rec.Code != http.StatusAccepted || time.Now().After(deadline) {
			return rec
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestCommandTreeIsMadeOnceAndKept(t *testing.T) {
	p := newPanel(t)
	dir := t.TempDir()
	p.srv.Config.CommandsDir = dir
	raw := commandsFixture(t)
	made := make(chan string, 10)
	p.srv.CommandTree = func(_ context.Context, version string) ([]byte, error) {
		made <- version
		return raw, nil
	}

	first := getCommandTree(p, "operator", nil)
	if first.Code != http.StatusAccepted || !strings.Contains(first.Body.String(), `"making":true`) {
		t.Fatalf("first ask: %d %s, want 202 while it is made", first.Code, first.Body)
	}
	rec := waitCommandTree(p, http.Header{"Accept-Encoding": {"gzip, deflate, br"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
	if v := <-made; v != "1.21.4" {
		t.Errorf("made for %q, want the server's version", v)
	}
	if rec.Header().Get("Content-Encoding") != "gzip" || rec.Header().Get("X-Game-Version") != "1.21.4" {
		t.Errorf("headers: %v", rec.Header())
	}
	z, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatal(err)
	}
	var tree struct {
		Type     string                     `json:"type"`
		Children map[string]json.RawMessage `json:"children"`
	}
	if err := json.NewDecoder(z).Decode(&tree); err != nil || tree.Type != "root" || tree.Children["gamemode"] == nil {
		t.Fatalf("not the tree: %v %+v", err, tree.Type)
	}

	// A client that takes no gzip is sent it plain.
	plain := getCommandTree(p, "operator", nil)
	if plain.Header().Get("Content-Encoding") != "" || !json.Valid(plain.Body.Bytes()) {
		t.Errorf("plain: %v, valid %v", plain.Header(), json.Valid(plain.Body.Bytes()))
	}
	// One the browser has is not sent again.
	etag := rec.Header().Get("ETag")
	if again := getCommandTree(p, "operator", http.Header{"If-None-Match": {etag}}); again.Code != http.StatusNotModified || again.Body.Len() != 0 {
		t.Errorf("asked again with its tag: %d", again.Code)
	}
	select {
	case v := <-made:
		t.Errorf("made again for %q", v)
	default:
	}

	// Kept on the panel's volume: a panel started again has it at once,
	// and makes nothing.
	files, _ := os.ReadDir(dir)
	if len(files) != 1 || files[0].Name() != "1.21.4.json.gz" {
		t.Fatalf("kept as %v", files)
	}
	q := newPanel(t)
	q.srv.Config.CommandsDir = dir
	q.srv.CommandTree = func(context.Context, string) ([]byte, error) {
		t.Error("made again after a restart")
		return nil, errors.New("no")
	}
	if rec := getCommandTree(q, "operator", nil); rec.Code != http.StatusOK || rec.Header().Get("ETag") != etag {
		t.Errorf("after a restart: %d %s", rec.Code, rec.Header().Get("ETag"))
	}
}

func TestCommandTreeThatCannotBeMade(t *testing.T) {
	p := newPanel(t)
	tries := 0
	p.srv.CommandTree = func(context.Context, string) ([]byte, error) {
		tries++
		return nil, ErrNoCommands
	}
	if rec := waitCommandTree(p, nil); rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "no_commands") {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
	if rec := getCommandTree(p, "operator", nil); rec.Code != http.StatusNotFound || tries != 1 {
		t.Errorf("asked again: %d, made %d times", rec.Code, tries)
	}

	// One that failed for a while is tried again later; what was made
	// that is not a tree is not kept.
	q := newPanel(t)
	q.srv.CommandTree = func(context.Context, string) ([]byte, error) { return []byte(`{"type":"root"}`), nil }
	if rec := waitCommandTree(q, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
	q.srv.commands.mu.Lock()
	until := q.srv.commands.failed["1.21.4"]
	q.srv.commands.failed["1.21.4"] = time.Now().Add(-time.Second)
	q.srv.commands.mu.Unlock()
	if d := time.Until(until); d < commandsRetry-time.Minute || d > commandsRetry {
		t.Errorf("tried again in %v, want %v", d, commandsRetry)
	}
	q.srv.CommandTree = func(context.Context, string) ([]byte, error) { return commandsFixture(t), nil }
	if rec := waitCommandTree(q, nil); rec.Code != http.StatusOK {
		t.Errorf("tried again: %d", rec.Code)
	}
}

// The version a server's commands are of.
func TestCommandsVersion(t *testing.T) {
	p := newPanel(t)
	p.srv.catalog.http.Transport = upstream
	ctx := context.Background()
	for _, c := range []struct {
		spec   avsysv1.MinecraftServerSpec
		status string
		want   string
	}{
		{avsysv1.MinecraftServerSpec{Version: "1.21.4"}, "Paper 1.21.4", "1.21.4"},
		{avsysv1.MinecraftServerSpec{Version: "1.20.1"}, "", "1.20.1"},
		{avsysv1.MinecraftServerSpec{Version: "LATEST"}, "Paper 26.2", "26.2"},
		{avsysv1.MinecraftServerSpec{Version: "LATEST"}, "", "26.3"},
		{avsysv1.MinecraftServerSpec{Modpack: &avsysv1.ModpackSpec{GameVersion: "1.20.1"}}, "1.20.1", "1.20.1"},
	} {
		mc := &avsysv1.MinecraftServer{Spec: c.spec, Status: avsysv1.MinecraftServerStatus{Version: c.status}}
		if got := p.srv.commandsVersion(ctx, mc); got != c.want {
			t.Errorf("%+v %q: %q, want %q", c.spec, c.status, got, c.want)
		}
	}
}

// mojangVersions answers Mojang's list of versions and the page of each.
var mojangVersions = offline{
	mojangManifest: `{"latest":{"release":"1.21.4"},"versions":[
		{"id":"1.21.4","type":"release","url":"https://piston-meta.test/1.21.4.json","releaseTime":"2024-12-03T10:12:57+00:00"},
		{"id":"1.17.1","type":"release","url":"https://piston-meta.test/1.17.1.json","releaseTime":"2021-07-06T12:01:34+00:00"},
		{"id":"1.12.2","type":"release","url":"https://piston-meta.test/1.12.2.json","releaseTime":"2017-09-18T08:39:46+00:00"},
		{"id":"clientonly","type":"release","url":"https://piston-meta.test/clientonly.json","releaseTime":"2024-01-01T00:00:00+00:00"}]}`,
	"https://piston-meta.test/1.21.4.json":     `{"downloads":{"server":{"sha1":"4707d00eb834b446575d89a61a11b5d548d8c001","url":"https://piston-data.test/server-1.21.4.jar"}},"javaVersion":{"majorVersion":21}}`,
	"https://piston-meta.test/1.17.1.json":     `{"downloads":{"server":{"sha1":"a16d67e5807f57fc4e550299cf20226194497dc2","url":"https://piston-data.test/server-1.17.1.jar"}},"javaVersion":{"majorVersion":16}}`,
	"https://piston-meta.test/clientonly.json": `{"downloads":{},"javaVersion":{"majorVersion":21}}`,
}

func TestServerJarOf(t *testing.T) {
	p := newPanel(t)
	p.srv.catalog.http.Transport = mojangVersions
	ctx := context.Background()
	jar, err := p.srv.serverJarOf(ctx, "1.21.4")
	if err != nil || jar.URL != "https://piston-data.test/server-1.21.4.jar" || jar.SHA1 != "4707d00eb834b446575d89a61a11b5d548d8c001" || jar.Java != 21 {
		t.Errorf("1.21.4: %+v %v", jar, err)
	}
	if jar, err := p.srv.serverJarOf(ctx, "1.17.1"); err != nil || jar.Java != 16 {
		t.Errorf("1.17.1: %+v %v", jar, err)
	}
	// Older than the command tree, or with no server: there is none, nor
	// will be.
	for _, v := range []string{"1.12.2", "clientonly"} {
		if _, err := p.srv.serverJarOf(ctx, v); !errors.Is(err, ErrNoCommands) {
			t.Errorf("%s: %v, want none", v, err)
		}
	}
	// One not listed may be listed later.
	if _, err := p.srv.serverJarOf(ctx, "26.9"); err == nil || errors.Is(err, ErrNoCommands) {
		t.Errorf("26.9: %v, want an error to try again after", err)
	}
}

func TestCommandsPod(t *testing.T) {
	mc := &avsysv1.MinecraftServer{}
	mc.Namespace, mc.Name, mc.UID = "mc-alpha", "world", "uid-1"
	mc.Status.Node = "node-a"
	pod := commandsPod(mc, &serverJar{URL: "https://piston-data.test/server.jar", SHA1: "abc", Java: 16}, "avsys-commands-1")
	c := pod.Spec.Containers[0]
	if pod.Namespace != "mc-alpha" || c.Image != "itzg/minecraft-server:java17" {
		t.Errorf("in %s, image %s", pod.Namespace, c.Image)
	}
	if o := pod.OwnerReferences; len(o) != 1 || o[0].Name != "world" || o[0].UID != "uid-1" || o[0].Kind != "MinecraftServer" || o[0].Controller != nil {
		t.Errorf("owned by %+v, want the server, not as its controller", o)
	}
	if pod.Labels[commandsLabel] != "true" || pod.Labels["app.kubernetes.io/managed-by"] != "avsys" || pod.Labels[avsysv1.LabelServer] != "" {
		t.Errorf("labels %v", pod.Labels)
	}
	env := map[string]string{}
	for _, e := range c.Env {
		env[e.Name] = e.Value
	}
	if env["JAR_URL"] != "https://piston-data.test/server.jar" || env["JAR_SHA1"] != "abc" {
		t.Errorf("env %v", env)
	}
	if pod.Spec.RestartPolicy != corev1.RestartPolicyNever || pod.Spec.ActiveDeadlineSeconds == nil || *pod.Spec.SecurityContext.RunAsNonRoot != true {
		t.Error("not a pod that runs once, as no one")
	}
	if c.Resources.Limits.Memory().Value() != 1<<30 {
		t.Errorf("memory limit %v", c.Resources.Limits.Memory())
	}
	terms := pod.Spec.Affinity.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution
	if len(terms) != 1 || terms[0].Preference.MatchExpressions[0].Values[0] != "node-a" {
		t.Errorf("affinity %+v, want the server's node preferred", terms)
	}
	mc.Status.Node = ""
	if commandsPod(mc, &serverJar{Java: 21}, "x").Spec.Affinity != nil {
		t.Error("a node preferred for a server on none")
	}
}

// The tree made in a pod, as a cluster runs it: the pod is made, runs to
// the end, and is read and removed. Pods left from before are removed first.
func TestGenerateCommands(t *testing.T) {
	p := newPanel(t)
	p.srv.catalog.http.Transport = mojangVersions
	// Not the ten minutes a cluster is given, should a pod not finish.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	raw := commandsFixture(t)
	printed := commandsPrinted(t, raw)
	read := ""
	was := commandsLogs
	commandsLogs = func(_ context.Context, _ *Server, namespace, pod string) ([]byte, error) {
		read = namespace + "/" + pod
		return printed, nil
	}
	poll := commandsPoll
	commandsPoll = 10 * time.Millisecond
	t.Cleanup(func() { commandsLogs, commandsPoll = was, poll })

	left := commandsPod(&avsysv1.MinecraftServer{}, &serverJar{Java: 21}, "avsys-commands-left")
	left.Namespace = "mc-beta"
	if err := p.srv.Client.Create(ctx, left); err != nil {
		t.Fatal(err)
	}

	mc := &avsysv1.MinecraftServer{}
	if err := p.srv.Client.Get(ctx, client.ObjectKey{Namespace: "mc-alpha", Name: "world"}, mc); err != nil {
		t.Fatal(err)
	}
	// The cluster: each pod made runs to the end, as phase says.
	finish := func(phase corev1.PodPhase) (seen chan *corev1.Pod) {
		seen = make(chan *corev1.Pod, 1)
		go func() {
			for range 500 {
				var pods corev1.PodList
				_ = p.srv.Client.List(ctx, &pods, client.InNamespace("mc-alpha"), client.MatchingLabels{commandsLabel: "true"})
				if len(pods.Items) == 1 {
					pod := pods.Items[0]
					seen <- pod.DeepCopy()
					pod.Status.Phase = phase
					_ = p.srv.Client.Status().Update(ctx, &pod)
					return
				}
				time.Sleep(10 * time.Millisecond)
			}
			close(seen)
		}()
		return seen
	}

	seen := finish(corev1.PodSucceeded)
	got, err := p.srv.generateCommands(ctx, mc, "1.21.4")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, raw) {
		t.Error("not the tree the pod printed")
	}
	pod := <-seen
	if pod == nil {
		t.Fatal("no pod was made")
	}
	if read != "mc-alpha/"+pod.Name || pod.Spec.Containers[0].Image != "itzg/minecraft-server:java21" {
		t.Errorf("read %s, pod %s with %s", read, pod.Name, pod.Spec.Containers[0].Image)
	}
	if err := p.srv.Client.Get(ctx, client.ObjectKeyFromObject(pod), &corev1.Pod{}); !apierrors.IsNotFound(err) {
		t.Errorf("the pod is still there: %v", err)
	}
	if err := p.srv.Client.Get(ctx, client.ObjectKeyFromObject(left), &corev1.Pod{}); !apierrors.IsNotFound(err) {
		t.Errorf("the pod left from before is still there: %v", err)
	}

	// A pod that fails says why, from the end of its log, and is removed.
	seen = finish(corev1.PodFailed)
	commandsLogs = func(context.Context, *Server, string, string) ([]byte, error) {
		return []byte("[12:00:00] [main/INFO]: Starting\nException in thread \"main\" java.lang.OutOfMemoryError\n"), nil
	}
	if _, err := p.srv.generateCommands(ctx, mc, "1.21.4"); err == nil || !strings.Contains(err.Error(), "OutOfMemoryError") {
		t.Errorf("a failed pod: %v", err)
	}
	if pod := <-seen; pod != nil {
		if err := p.srv.Client.Get(ctx, client.ObjectKeyFromObject(pod), &corev1.Pod{}); !apierrors.IsNotFound(err) {
			t.Errorf("the failed pod is still there: %v", err)
		}
	}

	// A version there is none of makes no pod.
	if _, err := p.srv.generateCommands(ctx, mc, "1.12.2"); !errors.Is(err, ErrNoCommands) {
		t.Errorf("1.12.2: %v", err)
	}
	var pods corev1.PodList
	_ = p.srv.Client.List(ctx, &pods, client.MatchingLabels{commandsLabel: "true"})
	if len(pods.Items) != 0 {
		t.Errorf("%d pods left", len(pods.Items))
	}
}

func TestAcceptsGzip(t *testing.T) {
	for header, want := range map[string]bool{
		"":                     false,
		"gzip":                 true,
		"gzip, deflate, br":    true,
		"br;q=1.0, gzip;q=0.8": true,
		"gzip;q=0":             false,
		"identity":             false,
	} {
		r := httptest.NewRequest("GET", "/", nil)
		r.Header.Set("Accept-Encoding", header)
		if got := acceptsGzip(r); got != want {
			t.Errorf("%q: %v", header, got)
		}
	}
}
