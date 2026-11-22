package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
)

const (
	MinecraftContainer = "minecraft"
	WorkspaceContainer = "workspace"

	MinecraftPort = 25565
	RCONPort      = 25575
	WorkspacePort = 2222

	// WorkspaceSecret holds the authorized_keys file trusted by workspace
	// containers. There is one per org namespace.
	WorkspaceSecret = "avsys-workspace"

	// ContentDir is where files uploaded through the panel are kept on the
	// server's volume. The server image copies them into place on start.
	ContentDir = "/data/.avsys/content"

	annotationSpecHash = "avsys.dev/spec-hash"

	defaultImageRepo = "itzg/minecraft-server"
	// The uid/gid itzg/minecraft-server runs the game as. The workspace uses the
	// same so files stay editable from both sides.
	runAsID = 1000
)

func RCONSecretName(server string) string { return server + "-rcon" }
func DataPVCName(server string) string    { return server + "-data" }

func labelsFor(mc *avsysv1.MinecraftServer) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":       "minecraft",
		"app.kubernetes.io/instance":   mc.Name,
		"app.kubernetes.io/managed-by": "avsys",
		avsysv1.LabelServer:            mc.Name,
	}
}

// WantsMinecraft reports whether the game container should be running.
func WantsMinecraft(mc *avsysv1.MinecraftServer) bool {
	if mc.Spec.State == avsysv1.StateStopped {
		return false
	}
	// A restore is rewriting the files, or was cut short while doing so.
	// Whatever the spec says, and whoever changed it, the game does not run
	// on them until a restore has completed.
	if _, restoring := mc.Annotations[avsysv1.AnnotationRestoring]; restoring {
		return false
	}
	// Nor while an import puts its files in, or after one that did not finish.
	if _, importing := mc.Annotations[avsysv1.AnnotationImporting]; importing {
		return false
	}
	_, asleep := mc.Annotations[avsysv1.AnnotationAsleep]
	return !asleep
}

// GameJava is the Java release a game version is built for, or 0 when that is
// not known (LATEST, snapshots, versions newer than this code).
// GameJavaOf is the Java a server's game version ships with: what the
// version's name says, or where it does not, what was found out when the
// version was chosen. 0 when neither is known.
func GameJavaOf(spec *avsysv1.MinecraftServerSpec) int {
	if java := GameJava(GameVersionOf(spec)); java > 0 {
		return java
	}
	if spec.Modpack != nil {
		return 0
	}
	return int(spec.GameJava)
}

func leadingDigits(s string) string {
	for i, r := range s {
		if r < '0' || r > '9' {
			return s[:i]
		}
	}
	return s
}

func GameJava(version string) int {
	parts := strings.Split(version, ".")
	if len(parts) < 2 {
		return 0
	}
	// A pre-release says which release it is of: 26.3-rc-3, 1.21.11-pre5.
	major, err1 := strconv.Atoi(parts[0])
	minor, err2 := strconv.Atoi(leadingDigits(parts[1]))
	if err1 != nil || err2 != nil {
		return 0
	}
	if major == 26 {
		// The first year-numbered releases.
		return 25
	}
	if major != 1 {
		return 0
	}
	patch := 0
	if len(parts) > 2 {
		patch, _ = strconv.Atoi(leadingDigits(parts[2]))
	}
	switch {
	case minor <= 16:
		return 8
	case minor < 20 || (minor == 20 && patch < 5):
		return 17
	default:
		return 21
	}
}

// javaTag is the itzg/minecraft-server tag that carries at least the given
// Java release.
func javaTag(java int) string {
	for _, have := range []int{8, 11, 17, 21, 25} {
		if java <= have {
			return "java" + strconv.Itoa(have)
		}
	}
	return "latest"
}

// ImageForJava is the server image that carries at least the given Java
// release.
func ImageForJava(java int) string { return defaultImageRepo + ":" + javaTag(java) }

// RanVersion is the game version in what a server's game says it runs when
// pinged, its status's version: after the software's name, as in "Paper
// 1.21.4", or alone. "" when it has said nothing.
func RanVersion(status string) string {
	fields := strings.Fields(status)
	if len(fields) == 0 {
		return ""
	}
	return fields[len(fields)-1]
}

// GameVersionOf is the game version a server runs, as far as it is known: for
// a modpack that is whatever the pack was built for.
func GameVersionOf(spec *avsysv1.MinecraftServerSpec) string {
	if spec.Modpack != nil {
		return spec.Modpack.GameVersion
	}
	return spec.Version
}

// JavaOf reports the Java a server's game version ships with and the Java the
// server actually runs on, which is higher when its content needs more. Both
// are 0 when the game version is not known.
func JavaOf(spec *avsysv1.MinecraftServerSpec) (game, effective int) {
	game = GameJavaOf(spec)
	if game == 0 {
		return 0, 0
	}
	effective = game
	for _, m := range spec.Mods {
		effective = max(effective, int(m.Java))
	}
	return game, effective
}

// imageFor chooses the server image. The Java in it is what the game version
// needs, raised to what the installed content was compiled for: newer Java
// runs older code, never the other way around.
func imageFor(mc *avsysv1.MinecraftServer) string {
	if mc.Spec.Image != "" {
		return mc.Spec.Image
	}
	_, java := JavaOf(&mc.Spec)
	if java == 0 {
		// Unknown game version: run whatever the image currently ships.
		return defaultImageRepo + ":latest"
	}
	return defaultImageRepo + ":" + javaTag(java)
}

// heapSize is the Java heap for a container memory limit, in the form the
// image's MEMORY variable takes. A JVM running Minecraft needs memory beyond
// its heap (code, threads, network buffers, the collector itself) that is
// mostly a fixed cost rather than a share, so a flat allowance plus a tenth
// is held back. A plain percentage starves small servers: at 75% a 2Gi
// server was killed for exceeding its limit with one player online.
func heapSize(limit resource.Quantity) string {
	const reserveMi = 768
	limitMi := limit.Value() / (1 << 20)
	heap := limitMi - reserveMi - limitMi/10
	// Below about 2Gi the allowance would leave almost nothing to play in.
	if floor := limitMi / 2; heap < floor {
		heap = floor
	}
	return strconv.FormatInt(heap, 10) + "M"
}

// ConsoleDir is shared by the game and the workspace, and ConsolePipe in it
// is the game's own console input: a line written there is a command typed
// at the server console. That is how commands from the panel come from
// "Server". Over RCON the game calls their sender "Rcon", in chat and in
// its log.
const (
	ConsoleDir  = "/run/avsys"
	ConsolePipe = ConsoleDir + "/console-in"
)

var consoleMount = corev1.VolumeMount{Name: "console", MountPath: ConsoleDir}

func minecraftEnv(mc *avsysv1.MinecraftServer, cfg Config) []corev1.EnvVar {
	env := map[string]string{
		"EULA":                           strconv.FormatBool(mc.Spec.EULA),
		"MEMORY":                         heapSize(mc.Spec.Resources.Memory),
		"MODRINTH_DOWNLOAD_DEPENDENCIES": "required",
		"CREATE_CONSOLE_IN_PIPE":         "true",
		"CONSOLE_IN_NAMED_PIPE":          ConsolePipe,
	}
	// Set only when asked for, so that a server made before these were
	// offered keeps the environment, and the pod, it has.
	if on := mc.Spec.JVM.AikarFlags; on != nil && *on {
		env["USE_AIKAR_FLAGS"] = "true"
	}
	// After the heap and Aikar's on the command line, so that one given here
	// wins over theirs: the image puts JVM_OPTS last.
	if extra := strings.Join(strings.Fields(mc.Spec.JVM.ExtraFlags), " "); extra != "" {
		env["JVM_OPTS"] = extra
	}

	switch {
	case mc.Spec.Modpack != nil && mc.Spec.Modpack.Source == avsysv1.SourceModrinth:
		env["MODPACK_PLATFORM"] = "MODRINTH"
		env["MODRINTH_MODPACK"] = mc.Spec.Modpack.Project
		if mc.Spec.Modpack.Version != "" {
			env["MODRINTH_VERSION"] = mc.Spec.Modpack.Version
		}
	case mc.Spec.Modpack != nil && mc.Spec.Modpack.Source == avsysv1.SourceUpload:
		// The image takes a path to a .mrpack in the container. It installs
		// the pack from it on every start: the files the index lists are
		// fetched from their own addresses where they are not there yet,
		// and the pack's own files (overrides) are put in place again. One
		// not uploaded yet has no path, and the image says so.
		env["MODPACK_PLATFORM"] = "MODRINTH"
		env["MODRINTH_MODPACK"] = ""
		if file := mc.Spec.Modpack.UploadedFile(); file != "" {
			env["MODRINTH_MODPACK"] = ContentDir + "/" + file
		}
	case mc.Spec.Modpack != nil && mc.Spec.Modpack.Source == avsysv1.SourceCurseForge:
		env["MODPACK_PLATFORM"] = "AUTO_CURSEFORGE"
		env["CF_SLUG"] = mc.Spec.Modpack.Project
		if mc.Spec.Modpack.Version != "" {
			env["CF_FILE_ID"] = mc.Spec.Modpack.Version
		}
		// Files of the pack whose authors allow downloads only on
		// CurseForge itself are looked for here, where they can be uploaded.
		env["CF_DOWNLOADS_REPO"] = CurseForgeDownloads
	default:
		env["TYPE"] = strings.ToUpper(string(mc.Spec.Loader))
		env["VERSION"] = mc.Spec.Version
		if v := mc.Spec.LoaderVersion; v != "" {
			switch mc.Spec.Loader {
			case avsysv1.LoaderFabric:
				env["FABRIC_LOADER_VERSION"] = v
			case avsysv1.LoaderQuilt:
				env["QUILT_LOADER_VERSION"] = v
			case avsysv1.LoaderForge:
				env["FORGE_VERSION"] = v
			case avsysv1.LoaderNeoForge:
				env["NEOFORGE_VERSION"] = v
			}
		}
	}
	var modrinth, curseforge, modURLs, pluginURLs, datapackURLs []string
	for _, m := range mc.Spec.Mods {
		switch m.Source {
		case avsysv1.SourceModrinth:
			ref := m.Project
			if m.Kind == avsysv1.KindDatapack {
				ref = "datapack:" + ref
			}
			if m.Version != "" {
				ref += ":" + m.Version
			}
			modrinth = append(modrinth, ref)
		case avsysv1.SourceCurseForge:
			// The image installs mods and plugins from CurseForge itself,
			// but not datapacks: those are fetched from where CurseForge
			// keeps the file, as one from a URL is.
			if m.Kind == avsysv1.KindDatapack {
				if u := CurseForgeFileURL(m.Version, m.File); u != "" {
					datapackURLs = append(datapackURLs, u)
				}
				continue
			}
			ref := m.Project
			if m.Version != "" {
				ref += ":" + m.Version
			}
			curseforge = append(curseforge, ref)
		case avsysv1.SourceURL, avsysv1.SourceUpload:
			// An upload kept where it was found, in the server's own files,
			// is there already: nothing is put over it.
			if m.Source == avsysv1.SourceUpload && m.File != "" {
				continue
			}
			// The image takes a URL or a path inside the container alike.
			location := m.Project
			if m.Source == avsysv1.SourceUpload {
				location = ContentDir + "/" + m.Project
			}
			switch m.Kind {
			case avsysv1.KindPlugin:
				pluginURLs = append(pluginURLs, location)
			case avsysv1.KindDatapack:
				datapackURLs = append(datapackURLs, location)
			default:
				modURLs = append(modURLs, location)
			}
		}
	}
	// These are always set, even when empty, so that removing the last entry
	// from the spec also removes its files.
	env["MODRINTH_PROJECTS"] = strings.Join(modrinth, "\n")
	env["CURSEFORGE_FILES"] = strings.Join(curseforge, "\n")
	env["MODS"] = strings.Join(modURLs, "\n")
	env["PLUGINS"] = strings.Join(pluginURLs, "\n")
	if len(datapackURLs) > 0 {
		env["DATAPACKS"] = strings.Join(datapackURLs, "\n")
	}

	if rp := mc.Spec.ResourcePack; rp != nil && rp.URL != "" {
		env["RESOURCE_PACK"] = rp.URL
		// One uploaded to the panel is fetched from wherever the panel
		// serves packs now, which may have changed since.
		if rp.Uploaded != nil && rp.SHA1 != "" && cfg.PackURL != "" {
			env["RESOURCE_PACK"] = cfg.PackURL + "/packs/" + rp.SHA1 + ".zip"
		}
		env["RESOURCE_PACK_SHA1"] = rp.SHA1
		env["RESOURCE_PACK_ENFORCE"] = strings.ToUpper(strconv.FormatBool(rp.Enforce))
		// Always set, so that one taken away is gone from server.properties
		// too, where the image would otherwise leave the one before.
		env["RESOURCE_PACK_PROMPT"] = packPrompt(rp.Prompt)
	}

	if len(mc.Spec.Properties) > 0 {
		keys := make([]string, 0, len(mc.Spec.Properties))
		for k := range mc.Spec.Properties {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var b strings.Builder
		for _, k := range keys {
			fmt.Fprintf(&b, "%s=%s\n", k, mc.Spec.Properties[k])
		}
		env["CUSTOM_SERVER_PROPERTIES"] = b.String()
	}
	if ops := forImage(mc.Spec.Ops); len(ops) > 0 {
		env["OPS"] = strings.Join(ops, "\n")
	}
	if whitelist := forImage(mc.Spec.Whitelist); len(whitelist) > 0 {
		env["WHITELIST"] = strings.Join(whitelist, "\n")
	}

	// Set only where a port was opened for a mod, so that other servers
	// keep the environment, and the pod, they have.
	if ports := modPortsEnv(mc, mc.Status.Ports); ports != "" {
		env["AVSYS_MOD_PORTS"] = ports
	}

	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]corev1.EnvVar, 0, len(keys)+1+len(mc.Spec.Env))
	for _, k := range keys {
		out = append(out, corev1.EnvVar{Name: k, Value: env[k]})
	}
	out = append(out, rconPasswordEnv(mc))
	if UsesCurseForge(&mc.Spec) {
		out = append(out, curseForgeKeyEnv())
	}
	// A server's own environment comes last and wins, except over launch
	// flags set in their own fields: an older entry for those would
	// otherwise override what its settings now say.
	launch := mc.Spec.JVM.AikarFlags != nil || mc.Spec.JVM.ExtraFlags != ""
	for _, e := range mc.Spec.Env {
		if launch && (e.Name == "USE_AIKAR_FLAGS" || e.Name == "JVM_OPTS") {
			continue
		}
		out = append(out, e)
	}
	return out
}

// The image's own start, which it runs when given no command, and the file
// it writes, beside the jar, once it has installed Paper or Folia: the jar,
// the game version and which of the two, as KEY="value" lines.
const (
	imageStart = "/image/scripts/start"
	paperEnv   = "/data/.paper.env"
)

// paperAPI is where Paper and Folia's builds are listed, as the image asks.
const paperAPI = "https://fill.papermc.io"

// startHelpers is what the game container's commands below share: asking a
// site, and reading the records the image keeps in /data.
const startHelpers = `# Whether a site answers, within a few seconds.
answers() { curl -fsS -o /dev/null --connect-timeout 3 -m 5 "$@" 2>/dev/null; }
unanswered() { h=${1#*://}; echo "[avsys] ${h%%/*} does not answer: starting with the installed $2"; }
truthy() { case $(echo "${1:-}" | tr A-Z a-z) in true | yes | on | 1) return 0 ;; esac; return 1; }
# Settings the image loads from a file of the server's own before it installs.
loaded=${LOAD_ENV_FROM_FILE:-}${LOAD_ENV_FROM_ARCHIVE:-}
[ "${LOAD_ENV_FROM_GENERIC_PACK:-false}" = false ] || loaded=pack
# The value of "key":"value" in a record the image keeps as JSON.
recorded() { [ -f "$2" ] && sed -n "s/.*\"$1\"[[:space:]]*:[[:space:]]*\"\([^\"]*\)\".*/\1/p" "$2" | tail -n 1; }
# The value of KEY="value" in a record the image keeps as lines of those.
setting() { [ -f "$2" ] && sed -n "s/^$1=\"\(.*\)\"\$/\1/p" "$2" | tail -n 1; }
# The jar a record of KEY="value" lines says the image installed, if it is there.
jarOf() { j=$(setting SERVER "$1" 2>/dev/null); case $j in /*) ;; ?*) j=/data/${j#./} ;; esac; [ -f "$j" ] && echo "$j"; }
# The strings in the array $1 of the image's record $2, a line each. It
# fails on a record it cannot read plainly.
listed() {
  [ -f "$2" ] || return 1
  awk -v k="\"$1\"" '{ s = s $0 "\n" } END {
    i = index(s, k); if (!i) exit 1
    s = substr(s, i + length(k))
    sub(/^[ \t\n]*:[ \t\n]*\[/, "[", s)
    if (substr(s, 1, 1) != "[") exit 1
    s = substr(s, 2)
    for (;;) {
      sub(/^[ \t\n]+/, "", s); c = substr(s, 1, 1)
      if (c == "]") exit 0
      if (c == ",") { s = substr(s, 2); continue }
      if (c != "\"") exit 1
      s = substr(s, 2); j = index(s, "\"")
      if (!j) exit 1
      v = substr(s, 1, j - 1)
      if (index(v, "\\")) exit 1
      print v; s = substr(s, j + 1)
    }
  }' "$2"
}
# Started as a server of the image's own, on the jar installed.
custom() { export TYPE=CUSTOM CUSTOM_SERVER="$1" FAMILY="$2" MODRINTH_LOADER="${MODRINTH_LOADER:-$3}"; }
`

// keepBuild lets a Paper or Folia server start on the build it already has
// when Paper's site does not answer. The image asks that site for the newest
// build of the server's game version on every start, even with a build
// given, and the start fails while it does not answer. So the site is asked
// first, for a few seconds: when it answers, the image goes on as it always
// does, and updates the build within the same game version; when it does
// not, the image is told to use the jar the server has, and not to fetch
// the default config files either, which it does on every start though the
// server has them. The record of that jar is in the server's files, so it
// goes wherever they go: snapshots, copies, templates, restores, imports. A
// version or kind other than the one installed, a jar that is gone, or a
// download, jar or file of settings of the server's own in its environment
// leave the start to the image, which installs as it always has, and
// records what it got. AVSYS_PAPER_API, in a server's own environment, asks
// another site in place of Paper's.
const keepBuild = `f=` + paperEnv + `
# The build asked for, as the image reads it: for Folia, its own name for
# it comes before Paper's older one.
build=${PAPERBUILD:-}
[ "$kind" = FOLIA ] && [ -n "${FOLIABUILD:-}" ] && build=$FOLIABUILD
build=${PAPER_BUILD:-$build}
if [ -z "${PAPER_CUSTOM_JAR:-}${PAPER_DOWNLOAD_URL:-}${FOLIA_DOWNLOAD_URL:-}$loaded" ] && [ -f "$f" ]; then
  get() { sed -n "s/^$1=\"\(.*\)\"\$/\1/p" "$f" | tail -n 1; }
  jar=$(get SERVER)
  if [ -n "$jar" ] && [ -f "$jar" ] && [ "$(get TYPE)" = "$kind" ] && [ "$(get VERSION)" = "${VERSION:-}" ] &&
    { [ -z "$build" ] || [ "${jar%-"$build".jar}" != "$jar" ]; }; then
    project=$(echo "$kind" | tr A-Z a-z)
    if answers "${AVSYS_PAPER_API:-` + paperAPI + `}/v3/projects/$project/versions/$VERSION/builds/latest"; then
      echo "[avsys] Paper's site answers: looking for a newer build of $VERSION than ${jar##*/}"
    else
      echo "[avsys] Paper's site does not answer: starting on the installed ${jar##*/}"
      export PAPER_CUSTOM_JAR="$jar" SKIP_DOWNLOAD_DEFAULTS="${SKIP_DOWNLOAD_DEFAULTS:-true}"
    fi
  fi
fi
`

// The sites the image asks, on every start, about the loaders it installs,
// as it asks them: Mojang's list of game versions, Fabric's, Quilt's,
// Forge's, NeoForge's and Purpur's.
const (
	mojangVersions = "https://launchermeta.mojang.com/mc/game/version_manifest_v2.json"
	fabricMeta     = "https://meta.fabricmc.net"
	quiltLoaders   = "https://maven.quiltmc.org/repository/release/org/quiltmc/quilt-loader/maven-metadata.xml"
	forgeVersions  = "https://files.minecraftforge.net/net/minecraftforge/forge/promotions_slim.json"
	neoForgeBuilds = "https://maven.neoforged.net/releases/net/neoforged/neoforge/maven-metadata.xml"
	purpurAPI      = "https://api.purpurmc.org"
)

// keepLoader lets a Fabric, Quilt, Forge, NeoForge or Purpur server start on
// the loader it already has when the sites it came from do not answer, as
// keepBuild does for Paper. On every start, with the loader installed, the
// image still asks:
//   - Fabric's site for its newest installer, and its newest loader when
//     none is given. Once it knows both are the ones it has, it asks
//     nothing more.
//   - Forge's site, or NeoForge's, for the build when none is given. A
//     build given that is the one installed is asked about nowhere.
//   - Mojang's list of game versions, for Quilt, and Quilt's site for its
//     newest loader when none is given.
//   - Purpur's site, for its newest build.
//
// So the site is asked first, for a few seconds, only where the image would
// ask it, and only when what the image recorded it installed is the game
// version and the build asked for, and still there. When it answers, the
// image goes on as it always does. When it does not, the image is told the
// versions it has, for Fabric, Forge and NeoForge, and asks nothing; Quilt
// and Purpur, which it cannot be told that, are started as a server of the
// image's own (TYPE=CUSTOM) on the jar installed, with the mods or plugins
// where they were. A first start, another game version or build, a
// download, installer or file of settings of the server's own, or a
// reinstall asked for, are left to the image as before. NeoForge for
// 1.20.1, which the image always looks up, is too. FABRIC_META_BASE_URL,
// in a server's own environment, is where the image and this ask Fabric.
// Vanilla needs none of this: installed, the image asks nothing.
const keepLoader = `# Whether a version is one of its own, and not the newest or whatever is recommended.
given() { case $(echo "$1" | tr A-Z a-z) in "" | latest | recommended | beta) return 1 ;; esac; }
# Whether what is asked for is what is installed: the same, or none given.
same() { ! given "$1" || [ "$1" = "$2" ]; }
if [ -z "$loaded" ]; then
  case $kind in
  FABRIC)
    m=/data/.fabric-manifest.json
    loader=$(recorded loader "$m") installer=$(recorded installer "$m") jar=$(recorded launcherPath "$m")
    case $jar in /*) ;; ?*) jar=/data/${jar#./} ;; esac
    want=${FABRIC_LAUNCHER_VERSION:-${FABRIC_INSTALLER_VERSION:-}}
    if [ -z "${FABRIC_LAUNCHER:-}${FABRIC_LAUNCHER_URL:-}" ] && ! truthy "${FABRIC_FORCE_REINSTALL:-}" &&
      grep -q '"origin":{"@type":"versions"' "$m" 2>/dev/null && [ -n "$loader" ] && [ -n "$installer" ] && [ -f "$jar" ] &&
      [ "$(recorded game "$m")" = "${VERSION:-}" ] && same "${FABRIC_LOADER_VERSION:-}" "$loader" && same "$want" "$installer" &&
      { ! given "${FABRIC_LOADER_VERSION:-}" || ! given "$want"; }; then
      site=${FABRIC_META_BASE_URL:-` + fabricMeta + `}
      if ! answers "$site/v2/versions/installer"; then
        unanswered "$site" "Fabric loader $loader"
        export FABRIC_LOADER_VERSION="$loader" FABRIC_LAUNCHER_VERSION="$installer"
      fi
    fi
    ;;
  FORGE | NEOFORGE)
    if [ "$kind" = FORGE ]; then
      m=/data/.forge-manifest.json want=${FORGE_VERSION:-${FORGEVERSION:-}} site=` + forgeVersions + ` own=${FORGE_INSTALLER:-}${FORGE_INSTALLER_URL:-} again=${FORGE_FORCE_REINSTALL:-}
    else
      m=/data/.neoforge-manifest.json want=${NEOFORGE_VERSION:-} site=` + neoForgeBuilds + ` own=${NEOFORGE_INSTALLER:-} again=${NEOFORGE_FORCE_REINSTALL:-}
      [ "${VERSION:-}" = 1.20.1 ] && own=always
    fi
    have=$(recorded forgeVersion "$m") entry=$(recorded serverEntry "$m")
    case $entry in /*) ;; ?*) entry=/data/$entry ;; esac
    if [ -z "$own" ] && ! truthy "$again" && ! given "$want" && [ -n "$have" ] && [ -f "$entry" ] &&
      [ "$(recorded minecraftVersion "$m")" = "${VERSION:-}" ] && ! grep -q '"installerUrl":"' "$m"; then
      if ! answers "$site"; then
        if [ "$kind" = FORGE ]; then
          unanswered "$site" "Forge $have"
          export FORGE_VERSION="$have"
        else
          unanswered "$site" "NeoForge $have"
          export NEOFORGE_VERSION="$have"
        fi
      fi
    fi
    ;;
  QUILT)
    m=/data/.quilt-manifest.json
    loader=$(recorded loaderVersion "$m") jar=$(jarOf /data/.quilt.env)
    if [ -z "${QUILT_LAUNCHER:-}${QUILT_LAUNCHER_URL:-}${QUILT_INSTALLER_URL:-}${CURSEFORGE_FILES:-}" ] && ! truthy "${QUILT_FORCE_REINSTALL:-}" &&
      [ -n "$loader" ] && [ -n "$jar" ] && [ "$(recorded minecraftVersion "$m")" = "${VERSION:-}" ] && same "${QUILT_LOADER_VERSION:-}" "$loader"; then
      silent=
      answers "` + mojangVersions + `" || silent=` + mojangVersions + `
      [ -n "$silent" ] || given "${QUILT_LOADER_VERSION:-}" || answers "` + quiltLoaders + `" || silent=` + quiltLoaders + `
      if [ -n "$silent" ]; then
        unanswered "$silent" "Quilt loader $loader"
        custom "$jar" FABRIC quilt
      fi
    fi
    ;;
  PURPUR)
    m=/data/.purpur-manifest.json
    build=$(recorded build "$m") jar=$(jarOf /data/.purpur.env)
    if [ -z "${PURPUR_DOWNLOAD_URL:-}" ] && [ -n "$build" ] && [ -n "$jar" ] &&
      [ "$(recorded minecraftVersion "$m")" = "${VERSION:-}" ] && same "${PURPUR_BUILD:-}" "$build"; then
      if ! answers "` + purpurAPI + `/v2/purpur/$VERSION"; then
        unanswered ` + purpurAPI + ` "${jar##*/}"
        custom "$jar" SPIGOT purpur
      fi
    fi
    ;;
  esac
fi
`

// The records the image keeps, in /data, of what it installed from Modrinth
// for MODRINTH_PROJECTS, and, in the folder plugins or mods go to, of what
// it got for the MODS and PLUGINS lists. Each is written once all of its
// list is installed, as JSON: the files, relative to its folder, and for
// Modrinth the projects it was given, as given.
const (
	// ImportedRecord is the panel's record of the Modrinth projects it took
	// into a server's list from its own files, which the image has not
	// installed yet: "ref<TAB>file" a line, ref as MODRINTH_PROJECTS has it,
	// the file relative to /data.
	ImportedRecord = "/data/.avsys/imported-modrinth"
	modrinthRecord = "/data/.modrinth-manifest.json"
	listRecord     = ".var-list-manifest.json"
)

// urlRecord is the start script's own record of the URLs the image is
// known to have installed a file from, kept with the server's files so it
// goes wherever they go.
const urlRecord = "/data/.avsys/urls"

// modrinthAPI is Modrinth's API, as the image asks it.
const modrinthAPI = "https://api.modrinth.com"

// keepContent lets a server start with the mods, plugins and datapacks it
// has when the sites they come from do not answer. On every start the image
// asks Modrinth about each project, and fetches each file from a URL again
// or asks whether it changed, and the start fails when one does not answer.
// So the sites are asked first, for a few seconds, and only about content
// the server already has as asked for:
//   - Modrinth projects, when the image's record says it installed exactly
//     these, each pinned to a version, and every file it got for them, their
//     dependencies too, is there. When Modrinth does not answer, the image is
//     not given the projects, and leaves its files and record as they are.
//   - A mod, plugin or datapack from a URL, when the file it was saved as,
//     the last part of the URL, is there, for a mod or plugin in the image's
//     record of that list, and the image is known to have installed it from
//     that very URL. The script notes the URLs it hands the image, and takes
//     them as installed once the image's record of the list, or the
//     datapack's file, is newer than the note. When the site gives no answer
//     or a server error, the image copies the file in place of the URL, or
//     for a datapack, leaves it. Any other answer, a refusal of the request
//     or a file not found too, is left to the image.
//
// Anything else is left to the image, as before: a first start, a project
// added, taken off or pinned to another version, a URL changed, a file
// missing, a world other than the one the datapacks are in, old files the
// image is told to remove first. MODRINTH_API_BASE_URL, in a server's own
// environment, is where the image and this ask Modrinth.
const keepContent = `level=${LEVEL:-world}
# Lines of a list as the image reads one: split at commas and line ends,
# without comments, trimmed.
lines() { printf '%s\n' "$1" | tr , '\n' | sed 's/#.*//; s/^[[:space:]]*//; s/[[:space:]]*$//; /^$/d'; }
# The file a URL is saved as, as far as it can be told: the last part of its path.
saved() { n=${1%%[?]*}; echo "${n##*/}"; }
offline() { h=${1#*://}; echo "[avsys] ${h%%/*} does not answer: starting with the installed $2" >&2; }
# A file of the server's own a list names that is not there is left out of
# it, and said, rather than the image refusing to start over it.
present() {
  lines "$1" | while IFS= read -r e; do
    case $e in
      /*) if [ ! -e "$e" ]; then echo "[avsys] $e is not there: starting without it" >&2; continue; fi ;;
    esac
    printf '%s\n' "$e"
  done
}
for v in MODS PLUGINS DATAPACKS; do
  eval "was=\${$v:-}"
  [ -n "$was" ] || continue
  now=$(present "$was")
  [ "$now" = "$(lines "$was")" ] || export "$v=$now"
done
# Files the image is told to remove before it installs.
removes=
for v in "${REMOVE_OLD_MODS:-}" "${REMOVE_OLD_DATAPACKS:-}"; do
  case $v in "" | [Ff][Aa][Ll][Ss][Ee]) ;; *) removes=yes ;; esac
done
m=` + modrinthRecord + `
r=` + ImportedRecord + `
want=$(lines "${MODRINTH_PROJECTS:-}" | sort -u)
# The image's record, and the panel's of what it took into the list from
# the server's own files, which the image has not installed yet: they are
# there all the same.
had= files= readable=yes
if [ -f "$m" ]; then
  { had=$(listed projects "$m") && files=$(listed files "$m"); } || readable=
fi
if [ -f "$r" ]; then
  had=$(printf '%s\n' "$had"; cut -f1 "$r")
  files=$(printf '%s\n' "$files"; cut -f2 "$r")
fi
had=$(printf '%s\n' "$had" | sed '/^$/d' | sort -u)
files=$(printf '%s\n' "$files" | sed '/^$/d' | awk '!seen[$0]++')
# Installed as asked for, each pinned to a version, each file there, and
# datapacks in this world.
if [ -n "$want" ] && [ -z "$removes" ] && [ -n "$readable" ] && [ "$had" = "$want" ] && [ -n "$files" ] &&
  ! printf '%s\n' "$want" | sed 's/^datapack://' | grep -qv : &&
  printf '%s\n' "$files" | {
    while IFS= read -r p; do
      case $p in */datapacks/*) case $p in "$level"/datapacks/*) ;; *) exit 1 ;; esac ;; esac
      [ -f "/data/$p" ] || exit 1
    done
  }; then
  names=$(printf '%s\n' "$files" | awk -F/ '{ printf "%s%s", (NR > 1 ? ", " : ""), $NF }')
  if answers "${MODRINTH_API_BASE_URL:-` + modrinthAPI + `}/v2/tag/project_type"; then
    echo "[avsys] Modrinth answers: checking the installed $names"
  else
    echo "[avsys] Modrinth does not answer: starting with the installed $names"
    unset MODRINTH_PROJECTS
  fi
fi
# Mods and plugins from URLs, in the one folder the image puts both in, and
# datapacks, in the world's.
case "${USES_MODS:-}${USES_PLUGINS:-}$kind" in
  PAPER | FOLIA | PURPUR) dir=${PLUGINS_OUT_DIR:-/data/plugins} ;;
  FABRIC | QUILT | FORGE | NEOFORGE) dir=${MODS_OUT_DIR:-/data/mods} ;;
  *) dir= ;;
esac
packs=/data/$level/datapacks
# The URLs the image is known to have installed a file from, and those it
# was handed at the last start: "list URL" or "datapack URL", a line each.
known=` + urlRecord + `
noted=$known.pending
# Whether the image installed what it was handed since: the whole list, once
# its record of it is newer than the note; a datapack, once its file is.
since() {
  case $1 in
    list) [ -n "$dir" ] && [ "$dir/` + listRecord + `" -nt "$noted" ] ;;
    datapack) [ "$packs/$(saved "$2")" -nt "$noted" ] ;;
  esac
}
if [ -f "$noted" ]; then
  while read -r k u; do
    [ -n "$u" ] || continue
    n=$(saved "$u")
    # Two of one name in one place: which the file came from cannot be told.
    [ "$(while read -r k2 u2; do [ "$k2" = "$k" ] && [ "$(saved "$u2")" = "$n" ] && echo x; done <"$noted")" = x ] || continue
    if since "$k" "$u" || { [ -f "$known" ] && grep -qxF "$k $u" "$known"; }; then echo "$k $u"; fi
  done <"$noted" 2>/dev/null >"$known.new" && mv "$known.new" "$known" 2>/dev/null
  rm -f "$noted" "$known.new" 2>/dev/null
fi
installed() { [ -f "$known" ] && grep -qxF "$1 $2" "$known"; }
# Whether a file's site is down: no answer at all, or a server error. Any
# other answer, a refusal or a file not found too, is the image's to deal
# with, as before.
down() {
  code=$(curl -sS -o /dev/null -I -L -w '%{http_code}' --connect-timeout 3 -m 5 "$1" 2>/dev/null)
  case $code in [1234][0-9][0-9]) return 1 ;; esac
}
asked=$(
  [ -z "$dir" ] || { lines "${MODS:-}"; lines "${PLUGINS:-}"; } | grep -E '^https?://' | sed 's/^/list /'
  printf '%s\n' "${DATAPACKS:-}" | tr ', \t' '\n\n\n' | grep -E '^https?://' | sed 's/^/datapack /'
)
kept() {
  got=$(listed files "$dir/` + listRecord + `") || got=
  lines "$1" | while IFS= read -r e; do
    case $e in
      http://* | https://*)
        n=$(saved "$e")
        if [ -n "$n" ] && [ -f "$dir/$n" ] && printf '%s\n' "$got" | grep -qxF -e "$n" && installed list "$e" && down "$e"; then
          offline "$e" "$n"
          e=$dir/$n
        fi
        ;;
    esac
    printf '%s\n' "$e"
  done
}
if [ -n "$dir" ] && [ -z "$removes" ]; then
  if [ -n "${MODS:-}" ]; then
    now=$(kept "$MODS")
    [ "$now" = "$(lines "$MODS")" ] || export MODS="$now"
  fi
  if [ -n "${PLUGINS:-}" ]; then
    now=$(kept "$PLUGINS")
    [ "$now" = "$(lines "$PLUGINS")" ] || export PLUGINS="$now"
  fi
fi
# Datapacks from URLs, which the image fetches again on every start.
if [ -n "${DATAPACKS:-}" ] && [ -z "$removes" ]; then
  was=$(printf '%s\n' "$DATAPACKS" | tr ', \t' '\n\n\n' | sed '/^$/d')
  now=$(printf '%s\n' "$was" | while IFS= read -r e; do
    case $e in
      (http://* | https://*)
        n=$(saved "$e")
        if [ -n "$n" ] && [ -f "$packs/$n" ] && installed datapack "$e" && down "$e"; then
          offline "$e" "$n"
          continue
        fi
        ;;
    esac
    printf '%s\n' "$e"
  done)
  [ "$now" = "$was" ] || export DATAPACKS="$now"
fi
# What the image is handed, known as installed once it has been.
mkdir -p "${known%/*}" 2>/dev/null && { [ -z "$asked" ] || printf '%s\n' "$asked"; } 2>/dev/null >"$noted"
`

// startScript is the game container's command for a server run by the
// image it is written for: what the server has installed is kept when the
// sites it came from do not answer, the mods ports were opened for are set
// up to use them, and then the image starts as it does by itself.
const startScript = `kind=$(echo "${TYPE:-}" | tr a-z A-Z)
` + startHelpers + keepBuild + keepLoader + keepContent + modConfig + `exec ` + imageStart + `
`

// modpackManifest is the image's record of what it installed from a Modrinth
// modpack: the files, relative to /data, as JSON.
const modpackManifest = "/data/.modrinth-modpack-manifest.json"

// keepWorld comes first in the game container's command for a server
// that runs a modpack in the Modrinth format, from Modrinth or uploaded.
// The image puts the pack's own files (its overrides) into /data as it
// installs the pack, over what is there, and later takes out the files its
// record lists that the next version of the pack does not have. A pack may
// have a world among them. Once the server has a world, chosen when it was
// made, put in since, or the pack's own from an earlier start, the world is
// the server's: the pack's world files are left out, and taken off the
// record, so that they are neither put over the world nor taken out of it.
// Its datapacks and per-world settings (serverconfig, as Forge keeps it)
// still come from the pack. Without jq, the image installs the pack as it
// does by itself.
const keepWorld = `level=${LEVEL:-world}
m=` + modpackManifest + `
# Only a plain name, which the image's patterns take as it is.
case $level in '' | .* | *[!A-Za-z0-9_-]*) level= ;; esac
if [ -n "$level" ] && [ -f "/data/$level/level.dat" ] && command -v jq >/dev/null 2>&1; then
  ok=yes
  if [ -f "$m" ]; then
    if jq --arg l "$level" '.files |= map(select(
        (startswith($l + "/") and (startswith($l + "/datapacks/") or startswith($l + "/serverconfig/") | not))
        or startswith($l + "_nether/") or startswith($l + "_the_end/") | not))' "$m" >"$m.avsys" 2>/dev/null; then
      # Written over in place, so that it stays the image's to write.
      cat "$m.avsys" >"$m" || ok=
    else
      ok=
    fi
    rm -f "$m.avsys"
  fi
  if [ -n "$ok" ]; then
    out="$level/*,${level}_nether/**,${level}_the_end/**"
    for d in region entities poi data playerdata stats advancements DIM-1 DIM1 dimensions "/data/$level"/*/; do
      d=${d%/}
      d=${d##*/}
      case $d in datapacks | serverconfig | *[!A-Za-z0-9_.-]*) continue ;; esac
      out="$out,$level/$d/**"
    done
    export MODRINTH_OVERRIDES_EXCLUSIONS="$out${MODRINTH_OVERRIDES_EXCLUSIONS:+,$MODRINTH_OVERRIDES_EXCLUSIONS}"
    echo "[avsys] The server has a world: the modpack's world files are left out"
  fi
fi
`

// The image's records of what it installed from a CurseForge modpack, as
// JSON, and of the server it set up from a modpack in either format, as
// KEY="value" lines: the jar or script it starts, the game version, the
// pack's name and version.
const (
	curseForgeManifest = "/data/.curseforge-manifest.json"
	modrinthPackEnv    = "/data/.install-modrinth.env"
	curseForgePackEnv  = "/data/.install-curseforge.env"
)

// curseForgeAPI is CurseForge's API, as the image asks it.
const curseForgeAPI = "https://api.curseforge.com"

// keepPack lets a server from a modpack start with the pack it has when the
// sites the image asks about it do not answer, as keepLoader does for a
// loader. On every start, with the pack installed, the image still asks:
//   - for a pack from Modrinth, Modrinth, about the version. Once it knows
//     it is the one installed, with all its files, it asks nothing more,
//     and puts nothing back.
//   - for an uploaded pack, which it reads again from the server's files
//     and whose own files it puts back, the loader's site: Fabric's, for its
//     newest installer, or Mojang's list of game versions, for Quilt. For
//     Forge or NeoForge, it asks nothing.
//   - for a pack from CurseForge, CurseForge, about the pack, and then
//     Fabric's site, for a Fabric pack. Its key is not needed to tell
//     whether CurseForge answers.
//
// So those sites are asked first, for a few seconds, only where the image
// would ask them, and only when what the image recorded it installed is the
// pack and version asked for, with every file of it there, and the loader
// it set up for it. When they answer, the image goes on as it always does.
// When one does not, the image is not given the pack, and starts the loader
// the pack has as a server of that loader, told the versions it has, as
// keepLoader would: Fabric, Forge and NeoForge asking nothing, and Quilt
// started as the image's own server (TYPE=CUSTOM) on its jar. The files are
// left as they are, the pack's own too, and REMOVE_OLD_MODS, which the image
// turns off for a pack, stays off. A first start, another pack or version,
// a file of it gone, a pack from an address or a file of settings of the
// server's own, or a reinstall asked for, are left to the image as before.
// NeoForge for 1.20.1, which the image always looks up, is too.
// MODRINTH_API_BASE_URL and CF_API_BASE_URL, in a server's own
// environment, are where the image and this ask Modrinth and CurseForge.
// FABRIC_META_BASE_URL is not: the image's pack step asks Fabric's own site
// whatever it says, and only its Fabric step, for TYPE=FABRIC, reads it.
const keepPack = `platform=$(echo "${MODPACK_PLATFORM:-${MOD_PLATFORM:-}}" | tr a-z A-Z)
# The pack's record, and of the server set up from it; the site the image
# asks about the pack, and about its loader; the game version and the
# loader the pack is for, and that loader's version.
m= env= api= meta= game= loader= want=
# The image's own TYPE is VANILLA: another one is a server's own.
case $(echo "${TYPE:-}" | tr a-z A-Z) in "" | VANILLA) own= ;; *) own=yes ;; esac
if [ -z "$own$loaded" ]; then
  case $platform in
  MODRINTH)
    m=` + modpackManifest + ` env=` + modrinthPackEnv + `
    ref=${MODRINTH_MODPACK:-${MODRINTH_PROJECT:-}} version=${MODRINTH_VERSION:-${MODRINTH_VERSION_ID:-}}
    if ! truthy "${MODRINTH_FORCE_SYNCHRONIZE:-}" && ! truthy "${MODRINTH_FORCE_MODLOADER_REINSTALL:-${FORCE_MODLOADER_REINSTALL:-}}"; then
      case $ref in
      /*.mrpack)
        # A pack file in the server's own files, recorded under its name.
        n=${ref##*/}
        if [ -f "$ref" ] && [ "$(recorded projectSlug "$m")" = "${n%.mrpack}" ]; then
          api=none
        fi
        ;;
      *[/:]*) ;;
      ?*)
        if { [ -n "$version" ] && [ "$(recorded versionId "$m")" = "$version" ]; } ||
          { [ -z "$version" ] && [ "$(recorded projectSlug "$m")" = "$ref" ]; }; then
          api=${MODRINTH_API_BASE_URL:-` + modrinthAPI + `}/v2/tag/project_type
        fi
        ;;
      esac
      game=$(recorded minecraft "$m")
      for l in fabric-loader quilt-loader forge neoforge; do
        want=$(recorded "$l" "$m")
        [ -z "$want" ] || { loader=$l; break; }
      done
      case ${VERSION:-} in "" | [Ll][Aa][Tt][Ee][Ss][Tt] | [Ss][Nn][Aa][Pp][Ss][Hh][Oo][Tt]) ;; *) [ "$VERSION" = "$game" ] || api= ;; esac
    fi
    ;;
  AUTO_CURSEFORGE)
    m=` + curseForgeManifest + ` env=` + curseForgePackEnv + `
    if [ -n "${CF_SLUG:-}" ] && [ -z "${CF_PAGE_URL:-}${CF_MODPACK_ZIP:-}${CF_MODPACK_MANIFEST:-}${CF_MOD_LOADER_VERSION:-}" ] &&
      ! truthy "${CF_FORCE_SYNCHRONIZE:-}" && ! truthy "${CF_FORCE_REINSTALL_MODLOADER:-}" &&
      [ "$(recorded slug "$m")" = "$CF_SLUG" ] &&
      { [ -z "${CF_FILE_ID:-}" ] || [ "$(sed -n 's/.*"fileId"[[:space:]]*:[[:space:]]*\([0-9]*\).*/\1/p' "$m" | tail -n 1)" = "$CF_FILE_ID" ]; }; then
      api=${CF_API_BASE_URL:-` + curseForgeAPI + `}/v1/categories?gameId=432
      game=$(recorded minecraftVersion "$m") want=$(recorded modLoaderId "$m")
      case $want in fabric-*) loader=fabric-loader ;; forge-*) loader=forge ;; neoforge-*) loader=neoforge ;; esac
    fi
    ;;
  esac
fi
# Every file of the pack there, as the image recorded them.
if [ -n "$api" ] && [ -n "$game" ] && files=$(listed files "$m") &&
  printf '%s\n' "$files" | { while IFS= read -r p; do [ -z "$p" ] || [ -e "/data/$p" ] || exit 1; done; }; then
  # The loader the image set up for the pack, still there.
  have= jar=
  case $loader in
  fabric-loader)
    l=/data/.fabric-manifest.json
    have=$(recorded loader "$l") installer=$(recorded installer "$l") jar=$(recorded launcherPath "$l")
    case $jar in /*) ;; ?*) jar=/data/${jar#./} ;; esac
    [ "$(recorded @type "$l")" = versions ] && [ "$(recorded game "$l")" = "$game" ] && [ -n "$installer" ] && [ -f "$jar" ] || have=
    # Fabric's site is asked for an uploaded pack, and for one from CurseForge.
    [ "$platform" = MODRINTH ] && [ "$api" != none ] || meta=` + fabricMeta + `/v2/versions/installer
    ;;
  quilt-loader)
    l=/data/.quilt-manifest.json
    have=$(recorded loaderVersion "$l") jar=$(jarOf "$env")
    { [ "$(recorded minecraftVersion "$l")" = "$game" ] && [ -n "$jar" ] && [ -z "${CURSEFORGE_FILES:-}" ]; } || have=
    [ "$api" != none ] || meta=` + mojangVersions + `
    ;;
  forge | neoforge)
    l=/data/.$loader-manifest.json
    have=$(recorded forgeVersion "$l") jar=$(recorded serverEntry "$l")
    case $jar in /*) ;; ?*) jar=/data/${jar#./} ;; esac
    { [ "$(recorded minecraftVersion "$l")" = "$game" ] && [ -f "$jar" ] && ! { [ "$loader" = neoforge ] && [ "$game" = 1.20.1 ]; }; } || have=
    ;;
  esac
  # As the pack asks: its own version of the loader, or for CurseForge, an ID that ends with it.
  case $platform in
  MODRINTH) [ "$want" = "$have" ] || have= ;;
  *) case $want in *-"$have") ;; *) have= ;; esac ;;
  esac
  [ "$api" = none ] && api=
  silent=
  if [ -n "$have" ]; then
    if [ -n "$api" ]; then
      if [ "$platform" = MODRINTH ]; then
        answers "$api" || silent=$api
      else
        # CurseForge answers this without the key the image sends: any
        # answer but a server error is one.
        case $(curl -sS -o /dev/null -w '%{http_code}' --connect-timeout 3 -m 5 "$api" 2>/dev/null) in
        [1234][0-9][0-9]) ;;
        *) silent=$api ;;
        esac
      fi
    fi
    [ -n "$silent" ] || [ -z "$meta" ] || answers "$meta" || silent=$meta
  fi
  if [ -n "$silent" ]; then
    name=$(setting MODPACK_NAME "$env") version=$(setting MODPACK_VERSION "$env")
    [ "$version" != local ] || version=
    unanswered "$silent" "${name:-modpack}${version:+ $version}"
    unset MODPACK_PLATFORM MOD_PLATFORM
    export VERSION="$game" REMOVE_OLD_MODS=false
    case $loader in
    fabric-loader) kind=FABRIC && export TYPE=FABRIC FABRIC_LOADER_VERSION="$have" FABRIC_LAUNCHER_VERSION="$installer" ;;
    quilt-loader) kind=QUILT && custom "$jar" FABRIC quilt ;;
    forge) kind=FORGE && export TYPE=FORGE FORGE_VERSION="$have" ;;
    neoforge) kind=NEOFORGE && export TYPE=NEOFORGE NEOFORGE_VERSION="$have" ;;
    esac
    # The world the image names in the record of a pack from CurseForge.
    level=$(recorded levelName "$m")
    [ -z "$level" ] || export LEVEL="$level"
  fi
fi
`

// packScript is the game container's command for a server from a modpack
// the image installs: a pack in the Modrinth format leaves the world alone
// (keepWorld), the pack and content it has are kept when the sites they
// came from do not answer, the mods ports were opened for are set up to use
// them, and then the image installs and starts the pack as it does by
// itself. The pack's loader, as the panel recorded it, says where its mods
// keep their config: the image only finds out once the script has run.
func packScript(pack *avsysv1.ModpackSpec) string {
	kind := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' {
			return r - 'a' + 'A'
		}
		if r >= 'A' && r <= 'Z' {
			return r
		}
		return -1
	}, pack.Loader)
	world := ""
	if pack.Source != avsysv1.SourceCurseForge {
		world = keepWorld
	}
	return world + "kind=" + kind + "\n" + startHelpers + keepPack + keepContent + modConfig + "exec " + imageStart + "\n"
}

// startsThroughScript says whether a server's game is started through
// startScript: one run by the image it is written for, and not from a
// modpack, which the image installs its own way.
func startsThroughScript(mc *avsysv1.MinecraftServer) bool {
	return mc.Spec.Modpack == nil && mc.Spec.Image == ""
}

// startsThroughPackScript says whether a server's game is started through
// packScript: one run by the image it is written for, from a modpack.
func startsThroughPackScript(mc *avsysv1.MinecraftServer) bool {
	return mc.Spec.Modpack != nil && mc.Spec.Image == ""
}

func rconPasswordEnv(mc *avsysv1.MinecraftServer) corev1.EnvVar {
	return corev1.EnvVar{
		Name: "RCON_PASSWORD",
		ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: RCONSecretName(mc.Name)},
			Key:                  "password",
		}},
	}
}

func minecraftContainer(mc *avsysv1.MinecraftServer, cfg Config) corev1.Container {
	limits := corev1.ResourceList{corev1.ResourceMemory: mc.Spec.Resources.Memory}
	if mc.Spec.Resources.CPU != nil {
		limits[corev1.ResourceCPU] = *mc.Spec.Resources.CPU
	}
	health := corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: []string{"mc-health"}}}
	return corev1.Container{
		Name:  MinecraftContainer,
		Image: imageFor(mc),
		Env:   minecraftEnv(mc, cfg),
		Ports: []corev1.ContainerPort{
			{Name: "minecraft", ContainerPort: MinecraftPort},
			{Name: "rcon", ContainerPort: RCONPort},
		},
		Resources: corev1.ResourceRequirements{
			Limits: limits,
			Requests: corev1.ResourceList{
				corev1.ResourceMemory: mc.Spec.Resources.Memory,
				corev1.ResourceCPU:    resource.MustParse("250m"),
			},
		},
		VolumeMounts: []corev1.VolumeMount{{Name: "data", MountPath: "/data"}, consoleMount},
		// Large modpacks can take many minutes to install on first boot.
		StartupProbe: &corev1.Probe{
			ProbeHandler: health, PeriodSeconds: 5, TimeoutSeconds: 5, FailureThreshold: 720,
		},
		ReadinessProbe: &corev1.Probe{
			ProbeHandler: health, PeriodSeconds: 10, TimeoutSeconds: 5, FailureThreshold: 3,
		},
	}
}

func workspaceContainer(mc *avsysv1.MinecraftServer, cfg Config) corev1.Container {
	return corev1.Container{
		Name:            WorkspaceContainer,
		Image:           cfg.WorkspaceImage,
		ImagePullPolicy: cfg.WorkspaceImagePullPolicy,
		Env: []corev1.EnvVar{
			{Name: "AVSYS_SERVER", Value: mc.Name},
			{Name: "AVSYS_NAMESPACE", Value: mc.Namespace},
			rconPasswordEnv(mc),
		},
		Ports: []corev1.ContainerPort{{Name: "ssh", ContainerPort: WorkspacePort}},
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("10m"),
				corev1.ResourceMemory: resource.MustParse("32Mi"),
			},
			Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("2Gi")},
		},
		VolumeMounts: []corev1.VolumeMount{
			{Name: "data", MountPath: "/data"},
			{Name: "workspace-keys", MountPath: "/etc/avsys/keys", ReadOnly: true},
			consoleMount,
		},
	}
}

// buildPod renders the pod for a server. The workspace container is always
// present so files stay reachable while the game is stopped or asleep.
func buildPod(mc *avsysv1.MinecraftServer, cfg Config) *corev1.Pod {
	id := int64(runAsID)
	nonRoot := true
	serviceLinks := false
	onMismatch := corev1.FSGroupChangeOnRootMismatch
	grace := int64(5)

	containers := []corev1.Container{workspaceContainer(mc, cfg)}
	if WantsMinecraft(mc) {
		containers = append([]corev1.Container{minecraftContainer(mc, cfg)}, containers...)
		grace = 90
	}

	spec := corev1.PodSpec{
		Containers:                    containers,
		EnableServiceLinks:            &serviceLinks,
		TerminationGracePeriodSeconds: &grace,

		SecurityContext: &corev1.PodSecurityContext{
			RunAsUser: &id, RunAsGroup: &id, FSGroup: &id,
			RunAsNonRoot:        &nonRoot,
			FSGroupChangePolicy: &onMismatch,
		},
		Volumes: []corev1.Volume{
			{Name: "data", VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: DataPVCName(mc.Name)},
			}},
			{Name: "workspace-keys", VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{SecretName: WorkspaceSecret},
			}},
			{Name: "console", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
		},
	}

	// Hashed before the node is filled in: where a server runs is not a
	// setting whose change should ask for a restart.
	annotations := map[string]string{annotationSpecHash: hashOf(spec)}
	// Also after the hash, so that servers already running are not asked to
	// restart for it: the end of the game's log is kept when it fails, and
	// that is how the status can say why it stopped (see startFailure).
	// So is starting on the build, pack and content a server has, and a
	// modpack's install leaving the world alone: it changes nothing a
	// running game would be restarted for, and is what each start after
	// this one does.
	// Whatever changes it, its software or content, changes the hash itself.
	for i := range spec.Containers {
		if spec.Containers[i].Name == MinecraftContainer {
			spec.Containers[i].TerminationMessagePolicy = corev1.TerminationMessageFallbackToLogsOnError
			if startsThroughScript(mc) {
				spec.Containers[i].Command = []string{"/bin/sh", "-c", startScript}
			} else if startsThroughPackScript(mc) {
				spec.Containers[i].Command = []string{"/bin/sh", "-c", packScript(mc.Spec.Modpack)}
			}
		}
	}
	spec.NodeSelector = mc.Spec.NodeSelector
	// Kept to its node by the node's name, which its hostname label need
	// not be.
	if node := mc.Annotations[avsysv1.AnnotationNode]; len(spec.NodeSelector) == 0 && node != "" {
		spec.Affinity = &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{{
				MatchFields: []corev1.NodeSelectorRequirement{{Key: "metadata.name", Operator: corev1.NodeSelectorOpIn, Values: []string{node}}},
			}}},
		}}
	}
	spec.Tolerations = readingForMove(mc)
	if v := mc.Annotations[avsysv1.AnnotationRestart]; v != "" {
		annotations[avsysv1.AnnotationRestart] = v
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:        mc.Name,
			Namespace:   mc.Namespace,
			Labels:      labelsFor(mc),
			Annotations: annotations,
		},
		Spec: spec,
	}
}

// cordoned is the taint a node takes when it is cordoned, as before
// maintenance: it takes no new pods.
var cordoned = corev1.Taint{Key: corev1.TaintNodeUnschedulable, Effect: corev1.TaintEffectNoSchedule}

// readingForMove is what a server's pod tolerates while a move stops it to
// snapshot its files where they are: its node being cordoned, which is when
// servers are moved off it. Its pod is made again there to end every session
// to its files, and the move reads them through it. That is all: the game
// never starts on a cordoned node, and once the move lets go of the files,
// or is put back, the server's pods tolerate nothing again.
//
// Set after the spec is hashed, so that it asks for no restart.
func readingForMove(mc *avsysv1.MinecraftServer) []corev1.Toleration {
	if mc.Annotations[avsysv1.AnnotationMoveWas] == "" || mc.Annotations[avsysv1.AnnotationMoving] != "" || WantsMinecraft(mc) {
		return nil
	}
	return []corev1.Toleration{{Key: cordoned.Key, Operator: corev1.TolerationOpExists, Effect: cordoned.Effect}}
}

func hashOf(v any) string {
	b, _ := json.Marshal(v)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
}

func hasContainer(pod *corev1.Pod, name string) bool {
	for _, c := range pod.Spec.Containers {
		if c.Name == name {
			return true
		}
	}
	return false
}

func buildPVC(mc *avsysv1.MinecraftServer) *corev1.PersistentVolumeClaim {
	return &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: DataPVCName(mc.Name), Namespace: mc.Namespace, Labels: labelsFor(mc)},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			StorageClassName: mc.Spec.Storage.StorageClassName,
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: mc.Spec.Storage.Size},
			},
		},
	}
}

// applyService sets the desired state on svc, leaving fields the cluster
// allocates (cluster IP, node ports) alone.
func applyService(svc *corev1.Service, mc *avsysv1.MinecraftServer) {
	svc.Labels = labelsFor(mc)
	svc.Annotations = mc.Spec.Expose.ServiceAnnotations
	svc.Spec.Selector = map[string]string{avsysv1.LabelServer: mc.Name}

	port := corev1.ServicePort{Name: "minecraft", Port: MinecraftPort, TargetPort: intstr.FromString("minecraft"), Protocol: corev1.ProtocolTCP}
	for _, p := range svc.Spec.Ports {
		if p.Name == "minecraft" || len(svc.Spec.Ports) == 1 {
			port.NodePort = p.NodePort
		}
	}
	svc.Spec.LoadBalancerIP = ""
	switch mc.Spec.Expose.Type {
	case avsysv1.ExposeLoadBalancer:
		svc.Spec.Type = corev1.ServiceTypeLoadBalancer
		svc.Spec.LoadBalancerIP = mc.Spec.Expose.LoadBalancerIP
	case avsysv1.ExposeNodePort:
		svc.Spec.Type = corev1.ServiceTypeNodePort
		if mc.Spec.Expose.NodePort != 0 {
			port.NodePort = mc.Spec.Expose.NodePort
		}
	default:
		svc.Spec.Type = corev1.ServiceTypeClusterIP
		port.NodePort = 0
	}
	svc.Spec.Ports = append([]corev1.ServicePort{port}, extraServicePorts(mc, svc.Spec.Ports)...)
}

// addressFor is what a player types to join, or "" while it is not known yet.
func addressFor(mc *avsysv1.MinecraftServer, svc *corev1.Service, cfg Config) string {
	host := mc.Spec.Expose.Hostname
	switch mc.Spec.Expose.Type {
	case avsysv1.ExposeLoadBalancer:
		if host != "" {
			return host
		}
		for _, ing := range svc.Status.LoadBalancer.Ingress {
			if ing.Hostname != "" {
				return ing.Hostname
			}
			if ing.IP != "" {
				return ing.IP
			}
		}
		return ""
	case avsysv1.ExposeNodePort:
		if host == "" || len(svc.Spec.Ports) == 0 || svc.Spec.Ports[0].NodePort == 0 {
			return ""
		}
		return fmt.Sprintf("%s:%d", host, svc.Spec.Ports[0].NodePort)
	case avsysv1.ExposeNone:
		return ""
	default:
		if host == "" {
			return ""
		}
		if cfg.RouterPublicPort != 0 && cfg.RouterPublicPort != MinecraftPort {
			return fmt.Sprintf("%s:%d", host, cfg.RouterPublicPort)
		}
		return host
	}
}

// packPrompt is a resource pack's prompt as the game reads it from
// server.properties: a JSON text component. Plain text, as typed in the
// panel, is made a JSON string; one written as a component already, an
// object, list or string, is left as it is.
// forImage is the players of a server's operators or whitelist that the
// image is given, to put on them as it starts: all but Bedrock players
// named as Floodgate names them, behind a prefix such as ".". The image
// looks a name it does not find in the game's file up with Mojang, which
// has no such account, and the server would not start. A Bedrock player
// goes by the UUID Floodgate gives them, which the image takes as it is.
func forImage(players []string) []string {
	out := make([]string, 0, len(players))
	for _, p := range players {
		if p != "" && strings.ContainsRune(".*!~+-", rune(p[0])) {
			continue
		}
		out = append(out, p)
	}
	return out
}

func packPrompt(prompt string) string {
	if t := strings.TrimSpace(prompt); t != "" && strings.ContainsAny(t[:1], "{[\"") && json.Valid([]byte(t)) {
		return t
	}
	quoted, _ := json.Marshal(prompt)
	return string(quoted)
}
