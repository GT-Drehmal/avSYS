package controller

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
)

// What the image leaves in /data once it has installed a modpack, as it was
// on servers it installed on pc: its records, and the files they name. The
// one from Modrinth is SkyBlock Plus 1.0.9, its record as keepWorld writes it
// back once the server has a world. An uploaded pack is recorded under its
// file's name, here the SHA-1 the panel keeps it under. The CurseForge ones
// were installed by the image from a stand-in for CurseForge's API, which
// needs a key: the records are the image's own, the pack's IDs made up.
var (
	skyblockLauncher = "fabric-server-mc.26.2-loader.0.19.5-launcher.1.1.2.jar"
	modrinthPack     = map[string]string{
		".modrinth-modpack-manifest.json": `{
  "@type": "me.itzg.helpers.modrinth.ModrinthModpackManifest",
  "timestamp": "2026-11-17T02:10:19.776878366Z",
  "files": [
    "mods/NoCC-1.3.1.jar",
    "mods/skyblock-biome-islands-1.0.9.jar",
    "mods/fabric-api-0.160.0+26.2.jar",
    "mods/skyblock-advancements-1.0.13.jar",
    "mods/standard-skyblock-2.1.7.jar",
    "mods/sky-void-additions-1.5.3.jar",
    "config/fabric/indigo-renderer.properties"
  ],
  "projectSlug": "skyblock-plus",
  "versionId": "Nte4eTsy",
  "dependencies": {
    "fabric-loader": "0.19.5",
    "minecraft": "26.2"
  }
}`,
		".install-modrinth.env":                    "SERVER=\"/data/" + skyblockLauncher + "\"\nFAMILY=\"FABRIC\"\nTYPE=\"FABRIC\"\nVERSION=\"26.2\"\nMODPACK_NAME=\"SkyBlock Plus\"\nMODPACK_VERSION=\"1.0.9\"\n",
		".fabric-manifest.json":                    `{"@type":"me.itzg.helpers.fabric.FabricManifest","timestamp":"2026-11-17T02:10:19.757910482Z","files":["` + skyblockLauncher + `"],"launcherPath":"/data/` + skyblockLauncher + `","origin":{"@type":"versions","game":"26.2","loader":"0.19.5","installer":"1.1.2"}}`,
		skyblockLauncher:                           "jar",
		"mods/NoCC-1.3.1.jar":                      "jar",
		"mods/skyblock-biome-islands-1.0.9.jar":    "jar",
		"mods/fabric-api-0.160.0+26.2.jar":         "jar",
		"mods/skyblock-advancements-1.0.13.jar":    "jar",
		"mods/standard-skyblock-2.1.7.jar":         "jar",
		"mods/sky-void-additions-1.5.3.jar":        "jar",
		"config/fabric/indigo-renderer.properties": "x",
		"world/level.dat":                          "x",
	}

	uploadSHA      = strings.Repeat("ab", 20)
	uploadFile     = ".avsys/content/" + uploadSHA + ".mrpack"
	uploadLauncher = "fabric-server-mc.1.21.4-loader.0.16.10-launcher.1.1.2.jar"
	uploadedFabric = map[string]string{
		".modrinth-modpack-manifest.json":         `{"@type":"me.itzg.helpers.modrinth.ModrinthModpackManifest","timestamp":"2026-11-17T02:37:38.072310930Z","files":["mods/lithium-fabric-0.15.3+mc1.21.4.jar","config/pack.txt","server.txt"],"projectSlug":"` + uploadSHA + `","versionId":"2026-11-17T02:37:35.981274993Z","dependencies":{"minecraft":"1.21.4","fabric-loader":"0.16.10"}}`,
		".install-modrinth.env":                   "SERVER=\"/data/" + uploadLauncher + "\"\nFAMILY=\"FABRIC\"\nTYPE=\"FABRIC\"\nVERSION=\"1.21.4\"\nMODPACK_NAME=\"Test Pack\"\nMODPACK_VERSION=\"local\"\n",
		".fabric-manifest.json":                   `{"@type":"me.itzg.helpers.fabric.FabricManifest","timestamp":"2026-11-17T02:08:52.458861077Z","files":["` + uploadLauncher + `"],"launcherPath":"/data/` + uploadLauncher + `","origin":{"@type":"versions","game":"1.21.4","loader":"0.16.10","installer":"1.1.2"}}`,
		uploadLauncher:                            "jar",
		"mods/lithium-fabric-0.15.3+mc1.21.4.jar": "jar",
		"config/pack.txt":                         "from the pack",
		"server.txt":                              "for the server",
		uploadFile:                                "zip",
	}
	uploadQuiltJar = "quilt-server-1.21.1-0.29.2-launcher.jar"
	uploadedQuilt  = map[string]string{
		".modrinth-modpack-manifest.json": `{"@type":"me.itzg.helpers.modrinth.ModrinthModpackManifest","timestamp":"2026-11-17T02:33:40.797364403Z","files":["config/pack.txt","server.txt"],"projectSlug":"` + uploadSHA + `","versionId":"2026-11-17T02:33:36.529622766Z","dependencies":{"minecraft":"1.21.1","quilt-loader":"0.29.2"}}`,
		".install-modrinth.env":           "SERVER=\"/data/" + uploadQuiltJar + "\"\nFAMILY=\"FABRIC\"\nTYPE=\"QUILT\"\nVERSION=\"1.21.1\"\nMODPACK_NAME=\"Test Pack\"\nMODPACK_VERSION=\"local\"\nMODPACK_NAME=\"Test Pack\"\nMODPACK_VERSION=\"local\"\n",
		".quilt-manifest.json":            `{"@type":"me.itzg.helpers.quilt.QuiltManifest","timestamp":"2026-11-17T02:16:09.137367888Z","files":["` + uploadQuiltJar + `"],"minecraftVersion":"1.21.1","loaderVersion":"0.29.2"}`,
		uploadQuiltJar:                    "jar",
		"config/pack.txt":                 "from the pack",
		"server.txt":                      "for the server",
		uploadFile:                        "zip",
	}
	uploadedForge = map[string]string{
		".modrinth-modpack-manifest.json": `{"@type":"me.itzg.helpers.modrinth.ModrinthModpackManifest","timestamp":"2026-11-17T02:33:39.931092453Z","files":["config/pack.txt","server.txt"],"projectSlug":"` + uploadSHA + `","versionId":"2026-11-17T02:33:36.488310119Z","dependencies":{"minecraft":"1.20.1","forge":"47.4.28"}}`,
		".install-modrinth.env":           "SERVER=\"run.sh\"\nFAMILY=\"FORGE\"\nVERSION=\"1.20.1\"\nTYPE=\"FORGE\"\nMODPACK_NAME=\"Test Pack\"\nMODPACK_VERSION=\"local\"\n",
		".forge-manifest.json":            `{"@type":"me.itzg.helpers.forge.ForgeManifest","timestamp":"2026-11-17T02:15:07.650285331Z","files":null,"minecraftVersion":"1.20.1","forgeVersion":"47.4.28","serverEntry":"run.sh"}`,
		"run.sh":                          "#!/bin/sh\n",
		"config/pack.txt":                 "from the pack",
		"server.txt":                      "for the server",
		uploadFile:                        "zip",
	}
	uploadedNeoForge = map[string]string{
		".modrinth-modpack-manifest.json": `{"@type":"me.itzg.helpers.modrinth.ModrinthModpackManifest","timestamp":"2026-11-17T02:33:39.776968238Z","files":["config/pack.txt","server.txt"],"projectSlug":"` + uploadSHA + `","versionId":"2026-11-17T02:33:36.648310095Z","dependencies":{"minecraft":"1.21.1","neoforge":"21.1.266"}}`,
		".install-modrinth.env":           "SERVER=\"run.sh\"\nFAMILY=\"FORGE\"\nVERSION=\"1.21.1\"\nTYPE=\"NEOFORGE\"\nMODPACK_NAME=\"Test Pack\"\nMODPACK_VERSION=\"local\"\n",
		".neoforge-manifest.json":         `{"@type":"me.itzg.helpers.forge.ForgeManifest","timestamp":"2026-11-17T02:15:08.816247107Z","files":null,"minecraftVersion":"1.21.1","forgeVersion":"21.1.266","serverEntry":"run.sh"}`,
		"run.sh":                          "#!/bin/sh\n",
		"config/pack.txt":                 "from the pack",
		"server.txt":                      "for the server",
		uploadFile:                        "zip",
	}

	curseForgeForge = map[string]string{
		".curseforge-manifest.json": `{"@type":"me.itzg.helpers.curseforge.CurseForgeManifest","timestamp":"2026-11-17T02:20:42.753254930Z","files":["config/pack.txt","server.txt"],"modpackName":"Test CF Pack cf-forge","modpackVersion":"1.0.2001","slug":"cf-forge","modId":1001,"fileId":2001,"fileName":"Test CF Pack 1.0","minecraftVersion":"1.20.1","modLoaderId":"forge-47.4.28","levelName":null}`,
		".install-curseforge.env":   "SERVER=\"/data/./run.sh\"\nFAMILY=\"FORGE\"\nVERSION=\"1.20.1\"\nTYPE=\"FORGE\"\nMODPACK_NAME=\"Test CF Pack cf-forge\"\nMODPACK_VERSION=\"1.0.2001\"\n",
		".forge-manifest.json":      `{"@type":"me.itzg.helpers.forge.ForgeManifest","timestamp":"2026-11-17T02:20:42.737624554Z","files":null,"minecraftVersion":"1.20.1","forgeVersion":"47.4.28","serverEntry":"/data/./run.sh"}`,
		"run.sh":                    "#!/bin/sh\n",
		"config/pack.txt":           "from the pack",
		"server.txt":                "for the server",
	}
	curseForgeFabricLauncher = "fabric-server-mc.1.21.4-loader.0.16.10-launcher.1.1.2.jar"
	curseForgeFabric         = map[string]string{
		".curseforge-manifest.json": `{"@type":"me.itzg.helpers.curseforge.CurseForgeManifest","timestamp":"2026-11-17T02:19:48.408958456Z","files":["config/pack.txt","server.txt"],"modpackName":"Test CF Pack cf-fabric","modpackVersion":"1.0.2002","slug":"cf-fabric","modId":1002,"fileId":2002,"fileName":"Test CF Pack 1.0","minecraftVersion":"1.21.4","modLoaderId":"fabric-0.16.10","levelName":null}`,
		".install-curseforge.env":   "SERVER=\"./" + curseForgeFabricLauncher + "\"\nFAMILY=\"FABRIC\"\nTYPE=\"FABRIC\"\nVERSION=\"1.21.4\"\nMODPACK_NAME=\"Test CF Pack cf-fabric\"\nMODPACK_VERSION=\"1.0.2002\"\n",
		".fabric-manifest.json":     `{"@type":"me.itzg.helpers.fabric.FabricManifest","timestamp":"2026-11-17T02:19:48.394513817Z","files":["` + curseForgeFabricLauncher + `"],"launcherPath":"./` + curseForgeFabricLauncher + `","origin":{"@type":"versions","game":"1.21.4","loader":"0.16.10","installer":"1.1.2"}}`,
		curseForgeFabricLauncher:    "jar",
		"config/pack.txt":           "from the pack",
		"server.txt":                "for the server",
	}
	curseForgeNeoForge = map[string]string{
		".curseforge-manifest.json": `{"@type":"me.itzg.helpers.curseforge.CurseForgeManifest","timestamp":"2026-11-17T02:20:43.909209745Z","files":["config/pack.txt","server.txt"],"modpackName":"Test CF Pack cf-neoforge","modpackVersion":"1.0.2003","slug":"cf-neoforge","modId":1003,"fileId":2003,"fileName":"Test CF Pack 1.0","minecraftVersion":"1.21.1","modLoaderId":"neoforge-21.1.266","levelName":null}`,
		".install-curseforge.env":   "SERVER=\"/data/./run.sh\"\nFAMILY=\"FORGE\"\nVERSION=\"1.21.1\"\nTYPE=\"NEOFORGE\"\nMODPACK_NAME=\"Test CF Pack cf-neoforge\"\nMODPACK_VERSION=\"1.0.2003\"\n",
		".neoforge-manifest.json":   `{"@type":"me.itzg.helpers.forge.ForgeManifest","timestamp":"2026-11-17T02:20:43.890068723Z","files":null,"minecraftVersion":"1.21.1","forgeVersion":"21.1.266","serverEntry":"/data/./run.sh"}`,
		"run.sh":                    "#!/bin/sh\n",
		"config/pack.txt":           "from the pack",
		"server.txt":                "for the server",
	}
)

// with is files with some of them written another way.
func with(files map[string]string, name, old, new string) map[string]string {
	out := without(files)
	out[name] = strings.ReplaceAll(out[name], old, new)
	return out
}

// plus is files with another one.
func plus(files map[string]string, name string) map[string]string {
	out := without(files)
	out[name] = "x"
	return out
}

// What the image is handed, as the variables keepPack may change.
var packVars = []string{"MODPACK_PLATFORM", "TYPE", "VERSION", "FABRIC_LOADER_VERSION", "FABRIC_LAUNCHER_VERSION", "FORGE_VERSION", "NEOFORGE_VERSION", "CUSTOM_SERVER", "FAMILY", "MODRINTH_LOADER", "REMOVE_OLD_MODS", "LEVEL"}

// The image's own environment, which a server's comes on top of.
var imageEnv = map[string]string{"TYPE": "VANILLA", "VERSION": "LATEST"}

// runPack runs a modpack server's packScript as the game's container runs
// it, on /data laid out with files, with a curl that answers as the sites
// do, with code, but those whose address has one of down in it. It gives
// what the image would be handed, what the script said, and the addresses
// it asked, in order.
func runPack(t *testing.T, mc *avsysv1.MinecraftServer, files map[string]string, extra []string, code string, down ...string) (handed map[string]string, said string, asked []string) {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(strings.ReplaceAll(body, "/data/", dir+"/")), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(bin, "asked")
	// As curl does: with -w, the code it got, 000 for none; with -f, a
	// failure for a code of 400 or more.
	curl := `#!/bin/sh
w= f=
for a; do last=$a; case $a in -w) w=yes ;; -f*) f=yes ;; esac; done
echo "$last" >> ` + log + `
for d in $DOWN; do case $last in *"$d"*) [ -z "$w" ] || printf 000; exit 7 ;; esac; done
[ -z "$w" ] || printf %s "$CODE"
[ -n "$f" ] && [ "$CODE" -ge 400 ] && exit 22
exit 0
`
	if err := os.WriteFile(filepath.Join(bin, "curl"), []byte(curl), 0o755); err != nil {
		t.Fatal(err)
	}
	var show strings.Builder
	for _, v := range packVars {
		show.WriteString(v + "=${" + v + "-unset}\n")
	}
	script := strings.ReplaceAll(packScript(mc.Spec.Modpack), "/data/", dir+"/")
	script = strings.ReplaceAll(script, "exec "+imageStart, "printf '%s\\n' '---' \""+strings.TrimSuffix(show.String(), "\n")+"\"")
	cmd := exec.Command("sh", "-c", script)
	cmd.Env = []string{"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"), "DOWN=" + strings.Join(down, " "), "CODE=" + code}
	for k, v := range imageEnv {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	for k, v := range envOf(mc) {
		cmd.Env = append(cmd.Env, k+"="+strings.ReplaceAll(v, "/data/", dir+"/"))
	}
	cmd.Env = append(cmd.Env, extra...)
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

// The variables keepPack may change as the image would see them for a
// server, with extra in its own environment after them.
func packEnv(mc *avsysv1.MinecraftServer, extra []string) map[string]string {
	vars := map[string]string{}
	for _, v := range packVars {
		vars[v] = "unset"
	}
	for k, v := range imageEnv {
		vars[k] = v
	}
	for k, v := range envOf(mc) {
		if _, ok := vars[k]; ok {
			vars[k] = v
		}
	}
	for _, e := range extra {
		if k, v, _ := strings.Cut(e, "="); vars[k] != "" {
			vars[k] = v
		}
	}
	return vars
}

// packServer is a server from a modpack: from Modrinth, version pinned;
// uploaded, its file's SHA-1; from CurseForge, the file pinned.
func packServer(source avsysv1.ContentSource, project, version, loader string) *avsysv1.MinecraftServer {
	return server(func(mc *avsysv1.MinecraftServer) {
		mc.Spec.Modpack = &avsysv1.ModpackSpec{Source: source, Project: project, Version: version, Loader: loader}
	})
}

// packScript, on a server whose modpack is installed as asked, starts the
// loader the pack has, with the files as they are, when the sites the image
// asks about the pack do not answer, and asks them only where the image
// would. Anything else is left to the image.
func TestKeepPack(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	modrinth := func(version string) *avsysv1.MinecraftServer {
		return packServer(avsysv1.SourceModrinth, "skyblock-plus", version, "fabric")
	}
	uploaded := func(sha, loader string) *avsysv1.MinecraftServer {
		return packServer(avsysv1.SourceUpload, "pack.mrpack", sha, loader)
	}
	curseForge := func(slug, file, loader string) *avsysv1.MinecraftServer {
		return packServer(avsysv1.SourceCurseForge, slug, file, loader)
	}
	modrinthAsked := []string{modrinthAPI + "/v2/tag/project_type"}
	curseForgeAsked := []string{curseForgeAPI + "/v1/categories?gameId=432"}
	fabricAsked := []string{fabricMeta + "/v2/versions/installer"}
	// The image is handed no pack, and the loader it has as its server.
	fabric := func(game, loader string) map[string]string {
		return map[string]string{"MODPACK_PLATFORM": "unset", "TYPE": "FABRIC", "VERSION": game, "FABRIC_LOADER_VERSION": loader, "FABRIC_LAUNCHER_VERSION": "1.1.2", "REMOVE_OLD_MODS": "false"}
	}
	forge := func(kind, game, build string) map[string]string {
		return map[string]string{"MODPACK_PLATFORM": "unset", "TYPE": kind, "VERSION": game, kind + "_VERSION": build, "REMOVE_OLD_MODS": "false"}
	}
	quilt := map[string]string{"MODPACK_PLATFORM": "unset", "TYPE": "CUSTOM", "VERSION": "1.21.1", "CUSTOM_SERVER": "/data/" + uploadQuiltJar, "FAMILY": "FABRIC", "MODRINTH_LOADER": "quilt", "REMOVE_OLD_MODS": "false"}
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
		{"modrinth", modrinth("Nte4eTsy"), modrinthPack, nil, modrinthAsked, fabric("26.2", "0.19.5"), "[avsys] api.modrinth.com does not answer: starting with the installed SkyBlock Plus 1.0.9"},
		{"modrinth, no version given", modrinth(""), modrinthPack, nil, modrinthAsked, fabric("26.2", "0.19.5"), "[avsys] api.modrinth.com does not answer"},
		{"modrinth, its own Modrinth", modrinth("Nte4eTsy"), modrinthPack, []string{"MODRINTH_API_BASE_URL=http://127.0.0.1:9"}, []string{"http://127.0.0.1:9/v2/tag/project_type"}, fabric("26.2", "0.19.5"), "[avsys] 127.0.0.1:9 does not answer"},
		{"modrinth, its first start", modrinth("Nte4eTsy"), nil, nil, nil, nil, ""},
		{"modrinth, another version", modrinth("AbCdEf12"), modrinthPack, nil, nil, nil, ""},
		{"modrinth, no version given, another pack", packServer(avsysv1.SourceModrinth, "other-pack", "", "fabric"), modrinthPack, nil, nil, nil, ""},
		{"modrinth, a file of it gone", modrinth("Nte4eTsy"), without(modrinthPack, "mods/NoCC-1.3.1.jar"), nil, nil, nil, ""},
		{"modrinth, its loader gone", modrinth("Nte4eTsy"), without(modrinthPack, skyblockLauncher), nil, nil, nil, ""},
		{"modrinth, another loader installed", modrinth("Nte4eTsy"), with(modrinthPack, ".fabric-manifest.json", `"loader":"0.19.5"`, `"loader":"0.19.4"`), nil, nil, nil, ""},
		{"modrinth, a launcher of its own", modrinth("Nte4eTsy"), with(modrinthPack, ".fabric-manifest.json", `{"@type":"versions","game":"26.2","loader":"0.19.5","installer":"1.1.2"}`, `{"@type":"remote","uri":"https://example.org/fabric.jar"}`), nil, nil, nil, ""},
		{"modrinth, a resync asked for", modrinth("Nte4eTsy"), modrinthPack, []string{"MODRINTH_FORCE_SYNCHRONIZE=true"}, nil, nil, ""},
		{"modrinth, a loader reinstall asked for", modrinth("Nte4eTsy"), modrinthPack, []string{"MODRINTH_FORCE_MODLOADER_REINSTALL=TRUE"}, nil, nil, ""},
		{"modrinth, another game version", modrinth("Nte4eTsy"), modrinthPack, []string{"VERSION=1.21.4"}, nil, nil, ""},
		{"modrinth, a type of its own", modrinth("Nte4eTsy"), modrinthPack, []string{"TYPE=FABRIC"}, nil, nil, ""},
		{"modrinth, settings from a file of its own", modrinth("Nte4eTsy"), modrinthPack, []string{"LOAD_ENV_FROM_FILE=/data/pack.env"}, nil, nil, ""},
		{"modrinth, a pack at an address", packServer(avsysv1.SourceModrinth, "https://example.org/pack.mrpack", "", "fabric"), modrinthPack, nil, nil, nil, ""},

		{"uploaded fabric", uploaded(uploadSHA, "fabric"), uploadedFabric, nil, fabricAsked, fabric("1.21.4", "0.16.10"), "[avsys] meta.fabricmc.net does not answer: starting with the installed Test Pack\n"},
		{"uploaded quilt", uploaded(uploadSHA, "quilt"), uploadedQuilt, nil, []string{mojangVersions}, quilt, "[avsys] launchermeta.mojang.com does not answer: starting with the installed Test Pack"},
		// The image asks nothing for these.
		{"uploaded forge", uploaded(uploadSHA, "forge"), uploadedForge, nil, nil, nil, ""},
		{"uploaded neoforge", uploaded(uploadSHA, "neoforge"), uploadedNeoForge, nil, nil, nil, ""},
		{"uploaded, another file", uploaded(strings.Repeat("cd", 20), "fabric"), plus(uploadedFabric, ".avsys/content/"+strings.Repeat("cd", 20)+".mrpack"), nil, nil, nil, ""},
		{"uploaded, its file gone", uploaded(uploadSHA, "fabric"), without(uploadedFabric, uploadFile), nil, nil, nil, ""},
		{"uploaded, not yet", uploaded("", "fabric"), uploadedFabric, nil, nil, nil, ""},
		{"uploaded, a file of it gone", uploaded(uploadSHA, "fabric"), without(uploadedFabric, "server.txt"), nil, nil, nil, ""},
		{"uploaded quilt, its jar gone", uploaded(uploadSHA, "quilt"), without(uploadedQuilt, uploadQuiltJar), nil, nil, nil, ""},
		// The image's own server is told no loader for CurseForge.
		{"uploaded quilt, with files from CurseForge", uploaded(uploadSHA, "quilt"), uploadedQuilt, []string{"CURSEFORGE_FILES=jei:4593548"}, nil, nil, ""},

		{"curseforge forge", curseForge("cf-forge", "2001", "forge"), curseForgeForge, nil, curseForgeAsked, forge("FORGE", "1.20.1", "47.4.28"), "[avsys] api.curseforge.com does not answer: starting with the installed Test CF Pack cf-forge 1.0.2001"},
		{"curseforge neoforge", curseForge("cf-neoforge", "2003", "neoforge"), curseForgeNeoForge, nil, curseForgeAsked, forge("NEOFORGE", "1.21.1", "21.1.266"), "[avsys] api.curseforge.com does not answer"},
		{"curseforge fabric", curseForge("cf-fabric", "2002", "fabric"), curseForgeFabric, nil, append(curseForgeAsked, fabricAsked...), fabric("1.21.4", "0.16.10"), "[avsys] api.curseforge.com does not answer"},
		{"curseforge, no file given", curseForge("cf-forge", "", "forge"), curseForgeForge, nil, curseForgeAsked, forge("FORGE", "1.20.1", "47.4.28"), "[avsys] api.curseforge.com does not answer"},
		{"curseforge, its own API", curseForge("cf-forge", "2001", "forge"), curseForgeForge, []string{"CF_API_BASE_URL=http://127.0.0.1:9"}, []string{"http://127.0.0.1:9/v1/categories?gameId=432"}, forge("FORGE", "1.20.1", "47.4.28"), "[avsys] 127.0.0.1:9 does not answer"},
		{"curseforge, a world named", curseForge("cf-forge", "2001", "forge"), with(curseForgeForge, ".curseforge-manifest.json", `"levelName":null`, `"levelName":"Pack World"`), nil, curseForgeAsked, map[string]string{"MODPACK_PLATFORM": "unset", "TYPE": "FORGE", "VERSION": "1.20.1", "FORGE_VERSION": "47.4.28", "REMOVE_OLD_MODS": "false", "LEVEL": "Pack World"}, "[avsys] api.curseforge.com does not answer"},
		{"curseforge, its first start", curseForge("cf-forge", "2001", "forge"), nil, nil, nil, nil, ""},
		{"curseforge, another file", curseForge("cf-forge", "2004", "forge"), curseForgeForge, nil, nil, nil, ""},
		{"curseforge, another pack", curseForge("other-pack", "2001", "forge"), curseForgeForge, nil, nil, nil, ""},
		{"curseforge, its start gone", curseForge("cf-forge", "2001", "forge"), without(curseForgeForge, "run.sh"), nil, nil, nil, ""},
		{"curseforge, another build installed", curseForge("cf-forge", "2001", "forge"), with(curseForgeForge, ".forge-manifest.json", "47.4.28", "47.4.29"), nil, nil, nil, ""},
		{"curseforge, a resync asked for", curseForge("cf-forge", "2001", "forge"), curseForgeForge, []string{"CF_FORCE_SYNCHRONIZE=true"}, nil, nil, ""},
		{"curseforge, a loader reinstall asked for", curseForge("cf-forge", "2001", "forge"), curseForgeForge, []string{"CF_FORCE_REINSTALL_MODLOADER=true"}, nil, nil, ""},
		{"curseforge, a loader of its own", curseForge("cf-forge", "2001", "forge"), curseForgeForge, []string{"CF_MOD_LOADER_VERSION=47.4.29"}, nil, nil, ""},
		{"curseforge, its pack's file of its own", curseForge("cf-forge", "2001", "forge"), curseForgeForge, []string{"CF_MODPACK_ZIP=/data/pack.zip"}, nil, nil, ""},
		// NeoForge for 1.20.1 is looked up by the image on every start.
		{"curseforge neoforge on 1.20.1", curseForge("cf-neoforge", "2003", "neoforge"),
			with(with(curseForgeNeoForge, ".curseforge-manifest.json", `"minecraftVersion":"1.21.1"`, `"minecraftVersion":"1.20.1"`), ".neoforge-manifest.json", `"minecraftVersion":"1.21.1"`, `"minecraftVersion":"1.20.1"`), nil, nil, nil, ""},
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
				want := packEnv(c.mc, c.extra)
				if !up {
					for k, v := range c.kept {
						want[k] = v
					}
				}
				handed, said, asked := runPack(t, c.mc, c.files, c.extra, "200", down...)
				for _, k := range packVars {
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

// A pack from CurseForge whose Fabric site is down, CurseForge answering,
// is kept too: the image asks both. One from Modrinth is not: installed, the
// image asks Fabric nothing about it.
func TestKeepPackAsksTheLoaderOnlyWhereTheImageDoes(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	handed, said, _ := runPack(t, packServer(avsysv1.SourceCurseForge, "cf-fabric", "2002", "fabric"), curseForgeFabric, nil, "200", "meta.fabricmc.net")
	if handed["TYPE"] != "FABRIC" || handed["FABRIC_LAUNCHER_VERSION"] != "1.1.2" || !strings.Contains(said, "[avsys] meta.fabricmc.net does not answer: starting with the installed Test CF Pack cf-fabric 1.0.2002") {
		t.Errorf("curseforge: handed %v, said %q", handed, said)
	}
	handed, said, asked := runPack(t, packServer(avsysv1.SourceModrinth, "skyblock-plus", "Nte4eTsy", "fabric"), modrinthPack, nil, "200", "meta.fabricmc.net")
	if handed["MODPACK_PLATFORM"] != "MODRINTH" || strings.Contains(said, "does not answer") || strings.Join(asked, " ") != modrinthAPI+"/v2/tag/project_type" {
		t.Errorf("modrinth: handed %v, said %q, asked %q", handed, said, asked)
	}
}

// CurseForge answers without the key the image sends it: a refusal is an
// answer, and a server error is none.
func TestKeepPackCurseForgeAnswers(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	mc := packServer(avsysv1.SourceCurseForge, "cf-forge", "2001", "forge")
	for code, kept := range map[string]bool{"200": false, "403": false, "404": false, "500": true, "503": true} {
		handed, said, _ := runPack(t, mc, curseForgeForge, nil, code)
		if (handed["TYPE"] == "FORGE") != kept || strings.Contains(said, "does not answer") != kept {
			t.Errorf("%s: handed %v, said %q", code, handed, said)
		}
	}
}

// A pack's server keeps its content as one without a pack does, and sets
// its mods up for the ports opened for them.
func TestPackScriptKeepsContent(t *testing.T) {
	for _, source := range []avsysv1.ContentSource{avsysv1.SourceModrinth, avsysv1.SourceUpload, avsysv1.SourceCurseForge} {
		script := packScript(&avsysv1.ModpackSpec{Source: source, Loader: "fabric"})
		if !strings.Contains(script, keepPack+keepContent+modConfig+"exec "+imageStart) {
			t.Errorf("%s: %q", source, script)
		}
		if world := strings.HasPrefix(script, keepWorld); world != (source != avsysv1.SourceCurseForge) {
			t.Errorf("%s: leaves the world alone: %v", source, world)
		}
	}
}
