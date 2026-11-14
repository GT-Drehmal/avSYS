package api

import (
	"context"
	"errors"
	"math"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
	"github.com/GT-Drehmal/avSYS/internal/controller"
	"github.com/GT-Drehmal/avSYS/internal/rcon"
)

// tickWait is how long the sampler waits on running servers to say how fast
// their games tick, all asked at once. One slower than that has no figure
// this time, and is not asked again until it has answered.
//
// tickGiveUp is how long a game is given to answer before its connection
// is closed, to be made again at the next sample. A game slow for a moment,
// as under load, keeps its connection: each one made shows in its log.
var (
	tickWait   = 3 * time.Second
	tickGiveUp = 20 * time.Second
)

// tickSample is how fast a server's game was ticking.
type tickSample struct {
	// TPS is the ticks the game ran a second, lately.
	TPS float64
	// MSPT is the milliseconds the game took over each tick, on average:
	// nil where it does not say.
	MSPT *float64
	// Target is the ticks a second the game aims for.
	Target float64
}

// tickWay is how a server's game is asked how fast it ticks.
type tickWay int

const (
	// tickUnasked is a game not yet asked: each way is tried in turn.
	tickUnasked tickWay = iota
	// tickPaper is Paper's tps and mspt, of Paper and what is built on it.
	tickPaper
	// tickVanilla is the game's own tick query, from 1.20.3 on.
	tickVanilla
	// tickNone is a game that answered to neither.
	tickNone
)

// tickState is what the sampler keeps of one server between samples.
type tickState struct {
	mu  sync.Mutex
	uid types.UID
	// game is the software the server ran when it was last asked: the
	// way found is tried again on other software, as after an update.
	game string
	way  tickWay
	// session is a console connection held open from one sample to the
	// next. The game logs each connection made to it, in two lines, which
	// every 30 seconds would fill its log.
	session *rcon.Client
}

// ticker is what the sampler keeps of each server, by namespace/name.
type ticker struct {
	mu      sync.Mutex
	servers map[string]*tickState
	// silent is the servers whose games answered to no way, by uid: told
	// here rather than read from their states, so that the page is not
	// held up while one is being asked.
	silent map[string]types.UID
}

// setSilent notes whether a server's game answered to no way.
func (t *ticker) setSilent(key string, uid types.UID, silent bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !silent {
		delete(t.silent, key)
		return
	}
	if t.silent == nil {
		t.silent = map[string]types.UID{}
	}
	t.silent[key] = uid
}

func (t *ticker) state(key string) *tickState {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.servers == nil {
		t.servers = map[string]*tickState{}
	}
	st := t.servers[key]
	if st == nil {
		st = &tickState{}
		t.servers[key] = st
	}
	return st
}

// isSilent says whether a server's game was found to say nothing of how fast
// it ticks, when it last ran.
func (t *ticker) isSilent(key string, uid types.UID) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	silent, ok := t.silent[key]
	return ok && silent == uid
}

// keep hangs up on the servers that are not running, and lets go of all
// that is kept of those that are gone, by namespace/name and uid.
func (t *ticker) keep(servers map[string]types.UID, running map[string]bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for key, st := range t.servers {
		uid, ok := servers[key]
		if running[key] && ok {
			continue
		}
		// One still being asked is left to its asking.
		if !st.mu.TryLock() {
			continue
		}
		st.hangUp()
		if !ok || uid != st.uid {
			delete(t.servers, key)
		}
		st.mu.Unlock()
	}
	for key, uid := range t.silent {
		if servers[key] != uid {
			delete(t.silent, key)
		}
	}
}

func (st *tickState) hangUp() {
	if st.session != nil {
		st.session.Close()
		st.session = nil
	}
}

// paperBased is whether a server runs Paper or software built on it, which
// have their own commands for how fast the game ticks.
func paperBased(mc *avsysv1.MinecraftServer) bool {
	if mc.Spec.Modpack != nil {
		return false
	}
	l := mc.Spec.Loader
	return l == avsysv1.LoaderPaper || l == avsysv1.LoaderPurpur || l == avsysv1.LoaderFolia
}

// sampleTicks asks each running server how fast its game is ticking, all at
// once and briefly, by namespace/name. A server that does not answer in
// time, or does not say, is left out.
func (s *Server) sampleTicks(ctx context.Context, servers []avsysv1.MinecraftServer) map[string]tickSample {
	wait, stopWaiting := context.WithTimeout(ctx, tickWait)
	defer stopWaiting()
	// The asking goes on past the wait, on its own time.
	ask, stopAsking := context.WithTimeout(ctx, tickGiveUp)
	var (
		mu sync.Mutex
		wg sync.WaitGroup
		// out is nil once the wait is over: what comes after is too late.
		out = map[string]tickSample{}
	)
	all, running := map[string]types.UID{}, map[string]bool{}
	for i := range servers {
		mc := servers[i]
		key := mc.Namespace + "/" + mc.Name
		all[key] = mc.UID
		if mc.Status.Phase != avsysv1.PhaseRunning {
			continue
		}
		running[key] = true
		wg.Go(func() {
			if t, ok := s.askTicks(ask, key, &mc); ok {
				mu.Lock()
				if out != nil {
					out[key] = t
				}
				mu.Unlock()
			}
		})
	}
	done := make(chan struct{})
	go func() {
		wg.Wait()
		stopAsking()
		close(done)
	}()
	select {
	case <-done:
	case <-wait.Done():
	}
	mu.Lock()
	got := out
	out = nil
	mu.Unlock()
	s.ticks.keep(all, running)
	return got
}

// askTicks asks one running server how fast its game is ticking. The first
// time, each way that software might answer to is tried, and the one that
// worked is remembered; a game that answered to none is not asked again
// until it runs other software. One that cannot be reached is asked again
// next time.
func (s *Server) askTicks(ctx context.Context, key string, mc *avsysv1.MinecraftServer) (tickSample, bool) {
	st := s.ticks.state(key)
	// Still being asked from before: no figure this time.
	if !st.mu.TryLock() {
		return tickSample{}, false
	}
	defer st.mu.Unlock()
	defer func() { s.ticks.setSilent(key, mc.UID, st.way == tickNone) }()
	game := string(mc.Spec.Loader) + " " + mc.Status.Version
	if st.uid != mc.UID || st.game != game {
		st.hangUp()
		st.uid, st.game, st.way = mc.UID, game, tickUnasked
	}
	ways := []tickWay{st.way}
	switch {
	case st.way == tickNone:
		return tickSample{}, false
	case st.way != tickUnasked:
	case paperBased(mc):
		ways = []tickWay{tickPaper, tickVanilla}
	default:
		ways = []tickWay{tickVanilla}
	}
	for _, way := range ways {
		t, ok, err := s.askTicksBy(ctx, st, mc, way)
		if err != nil {
			return tickSample{}, false
		}
		if ok {
			st.way = way
			return t, true
		}
	}
	if st.way == tickUnasked {
		st.way = tickNone
	}
	return tickSample{}, false
}

// askTicksBy asks one way. ok is false where the game's answer says nothing
// of its ticks, as to a command it does not have.
func (s *Server) askTicksBy(ctx context.Context, st *tickState, mc *avsysv1.MinecraftServer, way tickWay) (t tickSample, ok bool, err error) {
	switch way {
	case tickPaper:
		out, err := s.tickCommand(ctx, st, mc, "tps")
		if err != nil {
			return t, false, err
		}
		if t.TPS, ok = paperTPS(out); !ok {
			return t, false, nil
		}
		t.Target = 20
		// Not said by every build: the rate alone is enough.
		if out, err := s.tickCommand(ctx, st, mc, "mspt"); err == nil {
			if mspt, ok := paperMSPT(out); ok {
				t.MSPT = &mspt
			}
		}
		return t, true, nil
	case tickVanilla:
		out, err := s.tickCommand(ctx, st, mc, "tick query")
		if err != nil {
			return t, false, err
		}
		t, ok = tickQuery(out)
		return t, ok, nil
	}
	return t, false, nil
}

// tickCommand runs a command on a server's game, over the connection kept
// open for it, made first where there is none. GameCommand, where set, is
// asked instead.
func (s *Server) tickCommand(ctx context.Context, st *tickState, mc *avsysv1.MinecraftServer, command string) (string, error) {
	if s.GameCommand != nil {
		return s.GameCommand(ctx, mc, command)
	}
	if s.Kube == nil || s.Dialer == nil {
		return "", errors.New("no way to reach the game")
	}
	if st.session == nil {
		secret, err := s.Kube.CoreV1().Secrets(mc.Namespace).Get(ctx, controller.RCONSecretName(mc.Name), metav1.GetOptions{})
		if err != nil {
			return "", err
		}
		conn, err := s.Dialer.DialPod(ctx, mc.Namespace, mc.Name, controller.RCONPort)
		if err != nil {
			return "", err
		}
		// Tunnelled connections ignore deadlines: the time allowed is kept
		// to by closing.
		stop := context.AfterFunc(ctx, func() { conn.Close() })
		session, err := rcon.Login(conn, string(secret.Data["password"]))
		if !stop() && err == nil {
			err = ctx.Err()
		}
		if err != nil {
			conn.Close()
			return "", err
		}
		st.session = session
	}
	session := st.session
	stop := context.AfterFunc(ctx, func() { session.Close() })
	out, err := session.Command(command)
	if !stop() && err == nil {
		err = ctx.Err()
	}
	if err != nil {
		st.hangUp()
	}
	return out, err
}

// formatting is the game's color and style codes: a section sign and the
// character after it.
var formatting = regexp.MustCompile(`§.`)

func plain(out string) string { return formatting.ReplaceAllString(out, "") }

var (
	tickRate      = regexp.MustCompile(`Target tick rate: ([0-9.]+) per second`)
	tickTime      = regexp.MustCompile(`Average time per tick: ([0-9.]+) ?ms`)
	paperRates    = regexp.MustCompile(`TPS from last ([^:]+):\s*[^0-9*]*([^\n]+)`)
	foliaRate     = regexp.MustCompile(`Lowest Region TPS: ([0-9.]+)`)
	paperTimes    = regexp.MustCompile(`from last ([^:]+):\s*[^0-9]*([^\n]+)`)
	listSeparator = regexp.MustCompile(`,\s*`)
)

// tickQuery reads the game's answer to tick query, as of 1.20.3:
//
//	The game is running normally
//	Target tick rate: 20.0 per second.
//	Average time per tick: 0.1ms (Target: 50.0ms)
//	Percentiles: P50: 0.1ms P95: 0.2ms P99: 0.3ms, sample: 100
//
// with or without the line breaks between messages. It does not say how
// many ticks the game ran: a game that keeps up runs at its target, and one
// that does not runs a tick as soon as the last is done.
func tickQuery(out string) (tickSample, bool) {
	out = plain(out)
	rate, took := tickRate.FindStringSubmatch(out), tickTime.FindStringSubmatch(out)
	if rate == nil || took == nil {
		return tickSample{}, false
	}
	target, err1 := strconv.ParseFloat(rate[1], 64)
	mspt, err2 := strconv.ParseFloat(took[1], 64)
	if err1 != nil || err2 != nil || target <= 0 {
		return tickSample{}, false
	}
	tps := target
	// Sprinting, the target is set aside: ticks are run one after another.
	sprinting := strings.Contains(out, "(ignored")
	if mspt > 0 && (sprinting || 1000/mspt < target) {
		tps = 1000 / mspt
	}
	return tickSample{TPS: round2(tps), MSPT: &mspt, Target: target}, true
}

// paperTPS reads the figure for the last minute from Paper's answer to tps:
//
//	TPS from last 1m, 5m, 15m: 20.0, 20.0, 20.0
//
// Purpur's starts with the last 5s. A figure above 20 is marked with a
// star. Folia's answer is a report on its regions, of which the slowest is
// taken.
func paperTPS(out string) (float64, bool) {
	out = plain(out)
	if m := foliaRate.FindStringSubmatch(out); m != nil {
		tps, err := strconv.ParseFloat(m[1], 64)
		return tps, err == nil
	}
	m := paperRates.FindStringSubmatch(out)
	if m == nil {
		return 0, false
	}
	figures := listSeparator.Split(strings.TrimSpace(m[2]), -1)
	at := indexOf(listSeparator.Split(m[1], -1), "1m")
	if at < 0 || at >= len(figures) {
		return 0, false
	}
	tps, err := strconv.ParseFloat(strings.TrimPrefix(figures[at], "*"), 64)
	return tps, err == nil
}

// paperMSPT reads the average for the last minute from Paper's answer to
// mspt:
//
//	Server tick times (avg/min/max) from last 5s, 10s, 1m:
//	◴ 0.5/0.2/2.7, 0.6/0.2/3.4, 0.7/0.2/342.1
//
// Folia answers with figures that do not hold together, which are not taken.
func paperMSPT(out string) (float64, bool) {
	out = plain(out)
	m := paperTimes.FindStringSubmatch(out)
	if m == nil || !strings.Contains(out, "tick times") {
		return 0, false
	}
	figures := listSeparator.Split(strings.TrimSpace(m[2]), -1)
	at := indexOf(listSeparator.Split(m[1], -1), "1m")
	if at < 0 || at >= len(figures) {
		return 0, false
	}
	parts := strings.Split(figures[at], "/")
	if len(parts) != 3 {
		return 0, false
	}
	var n [3]float64
	for i, p := range parts {
		f, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
		if err != nil {
			return 0, false
		}
		n[i] = f
	}
	avg, low, high := n[0], n[1], n[2]
	if low > high || avg < low || avg > high {
		return 0, false
	}
	return avg, true
}

// indexOf returns where want is in names, or -1.
func indexOf(names []string, want string) int {
	for i, n := range names {
		if strings.TrimSpace(n) == want {
			return i
		}
	}
	return -1
}

func round2(f float64) float64 { return math.Round(f*100) / 100 }
