package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
	"github.com/GT-Drehmal/avSYS/internal/store"
)

// mojangAccounts answers Mojang's lookup of a name as Mojang does: the
// account, or 404 for a name nobody has. down makes it fail as an outage
// would.
type mojangAccounts struct {
	mu    sync.Mutex
	known map[string]string // UUID by lowercase name
	// names are the accounts' names as they have them, by lowercase name.
	names map[string]string
	asked []string
	down  bool
	// files are other addresses' answers, as offline's.
	files map[string][]byte
}

func newMojangAccounts(names ...string) *mojangAccounts {
	m := &mojangAccounts{known: map[string]string{}, names: map[string]string{}, files: map[string][]byte{}}
	for i, name := range names {
		m.known[strings.ToLower(name)] = strings.Repeat(string(rune('a'+i)), 32)
		m.names[strings.ToLower(name)] = name
	}
	return m
}

func (m *mojangAccounts) RoundTrip(r *http.Request) (*http.Response, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	answer := func(status int, body []byte) (*http.Response, error) {
		return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(body)), Request: r}, nil
	}
	if name, ok := strings.CutPrefix(r.URL.String(), "https://api.mojang.com/users/profiles/minecraft/"); ok {
		m.asked = append(m.asked, name)
		if m.down {
			return nil, errors.New("offline")
		}
		// As the account has it, whatever the case asked in.
		if uuid, ok := m.known[strings.ToLower(name)]; ok {
			return answer(http.StatusOK, []byte(`{"id":"`+uuid+`","name":"`+m.names[strings.ToLower(name)]+`"}`))
		}
		return answer(http.StatusNotFound, nil)
	}
	if body, ok := m.files[r.URL.String()]; ok {
		return answer(http.StatusOK, body)
	}
	return nil, errors.New("offline: " + r.URL.Host)
}

func (m *mojangAccounts) lookups() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := m.asked
	m.asked = nil
	return out
}

// listAnswer is a saved list as the API answers with it.
type listAnswer struct {
	playerListView
	Missing []string `json:"missing"`
}

func (l listAnswer) names() string {
	var out []string
	for _, p := range l.Players {
		out = append(out, p.Name)
	}
	return strings.Join(out, " ")
}

func sendList(t *testing.T, p *panel, who, method, path, body string) (int, listAnswer) {
	t.Helper()
	code, text := call(p, who, method, path, body)
	var out listAnswer
	if code < 300 && text != "" {
		if err := json.Unmarshal([]byte(text), &out); err != nil {
			t.Fatalf("%s %s: %v: %s", method, path, err, text)
		}
	}
	return code, out
}

// A list is made, named players are looked up with Mojang and put on it as
// their accounts have them, and taken off. A name nobody has is said to be
// missing; Mojang out of reach fails the whole, adding nobody.
func TestSavedPlayerLists(t *testing.T) {
	p := newPanel(t)
	m := newMojangAccounts("Steve", "Alex", "Notch")
	p.srv.catalog.http.Transport = m

	code, club := sendList(t, p, "admin", "POST", "/orgs/alpha/player-lists", `{"name":" Club members ","names":["steve","Alex","STEVE","Herobrine","not a name!"]}`)
	if code != http.StatusCreated || club.Name != "Club members" || club.names() != "Alex Steve" {
		t.Fatalf("made: %d %+v", code, club)
	}
	if strings.Join(club.Missing, ",") != "not a name!,Herobrine" {
		t.Errorf("missing: %v", club.Missing)
	}
	if club.Players[1].UUID != m.known["steve"] {
		t.Errorf("Steve's UUID: %+v", club.Players[1])
	}
	if code, _ := call(p, "admin", "POST", "/orgs/alpha/player-lists", `{"name":"club MEMBERS"}`); code != http.StatusConflict {
		t.Errorf("a second list of the same name: %d", code)
	}
	if code, _ := call(p, "admin", "POST", "/orgs/alpha/player-lists", `{"name":"  "}`); code != http.StatusBadRequest {
		t.Errorf("a list with no name: %d", code)
	}
	m.lookups()

	// Pasted again, with one more: each name asked about once, the ones
	// looked up a moment ago not at all.
	id := club.ID
	code, club = sendList(t, p, "admin", "POST", "/orgs/alpha/player-lists/"+id+"/players", `{"names":["Notch","notch","Steve"]}`)
	if code != http.StatusOK || club.names() != "Alex Notch Steve" || len(club.Missing) != 0 {
		t.Errorf("added: %d %+v", code, club)
	}
	if asked := m.lookups(); strings.Join(asked, ",") != "notch" {
		t.Errorf("asked Mojang about %v, want Notch alone", asked)
	}

	// Out of reach: nothing is added, and it says so.
	m.down = true
	code, text := call(p, "admin", "POST", "/orgs/alpha/player-lists/"+id+"/players", `{"names":["Dinnerbone","Alex"]}`)
	if code != http.StatusBadGateway || !strings.Contains(text, "Could not check the names with Mojang") {
		t.Errorf("with Mojang down: %d %s", code, text)
	}
	m.down = false
	if _, got := sendList(t, p, "viewer", "GET", "/orgs/alpha/player-lists/"+id, ""); got.names() != "Alex Notch Steve" {
		t.Errorf("after a failed add: %s", got.names())
	}

	// Taken off by UUID; renamed.
	code, club = sendList(t, p, "admin", "DELETE", "/orgs/alpha/player-lists/"+id+"/players/"+m.known["notch"], "")
	if code != http.StatusOK || club.names() != "Alex Steve" {
		t.Errorf("taken off: %d %s", code, club.names())
	}
	if code, _ := call(p, "admin", "DELETE", "/orgs/alpha/player-lists/"+id+"/players/"+m.known["notch"], ""); code != http.StatusNotFound {
		t.Errorf("taking off one not on it: %d", code)
	}
	if code, got := sendList(t, p, "admin", "PATCH", "/orgs/alpha/player-lists/"+id, `{"name":"Club"}`); code != http.StatusOK || got.Name != "Club" {
		t.Errorf("renamed: %d %+v", code, got)
	}

	// Another organization's is not there for anyone outside it.
	if code, _ := call(p, "outsider", "GET", "/orgs/alpha/player-lists/"+id, ""); code != http.StatusNotFound {
		t.Errorf("seen from outside: %d", code)
	}
	var all []playerListView
	p.fetch("/orgs/alpha/player-lists", &all)
	if len(all) != 1 || all[0].Name != "Club" || len(all[0].Players) != 2 || all[0].Servers == nil {
		t.Errorf("all lists: %+v", all)
	}
}

// waitFor runs look until it says yes, for a few seconds: what a change to a
// list sets going runs on its own.
func waitFor(t *testing.T, what string, look func() bool) {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if look() {
			return
		}
	}
	t.Errorf("never: %s", what)
}

// ran collects what the game was told, from now on.
func (g *listGame) since() func() string {
	var all []string
	return func() string {
		if c := g.commands(); c != "" {
			all = append(all, c)
		}
		return strings.Join(all, "; ")
	}
}

// A server that uses a saved list has its players on its whitelist, and
// follows the list: a player added to it is put on, one taken off is taken
// off, and the list deleted takes them all off and is dropped from every
// server that used it. Its players are the list's, not added in game, and
// are taken off there.
func TestServersFollowTheirSavedLists(t *testing.T) {
	p := newPanel(t)
	ctx := context.Background()
	g := runningWithLists(t, p)
	m := newMojangAccounts("Steve", "Alex", "Notch")
	p.srv.catalog.http.Transport = m
	p.addServer("alpha", "lobby", "lobby.alpha.example")

	_, club := sendList(t, p, "admin", "POST", "/orgs/alpha/player-lists", `{"name":"Club","names":["Steve","Alex"]}`)
	_, staff := sendList(t, p, "admin", "POST", "/orgs/alpha/player-lists", `{"name":"Staff","names":["Notch"]}`)

	ran := g.since()
	if code, msg := p.do("admin", "PATCH", "/orgs/alpha/servers/world", `{"spec":{"whitelistLists":["`+club.ID+`","`+club.ID+`","no-such-list"],"opLists":["`+staff.ID+`"],"autoOp":"off"}}`); code != http.StatusOK {
		t.Fatalf("using the lists: %d %s", code, msg)
	}
	if got := world(t, p).Spec; !slices.Equal(got.WhitelistLists, []string{club.ID}) || !slices.Equal(got.OpLists, []string{staff.ID}) {
		t.Errorf("lists kept: %v %v, want each once, and none that is not there", got.WhitelistLists, got.OpLists)
	}
	waitFor(t, "the lists' players are put on", func() bool { return ran() == "op Notch; whitelist add Alex; whitelist add Steve" })
	if code, _ := p.do("admin", "PATCH", "/orgs/alpha/servers/lobby", `{"spec":{"whitelistLists":["`+club.ID+`"]}}`); code != http.StatusOK {
		t.Fatal("lobby using the list")
	}
	// Not added in game: the panel's, for the list.
	if got := gameLists(t, p); got["whitelist"] != "" || got["ops"] != "" {
		t.Errorf("added in game: %v", got)
	}
	if code, msg := p.do("admin", "DELETE", "/orgs/alpha/servers/world/lists/whitelist/Steve", ""); code != http.StatusConflict || msg != "saved_list" {
		t.Errorf("taking a list's player off the server: %d %s", code, msg)
	}

	// The list is changed: the server follows.
	ran = g.since()
	sendList(t, p, "admin", "POST", "/orgs/alpha/player-lists/"+club.ID+"/players", `{"names":["Notch"]}`)
	waitFor(t, "a player added to the list is put on", func() bool { return ran() == "whitelist add Notch" })
	ran = g.since()
	sendList(t, p, "admin", "DELETE", "/orgs/alpha/player-lists/"+club.ID+"/players/"+m.known["steve"], "")
	waitFor(t, "a player taken off the list is taken off", func() bool { return ran() == "whitelist remove Steve" })

	// Used by both servers, which the list says.
	var lists []playerListView
	p.fetch("/orgs/alpha/player-lists", &lists)
	if len(lists) != 2 || len(lists[0].Servers) != 2 || lists[0].Servers[0].Name != "lobby" || !lists[0].Servers[1].Whitelist || lists[0].Servers[1].Ops {
		t.Errorf("servers using each: %+v", lists)
	}

	// Deleted: dropped from both servers, and its players taken off.
	ran = g.since()
	if code, _ := p.do("admin", "DELETE", "/orgs/alpha/player-lists/"+club.ID, ""); code != http.StatusNoContent {
		t.Fatalf("deleting: %d", code)
	}
	if got := world(t, p).Spec; got.WhitelistLists != nil || !slices.Equal(got.OpLists, []string{staff.ID}) {
		t.Errorf("lists after one was deleted: %v %v", got.WhitelistLists, got.OpLists)
	}
	lobby := &avsysv1.MinecraftServer{}
	if err := p.srv.Client.Get(ctx, client.ObjectKey{Namespace: "mc-alpha", Name: "lobby"}, lobby); err != nil || lobby.Spec.WhitelistLists != nil {
		t.Errorf("lobby's lists after the list was deleted: %v %v", lobby.Spec.WhitelistLists, err)
	}
	waitFor(t, "a deleted list's players are taken off", func() bool { return ran() == "whitelist remove Alex; whitelist remove Notch" })
}

// Saved lists go with a server copied, and with a template, as the rest of
// its whitelist does; one that is not there any more, or is another
// organization's, is dropped.
func TestSavedListsCarried(t *testing.T) {
	p := newPanel(t)
	p.srv.catalog.http.Transport = newMojangAccounts()
	_, club := sendList(t, p, "admin", "POST", "/orgs/alpha/player-lists", `{"name":"Club"}`)
	_, gone := sendList(t, p, "admin", "POST", "/orgs/alpha/player-lists", `{"name":"Gone"}`)
	_, theirs := sendList(t, p, "outsider", "POST", "/orgs/beta/player-lists", `{"name":"Theirs"}`)
	if code, _ := p.do("admin", "DELETE", "/orgs/alpha/player-lists/"+gone.ID, ""); code != http.StatusNoContent {
		t.Fatal("deleting")
	}
	lists := `"whitelistLists":["` + club.ID + `","` + gone.ID + `","` + theirs.ID + `"],"opLists":["` + club.ID + `"]`

	// A new server.
	if code, msg := p.do("admin", "POST", "/orgs/alpha/servers", `{"name":"event","spec":{"eula":true,"version":"1.21.4",`+lists+`}}`); code != http.StatusCreated {
		t.Fatalf("made: %d %s", code, msg)
	}
	mc := &avsysv1.MinecraftServer{}
	if err := p.srv.Client.Get(context.Background(), client.ObjectKey{Namespace: "mc-alpha", Name: "event"}, mc); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(mc.Spec.WhitelistLists, []string{club.ID}) || !slices.Equal(mc.Spec.OpLists, []string{club.ID}) {
		t.Errorf("a new server's lists: %v %v", mc.Spec.WhitelistLists, mc.Spec.OpLists)
	}

	// A template of settings, and so every server made from it.
	code, made := p.get("admin", "POST", "/orgs/alpha/templates", `{"name":"Event","spec":{"loader":"paper","version":"1.21.4","resources":{"memory":"4Gi"},"storage":{"size":"10Gi"},`+lists+`}}`)
	if code != http.StatusCreated {
		t.Fatalf("template: %d %v", code, made)
	}
	spec, _ := made["spec"].(map[string]any)
	if got, _ := json.Marshal([]any{spec["whitelistLists"], spec["opLists"]}); string(got) != `[["`+club.ID+`"],["`+club.ID+`"]]` {
		t.Errorf("a template's lists: %s", got)
	}

	// A copy of what a snapshot recorded takes them unless asked otherwise.
	recorded := &avsysv1.MinecraftServerSpec{WhitelistLists: []string{club.ID}, OpLists: []string{club.ID}}
	if got := fromSnapshot(&avsysv1.MinecraftServerSpec{}, recorded, nil); !slices.Equal(got.WhitelistLists, recorded.WhitelistLists) || !slices.Equal(got.OpLists, recorded.OpLists) {
		t.Errorf("a copy's lists: %v %v", got.WhitelistLists, got.OpLists)
	}
	asked, named := &avsysv1.MinecraftServerSpec{}, map[string]json.RawMessage{"whitelistLists": json.RawMessage(`[]`)}
	if got := fromSnapshot(asked, recorded, named); len(got.WhitelistLists) != 0 || !slices.Equal(got.OpLists, recorded.OpLists) {
		t.Errorf("a copy asked for none: %v %v", got.WhitelistLists, got.OpLists)
	}
}

// "Save as list" starts a list with who is on a server's whitelist now: in
// game, by UUID where the game has one, and who the panel puts there.
func TestSaveServerListAsSavedList(t *testing.T) {
	p := newPanel(t)
	g := runningWithLists(t, p)
	m := newMojangAccounts("Steve", "Ada", "Olive", "Dinnerbone")
	p.srv.catalog.http.Transport = m
	// Steve is on in game; Dinnerbone is named in the settings, and Ada
	// and Olive on for their role, not put on yet.
	g.whitelist = []string{"Steve"}
	changeWorld(t, p, func(mc *avsysv1.MinecraftServer) {
		mc.Spec.AutoWhitelist = "admin"
		mc.Spec.Whitelist = []string{"Dinnerbone"}
	})
	code, made := sendList(t, p, "admin", "POST", "/orgs/alpha/player-lists", `{"name":"From world","fromServer":"world","list":"whitelist"}`)
	if code != http.StatusCreated || made.names() != "Ada Dinnerbone Olive Steve" {
		t.Errorf("saved as a list: %d %s %v", code, made.names(), made.Missing)
	}
	if code, _ := call(p, "admin", "POST", "/orgs/alpha/player-lists", `{"name":"Nope","fromServer":"nobody","list":"whitelist"}`); code != http.StatusNotFound {
		t.Errorf("from a server that is not there: %d", code)
	}
	if code, _ := call(p, "admin", "POST", "/orgs/alpha/player-lists", `{"name":"Nope","fromServer":"world","list":"bans"}`); code != http.StatusBadRequest {
		t.Errorf("from a list that is not one: %d", code)
	}
}

// A player's head is the face of their skin with its outer layer on top,
// made by the panel from the skin Mojang has; one with no skin of their own
// has none.
func TestPlayerHead(t *testing.T) {
	p := newPanel(t)
	m := newMojangAccounts()
	p.srv.catalog.http.Transport = m
	skin := image.NewNRGBA(image.Rect(0, 0, 64, 64))
	for y := 8; y < 16; y++ {
		for x := 8; x < 16; x++ {
			skin.Set(x, y, color.NRGBA{R: 200, A: 255})
		}
	}
	// The outer layer covers one pixel of the face.
	skin.Set(40, 8, color.NRGBA{B: 200, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, skin); err != nil {
		t.Fatal(err)
	}
	const uuid = "069a79f444e94726a5befca90e38aaf5"
	textures := base64.StdEncoding.EncodeToString([]byte(`{"textures":{"SKIN":{"url":"http://textures.minecraft.net/texture/abc"}}}`))
	m.files["https://sessionserver.mojang.com/session/minecraft/profile/"+uuid] = []byte(`{"id":"` + uuid + `","name":"Notch","properties":[{"name":"textures","value":"` + textures + `"}]}`)
	m.files["https://textures.minecraft.net/texture/abc"] = buf.Bytes()
	m.files["https://sessionserver.mojang.com/session/minecraft/profile/"+strings.Repeat("b", 32)] = []byte(`{"id":"bbb","name":"Plain","properties":[]}`)

	rec := p.send("viewer", "GET", "/players/069a79f4-44e9-4726-a5be-fca90e38aaf5/head", nil)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("head: %d %s", rec.Code, rec.Body.String())
	}
	face, err := png.Decode(rec.Body)
	if err != nil {
		t.Fatal(err)
	}
	if b := face.Bounds(); b.Dx() != 8 || b.Dy() != 8 {
		t.Errorf("size: %v", b)
	}
	if r, _, b, _ := face.At(0, 0).RGBA(); b>>8 != 200 || r != 0 {
		t.Errorf("the outer layer is not on top: %v", face.At(0, 0))
	}
	if r, _, _, _ := face.At(1, 1).RGBA(); r>>8 != 200 {
		t.Errorf("the face: %v", face.At(1, 1))
	}
	for _, who := range []string{strings.Repeat("b", 32), "not-a-uuid", strings.Repeat("c", 32)} {
		if rec := p.send("viewer", "GET", "/players/"+who+"/head", nil); rec.Code != http.StatusNotFound {
			t.Errorf("%s: %d", who, rec.Code)
		}
	}
}

// A server takes up to a thousand players from saved lists for each of its
// lists, the most a list has: chosen, or added to a list it uses.
func TestSavedListsBounded(t *testing.T) {
	p := newPanel(t)
	ctx := context.Background()
	p.srv.catalog.http.Transport = newMojangAccounts("Steve")
	org, err := p.srv.Store.OrgBySlug(ctx, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	made := func(name string, from, n int) string {
		l := &store.PlayerList{OrgID: org.ID, Name: name}
		for i := from; i < from+n; i++ {
			l.Players = append(l.Players, store.ListPlayer{UUID: fmt.Sprintf("%032d", i), Name: fmt.Sprintf("P%d", i)})
		}
		if err := p.srv.Store.CreatePlayerList(ctx, l); err != nil {
			t.Fatal(err)
		}
		return l.ID
	}
	big, other, full := made("Big", 0, 600), made("Other", 300, 600), made("Full", 1000, 1000)
	// 900 between them, those on both counted once.
	if code, msg := p.do("admin", "PATCH", "/orgs/alpha/servers/world", `{"spec":{"whitelistLists":["`+big+`","`+other+`"]}}`); code != http.StatusOK {
		t.Fatalf("900 players: %d %s", code, msg)
	}
	if code, _ := p.do("admin", "PATCH", "/orgs/alpha/servers/world", `{"spec":{"opLists":["`+big+`","`+full+`"]}}`); code != http.StatusBadRequest {
		t.Errorf("1600 operators: %d", code)
	}
	if code, _ := p.do("admin", "PATCH", "/orgs/alpha/servers/world", `{"spec":{"opLists":["`+full+`"]}}`); code != http.StatusOK {
		t.Errorf("a full list alone: %d", code)
	}
	// Full, it takes nobody more; a list with room does.
	if code, text := call(p, "admin", "POST", "/orgs/alpha/player-lists/"+full+"/players", `{"names":["Steve"]}`); code != http.StatusBadRequest {
		t.Errorf("adding to a full one: %d %s", code, text)
	}
	if code, text := call(p, "admin", "POST", "/orgs/alpha/player-lists/"+big+"/players", `{"names":["Steve"]}`); code != http.StatusOK {
		t.Errorf("adding where there is room: %d %s", code, text)
	}
}

// A deleted list is taken out of a server as the server is now: lists
// picked for it since the panel last looked are kept.
func TestDeletedListKeepsOthersPickedMeanwhile(t *testing.T) {
	p := newPanel(t)
	stale := world(t, p).DeepCopy()
	stale.Spec.WhitelistLists = []string{"a"}
	changeWorld(t, p, func(mc *avsysv1.MinecraftServer) { mc.Spec.WhitelistLists = []string{"a", "b"} })
	if err := p.srv.dropSavedListFrom(context.Background(), stale, "a"); err != nil {
		t.Fatal(err)
	}
	if got := world(t, p).Spec.WhitelistLists; !slices.Equal(got, []string{"b"}) {
		t.Errorf("lists after a was deleted: %v, want b kept", got)
	}
}

// Players on saved lists are looked up for a new name a few at a time, and
// each not again for hours.
func TestListNamesRefreshedAFewAtATime(t *testing.T) {
	p := newPanel(t)
	ctx := context.Background()
	m := newMojangAccounts()
	p.srv.catalog.http.Transport = m
	org, err := p.srv.Store.OrgBySlug(ctx, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	l := &store.PlayerList{OrgID: org.ID, Name: "Club"}
	for _, n := range []string{"Ann", "Bob", "Cat"} {
		uuid := strings.Repeat(strings.ToLower(n[:1]), 32)
		l.Players = append(l.Players, store.ListPlayer{UUID: uuid, Name: n})
		m.files["https://sessionserver.mojang.com/session/minecraft/profile/"+uuid] = []byte(`{"id":"` + uuid + `","name":"` + n + `2"}`)
	}
	if err := p.srv.Store.CreatePlayerList(ctx, l); err != nil {
		t.Fatal(err)
	}
	names := func() string {
		got, _ := p.srv.Store.PlayerList(ctx, org.ID, l.ID)
		var out []string
		for _, p := range got.Players {
			out = append(out, p.Name)
		}
		return strings.Join(out, " ")
	}
	budget := 2
	p.srv.refreshListNames(ctx, org.ID, &budget)
	if got := names(); got != "Ann2 Bob2 Cat" || budget != 0 {
		t.Errorf("after one look: %s, %d left", got, budget)
	}
	budget = 2
	p.srv.refreshListNames(ctx, org.ID, &budget)
	if got := names(); got != "Ann2 Bob2 Cat2" || budget != 1 {
		t.Errorf("after two: %s, %d left, want only Cat asked", got, budget)
	}
}
