package api

import (
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestArchiveRoot(t *testing.T) {
	for in, want := range map[string]string{
		"":             "",
		"MyServer":     "MyServer",
		"MyServer/":    "MyServer",
		"a/b c/server": "a/b c/server",
		"/MyServer":    "MyServer",
	} {
		if got, err := archiveRoot(in); err != nil || got != want {
			t.Errorf("archiveRoot(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"..", "../x", "a/../../b", "a/./b", "a//b", "a\nb"} {
		if got, err := archiveRoot(in); err == nil {
			t.Errorf("archiveRoot(%q) = %q, want it refused", in, got)
		}
	}
}

// An imported server's lists of players keep only those still named in its
// settings, each as its entry was: an operator keeps their level.
func TestKeepListed(t *testing.T) {
	sc := memFiles(t)
	if err := sc.MkdirAll("/data"); err != nil {
		t.Fatal(err)
	}
	write := func(name, data string) {
		f, err := sc.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte(data)); err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
	read := func(name string) string {
		f, err := sc.Open(name)
		if err != nil {
			return "gone"
		}
		defer f.Close()
		b, _ := io.ReadAll(f)
		return string(b)
	}
	write("/data/ops.json", `[{"uuid":"853c80ef-3c37-49fd-aa49-938b674adae6","name":"jeb_","level":1,"bypassesPlayerLimit":true},{"uuid":"069a79f4-44e9-4726-a5be-fca90e38aaf5","name":"Notch","level":4}]`)
	if err := keepListed(sc, "/data/ops.json", []string{"JEB_"}); err != nil {
		t.Fatal(err)
	}
	var ops []map[string]any
	if err := json.Unmarshal([]byte(read("/data/ops.json")), &ops); err != nil || len(ops) != 1 || ops[0]["name"] != "jeb_" || ops[0]["level"] != float64(1) || ops[0]["bypassesPlayerLimit"] != true {
		t.Errorf("ops kept: %s", read("/data/ops.json"))
	}
	// By UUID, with or without its dashes.
	write("/data/whitelist.json", `[{"uuid":"069a79f4-44e9-4726-a5be-fca90e38aaf5","name":"Notch"},{"uuid":"853c80ef-3c37-49fd-aa49-938b674adae6","name":"jeb_"}]`)
	if err := keepListed(sc, "/data/whitelist.json", []string{"069a79f444e94726a5befca90e38aaf5"}); err != nil {
		t.Fatal(err)
	}
	if got := read("/data/whitelist.json"); !strings.Contains(got, "Notch") || strings.Contains(got, "jeb_") {
		t.Errorf("whitelist kept: %s", got)
	}
	// Everyone still named: left as it is, byte for byte.
	write("/data/whitelist.json", `[{"name":"Notch"}]`)
	if err := keepListed(sc, "/data/whitelist.json", []string{"Notch"}); err != nil || read("/data/whitelist.json") != `[{"name":"Notch"}]` {
		t.Errorf("rewritten when nothing changed: %v %s", err, read("/data/whitelist.json"))
	}
	// One that is not a list goes; none at all is fine.
	write("/data/ops.json", `not json`)
	if err := keepListed(sc, "/data/ops.json", nil); err != nil || read("/data/ops.json") != "gone" {
		t.Errorf("unreadable list: %v %s", err, read("/data/ops.json"))
	}
	if err := keepListed(sc, "/data/none.json", nil); err != nil {
		t.Errorf("no list: %v", err)
	}
}

// A world given to a server that has its files but no world yet, as one
// copied with a new world, goes in beside those files, and the datapacks it
// had stay, with the world's own where their names are not taken.
func TestWorldFillsAServerWithNone(t *testing.T) {
	for _, tool := range []string{"bsdtar", "sh"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed", tool)
		}
	}
	root := t.TempDir()
	data := filepath.Join(root, "data")
	write := func(path, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(data, "mods", "a.jar"), "a mod")
	write(filepath.Join(data, "server.properties"), "motd=hi\n")
	write(filepath.Join(data, "world", "datapacks", "kept.zip"), "the server's")
	write(filepath.Join(data, "world", "datapacks", "both.zip"), "the server's")
	write(filepath.Join(data, "world_nether", "DIM-1", "old"), "an old nether")
	src := filepath.Join(root, "src")
	write(filepath.Join(src, "MyWorld", "level.dat"), "my world")
	write(filepath.Join(src, "MyWorld", "datapacks", "both.zip"), "the world's")
	write(filepath.Join(src, "MyWorld", "datapacks", "theirs.zip"), "the world's")
	if err := os.MkdirAll(filepath.Join(data, ".avsys"), 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("bsdtar", "-cf", filepath.Join(data, ".avsys", "import.archive"), "-C", src, "MyWorld").CombinedOutput(); err != nil {
		t.Fatalf("bsdtar: %v: %s", err, out)
	}
	// The server directory, and not the start of a name such as datapacks.
	script := regexp.MustCompile(regexp.QuoteMeta(dataRoot)+`\b`).ReplaceAllString(worldScript, data)
	cmd := exec.Command("sh", "-c", script)
	cmd.Stdin = strings.NewReader("MyWorld\n\n\nfill\nworld\n")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("script: %v: %s", err, out)
	}
	for path, want := range map[string]string{
		"world/level.dat":            "my world",
		"world/datapacks/kept.zip":   "the server's",
		"world/datapacks/both.zip":   "the world's",
		"world/datapacks/theirs.zip": "the world's",
		"mods/a.jar":                 "a mod",
		"server.properties":          "motd=hi\n",
	} {
		got, err := os.ReadFile(filepath.Join(data, path))
		if err != nil || string(got) != want {
			t.Errorf("%s: %q, %v; want %q", path, got, err, want)
		}
	}
	if _, err := os.Stat(filepath.Join(data, "world_nether")); !os.IsNotExist(err) {
		t.Errorf("the old Nether is still there: %v", err)
	}
	if left, _ := filepath.Glob(filepath.Join(data, ".avsys", "import*")); len(left) > 0 {
		t.Errorf("left behind: %v", left)
	}
}
