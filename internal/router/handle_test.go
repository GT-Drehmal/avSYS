package router

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
	"github.com/GT-Drehmal/avSYS/internal/mcproto"
)

// game stands in for a server's pod: what the router sends it, and a status
// it answers with, as a game would.
type game struct {
	mu     sync.Mutex
	got    []byte
	dialed []string
	fail   bool
}

func (g *game) DialPod(_ context.Context, namespace, pod string, port int) (net.Conn, error) {
	g.mu.Lock()
	g.dialed = append(g.dialed, namespace+"/"+pod)
	fail := g.fail
	g.mu.Unlock()
	if fail {
		return nil, errors.New("no route to the pod")
	}
	ours, theirs := net.Pipe()
	go func() {
		defer theirs.Close()
		br := bufio.NewReader(theirs)
		hs, err := mcproto.ReadHandshake(br)
		if err != nil {
			return
		}
		g.mu.Lock()
		g.got = hs.Raw
		g.mu.Unlock()
		_ = mcproto.ServeStatus(theirs, br, mcproto.Status{Version: mcproto.Version{Name: "Paper 1.21.4", Protocol: 769}, Players: mcproto.Players{Max: 20, Online: 3}, Description: json.RawMessage(`{"text":"the game itself"}`)})
	}()
	return ours, nil
}

// routed is a router over these servers, listening on a port of its own, and
// the game behind every running one.
func routed(t *testing.T, servers ...*avsysv1.MinecraftServer) (*Router, string, *game) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := avsysv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	objs := make([]client.Object, len(servers))
	for i := range servers {
		objs[i] = servers[i]
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithIndex(&avsysv1.MinecraftServer{}, HostnameIndex, IndexHostname).WithObjects(objs...).Build()
	g := &game{}
	r := &Router{Client: c, Dialer: g, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = r.Serve(ctx, ln) }()
	return r, ln.Addr().String(), g
}

func server(name, host string, phase avsysv1.Phase, change ...func(*avsysv1.MinecraftServer)) *avsysv1.MinecraftServer {
	mc := &avsysv1.MinecraftServer{ObjectMeta: metav1.ObjectMeta{Namespace: "mc-alpha", Name: name}}
	mc.Spec.Expose = avsysv1.ExposeSpec{Type: avsysv1.ExposeRouter, Hostname: host}
	mc.Status.Phase = phase
	for _, f := range change {
		f(mc)
	}
	return mc
}

// ping asks the router at addr for the server list entry of host.
func ping(t *testing.T, addr, host string) (mcproto.Status, error) {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	return mcproto.Ping(conn, host)
}

// lines is a status's description as text.
func lines(st mcproto.Status) string {
	var text mcproto.Text
	_ = json.Unmarshal(st.Description, &text)
	out := text.Text
	for _, e := range text.Extra {
		out += e.Text
	}
	return out
}

// join tries to log in to host through the router at addr, and returns the
// reason it was turned away with.
func join(t *testing.T, addr, host string) string {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	varint := func(b []byte, v int) []byte {
		u := uint32(v)
		for u >= 0x80 {
			b = append(b, byte(u)|0x80)
			u >>= 7
		}
		return append(b, byte(u))
	}
	str := func(b []byte, s string) []byte { return append(varint(b, len(s)), s...) }
	packet := func(id int, body []byte) []byte {
		p := varint(nil, id)
		p = append(p, body...)
		return append(varint(nil, len(p)), p...)
	}
	hs := varint(nil, 769)
	hs = str(hs, host)
	hs = binary.BigEndian.AppendUint16(hs, 25565)
	hs = varint(hs, 2)
	if _, err := conn.Write(append(packet(0, hs), packet(0, str(nil, "Steve"))...)); err != nil {
		t.Fatal(err)
	}
	reply, _ := io.ReadAll(conn)
	return string(reply)
}

// The router reaches the server a player asks for by name, however the game
// wrote the name, and says what it can of one it cannot connect them to.
func TestRouterStatus(t *testing.T) {
	_, addr, g := routed(t,
		server("world", "world.example.org", avsysv1.PhaseRunning),
		server("lobby", "lobby.example.org", avsysv1.PhaseSleeping, func(mc *avsysv1.MinecraftServer) {
			mc.Spec.DisplayName = "The Lobby"
			mc.Spec.Properties = map[string]string{"max-players": "40"}
		}),
		server("build", "build.example.org", avsysv1.PhaseStarting),
		server("old", "old.example.org", avsysv1.PhaseStopped),
		// Reached at an address of its own, not through the router.
		server("own", "own.example.org", avsysv1.PhaseRunning, func(mc *avsysv1.MinecraftServer) { mc.Spec.Expose.Type = avsysv1.ExposeLoadBalancer }),
	)

	// Running: the game answers for itself, sent exactly what the player sent.
	for _, host := range []string{"world.example.org", "WORLD.Example.org.", "world.example.org\x00FML3\x00"} {
		st, err := ping(t, addr, host)
		if err != nil || lines(st) != "the game itself" || st.Players.Online != 3 {
			t.Errorf("%q: %+v, %v; want the game's own answer", host, st, err)
		}
		g.mu.Lock()
		if !strings.Contains(string(g.got), host) {
			t.Errorf("%q: the game was sent %q", host, g.got)
		}
		g.mu.Unlock()
	}

	for host, want := range map[string]struct {
		text, version string
		max           int
	}{
		"lobby.example.org": {"The Lobby\nAsleep. Join to wake it up.", "avSYS", 40},
		"build.example.org": {"build\nStarting up. Try again in a moment.", "Starting", 0},
		"old.example.org":   {"old\nOffline.", "Offline", 0},
		"own.example.org":   {"Unknown server\nNothing is hosted at own.example.org.", "Offline", 0},
		"nobody.example":    {"Unknown server\nNothing is hosted at nobody.example.", "Offline", 0},
	} {
		st, err := ping(t, addr, host)
		if err != nil || lines(st) != want.text || st.Version.Name != want.version || st.Players.Max != want.max {
			t.Errorf("%s: %q, version %q, max %d (%v); want %q, %q, %d", host, lines(st), st.Version.Name, st.Players.Max, err, want.text, want.version, want.max)
		}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, d := range g.dialed {
		if d != "mc-alpha/world" {
			t.Errorf("dialed %s, which is not running", d)
		}
	}
}

// Joining a sleeping server wakes it, and says so; listing it does not.
func TestRouterWakesOnJoin(t *testing.T) {
	asleep := func(mc *avsysv1.MinecraftServer) {
		mc.Annotations = map[string]string{avsysv1.AnnotationAsleep: "since"}
	}
	r, addr, _ := routed(t, server("lobby", "lobby.example.org", avsysv1.PhaseSleeping, asleep))
	isAsleep := func() bool {
		mc := &avsysv1.MinecraftServer{}
		if err := r.Client.Get(context.Background(), client.ObjectKey{Namespace: "mc-alpha", Name: "lobby"}, mc); err != nil {
			t.Fatal(err)
		}
		_, is := mc.Annotations[avsysv1.AnnotationAsleep]
		return is
	}
	if _, err := ping(t, addr, "lobby.example.org"); err != nil || !isAsleep() {
		t.Errorf("listing it woke it (%v)", err)
	}
	if got := join(t, addr, "lobby.example.org"); !strings.Contains(got, "Waking up. Join again in a minute.") {
		t.Errorf("joining it was answered %q", got)
	}
	if isAsleep() {
		t.Error("joining it did not wake it")
	}
	// Turned away from one that cannot be joined, with why.
	if got := join(t, addr, "nobody.example"); !strings.Contains(got, "Nothing is hosted at nobody.example.") {
		t.Errorf("joining a server that is not there was answered %q", got)
	}
}

// A running server whose pod cannot be reached ends the connection rather
// than holding the player there.
func TestRouterUnreachableGame(t *testing.T) {
	_, addr, g := routed(t, server("world", "world.example.org", avsysv1.PhaseRunning))
	g.fail = true
	began := time.Now()
	if _, err := ping(t, addr, "world.example.org"); err == nil {
		t.Error("a game that cannot be reached answered")
	}
	if time.Since(began) > 3*time.Second {
		t.Errorf("held for %v", time.Since(began))
	}
}

// What is not a handshake, as an old client's ping or noise, is dropped.
func TestRouterIgnoresNoise(t *testing.T) {
	_, addr, g := routed(t, server("world", "world.example.org", avsysv1.PhaseRunning))
	for _, noise := range [][]byte{{0xFE, 0x01}, []byte("GET / HTTP/1.1\r\nHost: world.example.org\r\n\r\n")} {
		conn, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		_, _ = conn.Write(noise)
		_ = conn.(*net.TCPConn).CloseWrite()
		if reply, _ := io.ReadAll(conn); len(reply) != 0 {
			t.Errorf("%q was answered %q", noise, reply)
		}
		conn.Close()
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.dialed) != 0 {
		t.Errorf("noise reached a game: %v", g.dialed)
	}
}
