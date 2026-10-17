package api

import (
	"context"
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/client"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
)

func TestDataPathStaysInsideServerDirectory(t *testing.T) {
	for in, want := range map[string]string{
		"":                            "/data",
		"/":                           "/data",
		"config/paper.yml":            "/data/config/paper.yml",
		"/world/../server.properties": "/data/server.properties",
		"../../etc/passwd":            "/data/etc/passwd",
		"/../..":                      "/data",
	} {
		got, err := dataPath(in)
		if err != nil || got != want {
			t.Errorf("dataPath(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}

func TestSafeNext(t *testing.T) {
	for in, want := range map[string]string{
		"/o/friends":           "/o/friends",
		"":                     "/",
		"https://evil.example": "/",
		"//evil.example":       "/",
		"/\\evil.example":      "/",
	} {
		if got := safeNext(in); got != want {
			t.Errorf("safeNext(%q) = %q, want %q", in, got, want)
		}
	}
}

// Properties are handed to the server as name=value lines, so an entry that
// spans lines would set a second property the caller never listed.
func TestValidateProperties(t *testing.T) {
	for _, ok := range []map[string]string{
		nil,
		{"difficulty": "hard", "level-seed": "-4172144997902289642", "motd": "Hello = world"},
		{"level-type": "flat", "generate-structures": "false", "query.port": "25565"},
	} {
		if err := validateProperties(ok); err != nil {
			t.Errorf("validateProperties(%v) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []map[string]string{
		{"motd": "hi\nonline-mode=false"},
		{"motd": "hi\ronline-mode=false"},
		{"motd\nonline-mode": "false"},
		{"": "x"},
		{"enable rcon": "true"},
		{"a=b": "c"},
	} {
		if err := validateProperties(bad); err == nil {
			t.Errorf("validateProperties(%q) = nil, want an error", bad)
		}
	}
}

// What is sent to a console is one command, from the server or over RCON.
func TestConsoleCommandsAreOneLine(t *testing.T) {
	p := newPanel(t)
	const console = "/orgs/alpha/servers/world/console"
	for name, body := range map[string]string{
		"two commands in one": `{"command":"say hi\nop Steve","as":"server"}`,
		"a carriage return":   `{"command":"say hi\rop Steve"}`,
		"an unknown sender":   `{"command":"list","as":"Steve"}`,
		"nothing":             `{"command":"  "}`,
	} {
		if got, code := p.do("operator", "POST", console, body); got != 400 || code != "invalid" {
			t.Errorf("%s: got %d %q, want 400 invalid", name, got, code)
		}
	}
	// Well formed, it gets as far as the server not running.
	for _, body := range []string{`{"command":"list"}`, `{"command":"/say hi","as":"server"}`} {
		if got, code := p.do("operator", "POST", console, body); got != 409 || code != "not_running" {
			t.Errorf("%s: got %d %q, want 409 not_running", body, got, code)
		}
	}
}

func TestConsoleTurnsArePerServer(t *testing.T) {
	var s Server
	a := s.consoleTurn("mc-alpha/world")
	if s.consoleTurn("mc-alpha/world") != a {
		t.Error("two writers to one console were given different locks")
	}
	if s.consoleTurn("mc-alpha/other") == a {
		t.Error("two servers share a console lock")
	}
}

// While a snapshot holds the game's saves, the panel's console does not let
// them be turned back on or written out under it. Anything else goes through.
func TestConsoleKeepsSavesHeldForASnapshot(t *testing.T) {
	p := newPanel(t)
	const console = "/orgs/alpha/servers/world/console"
	p.setPhase("alpha", "world", avsysv1.PhaseRunning)
	mc := &avsysv1.MinecraftServer{}
	if err := p.srv.Client.Get(context.Background(), client.ObjectKey{Namespace: "mc-alpha", Name: "world"}, mc); err != nil {
		t.Fatal(err)
	}
	mc.Annotations = map[string]string{avsysv1.AnnotationSavingPaused: "now"}
	if err := p.srv.Client.Update(context.Background(), mc); err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"save-on", "/save-off", "save-all flush", "minecraft:save-all", "SAVE-ON"} {
		if got, code := p.do("operator", "POST", console, `{"command":"`+command+`"}`); got != 409 || code != "snapshotting" {
			t.Errorf("%s: got %d %q, want 409 snapshotting", command, got, code)
		}
	}
	// Any other command goes through: only the game's own for saving.
	for _, command := range []string{"list", "say save-on", "save-onwards", "op save-all"} {
		if savesCommand(command) {
			t.Errorf("%s is taken for a command for saving", command)
		}
	}
}
