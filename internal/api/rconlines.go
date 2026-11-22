package api

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"regexp"
	"strings"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
	"github.com/GT-Drehmal/avSYS/internal/controller"
	"github.com/GT-Drehmal/avSYS/internal/kube"
	"github.com/GT-Drehmal/avSYS/internal/rcon"
)

// A game that stops ends each RCON connection open to it and waits for it
// to finish, one second at a time, logging a warning with a stack trace
// each second one has not, and after five gives up waiting. The
// connections the panel keeps open to a game, to ask how fast it ticks and
// to read its lists, are therefore closed as soon as it is seen to stop:
// before the panel tells it to, when its log says it is stopping, or when
// its server is asked to stop or is no longer running. None is made or
// used again until it is seen running again.

// errGameStopping is the answer to a connection asked for while the game is
// stopping, or has not been seen running since.
var errGameStopping = errors.New("the game is stopping")

// holdFor is how long a game told to stop is left alone where it is not
// seen to stop: one that kept running is asked again after that.
var holdFor = 2 * time.Minute

// rconLines are the connections the panel keeps open to games, by
// namespace/name.
type rconLines struct {
	mu      sync.Mutex
	servers map[string]*rconLine
}

type rconLine struct {
	// held is when the game was last seen stopping: zero while it runs.
	held  time.Time
	conns map[net.Conn]bool
	// unfollow stops the following of the game's log, while there is a
	// connection to follow it for.
	unfollow context.CancelFunc
}

func (l *rconLines) line(key string) *rconLine {
	if l.servers == nil {
		l.servers = map[string]*rconLine{}
	}
	line := l.servers[key]
	if line == nil {
		line = &rconLine{conns: map[net.Conn]bool{}}
		l.servers[key] = line
	}
	return line
}

// heldOff says whether a game is held: no connection is made to it.
func (l *rconLines) heldOff(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.heldLocked(key)
}

func (l *rconLines) heldLocked(key string) bool {
	line := l.servers[key]
	if line == nil || line.held.IsZero() {
		return false
	}
	if time.Since(line.held) < holdFor {
		return true
	}
	line.held = time.Time{}
	return false
}

// add notes conn as open to a game, and follows the game's log with follow
// while any is. ok is false, and conn is not noted, while the game is held.
func (l *rconLines) add(key string, conn net.Conn, follow func() context.CancelFunc) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.heldLocked(key) {
		return false
	}
	line := l.line(key)
	line.conns[conn] = true
	if line.unfollow == nil && follow != nil {
		line.unfollow = follow()
	}
	return true
}

// open says whether conn is still noted as open: not hung up on.
func (l *rconLines) open(key string, conn net.Conn) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	line := l.servers[key]
	return line != nil && line.conns[conn]
}

// remove closes conn and lets go of it.
func (l *rconLines) remove(key string, conn net.Conn) {
	conn.Close()
	l.mu.Lock()
	defer l.mu.Unlock()
	line := l.servers[key]
	if line == nil || !line.conns[conn] {
		return
	}
	delete(line.conns, conn)
	if len(line.conns) == 0 && line.unfollow != nil {
		line.unfollow()
		line.unfollow = nil
	}
}

// hangUp closes every connection open to a game, and makes none again until
// it is seen running again.
func (l *rconLines) hangUp(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	line := l.line(key)
	line.held = time.Now()
	for conn := range line.conns {
		conn.Close()
	}
	clear(line.conns)
	if line.unfollow != nil {
		line.unfollow()
		line.unfollow = nil
	}
}

// resume lets connections be made to a game seen running again.
func (l *rconLines) resume(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if line := l.servers[key]; line != nil {
		line.held = time.Time{}
	}
}

// forget hangs up on a server that is gone, and lets go of all that is
// kept of it: one made under its name later is another.
func (l *rconLines) forget(key string) {
	l.hangUp(key)
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.servers, key)
}

// keptLine is an RCON connection to a game, logged in, kept noted until it
// is released so that it is closed as the game stops.
type keptLine struct {
	*rcon.Client
	key   string
	conn  net.Conn
	lines *rconLines
}

// hungUp says whether the game was hung up on since.
func (k *keptLine) hungUp() bool { return !k.lines.open(k.key, k.conn) }

// release closes the connection.
func (k *keptLine) release() { k.lines.remove(k.key, k.conn) }

// dialGame opens an RCON connection to a server's game, to be kept for one
// command after another. The time allowed for logging in is ctx's; the
// connection then lasts until released, or until the game stops.
func (s *Server) dialGame(ctx context.Context, mc *avsysv1.MinecraftServer) (*keptLine, error) {
	if s.Kube == nil || s.Dialer == nil {
		return nil, errors.New("no way to reach the game")
	}
	key := mc.Namespace + "/" + mc.Name
	if s.lines.heldOff(key) {
		return nil, errGameStopping
	}
	secret, err := s.Kube.CoreV1().Secrets(mc.Namespace).Get(ctx, controller.RCONSecretName(mc.Name), metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	conn, err := s.Dialer.DialPod(ctx, mc.Namespace, mc.Name, controller.RCONPort)
	if err != nil {
		return nil, err
	}
	// Noted before logging in, so that a stop meanwhile closes it too.
	ns, name := mc.Namespace, mc.Name
	if !s.lines.add(key, conn, func() context.CancelFunc {
		follow, unfollow := context.WithCancel(context.Background())
		go s.watchForStop(follow, ns, name)
		return unfollow
	}) {
		conn.Close()
		return nil, errGameStopping
	}
	k := &keptLine{key: key, conn: conn, lines: &s.lines}
	// Tunnelled connections ignore deadlines: the time allowed is kept to
	// by closing.
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	k.Client, err = rcon.Login(conn, string(secret.Data["password"]))
	if !stop() && err == nil {
		err = ctx.Err()
	}
	if err != nil {
		if k.hungUp() {
			err = errGameStopping
		}
		k.release()
		return nil, err
	}
	return k, nil
}

// followLog follows a game's log from now on. Tests replace it.
func (s *Server) followLog(ctx context.Context, namespace, name string) (io.ReadCloser, error) {
	if s.gameLog != nil {
		return s.gameLog(ctx, namespace, name)
	}
	if s.Kube == nil {
		return nil, errors.New("no way to read the game's log")
	}
	return kube.StreamLogs(ctx, s.Kube, namespace, name, controller.MinecraftContainer, 0)
}

// watchForStop follows a game's log while the panel has a connection open
// to it, and hangs up as soon as the game says it is stopping: however it
// was told to, as from a shell in its workspace, it logs that some seconds
// before it waits for the connections.
func (s *Server) watchForStop(ctx context.Context, namespace, name string) {
	key := namespace + "/" + name
	for ctx.Err() == nil {
		logs, err := s.followLog(ctx, namespace, name)
		if err == nil {
			sc := bufio.NewScanner(logs)
			sc.Buffer(make([]byte, 64*1024), 1024*1024)
			for sc.Scan() {
				if stoppingLine(sc.Text()) {
					logs.Close()
					s.lines.hangUp(key)
					return
				}
			}
			logs.Close()
		}
		// The log ends where the game does, or where the cluster stopped
		// sending it: followed again, while there is a connection.
		select {
		case <-ctx.Done():
		case <-time.After(5 * time.Second):
		}
	}
}

var ansiCodes = regexp.MustCompile("\x1b\\[[0-9;]*[A-Za-z]")

// stoppingLine says whether a line of a game's log is the one it logs as it
// starts to stop, on every kind of server:
//
//	[12:00:00] [Server thread/INFO]: Stopping server
//	[12:00:00 INFO]: Stopping server
//
// A player saying so in chat is not it: their name comes before.
func stoppingLine(line string) bool {
	line = strings.TrimSpace(ansiCodes.ReplaceAllString(line, ""))
	return strings.HasSuffix(line, "]: Stopping server")
}

// gameStopping says whether a server seen changing from old to mc is
// stopping its game, or not running it: asked to stop, to sleep or to
// restart, or no longer running.
func gameStopping(old, mc *avsysv1.MinecraftServer) bool {
	return !mc.DeletionTimestamp.IsZero() || !controller.WantsMinecraft(mc) || mc.Status.Phase != avsysv1.PhaseRunning ||
		(old != nil && old.Annotations[avsysv1.AnnotationRestart] != mc.Annotations[avsysv1.AnnotationRestart])
}

// watchGame hangs up on a server's game as soon as a change to the server
// says it is stopping, and lets the panel connect to it again once it is
// seen running again.
func (s *Server) watchGame(old, mc *avsysv1.MinecraftServer) {
	key := mc.Namespace + "/" + mc.Name
	switch {
	case gameStopping(old, mc):
		s.lines.hangUp(key)
	case old != nil && old.Status.Phase != avsysv1.PhaseRunning:
		s.lines.resume(key)
	}
}

// stopsCommand says whether a console command stops the game: stop, or
// restart on Paper and its kind, with or without a slash or a namespace,
// and also when execute runs one.
func stopsCommand(command string, restarts bool) bool {
	words := strings.Fields(strings.ToLower(command))
	for i, w := range words {
		if i > 0 && words[i-1] != "run" {
			continue
		}
		w = strings.TrimPrefix(w, "/")
		if at := strings.LastIndex(w, ":"); at >= 0 {
			w = w[at+1:]
		}
		if w == "stop" || (restarts && w == "restart") {
			return true
		}
	}
	return false
}
