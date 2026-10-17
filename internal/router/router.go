// Package router is the shared front door for players. Many servers can sit
// behind one address: the hostname a client asks for in its handshake decides
// which server it reaches. Sleeping servers are woken by a join attempt.
package router

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
	"github.com/GT-Drehmal/avSYS/internal/controller"
	"github.com/GT-Drehmal/avSYS/internal/kube"
	"github.com/GT-Drehmal/avSYS/internal/mcproto"
)

// HostnameIndex is the cache index servers are looked up by. It must be
// registered with IndexHostname before the cache starts.
const HostnameIndex = "avsys.routerHostname"

// IndexHostname is the index function for HostnameIndex.
func IndexHostname(obj client.Object) []string {
	mc := obj.(*avsysv1.MinecraftServer)
	if mc.Spec.Expose.Type != avsysv1.ExposeRouter || mc.Spec.Expose.Hostname == "" {
		return nil
	}
	return []string{NormalizeHostname(mc.Spec.Expose.Hostname)}
}

func NormalizeHostname(h string) string {
	return mcproto.Handshake{Host: h}.Hostname()
}

type Router struct {
	Client client.Client
	Dialer kube.PodDialer
	Log    *slog.Logger
	// Icon returns a server's icon as a 64 pixel PNG, or nil for none. It
	// is optional. A running server sends its own; this is for the entries
	// the router answers itself.
	Icon func(ctx context.Context, namespace, name string) []byte
	// Verify, if set, is told of players proving an account is theirs: a
	// login by a name it has a check pending for is not passed on to a
	// server, but has the game prove who is playing.
	Verify Verifier
	// HasJoined asks Mojang whether a player is who they say; nil is
	// Mojang's own session server.
	HasJoined mcproto.HasJoined
}

// Verifier keeps the checks of Minecraft accounts people have asked for.
type Verifier interface {
	// Pending says whether a check is waiting for a player of this name.
	Pending(name string) bool
	// Proven is told that the player who logged in as name is, as Mojang
	// vouches, the account of this UUID. It says what to tell them.
	Proven(ctx context.Context, name, uuid string) string
}

// mojangHasJoined asks Mojang's session server whether a player joined the
// server of a hash, as an online-mode server does.
func mojangHasJoined(ctx context.Context, name, hash string) (string, error) {
	u := "https://sessionserver.mojang.com/session/minecraft/hasJoined?" + url.Values{"username": {name}, "serverId": {hash}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	resp, err := mojangHTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusForbidden {
		return "", mcproto.ErrNotJoined
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("mojang answered %s", resp.Status)
	}
	var profile struct{ ID string }
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&profile); err != nil || profile.ID == "" {
		return "", mcproto.ErrNotJoined
	}
	return profile.ID, nil
}

var mojangHTTP = &http.Client{Timeout: 10 * time.Second}

// iconWait is how long a server list is kept waiting for an icon that has
// to be read out of the server's files. One that takes longer is left out
// this time and is there the next.
const iconWait = 800 * time.Millisecond

// favicon is a server's icon in the form the server list takes it, or ""
// for none.
//
// Anyone can ask for a server list, signed in or not. Icon is expected to
// do at most one read per server at a time however often it is asked, and
// to give up on one that does not finish: what is started here per ask is
// only the wait for it.
func (r *Router) favicon(ctx context.Context, mc *avsysv1.MinecraftServer) string {
	if r.Icon == nil {
		return ""
	}
	found := make(chan []byte, 1)
	go func() {
		// Seen through whoever is waiting, so that it is remembered.
		found <- r.Icon(context.WithoutCancel(ctx), mc.Namespace, mc.Name)
	}()
	select {
	case png := <-found:
		if len(png) == 0 {
			return ""
		}
		return "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
	case <-time.After(iconWait):
		return ""
	}
}

func (r *Router) Serve(ctx context.Context, ln net.Listener) error {
	context.AfterFunc(ctx, func() { ln.Close() })
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go r.handle(ctx, conn)
	}
}

func (r *Router) lookup(ctx context.Context, host string) (*avsysv1.MinecraftServer, error) {
	var list avsysv1.MinecraftServerList
	if err := r.Client.List(ctx, &list, client.MatchingFields{HostnameIndex: host}); err != nil {
		return nil, err
	}
	if len(list.Items) == 0 {
		return nil, nil
	}
	return &list.Items[0], nil
}

func motd(title, detail string) json.RawMessage {
	b, _ := json.Marshal(mcproto.Text{Text: title + "\n", Extra: []mcproto.Text{{Text: detail, Color: "gray"}}})
	return b
}

func (r *Router) handle(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	br := bufio.NewReader(conn)
	hs, err := mcproto.ReadHandshake(br)
	if err != nil {
		return
	}
	// A player proving an account is theirs, at whichever address.
	if hs.NextState == mcproto.NextLogin && r.Verify != nil {
		if name, err := mcproto.LoginName(br); err == nil && r.Verify.Pending(name) {
			r.prove(ctx, conn, br, hs, name)
			return
		}
	}
	_ = conn.SetReadDeadline(time.Time{})

	host := hs.Hostname()
	mc, err := r.lookup(ctx, host)
	if err != nil {
		r.Log.Error("looking up server", "host", host, "error", err)
		return
	}

	// answer replies without involving a backend: a line for the server list,
	// or a reason shown when a join is refused.
	answer := func(e entry) {
		if hs.NextState == mcproto.NextStatus {
			_ = mcproto.ServeStatus(conn, br, e.status(hs.Protocol))
			return
		}
		_ = mcproto.Disconnect(conn, mcproto.Text{Text: e.title + "\n\n", Extra: []mcproto.Text{{Text: e.detail, Color: "gray"}}})
	}

	if mc == nil {
		answer(entry{title: "Unknown server", detail: "Nothing is hosted at " + host + ".", closed: "Offline"})
		return
	}
	name := mc.Spec.DisplayName
	if name == "" {
		name = mc.Name
	}

	// Its own icon is shown in the list whatever state it is in. Only asked
	// for when it is the list that is asking.
	icon := ""
	if hs.NextState == mcproto.NextStatus && mc.Status.Phase != avsysv1.PhaseRunning {
		icon = r.favicon(ctx, mc)
	}

	switch mc.Status.Phase {
	case avsysv1.PhaseRunning:
		r.proxy(ctx, conn, br, hs, mc)
	case avsysv1.PhaseSleeping:
		if hs.NextState == mcproto.NextStatus {
			// Listed as a server one can join, because joining is what
			// wakes it.
			answer(entry{title: name, detail: "Asleep. Join to wake it up.", max: maxPlayers(mc), icon: icon})
			return
		}
		if err := controller.SetAsleep(ctx, r.Client, mc, false); err != nil {
			r.Log.Error("waking server", "server", client.ObjectKeyFromObject(mc), "error", err)
			answer(entry{title: name, detail: "This server could not be woken. Tell an admin."})
			return
		}
		r.Log.Info("woken by join", "server", client.ObjectKeyFromObject(mc))
		answer(entry{title: name, detail: "Waking up. Join again in a minute."})
	case avsysv1.PhaseStarting:
		answer(entry{title: name, detail: "Starting up. Try again in a moment.", closed: "Starting", icon: icon})
	default:
		answer(entry{title: name, detail: "Offline.", closed: "Offline", icon: icon})
	}
}

// prove has the game of a player with a check pending prove who is playing,
// and tells them how it went.
func (r *Router) prove(ctx context.Context, conn net.Conn, br *bufio.Reader, hs mcproto.Handshake, name string) {
	joined := r.HasJoined
	if joined == nil {
		joined = mojangHasJoined
	}
	uuid, refuse, err := mcproto.Authenticate(ctx, conn, br, hs.Protocol, name, joined)
	say := func(title, detail string) {
		msg := mcproto.Text{Text: title + "\n\n", Extra: []mcproto.Text{{Text: detail, Color: "gray"}}}
		if refuse != nil {
			_ = refuse(msg)
		} else {
			_ = mcproto.Disconnect(conn, msg)
		}
	}
	if err != nil {
		r.Log.Info("a Minecraft account could not be proven", "name", name, "error", err)
		say("Not verified", "Mojang did not vouch for "+name+". Sign in to Minecraft as that account, and join again while the check is open.")
		return
	}
	say("avSYS", r.Verify.Proven(ctx, name, uuid))
}

// entry is what the router says of a server it is not connecting a player
// to: two lines of text, and whether it can be joined at all.
type entry struct {
	title, detail string
	// closed is set for a server that cannot be joined just now, to the one
	// word that says why. The game shows it in red where the number of
	// players would be, with a cross in place of the signal bars.
	closed string
	// max is how many players it takes, for one that can be joined.
	max int
	// icon is its own picture, as the list takes one, if it has one.
	icon string
}

// status is the entry as the server list is told it.
//
// The game has no way to be told "this server is off". What it does have is
// a server on a version it cannot join, which it shows as exactly that: the
// version's name in red and no signal. So a server that is off answers as
// one on a version called "Offline". Answering as an ordinary server with
// nobody on it showed green bars and "0/0", which reads as a server one
// could walk into.
func (e entry) status(protocol int) mcproto.Status {
	st := mcproto.Status{
		Version:     mcproto.Version{Name: "avSYS", Protocol: protocol},
		Players:     mcproto.Players{Max: e.max},
		Description: motd(e.title, e.detail),
		Favicon:     e.icon,
	}
	if e.closed != "" {
		// No client speaks this version.
		st.Version = mcproto.Version{Name: e.closed, Protocol: -1}
	}
	return st
}

// maxPlayers is how many players a server that is not running will take
// once it is: what its settings say, else what the game itself last said,
// which covers a limit set by hand in server.properties, else the game's
// own default.
func maxPlayers(mc *avsysv1.MinecraftServer) int {
	if n, err := strconv.Atoi(mc.Spec.Properties["max-players"]); err == nil && n > 0 {
		return n
	}
	if mc.Status.MaxPlayers > 0 {
		return int(mc.Status.MaxPlayers)
	}
	return 20
}

func (r *Router) proxy(ctx context.Context, conn net.Conn, br *bufio.Reader, hs mcproto.Handshake, mc *avsysv1.MinecraftServer) {
	dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	backend, err := r.Dialer.DialPod(dialCtx, mc.Namespace, mc.Name, controller.MinecraftPort)
	cancel()
	if err != nil {
		r.Log.Warn("backend unreachable", "server", client.ObjectKeyFromObject(mc), "error", err)
		return
	}
	defer backend.Close()
	if tcp, ok := conn.(*net.TCPConn); ok {
		_ = tcp.SetNoDelay(true)
	}
	if _, err := backend.Write(hs.Raw); err != nil {
		return
	}

	done := make(chan struct{}, 2)
	go func() {
		// br still holds whatever the client sent after its handshake.
		_, _ = io.Copy(backend, br)
		closeWrite(backend)
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(conn, backend)
		closeWrite(conn)
		done <- struct{}{}
	}()
	select {
	case <-done:
		// Give the other direction a moment to flush, then drop both.
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
	case <-ctx.Done():
	}
}

func closeWrite(c net.Conn) {
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
	}
}
