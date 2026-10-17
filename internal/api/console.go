package api

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
	"github.com/GT-Drehmal/avSYS/internal/controller"
	"github.com/GT-Drehmal/avSYS/internal/kube"
	"github.com/GT-Drehmal/avSYS/internal/store"
)

// sse prepares a response for server-sent events and returns a function that
// writes one event and flushes it.
func sse(w http.ResponseWriter) func(event string, data any) error {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	// Tells nginx-style proxies not to buffer the stream.
	h.Set("X-Accel-Buffering", "no")
	rc := http.NewResponseController(w)
	w.WriteHeader(http.StatusOK)
	_ = rc.Flush()
	return func(event string, data any) error {
		body, err := json.Marshal(data)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, body); err != nil {
			return err
		}
		return rc.Flush()
	}
}

const (
	consoleTail  = 500
	consoleBatch = 200
)

// streamConsole follows the game's log. Each time it (re)attaches, after a
// restart for example, it sends "reset" followed by the recent history, so
// the client always shows one coherent log.
func (s *Server) streamConsole(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, mc, err := s.server(r, u, store.RoleViewer)
	if err != nil {
		return err
	}
	ctx := r.Context()
	send := sse(w)

	for ctx.Err() == nil {
		logs, err := kube.StreamLogs(ctx, s.Kube, a.Namespace, mc.Name, controller.MinecraftContainer, consoleTail)
		if err != nil {
			// No pod or no game container yet: the server is stopped or still
			// being created. Keep the stream open and look again shortly.
			if err := send("waiting", nil); err != nil {
				return nil
			}
			select {
			case <-ctx.Done():
			case <-time.After(2 * time.Second):
			}
			continue
		}
		if err := send("reset", nil); err != nil {
			logs.Close()
			return nil
		}

		lines := make(chan string, consoleBatch)
		go func() {
			defer close(lines)
			sc := bufio.NewScanner(logs)
			sc.Buffer(make([]byte, 64*1024), 1024*1024)
			for sc.Scan() {
				select {
				case lines <- sc.Text():
				case <-ctx.Done():
					return
				}
			}
		}()
		// Lines are sent in batches of whatever has accumulated, which keeps
		// a burst (the initial history, a crash dump) from costing one flush
		// per line.
		for line := range lines {
			batch := []string{line}
		drain:
			for len(batch) < consoleBatch {
				select {
				case more, ok := <-lines:
					if !ok {
						break drain
					}
					batch = append(batch, more)
				default:
					break drain
				}
			}
			if err := send("lines", batch); err != nil {
				logs.Close()
				return nil
			}
		}
		logs.Close()
		select {
		case <-ctx.Done():
		case <-time.After(time.Second):
		}
	}
	return nil
}

func (s *Server) runCommand(w http.ResponseWriter, r *http.Request, u *store.User) error {
	a, mc, err := s.server(r, u, store.RoleOperator)
	if err != nil {
		return err
	}
	var in struct {
		Command string
		// As says who the game should take the command to be from. "server"
		// types it at the server console: players see "[Server]", and what
		// the command prints goes to the log, not into the answer. Left out,
		// the command goes over RCON: the answer carries what it printed,
		// and the game names its sender "Rcon".
		As string
	}
	if err := decode(r, &in); err != nil {
		return err
	}
	command := strings.TrimPrefix(strings.TrimSpace(in.Command), "/")
	if command == "" {
		return errBadRequest("Enter a command.")
	}
	if strings.ContainsAny(command, "\r\n") {
		return errBadRequest("Enter one command, on one line.")
	}
	if in.As != "" && in.As != "server" {
		return errBadRequest(`"as" is "server" or left out.`)
	}
	if mc.Status.Phase != avsysv1.PhaseRunning {
		return &Error{Status: http.StatusConflict, Code: "not_running", Message: "The server is not running."}
	}
	// While a snapshot holds the game's saves, none is let undo that or
	// write the world out under it: the copy would hold files from before
	// and after one another.
	if _, paused := mc.Annotations[avsysv1.AnnotationSavingPaused]; paused && savesCommand(command) {
		return &Error{Status: http.StatusConflict, Code: "snapshotting", Message: "A snapshot is being taken. Try again when it is done."}
	}
	output := ""
	typed := false
	if in.As == "server" {
		typed, err = s.typeAtConsole(r.Context(), mc, command)
		if err != nil {
			s.Log.Warn("console command failed", "server", mc.Name, "error", err)
			return &Error{Status: http.StatusBadGateway, Code: "console", Message: "The server did not take the command."}
		}
	}
	// A server started before the console could be typed at has no way in
	// but RCON, until its next restart.
	if !typed {
		if output, err = s.rcon(r.Context(), mc, command); err != nil {
			s.Log.Warn("rcon command failed", "server", mc.Name, "error", err)
			return &Error{Status: http.StatusBadGateway, Code: "rcon", Message: "The server did not answer the command."}
		}
	}
	s.audit(r.Context(), u, a.Org, "console.command", mc.Name, command)
	writeJSON(w, http.StatusOK, map[string]string{"output": output})
	return nil
}

// consoleTurn returns the lock that writers to one server's console take in turn.
func (s *Server) consoleTurn(key string) *sync.Mutex {
	turn, _ := s.consoles.LoadOrStore(key, &sync.Mutex{})
	return turn.(*sync.Mutex)
}

// typeAtConsole writes one line to the game's console input, through the
// workspace that shares it. ok is false when this server has no such input:
// its pod is from before there was one.
func (s *Server) typeAtConsole(ctx context.Context, mc *avsysv1.MinecraftServer, command string) (ok bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	// One writer at a time per server. Two long lines written to a pipe at
	// once can come out interleaved, as one command that nobody typed.
	turn := s.consoleTurn(mc.Namespace + "/" + mc.Name)
	turn.Lock()
	defer turn.Unlock()
	ws, err := s.Gateway.DialWorkspace(ctx, mc.Namespace, mc.Name)
	if err != nil {
		return false, err
	}
	defer ws.Close()
	// Nothing below watches ctx by itself. Closing the connection when the
	// time is up is what ends a session that has stopped answering.
	stop := context.AfterFunc(ctx, func() { ws.Close() })
	defer stop()
	session, err := ws.NewSession()
	if err != nil {
		return false, err
	}
	defer session.Close()
	session.Stdin = strings.NewReader(command + "\n")
	// Opening the pipe waits for the game to be reading it, which it is
	// unless it is between two runs; hence the limit. The command itself
	// arrives on stdin and is never part of a command line.
	const script = `[ -p ` + controller.ConsolePipe + ` ] || exit 3; exec timeout 5 sh -c 'cat > ` + controller.ConsolePipe + `'`
	err = session.Run(script)
	var exit *ssh.ExitError
	if errors.As(err, &exit) && exit.ExitStatus() == 3 {
		return false, nil
	}
	return err == nil, err
}

// savesCommand says whether a console command is one of the game's own for
// its saving: save-on, save-off and save-all, with or without a slash or the
// game's namespace before them, and also when execute runs one ("execute as
// @a run save-all"), at any depth.
func savesCommand(command string) bool {
	words := strings.Fields(strings.ToLower(command))
	for i, w := range words {
		if i > 0 && words[i-1] != "run" {
			continue
		}
		w = strings.TrimPrefix(strings.TrimPrefix(w, "/"), "minecraft:")
		if w == "save-on" || w == "save-off" || w == "save-all" {
			return true
		}
	}
	return false
}
