package router

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GT-Drehmal/avSYS/internal/mcproto"
	"github.com/GT-Drehmal/avSYS/internal/mojang"
)

// Mojang's session server is asked again where it gives no answer, or says
// to come back later; that the player did not join is its answer, asked
// once.
func TestHasJoinedAskedAgain(t *testing.T) {
	was := struct {
		try, backoff time.Duration
	}{mojang.Try, mojang.Backoff}
	mojang.Try, mojang.Backoff = 200*time.Millisecond, 10*time.Millisecond
	defer func() { mojang.Try, mojang.Backoff = was.try, was.backoff }()

	var asked atomic.Int32
	joined := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch asked.Add(1) {
		case 1:
			// No answer at all.
			<-r.Context().Done()
			return
		case 2:
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		if !joined || r.URL.Query().Get("username") != "Steve" || r.URL.Query().Get("serverId") != "hash" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		_, _ = w.Write([]byte(`{"id":"8667ba71b85a4004af54457a9734eed7","name":"Steve"}`))
	}))
	defer srv.Close()

	start := time.Now()
	uuid, err := hasJoined(context.Background(), srv.URL, "Steve", "hash")
	if err != nil || uuid != "8667ba71b85a4004af54457a9734eed7" || asked.Load() != 3 {
		t.Errorf("got %q %v, asked %d times", uuid, err, asked.Load())
	}
	if took := time.Since(start); took > time.Second {
		t.Errorf("took %v", took)
	}

	asked.Store(2)
	joined = false
	if _, err := hasJoined(context.Background(), srv.URL, "Steve", "hash"); !errors.Is(err, mcproto.ErrNotJoined) || asked.Load() != 3 {
		t.Errorf("not joined: %v, asked %d times", err, asked.Load()-2)
	}
}
