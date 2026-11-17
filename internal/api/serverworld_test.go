package api

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubefake "k8s.io/client-go/kubernetes/fake"
	"sigs.k8s.io/controller-runtime/pkg/client"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
	"github.com/GT-Drehmal/avSYS/internal/controller"
	"github.com/GT-Drehmal/avSYS/internal/sshgw"
)

// events is what happened, in order, to a game and its workspace.
type events struct {
	mu   sync.Mutex
	seen []string
}

func (e *events) add(s string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.seen = append(e.seen, s)
}

func (e *events) list() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.seen)
}

// shellWorkspace stands in for a server's workspace: the panel's commands
// run in a local sh, with /data and /tmp/avsys in folders of the test's own.
// A tar of the world is noted, with whether the game's saves were said to be
// held as it ran.
type shellWorkspace struct {
	data, marks string
	events      *events
	// stall, where set, holds back what a tar of the world writes after its
	// first bytes until it is closed; stalled is closed once it does.
	stall, stalled chan struct{}
	once           sync.Once
}

func (w *shellWorkspace) DialPod(_ context.Context, _, _ string, _ int) (net.Conn, error) {
	// A real connection: over net.Pipe both ends of an SSH handshake write
	// their version first, and each waits for the other to read.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	defer ln.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			close(accepted)
			return
		}
		accepted <- c
	}()
	ours, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		return nil, err
	}
	theirs, ok := <-accepted
	if !ok {
		ours.Close()
		return nil, errors.New("the workspace did not answer")
	}
	cfg := &ssh.ServerConfig{NoClientAuth: true}
	cfg.AddHostKey(testSigner())
	go func() {
		conn, chans, reqs, err := ssh.NewServerConn(theirs, cfg)
		if err != nil {
			return
		}
		defer conn.Close()
		go ssh.DiscardRequests(reqs)
		for nc := range chans {
			ch, chReqs, err := nc.Accept()
			if err != nil {
				continue
			}
			go w.session(ch, chReqs)
		}
	}()
	return ours, nil
}

func (w *shellWorkspace) session(ch ssh.Channel, reqs <-chan *ssh.Request) {
	defer ch.Close()
	for req := range reqs {
		switch req.Type {
		case "env":
			_ = req.Reply(true, nil)
		case "exec":
			var cmd struct{ Command string }
			_ = ssh.Unmarshal(req.Payload, &cmd)
			_ = req.Reply(true, nil)
			status := w.run(cmd.Command, ch, reqs)
			_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{status}))
			return
		default:
			_ = req.Reply(false, nil)
		}
	}
}

func (w *shellWorkspace) run(command string, ch ssh.Channel, reqs <-chan *ssh.Request) uint32 {
	local := strings.ReplaceAll(strings.ReplaceAll(command, "/tmp/avsys", w.marks), dataRoot, w.data)
	tar := strings.Contains(command, "tar -cf")
	if tar {
		_, err := os.Stat(filepath.Join(w.marks, "saving-paused"))
		w.events.add(fmt.Sprintf("tar, saves held: %v", err == nil))
	}
	cmd := exec.Command("sh", "-c", local)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	killed := make(chan struct{})
	var out io.Writer = ch
	if tar && w.stall != nil {
		out = &stallWriter{w: ch, left: 4096, stall: w.stall, killed: killed, stalled: func() { w.once.Do(func() { close(w.stalled) }) }}
	}
	cmd.Stdout, cmd.Stderr = out, ch.Stderr()
	if err := cmd.Start(); err != nil {
		return 127
	}
	// A signal, or the session closed, ends it.
	done := make(chan struct{})
	go func() {
		kill := func() {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			close(killed)
		}
		for {
			select {
			case req, ok := <-reqs:
				if !ok {
					kill()
					return
				}
				if req.WantReply {
					_ = req.Reply(false, nil)
				}
				if req.Type == "signal" {
					kill()
					return
				}
			case <-done:
				return
			}
		}
	}()
	err := cmd.Wait()
	close(done)
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return uint32(max(exit.ExitCode(), 1))
	} else if err != nil {
		return 1
	}
	return 0
}

// stallWriter writes the first bytes it is given, then waits.
type stallWriter struct {
	w             io.Writer
	left          int
	stall, killed chan struct{}
	stalled       func()
}

func (s *stallWriter) Write(b []byte) (int, error) {
	if s.left > 0 {
		n := min(len(b), s.left)
		s.left -= n
		if _, err := s.w.Write(b[:n]); err != nil {
			return 0, err
		}
		if n == len(b) {
			return n, nil
		}
		b = b[n:]
		s.stalled()
		select {
		case <-s.stall:
		case <-s.killed:
			return n, errors.New("killed")
		}
		m, err := s.w.Write(b)
		return n + m, err
	}
	return s.w.Write(b)
}

func testSigner() ssh.Signer {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		panic(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		panic(err)
	}
	return signer
}

// worldPanel is a panel whose server "world", shown as Survival SMP, has the
// files given in its workspace, and a game that notes what it is told.
func worldPanel(t *testing.T, files map[string]string) (*panel, *shellWorkspace, *rconGame) {
	t.Helper()
	if _, err := exec.LookPath("tar"); err != nil {
		t.Skip("tar is not installed")
	}
	p := newPanel(t)
	mc := p.server("world")
	mc.Spec.DisplayName = "Survival SMP"
	if err := p.srv.Client.Update(context.Background(), mc); err != nil {
		t.Fatal(err)
	}
	ev := &events{}
	ws := &shellWorkspace{data: filepath.Join(t.TempDir(), "data"), marks: t.TempDir(), events: ev}
	writeFiles(t, ws.data, files)
	p.srv.Gateway = &sshgw.Gateway{Dialer: ws, WorkspaceKey: testSigner(), WorkspaceTries: 1, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	p.srv.Kube = kubefake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: "mc-alpha", Name: controller.RCONSecretName("world")},
		Data:       map[string][]byte{"password": []byte("secret")},
	})
	game := &rconGame{password: "secret", answer: func(command string) string {
		ev.add("rcon " + command)
		return ""
	}}
	p.srv.Dialer = game
	return p, ws, game
}

// download asks for the world of server "world" as who, over a real
// connection, which the download may cut off.
func download(t *testing.T, p *panel, ctx context.Context, who string) (*http.Response, error) {
	t.Helper()
	srv := httptest.NewServer(p.h)
	t.Cleanup(srv.Close)
	req, err := http.NewRequestWithContext(ctx, "GET", srv.URL+"/api/v1/orgs/alpha/servers/world/world/archive", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+p.tokens[who])
	return http.DefaultClient.Do(req)
}

func fetchWorld(t *testing.T, p *panel) (map[string]string, *http.Response) {
	t.Helper()
	res, err := download(t, p, context.Background(), "operator")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("the download was cut off: %v", err)
	}
	if res.StatusCode != 200 {
		t.Fatalf("%d %s", res.StatusCode, body)
	}
	return unzipped(t, body), res
}

func sameFiles(t *testing.T, got, want map[string]string) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("the zip holds %d files, want %d: %v", len(got), len(want), got)
	}
	for name, content := range want {
		if got[name] != content {
			t.Errorf("%s: %q, want %q", name, got[name], content)
		}
	}
}

// A stopped server's world, kept as a single-player save has it, Nether and
// End within: in a folder named after the server, with nothing else of the
// server's files. Nothing is said to the game, which is not running.
func TestDownloadAServersWorld(t *testing.T) {
	p, _, _ := worldPanel(t, map[string]string{
		"server.properties":            "motd=hi\n",
		"world/level.dat":              "the level",
		"world/region/r.0.0.mca":       "a region",
		"world/DIM-1/region/r.0.0.mca": "the nether",
		"world/DIM1/region/r.0.0.mca":  "the end",
		"world/datapacks/a.zip":        "a datapack",
		"plugins/a.jar":                "not the world",
		"other/level.dat":              "another folder",
		"world_backup/level.dat":       "beside it, but not of it",
	})
	got, res := fetchWorld(t, p)
	if cd := res.Header.Get("Content-Disposition"); !strings.Contains(cd, "Survival%20SMP%20world.zip") {
		t.Errorf("named %q", cd)
	}
	sameFiles(t, got, map[string]string{
		"Survival SMP/level.dat":              "the level",
		"Survival SMP/region/r.0.0.mca":       "a region",
		"Survival SMP/DIM-1/region/r.0.0.mca": "the nether",
		"Survival SMP/DIM1/region/r.0.0.mca":  "the end",
		"Survival SMP/datapacks/a.zip":        "a datapack",
	})
	if ev := p.worldEvents(); !slices.Equal(ev, []string{"tar, saves held: false"}) {
		t.Errorf("a stopped server: %v", ev)
	}
}

// events is what the server's game and workspace saw.
func (p *panel) worldEvents() []string {
	return p.srv.Gateway.Dialer.(*shellWorkspace).events.list()
}

// A world kept as Paper keeps it, under the level-name its properties give,
// its Nether and End in folders beside it, is put back together as a
// single-player save has it: what else those folders hold is the server's.
func TestDownloadASplitWorld(t *testing.T) {
	p, _, _ := worldPanel(t, map[string]string{
		"server.properties":                    "level-name=island\r\nmotd=hi\n",
		"island/level.dat":                     "the level",
		"island/region/r.0.0.mca":              "a region",
		"island_nether/level.dat":              "the nether's own",
		"island_nether/DIM-1/region/r.0.0.mca": "the nether",
		"island_the_end/uid.dat":               "the end's own",
		"island_the_end/DIM1/region/r.0.0.mca": "the end",
		"world/level.dat":                      "not the server's world",
	})
	got, _ := fetchWorld(t, p)
	sameFiles(t, got, map[string]string{
		"Survival SMP/level.dat":              "the level",
		"Survival SMP/region/r.0.0.mca":       "a region",
		"Survival SMP/DIM-1/region/r.0.0.mca": "the nether",
		"Survival SMP/DIM1/region/r.0.0.mca":  "the end",
	})
}

// A server whose game has not made its world yet has none to send.
func TestDownloadNoWorldYet(t *testing.T) {
	p, _, _ := worldPanel(t, map[string]string{"server.properties": "motd=hi\n"})
	if code, e := p.do("operator", "GET", "/orgs/alpha/servers/world/world/archive", ""); code != 404 || e != "no_world" {
		t.Errorf("%d %s", code, e)
	}
	if ev := p.worldEvents(); len(ev) != 0 {
		t.Errorf("%v", ev)
	}
}

// A running game writes its world out and holds its saves while it is read,
// and saves again once it is sent.
func TestDownloadHoldsTheGamesSaves(t *testing.T) {
	p, _, _ := worldPanel(t, map[string]string{"world/level.dat": "the level"})
	p.setPhase("alpha", "world", avsysv1.PhaseRunning)
	got, _ := fetchWorld(t, p)
	sameFiles(t, got, map[string]string{"Survival SMP/level.dat": "the level"})
	if ev := p.worldEvents(); !slices.Equal(ev, []string{"rcon save-off", "rcon save-all flush", "tar, saves held: true", "rcon save-on"}) {
		t.Errorf("%v", ev)
	}
	p.savesBack(t)
}

// savesBack waits for the game to be saving again, and the server let go of.
func (p *panel) savesBack(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		mc := p.server("world")
		_, paused := mc.Annotations[avsysv1.AnnotationSavingPaused]
		ev := p.worldEvents()
		held := slices.Contains(ev, "rcon save-off")
		back := !held || (len(ev) > 0 && ev[len(ev)-1] == "rcon save-on")
		ws := p.srv.Gateway.Dialer.(*shellWorkspace)
		_, marked := os.Stat(filepath.Join(ws.marks, "saving-paused"))
		if back && !paused && os.IsNotExist(marked) && !p.srv.ops.busy("mc-alpha", "world") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("saving not back: events %v, noted %v, marked %v, held %v", ev, paused, marked, p.srv.ops.busy("mc-alpha", "world"))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A download given up on part of the way lets the game save again. While it
// lasted the server was held: nothing else began, and the game's own
// commands for saving were turned away.
func TestDownloadCancelledLetsTheGameSave(t *testing.T) {
	p, ws, _ := worldPanel(t, map[string]string{"world/level.dat": "the level", "world/region/r.0.0.mca": strings.Repeat("region ", 4096)})
	ws.stall, ws.stalled = make(chan struct{}), make(chan struct{})
	p.setPhase("alpha", "world", avsysv1.PhaseRunning)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Answered once some of the zip is made, which the stall may hold back.
	answered := make(chan struct{})
	go func() {
		defer close(answered)
		if res, err := download(t, p, ctx, "operator"); err == nil {
			_, _ = io.Copy(io.Discard, res.Body)
			res.Body.Close()
		}
	}()
	<-ws.stalled
	if _, ok := p.srv.ops.begin("mc-alpha", "alpha", "world", kindSnapshot, ""); ok {
		t.Error("a snapshot began while the world was sent")
	}
	if code, e := p.do("operator", "POST", "/orgs/alpha/servers/world/console", `{"command":"save-on"}`); code != 409 || e != "snapshotting" {
		t.Errorf("the game was told to save while its world was sent: %d %s", code, e)
	}
	cancel()
	<-answered
	p.savesBack(t)
	if ev := p.worldEvents(); !slices.Equal(ev, []string{"rcon save-off", "rcon save-all flush", "tar, saves held: true", "rcon save-on"}) {
		t.Errorf("%v", ev)
	}
}

// A game restarted while its world is read writes it again: the download is
// cut off rather than finished with a world that mixes the two.
func TestDownloadAcrossARestartIsCutOff(t *testing.T) {
	p, ws, _ := worldPanel(t, map[string]string{"world/level.dat": "the level", "world/region/r.0.0.mca": strings.Repeat("region ", 4096)})
	ws.stall, ws.stalled = make(chan struct{}), make(chan struct{})
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "mc-alpha", Name: "world"},
		Status:     corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{Name: controller.MinecraftContainer, ContainerID: "one"}}},
	}
	if err := p.srv.Client.Create(context.Background(), pod); err != nil {
		t.Fatal(err)
	}
	p.setPhase("alpha", "world", avsysv1.PhaseRunning)
	type result struct {
		status int
		body   []byte
		err    error
	}
	done := make(chan result, 1)
	go func() {
		res, err := download(t, p, context.Background(), "operator")
		if err != nil {
			done <- result{err: err}
			return
		}
		defer res.Body.Close()
		body, err := io.ReadAll(res.Body)
		done <- result{res.StatusCode, body, err}
	}()
	<-ws.stalled
	if err := p.srv.Client.Get(context.Background(), client.ObjectKeyFromObject(pod), pod); err != nil {
		t.Fatal(err)
	}
	pod.Status.ContainerStatuses[0].RestartCount, pod.Status.ContainerStatuses[0].ContainerID = 1, "two"
	if err := p.srv.Client.Status().Update(context.Background(), pod); err != nil {
		t.Fatal(err)
	}
	if now := p.srv.gameInstance(context.Background(), p.server("world")); now != "/1/two" {
		t.Fatalf("the game was not restarted: %q", now)
	}
	close(ws.stall)
	// Refused, if none of it had gone yet, or cut off.
	got := <-done
	if got.err == nil && got.status == 200 {
		if _, err := zip.NewReader(bytes.NewReader(got.body), int64(len(got.body))); err == nil {
			t.Error("a world read across a restart was sent whole")
		}
	}
	p.savesBack(t)
}

// One download at a time: a second would write the world out under the
// first, and let the game save again before the first was done.
func TestOneDownloadAtATime(t *testing.T) {
	p, ws, _ := worldPanel(t, map[string]string{"world/level.dat": "the level", "world/region/r.0.0.mca": strings.Repeat("region ", 4096)})
	ws.stall, ws.stalled = make(chan struct{}), make(chan struct{})
	p.setPhase("alpha", "world", avsysv1.PhaseRunning)
	first := make(chan []byte, 1)
	go func() {
		res, err := download(t, p, context.Background(), "operator")
		if err != nil {
			first <- nil
			return
		}
		defer res.Body.Close()
		body, _ := io.ReadAll(res.Body)
		first <- body
	}()
	<-ws.stalled
	if code, e := p.do("admin", "GET", "/orgs/alpha/servers/world/world/archive", ""); code != 409 || e != "busy" {
		t.Errorf("a second download: %d %s", code, e)
	}
	close(ws.stall)
	got := unzipped(t, <-first)
	if got["Survival SMP/level.dat"] != "the level" {
		t.Errorf("the first: %v", got)
	}
	if ev := p.worldEvents(); !slices.Equal(ev, []string{"rcon save-off", "rcon save-all flush", "tar, saves held: true", "rcon save-on"}) {
		t.Errorf("%v", ev)
	}
	p.savesBack(t)
}

// A browser that stops reading, but stays, does not keep the game from saving
// past the limit: the game saves again, and the download is given up on.
func TestDownloadToABrowserThatStopsReading(t *testing.T) {
	was := worldHoldLimit
	worldHoldLimit = time.Second
	t.Cleanup(func() { worldHoldLimit = was })
	// More than every buffer between the game's files and the browser holds.
	region := make([]byte, 48<<20)
	if _, err := rand.Read(region); err != nil {
		t.Fatal(err)
	}
	p, _, _ := worldPanel(t, map[string]string{"world/level.dat": "the level", "world/region/r.0.0.mca": string(region)})
	p.setPhase("alpha", "world", avsysv1.PhaseRunning)
	srv := httptest.NewServer(p.h)
	t.Cleanup(srv.Close)
	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "GET /api/v1/orgs/alpha/servers/world/world/archive HTTP/1.1\r\nHost: panel\r\nAuthorization: Bearer %s\r\n\r\n", p.tokens["operator"])
	// Never read.
	for deadline := time.Now().Add(10 * time.Second); !slices.Contains(p.worldEvents(), "tar, saves held: true"); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("the world was not read: %v", p.worldEvents())
		}
	}
	p.savesBack(t)
}

// A game that does not answer is not saving either: its world is sent as its
// files are.
func TestDownloadFromAGameThatDoesNotAnswer(t *testing.T) {
	p, _, game := worldPanel(t, map[string]string{"world/level.dat": "the level"})
	game.password = "not it"
	p.setPhase("alpha", "world", avsysv1.PhaseRunning)
	got, _ := fetchWorld(t, p)
	sameFiles(t, got, map[string]string{"Survival SMP/level.dat": "the level"})
	if ev := p.worldEvents(); !slices.Equal(ev, []string{"tar, saves held: false"}) {
		t.Errorf("%v", ev)
	}
	if p.srv.ops.busy("mc-alpha", "world") {
		t.Error("the server is still held")
	}
}

// A download waits for nothing: while something else is done to the server,
// it is refused.
func TestDownloadWhileAnOperationRuns(t *testing.T) {
	p, _, _ := worldPanel(t, map[string]string{"world/level.dat": "the level"})
	if _, ok := p.srv.ops.begin("mc-alpha", "alpha", "world", kindRestore, ""); !ok {
		t.Fatal("could not begin")
	}
	if code, e := p.do("operator", "GET", "/orgs/alpha/servers/world/world/archive", ""); code != 409 || e == "" {
		t.Errorf("%d %s", code, e)
	}
	p.srv.ops.end("mc-alpha", "world")
	p.setPhase("alpha", "world", avsysv1.PhaseStarting)
	if code, e := p.do("operator", "GET", "/orgs/alpha/servers/world/world/archive", ""); code != 409 || e != "unsettled" {
		t.Errorf("starting: %d %s", code, e)
	}
}

// Deleting a server's world is a new world in its place, asked for with
// nothing of its own: its world, Nether and End go, and the settings it is
// made again with stay as they are.
func TestDeletedWorldKeepsItsSettings(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh is not installed")
	}
	data := filepath.Join(t.TempDir(), "data")
	props := "motd=hi\nlevel-seed=5\nlevel-type=flat\n"
	writeFiles(t, data, map[string]string{
		"server.properties":      props,
		"world/level.dat":        "the old world",
		"world/region/r.0.0.mca": "the old world",
		"world_nether/DIM-1/x":   "the old nether",
		"world_the_end/DIM1/x":   "the old end",
	})
	script := regexp.MustCompile(regexp.QuoteMeta(dataRoot)+`\b`).ReplaceAllString(newWorldScript, data)
	cmd := exec.Command("sh", "-c", script)
	cmd.Stdin = strings.NewReader("\n\n\nreplace\nworld\n\n")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("script: %v: %s", err, out)
	}
	if got, err := os.ReadFile(filepath.Join(data, "server.properties")); err != nil || string(got) != props {
		t.Errorf("server.properties: %q, %v", got, err)
	}
	for _, path := range []string{"world/level.dat", "world/region", "world_nether", "world_the_end"} {
		if _, err := os.Stat(filepath.Join(data, path)); !os.IsNotExist(err) {
			t.Errorf("%s is still there: %v", path, err)
		}
	}
}
