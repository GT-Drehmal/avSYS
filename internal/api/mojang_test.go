package api

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GT-Drehmal/avSYS/internal/mojang"
)

// droppingMojang is Mojang as it is seen from a home line or a cluster: a
// real server, at every one of Mojang's names, where some new connections
// get no answer at all. Each request is on a connection of its own, as
// none is kept open, and every dropEvery-th connection made goes nowhere.
type droppingMojang struct {
	srv *httptest.Server
	// accounts are the accounts it knows, by lowercase name.
	accounts map[string]fakeAccount
	// hang says which batches of names it never answers.
	hang func(names []string) bool
	// skin is every account's skin.
	skin []byte

	dials, dropped atomic.Int32
	mu             sync.Mutex
	batches        [][]string     // answered
	asked          map[string]int // names looked up on their own
}

type fakeAccount struct{ id, name string }

const dropEvery = 3

func newDroppingMojang(t *testing.T) *droppingMojang {
	m := &droppingMojang{accounts: map[string]fakeAccount{}, asked: map[string]int{}}
	img := image.NewNRGBA(image.Rect(0, 0, 64, 64))
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	m.skin = buf.Bytes()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /minecraft/profile/lookup/bulk/byname", func(w http.ResponseWriter, r *http.Request) {
		var names []string
		if err := json.NewDecoder(r.Body).Decode(&names); err != nil || len(names) > nameBatch {
			http.Error(w, "CONSTRAINT_VIOLATION", http.StatusBadRequest)
			return
		}
		if m.hang != nil && m.hang(names) {
			<-r.Context().Done()
			return
		}
		m.mu.Lock()
		m.batches = append(m.batches, names)
		m.mu.Unlock()
		out := []map[string]string{}
		for _, name := range names {
			if a, ok := m.accounts[strings.ToLower(name)]; ok {
				out = append(out, map[string]string{"id": a.id, "name": a.name})
			}
		}
		_ = json.NewEncoder(w).Encode(out)
	})
	mux.HandleFunc("GET /users/profiles/minecraft/{name}", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		m.asked[strings.ToLower(r.PathValue("name"))]++
		m.mu.Unlock()
		a, ok := m.accounts[strings.ToLower(r.PathValue("name"))]
		if !ok {
			http.Error(w, `{"errorMessage":"Couldn't find any profile with name"}`, http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"id": a.id, "name": a.name})
	})
	mux.HandleFunc("GET /session/minecraft/profile/{uuid}", func(w http.ResponseWriter, r *http.Request) {
		for _, a := range m.accounts {
			if a.id == r.PathValue("uuid") {
				textures := base64.StdEncoding.EncodeToString([]byte(`{"textures":{"SKIN":{"url":"http://textures.minecraft.net/texture/abc"}}}`))
				fmt.Fprintf(w, `{"id":%q,"name":%q,"properties":[{"name":"textures","value":%q}]}`, a.id, a.name, textures)
				return
			}
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /texture/abc", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(m.skin) })
	m.srv = httptest.NewUnstartedServer(mux)
	m.srv.Config.SetKeepAlivesEnabled(false)
	m.srv.StartTLS()
	t.Cleanup(m.srv.Close)
	return m
}

// transport reaches it at every name, through the panel's own transport.
func (m *droppingMojang) transport() *http.Transport {
	tr := mojang.Transport()
	tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	tr.Proxy = nil
	dial := tr.DialContext
	tr.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		if m.dials.Add(1)%dropEvery == 0 {
			m.dropped.Add(1)
			// A port that gets no answer: an address nothing answers at.
			return dial(ctx, network, "192.0.2.1:443")
		}
		return dial(ctx, network, m.srv.Listener.Addr().String())
	}
	return tr
}

func (m *droppingMojang) answered() [][]string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.batches)
}

// 88 names pasted at once are checked ten at a time, each batch asked again
// on a new connection where one gets no answer: all are added in one
// request, in a few seconds, and the names nobody has are said.
func TestManyNamesChecked(t *testing.T) {
	askMojang(t, 2*time.Second, 10*time.Millisecond, 200*time.Millisecond)
	p := newPanel(t)
	m := newDroppingMojang(t)
	p.srv.catalog.http.Transport = m.transport()
	var names, want []string
	for i := range 88 {
		name := fmt.Sprintf("player%02d", i)
		names = append(names, name)
		if i%30 == 7 {
			continue // player07, player37, player67: nobody's
		}
		m.accounts[name] = fakeAccount{id: fmt.Sprintf("%032x", i+1), name: fmt.Sprintf("Player%02d", i)}
		want = append(want, fmt.Sprintf("Player%02d", i))
	}
	body, _ := json.Marshal(map[string]any{"name": "Event", "names": names})
	start := time.Now()
	code, l := sendList(t, p, "admin", "POST", "/orgs/alpha/player-lists", string(body))
	took := time.Since(start)
	if code != http.StatusCreated {
		t.Fatalf("made: %d %+v", code, l)
	}
	if l.names() != strings.Join(want, " ") {
		t.Errorf("players: %s", l.names())
	}
	if strings.Join(l.Missing, ",") != "player07,player37,player67" || len(l.Unchecked) != 0 {
		t.Errorf("missing %v, unchecked %v", l.Missing, l.Unchecked)
	}
	var asked []string
	for _, b := range m.answered() {
		if len(b) > nameBatch {
			t.Errorf("a batch of %d", len(b))
		}
		asked = append(asked, b...)
	}
	if slices.Sort(asked); !slices.Equal(asked, names) {
		t.Errorf("answered for %d names, want each of 88 once", len(asked))
	}
	if m.dropped.Load() < 2 {
		t.Errorf("only %d of %d connections went nowhere", m.dropped.Load(), m.dials.Load())
	}
	if took > 5*time.Second {
		t.Errorf("took %v", took)
	}

	// Looked up a moment ago: not asked again.
	before := len(m.answered())
	if code, l = sendList(t, p, "admin", "POST", "/orgs/alpha/player-lists/"+l.ID+"/players", `{"names":["PLAYER01","player02"]}`); code != http.StatusOK || len(l.Missing)+len(l.Unchecked) != 0 {
		t.Errorf("again: %d %+v", code, l)
	}
	if len(m.answered()) != before {
		t.Errorf("asked again about names looked up a moment ago")
	}
}

// A batch Mojang never answers is left unchecked after its tries, and the
// names of the others are added: in bounded time, and said apart from those
// nobody has.
func TestNamesLeftUnchecked(t *testing.T) {
	askMojang(t, 300*time.Millisecond, 10*time.Millisecond, 200*time.Millisecond)
	p := newPanel(t)
	m := newDroppingMojang(t)
	m.hang = func(names []string) bool { return slices.Contains(names, "player13") }
	p.srv.catalog.http.Transport = m.transport()
	var names []string
	for i := range 25 {
		name := fmt.Sprintf("player%02d", i)
		names = append(names, name)
		if i != 3 {
			m.accounts[name] = fakeAccount{id: fmt.Sprintf("%032x", i+1), name: name}
		}
	}
	body, _ := json.Marshal(map[string]any{"name": "Event", "names": names})
	start := time.Now()
	code, l := sendList(t, p, "admin", "POST", "/orgs/alpha/player-lists", string(body))
	took := time.Since(start)
	if code != http.StatusCreated {
		t.Fatalf("made: %d %+v", code, l)
	}
	if len(l.Players) != 14 || strings.Join(l.Missing, ",") != "player03" || strings.Join(l.Unchecked, ",") != strings.Join(names[10:20], ",") {
		t.Errorf("%d players, missing %v, unchecked %v", len(l.Players), l.Missing, l.Unchecked)
	}
	// Five tries of 300 ms at most, and the waits between.
	if most := time.Duration(mojang.Tries)*mojang.Try + 15*mojang.Backoff + time.Second; took > most {
		t.Errorf("took %v, more than %v", took, most)
	}

	// Tried again once it answers: they are added.
	m.hang = nil
	body, _ = json.Marshal(map[string]any{"names": l.Unchecked})
	if code, l = sendList(t, p, "admin", "POST", "/orgs/alpha/player-lists/"+l.ID+"/players", string(body)); code != http.StatusOK || len(l.Players) != 24 || len(l.Unchecked) != 0 {
		t.Errorf("tried again: %d %d players, unchecked %v", code, len(l.Players), l.Unchecked)
	}
}

// A player's head, and a name looked up on its own, are asked for again
// where a connection gets no answer; a name nobody has is Mojang's answer,
// asked once.
func TestMojangLookupsAskedAgain(t *testing.T) {
	askMojang(t, 2*time.Second, 10*time.Millisecond, 200*time.Millisecond)
	p := newPanel(t)
	m := newDroppingMojang(t)
	m.accounts["steve"] = fakeAccount{id: "8667ba71b85a4004af54457a9734eed7", name: "Steve"}
	p.srv.catalog.http.Transport = m.transport()

	// Each of the four asks a new connection; one in three goes nowhere.
	for range 4 {
		p.srv.heads = heads{}
		p.srv.catalog = &catalog{http: p.srv.catalog.http, userAgent: "test", entries: map[string]cacheEntry{}}
		if rec := p.send("viewer", "GET", "/players/8667ba71b85a4004af54457a9734eed7/head", nil); rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/png" {
			t.Fatalf("head: %d %s", rec.Code, rec.Body)
		}
	}
	if m.dropped.Load() < 2 {
		t.Errorf("only %d of %d connections went nowhere", m.dropped.Load(), m.dials.Load())
	}
	for range 3 {
		p.srv.catalog = &catalog{http: p.srv.catalog.http, userAgent: "test", entries: map[string]cacheEntry{}}
		if code, text := call(p, "viewer", "PUT", "/me/minecraft", `{"name":"steve"}`); code != http.StatusOK || !strings.Contains(text, "8667ba71b85a4004af54457a9734eed7") {
			t.Fatalf("linked: %d %s", code, text)
		}
		if name, err := p.srv.currentName(context.Background(), "8667ba71b85a4004af54457a9734eed7"); err != nil || name != "Steve" {
			t.Fatalf("current name: %q %v", name, err)
		}
	}
	if code, text := call(p, "viewer", "PUT", "/me/minecraft", `{"name":"Herobrine"}`); code != http.StatusNotFound || !strings.Contains(text, "no_such_player") {
		t.Errorf("nobody's name: %d %s", code, text)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if n := m.asked["herobrine"]; n != 1 {
		t.Errorf("a name nobody has was asked about %d times", n)
	}
}
