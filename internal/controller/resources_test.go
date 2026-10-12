package controller

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
)

func TestGameJava(t *testing.T) {
	for version, want := range map[string]int{
		"1.8.9":    8,
		"1.12.2":   8,
		"1.16.5":   8,
		"1.17.1":   17,
		"1.20.4":   17,
		"1.20.5":   21,
		"1.21":     21,
		"1.21.4":   21,
		"26.1":     25,
		"26.3":     25,
		"27.1":     0,
		"LATEST":   0,
		"24w14a":   0,
		"SNAPSHOT": 0,
		"":         0,
	} {
		if got := GameJava(version); got != want {
			t.Errorf("GameJava(%q) = %d, want %d", version, got, want)
		}
	}
}

func TestImageFollowsGameAndContent(t *testing.T) {
	image := func(mutate func(*avsysv1.MinecraftServer)) string { return imageFor(server(mutate)) }

	if got := image(nil); got != "itzg/minecraft-server:java21" {
		t.Errorf("1.21.4 = %s", got)
	}
	// Content compiled for a newer Java raises the server's Java...
	if got := image(func(mc *avsysv1.MinecraftServer) {
		mc.Spec.Mods = []avsysv1.ContentRef{{Project: "worldedit", Java: 25}, {Project: "old", Java: 8}}
	}); got != "itzg/minecraft-server:java25" {
		t.Errorf("with a Java 25 plugin = %s", got)
	}
	// ...but content built for an older one never lowers it.
	if got := image(func(mc *avsysv1.MinecraftServer) {
		mc.Spec.Mods = []avsysv1.ContentRef{{Project: "old", Java: 8}}
	}); got != "itzg/minecraft-server:java21" {
		t.Errorf("with a Java 8 plugin = %s", got)
	}
	// A modpack's own game version decides, not the server's unused field.
	if got := image(func(mc *avsysv1.MinecraftServer) {
		mc.Spec.Modpack = &avsysv1.ModpackSpec{Project: "pack", GameVersion: "1.20.1"}
	}); got != "itzg/minecraft-server:java17" {
		t.Errorf("1.20.1 modpack = %s", got)
	}
	if got := image(func(mc *avsysv1.MinecraftServer) { mc.Spec.Version = "LATEST" }); got != "itzg/minecraft-server:latest" {
		t.Errorf("LATEST = %s", got)
	}
	if got := image(func(mc *avsysv1.MinecraftServer) { mc.Spec.Image = "example/custom:1" }); got != "example/custom:1" {
		t.Errorf("override = %s", got)
	}
}

func TestHeapLeavesRoomBelowTheLimit(t *testing.T) {
	for limit, want := range map[string]string{
		"1Gi":  "512M",
		"2Gi":  "1076M",
		"4Gi":  "2919M",
		"8Gi":  "6605M",
		"16Gi": "13978M",
	} {
		if got := heapSize(resource.MustParse(limit)); got != want {
			t.Errorf("heapSize(%s) = %s, want %s", limit, got, want)
		}
	}
}

func server(mutate func(*avsysv1.MinecraftServer)) *avsysv1.MinecraftServer {
	mc := &avsysv1.MinecraftServer{
		ObjectMeta: metav1.ObjectMeta{Name: "survival", Namespace: "mc-test"},
		Spec: avsysv1.MinecraftServerSpec{
			State:     avsysv1.StateRunning,
			EULA:      true,
			Version:   "1.21.4",
			Loader:    avsysv1.LoaderFabric,
			Resources: avsysv1.ResourcesSpec{Memory: resource.MustParse("4Gi")},
			Storage:   avsysv1.StorageSpec{Size: resource.MustParse("10Gi")},
		},
	}
	if mutate != nil {
		mutate(mc)
	}
	return mc
}

func envOf(mc *avsysv1.MinecraftServer) map[string]string {
	out := map[string]string{}
	for _, e := range minecraftEnv(mc, Config{}) {
		out[e.Name] = e.Value
	}
	return out
}

func TestMinecraftEnvContent(t *testing.T) {
	env := envOf(server(func(mc *avsysv1.MinecraftServer) {
		mc.Spec.Mods = []avsysv1.ContentRef{
			{Source: avsysv1.SourceModrinth, Kind: avsysv1.KindMod, Project: "fabric-api"},
			{Source: avsysv1.SourceModrinth, Kind: avsysv1.KindMod, Project: "lithium", Version: "abc123"},
			{Source: avsysv1.SourceModrinth, Kind: avsysv1.KindDatapack, Project: "terralith"},
			{Source: avsysv1.SourceCurseForge, Kind: avsysv1.KindMod, Project: "jei", Version: "4593548"},
			{Source: avsysv1.SourceURL, Kind: avsysv1.KindMod, Project: "https://example.com/a.jar"},
		}
		mc.Spec.Properties = map[string]string{"motd": "hi", "difficulty": "hard"}
	}))
	for name, want := range map[string]string{
		"TYPE":                     "FABRIC",
		"VERSION":                  "1.21.4",
		"MODRINTH_PROJECTS":        "fabric-api\nlithium:abc123\ndatapack:terralith",
		"CURSEFORGE_FILES":         "jei:4593548",
		"MODS":                     "https://example.com/a.jar",
		"CUSTOM_SERVER_PROPERTIES": "difficulty=hard\nmotd=hi\n",
	} {
		if env[name] != want {
			t.Errorf("%s = %q, want %q", name, env[name], want)
		}
	}
}

// Removing the last mod must still set the variables, empty, or the image
// would leave the old files in place.
func TestMinecraftEnvClearsRemovedContent(t *testing.T) {
	env := envOf(server(nil))
	for _, name := range []string{"MODRINTH_PROJECTS", "CURSEFORGE_FILES", "MODS", "PLUGINS"} {
		if v, ok := env[name]; !ok || v != "" {
			t.Errorf("%s = %q (set: %v), want it set and empty", name, v, ok)
		}
	}
}

func TestMinecraftEnvModpack(t *testing.T) {
	env := envOf(server(func(mc *avsysv1.MinecraftServer) {
		mc.Spec.Modpack = &avsysv1.ModpackSpec{Source: avsysv1.SourceModrinth, Project: "cobblemon-fabric", Version: "v1"}
	}))
	if env["MODPACK_PLATFORM"] != "MODRINTH" || env["MODRINTH_MODPACK"] != "cobblemon-fabric" || env["MODRINTH_VERSION"] != "v1" {
		t.Errorf("modpack env = %v", env)
	}
	if _, ok := env["TYPE"]; ok {
		t.Error("TYPE must not be set alongside a modpack: the pack chooses the loader")
	}
}

func TestPodKeepsWorkspaceWhenGameIsOff(t *testing.T) {
	stopped := server(func(mc *avsysv1.MinecraftServer) { mc.Spec.State = avsysv1.StateStopped })
	asleep := server(func(mc *avsysv1.MinecraftServer) {
		mc.Annotations = map[string]string{avsysv1.AnnotationAsleep: "now"}
	})
	for name, mc := range map[string]*avsysv1.MinecraftServer{"stopped": stopped, "asleep": asleep} {
		pod := buildPod(mc, Config{})
		if hasContainer(pod, MinecraftContainer) || !hasContainer(pod, WorkspaceContainer) {
			t.Errorf("%s: want only the workspace container, got %d containers", name, len(pod.Spec.Containers))
		}
	}
	if pod := buildPod(server(nil), Config{}); !hasContainer(pod, MinecraftContainer) || !hasContainer(pod, WorkspaceContainer) {
		t.Error("running: want both containers")
	}
}

func TestPodFollowsRecordedNode(t *testing.T) {
	placed := server(func(mc *avsysv1.MinecraftServer) { mc.Annotations = map[string]string{avsysv1.AnnotationNode: "pc"} })
	if got := buildPod(placed, Config{}).Spec.NodeSelector; got[corev1.LabelHostname] != "pc" {
		t.Errorf("recorded node not used: %v", got)
	}
	own := server(func(mc *avsysv1.MinecraftServer) {
		mc.Annotations = map[string]string{avsysv1.AnnotationNode: "pc"}
		mc.Spec.NodeSelector = map[string]string{"disk": "fast"}
	})
	if got := buildPod(own, Config{}).Spec.NodeSelector; len(got) != 1 || got["disk"] != "fast" {
		t.Errorf("a server's own selector should win: %v", got)
	}
	// Recording the node must not look like a settings change.
	if buildPod(server(nil), Config{}).Annotations[annotationSpecHash] != buildPod(placed, Config{}).Annotations[annotationSpecHash] {
		t.Error("placement changed the spec hash")
	}
}

// A spec change that only matters to the game must not disturb a pod that is
// not running the game.
func TestPodHashIgnoresGameSettingsWhileStopped(t *testing.T) {
	a := server(func(mc *avsysv1.MinecraftServer) { mc.Spec.State = avsysv1.StateStopped })
	b := server(func(mc *avsysv1.MinecraftServer) {
		mc.Spec.State = avsysv1.StateStopped
		mc.Spec.Version = "1.20.1"
	})
	if buildPod(a, Config{}).Annotations[annotationSpecHash] != buildPod(b, Config{}).Annotations[annotationSpecHash] {
		t.Error("hash changed for a stopped server")
	}
	running, changed := server(nil), server(func(mc *avsysv1.MinecraftServer) { mc.Spec.Version = "1.20.1" })
	if buildPod(running, Config{}).Annotations[annotationSpecHash] == buildPod(changed, Config{}).Annotations[annotationSpecHash] {
		t.Error("hash did not change for a running server")
	}
}

func TestPhaseOf(t *testing.T) {
	gamePod := func(status corev1.ContainerStatus) *corev1.Pod {
		pod := buildPod(server(nil), Config{})
		status.Name = MinecraftContainer
		pod.Status.ContainerStatuses = []corev1.ContainerStatus{status}
		return pod
	}
	workspaceOnly := buildPod(server(func(mc *avsysv1.MinecraftServer) { mc.Spec.State = avsysv1.StateStopped }), Config{})
	stopped := server(func(mc *avsysv1.MinecraftServer) { mc.Spec.State = avsysv1.StateStopped })
	asleep := server(func(mc *avsysv1.MinecraftServer) {
		mc.Annotations = map[string]string{avsysv1.AnnotationAsleep: "now"}
	})
	// Its spec says Running, as it would after a kubectl edit or a GitOps sync.
	restoring := server(func(mc *avsysv1.MinecraftServer) {
		mc.Annotations = map[string]string{avsysv1.AnnotationRestoring: "aaaa1111"}
	})

	// A server made from a snapshot is created stopped, and says why.
	creating := server(func(mc *avsysv1.MinecraftServer) {
		mc.Spec.State = avsysv1.StateStopped
		mc.Annotations = map[string]string{avsysv1.AnnotationRestoring: "aaaa1111", avsysv1.AnnotationCreating: "aaaa1111"}
	})
	if phase, message := phaseOf(creating, workspaceOnly); phase != avsysv1.PhaseStopped || !strings.Contains(message, "snapshot") {
		t.Errorf("a server being made from a snapshot: %s %q, want Stopped and the reason", phase, message)
	}
	if _, message := phaseOf(stopped, workspaceOnly); message != "" {
		t.Errorf("an ordinary stopped server has a message: %q", message)
	}

	cases := []struct {
		name string
		mc   *avsysv1.MinecraftServer
		pod  *corev1.Pod
		want avsysv1.Phase
	}{
		{"no pod yet", server(nil), nil, avsysv1.PhaseStarting},
		{"game not ready", server(nil), gamePod(corev1.ContainerStatus{}), avsysv1.PhaseStarting},
		{"game ready", server(nil), gamePod(corev1.ContainerStatus{Ready: true}), avsysv1.PhaseRunning},
		{"crash loop", server(nil), gamePod(corev1.ContainerStatus{State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}}), avsysv1.PhaseError},
		{"workspace pod while starting", server(nil), workspaceOnly, avsysv1.PhaseStarting},
		{"stopped", stopped, workspaceOnly, avsysv1.PhaseStopped},
		{"stopping", stopped, gamePod(corev1.ContainerStatus{Ready: true}), avsysv1.PhaseStopping},
		{"asleep", asleep, workspaceOnly, avsysv1.PhaseSleeping},
		{"wanted running, but a restore has not finished", restoring, workspaceOnly, avsysv1.PhaseStopped},
		{"a restore begins under a running game", restoring, gamePod(corev1.ContainerStatus{Ready: true}), avsysv1.PhaseStopping},
	}
	for _, c := range cases {
		if got, _ := phaseOf(c.mc, c.pod); got != c.want {
			t.Errorf("%s: phase = %s, want %s", c.name, got, c.want)
		}
	}
}

// While a restore is rewriting a server's files, or was cut short doing so,
// no pod is built with the game in it, whatever the spec says. This is the
// one place every way of starting a server passes through.
func TestNoGameWhileRestoring(t *testing.T) {
	mc := server(func(mc *avsysv1.MinecraftServer) {
		mc.Spec.State = avsysv1.StateRunning
		mc.Annotations = map[string]string{avsysv1.AnnotationRestoring: "aaaa1111"}
	})
	if wantsMinecraft(mc) {
		t.Fatal("the game is wanted during a restore")
	}
	if pod := buildPod(mc, Config{}); hasContainer(pod, MinecraftContainer) || !hasContainer(pod, WorkspaceContainer) {
		t.Errorf("pod during a restore has containers %v; want only the workspace", containerNames(pod))
	}
	delete(mc.Annotations, avsysv1.AnnotationRestoring)
	if !wantsMinecraft(mc) {
		t.Error("the game is still held once the restore has completed")
	}
}

func containerNames(pod *corev1.Pod) []string {
	names := []string{}
	for _, c := range pod.Spec.Containers {
		names = append(names, c.Name)
	}
	return names
}
