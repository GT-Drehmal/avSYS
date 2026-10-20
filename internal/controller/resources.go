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

// wantsMinecraft reports whether the game container should be running.
func wantsMinecraft(mc *avsysv1.MinecraftServer) bool {
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
	case mc.Spec.Modpack != nil && mc.Spec.Modpack.Source == avsysv1.SourceCurseForge:
		env["MODPACK_PLATFORM"] = "AUTO_CURSEFORGE"
		env["CF_SLUG"] = mc.Spec.Modpack.Project
		if mc.Spec.Modpack.Version != "" {
			env["CF_FILE_ID"] = mc.Spec.Modpack.Version
		}
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
	if cfg.CurseForgeAPIKey != "" {
		env["CF_API_KEY"] = cfg.CurseForgeAPIKey
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
			ref := m.Project
			if m.Version != "" {
				ref += ":" + m.Version
			}
			curseforge = append(curseforge, ref)
		case avsysv1.SourceURL, avsysv1.SourceUpload:
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
	if len(mc.Spec.Ops) > 0 {
		env["OPS"] = strings.Join(mc.Spec.Ops, "\n")
	}
	if len(mc.Spec.Whitelist) > 0 {
		env["WHITELIST"] = strings.Join(mc.Spec.Whitelist, "\n")
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
	if wantsMinecraft(mc) {
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
	for i := range spec.Containers {
		if spec.Containers[i].Name == MinecraftContainer {
			spec.Containers[i].TerminationMessagePolicy = corev1.TerminationMessageFallbackToLogsOnError
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
func packPrompt(prompt string) string {
	if t := strings.TrimSpace(prompt); t != "" && strings.ContainsAny(t[:1], "{[\"") && json.Valid([]byte(t)) {
		return t
	}
	quoted, _ := json.Marshal(prompt)
	return string(quoted)
}
