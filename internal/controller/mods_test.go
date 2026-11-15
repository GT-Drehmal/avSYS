package controller

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/yaml"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
)

func modrinth(project string) avsysv1.ContentRef {
	return avsysv1.ContentRef{Source: avsysv1.SourceModrinth, Kind: avsysv1.KindPlugin, Project: project}
}

var (
	geyserUDP = avsysv1.ExtraPort{Name: "geyser", Port: 19132, Protocol: corev1.ProtocolUDP, For: "geyser"}
	geyserTCP = avsysv1.ExtraPort{Name: "geyser-tcp", Port: 19132, Protocol: corev1.ProtocolTCP, For: "geyser"}
	voicePort = avsysv1.ExtraPort{Name: "voice-chat", Port: 24454, Protocol: corev1.ProtocolUDP, For: "simple-voice-chat"}
	votifier  = avsysv1.ExtraPort{Name: "votifier", Port: 8192, Protocol: corev1.ProtocolTCP}
)

func TestFitModPorts(t *testing.T) {
	geyser, chat, lithium := modrinth("geyser"), modrinth("simple-voice-chat"), modrinth("lithium")
	eight := func(extra ...avsysv1.ExtraPort) []avsysv1.ExtraPort {
		var out []avsysv1.ExtraPort
		for i := range 7 {
			out = append(out, avsysv1.ExtraPort{Name: "p" + string(rune('a'+i)), Port: int32(1000 + i), Protocol: corev1.ProtocolTCP})
		}
		return append(out, extra...)
	}
	for _, c := range []struct {
		name          string
		before, after []avsysv1.ContentRef
		ports, want   []avsysv1.ExtraPort
	}{
		{"added", nil, []avsysv1.ContentRef{lithium, geyser}, []avsysv1.ExtraPort{votifier}, []avsysv1.ExtraPort{votifier, geyserUDP, geyserTCP}},
		{"by its project ID, as an import lists it", nil, []avsysv1.ContentRef{modrinth("wKkoqHrH")}, nil, []avsysv1.ExtraPort{geyserUDP, geyserTCP}},
		{"not from Modrinth", nil, []avsysv1.ContentRef{{Source: avsysv1.SourceURL, Project: "geyser"}}, nil, nil},
		{"taken off", []avsysv1.ContentRef{geyser, chat}, []avsysv1.ContentRef{chat}, []avsysv1.ExtraPort{geyserUDP, votifier, voicePort, geyserTCP}, []avsysv1.ExtraPort{votifier, voicePort}},
		{"there before and after, its port closed by hand", []avsysv1.ContentRef{geyser}, []avsysv1.ContentRef{geyser, lithium}, []avsysv1.ExtraPort{votifier}, []avsysv1.ExtraPort{votifier}},
		{"a port of its own by the name", nil, []avsysv1.ContentRef{geyser}, []avsysv1.ExtraPort{{Name: "geyser", Port: 19133, Protocol: corev1.ProtocolUDP}}, []avsysv1.ExtraPort{{Name: "geyser", Port: 19133, Protocol: corev1.ProtocolUDP}, geyserTCP}},
		{"a port of its own by the number", nil, []avsysv1.ContentRef{chat}, []avsysv1.ExtraPort{{Name: "voice", Port: 24454, Protocol: corev1.ProtocolUDP}}, []avsysv1.ExtraPort{{Name: "voice", Port: 24454, Protocol: corev1.ProtocolUDP}}},
		{"one of its own kept when the mod goes", []avsysv1.ContentRef{geyser}, nil, []avsysv1.ExtraPort{{Name: "geyser", Port: 19133, Protocol: corev1.ProtocolUDP}}, []avsysv1.ExtraPort{{Name: "geyser", Port: 19133, Protocol: corev1.ProtocolUDP}}},
		{"as many as it may have", nil, []avsysv1.ContentRef{geyser}, eight(), eight(geyserUDP)},
		{"its port changed by hand goes with it", []avsysv1.ContentRef{chat}, nil, []avsysv1.ExtraPort{{Name: "voice-chat", Port: 24455, Protocol: corev1.ProtocolUDP, For: "simple-voice-chat"}}, nil},
	} {
		if got := FitModPorts(c.before, c.after, c.ports); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
	}
	// One that comes back as it was in a snapshot has the ports it had there.
	moved := avsysv1.ExtraPort{Name: "voice-chat", Port: 24455, Protocol: corev1.ProtocolUDP, For: "simple-voice-chat"}
	if got := FitModPortsLike(nil, []avsysv1.ContentRef{chat}, nil, []avsysv1.ExtraPort{votifier, moved}); !reflect.DeepEqual(got, []avsysv1.ExtraPort{moved}) {
		t.Errorf("like a snapshot: %+v", got)
	}
}

func podEnv(pod *corev1.Pod, name string) (string, bool) {
	for _, e := range gameContainer(pod).Env {
		if e.Name == name {
			return e.Value, true
		}
	}
	return "", false
}

// The start script is told which ports were opened for mods, and where
// players reach them; a server with none is told nothing, and its pod is
// as it was.
func TestModPortsEnv(t *testing.T) {
	own := server(func(mc *avsysv1.MinecraftServer) {
		mc.Spec.Loader = avsysv1.LoaderPaper
		mc.Spec.Ports = []avsysv1.ExtraPort{votifier}
	})
	if v, ok := podEnv(buildPod(own, Config{}), "AVSYS_MOD_PORTS"); ok {
		t.Errorf("ports of its own: %q", v)
	}
	mc := server(func(mc *avsysv1.MinecraftServer) {
		mc.Spec.Loader = avsysv1.LoaderPaper
		mc.Spec.Mods = []avsysv1.ContentRef{modrinth("geyser"), modrinth("simple-voice-chat")}
		mc.Spec.Ports = []avsysv1.ExtraPort{votifier, geyserUDP, geyserTCP, voicePort}
	})
	before := buildPod(mc, Config{})
	if v, _ := podEnv(before, "AVSYS_MOD_PORTS"); v != "geyser 19132 TCP 0 -\ngeyser 19132 UDP 0 -\nsimple-voice-chat 24454 UDP 0 -" {
		t.Errorf("not reached yet: %q", v)
	}
	mc.Status.Ports = []avsysv1.PortStatus{
		{Name: "geyser", Port: 19132, Protocol: corev1.ProtocolUDP, PublicPort: 25601, Address: "play.example.org:25601"},
		{Name: "geyser-tcp", Port: 19132, Protocol: corev1.ProtocolTCP, PublicPort: 25601, Address: "play.example.org:25601"},
		// One that cannot be opened is not reached.
		{Name: "voice-chat", Port: 24454, Protocol: corev1.ProtocolUDP, Message: "Not open"},
	}
	after := buildPod(mc, Config{})
	if v, _ := podEnv(after, "AVSYS_MOD_PORTS"); v != "geyser 19132 TCP 25601 play.example.org:25601\ngeyser 19132 UDP 25601 play.example.org:25601\nsimple-voice-chat 24454 UDP 0 -" {
		t.Errorf("reached: %q", v)
	}
	// Where they are reached is a change the mods are set up for again.
	if before.Annotations[annotationSpecHash] == after.Annotations[annotationSpecHash] {
		t.Error("the hash leaves out where the ports are reached")
	}
	// A modpack's server sets them up too, where its loader keeps them, after
	// leaving the world alone as a Modrinth pack's start does without them.
	pack := mc.DeepCopy()
	pack.Spec.Modpack = &avsysv1.ModpackSpec{Source: avsysv1.SourceModrinth, Project: "pack", Loader: "neoforge"}
	if got := gameContainer(buildPod(pack, Config{})).Command; len(got) != 3 || !strings.HasPrefix(got[2], keepWorld+"kind=NEOFORGE\n"+modConfig) {
		t.Errorf("modpack: %q", got)
	}
	pack.Spec.Ports = []avsysv1.ExtraPort{votifier}
	if got := gameContainer(buildPod(pack, Config{})).Command; len(got) != 3 || got[2] != packScript {
		t.Errorf("modpack without: %q", got)
	}
	if !strings.Contains(startScript, modConfig) {
		t.Error("the start script does not set the mods up")
	}
	if got := modpackScript(&avsysv1.ModpackSpec{Loader: "fabric$(reboot)"}); !strings.HasPrefix(got, keepWorld+"kind=FABRICREBOOT\n") {
		t.Errorf("a loader's name is put in the script as letters alone: %q", strings.TrimPrefix(got, keepWorld)[:30])
	}
}

// Geyser's config is where its loader keeps it.
func TestModConfigByLoader(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	for kind, want := range map[string]string{
		"PAPER": "plugins/Geyser-Spigot/config.yml", "PURPUR": "plugins/Geyser-Spigot/config.yml", "FOLIA": "plugins/Geyser-Spigot/config.yml",
		"FABRIC": "config/Geyser-Fabric/config.yml", "QUILT": "config/Geyser-Fabric/config.yml", "NEOFORGE": "config/Geyser-NeoForge/config.yml",
	} {
		data := t.TempDir()
		runModConfig(t, data, kind, "geyser 19133 UDP 0 -")
		if got := read(t, filepath.Join(data, want)); got != "bedrock:\n  port: 19133\n" {
			t.Errorf("%s: %q", kind, got)
		}
	}
}

// runModConfig runs the start script's setting up of mods, as the game's
// container runs it, on a copy of these files as the mods wrote them.
func runModConfig(t *testing.T, data, kind, ports string) string {
	t.Helper()
	script := "kind=" + kind + "\n" + strings.ReplaceAll(modConfig, "to=/data/$2", "to="+data+"/$2")
	script = strings.ReplaceAll(script, `"/data/$f"`, `"`+data+`/$f"`)
	cmd := exec.Command("sh", "-c", script)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "AVSYS_MOD_PORTS=" + ports, "EULA=true"}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	return string(out)
}

func copyTree(t *testing.T, from, to string) {
	t.Helper()
	err := filepath.Walk(from, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(from, path)
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Join(to, filepath.Dir(rel)), 0o755); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(to, rel), b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// at is the value at a path of keys in a YAML file.
func at(t *testing.T, file, path string) any {
	t.Helper()
	var v any
	if err := yaml.Unmarshal([]byte(read(t, file)), &v); err != nil {
		t.Fatalf("%s is not YAML now: %v", file, err)
	}
	for _, k := range strings.Split(path, ".") {
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = m[k]
	}
	return v
}

// lines are a file's lines, counted.
func lines(s string) map[string]int {
	out := map[string]int{}
	for _, l := range strings.Split(s, "\n") {
		out[l]++
	}
	return out
}

// changedOnly checks that a file lost and gained only lines that match.
func changedOnly(t *testing.T, name, was, now string, may *regexp.Regexp) {
	t.Helper()
	a, b := lines(was), lines(now)
	for l, n := range a {
		if b[l] < n && !may.MatchString(l) {
			t.Errorf("%s: lost %q", name, l)
		}
	}
	for l, n := range b {
		if a[l] < n && !may.MatchString(l) {
			t.Errorf("%s: gained %q", name, l)
		}
	}
}

const allMods = "bluemap 8101 TCP 25604 play.example.org:25604\n" +
	"dynmap 8124 TCP 25605 play.example.org:25605\n" +
	"geyser 19133 TCP 25601 play.example.org:25601\n" +
	"geyser 19133 UDP 25601 play.example.org:25601\n" +
	"plasmo-voice 60606 UDP 25603 203.0.113.7:25603\n" +
	"simple-voice-chat 24455 UDP 25602 play.example.org:25602\n" +
	"squaremap 8081 TCP 25606 play.example.org:25606"

// The mods' own config files, as they wrote them on a first start, have
// what must match the panel set, and nothing else changed: hand edits
// stay. Run again, nothing changes.
func TestModConfigOnTheModsOwnFiles(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	for _, c := range []struct {
		layout, kind                                      string
		geyser, voice, plasmo, bluemap, dynmap, squaremap string
		newGeyser                                         bool
	}{
		{"plugins", "PAPER", "plugins/Geyser-Spigot/config.yml", "plugins/voicechat/voicechat-server.properties", "plugins/PlasmoVoice/config.toml", "plugins/BlueMap", "plugins/dynmap/configuration.txt", "plugins/squaremap/config.yml", true},
		{"mods", "FABRIC", "config/Geyser-Fabric/config.yml", "config/voicechat/voicechat-server.properties", "config/plasmovoice/server/config.toml", "config/bluemap", "dynmap/configuration.txt", "squaremap/config.yml", false},
	} {
		t.Run(c.kind, func(t *testing.T) {
			data := t.TempDir()
			src := filepath.Join("testdata", "mods", c.layout)
			copyTree(t, src, data)
			// A hand edit, which stays.
			g := filepath.Join(data, c.geyser)
			if err := os.WriteFile(g, []byte(strings.Replace(read(t, g), "Another Geyser server.", "Our club's server", 1)), 0o644); err != nil {
				t.Fatal(err)
			}
			out := runModConfig(t, data, c.kind, allMods)

			if got := at(t, g, "bedrock.port"); got != float64(19133) {
				t.Errorf("Geyser's port: %v", got)
			}
			if c.newGeyser {
				if got := at(t, g, "advanced.bedrock.broadcast-port"); got != float64(25601) {
					t.Errorf("Geyser's broadcast port: %v", got)
				}
				if got := at(t, g, "bedrock.signaling.advertise-addresses"); !reflect.DeepEqual(got, []any{"play.example.org:25601"}) {
					t.Errorf("Geyser's addresses: %v", got)
				}
			} else if got := at(t, g, "bedrock.broadcast-port"); got != float64(25601) {
				t.Errorf("Geyser's broadcast port: %v", got)
			}
			if !strings.Contains(read(t, g), "Our club's server") {
				t.Error("the hand edit to Geyser's config is gone")
			}
			if got := at(t, filepath.Join(data, c.squaremap), "settings.internal-webserver.port"); got != float64(8081) {
				t.Errorf("squaremap's port: %v", got)
			}
			if got := at(t, filepath.Join(data, c.squaremap), "settings.web-address"); got != "http://play.example.org:25606" {
				t.Errorf("squaremap's address: %v", got)
			}
			if got := at(t, filepath.Join(data, c.dynmap), "webserver-port"); got != float64(8124) {
				t.Errorf("Dynmap's port: %v", got)
			}
			if got := at(t, filepath.Join(data, c.dynmap), "publicURL"); got != "http://play.example.org:25605/" {
				t.Errorf("Dynmap's address: %v", got)
			}
			voice := read(t, filepath.Join(data, c.voice))
			for _, want := range []string{"\nport=24455\n", "\nvoice_host=play.example.org:25602\n"} {
				if !strings.Contains(voice, want) {
					t.Errorf("voice chat: no %q", want)
				}
			}
			plasmo := read(t, filepath.Join(data, c.plasmo))
			if !regexp.MustCompile(`(?s)\n\[host\]\n[^\[]*\nport = 60606\n`).MatchString(plasmo) ||
				!strings.HasSuffix(plasmo, "\n[host.public]\nip = \"203.0.113.7\"\nport = 25603\n") {
				t.Errorf("Plasmo Voice:\n%s", plasmo)
			}
			if !strings.Contains(read(t, filepath.Join(data, c.bluemap, "webserver.conf")), "\nport: 8101\n") ||
				!strings.Contains(read(t, filepath.Join(data, c.bluemap, "core.conf")), "\naccept-download: true\n") {
				t.Error("BlueMap")
			}

			// Nothing else.
			may := regexp.MustCompile(`^ *(#? ?(port|broadcast-port|webserver-port|publicURL|web-address|accept-download|voice_host|ip|advertise-addresses) *[:=]|\[host\.public\]$|- play\.example\.org|signaling:$|$)`)
			for _, f := range []string{c.geyser, c.voice, c.plasmo, c.bluemap + "/webserver.conf", c.bluemap + "/core.conf", c.dynmap, c.squaremap} {
				was := read(t, filepath.Join(src, f))
				if f == c.geyser {
					was = strings.Replace(was, "Another Geyser server.", "Our club's server", 1)
				}
				changedOnly(t, f, was, read(t, filepath.Join(data, f)), may)
			}
			if !strings.Contains(out, "[avsys] Geyser: bedrock.port set to 19133 in "+c.geyser) {
				t.Errorf("not said: %s", out)
			}

			// Again, as on the next start: nothing to do.
			was := map[string]string{}
			for _, f := range []string{c.geyser, c.voice, c.plasmo, c.dynmap, c.squaremap} {
				was[f] = read(t, filepath.Join(data, f))
			}
			if out := runModConfig(t, data, c.kind, allMods); out != "" {
				t.Errorf("again: %s", out)
			}
			for f, s := range was {
				if read(t, filepath.Join(data, f)) != s {
					t.Errorf("%s changed again", f)
				}
			}
		})
	}
}

// Before a mod's first start its file is made with only what must match
// the panel, which it fills in; Dynmap's is left for Dynmap to write whole.
func TestModConfigBeforeTheFirstStart(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	data := t.TempDir()
	runModConfig(t, data, "PAPER", allMods)
	for f, want := range map[string]string{
		"plugins/Geyser-Spigot/config.yml":              "bedrock:\n  port: 19133\n  broadcast-port: 25601\n  signaling:\n    advertise-addresses:\n      - play.example.org:25601\n",
		"plugins/voicechat/voicechat-server.properties": "port=24455\nvoice_host=play.example.org:25602\n",
		"plugins/PlasmoVoice/config.toml":               "[host]\nport = 60606\n\n[host.public]\nip = \"203.0.113.7\"\nport = 25603\n",
		"plugins/BlueMap/webserver.conf":                "port: 8101\n",
		"plugins/BlueMap/core.conf":                     "accept-download: true\n",
		"plugins/squaremap/config.yml":                  "settings:\n  internal-webserver:\n    port: 8081\n  web-address: http://play.example.org:25606\n",
	} {
		if got := read(t, filepath.Join(data, f)); got != want {
			t.Errorf("%s:\n%s\nwant:\n%s", f, got, want)
		}
	}
	if _, err := os.Stat(filepath.Join(data, "plugins/dynmap/configuration.txt")); !os.IsNotExist(err) {
		t.Errorf("Dynmap's file made: %v", err)
	}
	// Where they are reached not known yet, only the ports are set.
	data = t.TempDir()
	runModConfig(t, data, "FABRIC", "simple-voice-chat 24454 UDP 0 -\ngeyser 19132 UDP 0 -")
	if got := read(t, filepath.Join(data, "config/voicechat/voicechat-server.properties")); got != "port=24454\n" {
		t.Errorf("voice chat: %q", got)
	}
	if got := read(t, filepath.Join(data, "config/Geyser-Fabric/config.yml")); got != "bedrock:\n  port: 19132\n" {
		t.Errorf("Geyser: %q", got)
	}
}

// A value that cannot be set without rewriting what is around it, as a
// parent with a value on its own line, leaves the file as it is.
func TestModConfigLeavesWhatItCannotRead(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	data := t.TempDir()
	f := filepath.Join(data, "squaremap/config.yml")
	if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
		t.Fatal(err)
	}
	const flow = "settings: {internal-webserver: {port: 8080}}\n"
	if err := os.WriteFile(f, []byte(flow), 0o644); err != nil {
		t.Fatal(err)
	}
	runModConfig(t, data, "FABRIC", "squaremap 8081 TCP 0 -")
	if got := read(t, f); got != flow {
		t.Errorf("changed: %q", got)
	}
}
