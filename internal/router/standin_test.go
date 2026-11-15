package router

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
)

var (
	geyserPort = avsysv1.ExtraPort{Name: "geyser", Port: 19132, Protocol: corev1.ProtocolUDP}
	voicePort  = avsysv1.ExtraPort{Name: "voice-chat", Port: 24454, Protocol: corev1.ProtocolUDP}
	mapPort    = avsysv1.ExtraPort{Name: "bluemap", Port: 8100, Protocol: corev1.ProtocolTCP}
)

// sleeping is a server asleep, as the controller leaves one.
func sleeping(name, title string) *avsysv1.MinecraftServer {
	mc := &avsysv1.MinecraftServer{ObjectMeta: metav1.ObjectMeta{Namespace: "mc-club", Name: name, Annotations: map[string]string{avsysv1.AnnotationAsleep: "2026-11-14T00:00:00Z"}}}
	mc.Spec.DisplayName = title
	mc.Spec.Expose.Type = avsysv1.ExposeRouter
	mc.Status.Phase = avsysv1.PhaseSleeping
	return mc
}

// standIn is the panel's stand-in over these servers, as the cluster has
// them.
func standIn(t *testing.T, servers ...*avsysv1.MinecraftServer) (*StandIn, client.Client) {
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
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
	s := &StandIn{Client: c, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), IP: "127.0.0.1"}
	t.Cleanup(func() {
		for _, mc := range servers {
			s.Close(client.ObjectKeyFromObject(mc))
		}
	})
	return s, c
}

func open(t *testing.T, s *StandIn, mc *avsysv1.MinecraftServer, ports ...avsysv1.ExtraPort) map[string]int32 {
	t.Helper()
	ip, at, err := s.Open(client.ObjectKeyFromObject(mc), ports)
	if err != nil {
		t.Fatal(err)
	}
	if ip != "127.0.0.1" || len(at) != len(ports) {
		t.Fatalf("opened at %s: %v", ip, at)
	}
	return at
}

func udp(t *testing.T, port int32) net.Conn {
	t.Helper()
	conn, err := net.Dial("udp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

// bedrockPing pings as a Bedrock server list does, and reads the line the
// answer carries.
func bedrockPing(t *testing.T, port int32) string {
	t.Helper()
	conn := udp(t, port)
	if _, err := conn.Write(unhex(t, pingBytes)); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 2048)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("no answer to a ping: %v", err)
	}
	b := buf[:n]
	if len(b) < 35 || b[0] != raknetPong || !bytes.Equal(b[1:9], unhex(t, "000000000003b6e4")) || !bytes.Equal(b[17:33], raknetMagic) {
		t.Fatalf("not a pong to it: %x", b)
	}
	if int(binary.BigEndian.Uint16(b[33:35])) != len(b)-35 {
		t.Fatalf("its line's length is wrong: %x", b)
	}
	return string(b[35:])
}

func asleepNow(t *testing.T, c client.Client, mc *avsysv1.MinecraftServer) bool {
	t.Helper()
	now := &avsysv1.MinecraftServer{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(mc), now); err != nil {
		t.Fatal(err)
	}
	_, ok := now.Annotations[avsysv1.AnnotationAsleep]
	return ok
}

// wokenSoon waits for a server to be woken.
func wokenSoon(t *testing.T, c client.Client, mc *avsysv1.MinecraftServer) bool {
	t.Helper()
	for range 100 {
		if !asleepNow(t, c, mc) {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

// stillAsleep checks a server is not woken, for a moment.
func stillAsleep(t *testing.T, c client.Client, mc *avsysv1.MinecraftServer) bool {
	t.Helper()
	time.Sleep(200 * time.Millisecond)
	return asleepNow(t, c, mc)
}

// Bedrock players see a sleeping server in their list, saying it is asleep,
// which a ping does not wake. Joining wakes it, and it is then said to be
// starting.
func TestStandInBedrock(t *testing.T) {
	mc := sleeping("survival", "Club Survival")
	s, c := standIn(t, mc)
	at := open(t, s, mc, geyserPort)

	line := bedrockPing(t, at["geyser"])
	fields := strings.Split(line, ";")
	if len(fields) != 13 || fields[0] != "MCPE" || fields[1] != "Asleep. Join to wake it up." || fields[7] != "Club Survival" || fields[5] != "20" || fields[10] != "19132" {
		t.Errorf("line: %q", line)
	}
	if !stillAsleep(t, c, mc) {
		t.Fatal("woken by the server list")
	}

	if _, err := udp(t, at["geyser"]).Write(openRequest(t)); err != nil {
		t.Fatal(err)
	}
	if !wokenSoon(t, c, mc) {
		t.Fatal("a Bedrock player joining did not wake it")
	}
	// Before the controller has said so, as right after.
	if line := bedrockPing(t, at["geyser"]); !strings.HasPrefix(line, "MCPE;Starting up. Try again in a moment.;") {
		t.Errorf("woken, it says %q", line)
	}
	// And after.
	now := &avsysv1.MinecraftServer{}
	_ = c.Get(context.Background(), client.ObjectKeyFromObject(mc), now)
	now.Status.Phase = avsysv1.PhaseStarting
	if err := c.Update(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if line := bedrockPing(t, at["geyser"]); !strings.HasPrefix(line, "MCPE;Starting up. Try again in a moment.;") {
		t.Errorf("starting, it says %q", line)
	}
}

// Anything else over UDP, as voice chat, wakes a sleeping server.
func TestStandInVoiceChat(t *testing.T) {
	mc := sleeping("survival", "")
	s, c := standIn(t, mc)
	at := open(t, s, mc, voicePort)
	if _, err := udp(t, at["voice-chat"]).Write([]byte{0xff, 0x01, 0x02, 0x03}); err != nil {
		t.Fatal(err)
	}
	if !wokenSoon(t, c, mc) {
		t.Fatal("voice chat did not wake it")
	}
}

func httpGet(t *testing.T, port int32, request string) string {
	t.Helper()
	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte(request)); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	b, _ := io.ReadAll(conn)
	return string(b)
}

// A map's page in a browser wakes a sleeping server, and says it is
// starting, reloading itself until it is up. A connection that sends
// nothing wakes nothing.
func TestStandInWebMap(t *testing.T) {
	mc := sleeping("survival", "Club <Survival>")
	s, c := standIn(t, mc)
	at := open(t, s, mc, mapPort)

	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", at["bluemap"]))
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	if !stillAsleep(t, c, mc) {
		t.Fatal("woken by a connection that sent nothing")
	}

	page := httpGet(t, at["bluemap"], "GET / HTTP/1.1\r\nHost: map.example.org\r\nUser-Agent: test\r\n\r\n")
	if !wokenSoon(t, c, mc) {
		t.Fatal("a browser did not wake it")
	}
	for _, want := range []string{"HTTP/1.1 503 ", "Retry-After: 10", "Starting up. This page reloads in a moment.", `http-equiv="refresh"`, "Club &lt;Survival&gt;"} {
		if !strings.Contains(page, want) {
			t.Errorf("no %q in\n%s", want, page)
		}
	}
	if head := httpGet(t, at["bluemap"], "HEAD / HTTP/1.1\r\nHost: x\r\n\r\n"); !strings.HasPrefix(head, "HTTP/1.1 503 ") || strings.Contains(head, "<html") {
		t.Errorf("HEAD: %q", head)
	}
}

// Something other than a browser over TCP wakes a sleeping server, and is
// sent nothing.
func TestStandInOtherTCP(t *testing.T) {
	mc := sleeping("survival", "")
	s, c := standIn(t, mc)
	at := open(t, s, mc, mapPort)
	if got := httpGet(t, at["bluemap"], "\x00\x01hello"); got != "" {
		t.Errorf("sent %q", got)
	}
	if !wokenSoon(t, c, mc) {
		t.Fatal("not woken")
	}
}

// A server that is off says so, and is not started by anyone reaching it.
func TestStandInStoppedServer(t *testing.T) {
	mc := sleeping("survival", "Survival")
	mc.Annotations = nil
	mc.Spec.State = avsysv1.StateStopped
	mc.Status.Phase = avsysv1.PhaseStopped
	s, c := standIn(t, mc)
	before := &avsysv1.MinecraftServer{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(mc), before); err != nil {
		t.Fatal(err)
	}
	at := open(t, s, mc, geyserPort, mapPort)
	if line := bedrockPing(t, at["geyser"]); !strings.HasPrefix(line, "MCPE;Offline.;") {
		t.Errorf("line: %q", line)
	}
	if _, err := udp(t, at["geyser"]).Write(openRequest(t)); err != nil {
		t.Fatal(err)
	}
	page := httpGet(t, at["bluemap"], "GET / HTTP/1.1\r\n\r\n")
	if !strings.Contains(page, "Offline.") || strings.Contains(page, "refresh") {
		t.Errorf("page: %s", page)
	}
	time.Sleep(200 * time.Millisecond)
	now := &avsysv1.MinecraftServer{}
	_ = c.Get(context.Background(), client.ObjectKeyFromObject(mc), now)
	if now.Spec.State != avsysv1.StateStopped || now.ResourceVersion != before.ResourceVersion {
		t.Errorf("changed: %s %s", now.Spec.State, now.ResourceVersion)
	}
}

// Several servers asleep at once, each with the same port in its own pod,
// are each listened for on a port of the panel's own, and each says and
// wakes only itself.
func TestStandInSeveralServers(t *testing.T) {
	a, b := sleeping("survival", "Survival"), sleeping("creative", "Creative")
	s, c := standIn(t, a, b)
	atA, atB := open(t, s, a, geyserPort), open(t, s, b, geyserPort)
	if atA["geyser"] == atB["geyser"] {
		t.Fatalf("both on %d", atA["geyser"])
	}
	if line := bedrockPing(t, atB["geyser"]); !strings.Contains(line, ";Creative;") {
		t.Errorf("the second says %q", line)
	}
	if guidA, guidB := guidOf(client.ObjectKeyFromObject(a)), guidOf(client.ObjectKeyFromObject(b)); guidA == guidB {
		t.Error("both have one id")
	}
	if _, err := udp(t, atA["geyser"]).Write(openRequest(t)); err != nil {
		t.Fatal(err)
	}
	if !wokenSoon(t, c, a) {
		t.Fatal("the first not woken")
	}
	if !stillAsleep(t, c, b) {
		t.Error("the second woken with the first")
	}
}

// A port is listened for on the same port of the panel's for as long as it
// is asked for, and on another once it is another; one no longer asked for,
// or of a server let go of, is closed.
func TestStandInOpenAndClose(t *testing.T) {
	mc := sleeping("survival", "")
	s, _ := standIn(t, mc)
	key := client.ObjectKeyFromObject(mc)
	first := open(t, s, mc, geyserPort, mapPort)
	again := open(t, s, mc, geyserPort, mapPort)
	if first["geyser"] != again["geyser"] || first["bluemap"] != again["bluemap"] {
		t.Errorf("moved: %v then %v", first, again)
	}
	asTCP := geyserPort
	asTCP.Protocol = corev1.ProtocolTCP
	moved := open(t, s, mc, asTCP)
	if len(moved) != 1 {
		t.Fatalf("still has %v", moved)
	}
	if _, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", first["bluemap"]), time.Second); err == nil {
		t.Error("a port no longer asked for is still open")
	}
	if conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", moved["geyser"]), time.Second); err != nil {
		t.Errorf("a port now over TCP is not open: %v", err)
	} else {
		conn.Close()
	}
	s.Close(key)
	if _, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", moved["geyser"]), time.Second); err == nil {
		t.Error("let go of, its port is still open")
	}
	s.Close(types.NamespacedName{Namespace: "mc-club", Name: "never-opened"})
}
