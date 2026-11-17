package controller

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
)

// What the image leaves in /data once it has installed each loader, as it
// was on servers it installed: its records, and the files they name.
var (
	fabricLauncher  = "fabric-server-mc.1.20.1-loader.0.16.14-launcher.1.1.2.jar"
	fabricInstalled = map[string]string{
		".fabric-manifest.json": `{"@type":"me.itzg.helpers.fabric.FabricManifest","timestamp":"2026-11-17T01:26:28.089613538Z","files":["` + fabricLauncher + `"],"launcherPath":"./` + fabricLauncher + `","origin":{"@type":"versions","game":"1.20.1","loader":"0.16.14","installer":"1.1.2"}}`,
		".install-fabric.env":   "SERVER=\"./" + fabricLauncher + "\"\nFAMILY=\"FABRIC\"\nTYPE=\"FABRIC\"\nVERSION=\"1.20.1\"\n",
		fabricLauncher:          "jar",
	}
	// One installed from a launcher at an address of the server's own.
	fabricFromURL = map[string]string{
		".fabric-manifest.json": `{"@type":"me.itzg.helpers.fabric.FabricManifest","timestamp":"2026-11-17T01:26:28.089613538Z","files":["` + fabricLauncher + `"],"launcherPath":"./` + fabricLauncher + `","origin":{"@type":"remote","uri":"https://example.org/fabric.jar"}}`,
		fabricLauncher:          "jar",
	}
	forgeInstalled = map[string]string{
		".forge-manifest.json": `{"@type":"me.itzg.helpers.forge.ForgeManifest","timestamp":"2026-11-17T01:30:51.235790186Z","files":null,"minecraftVersion":"1.20.1","forgeVersion":"47.4.28","serverEntry":"run.sh"}`,
		".run-forge.env":       "SERVER=\"run.sh\"\nFAMILY=\"FORGE\"\nVERSION=\"1.20.1\"\nTYPE=\"FORGE\"\n",
		"run.sh":               "#!/bin/sh\n",
	}
	neoForgeInstalled = map[string]string{
		".neoforge-manifest.json": `{"@type":"me.itzg.helpers.forge.ForgeManifest","timestamp":"2026-11-17T01:30:40.420961221Z","files":null,"minecraftVersion":"1.21.1","forgeVersion":"21.1.266","serverEntry":"run.sh"}`,
		".run-neoforge.env":       "SERVER=\"run.sh\"\nFAMILY=\"FORGE\"\nVERSION=\"1.21.1\"\nTYPE=\"NEOFORGE\"\n",
		"run.sh":                  "#!/bin/sh\n",
	}
	quiltLauncher  = "quilt-server-1.20.1-0.31.0-beta.6-launcher.jar"
	quiltInstalled = map[string]string{
		".quilt-manifest.json": `{"@type":"me.itzg.helpers.quilt.QuiltManifest","timestamp":"2026-11-17T01:26:31.963392591Z","files":["` + quiltLauncher + `"],"minecraftVersion":"1.20.1","loaderVersion":"0.31.0-beta.6"}`,
		".quilt.env":           "SERVER=\"/data/" + quiltLauncher + "\"\nFAMILY=\"FABRIC\"\nTYPE=\"QUILT\"\nVERSION=\"1.20.1\"\n",
		quiltLauncher:          "jar",
	}
	purpurJar       = "purpur-1.20.1-2062.jar"
	purpurInstalled = map[string]string{
		".purpur-manifest.json": `{"@type":"me.itzg.helpers.purpur.PurpurManifest","timestamp":"2026-11-17T01:30:19.718269973Z","files":["` + purpurJar + `"],"minecraftVersion":"1.20.1","build":"2062","customDownloadUrl":null}`,
		".purpur.env":           "SERVER=\"/data/" + purpurJar + "\"\nTYPE=\"PURPUR\"\nVERSION=\"1.20.1\"\n",
		purpurJar:               "jar",
	}
	vanillaInstalled = map[string]string{
		".vanilla-manifest.json":      `{"@type":"me.itzg.helpers.vanilla.VanillaManifest","timestamp":"2026-11-17T01:26:29.828196977Z","files":["minecraft_server.1.20.1.jar"],"minecraftVersion":"1.20.1","serverEntry":"minecraft_server.1.20.1.jar"}`,
		"minecraft_server.1.20.1.jar": "jar",
	}
)

// without is files with some taken away.
func without(files map[string]string, gone ...string) map[string]string {
	out := map[string]string{}
	for k, v := range files {
		out[k] = v
	}
	for _, g := range gone {
		delete(out, g)
	}
	return out
}

// What the image is handed, as the variables keepLoader may change.
var loaderVars = []string{"TYPE", "CUSTOM_SERVER", "FAMILY", "MODRINTH_LOADER", "FABRIC_LOADER_VERSION", "FABRIC_LAUNCHER_VERSION", "FORGE_VERSION", "NEOFORGE_VERSION"}

// runLoader runs startScript as the game's container runs it, on /data laid
// out with files, with a curl that answers as the sites do but those whose
// address has one of down in it. It gives what the image would be handed,
// what the script said, and the addresses it asked, in order.
func runLoader(t *testing.T, files map[string]string, env []string, down ...string) (handed map[string]string, said string, asked []string) {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(strings.ReplaceAll(body, "/data/", dir+"/")), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(bin, "asked")
	curl := "#!/bin/sh\nfor a; do last=$a; done\necho \"$last\" >> " + log + "\nfor d in $DOWN; do case $last in *\"$d\"*) exit 7 ;; esac; done\nexit 0\n"
	if err := os.WriteFile(filepath.Join(bin, "curl"), []byte(curl), 0o755); err != nil {
		t.Fatal(err)
	}
	var show strings.Builder
	for _, v := range loaderVars {
		show.WriteString(v + "=${" + v + "-unset}\n")
	}
	script := strings.ReplaceAll(startScript, "/data/", dir+"/")
	script = strings.ReplaceAll(script, "exec "+imageStart, "printf '%s\\n' '---' \""+strings.TrimSuffix(show.String(), "\n")+"\"")
	cmd := exec.Command("sh", "-c", script)
	cmd.Env = append([]string{"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"), "DOWN=" + strings.Join(down, " ")}, env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	before, after, ok := strings.Cut(string(out), "---\n")
	if !ok {
		t.Fatalf("the image was not started: %s", out)
	}
	handed = map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(after), "\n") {
		k, v, _ := strings.Cut(line, "=")
		handed[k] = strings.ReplaceAll(v, dir+"/", "/data/")
	}
	calls, _ := os.ReadFile(log)
	if s := strings.TrimSpace(string(calls)); s != "" {
		asked = strings.Split(s, "\n")
	}
	return handed, before, asked
}

// The variables keepLoader may change as the controller hands them to the
// image for a server, with extra in its own environment after them.
func loaderEnv(mc *avsysv1.MinecraftServer, extra []string) (env []string, vars map[string]string) {
	vars = map[string]string{}
	for _, v := range loaderVars {
		vars[v] = "unset"
	}
	for k, v := range envOf(mc) {
		if k == "MODRINTH_PROJECTS" || k == "CURSEFORGE_FILES" || k == "MODS" || k == "PLUGINS" {
			// Content is keepContent's, and asks sites of its own.
			continue
		}
		env = append(env, k+"="+v)
		if _, ok := vars[k]; ok {
			vars[k] = v
		}
	}
	for _, e := range extra {
		env = append(env, e)
		if k, v, _ := strings.Cut(e, "="); vars[k] != "" {
			vars[k] = v
		}
	}
	return env, vars
}

// startScript, on a server whose loader is installed as asked, starts it
// with the loader it has when the loader's sites do not answer, and asks
// them only where the image would. Anything else is left to the image.
func TestKeepLoader(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	on := func(loader avsysv1.Loader, version, build string) *avsysv1.MinecraftServer {
		return server(func(mc *avsysv1.MinecraftServer) {
			mc.Spec.Loader, mc.Spec.Version, mc.Spec.LoaderVersion = loader, version, build
		})
	}
	fabricMetaAsked := []string{"https://meta.fabricmc.net/v2/versions/installer"}
	keptFabric := map[string]string{"FABRIC_LOADER_VERSION": "0.16.14", "FABRIC_LAUNCHER_VERSION": "1.1.2"}
	for _, c := range []struct {
		name  string
		mc    *avsysv1.MinecraftServer
		files map[string]string
		extra []string
		// asked is what is asked, in order, and kept what the image is
		// handed in place of what it was when nothing answers; nil is
		// nothing.
		asked []string
		kept  map[string]string
		said  string
	}{
		{"fabric", on(avsysv1.LoaderFabric, "1.20.1", "0.16.14"), fabricInstalled, nil, fabricMetaAsked, keptFabric, "[avsys] meta.fabricmc.net does not answer: starting with the installed Fabric loader 0.16.14"},
		{"fabric with no loader given", on(avsysv1.LoaderFabric, "1.20.1", ""), fabricInstalled, nil, fabricMetaAsked, keptFabric, "[avsys] meta.fabricmc.net does not answer: starting with the installed Fabric loader 0.16.14"},
		{"fabric, its first start", on(avsysv1.LoaderFabric, "1.20.1", "0.16.14"), nil, nil, nil, nil, ""},
		{"fabric, its launcher gone", on(avsysv1.LoaderFabric, "1.20.1", "0.16.14"), without(fabricInstalled, fabricLauncher), nil, nil, nil, ""},
		{"fabric, another game version", on(avsysv1.LoaderFabric, "1.20.2", "0.16.14"), fabricInstalled, nil, nil, nil, ""},
		{"fabric, the latest game version", on(avsysv1.LoaderFabric, "LATEST", "0.16.14"), fabricInstalled, nil, nil, nil, ""},
		{"fabric, another loader", on(avsysv1.LoaderFabric, "1.20.1", "0.16.10"), fabricInstalled, nil, nil, nil, ""},
		{"fabric, the installer it has given", on(avsysv1.LoaderFabric, "1.20.1", "0.16.14"), fabricInstalled, []string{"FABRIC_LAUNCHER_VERSION=1.1.2"}, nil, nil, ""},
		{"fabric, its installer's older name", on(avsysv1.LoaderFabric, "1.20.1", "0.16.14"), fabricInstalled, []string{"FABRIC_INSTALLER_VERSION=1.1.2"}, nil, nil, ""},
		{"fabric, another installer given", on(avsysv1.LoaderFabric, "1.20.1", ""), fabricInstalled, []string{"FABRIC_LAUNCHER_VERSION=1.0.1"}, nil, nil, ""},
		{"fabric, a launcher of its own", on(avsysv1.LoaderFabric, "1.20.1", "0.16.14"), fabricInstalled, []string{"FABRIC_LAUNCHER=/data/" + fabricLauncher}, nil, nil, ""},
		{"fabric, a launcher's address of its own", on(avsysv1.LoaderFabric, "1.20.1", "0.16.14"), fabricFromURL, nil, nil, nil, ""},
		{"fabric, a reinstall asked for", on(avsysv1.LoaderFabric, "1.20.1", "0.16.14"), fabricInstalled, []string{"FABRIC_FORCE_REINSTALL=TRUE"}, nil, nil, ""},
		{"fabric, settings from a file of its own", on(avsysv1.LoaderFabric, "1.20.1", "0.16.14"), fabricInstalled, []string{"LOAD_ENV_FROM_FILE=/data/server.env"}, nil, nil, ""},
		{"fabric's site named", on(avsysv1.LoaderFabric, "1.20.1", "0.16.14"), fabricInstalled, []string{"FABRIC_META_BASE_URL=http://127.0.0.1:9"}, []string{"http://127.0.0.1:9/v2/versions/installer"}, keptFabric, "[avsys] 127.0.0.1:9 does not answer: starting with the installed Fabric loader 0.16.14"},

		{"forge", on(avsysv1.LoaderForge, "1.20.1", ""), forgeInstalled, nil, []string{forgeVersions}, map[string]string{"FORGE_VERSION": "47.4.28"}, "[avsys] files.minecraftforge.net does not answer: starting with the installed Forge 47.4.28"},
		{"forge, recommended", on(avsysv1.LoaderForge, "1.20.1", ""), forgeInstalled, []string{"FORGE_VERSION=RECOMMENDED"}, []string{forgeVersions}, map[string]string{"FORGE_VERSION": "47.4.28"}, "[avsys] files.minecraftforge.net does not answer"},
		// The image asks nothing for the build it has.
		{"forge, the build it has", on(avsysv1.LoaderForge, "1.20.1", "47.4.28"), forgeInstalled, nil, nil, nil, ""},
		{"forge, another build", on(avsysv1.LoaderForge, "1.20.1", "47.4.29"), forgeInstalled, nil, nil, nil, ""},
		{"forge, another game version", on(avsysv1.LoaderForge, "1.20.2", ""), forgeInstalled, nil, nil, nil, ""},
		{"forge, its first start", on(avsysv1.LoaderForge, "1.20.1", ""), nil, nil, nil, nil, ""},
		{"forge, its start gone", on(avsysv1.LoaderForge, "1.20.1", ""), without(forgeInstalled, "run.sh"), nil, nil, nil, ""},
		{"forge, an installer of its own", on(avsysv1.LoaderForge, "1.20.1", ""), forgeInstalled, []string{"FORGE_INSTALLER_URL=https://example.org/forge.jar"}, nil, nil, ""},
		{"forge, a reinstall asked for", on(avsysv1.LoaderForge, "1.20.1", ""), forgeInstalled, []string{"FORGE_FORCE_REINSTALL=true"}, nil, nil, ""},

		{"neoforge", on(avsysv1.LoaderNeoForge, "1.21.1", ""), neoForgeInstalled, nil, []string{neoForgeBuilds}, map[string]string{"NEOFORGE_VERSION": "21.1.266"}, "[avsys] maven.neoforged.net does not answer: starting with the installed NeoForge 21.1.266"},
		{"neoforge, the build it has", on(avsysv1.LoaderNeoForge, "1.21.1", "21.1.266"), neoForgeInstalled, nil, nil, nil, ""},
		{"neoforge, another game version", on(avsysv1.LoaderNeoForge, "1.21.4", ""), neoForgeInstalled, nil, nil, nil, ""},

		{"quilt", on(avsysv1.LoaderQuilt, "1.20.1", ""), quiltInstalled, nil, []string{mojangVersions, quiltLoaders}, map[string]string{"TYPE": "CUSTOM", "CUSTOM_SERVER": "/data/" + quiltLauncher, "FAMILY": "FABRIC", "MODRINTH_LOADER": "quilt"}, "[avsys] launchermeta.mojang.com does not answer: starting with the installed Quilt loader 0.31.0-beta.6"},
		{"quilt, its loader given", on(avsysv1.LoaderQuilt, "1.20.1", "0.31.0-beta.6"), quiltInstalled, nil, []string{mojangVersions}, map[string]string{"TYPE": "CUSTOM", "CUSTOM_SERVER": "/data/" + quiltLauncher, "FAMILY": "FABRIC", "MODRINTH_LOADER": "quilt"}, "[avsys] launchermeta.mojang.com does not answer"},
		{"quilt, another loader", on(avsysv1.LoaderQuilt, "1.20.1", "0.30.0"), quiltInstalled, nil, nil, nil, ""},
		{"quilt, another game version", on(avsysv1.LoaderQuilt, "1.20.2", ""), quiltInstalled, nil, nil, nil, ""},
		{"quilt, its launcher gone", on(avsysv1.LoaderQuilt, "1.20.1", ""), without(quiltInstalled, quiltLauncher), nil, nil, nil, ""},
		// The image's own server is told no loader for CurseForge.
		{"quilt, with files from CurseForge", on(avsysv1.LoaderQuilt, "1.20.1", ""), quiltInstalled, []string{"CURSEFORGE_FILES=jei:4593548"}, nil, nil, ""},
		{"quilt, a Modrinth loader of its own", on(avsysv1.LoaderQuilt, "1.20.1", ""), quiltInstalled, []string{"MODRINTH_LOADER=fabric"}, []string{mojangVersions, quiltLoaders}, map[string]string{"TYPE": "CUSTOM", "CUSTOM_SERVER": "/data/" + quiltLauncher, "FAMILY": "FABRIC"}, "[avsys] launchermeta.mojang.com does not answer"},

		{"purpur", on(avsysv1.LoaderPurpur, "1.20.1", ""), purpurInstalled, nil, []string{purpurAPI + "/v2/purpur/1.20.1"}, map[string]string{"TYPE": "CUSTOM", "CUSTOM_SERVER": "/data/" + purpurJar, "FAMILY": "SPIGOT", "MODRINTH_LOADER": "purpur"}, "[avsys] api.purpurmc.org does not answer: starting with the installed " + purpurJar},
		{"purpur, the build it has", on(avsysv1.LoaderPurpur, "1.20.1", ""), purpurInstalled, []string{"PURPUR_BUILD=2062"}, []string{purpurAPI + "/v2/purpur/1.20.1"}, map[string]string{"TYPE": "CUSTOM", "CUSTOM_SERVER": "/data/" + purpurJar, "FAMILY": "SPIGOT", "MODRINTH_LOADER": "purpur"}, "[avsys] api.purpurmc.org does not answer"},
		{"purpur, another build", on(avsysv1.LoaderPurpur, "1.20.1", ""), purpurInstalled, []string{"PURPUR_BUILD=2061"}, nil, nil, ""},
		{"purpur, another game version", on(avsysv1.LoaderPurpur, "1.21.4", ""), purpurInstalled, nil, nil, nil, ""},
		{"purpur, a download of its own", on(avsysv1.LoaderPurpur, "1.20.1", ""), purpurInstalled, []string{"PURPUR_DOWNLOAD_URL=https://example.org/purpur.jar"}, nil, nil, ""},
		{"purpur, its jar gone", on(avsysv1.LoaderPurpur, "1.20.1", ""), without(purpurInstalled, purpurJar), nil, nil, nil, ""},
		{"purpur, another kind in its own environment", on(avsysv1.LoaderPurpur, "1.20.1", ""), purpurInstalled, []string{"TYPE=PAPER"}, nil, nil, ""},

		// Installed, the image asks nothing about vanilla.
		{"vanilla", on(avsysv1.LoaderVanilla, "1.20.1", ""), vanillaInstalled, nil, nil, nil, ""},
	} {
		for _, up := range []bool{true, false} {
			name := c.name + "/down"
			var down []string
			if up {
				name = c.name + "/up"
			} else {
				down = []string{"://"}
			}
			t.Run(name, func(t *testing.T) {
				env, want := loaderEnv(c.mc, c.extra)
				if !up {
					for k, v := range c.kept {
						want[k] = v
					}
				}
				handed, said, asked := runLoader(t, c.files, env, down...)
				for _, k := range loaderVars {
					if handed[k] != want[k] {
						t.Errorf("%s=%s, want %s", k, handed[k], want[k])
					}
				}
				// The first site that does not answer is enough.
				wantAsked := c.asked
				if !up && len(wantAsked) > 1 {
					wantAsked = wantAsked[:1]
				}
				if strings.Join(asked, " ") != strings.Join(wantAsked, " ") {
					t.Errorf("asked %q, want %q", asked, wantAsked)
				}
				if tells := strings.Contains(said, "does not answer"); tells != (!up && c.kept != nil) {
					t.Errorf("said %q", said)
				} else if tells && !strings.Contains(said, c.said) {
					t.Errorf("said %q, want %q", said, c.said)
				}
			})
		}
	}
}

// Quilt's start asks two sites, and is kept when either does not answer.
func TestKeepLoaderQuiltAsksBothSites(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	env, _ := loaderEnv(server(func(mc *avsysv1.MinecraftServer) {
		mc.Spec.Loader, mc.Spec.Version = avsysv1.LoaderQuilt, "1.20.1"
	}), nil)
	for down, said := range map[string]string{
		"launchermeta.mojang.com": "[avsys] launchermeta.mojang.com does not answer",
		"maven.quiltmc.org":       "[avsys] maven.quiltmc.org does not answer",
	} {
		handed, out, _ := runLoader(t, quiltInstalled, env, down)
		if handed["TYPE"] != "CUSTOM" || !strings.Contains(out, said) {
			t.Errorf("%s down: handed %v, said %q", down, handed, out)
		}
	}
}
