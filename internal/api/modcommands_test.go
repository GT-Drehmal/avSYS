package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
	"github.com/GT-Drehmal/avSYS/internal/controller"
)

// helpRecording is what a real server answered over RCON to each help
// command the panel asked it: a Fabric 1.20.1 server with Yet Another
// Bingo (bingo), or a Paper 1.21.4 server with LuckPerms (paper).
func helpRecording(t *testing.T, name string) map[string]string {
	t.Helper()
	b, err := os.ReadFile("../../web/tests/upstream/commands/help/" + name + ".json")
	if err != nil {
		t.Fatal(err)
	}
	var rec map[string]string
	if err := json.Unmarshal(b, &rec); err != nil {
		t.Fatal(err)
	}
	return rec
}

// answering runs commands from a recording, noting each; one not in it is
// answered as the game answers a help it has none of.
func answering(t *testing.T, rec map[string]string, asked *[]string) func(string) (string, error) {
	return func(command string) (string, error) {
		*asked = append(*asked, command)
		if !helpCommand(command) {
			t.Errorf("ran %q, not a help command", command)
		}
		out, ok := rec[command]
		if !ok {
			t.Errorf("asked %q, which is not recorded", command)
			return "Unknown command or insufficient permissions", nil
		}
		return out, nil
	}
}

func vanillaNames(t *testing.T) map[string]bool {
	t.Helper()
	gz, err := packCommands(commandsFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	return newCommandTree(gz).names()
}

func TestParseUsage(t *testing.T) {
	for _, c := range []struct {
		line string
		want usageLine
	}{
		// The lines of help seen on a server with Yet Another Bingo.
		{"bingo [card|difficulty|reroll|lobby|goal|mode|options|timelimit|start|shuffleteams|filter|book]",
			usageLine{chain: []string{"bingo"}, choices: []string{"card", "difficulty", "reroll", "lobby", "goal", "mode", "options", "timelimit", "start", "shuffleteams", "filter", "book"}, choicesOptional: true}},
		{"bingoprefs (bossbar|hide_lobby_prompt|scoreboard|scoreboard_auto_hide|leading_messages|score_messages|item_messages|night_vision)",
			usageLine{chain: []string{"bingoprefs"}, choices: []string{"bossbar", "hide_lobby_prompt", "scoreboard", "scoreboard_auto_hide", "leading_messages", "score_messages", "item_messages", "night_vision"}}},
		{"bingostats [<player>|reset]", usageLine{chain: []string{"bingostats"}, choices: []string{"<player>", "reset"}, choicesOptional: true}},
		{"[card] [set|seed|assign|stack]", usageLine{chain: []string{"card"}, optional: true, choices: []string{"set", "seed", "assign", "stack"}, choicesOptional: true}},
		{"[difficulty] (<S>|easy|medium|hard|extreme|impossible)", usageLine{chain: []string{"difficulty"}, optional: true, choices: []string{"<S>", "easy", "medium", "hard", "extreme", "impossible"}}},
		{"[mode] (lockout|inventory|hidden_items)", usageLine{chain: []string{"mode"}, optional: true, choices: []string{"lockout", "inventory", "hidden_items"}}},
		{"[timelimit] (<minutes>|off)", usageLine{chain: []string{"timelimit"}, optional: true, choices: []string{"<minutes>", "off"}}},
		{"[shuffleteams] <number_of_teams>", usageLine{chain: []string{"shuffleteams", "<number_of_teams>"}, optional: true}},
		{"[lobby] [confirm]", usageLine{chain: []string{"lobby"}, optional: true, choices: []string{"confirm"}, choicesOptional: true}},
		{"[reroll]", usageLine{chain: []string{"reroll"}, optional: true}},
		{"card set <tile> (<objective>|random|free_space)", usageLine{chain: []string{"card", "set", "<tile>"}, choices: []string{"<objective>", "random", "free_space"}}},
		{"g <message>", usageLine{chain: []string{"g", "<message>"}}},
		{"seed", usageLine{chain: []string{"seed"}}},
		// Another name for a command, and one that goes on as any command.
		{"xp -> experience", usageLine{chain: []string{"xp"}, redirect: "experience"}},
		{"align <axes> -> execute", usageLine{chain: []string{"align", "<axes>"}, redirect: "execute"}},
		{"run ...", usageLine{chain: []string{"run"}, toRoot: true}},
		// A Bukkit command on Paper, and WorldEdit's.
		{"lp [<args>]", usageLine{chain: []string{"lp"}, choices: []string{"<args>"}, choicesOptional: true}},
		{"/wand [<args>]", usageLine{chain: []string{"/wand"}, choices: []string{"<args>"}, choicesOptional: true}},
	} {
		got, ok := parseUsage(c.line)
		if !ok || !reflect.DeepEqual(got, c.want) {
			t.Errorf("%q: %+v %v, want %+v", c.line, got, ok, c.want)
		}
	}
	for _, bad := range []string{
		"", `help" for help.`, "help<--[HERE]", "see below for error, here",
		"(a|b)", "foo (a|b) bar", "foo [a] bar baz [c]", "foo ->", "-> foo", "foo -> bar baz", "foo ... bar", "foo (a|)", "foo (a|[b])", "foo <a", "foo (a)",
	} {
		if u, ok := parseUsage(bad); ok {
			t.Errorf("%q read as %+v", bad, u)
		}
	}
}

func TestHelpLines(t *testing.T) {
	// Over RCON, with nothing between the lines; at a console, one to a line.
	for _, out := range []string{
		"/bingo [card] [set|seed|assign|stack]/bingo [timelimit] (<minutes>|off)/bingo [book]",
		"/bingo [card] [set|seed|assign|stack]\n/bingo [timelimit] (<minutes>|off)\n/bingo [book]\n",
	} {
		if got, want := helpLines(out, "bingo"), []string{"[card] [set|seed|assign|stack]", "[timelimit] (<minutes>|off)", "[book]"}; !reflect.DeepEqual(got, want) {
			t.Errorf("%q: %q", out, got)
		}
	}
	for _, out := range []string{
		"/xp -> experience/bingostats [<player>|reset]//wand [<args>]/save-off/a/b",
		"/xp -> experience\n/bingostats [<player>|reset]\n//wand [<args>]\n/save-off\n/a/b\n",
	} {
		if got, want := helpLines(out, ""), []string{"xp -> experience", "bingostats [<player>|reset]", "/wand [<args>]", "save-off", "a", "b"}; !reflect.DeepEqual(got, want) {
			t.Errorf("%q: %q", out, got)
		}
	}
	// What is not help of that command is left out.
	if got := helpLines("Unknown command or insufficient permissions", "bingo"); len(got) != 0 {
		t.Errorf("an error read as %q", got)
	}
	if got := helpLines("§6/bingo §f[card]", "bingo"); !reflect.DeepEqual(got, []string{"[card]"}) {
		t.Errorf("with colours: %q", got)
	}
}

// The commands of a server with Yet Another Bingo, from its help as it
// answered over RCON: the same tree the browser tests complete from.
func TestReadHelpOfAModdedServer(t *testing.T) {
	var asked []string
	root, err := readHelp(context.Background(), answering(t, helpRecording(t, "bingo"), &asked), vanillaNames(t))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := root.MarshalJSON()
	var pretty bytes.Buffer
	_ = json.Indent(&pretty, got, "", "  ")
	pretty.WriteByte('\n')
	const golden = "../../web/tests/upstream/commands/help/bingo.tree.json"
	if os.Getenv("AVSYS_UPDATE_GOLDEN") != "" {
		_ = os.WriteFile(golden, pretty.Bytes(), 0o644)
	}
	want, _ := os.ReadFile(golden)
	if !bytes.Equal(pretty.Bytes(), want) {
		t.Errorf("the tree read is not %s (AVSYS_UPDATE_GOLDEN=1 writes it):\n%s", golden, pretty.Bytes())
	}
	// Only the game's commands it does not have, in the order of its help.
	if want := []string{"bingo", "bingoprefs", "g", "global", "all", "join", "bingostats", "bingodata"}; !reflect.DeepEqual(root.order, want) {
		t.Errorf("commands %q, want %q", root.order, want)
	}
	// The help of a command whose subcommands are only named is asked for,
	// and so on down, but not of one whose usage is all known already.
	for _, want := range []string{"help", "help bingo", "help bingo card", "help bingo card stack", "help bingo options play_to", "help bingostats reset"} {
		if !contains(asked, want) {
			t.Errorf("did not ask %q", want)
		}
	}
	for _, not := range []string{"help g", "help join", "help bingo reroll", "help tp", "help execute"} {
		if contains(asked, not) {
			t.Errorf("asked %q", not)
		}
	}
	difficulty := root.children["bingo"].children["difficulty"]
	if difficulty.Executable || difficulty.children["hard"] == nil || !difficulty.children["hard"].Executable || difficulty.children["S"].Type != "argument" {
		t.Errorf("bingo difficulty: %s", must(difficulty.MarshalJSON()))
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// On Paper, help is Bukkit's: the game's own is minecraft:help.
func TestReadHelpOfAPaperServer(t *testing.T) {
	var asked []string
	root, err := readHelp(context.Background(), answering(t, helpRecording(t, "paper"), &asked), vanillaNames(t))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(asked, []string{"help", "minecraft:help"}) {
		t.Errorf("asked %q", asked)
	}
	for _, name := range []string{"luckperms", "lp", "perms", "spark", "plugins", "tps", "mspt"} {
		n := root.children[name]
		if n == nil || !n.Executable || n.children["args"] == nil || n.children["args"].Type != "argument" {
			t.Errorf("%s: %+v", name, n)
		}
	}
	// The game's own, and namespaced names of what is there without, are
	// left out.
	for _, name := range []string{"give", "help", "reload", "minecraft:give", "luckperms:lp", "bukkit:plugins", "paper:spark"} {
		if root.children[name] != nil {
			t.Errorf("%s is in", name)
		}
	}

	// Where the game's own help says nothing, the names on Bukkit's pages.
	rec := helpRecording(t, "paper")
	page1 := strings.Replace(rec["help"], "(1/21)", "(1/2)", 1)
	page2 := "§e--------- §fHelp: Index (2/2) §e--------------------\n§f§6/banlist: §fA Mojang provided command.\n§f§6/callback: §fClickEvent callback\n§f§6/luckperms: §fManage permissions\n"
	asked = nil
	root, err = readHelp(context.Background(), answering(t, map[string]string{
		"help": page1, "minecraft:help": "Unknown command. Type \"/help\" for help.", "help 2": page2,
	}, &asked), vanillaNames(t))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(root.order, []string{"callback", "luckperms"}) || !root.children["luckperms"].Executable || len(root.children["luckperms"].order) != 0 {
		t.Errorf("from Bukkit's pages: %q", root.order)
	}
	if !reflect.DeepEqual(asked, []string{"help", "minecraft:help", "help 2"}) {
		t.Errorf("asked %q", asked)
	}
}

func TestReadHelpBounds(t *testing.T) {
	// A server whose help never ends asks no more than so many times.
	var asked []string
	run := func(command string) (string, error) {
		asked = append(asked, command)
		path := strings.TrimPrefix(strings.TrimPrefix(command, "help"), " ")
		if path == "" {
			return "/deep (a|b)", nil
		}
		return "/" + path + " a (a|b)/" + path + " b (a|b)", nil
	}
	if _, err := readHelp(context.Background(), run, nil); err != nil {
		t.Fatal(err)
	}
	if len(asked) > modsCalls {
		t.Errorf("asked %d times", len(asked))
	}
	for _, c := range asked {
		if len(strings.Fields(c))-1 > modsDepth {
			t.Errorf("asked %q, deeper than %d", c, modsDepth)
		}
	}
	// An error ends it, with what was read before.
	asked = nil
	root, err := readHelp(context.Background(), func(command string) (string, error) {
		if command == "help" {
			return "/foo (a|b)/bar <x>", nil
		}
		return "", errors.New("connection reset")
	}, nil)
	if err == nil || root.children["bar"] == nil {
		t.Errorf("after an error: %v %v", err, root.order)
	}
	// A game's messages are no commands.
	for _, out := range []string{
		"Unknown command or insufficient permissions",
		`Unknown command. Type "/help" for help.`,
		"Unknown or incomplete command, see below for error/help<--[HERE]",
		"",
	} {
		if root, err := readHelp(context.Background(), func(string) (string, error) { return out, nil }, nil); err != nil || len(root.order) != 0 {
			t.Errorf("%q read as %q", out, root.order)
		}
	}
	// Nothing but help is run, whatever a name is.
	if helpCommand("say hi") || helpCommand("helpme") || helpCommand("help a\nstop") || !helpCommand("minecraft:help bingo card") || !helpCommand("help 2") {
		t.Error("helpCommand")
	}
}

func TestMergeCommands(t *testing.T) {
	raw := commandsFixture(t)
	gz, _ := packCommands(raw)
	mods := &usageNode{Type: "root"}
	for _, l := range []string{"time (warp|stop)", "bingo [card|book]", "g <message>"} {
		u, _ := parseUsage(l)
		mods.add(u)
	}
	mods.finish()
	merged, err := mergeCommands(gz, mods)
	if err != nil {
		t.Fatal(err)
	}
	out, _ := gunzip(merged)
	var tree, vanilla struct {
		Type     string                     `json:"type"`
		Children map[string]json.RawMessage `json:"children"`
	}
	_ = json.Unmarshal(out, &tree)
	_ = json.Unmarshal(raw, &vanilla)
	if tree.Type != "root" || len(tree.Children) != len(vanilla.Children)+2 {
		t.Fatalf("%d commands, want %d", len(tree.Children), len(vanilla.Children)+2)
	}
	// The game's own stays as the game has it.
	var a, b any
	_ = json.Unmarshal(tree.Children["time"], &a)
	_ = json.Unmarshal(vanilla.Children["time"], &b)
	if !reflect.DeepEqual(a, b) {
		t.Errorf("time changed: %s", tree.Children["time"])
	}
	if got := string(tree.Children["bingo"]); got != `{"type":"literal","children":{"card":{"type":"literal","executable":true},"book":{"type":"literal","executable":true}},"executable":true}` {
		t.Errorf("bingo: %s", got)
	}
	if got := string(tree.Children["g"]); got != `{"type":"literal","children":{"message":{"type":"argument","executable":true}}}` {
		t.Errorf("g: %s", got)
	}
	// In the game's order, then the server's.
	if i, j := bytes.Index(out, []byte(`"advancement"`)), bytes.Index(out, []byte(`"bingo"`)); i < 0 || j < i {
		t.Error("order")
	}
}

// helpServer is a running server's game answering help from a recording,
// counting what it is asked.
type helpServer struct {
	mu    sync.Mutex
	rec   map[string]string
	asked int
	fail  bool
	hold  chan struct{}
}

func (h *helpServer) command(_ context.Context, _ *avsysv1.MinecraftServer, command string) (string, error) {
	if h.hold != nil {
		<-h.hold
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.asked++
	if h.fail {
		return "", errors.New("connection refused")
	}
	return h.rec[command], nil
}

func (h *helpServer) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.asked
}

// commandNames are the commands in an answer.
func commandNames(t *testing.T, body []byte) map[string]json.RawMessage {
	t.Helper()
	var tree struct {
		Children map[string]json.RawMessage `json:"children"`
	}
	if err := json.Unmarshal(body, &tree); err != nil {
		t.Fatal(err)
	}
	return tree.Children
}

// gamePod stands in for the pod of the game, started for the nth time.
func gamePod(t *testing.T, p *panel, restarts int32) {
	t.Helper()
	ctx := context.Background()
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: p.srv.Namespace("alpha"), Name: "world"}}
	if err := p.srv.Client.Get(ctx, client.ObjectKeyFromObject(pod), pod); err != nil {
		pod.Spec.Containers = []corev1.Container{{Name: controller.MinecraftContainer, Image: "x"}}
		if err := p.srv.Client.Create(ctx, pod); err != nil {
			t.Fatal(err)
		}
	}
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: controller.MinecraftContainer, RestartCount: restarts, ContainerID: "containerd://x"}}
	if err := p.srv.Client.Status().Update(ctx, pod); err != nil {
		t.Fatal(err)
	}
}

func TestServerCommandsAreReadOncePerRun(t *testing.T) {
	p := newPanel(t)
	raw := commandsFixture(t)
	p.srv.CommandTree = func(context.Context, string) ([]byte, error) { return raw, nil }
	game := &helpServer{rec: helpRecording(t, "bingo")}
	p.srv.GameCommand = game.command

	// Stopped: the game's own commands, and nothing asked.
	stopped := waitCommandTree(p, nil)
	if stopped.Code != http.StatusOK || commandNames(t, stopped.Body.Bytes())["bingo"] != nil || game.count() != 0 {
		t.Fatalf("stopped: %d, asked %d", stopped.Code, game.count())
	}
	vanillaTag := stopped.Header().Get("ETag")

	// Running: with the server's own, read once for this run.
	p.setPhase("alpha", "world", avsysv1.PhaseRunning)
	gamePod(t, p, 0)
	rec := waitCommandTree(p, nil)
	names := commandNames(t, rec.Body.Bytes())
	if rec.Code != http.StatusOK || names["bingo"] == nil || names["bingostats"] == nil || names["time"] == nil {
		t.Fatalf("running: %d, %d commands", rec.Code, len(names))
	}
	if !bytes.Contains(names["bingo"], []byte(`"difficulty":{"type":"literal","children":{"S":{"type":"argument"`)) {
		t.Errorf("bingo: %.300s", names["bingo"])
	}
	tag := rec.Header().Get("ETag")
	if tag == vanillaTag {
		t.Error("the same tag as the game's own")
	}
	asked := game.count()
	if again := getCommandTree(p, "operator", http.Header{"If-None-Match": {tag}}); again.Code != http.StatusNotModified || game.count() != asked {
		t.Errorf("asked again: %d, asked %d more", again.Code, game.count()-asked)
	}

	// Started again: read afresh.
	gamePod(t, p, 1)
	if rec := waitCommandTree(p, nil); rec.Code != http.StatusOK || game.count() != 2*asked {
		t.Errorf("after a restart: %d, asked %d, want %d", rec.Code, game.count(), 2*asked)
	}
}

func TestServerCommandsWhenTheGameDoesNotAnswer(t *testing.T) {
	p := newPanel(t)
	raw := commandsFixture(t)
	p.srv.CommandTree = func(context.Context, string) ([]byte, error) { return raw, nil }
	game := &helpServer{rec: helpRecording(t, "bingo"), fail: true}
	p.srv.GameCommand = game.command
	p.setPhase("alpha", "world", avsysv1.PhaseRunning)

	// The game's own commands, and asked again a while later.
	rec := waitCommandTree(p, nil)
	if rec.Code != http.StatusOK || commandNames(t, rec.Body.Bytes())["bingo"] != nil || game.count() != 1 {
		t.Fatalf("not answering: %d, asked %d", rec.Code, game.count())
	}
	getCommandTree(p, "operator", nil)
	if game.count() != 1 {
		t.Errorf("asked again at once")
	}
	game.mu.Lock()
	game.fail = false
	game.mu.Unlock()
	p.srv.mods.mu.Lock()
	for _, m := range p.srv.mods.by {
		if d := time.Until(m.retry); d < modsRetry-time.Minute || d > modsRetry {
			t.Errorf("asked again in %v", d)
		}
		m.retry = time.Now().Add(-time.Second)
	}
	p.srv.mods.mu.Unlock()
	if rec := waitCommandTree(p, nil); commandNames(t, rec.Body.Bytes())["bingo"] == nil {
		t.Error("not read once the game answered")
	}

	// Slow to answer: asked to ask again, then there.
	q := newPanel(t)
	q.srv.CommandTree = p.srv.CommandTree
	slow := &helpServer{rec: helpRecording(t, "bingo"), hold: make(chan struct{})}
	q.srv.GameCommand = slow.command
	q.setPhase("alpha", "world", avsysv1.PhaseRunning)
	defer func(w time.Duration) { modsWait = w }(modsWait)
	modsWait = 50 * time.Millisecond
	for i := 0; i < 100; i++ {
		rec = getCommandTree(q, "operator", nil)
		if rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), `"making":true`) {
			break
		}
		// The tree is made first; then the help is waited for.
		q.srv.commands.mu.Lock()
		made := q.srv.commands.trees["1.21.4"] != nil
		q.srv.commands.mu.Unlock()
		if made {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if rec = getCommandTree(q, "operator", nil); rec.Code != http.StatusAccepted {
		t.Errorf("while the help is read: %d", rec.Code)
	}
	close(slow.hold)
	if rec := waitCommandTree(q, nil); rec.Code != http.StatusOK || commandNames(t, rec.Body.Bytes())["bingo"] == nil {
		t.Errorf("once read: %d", rec.Code)
	}
}
