package router

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"html"
	"log/slog"
	"net"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
	"github.com/GT-Drehmal/avSYS/internal/controller"
)

// StandIn answers on the extra ports of servers whose games are not running,
// in their place: the controller points those ports at it meanwhile (see
// controller.StandIn). Reached, a sleeping server is woken, as a join through
// the router wakes one:
//
//   - Bedrock players, through Geyser, ping a server for the server list,
//     and are answered with a line saying it is asleep or starting. Joining
//     wakes it.
//   - Anything else over UDP, as voice chat, wakes it, and is not answered.
//   - Over TCP, a connection that sends something wakes it. A web browser,
//     as for a map, is sent a page saying the server is starting, which
//     reloads itself. A connection that sends nothing, as a check that the
//     port is open, wakes nothing.
//
// Each port of each server is listened for on a port of the panel's own,
// whichever one is free, so that any number of servers can have any ports.
type StandIn struct {
	// Client reads servers, from the cache, and wakes them.
	Client client.Client
	Log    *slog.Logger
	// IP is the panel's address in the cluster, which the ports are
	// pointed at.
	IP string

	mu      sync.Mutex
	servers map[types.NamespacedName]map[string]*standInPort
	// woken is when each server was last woken from here, so that what
	// reaches it before its status says so is not taken as asleep.
	woken map[types.NamespacedName]time.Time
}

var _ controller.StandIn = (*StandIn)(nil)

type standInPort struct {
	protocol corev1.Protocol
	port     int32
	at       int32
	close    func()
}

// standInRead is how long a TCP connection is given to say something.
const standInRead = 10 * time.Second

// wokenFor is how long a server woken from here is taken as starting, though
// its status may not say so yet.
const wokenFor = 30 * time.Second

func (s *StandIn) Open(server types.NamespacedName, ports []avsysv1.ExtraPort) (string, map[string]int32, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.servers == nil {
		s.servers = map[types.NamespacedName]map[string]*standInPort{}
	}
	had := s.servers[server]
	kept := map[string]*standInPort{}
	var opened []*standInPort
	for _, p := range ports {
		protocol := p.Protocol
		if protocol == "" {
			protocol = corev1.ProtocolTCP
		}
		if h := had[p.Name]; h != nil && h.protocol == protocol && h.port == p.Port {
			kept[p.Name] = h
			continue
		}
		l, err := s.listen(server, p.Name, protocol, p.Port)
		if err != nil {
			for _, o := range opened {
				o.close()
			}
			return "", nil, err
		}
		opened = append(opened, l)
		kept[p.Name] = l
	}
	for name, h := range had {
		if kept[name] != h {
			h.close()
		}
	}
	s.servers[server] = kept
	at := make(map[string]int32, len(kept))
	for name, l := range kept {
		at[name] = l.at
	}
	return s.IP, at, nil
}

func (s *StandIn) Close(server types.NamespacedName) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, l := range s.servers[server] {
		l.close()
	}
	delete(s.servers, server)
	delete(s.woken, server)
}

// listen opens a port of the panel's for one of a server's.
func (s *StandIn) listen(server types.NamespacedName, name string, protocol corev1.Protocol, port int32) (*standInPort, error) {
	l := &standInPort{protocol: protocol, port: port}
	switch protocol {
	case corev1.ProtocolUDP:
		conn, err := net.ListenPacket("udp", ":0")
		if err != nil {
			return nil, err
		}
		l.at = int32(conn.LocalAddr().(*net.UDPAddr).Port)
		l.close = func() { conn.Close() }
		go s.serveUDP(server, name, l, conn)
	case corev1.ProtocolTCP:
		ln, err := net.Listen("tcp", ":0")
		if err != nil {
			return nil, err
		}
		l.at = int32(ln.Addr().(*net.TCPAddr).Port)
		l.close = func() { ln.Close() }
		go s.serveTCP(server, name, ln)
	default:
		return nil, fmt.Errorf("%s: no such protocol", protocol)
	}
	return l, nil
}

// server is the server as it is now, or nil if it is gone.
func (s *StandIn) server(key types.NamespacedName) *avsysv1.MinecraftServer {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	mc := &avsysv1.MinecraftServer{}
	if err := s.Client.Get(ctx, key, mc); err != nil {
		return nil
	}
	return mc
}

// state is what is said of a server: asleep, starting, or off.
type standInState int

const (
	stateAsleep standInState = iota
	stateStarting
	stateOff
)

func (s *StandIn) state(mc *avsysv1.MinecraftServer) standInState {
	switch mc.Status.Phase {
	case avsysv1.PhaseSleeping:
		s.mu.Lock()
		at, ok := s.woken[client.ObjectKeyFromObject(mc)]
		s.mu.Unlock()
		if ok && time.Since(at) < wokenFor {
			return stateStarting
		}
		return stateAsleep
	// Running, it is about to be answered by the game itself.
	case avsysv1.PhaseStarting, avsysv1.PhaseRunning:
		return stateStarting
	}
	return stateOff
}

// wake wakes a sleeping server, once however many reach it at once. by says
// what reached it, for the log.
func (s *StandIn) wake(mc *avsysv1.MinecraftServer, port, by string) {
	if mc.Status.Phase != avsysv1.PhaseSleeping {
		return
	}
	key := client.ObjectKeyFromObject(mc)
	s.mu.Lock()
	if at, ok := s.woken[key]; ok && time.Since(at) < wokenFor {
		s.mu.Unlock()
		return
	}
	if s.woken == nil {
		s.woken = map[types.NamespacedName]time.Time{}
	}
	s.woken[key] = time.Now()
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := controller.SetAsleep(ctx, s.Client, mc, false); err != nil {
		s.Log.Error("waking server", "server", key, "port", port, "by", by, "error", err)
		s.mu.Lock()
		delete(s.woken, key)
		s.mu.Unlock()
		return
	}
	s.Log.Info("woken through an extra port", "server", key, "port", port, "by", by)
}

func displayName(mc *avsysv1.MinecraftServer) string {
	if mc.Spec.DisplayName != "" {
		return mc.Spec.DisplayName
	}
	return mc.Name
}

func (s *StandIn) serveUDP(server types.NamespacedName, name string, l *standInPort, conn net.PacketConn) {
	buf := make([]byte, 2048)
	for {
		n, from, err := conn.ReadFrom(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		mc := s.server(server)
		if mc == nil {
			continue
		}
		if sent, ok := raknetPingTime(buf[:n]); ok {
			_, _ = conn.WriteTo(raknetPongOf(sent, guidOf(server), s.bedrock(mc, l.port).line()), from)
			continue
		}
		// A Bedrock player joining, or anything else: voice chat, say.
		// Neither can be answered for the game.
		by := "a packet"
		if raknetJoin(buf[:n]) {
			by = "a Bedrock player joining"
		}
		s.wake(mc, name, by)
	}
}

// bedrock is what the server list of Bedrock players is told of a server.
func (s *StandIn) bedrock(mc *avsysv1.MinecraftServer, port int32) bedrockStatus {
	st := bedrockStatus{Detail: displayName(mc), Protocol: bedrockProtocol, Version: bedrockVersion, Max: maxPlayers(mc), GUID: guidOf(client.ObjectKeyFromObject(mc)), Port: port}
	switch s.state(mc) {
	case stateAsleep:
		st.Title = "Asleep. Join to wake it up."
	case stateStarting:
		st.Title = "Starting up. Try again in a moment."
	default:
		st.Title = "Offline."
	}
	return st
}

// guidOf is a server's id as RakNet has one: the same for as long as it is
// the same server.
func guidOf(server types.NamespacedName) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(server.String()))
	return h.Sum64()
}

func (s *StandIn) serveTCP(server types.NamespacedName, name string, ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			time.Sleep(100 * time.Millisecond)
			continue
		}
		go s.handleTCP(server, name, conn)
	}
}

func (s *StandIn) handleTCP(server types.NamespacedName, name string, conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(standInRead))
	// What a browser asks, to its end, so that what is sent back is not
	// lost to what was left unread.
	var got []byte
	buf := make([]byte, 4096)
	for len(got) < 16<<10 && !bytes.Contains(got, []byte("\r\n\r\n")) {
		n, err := conn.Read(buf)
		got = append(got, buf[:n]...)
		if err != nil {
			break
		}
		if !looksHTTP(got) {
			break
		}
	}
	if len(got) == 0 {
		return
	}
	mc := s.server(server)
	if mc == nil {
		return
	}
	http := looksHTTP(got)
	by := "a connection"
	if http {
		by = "a web browser"
	}
	s.wake(mc, name, by)
	if http {
		_, _ = conn.Write(s.page(mc, bytes.HasPrefix(got, []byte("HEAD "))))
	}
}

// looksHTTP says whether what a connection sent so far is, or begins, a web
// browser's request.
func looksHTTP(b []byte) bool {
	for _, method := range []string{"GET ", "HEAD ", "POST ", "PUT ", "DELETE ", "OPTIONS ", "PATCH "} {
		n := min(len(b), len(method))
		if bytes.Equal(b[:n], []byte(method[:n])) {
			return true
		}
	}
	return false
}

// page is what a browser is sent: that the server is starting, in a page
// that reloads itself, or that it is off.
func (s *StandIn) page(mc *avsysv1.MinecraftServer, head bool) []byte {
	name := html.EscapeString(displayName(mc))
	say, reload := "Offline.", ""
	if st := s.state(mc); st == stateAsleep || st == stateStarting {
		say, reload = "Starting up. This page reloads in a moment.", `<meta http-equiv="refresh" content="10">`
	}
	body := `<!doctype html><html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">` + reload +
		`<title>` + name + `</title><body style="font:16px/1.5 system-ui,sans-serif;max-width:32em;margin:15vh auto;padding:0 16px"><h1 style="font-size:1.4em">` + name + `</h1><p>` + say + "</p></body></html>\n"
	head503 := fmt.Sprintf("HTTP/1.1 503 Service Unavailable\r\nContent-Type: text/html; charset=utf-8\r\nContent-Length: %d\r\nRetry-After: 10\r\nCache-Control: no-store\r\nConnection: close\r\n\r\n", len(body))
	if head {
		return []byte(head503)
	}
	return []byte(head503 + body)
}
