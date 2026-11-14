package api

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	kubefake "k8s.io/client-go/kubernetes/fake"
	"sigs.k8s.io/controller-runtime/pkg/client"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
)

// answer is what a server of the kind in testdata/ticks answered to one of
// the commands the sampler runs, as it was recorded from a real one over RCON.
func answer(t *testing.T, game, command string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "ticks", game+"."+strings.ReplaceAll(command, " ", "-")+".txt"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Each kind of server answers how fast it ticks in its own words, or not at
// all: what each said is read as it was meant, and an answer that says
// nothing of ticks is not taken for one.
func TestTickAnswers(t *testing.T) {
	type want struct{ tps, mspt, target float64 }
	none := (*want)(nil)
	for _, c := range []struct {
		game      string
		tickQuery *want
		// paperTPS and paperMSPT are what tps and mspt say: nothing, where
		// the game has no such command.
		paperTPS, paperMSPT *float64
	}{
		{game: "vanilla-1.21.4", tickQuery: &want{20, 0.1, 20}},
		{game: "vanilla-26.3", tickQuery: &want{20, 0.2, 20}},
		{game: "fabric-1.21.4", tickQuery: &want{20, 0.1, 20}},
		{game: "neoforge-1.21.1", tickQuery: &want{20, 0.2, 20}},
		{game: "paper-1.21.4", tickQuery: &want{20, 0.5, 20}, paperTPS: ptr(20.0), paperMSPT: ptr(0.7)},
		{game: "paper-26.3", tickQuery: &want{20, 0.4, 20}, paperTPS: ptr(20.0), paperMSPT: ptr(1.4)},
		{game: "paper-1.20.1", tickQuery: none, paperTPS: ptr(20.0), paperMSPT: ptr(0.8)},
		// Purpur's tps starts with the last five seconds.
		{game: "purpur-1.21.4", tickQuery: &want{20, 0.2, 20}, paperTPS: ptr(20.0), paperMSPT: ptr(0.4)},
		// Folia's tps is a report on its regions, of which the slowest is
		// taken; its mspt is figures that do not hold together.
		{game: "folia-1.21.8", tickQuery: none, paperTPS: ptr(19.98)},
		{game: "vanilla-1.20.1", tickQuery: none},
		{game: "fabric-1.20.1", tickQuery: none},
		{game: "forge-1.20.1", tickQuery: none},
	} {
		got, ok := tickQuery(answer(t, c.game, "tick query"))
		switch {
		case c.tickQuery == nil && ok:
			t.Errorf("%s: tick query read as %+v, want nothing", c.game, got)
		case c.tickQuery != nil && (!ok || got.TPS != c.tickQuery.tps || got.MSPT == nil || *got.MSPT != c.tickQuery.mspt || got.Target != c.tickQuery.target):
			t.Errorf("%s: tick query read as %v %s, want %+v", c.game, ok, tickString(got), *c.tickQuery)
		}
		if tps, ok := paperTPS(answer(t, c.game, "tps")); ok != (c.paperTPS != nil) || ok && tps != *c.paperTPS {
			t.Errorf("%s: tps read as %v, %v; want %v", c.game, tps, ok, c.paperTPS)
		}
		if mspt, ok := paperMSPT(answer(t, c.game, "mspt")); ok != (c.paperMSPT != nil) || ok && mspt != *c.paperMSPT {
			t.Errorf("%s: mspt read as %v, %v; want %v", c.game, mspt, ok, c.paperMSPT)
		}
	}

	// A game that cannot keep up runs as fast as its ticks allow; one that
	// sprints ignores its target. These are as the game words them.
	for answer, want := range map[string]float64{
		"The game is running, but can't keep up with the current tick rateTarget tick rate: 20.0 per second.\nAverage time per tick: 80.0ms (Target: 50.0ms)Percentiles: P50: 79.2ms P95: 95.1ms P99: 120.3ms, sample: 100": 12.5,
		"The game is sprintingTarget tick rate: 20.0 per second (ignored, reference only).\nAverage time per tick: 4.0msPercentiles: P50: 3.9ms P95: 4.4ms P99: 5.0ms, sample: 100":                                         250,
		"The game is running normallyTarget tick rate: 40.0 per second.\nAverage time per tick: 30.0ms (Target: 25.0ms)Percentiles: P50: 30.0ms P95: 31.0ms P99: 33.0ms, sample: 100":                                       33.33,
		"The game is frozenTarget tick rate: 20.0 per second.\nAverage time per tick: 0.0ms (Target: 50.0ms)Percentiles: P50: 0.0ms P95: 0.1ms P99: 0.1ms, sample: 100":                                                     20,
	} {
		if got, ok := tickQuery(answer); !ok || got.TPS != want {
			t.Errorf("%q: read as %s, want %v a second", answer, tickString(got), want)
		}
	}
	// Above 20, Paper marks the figure with a star.
	if got, ok := paperTPS("§6TPS from last 1m, 5m, 15m: §a*20.0§r, §a*20.0§r, §a19.5\n"); !ok || got != 20 {
		t.Errorf("a starred figure: %v, %v", got, ok)
	}
	if got, ok := paperTPS("§6TPS from last 1m, 5m, 15m: §c14.21§r, §e18.3§r, §a19.9\n"); !ok || got != 14.21 {
		t.Errorf("a slow minute: %v, %v", got, ok)
	}
}

func ptr(f float64) *float64 { return &f }

func tickString(t tickSample) string {
	b, _ := json.Marshal(struct {
		TPS, Target float64
		MSPT        *float64
	}{t.TPS, t.Target, t.MSPT})
	return string(b)
}

// games answers the sampler's commands as servers of the kinds in
// testdata/ticks do, by server name, and counts what each was asked.
type games struct {
	mu    sync.Mutex
	kind  map[string]string
	asked map[string][]string
	// fail is the servers that cannot be reached, and hang those that
	// never answer.
	fail, hang map[string]bool
	// held is the servers that answer only once let go, and late what
	// each one's asking had come to when it did: nil, as it still went on.
	held map[string]chan struct{}
	late map[string]error
	t    *testing.T
}

func (g *games) command(ctx context.Context, mc *avsysv1.MinecraftServer, command string) (string, error) {
	g.mu.Lock()
	g.asked[mc.Name] = append(g.asked[mc.Name], command)
	kind, fail, hang, held := g.kind[mc.Name], g.fail[mc.Name], g.hang[mc.Name], g.held[mc.Name]
	g.mu.Unlock()
	if held != nil {
		<-held
		g.mu.Lock()
		g.late[mc.Name] = ctx.Err()
		g.mu.Unlock()
	}
	if hang {
		<-ctx.Done()
		return "", ctx.Err()
	}
	if fail {
		return "", errors.New("connection refused")
	}
	return answer(g.t, kind, command), nil
}

func (g *games) take() map[string][]string {
	g.mu.Lock()
	defer g.mu.Unlock()
	asked := g.asked
	g.asked = map[string][]string{}
	return asked
}

// Each server is asked in the way its software answers: Paper and those
// built on it with their own commands, others with the game's. The way that
// worked is remembered, and so is a game that answered to none: it is not
// asked again until it runs other software. A server that cannot be reached
// or is slow to answer has no figure this time, and holds up no other. One
// slow for a while is let answer, and not asked again meanwhile; one that
// never answers is given up on.
func TestTickSampling(t *testing.T) {
	p := newPanel(t)
	g := &games{t: t, asked: map[string][]string{}, kind: map[string]string{
		"paper": "paper-1.21.4", "purpur": "purpur-1.21.4", "folia": "folia-1.21.8", "old-paper": "paper-1.20.1",
		"vanilla": "vanilla-1.21.4", "fabric": "fabric-1.21.4", "old": "vanilla-1.20.1", "forge": "forge-1.20.1",
		"away": "vanilla-1.21.4", "slow": "paper-1.21.4", "never": "vanilla-1.21.4", "stopped": "vanilla-1.21.4",
	}, fail: map[string]bool{"away": true}, hang: map[string]bool{"never": true},
		held: map[string]chan struct{}{"slow": make(chan struct{})}, late: map[string]error{}}
	p.srv.GameCommand = g.command
	defer func(wait, giveUp time.Duration) { tickWait, tickGiveUp = wait, giveUp }(tickWait, tickGiveUp)
	tickWait, tickGiveUp = 300*time.Millisecond, time.Second
	loaders := map[string]avsysv1.Loader{"paper": avsysv1.LoaderPaper, "purpur": avsysv1.LoaderPurpur, "folia": avsysv1.LoaderFolia, "old-paper": avsysv1.LoaderPaper, "slow": avsysv1.LoaderPaper, "fabric": avsysv1.LoaderFabric, "forge": avsysv1.LoaderForge}
	var servers []avsysv1.MinecraftServer
	for name := range g.kind {
		mc := avsysv1.MinecraftServer{ObjectMeta: metav1.ObjectMeta{Namespace: "mc-alpha", Name: name, UID: types.UID(name)}}
		mc.Spec.Loader = avsysv1.LoaderVanilla
		if l, ok := loaders[name]; ok {
			mc.Spec.Loader = l
		}
		mc.Status.Phase, mc.Status.Version = avsysv1.PhaseRunning, g.kind[name]
		if name == "stopped" {
			mc.Status.Phase = avsysv1.PhaseStopped
		}
		servers = append(servers, mc)
	}
	slices.SortFunc(servers, func(a, b avsysv1.MinecraftServer) int { return strings.Compare(a.Name, b.Name) })

	ctx := context.Background()
	start := time.Now()
	got := p.srv.sampleTicks(ctx, servers)
	if took := time.Since(start); took > tickWait+time.Second {
		t.Errorf("sampling took %v, more than the %v a slow server is waited on", took, tickWait)
	}
	want := map[string]tickSample{
		"paper":     {TPS: 20, MSPT: ptr(0.7), Target: 20},
		"purpur":    {TPS: 20, MSPT: ptr(0.4), Target: 20},
		"folia":     {TPS: 19.98, Target: 20},
		"old-paper": {TPS: 20, MSPT: ptr(0.8), Target: 20},
		"vanilla":   {TPS: 20, MSPT: ptr(0.1), Target: 20},
		"fabric":    {TPS: 20, MSPT: ptr(0.1), Target: 20},
	}
	check := func(when string, got map[string]tickSample) {
		t.Helper()
		if len(got) != len(want) {
			t.Errorf("%s: figures for %v, want %v", when, slices.Sorted(mapKeys(got)), slices.Sorted(mapKeys(want)))
		}
		for name, w := range want {
			if g, ok := got["mc-alpha/"+name]; !ok || tickString(g) != tickString(w) {
				t.Errorf("%s: %s read as %s, want %s", when, name, tickString(g), tickString(w))
			}
		}
	}
	check("first", got)
	asked := g.take()
	for name, commands := range map[string][]string{
		"paper": {"tps", "mspt"}, "purpur": {"tps", "mspt"}, "folia": {"tps", "mspt"}, "old-paper": {"tps", "mspt"},
		"vanilla": {"tick query"}, "fabric": {"tick query"}, "old": {"tick query"}, "forge": {"tick query"},
		"away": {"tick query"}, "slow": {"tps"}, "never": {"tick query"}, "stopped": nil,
	} {
		if !slices.Equal(asked[name], commands) {
			t.Errorf("first: %s was asked %q, want %q", name, asked[name], commands)
		}
	}

	// Asked again, each in the way found; none asked of the games that
	// answered to nothing, nor of those still being asked. The one that
	// could not be reached is tried again.
	check("again", p.srv.sampleTicks(ctx, servers))
	asked = g.take()
	for name, commands := range map[string][]string{
		"paper": {"tps", "mspt"}, "vanilla": {"tick query"}, "old": nil, "forge": nil, "away": {"tick query"}, "slow": nil, "never": nil,
	} {
		if !slices.Equal(asked[name], commands) {
			t.Errorf("again: %s was asked %q, want %q", name, asked[name], commands)
		}
	}
	// The page says so of a game that answered to nothing, and of no other.
	for name, silent := range map[string]bool{"old": true, "forge": true, "vanilla": false, "away": false, "slow": false, "never": false, "stopped": false} {
		if got := p.srv.ticks.isSilent("mc-alpha/"+name, types.UID(name)); got != silent {
			t.Errorf("%s: said to report nothing: %v, want %v", name, got, silent)
		}
	}

	// The slow game answers well after the sample stopped waiting, and was
	// let: its asking had not been ended, which would have closed its
	// connection. The one that never answers is given up on in time.
	close(g.held["slow"])
	settle := func(name string) {
		st := p.srv.ticks.state("mc-alpha/" + name)
		st.mu.Lock()
		defer st.mu.Unlock()
	}
	settle("slow")
	if err, ok := g.late["slow"]; !ok || err != nil {
		t.Errorf("a slow game's asking had come to %v, %v when it answered; want it going on", err, ok)
	}
	given := time.Now()
	settle("never")
	if waited := time.Since(given); waited > tickGiveUp+time.Second {
		t.Errorf("a game that never answers was waited on for %v more", waited)
	}
	g.take()

	// Updated to a version that has it, the game is asked again.
	for i := range servers {
		if servers[i].Name == "old" {
			servers[i].Status.Version = "vanilla-1.21.4"
			g.mu.Lock()
			g.kind["old"] = "vanilla-1.21.4"
			g.mu.Unlock()
		}
	}
	got = p.srv.sampleTicks(ctx, servers)
	asked = g.take()
	if _, ok := got["mc-alpha/old"]; !ok || !slices.Equal(asked["old"], []string{"tick query"}) {
		t.Errorf("an updated server was not asked again, or did not answer: %+v", got)
	}
	// The slow game, done, is asked again in the way it answered to.
	if _, ok := got["mc-alpha/slow"]; !ok || !slices.Equal(asked["slow"], []string{"tps", "mspt"}) {
		t.Errorf("the slow game was asked %q after answering, and read as %+v", asked["slow"], got["mc-alpha/slow"])
	}
	settle("never")
	if p.srv.ticks.isSilent("mc-alpha/old", "old") {
		t.Error("an updated server that answers is still said to report nothing")
	}

	// Deleted, what was found of it is let go of.
	p.srv.sampleTicks(ctx, servers[:1])
	p.srv.ticks.mu.Lock()
	kept := len(p.srv.ticks.servers)
	p.srv.ticks.mu.Unlock()
	if kept != 1 {
		t.Errorf("%d servers kept after all but one were deleted", kept)
	}
}

func mapKeys[V any](m map[string]V) func(func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(strings.TrimPrefix(k, "mc-alpha/")) {
				return
			}
		}
	}
}

// What the sampler is told is noted with the server's use, and the page is
// told where the game does not say.
func TestTicksNoted(t *testing.T) {
	p := newPanel(t)
	ctx := context.Background()
	world := &avsysv1.MinecraftServer{}
	if err := p.srv.Client.Get(ctx, client.ObjectKey{Namespace: "mc-alpha", Name: "world"}, world); err != nil {
		t.Fatal(err)
	}
	world.Status.Phase, world.Status.Version = avsysv1.PhaseRunning, "1.21.4"
	if err := p.srv.Client.Status().Update(ctx, world); err != nil {
		t.Fatal(err)
	}
	kind := "vanilla-1.21.4"
	p.srv.GameCommand = func(_ context.Context, _ *avsysv1.MinecraftServer, command string) (string, error) {
		return answer(t, kind, command), nil
	}
	view := func() usageView {
		t.Helper()
		req := httptest.NewRequest("GET", "/api/v1/orgs/alpha/servers/world/usage", nil)
		req.Header.Set("Authorization", "Bearer "+p.tokens["viewer"])
		rec := httptest.NewRecorder()
		p.h.ServeHTTP(rec, req)
		var v usageView
		if err := json.Unmarshal(rec.Body.Bytes(), &v); rec.Code != 200 || err != nil {
			t.Fatalf("asking for a server's use: %d %s", rec.Code, rec.Body)
		}
		return v
	}
	p.srv.SampleUsage(ctx, time.Now())
	v := view()
	if len(v.Samples) != 1 || v.NoTicks {
		t.Fatalf("answered %+v", v)
	}
	if s := v.Samples[0]; s.TPS == nil || *s.TPS != 20 || s.TickTarget == nil || *s.TickTarget != 20 || s.MSPT == nil || *s.MSPT != 0.1 {
		t.Errorf("noted %s", sampleString(s))
	}

	// Updated to a version without tick query.
	world.Status.Version = "1.20.1"
	if err := p.srv.Client.Status().Update(ctx, world); err != nil {
		t.Fatal(err)
	}
	kind = "vanilla-1.20.1"
	p.srv.SampleUsage(ctx, time.Now())
	v = view()
	if len(v.Samples) != 2 || v.Samples[1].TPS != nil || v.Samples[1].MSPT != nil || !v.NoTicks {
		t.Errorf("for a game that does not say, answered %+v", v)
	}
	raw, _ := json.Marshal(v.Samples[1])
	if strings.Contains(string(raw), "tps") || strings.Contains(string(raw), "mspt") {
		t.Errorf("a sample without ticks is sent as %s", raw)
	}
}

// The game is asked over one connection, kept open from one sample to the
// next: it logs each connection made to it. A connection that breaks is
// made again at the next sample.
func TestTicksOverOneConnection(t *testing.T) {
	p := newPanel(t)
	ctx := context.Background()
	world := &avsysv1.MinecraftServer{}
	if err := p.srv.Client.Get(ctx, client.ObjectKey{Namespace: "mc-alpha", Name: "world"}, world); err != nil {
		t.Fatal(err)
	}
	world.Status.Phase, world.Status.Version = avsysv1.PhaseRunning, "1.21.4"
	p.srv.Kube = kubefake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: "mc-alpha", Name: "world-rcon"},
		Data:       map[string][]byte{"password": []byte("secret")},
	})
	g := &rconGame{password: "secret", answer: func(command string) string { return answer(t, "vanilla-1.21.4", command) }}
	p.srv.Dialer = g
	servers := []avsysv1.MinecraftServer{*world}
	for i := range 3 {
		if got := p.srv.sampleTicks(ctx, servers); got["mc-alpha/world"].TPS != 20 {
			t.Fatalf("sample %d: %+v", i, got)
		}
	}
	if g.connections() != 1 {
		t.Errorf("%d connections for three samples, want one", g.connections())
	}
	// The game goes away, and comes back.
	g.dropAll()
	p.srv.sampleTicks(ctx, servers)
	if got := p.srv.sampleTicks(ctx, servers); got["mc-alpha/world"].TPS != 20 || g.connections() != 2 {
		t.Errorf("after the connection broke: %+v, %d connections", got, g.connections())
	}
	// Stopped, the connection is closed.
	servers[0].Status.Phase = avsysv1.PhaseStopped
	p.srv.sampleTicks(ctx, servers)
	if open := g.open(); open != 0 {
		t.Errorf("%d connections left open to a stopped server", open)
	}
}

// rconGame answers RCON as Minecraft does, on connections the panel dials.
type rconGame struct {
	password string
	answer   func(command string) string
	mu       sync.Mutex
	made     int
	conns    []net.Conn
}

func (g *rconGame) connections() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.made
}

func (g *rconGame) dropAll() {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, c := range g.conns {
		c.Close()
	}
	g.conns = nil
}

// open counts the connections the panel has not closed.
func (g *rconGame) open() int {
	g.mu.Lock()
	conns := slices.Clone(g.conns)
	g.mu.Unlock()
	n := 0
	for _, c := range conns {
		_ = c.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		if _, err := c.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
			n++
		}
	}
	return n
}

func (g *rconGame) DialPod(_ context.Context, _, _ string, _ int) (net.Conn, error) {
	panel, game := net.Pipe()
	g.mu.Lock()
	g.made++
	g.conns = append(g.conns, game)
	g.mu.Unlock()
	go g.serve(game)
	return panel, nil
}

func (g *rconGame) serve(conn net.Conn) {
	read := func() (id, typ int32, body string, err error) {
		var head [12]byte
		if _, err := io.ReadFull(conn, head[:]); err != nil {
			return 0, 0, "", err
		}
		rest := make([]byte, binary.LittleEndian.Uint32(head[0:4])-8)
		if _, err := io.ReadFull(conn, rest); err != nil {
			return 0, 0, "", err
		}
		return int32(binary.LittleEndian.Uint32(head[4:8])), int32(binary.LittleEndian.Uint32(head[8:12])), string(rest[:len(rest)-2]), nil
	}
	write := func(id int32, body string) {
		buf := binary.LittleEndian.AppendUint32(nil, uint32(10+len(body)))
		buf = binary.LittleEndian.AppendUint32(buf, uint32(id))
		buf = binary.LittleEndian.AppendUint32(buf, 0)
		buf = append(append(buf, body...), 0, 0)
		_, _ = conn.Write(buf)
	}
	id, _, password, err := read()
	if err != nil {
		return
	}
	if password != g.password {
		write(-1, "")
		return
	}
	write(id, "")
	for {
		id, typ, body, err := read()
		if err != nil {
			return
		}
		if typ == 100 {
			write(id, "Unknown request 64")
			continue
		}
		write(id, g.answer(body))
	}
}
