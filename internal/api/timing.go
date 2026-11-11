package api

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

// timed says, on every answer of the API, how long the panel took to start
// it, as a Server-Timing header: the browser's network panel shows it beside
// each request, which is where a slow one is looked for. For a stream, such
// as the event stream or a download, that is the time to its first byte.
func timed(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(&timingWriter{ResponseWriter: w, start: time.Now()}, r)
	})
}

type timingWriter struct {
	http.ResponseWriter
	start   time.Time
	written bool
}

func (t *timingWriter) stamp() {
	if t.written {
		return
	}
	t.written = true
	ms := float64(time.Since(t.start).Microseconds()) / 1000
	t.Header().Add("Server-Timing", fmt.Sprintf("app;dur=%.1f", ms))
}

func (t *timingWriter) WriteHeader(code int) {
	t.stamp()
	t.ResponseWriter.WriteHeader(code)
}

func (t *timingWriter) Write(b []byte) (int, error) {
	t.stamp()
	return t.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach what is underneath, for
// flushing and deadlines.
func (t *timingWriter) Unwrap() http.ResponseWriter { return t.ResponseWriter }

// Flush and Hijack are passed through for code that asks for them directly,
// as a websocket does.
func (t *timingWriter) Flush() {
	t.stamp()
	if f, ok := t.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (t *timingWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := t.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, http.ErrNotSupported
	}
	return h.Hijack()
}
