package api

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

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
	var in struct{ Command string }
	if err := decode(r, &in); err != nil {
		return err
	}
	command := strings.TrimPrefix(strings.TrimSpace(in.Command), "/")
	if command == "" {
		return errBadRequest("Enter a command.")
	}
	if mc.Status.Phase != avsysv1.PhaseRunning {
		return &Error{Status: http.StatusConflict, Code: "not_running", Message: "The server is not running."}
	}
	output, err := s.rcon(r.Context(), mc, command)
	if err != nil {
		s.Log.Warn("rcon command failed", "server", mc.Name, "error", err)
		return &Error{Status: http.StatusBadGateway, Code: "rcon", Message: "The server did not answer the command."}
	}
	s.audit(r.Context(), u, a.Org, "console.command", mc.Name, command)
	writeJSON(w, http.StatusOK, map[string]string{"output": output})
	return nil
}
