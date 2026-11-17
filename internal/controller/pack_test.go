package controller

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
)

// antMatcher is the image's own reading of MODRINTH_OVERRIDES_EXCLUSIONS
// (mc-image-helper's AntPathMatcher): each pattern, split at commas, made a
// regular expression, all of them one alternation the whole path must match.
func antMatcher(patterns string) *regexp.Regexp {
	var alts []string
	for _, p := range strings.Split(patterns, ",") {
		p = strings.ReplaceAll(p, "**", "_DSTAR_")
		p = strings.ReplaceAll(p, "*", "_STAR_")
		p = regexp.MustCompile(`[.(\[]`).ReplaceAllString(p, `\$0`)
		p = strings.ReplaceAll(p, "?", ".")
		p = strings.ReplaceAll(p, "_DSTAR_", ".*?")
		p = strings.ReplaceAll(p, "_STAR_", "[^/]*?")
		alts = append(alts, p)
	}
	return regexp.MustCompile("^(?:" + strings.Join(alts, "|") + ")$")
}

// packStarted runs packScript as the game's container runs it, with dir as
// its /data, and returns what the image would be given as
// MODRINTH_OVERRIDES_EXCLUSIONS, "unset" for none.
func packStarted(t *testing.T, dir string, env ...string) string {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("no jq")
	}
	script := strings.ReplaceAll(packScript(&avsysv1.ModpackSpec{Source: avsysv1.SourceModrinth}), "/data/", dir+"/")
	script = strings.ReplaceAll(script, "exec "+imageStart, `exec sh -c 'printf "EXCLUDED<%s>\n" "${MODRINTH_OVERRIDES_EXCLUSIONS-unset}"'`)
	cmd := exec.Command("sh", "-c", script)
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH")}, env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	m := regexp.MustCompile(`(?s)EXCLUDED<(.*?)>`).FindStringSubmatch(string(out))
	if m == nil {
		t.Fatalf("the image was not started: %s", out)
	}
	return m[1]
}

func manifestFiles(t *testing.T, file string) []string {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		Files []string `json:"files"`
	}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	return m.Files
}

// A server with no world takes a modpack's world as the image puts it in.
// Once it has one, chosen when it was made, put in since, or the pack's
// own from before, the pack's world files are neither put over it nor taken
// out of it as the pack changes. The pack's datapacks and per-world
// settings still are, and so is everything outside the world.
func TestPackLeavesTheWorld(t *testing.T) {
	t.Run("no world", func(t *testing.T) {
		dir := t.TempDir()
		if got := packStarted(t, dir); got != "unset" {
			t.Errorf("a server with no world has the pack's left out: %q", got)
		}
	})

	t.Run("a world", func(t *testing.T) {
		dir := t.TempDir()
		write(t, filepath.Join(dir, "world/level.dat"), "mine")
		write(t, filepath.Join(dir, "world/region/r.0.0.mca"), "mine")
		write(t, filepath.Join(dir, "world/ftbchunks/claims.json"), "mine")
		write(t, filepath.Join(dir, "world/datapacks/a.zip"), "pack")
		manifest := filepath.Join(dir, ".modrinth-modpack-manifest.json")
		write(t, manifest, `{"@type":"modrinth-modpack","timestamp":"2026-01-01T00:00:00Z","files":["mods/a.jar","config/a.toml","world/level.dat","world/region/r.0.0.mca","world/datapacks/a.zip","world/serverconfig/b.toml","world_nether/DIM-1/region/r.0.0.mca","world_the_end/level.dat","worldedit/config.yml"],"projectSlug":"pack","versionId":"v1"}`)
		got := packStarted(t, dir)
		if got == "unset" {
			t.Fatal("the pack's world files are put over the server's world")
		}
		left := antMatcher(got)
		for _, p := range []string{"world/level.dat", "world/session.lock", "world/region/r.0.0.mca", "world/entities/r.0.0.mca", "world/playerdata/x.dat", "world/DIM-1/region/r.0.0.mca", "world/ftbchunks/claims.json", "world_nether/DIM-1/region/r.0.0.mca", "world_the_end/level.dat"} {
			if !left.MatchString(p) {
				t.Errorf("%s from the pack is put over the server's world", p)
			}
		}
		for _, p := range []string{"world/datapacks/a.zip", "world/serverconfig/b.toml", "mods/a.jar", "config/a.toml", "server.properties", "worldedit/config.yml", "world_extra/x"} {
			if left.MatchString(p) {
				t.Errorf("%s from the pack is left out", p)
			}
		}
		// Taken off the record, they are not taken out of the world when the
		// pack's next version does not have them.
		want := []string{"mods/a.jar", "config/a.toml", "world/datapacks/a.zip", "world/serverconfig/b.toml", "worldedit/config.yml"}
		if files := manifestFiles(t, manifest); !slices.Equal(files, want) {
			t.Errorf("the record lists %q, want %q", files, want)
		}
		var rest map[string]any
		data, _ := os.ReadFile(manifest)
		_ = json.Unmarshal(data, &rest)
		if rest["projectSlug"] != "pack" || rest["versionId"] != "v1" || rest["@type"] != "modrinth-modpack" {
			t.Errorf("the rest of the record changed: %s", data)
		}
	})

	t.Run("another level", func(t *testing.T) {
		dir := t.TempDir()
		write(t, filepath.Join(dir, "lobby/level.dat"), "mine")
		got := packStarted(t, dir, "LEVEL=lobby")
		if left := antMatcher(got); got == "unset" || !left.MatchString("lobby/level.dat") || left.MatchString("world/level.dat") {
			t.Errorf("the world of LEVEL=lobby: %q", got)
		}
	})

	t.Run("its own exclusions", func(t *testing.T) {
		dir := t.TempDir()
		write(t, filepath.Join(dir, "world/level.dat"), "mine")
		got := packStarted(t, dir, "MODRINTH_OVERRIDES_EXCLUSIONS=mods/client-*.jar")
		if left := antMatcher(got); !left.MatchString("mods/client-a.jar") || !left.MatchString("world/level.dat") {
			t.Errorf("the server's own exclusions are not kept beside the world's: %q", got)
		}
	})

	t.Run("a record it cannot read", func(t *testing.T) {
		dir := t.TempDir()
		write(t, filepath.Join(dir, "world/level.dat"), "mine")
		manifest := filepath.Join(dir, ".modrinth-modpack-manifest.json")
		write(t, manifest, "not json")
		// Left out without the record changed, the image would take what it
		// lists out of the world: it starts as it does by itself instead.
		if got := packStarted(t, dir); got != "unset" {
			t.Errorf("with a record it cannot change, the pack's world files are left out: %q", got)
		}
		if data, _ := os.ReadFile(manifest); string(data) != "not json" {
			t.Errorf("the record was changed: %q", data)
		}
	})
}
