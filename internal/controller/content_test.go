package controller

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
)

// The content of a server used here, and the files the image leaves for it
// once installed: Modrinth's record of the projects it was given and the
// files it got for them, a dependency among them; the record of the list of
// plugins from URLs and uploads; and the files themselves.
const (
	pluginURL   = "https://files.example.org/chunky/Chunky-1.4.jar?dl=1"
	datapackURL = "https://packs.example.org/v/Vein-1.2.zip"
)

func withContent(mc *avsysv1.MinecraftServer) {
	mc.Spec.Loader = avsysv1.LoaderPaper
	mc.Spec.Mods = []avsysv1.ContentRef{
		{Source: avsysv1.SourceModrinth, Kind: avsysv1.KindPlugin, Project: "luckperms", Version: "lp1"},
		{Source: avsysv1.SourceModrinth, Kind: avsysv1.KindPlugin, Project: "worldedit", Version: "we1"},
		{Source: avsysv1.SourceModrinth, Kind: avsysv1.KindDatapack, Project: "terralith", Version: "tl1"},
		{Source: avsysv1.SourceURL, Kind: avsysv1.KindPlugin, Project: pluginURL},
		{Source: avsysv1.SourceUpload, Kind: avsysv1.KindPlugin, Project: "Own.jar"},
		{Source: avsysv1.SourceURL, Kind: avsysv1.KindDatapack, Project: datapackURL},
		{Source: avsysv1.SourceUpload, Kind: avsysv1.KindDatapack, Project: "Mine.zip"},
	}
}

// installed lays out in dir what the image leaves once withContent's content
// is installed, plugins in folder.
func installed(t *testing.T, dir, folder string) {
	t.Helper()
	for file, body := range map[string]string{
		".modrinth-manifest.json": `{"@type":"me.itzg.helpers.modrinth.ModrinthManifest","timestamp":"2026-11-14T04:09:35.260982903Z",` +
			`"files":["` + folder + `/LuckPerms-5.jar","` + folder + `/worldedit-7.jar","` + folder + `/Dep-1.jar","world/datapacks/Terralith.zip"],` +
			`"projects":["luckperms:lp1","worldedit:we1","datapack:terralith:tl1"]}`,
		folder + "/.var-list-manifest.json": `{"@type":"me.itzg.helpers.sync.MultiCopyManifest","timestamp":"2026-11-14T04:09:31.011640029Z","files":["Chunky-1.4.jar","Own.jar"]}`,
		folder + "/LuckPerms-5.jar":         "jar",
		folder + "/worldedit-7.jar":         "jar",
		folder + "/Dep-1.jar":               "jar",
		folder + "/Chunky-1.4.jar":          "jar",
		folder + "/Own.jar":                 "jar",
		"world/datapacks/Terralith.zip":     "zip",
		"world/datapacks/Vein-1.2.zip":      "zip",
		"world/datapacks/Mine.zip":          "zip",
		".avsys/content/Own.jar":            "jar",
		".avsys/content/Mine.zip":           "zip",
	} {
		write(t, filepath.Join(dir, file), body)
	}
}

func write(t *testing.T, file, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

// started runs startScript as the game's container runs it, with dir as its
// /data, the server's environment and extra, and a curl that answers for
// the URLs starting with one of up ("*" for all) and fails as a site that
// cannot be reached does for the rest. It returns what the image would be
// given, by name, "unset" for one not set, and the URLs asked.
func started(t *testing.T, dir string, mc *avsysv1.MinecraftServer, extra []string, up ...string) (map[string]string, []string, string) {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	bin := filepath.Join(dir, "bin")
	asked := filepath.Join(dir, "asked")
	patterns := make([]string, len(up))
	for i, u := range up {
		patterns[i] = "'" + u + "'*"
		if u == "*" {
			patterns[i] = "*"
		}
	}
	curl := "#!/bin/sh\nfor a; do last=$a; done\necho \"$last\" >> " + asked + "\n"
	if len(patterns) > 0 {
		curl += "case \"$last\" in " + strings.Join(patterns, " | ") + ") exit 0 ;; esac\n"
	}
	write(t, filepath.Join(bin, "curl"), curl+"exit 6\n")
	script := strings.ReplaceAll(startScript, "/data/", dir+"/")
	script = strings.ReplaceAll(script, "exec "+imageStart, `exec sh -c 'printf "MODRINTH_PROJECTS<%s>\nMODS<%s>\nPLUGINS<%s>\nDATAPACKS<%s>\n" "${MODRINTH_PROJECTS-unset}" "${MODS-unset}" "${PLUGINS-unset}" "${DATAPACKS-unset}"'`)
	cmd := exec.Command("sh", "-c", script)
	cmd.Env = []string{"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH")}
	for _, e := range minecraftEnv(mc, Config{}) {
		if e.ValueFrom == nil {
			cmd.Env = append(cmd.Env, e.Name+"="+strings.ReplaceAll(e.Value, "/data/", dir+"/"))
		}
	}
	cmd.Env = append(cmd.Env, extra...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	given := map[string]string{}
	for _, m := range regexp.MustCompile(`(?s)(MODRINTH_PROJECTS|MODS|PLUGINS|DATAPACKS)<(.*?)>\n`).FindAllStringSubmatch(string(out), -1) {
		given[m[1]] = m[2]
	}
	calls, _ := os.ReadFile(asked)
	return given, strings.Fields(string(calls)), string(out)
}

// A server whose content is installed as its settings ask starts with it
// when the sites it comes from do not answer, each kept on its own: its
// Modrinth projects, when every file of theirs is there, and each file
// from a URL that is. Anything changed or missing is left to the image,
// which installs as it always has. Sites are asked only about what the
// server could start with, and while they answer, the image is given its
// content as before.
func TestKeepContent(t *testing.T) {
	type keeps struct{ modrinth, plugin, datapack bool }
	all := keeps{true, true, true}
	for _, c := range []struct {
		name   string
		mutate func(*avsysv1.MinecraftServer)
		// change changes the files installed, in dir.
		change func(dir string)
		extra  []string
		folder string
		keeps  keeps
	}{
		{name: "installed", keeps: all},
		{name: "first start", change: func(dir string) { os.RemoveAll(dir); os.MkdirAll(dir, 0o755) }},
		{name: "a dependency gone", change: func(dir string) { os.Remove(filepath.Join(dir, "plugins/Dep-1.jar")) }, keeps: keeps{false, true, true}},
		{name: "a datapack from Modrinth gone", change: func(dir string) { os.Remove(filepath.Join(dir, "world/datapacks/Terralith.zip")) }, keeps: keeps{false, true, true}},
		{name: "a project added", mutate: func(mc *avsysv1.MinecraftServer) {
			mc.Spec.Mods = append(mc.Spec.Mods, avsysv1.ContentRef{Source: avsysv1.SourceModrinth, Kind: avsysv1.KindPlugin, Project: "essentials", Version: "es1"})
		}, keeps: keeps{false, true, true}},
		{name: "a project taken off", mutate: func(mc *avsysv1.MinecraftServer) { mc.Spec.Mods = mc.Spec.Mods[1:] }, keeps: keeps{false, true, true}},
		{name: "another version", mutate: func(mc *avsysv1.MinecraftServer) { mc.Spec.Mods[0].Version = "lp2" }, keeps: keeps{false, true, true}},
		{name: "not pinned", mutate: func(mc *avsysv1.MinecraftServer) { mc.Spec.Mods[0].Version = "" }, change: func(dir string) {
			record := filepath.Join(dir, ".modrinth-manifest.json")
			body, _ := os.ReadFile(record)
			os.WriteFile(record, []byte(strings.Replace(string(body), `"luckperms:lp1"`, `"luckperms"`, 1)), 0o644)
		}, keeps: keeps{false, true, true}},
		{name: "a datapack not pinned", mutate: func(mc *avsysv1.MinecraftServer) { mc.Spec.Mods[2].Version = "" }, change: func(dir string) {
			record := filepath.Join(dir, ".modrinth-manifest.json")
			body, _ := os.ReadFile(record)
			os.WriteFile(record, []byte(strings.Replace(string(body), `"datapack:terralith:tl1"`, `"datapack:terralith"`, 1)), 0o644)
		}, keeps: keeps{false, true, true}},
		{name: "a record it cannot read", change: func(dir string) {
			record := filepath.Join(dir, ".modrinth-manifest.json")
			body, _ := os.ReadFile(record)
			os.WriteFile(record, []byte(strings.Replace(string(body), `Dep-1.jar`, `De\"p.jar`, 1)), 0o644)
		}, keeps: keeps{false, true, true}},
		{name: "no record", change: func(dir string) { os.Remove(filepath.Join(dir, ".modrinth-manifest.json")) }, keeps: keeps{false, true, true}},
		{name: "another world", extra: []string{"LEVEL=other"}, keeps: keeps{false, true, false}},
		{name: "the plugin from a URL gone", change: func(dir string) { os.Remove(filepath.Join(dir, "plugins/Chunky-1.4.jar")) }, keeps: keeps{true, false, true}},
		{name: "a plugin not the image's", change: func(dir string) {
			write(t, filepath.Join(dir, "plugins/.var-list-manifest.json"), `{"files":["Own.jar"]}`)
		}, keeps: keeps{true, false, true}},
		{name: "another URL", mutate: func(mc *avsysv1.MinecraftServer) {
			mc.Spec.Mods[3].Project = "https://files.example.org/chunky/Chunky-1.5.jar"
			mc.Spec.Mods[5].Project = "https://packs.example.org/v/Vein-1.3.zip"
		}, keeps: keeps{true, false, false}},
		{name: "the datapack from a URL gone", change: func(dir string) { os.Remove(filepath.Join(dir, "world/datapacks/Vein-1.2.zip")) }, keeps: keeps{true, true, false}},
		{name: "old mods removed first", extra: []string{"REMOVE_OLD_MODS=true"}},
		{name: "old datapacks removed first", extra: []string{"REMOVE_OLD_DATAPACKS=TRUE"}},
		{name: "old files kept", extra: []string{"REMOVE_OLD_MODS=false", "REMOVE_OLD_DATAPACKS=FALSE"}, keeps: all},
		{name: "plugins in a folder of their own", extra: []string{"PLUGINS_OUT_DIR=/data/elsewhere"}, keeps: keeps{true, false, true}},
		{name: "plugins and mods apart", extra: []string{"USES_PLUGINS=true"}, keeps: keeps{true, false, true}},
		{name: "fabric", mutate: func(mc *avsysv1.MinecraftServer) {
			mc.Spec.Loader = avsysv1.LoaderFabric
			for i := range mc.Spec.Mods {
				if mc.Spec.Mods[i].Kind == avsysv1.KindPlugin {
					mc.Spec.Mods[i].Kind = avsysv1.KindMod
				}
			}
		}, folder: "mods", keeps: all},
		// Vanilla takes no mods or plugins, and the image leaves the list.
		{name: "vanilla", mutate: func(mc *avsysv1.MinecraftServer) { mc.Spec.Loader = avsysv1.LoaderVanilla }, keeps: keeps{true, false, true}},
	} {
		for _, answers := range []bool{true, false} {
			name := c.name + "/sites down"
			if answers {
				name = c.name + "/sites up"
			}
			t.Run(name, func(t *testing.T) {
				dir := t.TempDir()
				folder := c.folder
				if folder == "" {
					folder = "plugins"
				}
				installed(t, dir, folder)
				if c.change != nil {
					c.change(dir)
				}
				mc := server(withContent)
				if c.mutate != nil {
					c.mutate(mc)
				}
				extra := make([]string, len(c.extra))
				for i, e := range c.extra {
					extra[i] = strings.ReplaceAll(e, "/data/", dir+"/")
				}
				var up []string
				if answers {
					up = []string{"*"}
				}
				given, asked, out := started(t, dir, mc, extra, up...)

				env := envOf(mc)
				list := func(name string) string { return strings.ReplaceAll(env[name], "/data/", dir+"/") }
				want := map[string]string{"MODRINTH_PROJECTS": list("MODRINTH_PROJECTS"), "MODS": list("MODS"), "PLUGINS": list("PLUGINS"), "DATAPACKS": list("DATAPACKS")}
				var wantAsked []string
				if c.keeps.modrinth {
					wantAsked = append(wantAsked, modrinthAPI+"/v2/tag/project_type")
					if !answers {
						want["MODRINTH_PROJECTS"] = "unset"
					}
				}
				lists := "PLUGINS"
				if folder == "mods" {
					lists = "MODS"
				}
				if c.keeps.plugin {
					wantAsked = append(wantAsked, pluginURL)
					if !answers {
						want[lists] = dir + "/" + folder + "/Chunky-1.4.jar\n" + dir + "/.avsys/content/Own.jar"
					}
				}
				if c.keeps.datapack {
					wantAsked = append(wantAsked, datapackURL)
					if !answers {
						want["DATAPACKS"] = dir + "/.avsys/content/Mine.zip"
					}
				}
				for name, w := range want {
					if given[name] != w {
						t.Errorf("%s given %q, want %q\n%s", name, given[name], w, out)
					}
				}
				if strings.Join(asked, " ") != strings.Join(wantAsked, " ") {
					t.Errorf("asked %q, want %q", asked, wantAsked)
				}
				if said := strings.Contains(out, "[avsys] Modrinth does not answer: starting with the installed LuckPerms-5.jar, worldedit-7.jar, Dep-1.jar, Terralith.zip"); said != (c.keeps.modrinth && !answers) {
					t.Errorf("says it starts with what Modrinth installed: %v, in %q", said, out)
				}
				if said := strings.Contains(out, "[avsys] Modrinth answers: checking the installed"); said != (c.keeps.modrinth && answers) {
					t.Errorf("says it checks with Modrinth: %v, in %q", said, out)
				}
				if said := strings.Contains(out, "[avsys] files.example.org does not answer: starting with the installed Chunky-1.4.jar"); said != (c.keeps.plugin && !answers) {
					t.Errorf("says it starts with the plugin from a URL: %v, in %q", said, out)
				}
				if said := strings.Contains(out, "[avsys] packs.example.org does not answer: starting with the installed Vein-1.2.zip"); said != (c.keeps.datapack && !answers) {
					t.Errorf("says it starts with the datapack from a URL: %v, in %q", said, out)
				}
			})
		}
	}
}

// Each site is asked on its own, and what comes from one that answers is
// given to the image as before. Modrinth is asked where the image asks it,
// MODRINTH_API_BASE_URL in the server's own environment.
func TestKeepContentAsksEachSite(t *testing.T) {
	dir := t.TempDir()
	installed(t, dir, "plugins")
	mc := server(withContent)
	given, asked, out := started(t, dir, mc, []string{"MODRINTH_API_BASE_URL=http://127.0.0.1:9"}, "https://files.example.org/")
	want := []string{"http://127.0.0.1:9/v2/tag/project_type", pluginURL, datapackURL}
	if strings.Join(asked, " ") != strings.Join(want, " ") {
		t.Errorf("asked %q, want %q", asked, want)
	}
	if given["MODRINTH_PROJECTS"] != "unset" {
		t.Errorf("Modrinth not answering, the image is given %q", given["MODRINTH_PROJECTS"])
	}
	if want := strings.ReplaceAll(envOf(mc)["PLUGINS"], "/data/", dir+"/"); given["PLUGINS"] != want {
		t.Errorf("the plugin's site answering, the image is given %q, want %q\n%s", given["PLUGINS"], want, out)
	}
	if want := dir + "/.avsys/content/Mine.zip"; given["DATAPACKS"] != want {
		t.Errorf("the datapack's site not answering, the image is given %q, want %q", given["DATAPACKS"], want)
	}
}

// Content asked of the image without the panel, as a list of its own with
// commas, is read as the image reads it.
func TestKeepContentReadsListsAsTheImage(t *testing.T) {
	dir := t.TempDir()
	installed(t, dir, "plugins")
	mc := server(func(mc *avsysv1.MinecraftServer) { mc.Spec.Loader = avsysv1.LoaderPaper })
	given, _, out := started(t, dir, mc, []string{
		"MODRINTH_PROJECTS= worldedit:we1, datapack:terralith:tl1 # theirs\n\nluckperms:lp1",
		"PLUGINS=" + pluginURL + " ,Own.jar",
		"DATAPACKS=" + datapackURL + "  " + dir + "/.avsys/content/Mine.zip",
	})
	for name, want := range map[string]string{
		"MODRINTH_PROJECTS": "unset",
		"PLUGINS":           dir + "/plugins/Chunky-1.4.jar\nOwn.jar",
		"DATAPACKS":         dir + "/.avsys/content/Mine.zip",
	} {
		if given[name] != want {
			t.Errorf("%s given %q, want %q\n%s", name, given[name], want, out)
		}
	}
}
