package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/pkg/sftp"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
	"github.com/GT-Drehmal/avSYS/internal/store"
)

// Two Bedrock players GeyserMC knows, as it answered for them, and the UUIDs
// Floodgate gave them on a real server (testdata/bedrock).
const (
	timXUID = "2535432196048835"
	timUUID = "00000000-0000-0000-0009-01f64f65c7c3"
	redXUID = "2535407489632336"
	redUUID = "00000000-0000-0000-0009-01f08ec7c850"
)

// geyserAccounts answers GeyserMC's lookups as it does: the XUID of a
// gamertag it knows, whatever its case; 503 "not in our cache" for one it
// does not; the gamertag of an XUID. down makes it fail as an outage would.
// Everything else is Mojang's.
type geyserAccounts struct {
	*mojangAccounts
	gmu   sync.Mutex
	xuids map[string]string // by lowercase gamertag
	tags  map[string]string // by XUID
	asked []string
	down  bool
}

func newGeyserAccounts(m *mojangAccounts, xuids map[string]string) *geyserAccounts {
	g := &geyserAccounts{mojangAccounts: m, xuids: map[string]string{}, tags: map[string]string{}}
	for tag, xuid := range xuids {
		g.xuids[strings.ToLower(tag)] = xuid
		g.tags[xuid] = tag
	}
	return g
}

func (g *geyserAccounts) RoundTrip(r *http.Request) (*http.Response, error) {
	rest, ok := strings.CutPrefix(r.URL.String(), geyserAPI+"xbox/")
	if !ok {
		return g.mojangAccounts.RoundTrip(r)
	}
	g.gmu.Lock()
	defer g.gmu.Unlock()
	g.asked = append(g.asked, rest)
	if g.down {
		return nil, errors.New("offline")
	}
	answer := func(status int, body string) (*http.Response, error) {
		return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	}
	notCached := `{"message":"Unable to find user in our cache. Please try specifying their Floodgate UUID instead"}`
	if tag, ok := strings.CutPrefix(rest, "xuid/"); ok {
		tag, _ = url.PathUnescape(tag)
		if xuid, ok := g.xuids[strings.ToLower(tag)]; ok {
			return answer(http.StatusOK, `{"xuid":`+xuid+`}`)
		}
		return answer(http.StatusServiceUnavailable, notCached)
	}
	if xuid, ok := strings.CutPrefix(rest, "gamertag/"); ok {
		if tag, ok := g.tags[xuid]; ok {
			return answer(http.StatusOK, `{"gamertag":"`+tag+`"}`)
		}
		return answer(http.StatusServiceUnavailable, notCached)
	}
	return answer(http.StatusNotFound, `{"message":"Not Found"}`)
}

func (g *geyserAccounts) lookups() []string {
	g.gmu.Lock()
	defer g.gmu.Unlock()
	out := g.asked
	g.asked = nil
	return out
}

func bedrockAccounts(m *mojangAccounts) *geyserAccounts {
	return newGeyserAccounts(m, map[string]string{"Tim203": timXUID, "Redned235": redXUID})
}

// A Bedrock player's UUID is the one Floodgate gives them: their XUID in the
// low 64 bits, as the game wrote it in whitelist.json.
func TestFloodgateUUID(t *testing.T) {
	for xuid, want := range map[string]string{timXUID: timUUID, redXUID: redUUID} {
		uuid, ok := floodgateUUID(xuid)
		if !ok || dashedUUID(uuid) != want || uuid != bareUUID(want) {
			t.Errorf("%s: %s, %v; want %s", xuid, uuid, ok, want)
		}
		if got := xuidOf(want); got != xuid {
			t.Errorf("XUID of %s: %q, want %s", want, got, xuid)
		}
		if got := xuidOf(uuid); got != xuid {
			t.Errorf("XUID of %s: %q, want %s", uuid, got, xuid)
		}
	}
	for _, bad := range []string{"", "0", "-1", "12a", "18446744073709551616"} {
		if uuid, ok := floodgateUUID(bad); ok {
			t.Errorf("%q made %s", bad, uuid)
		}
	}
	for _, java := range []string{"069a79f4-44e9-4726-a5be-fca90e38aaf5", "00000000-0000-0000-0000-000000000000", "not one", ""} {
		if got := xuidOf(java); got != "" {
			t.Errorf("%q taken for a Bedrock player's, of %s", java, got)
		}
	}
}

// A Bedrock player's name in game is their gamertag behind Floodgate's
// prefix, with spaces as "_", cut to 16 characters, as Floodgate makes it.
// The prefix is read from Floodgate's settings, as a plugin or as a mod.
func TestFloodgateNames(t *testing.T) {
	for tag, want := range map[string]string{"Tim203": ".Tim203", "Big Boss Man": ".Big_Boss_Man", "ABCDEFGHIJKLMNOP": ".ABCDEFGHIJKLMNO"} {
		if got := defaultFloodgate.name(tag); got != want {
			t.Errorf("%q: %q, want %q", tag, got, want)
		}
	}
	// Floodgate set to keep spaces: written as "_" all the same, so the
	// name can go in a command and be read back.
	if got := (floodgate{prefix: "*"}).name("A B"); got != "*A_B" || !listedNamePattern.MatchString(got) {
		t.Errorf("with spaces: %q", got)
	}
	if got := defaultFloodgate.gamertag(".Tim203"); got != "Tim203" {
		t.Errorf("gamertag of .Tim203: %q", got)
	}

	sc := memFiles(t)
	if err := sc.MkdirAll(dataRoot + "/plugins/floodgate"); err != nil {
		t.Fatal(err)
	}
	if err := sc.MkdirAll(dataRoot + "/config/floodgate"); err != nil {
		t.Fatal(err)
	}
	if got := floodgateFrom(sc); got != defaultFloodgate {
		t.Errorf("without Floodgate: %+v", got)
	}
	// The settings a real Floodgate wrote.
	real, err := os.ReadFile(filepath.Join("testdata", "bedrock", "floodgate-config.yml"))
	if err != nil {
		t.Fatal(err)
	}
	put(t, sc, dataRoot+"/plugins/floodgate/config.yml", string(real))
	if got := floodgateFrom(sc); got != defaultFloodgate {
		t.Errorf("Floodgate's own settings: %+v", got)
	}
	if err := sc.Remove(dataRoot + "/plugins/floodgate/config.yml"); err != nil {
		t.Fatal(err)
	}
	put(t, sc, dataRoot+"/config/floodgate/config.yml", strings.Replace(strings.Replace(string(real), `username-prefix: "."`, `username-prefix: "*"`, 1), "replace-spaces: true", "replace-spaces: false", 1))
	if got := floodgateFrom(sc); got != (floodgate{prefix: "*"}) {
		t.Errorf("a mod's settings, changed: %+v", got)
	}
	put(t, sc, dataRoot+"/config/floodgate/config.yml", "username-prefix: [\n")
	if got := floodgateFrom(sc); got != defaultFloodgate {
		t.Errorf("settings that cannot be read: %+v", got)
	}
}

// A real Paper server with Geyser and Floodgate was given these commands
// over RCON (testdata/bedrock). The game cannot look up a Bedrock player by
// name: "whitelist remove" and "op" say there is no such player, though he
// is on the whitelist. Floodgate's "fwhitelist" answers nothing at all, so
// whether it went through cannot be told. whitelist.json written with a
// Bedrock player's UUID, and read again, puts him on.
func TestFloodgateTranscript(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "bedrock", "paper-26.3-floodgate.txt"))
	if err != nil {
		t.Fatal(err)
	}
	transcript, files, _ := strings.Cut(string(data), "-- whitelist.json\n")
	whitelistFile, _, _ := strings.Cut(files, "-- ops.json\n")
	var lists [][]string
	for _, line := range strings.Split(transcript, "\n") {
		m := transcriptLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		command := m[1]
		out, err := strconv.Unquote(m[2])
		if err != nil {
			t.Fatalf("%q: %v", line, err)
		}
		switch {
		case command == "whitelist list":
			names, ok := parseWhitelist(out)
			if !ok {
				t.Errorf("%q not read", out)
			}
			lists = append(lists, names)
		case strings.HasPrefix(command, "fwhitelist "):
			if out != "" || noSuchPlayer.MatchString(out) {
				t.Errorf("%s answered %q", command, out)
			}
		case command == "whitelist reload":
			if out != "Reloaded the whitelist" {
				t.Errorf("reload answered %q", out)
			}
		default:
			if !noSuchPlayer.MatchString(plain(out)) {
				t.Errorf("%s answered %q, not taken for no such player", command, out)
			}
		}
	}
	want := [][]string{{}, {".Tim203"}, {".Tim203", ".Redned235"}, {".Redned235"}, {".Redned235"}}
	if len(lists) != len(want) {
		t.Fatalf("whitelists: %v", lists)
	}
	for i := range want {
		if !slices.Equal(lists[i], want[i]) {
			t.Errorf("whitelist %d: %v, want %v", i, lists[i], want[i])
		}
	}
	sc := memFiles(t)
	put(t, sc, "/whitelist.json", whitelistFile)
	players, err := listFile(sc, "/whitelist.json")
	if err != nil || len(players) != 1 {
		t.Fatalf("whitelist.json: %v %v", players, err)
	}
	if p := markBedrock(players)[0]; p.Name != ".Redned235" || p.UUID != redUUID || !p.Bedrock || xuidOf(p.UUID) != redXUID {
		t.Errorf("whitelist.json read as %+v", p)
	}
}

// Bedrock players are put on a list by gamertag, looked up with GeyserMC,
// each once and kept a while; one GeyserMC does not know is said to be
// missing, and GeyserMC out of reach fails the whole, adding nobody, Java
// players neither. A Java name behind Floodgate's prefix is a gamertag.
func TestBedrockPlayersLookedUp(t *testing.T) {
	p := newPanel(t)
	ctx := context.Background()
	m := newMojangAccounts("Steve", "Alex")
	g := bedrockAccounts(m)
	p.srv.catalog.http.Transport = g

	code, l := sendList(t, p, "admin", "POST", "/orgs/alpha/player-lists", `{"name":"Event","names":["Steve",".redned235"],"bedrock":["tim203","TIM203"," .Tim203 ","Nobody Here","bad!name"]}`)
	if code != http.StatusCreated || l.names() != "Redned235 Steve Tim203" {
		t.Fatalf("made: %d %+v", code, l)
	}
	if len(l.Missing) != 0 || strings.Join(l.MissingBedrock, ",") != "bad!name,Nobody Here" {
		t.Errorf("missing: %v, Bedrock %v", l.Missing, l.MissingBedrock)
	}
	for _, pl := range l.Players {
		want := map[string]store.ListPlayer{
			"Steve":     {UUID: m.known["steve"], Name: "Steve", Kind: store.KindJava},
			"Tim203":    {UUID: bareUUID(timUUID), Name: "Tim203", Kind: store.KindBedrock, XUID: timXUID},
			"Redned235": {UUID: bareUUID(redUUID), Name: "Redned235", Kind: store.KindBedrock, XUID: redXUID},
		}[pl.Name]
		if pl != want {
			t.Errorf("%s: %+v, want %+v", pl.Name, pl, want)
		}
	}
	asked := g.lookups()
	slices.Sort(asked)
	if got := strings.Join(asked, " "); got != "gamertag/"+redXUID+" gamertag/"+timXUID+" xuid/nobody%20here xuid/redned235 xuid/tim203" {
		t.Errorf("asked GeyserMC: %s", got)
	}
	// Asked again: answered from what was kept.
	if code, again := sendList(t, p, "admin", "POST", "/orgs/alpha/player-lists/"+l.ID+"/players", `{"bedrock":["Tim203"]}`); code != http.StatusOK || again.names() != "Redned235 Steve Tim203" {
		t.Errorf("added again: %d %s", code, again.names())
	}
	if got := g.lookups(); len(got) != 0 {
		t.Errorf("asked again: %v", got)
	}

	// GeyserMC is down: nobody is added.
	g.down = true
	if code, errCode := p.do("admin", "POST", "/orgs/alpha/player-lists/"+l.ID+"/players", `{"names":["Alex"],"bedrock":["Someone New"]}`); code != http.StatusBadGateway || errCode != "upstream" {
		t.Errorf("GeyserMC down: %d %s", code, errCode)
	}
	got, err := p.srv.Store.PlayerList(ctx, mustOrg(t, p, "alpha"), l.ID)
	if err != nil || len(got.Players) != 3 {
		t.Errorf("after GeyserMC was down: %+v %v", got, err)
	}
}

func mustOrg(t *testing.T, p *panel, slug string) string {
	t.Helper()
	org, err := p.srv.Store.OrgBySlug(context.Background(), slug)
	if err != nil {
		t.Fatal(err)
	}
	return org.ID
}

// A running server that uses a list with Bedrock players: they go on its
// whitelist by whitelist.json, which the game reads again, as the game
// cannot look up their names; they are made operators once the game knows
// them, having joined, and are tried every look till then. One added in
// game is shown as a Bedrock player, left on, and taken off from Settings
// by the file too. With Floodgate set to another prefix, their names have
// it.
func TestBedrockPlayersOnARunningServer(t *testing.T) {
	for _, c := range []struct{ name, config, prefix string }{
		{"without Floodgate", "", "."},
		{"with Floodgate's prefix changed", "username-prefix: \"*\"\nreplace-spaces: true\n", "*"},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := newPanel(t)
			g := runningWithLists(t, p)
			p.srv.catalog.http.Transport = bedrockAccounts(newMojangAccounts("Steve"))
			if c.config != "" {
				if err := g.sc.MkdirAll(dataRoot + "/config/floodgate"); err != nil {
					t.Fatal(err)
				}
				put(t, g.sc, dataRoot+"/config/floodgate/config.yml", c.config)
			}
			tim, red := c.prefix+"Tim203", c.prefix+"Redned235"
			g.uuids = map[string]string{strings.ToLower(tim): timUUID, strings.ToLower(red): redUUID}
			_, event := sendList(t, p, "admin", "POST", "/orgs/alpha/player-lists", `{"name":"Event","names":["Steve"],"bedrock":["Tim203"]}`)
			changeWorld(t, p, func(mc *avsysv1.MinecraftServer) {
				mc.Spec.AutoOp = "off"
				mc.Spec.WhitelistLists = []string{event.ID}
				mc.Spec.OpLists = []string{event.ID}
			})
			look := func() string {
				t.Helper()
				if err := p.srv.syncOps(context.Background(), world(t, p), nil); err != nil {
					t.Fatal(err)
				}
				return g.commands()
			}
			notes := func() string {
				a := world(t, p).Annotations
				return a[avsysv1.AnnotationAutoWhitelist] + " | " + a[avsysv1.AnnotationAutoOps]
			}
			whitelistFile := func() string {
				players, err := listFile(g.sc, dataRoot+"/whitelist.json")
				if err != nil {
					t.Fatal(err)
				}
				var out []string
				for _, p := range players {
					out = append(out, p.Name+"="+p.UUID)
				}
				slices.Sort(out)
				return strings.Join(out, " ")
			}
			steve := "Steve=" + g.uuidOf("Steve")

			if got := look(); got != "op "+tim+"; op Steve; whitelist add Steve; whitelist reload" {
				t.Errorf("put on: %s", got)
			}
			if got := whitelistFile(); got != tim+"="+timUUID+" "+steve {
				t.Errorf("whitelist.json: %s", got)
			}
			if got := strings.Join(g.whitelist, " "); got != "Steve "+tim {
				t.Errorf("whitelist in game: %s", got)
			}
			if got := notes(); got != tim+",Steve | Steve" {
				t.Errorf("notes: %s", got)
			}
			// Not an operator till he joins, and tried again each look.
			if got := look(); got != "op "+tim {
				t.Errorf("before he joins: %s", got)
			}
			g.joined = []string{tim}
			if got := look(); got != "op "+tim {
				t.Errorf("once he has joined: %s", got)
			}
			if got := notes(); got != tim+",Steve | "+tim+",Steve" {
				t.Errorf("notes once he is an operator: %s", got)
			}
			if got := look(); got != "" {
				t.Errorf("in step: %s", got)
			}
			if got := gameLists(t, p); got["whitelist"] != "" || got["ops"] != "" {
				t.Errorf("added in game: %v", got)
			}

			// Taken off in game, as by "fwhitelist remove": put back.
			g.mu.Lock()
			g.whitelist = []string{"Steve"}
			g.saveWhitelist()
			g.mu.Unlock()
			if got := look(); got != "whitelist reload" {
				t.Errorf("taken off in game: %s", got)
			}
			if got := strings.Join(g.whitelist, " "); got != "Steve "+tim {
				t.Errorf("whitelist in game after: %s", got)
			}

			// Redned235 is added in game, as by "fwhitelist add": shown,
			// as a Bedrock player, and left on.
			g.mu.Lock()
			g.whitelist = append(g.whitelist, red)
			g.saveWhitelist()
			g.mu.Unlock()
			_, body := call(p, "admin", "GET", "/orgs/alpha/servers/world/lists", "")
			var views map[string]gameListView
			if err := json.Unmarshal([]byte(body), &views); err != nil {
				t.Fatal(err)
			}
			if in := views["whitelist"].InGame; len(in) != 1 || in[0] != (listedPlayer{Name: red, UUID: redUUID, Bedrock: true}) {
				t.Errorf("added in game: %+v", in)
			}
			if got := look(); got != "" {
				t.Errorf("one added in game: %s", got)
			}
			// Taken off from Settings: by the file, the rest left as they were.
			if code, body := call(p, "admin", "DELETE", "/orgs/alpha/servers/world/lists/whitelist/"+red, ""); code != http.StatusNoContent {
				t.Fatalf("taking him off: %d %s", code, body)
			}
			if got := g.commands(); got != "whitelist reload" {
				t.Errorf("taking him off: %s", got)
			}
			if got := whitelistFile(); got != tim+"="+timUUID+" "+steve {
				t.Errorf("whitelist.json after: %s", got)
			}
			// Tim203 is on the list, not to be taken off here.
			if code, errCode := p.do("admin", "DELETE", "/orgs/alpha/servers/world/lists/whitelist/"+tim, ""); code != http.StatusConflict || errCode != "saved_list" {
				t.Errorf("taking a list's Bedrock player off: %d %s", code, errCode)
			}

			// Taken off the list: off the server.
			ran := g.since()
			if code, _ := call(p, "admin", "DELETE", "/orgs/alpha/player-lists/"+event.ID+"/players/"+bareUUID(timUUID), ""); code != http.StatusOK {
				t.Fatal("taking him off the list")
			}
			waitFor(t, "a Bedrock player taken off the list is taken off", func() bool { return ran() == "deop "+tim+"; whitelist reload" })
			if got := whitelistFile(); got != steve {
				t.Errorf("whitelist.json at the end: %s", got)
			}
			if got := notes(); got != "Steve | Steve" {
				t.Errorf("notes at the end: %s", got)
			}
		})
	}
}

// Two gamertags cut to 16 characters make one name in game: both go on the
// whitelist, by UUID, and taking one off the list takes only that one off.
// One account put in another's place on the list takes its place on the
// whitelist too.
func TestBedrockPlayersOfOneName(t *testing.T) {
	p := newPanel(t)
	g := runningWithLists(t, p)
	p.srv.catalog.http.Transport = newGeyserAccounts(newMojangAccounts(), map[string]string{"ABCDEFGHIJKLMNO1": "1001", "ABCDEFGHIJKLMNO2": "1002", "ABCDEFGHIJKLMNO3": "1003"})
	const one, two, three = "00000000-0000-0000-0000-0000000003e9", "00000000-0000-0000-0000-0000000003ea", "00000000-0000-0000-0000-0000000003eb"
	_, event := sendList(t, p, "admin", "POST", "/orgs/alpha/player-lists", `{"name":"Event","bedrock":["ABCDEFGHIJKLMNO1","ABCDEFGHIJKLMNO2"]}`)
	changeWorld(t, p, func(mc *avsysv1.MinecraftServer) {
		mc.Spec.AutoOp = "off"
		mc.Spec.WhitelistLists = []string{event.ID}
	})
	look := func() string {
		t.Helper()
		if err := p.srv.syncOps(context.Background(), world(t, p), nil); err != nil {
			t.Fatal(err)
		}
		return g.commands()
	}
	uuids := func() string {
		players, err := listFile(g.sc, dataRoot+"/whitelist.json")
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, p := range players {
			if p.Name != ".ABCDEFGHIJKLMNO" {
				t.Errorf("named %s", p.Name)
			}
			out = append(out, p.UUID)
		}
		slices.Sort(out)
		return strings.Join(out, " ")
	}
	if got := look(); got != "whitelist reload" {
		t.Errorf("put on: %s", got)
	}
	if got := uuids(); got != one+" "+two {
		t.Errorf("whitelist.json: %s", got)
	}
	if got := look(); got != "" {
		t.Errorf("in step: %s", got)
	}
	if got := gameLists(t, p); got["whitelist"] != "" {
		t.Errorf("added in game: %v", got)
	}

	// One taken off the list: only that one comes off.
	ran := g.since()
	if code, _ := call(p, "admin", "DELETE", "/orgs/alpha/player-lists/"+event.ID+"/players/"+bareUUID(one), ""); code != http.StatusOK {
		t.Fatal("taking one off the list")
	}
	waitFor(t, "one of two of one name is taken off", func() bool { return ran() == "whitelist reload" })
	if got := uuids(); got != two {
		t.Errorf("whitelist.json after one was taken off: %s", got)
	}
	// Another put in their place: on, and the other off.
	ran = g.since()
	sendList(t, p, "admin", "POST", "/orgs/alpha/player-lists/"+event.ID+"/players", `{"bedrock":["ABCDEFGHIJKLMNO3"]}`)
	if code, _ := call(p, "admin", "DELETE", "/orgs/alpha/player-lists/"+event.ID+"/players/"+bareUUID(two), ""); code != http.StatusOK {
		t.Fatal("taking the other off the list")
	}
	waitFor(t, "one in another's place is put on", func() bool { return strings.HasSuffix(ran(), "whitelist reload") && uuids() == three })
	if got := world(t, p).Annotations[avsysv1.AnnotationAutoWhitelist]; got != ".ABCDEFGHIJKLMNO" {
		t.Errorf("note: %s", got)
	}
}

// A Bedrock player the game does not know yet cannot be taken off as an
// operator by command: they stay noted, to be taken off once it can.
func TestBedrockOperatorNotKnownStaysNoted(t *testing.T) {
	run := func(command string) error {
		if command == "deop .Tim203" || command == "deop Gone" {
			return errNoSuchPlayer
		}
		return nil
	}
	got, _, err := applyNames([]string{".Tim203", "Ada", "Gone"}, nil, []string{".Tim203", "Ada", "Gone"}, run, "op ", "deop ", func(name string) bool {
		return bedrockListed(listedPlayer{Name: name})
	})
	if err != nil || strings.Join(got, " ") != ".Tim203" {
		t.Errorf("noted: %v, %v; want .Tim203 left", got, err)
	}
}

// A stopped server's whitelist with a Bedrock player added by Floodgate:
// shown as one, and taken off by its file. Saved as a list, he is kept as a
// Bedrock player, by his XUID, with the gamertag GeyserMC has for him.
func TestBedrockPlayersOfAStoppedServer(t *testing.T) {
	p := newPanel(t)
	sc := filesFrom(t, p, sftp.InMemHandler())
	m := newMojangAccounts("Steve")
	p.srv.catalog.http.Transport = bedrockAccounts(m)
	put(t, sc, dataRoot+"/whitelist.json", `[
  {"uuid": "`+timUUID+`", "name": ".Tim203"},
  {"uuid": "`+redUUID+`", "name": ".Redned235"},
  {"uuid": "11111111-0000-4000-8000-000000000001", "name": "Steve"}
]`)
	_, body := call(p, "admin", "GET", "/orgs/alpha/servers/world/lists", "")
	var views map[string]gameListView
	if err := json.Unmarshal([]byte(body), &views); err != nil {
		t.Fatal(err)
	}
	var shown []string
	for _, pl := range views["whitelist"].InGame {
		shown = append(shown, pl.Name+"="+strconv.FormatBool(pl.Bedrock))
	}
	if got := strings.Join(shown, " "); got != ".Redned235=true .Tim203=true Steve=false" {
		t.Errorf("added in game: %s", got)
	}

	// Saved as a list.
	code, saved := sendList(t, p, "admin", "POST", "/orgs/alpha/player-lists", `{"name":"From world","fromServer":"world","list":"whitelist"}`)
	if code != http.StatusCreated || saved.names() != "Redned235 Steve Tim203" {
		t.Fatalf("saved: %d %+v", code, saved)
	}
	for _, pl := range saved.Players {
		if pl.Name == "Tim203" && (pl.Kind != store.KindBedrock || pl.XUID != timXUID || pl.UUID != bareUUID(timUUID)) {
			t.Errorf("Tim203 saved as %+v", pl)
		}
		if pl.Name == "Steve" && pl.Kind != store.KindJava {
			t.Errorf("Steve saved as %+v", pl)
		}
	}

	if code, body := call(p, "admin", "DELETE", "/orgs/alpha/servers/world/lists/whitelist/.Tim203", ""); code != http.StatusNoContent {
		t.Fatalf("taking .Tim203 off: %d %s", code, body)
	}
	players, err := listFile(sc, dataRoot+"/whitelist.json")
	if err != nil || strings.Join(playerNames(players), " ") != ".Redned235 Steve" {
		t.Errorf("whitelist.json after: %v %v", players, err)
	}
}

// A Bedrock player's head is made from the skin GeyserMC made of theirs for
// Java; one it has none for has none.
func TestBedrockHead(t *testing.T) {
	p := newPanel(t)
	m := newMojangAccounts()
	p.srv.catalog.http.Transport = m
	skin := image.NewNRGBA(image.Rect(0, 0, 64, 64))
	for y := 8; y < 16; y++ {
		for x := 8; x < 16; x++ {
			skin.Set(x, y, color.NRGBA{G: 200, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, skin); err != nil {
		t.Fatal(err)
	}
	m.files[geyserAPI+"skin/"+timXUID] = []byte(`{"hash":"4b34","is_steve":false,"last_update":1698329152868,"signature":"x","texture_id":"8f1543eec80e2478e9d6942bdce8f06ea8749f69bbf3d08875cf341c8b15fe5a","value":"y"}`)
	m.files["https://textures.minecraft.net/texture/8f1543eec80e2478e9d6942bdce8f06ea8749f69bbf3d08875cf341c8b15fe5a"] = buf.Bytes()
	m.files[geyserAPI+"skin/"+redXUID] = []byte(`{}`)
	rec := p.send("viewer", "GET", "/players/"+timUUID+"/head", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("Tim203's head: %d %s", rec.Code, rec.Body.String())
	}
	face, err := png.Decode(rec.Body)
	if err != nil {
		t.Fatal(err)
	}
	if _, g, _, _ := face.At(1, 1).RGBA(); g>>8 != 200 {
		t.Errorf("the face: %v", face.At(1, 1))
	}
	// One GeyserMC has no skin for is shown by their initial.
	if rec := p.send("viewer", "GET", "/players/"+redUUID+"/head", nil); rec.Code != http.StatusNotFound {
		t.Errorf("Redned235's head: %d", rec.Code)
	}
}
