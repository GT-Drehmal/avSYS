package api

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubefake "k8s.io/client-go/kubernetes/fake"
	"sigs.k8s.io/controller-runtime/pkg/client"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
)

// stoppingGame is a running server whose game answers RCON as a real one
// does, and whose log the test writes to.
type stoppingGame struct {
	p     *panel
	game  *rconGame
	world *avsysv1.MinecraftServer
	mu    sync.Mutex
	// heard is what the game was told, and how many connections the
	// panel had open to it as it was told each: the one it came on too.
	heard []heard
	logs  *io.PipeWriter
}

type heard struct {
	command string
	open    int
}

func newStoppingGame(t *testing.T) *stoppingGame {
	t.Helper()
	p := newPanel(t)
	ctx := context.Background()
	p.setPhase("alpha", "world", avsysv1.PhaseRunning)
	world := &avsysv1.MinecraftServer{}
	if err := p.srv.Client.Get(ctx, client.ObjectKey{Namespace: "mc-alpha", Name: "world"}, world); err != nil {
		t.Fatal(err)
	}
	world.Spec.State = avsysv1.StateRunning
	if err := p.srv.Client.Update(ctx, world); err != nil {
		t.Fatal(err)
	}
	// As the controller has seen it.
	world.Status.ObservedGeneration = world.Generation
	if err := p.srv.Client.Status().Update(ctx, world); err != nil {
		t.Fatal(err)
	}
	world.Status.Version = "1.21.4"
	p.srv.Kube = kubefake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: "mc-alpha", Name: "world-rcon"},
		Data:       map[string][]byte{"password": []byte("secret")},
	})
	g := &stoppingGame{p: p, world: world}
	g.game = &rconGame{password: "secret", answer: func(command string) string {
		// Closed a moment ago, a connection may not have been seen closed
		// yet: given a second.
		open := 0
		if stopsCommand(command, true) {
			open = g.game.alive(1, time.Second)
		}
		g.mu.Lock()
		g.heard = append(g.heard, heard{command, open})
		g.mu.Unlock()
		switch command {
		case "tick query":
			return answer(t, "vanilla-1.21.4", command)
		case "list":
			return "There are 0 of a max of 20 players online: "
		}
		return ""
	}}
	p.srv.Dialer = g.game
	// Each time the log is followed, it is from what the test writes next.
	p.srv.gameLog = func(ctx context.Context, _, _ string) (io.ReadCloser, error) {
		r, w := io.Pipe()
		g.mu.Lock()
		g.logs = w
		g.mu.Unlock()
		context.AfterFunc(ctx, func() { w.Close() })
		return r, nil
	}
	return g
}

// sample asks the game how fast it ticks, as the sampler does, with the
// server as the cluster last said it was: a figure, or none.
func (g *stoppingGame) sample() (tickSample, bool) {
	got, ok := g.p.srv.sampleTicks(context.Background(), []avsysv1.MinecraftServer{*g.world})["mc-alpha/world"]
	return got, ok
}

// log writes a line to the game's log, once the panel follows it.
func (g *stoppingGame) log(t *testing.T, line string) {
	t.Helper()
	for end := time.Now().Add(2 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		g.mu.Lock()
		w := g.logs
		g.mu.Unlock()
		if w != nil {
			if _, err := w.Write([]byte(line + "\n")); err == nil {
				return
			}
		}
		if time.Now().After(end) {
			t.Fatal("the panel does not follow the game's log")
		}
	}
}

func (g *stoppingGame) told(command string) (open int, ok bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, h := range g.heard {
		if h.command == command {
			return h.open, true
		}
	}
	return 0, false
}

// running is the cluster saying the server runs again, after phase.
func (g *stoppingGame) running(phase avsysv1.Phase) {
	was := g.world.DeepCopy()
	was.Status.Phase = phase
	g.world.Status.Phase = avsysv1.PhaseRunning
	g.p.srv.watchGame(was, g.world)
}

// connected samples until the panel has its connection to the game open.
func (g *stoppingGame) connected(t *testing.T) {
	t.Helper()
	if got, ok := g.sample(); !ok || got.TPS != 20 {
		t.Fatalf("a running game: %+v, %v", got, ok)
	}
	if n := g.game.alive(1, time.Second); n != 1 {
		t.Fatalf("%d connections open to a running game, want one", n)
	}
}

// heldOff checks that, while the server still shows as running, the panel
// neither has a connection open to the game nor makes one.
func (g *stoppingGame) heldOff(t *testing.T, how string) {
	t.Helper()
	if n := g.game.alive(0, time.Second); n != 0 {
		t.Errorf("%s: %d connections left open to a stopping game", how, n)
	}
	made := g.game.connections()
	if got, ok := g.sample(); ok {
		t.Errorf("%s: a stopping game was asked how fast it ticks: %+v", how, got)
	}
	if g.game.connections() != made {
		t.Errorf("%s: a connection was made to a stopping game", how)
	}
}

// resumes checks that the game is asked again once it is running again.
func (g *stoppingGame) resumes(t *testing.T, how string) {
	t.Helper()
	g.running(avsysv1.PhaseStarting)
	if got, ok := g.sample(); !ok || got.TPS != 20 {
		t.Errorf("%s: running again, the game is not asked: %+v, %v", how, got, ok)
	}
}

// Told to stop from the panel, the game has no connection of the panel's
// open by the time it is told: it would wait on it, logging a stack trace a
// second. None is made until it runs again.
func TestStopHangsUpFirst(t *testing.T) {
	g := newStoppingGame(t)
	g.connected(t)
	if code, _ := g.p.do("operator", "POST", "/orgs/alpha/servers/world/stop", ""); code != http.StatusOK {
		t.Fatalf("stop: %d", code)
	}
	g.heldOff(t, "stopped")
	g.resumes(t, "stopped")
}

// Restarted in place, the game is told to stop over a connection of its
// own, with the one kept to ask how fast it ticks already closed.
func TestRestartHangsUpFirst(t *testing.T) {
	g := newStoppingGame(t)
	g.connected(t)
	if code, _ := g.p.do("operator", "POST", "/orgs/alpha/servers/world/restart", ""); code != http.StatusOK {
		t.Fatalf("restart: %d", code)
	}
	open, ok := g.told("stop")
	if !ok {
		t.Fatal("restarted in place, the game was not told to stop")
	}
	if open != 1 {
		t.Errorf("the game was told to stop with %d connections open, want only the one it came on", open)
	}
	g.heldOff(t, "restarted")
	g.resumes(t, "restarted")
}

// Typed at the panel's console, a stop closes the panel's connections first
// too, however it is written.
func TestConsoleStopHangsUpFirst(t *testing.T) {
	for _, command := range []string{"stop", "/minecraft:stop", "execute as @a run stop"} {
		t.Run(command, func(t *testing.T) {
			g := newStoppingGame(t)
			g.connected(t)
			if code, _ := g.p.do("operator", "POST", "/orgs/alpha/servers/world/console", `{"command":"`+command+`"}`); code != http.StatusOK {
				t.Fatalf("console: %d", code)
			}
			if open, ok := g.told(strings.TrimPrefix(command, "/")); !ok || open != 1 {
				t.Errorf("the game was told with %d connections open, want only the one it came on", open)
			}
			g.heldOff(t, command)
			g.resumes(t, command)
		})
	}
	// Anything else leaves the connection be.
	g := newStoppingGame(t)
	g.connected(t)
	others := []string{"say stop", "say run stop", "execute run say run stop", "list", "restart"}
	for _, command := range others {
		if code, _ := g.p.do("operator", "POST", "/orgs/alpha/servers/world/console", `{"command":"`+command+`"}`); code != http.StatusOK {
			t.Fatalf("%s: %d", command, code)
		}
	}
	if got, ok := g.sample(); !ok || got.TPS != 20 || g.game.connections() != 1+len(others) {
		t.Errorf("after other commands: %+v, %v, %d connections made, want the kept one and one each", got, ok, g.game.connections())
	}
}

func TestStopsCommand(t *testing.T) {
	for command, want := range map[string]bool{
		"stop":                                   true,
		"/stop":                                  true,
		"minecraft:stop":                         true,
		"STOP":                                   true,
		"execute run stop":                       true,
		"minecraft:execute run /stop":            true,
		"execute as @a at @s run minecraft:stop": true,
		"execute as @a run execute at @s run stop": true,
		// A word in what a command is given is not run.
		"say stop":                   false,
		"say run stop":               false,
		"tellraw @a \"run stop\"":    false,
		"execute run say run stop":   false,
		"execute as stop run say hi": false,
		"execute as @a":              false,
		"stopsound @a":               false,
		"someplugin:stop":            false,
		"list":                       false,
		"":                           false,
	} {
		if got := stopsCommand(command, false); got != want {
			t.Errorf("%q: %v, want %v", command, got, want)
		}
	}
	// Restart stops the game on Paper and its kind, and is no command of
	// the game's own.
	for command, want := range map[string]bool{
		"restart": true, "bukkit:restart": true, "spigot:restart": true, "execute run restart": true,
		"say restart": false, "say run restart": false,
	} {
		if got := stopsCommand(command, true); got != want {
			t.Errorf("on Paper, %q: %v, want %v", command, got, want)
		}
	}
	if stopsCommand("restart", false) {
		t.Error("restart is taken for a stop on a game without it")
	}
}

// Stopped some other way, as from a shell in its workspace, the game says
// so in its log some seconds before it waits on the connections: the panel
// hangs up then.
func TestLoggedStopHangsUp(t *testing.T) {
	g := newStoppingGame(t)
	g.connected(t)
	// A player saying it is not the game stopping.
	g.log(t, "[12:00:00] [Server thread/INFO]: <Steve> Stopping server")
	g.log(t, "[12:00:00] [Server thread/INFO]: [Steve] Stopping server")
	g.log(t, "[12:00:00] [Server thread/INFO]: <Steve> ]: Stopping server")
	g.log(t, "[12:00:00] [Server thread/INFO]: [Not Secure] <Steve> ]: Stopping server")
	g.log(t, "[12:00:00 INFO]: <Steve> ]: Stopping server")
	if n := g.game.alive(0, 200*time.Millisecond); n != 1 {
		t.Fatalf("a player's words closed the connection: %d open", n)
	}
	g.log(t, "\x1b[0m[12:00:01] [Server thread/INFO]: Stopping server\x1b[m")
	g.heldOff(t, "stopped from its log")
	g.resumes(t, "stopped from its log")
}

func TestStoppingLine(t *testing.T) {
	for line, want := range map[string]bool{
		// Vanilla and Fabric, Forge and NeoForge, Paper and its kind.
		"[12:00:00] [Server thread/INFO]: Stopping server":                                                       true,
		"\x1b[0m[12:00:00] [Server thread/INFO]: Stopping server\x1b[m":                                          true,
		"[12:00:00] [Server thread/INFO] [minecraft/MinecraftServer]: Stopping server\r":                         true,
		"[21Nov2026 12:00:00.000] [Server thread/INFO] [net.minecraft.server.MinecraftServer/]: Stopping server": true,
		"[12:00:00 INFO]: Stopping server":                                                                       true,
		// Not the game's own line.
		"[12:00:00] [Server thread/INFO]: Stopping the server":                             false,
		"[12:00:00] [Server thread/INFO]: System chat: Stopping the server":                false,
		"[12:00:00] [Server thread/INFO]: [Rcon: Stopping the server]":                     false,
		"[12:00:00] [Server thread/INFO]: Stopping server soon, says a plugin":             false,
		"[12:00:00] [Server thread/WARN]: Thread RconClient failed to exit after 1 second": false,
		"[12:00:00] [Server thread/WARN]: Stopping server":                                 false,
		"[12:00:00] [Worker-Main-1/INFO]: Stopping server":                                 false,
		"Stopping server": false,
		// Players saying so, however they shape it.
		"[12:00:00] [Server thread/INFO]: <Steve> Stopping server":                       false,
		"[12:00:00] [Server thread/INFO]: <Steve> ]: Stopping server":                    false,
		"[12:00:00] [Server thread/INFO]: [Not Secure] <Steve> ]: Stopping server":       false,
		"[12:00:00] [Server thread/INFO]: [Steve] ]: Stopping server":                    false,
		"[12:00:00 INFO]: <Steve> ]: Stopping server":                                    false,
		"[12:00:00 INFO]: [Not Secure] <Steve> [12:00:00 INFO]: Stopping server":         false,
		"[12:00:00] [Server thread/INFO]: <Steve> [Server thread/INFO]: Stopping server": false,
	} {
		if got := stoppingLine(line); got != want {
			t.Errorf("%q: %v, want %v", line, got, want)
		}
	}
}

// What the cluster says of the server closes the connections too, before
// the game is seen to stop: asked to stop, to sleep or to restart, no
// longer running, or gone.
func TestWatchHangsUp(t *testing.T) {
	for name, change := range map[string]func(mc *avsysv1.MinecraftServer){
		"stopped": func(mc *avsysv1.MinecraftServer) { mc.Spec.State = avsysv1.StateStopped },
		"asleep":  func(mc *avsysv1.MinecraftServer) { mc.Annotations = map[string]string{avsysv1.AnnotationAsleep: "now"} },
		"restarted": func(mc *avsysv1.MinecraftServer) {
			mc.Annotations = map[string]string{avsysv1.AnnotationRestart: "now"}
		},
		"starting": func(mc *avsysv1.MinecraftServer) { mc.Status.Phase = avsysv1.PhaseStarting },
		"stopping": func(mc *avsysv1.MinecraftServer) { mc.Status.Phase = avsysv1.PhaseStopping },
		"deleting": func(mc *avsysv1.MinecraftServer) { now := metav1.Now(); mc.DeletionTimestamp = &now },
	} {
		t.Run(name, func(t *testing.T) {
			g := newStoppingGame(t)
			g.connected(t)
			now := g.world.DeepCopy()
			change(now)
			g.p.srv.watchGame(g.world, now)
			// Still said to be running, as the sampler's list may be.
			g.heldOff(t, name)
			// Changes to it that do not stop it do not let it be asked
			// again: only its running again does.
			same := g.world.DeepCopy()
			same.Status.Players = &avsysv1.PlayersStatus{Online: 1}
			g.p.srv.watchGame(g.world, same)
			if _, ok := g.sample(); ok {
				t.Errorf("%s: asked again before it was seen running again", name)
			}
			g.resumes(t, name)
		})
	}
	// Changes to a running server that does not stop leave it be.
	g := newStoppingGame(t)
	g.connected(t)
	now := g.world.DeepCopy()
	now.Spec.Version = "1.21.5"
	now.Status.Players = &avsysv1.PlayersStatus{Online: 1}
	g.p.srv.watchGame(g.world, now)
	if got, ok := g.sample(); !ok || got.TPS != 20 || g.game.connections() != 1 {
		t.Errorf("after a change that does not stop it: %+v, %v, %d connections", got, ok, g.game.connections())
	}
}

// A game told to stop that does not, and is never seen to, is asked again
// after a while.
func TestHoldEnds(t *testing.T) {
	was := holdFor
	holdFor = 100 * time.Millisecond
	t.Cleanup(func() { holdFor = was })
	g := newStoppingGame(t)
	g.connected(t)
	g.p.srv.lines.hangUp("mc-alpha/world")
	if _, ok := g.sample(); ok {
		t.Fatal("asked while held")
	}
	time.Sleep(holdFor)
	if got, ok := g.sample(); !ok || got.TPS != 20 {
		t.Errorf("not asked again after the hold: %+v, %v", got, ok)
	}
}

// A connection made for one command is closed after it, whether the game
// took it or not.
func TestShortRCONCloses(t *testing.T) {
	g := newStoppingGame(t)
	ctx := context.Background()
	if out, err := g.p.srv.rcon(ctx, g.world, "list"); err != nil || out == "" {
		t.Fatalf("list: %q, %v", out, err)
	}
	g.game.password = "changed"
	if _, err := g.p.srv.rcon(ctx, g.world, "list"); err == nil {
		t.Fatal("a wrong password was taken")
	}
	if open := g.game.open(); open != 0 {
		t.Errorf("%d connections left open", open)
	}
}
