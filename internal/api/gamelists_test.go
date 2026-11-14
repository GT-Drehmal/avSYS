package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/pkg/sftp"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubefake "k8s.io/client-go/kubernetes/fake"
	"sigs.k8s.io/controller-runtime/pkg/client"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
	"github.com/GT-Drehmal/avSYS/internal/store"
)

// transcriptLine is one command in testdata/lists and what the game said.
var transcriptLine = regexp.MustCompile(`^(.*?) => (".*") err=<nil>$`)

// Each kind of server was given the same commands over RCON, as the panel
// gives them, and what it answered recorded in testdata/lists. Played back,
// every answer to "whitelist list" says who the commands before it left on
// the whitelist, the game's files at the end say the same, and only a name
// the game knows no player of is taken for one.
func TestWhitelistAnswersOfEachKind(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("testdata", "lists", "*.txt"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no transcripts: %v", err)
	}
	unknown := []string{"Nobody_here_1", "Zq9xk2vw8j1qz"}
	for _, file := range files {
		game := strings.TrimSuffix(filepath.Base(file), ".txt")
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		transcript, files, _ := strings.Cut(string(data), "-- whitelist.json\n")
		whitelistFile, opsFile, _ := strings.Cut(files, "-- ops.json\n")
		var whitelisted, ops []string
		lists := 0
		for _, line := range strings.Split(transcript, "\n") {
			m := transcriptLine.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			command := m[1]
			out, err := strconv.Unquote(m[2])
			if err != nil {
				t.Fatalf("%s: %q: %v", game, line, err)
			}
			if command == "whitelist list" {
				lists++
				names, ok := parseWhitelist(out)
				if !ok || !sameNames(names, whitelisted) || len(names) != len(whitelisted) {
					t.Errorf("%s: %q read as %v, %v; want %v", game, out, names, ok, whitelisted)
				}
				continue
			}
			verb, name := splitCommand(command)
			missing := slices.Contains(unknown, name)
			if got := noSuchPlayer.MatchString(plain(out)); got != missing {
				t.Errorf("%s: %s answered %q, taken for no such player: %v", game, command, out, got)
			}
			if missing {
				continue
			}
			switch verb {
			case "whitelist add":
				whitelisted = append(whitelisted, name)
			case "whitelist remove":
				whitelisted = slices.DeleteFunc(whitelisted, func(n string) bool { return n == name })
			case "op":
				ops = append(ops, name)
			case "deop":
				ops = slices.DeleteFunc(ops, func(n string) bool { return n == name })
			}
		}
		if lists < 5 {
			t.Errorf("%s: %d answers to whitelist list", game, lists)
		}
		sc := memFiles(t)
		for name, text := range map[string]string{"/whitelist.json": whitelistFile, "/ops.json": opsFile} {
			put(t, sc, name, text)
		}
		for name, want := range map[string][]string{"/whitelist.json": whitelisted, "/ops.json": ops} {
			players, err := listFile(sc, name)
			if err != nil || !sameNames(playerNames(players), want) || len(players) != len(want) {
				t.Errorf("%s: %s read as %v, %v; want %v", game, name, players, err, want)
			}
			for _, p := range players {
				if !playerUUIDPattern.MatchString(p.UUID) {
					t.Errorf("%s: %s has %s without a UUID: %+v", game, name, p.Name, p)
				}
			}
		}
	}
}

// splitCommand is a command to a list, as "whitelist add", and the player.
func splitCommand(command string) (verb, name string) {
	i := strings.LastIndex(command, " ")
	return command[:i], command[i+1:]
}

// What is not an answer to "whitelist list" is not read as one.
func TestWhitelistAnswerNotTakenForAnother(t *testing.T) {
	for _, out := range []string{
		"",
		"Unknown or incomplete command, see below for error",
		"There are 3 whitelisted player(s): Ada, Otto",
		"There are 1 whitelisted player(s): @a",
		"There are 2 of a max of 20 players online: Ada, Otto",
	} {
		if names, ok := parseWhitelist(out); ok {
			t.Errorf("%q read as %v", out, names)
		}
	}
	// The game's colors, as Paper may send them, are not part of a name.
	if names, ok := parseWhitelist("§eThere are 2 whitelisted player(s): §fAda§r, Otto"); !ok || strings.Join(names, " ") != "Ada Otto" {
		t.Errorf("with colors: %v, %v", names, ok)
	}
}

// A list the game keeps that is not there is nobody; one that is not a list
// is not taken for one; a name that could not go in a command is left out.
func TestListFile(t *testing.T) {
	sc := memFiles(t)
	if got, err := listFile(sc, "/whitelist.json"); err != nil || got == nil || len(got) != 0 {
		t.Errorf("no file: %v, %v", got, err)
	}
	put(t, sc, "/whitelist.json", "")
	if got, err := listFile(sc, "/whitelist.json"); err != nil || len(got) != 0 {
		t.Errorf("an empty file: %v, %v", got, err)
	}
	put(t, sc, "/whitelist.json", "not json")
	if _, err := listFile(sc, "/whitelist.json"); err == nil {
		t.Error("a file that is no list was read as one")
	}
	put(t, sc, "/whitelist.json", `[{"uuid":"069a79f4-44e9-4726-a5be-fca90e38aaf5","name":"Notch"},{"uuid":"x","name":"@a"},{"name":".BedrockGuy"}]`)
	if got, err := listFile(sc, "/whitelist.json"); err != nil || strings.Join(playerNames(got), " ") != "Notch .BedrockGuy" || got[0].UUID != "069a79f4-44e9-4726-a5be-fca90e38aaf5" {
		t.Errorf("a list: %+v, %v", got, err)
	}
}

// A player on a list in game is the panel's when it noted them, when they
// are there for being a member, or when the server's settings name them, by
// name or UUID. Anyone else was added some other way.
func TestAddedInGame(t *testing.T) {
	on := []listedPlayer{
		{Name: "steve"}, {Name: "Ada"}, {Name: "Otto"}, {Name: "Vera"},
		{Name: "Notch", UUID: "069a79f4-44e9-4726-a5be-fca90e38aaf5"}, {Name: "Alex"},
	}
	got := addedInGame(on, []string{"ada"}, []string{"Otto"}, []string{"VERA", "069a79f444e94726a5befca90e38aaf5"})
	if names := strings.Join(playerNames(got), " "); names != "Alex steve" {
		t.Errorf("added in game: %s, want Alex steve", names)
	}
}

// With who is on a list in game known, a player the panel should have on it
// who is not is put back, one it put there who is gone already needs no
// command, and one added by hand is never taken off.
func TestListChangesAgainstTheGame(t *testing.T) {
	for name, c := range map[string]struct {
		made, wanted, on []string
		add, remove      []string
	}{
		"in step":                           {[]string{"Ada"}, []string{"Ada"}, []string{"Ada", "Steve"}, nil, nil},
		"taken off by hand, put back":       {[]string{"Ada"}, []string{"Ada"}, []string{"Steve"}, []string{"Ada"}, nil},
		"on by hand and wanted, no command": {nil, []string{"Ada"}, []string{"ada"}, nil, nil},
		"gone already, no command":          {[]string{"Ada", "Otto"}, []string{"Ada"}, []string{"Ada"}, nil, nil},
		"one it put there, taken off":       {[]string{"Ada", "Otto"}, []string{"Ada"}, []string{"Ada", "Otto"}, nil, []string{"Otto"}},
		"added by hand, left":               {nil, nil, []string{"Steve"}, nil, nil},
		"switched off":                      {[]string{"Ada"}, nil, []string{"Ada", "Steve"}, nil, []string{"Ada"}},
	} {
		add, remove := listChanges(c.made, c.wanted, c.on, true)
		if !slices.Equal(add, c.add) || !slices.Equal(remove, c.remove) {
			t.Errorf("%s: add %v remove %v, want add %v remove %v", name, add, remove, c.add, c.remove)
		}
	}
	// The note, as it is true of the game: those it put there or should
	// have who are on it.
	if got := stillOn([]string{"Ada", "Gone", "Otto"}, []string{"Ada", "Vera"}, []string{"Otto", "Vera", "Steve", "ada"}); strings.Join(got, " ") != "Otto Vera ada" {
		t.Errorf("still on: %v", got)
	}
}

// listGame is a running server's game, as far as its lists go: it answers
// the commands for them as the game does (testdata/lists/vanilla-26.3.txt),
// and keeps its operators in ops.json, as the game does.
type listGame struct {
	t         *testing.T
	sc        *sftp.Client
	mu        sync.Mutex
	whitelist []string
	ran       []string
	// unknown are names the game knows no player of.
	unknown []string
	// console is the way to it the panel dials.
	console *rconGame
}

func (g *listGame) answer(command string) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	if command == "whitelist list" {
		if len(g.whitelist) == 0 {
			return "There are no whitelisted players"
		}
		return fmt.Sprintf("There are %d whitelisted player(s): %s", len(g.whitelist), strings.Join(g.whitelist, ", "))
	}
	g.ran = append(g.ran, command)
	verb, name := splitCommand(command)
	if slices.Contains(g.unknown, name) {
		return "That player does not exist"
	}
	switch verb {
	case "whitelist add":
		if !hasName(g.whitelist, name) {
			g.whitelist = append(g.whitelist, name)
		}
		return "Added " + name + " to the whitelist"
	case "whitelist remove":
		g.whitelist = slices.DeleteFunc(g.whitelist, func(n string) bool { return strings.EqualFold(n, name) })
		return "Removed " + name + " from the whitelist"
	case "op":
		g.setOps(append(g.ops(), name))
		return "Made " + name + " a server operator"
	case "deop":
		g.setOps(slices.DeleteFunc(g.ops(), func(n string) bool { return strings.EqualFold(n, name) }))
		return "Made " + name + " no longer a server operator"
	}
	return "Unknown or incomplete command, see below for error"
}

func (g *listGame) ops() []string {
	players, err := listFile(g.sc, dataRoot+"/ops.json")
	if err != nil {
		g.t.Error(err)
	}
	return playerNames(players)
}

func (g *listGame) setOps(names []string) {
	entries := []map[string]any{}
	for _, n := range names {
		entries = append(entries, map[string]any{"uuid": "00000000-0000-0000-0000-000000000000", "name": n, "level": 4, "bypassesPlayerLimit": false})
	}
	if err := writeListFile(g.sc, dataRoot+"/ops.json", entries); err != nil {
		g.t.Error(err)
	}
}

// commands is what was run on the game since it was last asked, but for
// "whitelist list".
func (g *listGame) commands() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := strings.Join(g.ran, "; ")
	g.ran = nil
	return out
}

// runningWithLists makes the test panel's server "world" a running one
// whose game is g, with Ada (admin) and Olive (owner) linked.
func runningWithLists(t *testing.T, p *panel) *listGame {
	t.Helper()
	ctx := context.Background()
	g := &listGame{t: t, sc: filesFrom(t, p, sftp.InMemHandler())}
	p.srv.Kube = kubefake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: "mc-alpha", Name: "world-rcon"},
		Data:       map[string][]byte{"password": []byte("secret")},
	})
	g.console = &rconGame{password: "secret", answer: g.answer}
	p.srv.Dialer = g.console
	for who, name := range map[string]string{"admin": "Ada", "owner": "Olive"} {
		if err := p.srv.Store.SetMinecraftAccount(ctx, p.users[who].ID, &store.MinecraftAccount{UUID: strings.Repeat(strconv.Itoa(len(name)), 32), Name: name}); err != nil {
			t.Fatal(err)
		}
	}
	p.setPhase("alpha", "world", avsysv1.PhaseRunning)
	return g
}

func world(t *testing.T, p *panel) *avsysv1.MinecraftServer {
	t.Helper()
	mc := &avsysv1.MinecraftServer{}
	if err := p.srv.Client.Get(context.Background(), client.ObjectKey{Namespace: "mc-alpha", Name: "world"}, mc); err != nil {
		t.Fatal(err)
	}
	return mc
}

func changeWorld(t *testing.T, p *panel, change func(*avsysv1.MinecraftServer)) {
	t.Helper()
	mc := world(t, p)
	change(mc)
	if err := p.srv.Client.Update(context.Background(), mc); err != nil {
		t.Fatal(err)
	}
}

// gameLists is what Settings is told of world's lists: who is on each other
// than by the panel, or "?" for one that could not be read.
func gameLists(t *testing.T, p *panel) map[string]string {
	t.Helper()
	code, body := call(p, "admin", "GET", "/orgs/alpha/servers/world/lists", "")
	if code != http.StatusOK {
		t.Fatalf("lists: %d %s", code, body)
	}
	var got map[string]gameListView
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for key, view := range got {
		out[key] = strings.Join(playerNames(view.InGame), " ")
		if !view.Known {
			out[key] = "?"
		}
	}
	return out
}

// call is p.do with the whole body of the answer.
func call(p *panel, who, method, path, body string) (int, string) {
	p.t.Helper()
	req := httptest.NewRequest(method, "/api/v1"+path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+p.tokens[who])
	rec := httptest.NewRecorder()
	p.h.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// A running server's lists are kept as the panel has them against what the
// game says: a member taken off by hand is put back, a player added by hand
// is left and shown, and the note of who the panel put there is kept true.
func TestListsFollowTheGame(t *testing.T) {
	p := newPanel(t)
	ctx := context.Background()
	g := runningWithLists(t, p)
	// Steve was whitelisted and Herobrine made an operator at the console.
	g.whitelist = []string{"Steve"}
	g.setOps([]string{"Herobrine"})
	changeWorld(t, p, func(mc *avsysv1.MinecraftServer) { mc.Spec.AutoWhitelist, mc.Spec.AutoOp = "admin", "owner" })
	look := func() string {
		t.Helper()
		if err := p.srv.syncOps(ctx, world(t, p), nil); err != nil {
			t.Fatal(err)
		}
		return g.commands()
	}
	notes := func() string {
		a := world(t, p).Annotations
		return a[avsysv1.AnnotationAutoWhitelist] + " | " + a[avsysv1.AnnotationAutoOps]
	}

	if got := look(); got != "op Olive; whitelist add Ada; whitelist add Olive" {
		t.Errorf("members put on: %s", got)
	}
	if got := notes(); got != "Ada,Olive | Olive" {
		t.Errorf("notes: %s", got)
	}
	if got := look(); got != "" {
		t.Errorf("in step, and still: %s", got)
	}
	if got := gameLists(t, p); got["whitelist"] != "Steve" || got["ops"] != "Herobrine" {
		t.Errorf("added in game: %v, want Steve and Herobrine", got)
	}

	// Ada is taken off the whitelist at the console, and Olive deopped.
	g.answer("whitelist remove Ada")
	g.answer("deop Olive")
	g.commands()
	if got := look(); got != "op Olive; whitelist add Ada" {
		t.Errorf("members taken off by hand: %s, want both put back", got)
	}
	if got := notes(); got != "Ada,Olive | Olive" {
		t.Errorf("notes after putting back: %s", got)
	}

	// The note says the panel put on someone who is gone already, and who
	// is no longer wanted: nothing is run, and the note is put right.
	changeWorld(t, p, func(mc *avsysv1.MinecraftServer) { mc.Annotations[avsysv1.AnnotationAutoWhitelist] = "Ada,Gone,Olive" })
	if got := look(); got != "" {
		t.Errorf("one noted who is gone already: %s", got)
	}
	if got := notes(); got != "Ada,Olive | Olive" {
		t.Errorf("notes after one was found gone: %s", got)
	}

	// Steve is kept in the server's settings: he is on already, so nothing
	// is run, and he is the panel's from then on.
	changeWorld(t, p, func(mc *avsysv1.MinecraftServer) { mc.Spec.Whitelist = []string{"Steve"} })
	if got := look(); got != "" {
		t.Errorf("kept, being on already: %s", got)
	}
	if got := gameLists(t, p); got["whitelist"] != "" {
		t.Errorf("added in game after Steve was kept: %v", got)
	}
	changeWorld(t, p, func(mc *avsysv1.MinecraftServer) { mc.Spec.Whitelist = nil })
	if got := look(); got != "whitelist remove Steve" {
		t.Errorf("taken out of the settings: %s", got)
	}

	// A member is kept off by their pick: taken off, and not put back.
	changeWorld(t, p, func(mc *avsysv1.MinecraftServer) {
		mc.Spec.WhitelistMembers = &avsysv1.MemberPicks{Except: []string{p.users["admin"].ID}}
	})
	if got := look(); got != "whitelist remove Ada" {
		t.Errorf("a member left out: %s", got)
	}
	if got := look(); got != "" {
		t.Errorf("a member left out is not put back: %s", got)
	}

	// A name the game knows no player of is tried once, and not noted.
	changeWorld(t, p, func(mc *avsysv1.MinecraftServer) { mc.Spec.Whitelist = []string{"Zq9xk2vw8j1qz"} })
	g.unknown = []string{"Zq9xk2vw8j1qz"}
	if got := look(); got != "whitelist add Zq9xk2vw8j1qz" {
		t.Errorf("a name nobody has: %s", got)
	}
	if got := look(); got != "" {
		t.Errorf("a name nobody has, again: %s", got)
	}
	if got := notes(); got != "Olive | Olive" {
		t.Errorf("notes after a name nobody has: %s", got)
	}

	// Herobrine, made an operator by hand, is taken off from Settings, by
	// the game's own command. Only an admin can.
	if code, _ := p.do("operator", "DELETE", "/orgs/alpha/servers/world/lists/ops/Herobrine", ""); code != http.StatusForbidden {
		t.Errorf("an operator taking a player off: %d", code)
	}
	if code, body := call(p, "admin", "DELETE", "/orgs/alpha/servers/world/lists/ops/herobrine", ""); code != http.StatusNoContent {
		t.Errorf("taking Herobrine off: %d %s", code, body)
	}
	if got := g.commands(); got != "deop Herobrine" {
		t.Errorf("commands to take Herobrine off: %s", got)
	}
	if got := gameLists(t, p); got["ops"] != "" {
		t.Errorf("operators added in game after taking Herobrine off: %v", got)
	}
	// One there for being a member is not taken off here.
	if code, errCode := p.do("admin", "DELETE", "/orgs/alpha/servers/world/lists/whitelist/Olive", ""); code != http.StatusConflict || errCode != "member" {
		t.Errorf("taking a member's player off: %d %s", code, errCode)
	}
	if got := g.commands(); got != "" {
		t.Errorf("commands run for a member's player: %s", got)
	}
	// All of it over one connection: the game logs each one made to it.
	if n := g.console.connections(); n != 1 {
		t.Errorf("%d connections to the game, want one", n)
	}
}

// A stopped server's lists are read from its files, and a player added in
// game taken off by changing them, every other entry left as it was.
func TestListsOfAStoppedServer(t *testing.T) {
	p := newPanel(t)
	ctx := context.Background()
	sc := filesFrom(t, p, sftp.InMemHandler())
	if err := p.srv.Store.SetMinecraftAccount(ctx, p.users["admin"].ID, &store.MinecraftAccount{UUID: strings.Repeat("a", 32), Name: "Ada"}); err != nil {
		t.Fatal(err)
	}
	notch := "069a79f4-44e9-4726-a5be-fca90e38aaf5"
	changeWorld(t, p, func(mc *avsysv1.MinecraftServer) {
		mc.Spec.AutoWhitelist = "admin"
		// Kept by UUID, which the file has beside the name.
		mc.Spec.Whitelist = []string{notch}
		if mc.Annotations == nil {
			mc.Annotations = map[string]string{}
		}
		mc.Annotations[avsysv1.AnnotationAutoWhitelist] = "Ada"
	})
	whitelist := `[
  {"uuid": "` + notch + `", "name": "Notch"},
  {"uuid": "11111111-1111-1111-1111-111111111111", "name": "Steve"},
  {"uuid": "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", "name": "Ada"}
]`
	put(t, sc, dataRoot+"/whitelist.json", whitelist)
	put(t, sc, dataRoot+"/ops.json", `[{"uuid": "22222222-2222-2222-2222-222222222222", "name": "Herobrine", "level": 4, "bypassesPlayerLimit": false}]`)

	if got := gameLists(t, p); got["whitelist"] != "Steve" || got["ops"] != "Herobrine" {
		t.Errorf("added in game: %v, want Steve and Herobrine", got)
	}
	for _, c := range []struct {
		path string
		code int
	}{
		{"whitelist/Ada", http.StatusConflict},
		{"whitelist/Notch", http.StatusConflict},
		{"whitelist/Nobody", http.StatusNotFound},
		{"whitelist/@a", http.StatusBadRequest},
		{"bans/Steve", http.StatusNotFound},
	} {
		if code, body := call(p, "admin", "DELETE", "/orgs/alpha/servers/world/lists/"+c.path, ""); code != c.code {
			t.Errorf("taking %s off: %d %s, want %d", c.path, code, body, c.code)
		}
	}
	if code, body := call(p, "admin", "DELETE", "/orgs/alpha/servers/world/lists/whitelist/Steve", ""); code != http.StatusNoContent {
		t.Fatalf("taking Steve off: %d %s", code, body)
	}
	var left []map[string]any
	f, err := sc.Open(dataRoot + "/whitelist.json")
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(f)
	f.Close()
	if err := json.Unmarshal(data, &left); err != nil {
		t.Fatal(err)
	}
	if len(left) != 2 || left[0]["name"] != "Notch" || left[0]["uuid"] != notch || left[1]["name"] != "Ada" {
		t.Errorf("whitelist.json after taking Steve off: %s", data)
	}
	if code, body := call(p, "admin", "DELETE", "/orgs/alpha/servers/world/lists/ops/Herobrine", ""); code != http.StatusNoContent {
		t.Fatalf("taking Herobrine off: %d %s", code, body)
	}
	if got := gameLists(t, p); got["whitelist"] != "" || got["ops"] != "" {
		t.Errorf("added in game after both were taken off: %v", got)
	}

	// Neither running nor stopped: its files may be about to change.
	p.setPhase("alpha", "world", avsysv1.PhaseStarting)
	put(t, sc, dataRoot+"/ops.json", `[{"name": "Herobrine"}]`)
	if code, errCode := p.do("admin", "DELETE", "/orgs/alpha/servers/world/lists/ops/Herobrine", ""); code != http.StatusConflict || errCode != "busy" {
		t.Errorf("taking a player off a starting server: %d %s", code, errCode)
	}
}
