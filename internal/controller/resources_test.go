package controller

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
)

func TestGameJava(t *testing.T) {
	for version, want := range map[string]int{
		"1.8.9":  8,
		"1.12.2": 8,
		"1.16.5": 8,
		"1.17.1": 17,
		"1.20.4": 17,
		"1.20.5": 21,
		"1.21":   21,
		"1.21.4": 21,
		"26.1":   25,
		"26.3":   25,
		// A pre-release needs what its release does.
		"26.3-rc-3":       25,
		"26.4-snapshot-3": 25,
		"1.21.11-pre5":    21,
		"1.20.5-rc1":      21,
		"1.20.4-rc1":      17,
		"1.16-pre2":       8,
		"27.1":            0,
		"LATEST":          0,
		"24w14a":          0,
		"SNAPSHOT":        0,
		"":                0,
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
	// By the node's name, which its hostname label need not be.
	pinned := buildPod(placed, Config{}).Spec
	if a := pinned.Affinity; len(pinned.NodeSelector) != 0 || a == nil || a.NodeAffinity == nil ||
		!reflect.DeepEqual(a.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[0].MatchFields,
			[]corev1.NodeSelectorRequirement{{Key: "metadata.name", Operator: corev1.NodeSelectorOpIn, Values: []string{"pc"}}}) {
		t.Errorf("recorded node not used: %+v %+v", pinned.NodeSelector, pinned.Affinity)
	}
	own := server(func(mc *avsysv1.MinecraftServer) {
		mc.Annotations = map[string]string{avsysv1.AnnotationNode: "pc"}
		mc.Spec.NodeSelector = map[string]string{"disk": "fast"}
	})
	if got := buildPod(own, Config{}).Spec; len(got.NodeSelector) != 1 || got.NodeSelector["disk"] != "fast" || got.Affinity != nil {
		t.Errorf("a server's own selector should win: %v %+v", got.NodeSelector, got.Affinity)
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

	// A stop or restart asked for while the game is loading waits for it to
	// finish, which can be long: it says so, rather than looking stuck.
	loadingGame := corev1.ContainerStatus{State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}
	if phase, message := phaseOf(stopped, gamePod(loadingGame)); phase != avsysv1.PhaseStopping || !strings.Contains(message, "still loading") {
		t.Errorf("stopped while loading: %s %q", phase, message)
	}
	if _, message := phaseOf(stopped, gamePod(corev1.ContainerStatus{Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}})); message != "" {
		t.Errorf("stopped while running has a message: %q", message)
	}
	replaced := func(status corev1.ContainerStatus) *corev1.Pod {
		pod := gamePod(status)
		now := metav1.Now()
		pod.DeletionTimestamp = &now
		return pod
	}
	if phase, message := phaseOf(server(nil), replaced(loadingGame)); phase != avsysv1.PhaseStarting || !strings.Contains(message, "still loading") {
		t.Errorf("restarted while loading: %s %q", phase, message)
	}
	if phase, message := phaseOf(server(nil), replaced(corev1.ContainerStatus{Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}})); phase != avsysv1.PhaseStarting || !strings.Contains(message, "saving the world and stopping") {
		t.Errorf("restarted while running: %s %q", phase, message)
	}
	// A game that crashed is not loading.
	crashed := corev1.ContainerStatus{State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}}
	if _, message := phaseOf(stopped, gamePod(crashed)); message != "" {
		t.Errorf("stopped after a crash: %q", message)
	}
}

// A game that could not look up where it downloads from is starting again,
// and says so, until it has failed that way too many times. The logs are the
// ends of two real ones, as the cluster keeps them: Fabric's installer on a
// home cluster, and the image's helper fetching Paper in CI.
func TestPhaseOfLookupFailure(t *testing.T) {
	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	backingOff := func(log string, restarts int32) *corev1.Pod {
		pod := buildPod(server(nil), Config{})
		pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
			Name:                 MinecraftContainer,
			RestartCount:         restarts,
			State:                corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
			LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1, Message: log}},
		}}
		return pod
	}
	// notReady is how long ago the pod's containers stopped being ready.
	notReady := func(pod *corev1.Pod, ago time.Duration) *corev1.Pod {
		pod.Status.Conditions = append(pod.Status.Conditions, corev1.PodCondition{
			Type: corev1.ContainersReady, Status: corev1.ConditionFalse,
			LastTransitionTime: metav1.NewTime(time.Now().Add(-ago)),
		})
		return pod
	}
	fabric := read("fabric-unknown-host.log")
	// A plugin could not look something up while the game ran, and later
	// the game crashed for another reason.
	pluginThenCrash := "[12:00:01] [Server thread/WARN]: [Updater] java.net.UnknownHostException: api.example.org\n" +
		"[12:30:00] [Server thread/ERROR]: Encountered an unexpected exception\n" +
		"java.lang.IllegalStateException: broken chunk\n\tat net.minecraft.server.Main.main(Main.java:1)\n" +
		"2026-10-14T06:00:55.213Z\tWARN\tmc-server-runner\tMinecraft server failed.\n"
	justExited := func(log string) *corev1.Pod {
		pod := buildPod(server(nil), Config{})
		pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
			Name:  MinecraftContainer,
			State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1, Message: log}},
		}}
		return pod
	}

	cases := []struct {
		name    string
		pod     *corev1.Pod
		want    avsysv1.Phase
		message string
	}{
		{"fabric, backing off", backingOff(read("fabric-unknown-host.log"), 1), avsysv1.PhaseStarting, "could not look up piston-data.mojang.com"},
		{"fabric, just exited", justExited(read("fabric-unknown-host.log")), avsysv1.PhaseStarting, "piston-data.mojang.com"},
		{"paper", backingOff(read("paper-dns-timeout.log"), 2), avsysv1.PhaseStarting, "could not look up fill.papermc.io "},
		{"curl", backingOff("curl: (6) Could not resolve host: cdn.modrinth.com\n", 0), avsysv1.PhaseStarting, "cdn.modrinth.com"},
		{"go", backingOff(`Get "https://meta.fabricmc.net/v2": dial tcp: lookup meta.fabricmc.net. on 10.43.0.10:53: no such host`, 0), avsysv1.PhaseStarting, "meta.fabricmc.net "},
		{"going on", notReady(backingOff(fabric, lookupRetries), lookupGrace+time.Minute), avsysv1.PhaseError, "kept failing for 6 minutes. Check that the cluster can reach the internet"},
		{"not yet as many restarts", notReady(backingOff(fabric, lookupRetries-1), time.Hour), avsysv1.PhaseStarting, "being started again"},
		// Restarts from earlier in the pod's life are not counted against it.
		{"after earlier restarts", notReady(backingOff(fabric, 40), time.Minute), avsysv1.PhaseStarting, "being started again"},
		{"a lookup before another crash", backingOff(pluginThenCrash, 1), avsysv1.PhaseError, "keeps crashing"},
		{"in colour", backingOff("\x1b[0;39mCaused by: java.net.UnknownHostException: piston-meta.mojang.com\x1b[0m\n\tat x.y(Z.java:1)\n", 0), avsysv1.PhaseStarting, "piston-meta.mojang.com "},
		{"a crash", backingOff("Exception in thread \"main\" java.lang.NoClassDefFoundError: foo\n", 1), avsysv1.PhaseError, "keeps crashing"},
		{"a pod from before the log was kept", backingOff("", 1), avsysv1.PhaseError, "keeps crashing"},
	}
	for _, c := range cases {
		phase, message := phaseOf(server(nil), c.pod)
		if phase != c.want || !strings.Contains(message, c.message) {
			t.Errorf("%s: %s %q, want %s and %q", c.name, phase, message, c.want, c.message)
		}
	}
}

// The game keeps the end of its log when it fails, and adding that does not
// ask servers that are already running to restart.
func TestGameKeepsItsLogWithoutARestart(t *testing.T) {
	pod := buildPod(server(nil), Config{})
	for _, c := range pod.Spec.Containers {
		if c.Name == MinecraftContainer && c.TerminationMessagePolicy != corev1.TerminationMessageFallbackToLogsOnError {
			t.Errorf("the game's termination message policy is %q", c.TerminationMessagePolicy)
		}
	}
	for i := range pod.Spec.Containers {
		pod.Spec.Containers[i].TerminationMessagePolicy = ""
	}
	pod.Spec.NodeSelector = nil
	if hashOf(pod.Spec) != pod.Annotations[annotationSpecHash] {
		t.Error("the hash covers the termination message policy")
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

// What is left of a deleted server is told from what belongs to the server
// of the same name that replaced it by whose it is, not by what it is called.
func TestLeftOf(t *testing.T) {
	mc := server(func(mc *avsysv1.MinecraftServer) { mc.UID = "new" })
	owned := func(uid string) *corev1.PersistentVolumeClaim {
		yes := true
		return &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{OwnerReferences: []metav1.OwnerReference{{Kind: "MinecraftServer", Name: mc.Name, UID: types.UID(uid), Controller: &yes}}}}
	}
	if !leftOf(owned("old"), mc) {
		t.Error("a volume of a deleted server of the same name was taken for this server's")
	}
	if leftOf(owned("new"), mc) {
		t.Error("the server's own volume was taken for a leftover")
	}
	// A volume made by hand for the server to use belongs to nobody yet.
	if leftOf(&corev1.PersistentVolumeClaim{}, mc) {
		t.Error("a volume with no owner was taken for a leftover")
	}
}

// The game reads its console from a pipe in a directory it shares with the
// workspace, which is how a command from the panel or a shell is typed at the
// server console rather than sent over RCON.
func TestConsoleIsSharedWithTheWorkspace(t *testing.T) {
	mounted := func(pod *corev1.Pod, container string) bool {
		for _, c := range pod.Spec.Containers {
			if c.Name != container {
				continue
			}
			for _, m := range c.VolumeMounts {
				if m.Name == "console" && m.MountPath == ConsoleDir {
					return true
				}
			}
		}
		return false
	}
	running := buildPod(server(nil), Config{})
	if !mounted(running, MinecraftContainer) || !mounted(running, WorkspaceContainer) {
		t.Error("the console directory is not mounted in both the game and the workspace")
	}
	env := envOf(server(nil))
	if env["CREATE_CONSOLE_IN_PIPE"] != "true" || env["CONSOLE_IN_NAMED_PIPE"] != ConsolePipe || !strings.HasPrefix(ConsolePipe, ConsoleDir+"/") {
		t.Errorf("the game is not told to read its console from %s: %v %v", ConsolePipe, env["CREATE_CONSOLE_IN_PIPE"], env["CONSOLE_IN_NAMED_PIPE"])
	}
	// It is not kept with the server's files, where a snapshot would pick it up.
	if strings.HasPrefix(ConsoleDir, "/data") {
		t.Errorf("the console pipe is among the server's files: %s", ConsoleDir)
	}
	stopped := buildPod(server(func(mc *avsysv1.MinecraftServer) { mc.Spec.State = avsysv1.StateStopped }), Config{})
	if !mounted(stopped, WorkspaceContainer) {
		t.Error("a stopped server's workspace has no console directory, so its pod would differ in more than the game")
	}
}

// Where a version's name does not say what Java it needs, what the panel
// noted when it was chosen is used. A name that does say always wins.
func TestGameJavaOf(t *testing.T) {
	for name, c := range map[string]struct {
		spec avsysv1.MinecraftServerSpec
		want int
	}{
		"named":              {avsysv1.MinecraftServerSpec{Version: "1.21.4"}, 21},
		"named, with a note": {avsysv1.MinecraftServerSpec{Version: "1.21.4", GameJava: 8}, 21},
		"weekly, noted":      {avsysv1.MinecraftServerSpec{Version: "24w14a", GameJava: 21}, 21},
		"weekly, not noted":  {avsysv1.MinecraftServerSpec{Version: "24w14a"}, 0},
		"a modpack":          {avsysv1.MinecraftServerSpec{GameJava: 8, Modpack: &avsysv1.ModpackSpec{GameVersion: "1.20.1"}}, 17},
		"a modpack unknown":  {avsysv1.MinecraftServerSpec{GameJava: 8, Modpack: &avsysv1.ModpackSpec{}}, 0},
	} {
		if got := GameJavaOf(&c.spec); got != c.want {
			t.Errorf("%s: %d, want %d", name, got, c.want)
		}
	}
	// And it is what decides the image.
	if game, runs := JavaOf(&avsysv1.MinecraftServerSpec{Version: "21w19a", GameJava: 17}); game != 17 || runs != 17 {
		t.Errorf("a weekly snapshot noted as 17: game %d, runs on %d", game, runs)
	}
}

// Aikar's flags and extra ones reach the image as it reads them, and a server
// that has neither starts as it always has.
func TestLaunchFlags(t *testing.T) {
	envOf := func(jvm avsysv1.JVMSpec) map[string]string {
		mc := server(func(mc *avsysv1.MinecraftServer) { mc.Spec.JVM = jvm })
		env := map[string]string{}
		for _, e := range minecraftEnv(mc, Config{}) {
			env[e.Name] = e.Value
		}
		return env
	}
	on, off := true, false
	for name, c := range map[string]struct {
		jvm          avsysv1.JVMSpec
		aikar, extra string
	}{
		"neither, as before they were offered": {avsysv1.JVMSpec{}, "", ""},
		"Aikar's":                              {avsysv1.JVMSpec{AikarFlags: &on}, "true", ""},
		"Aikar's and more":                     {avsysv1.JVMSpec{AikarFlags: &on, ExtraFlags: "-XX:+AlwaysPreTouch\n-Dfoo=bar"}, "true", "-XX:+AlwaysPreTouch -Dfoo=bar"},
		"only one's own":                       {avsysv1.JVMSpec{AikarFlags: &off, ExtraFlags: "-XX:+UseZGC"}, "", "-XX:+UseZGC"},
	} {
		env := envOf(c.jvm)
		if env["USE_AIKAR_FLAGS"] != c.aikar || env["JVM_OPTS"] != c.extra {
			t.Errorf("%s: USE_AIKAR_FLAGS=%q JVM_OPTS=%q, want %q and %q", name, env["USE_AIKAR_FLAGS"], env["JVM_OPTS"], c.aikar, c.extra)
		}
	}
	// Set in its own fields, a launch flag is not overridden by an older
	// entry in the server's own environment; one with neither keeps it.
	legacy := []corev1.EnvVar{{Name: "USE_AIKAR_FLAGS", Value: "true"}, {Name: "JVM_OPTS", Value: "-XX:+UseZGC"}, {Name: "TZ", Value: "UTC"}}
	count := func(mc *avsysv1.MinecraftServer, name string) (n int, last string) {
		for _, e := range minecraftEnv(mc, Config{}) {
			if e.Name == name {
				n, last = n+1, e.Value
			}
		}
		return
	}
	withFields := server(func(mc *avsysv1.MinecraftServer) {
		mc.Spec.JVM = avsysv1.JVMSpec{AikarFlags: &off}
		mc.Spec.Env = legacy
	})
	if n, _ := count(withFields, "USE_AIKAR_FLAGS"); n != 0 {
		t.Error("an old USE_AIKAR_FLAGS overrode Aikar's flags switched off")
	}
	if n, _ := count(withFields, "TZ"); n != 1 {
		t.Error("the rest of the server's own environment was dropped")
	}
	without := server(func(mc *avsysv1.MinecraftServer) { mc.Spec.Env = legacy })
	if n, v := count(without, "USE_AIKAR_FLAGS"); n != 1 || v != "true" {
		t.Error("a server not using the fields lost its own USE_AIKAR_FLAGS")
	}

	// Adding the setting asks nothing of a server that does not use it.
	before := buildPod(server(nil), Config{}).Annotations[annotationSpecHash]
	if after := buildPod(server(func(mc *avsysv1.MinecraftServer) { mc.Spec.JVM = avsysv1.JVMSpec{AikarFlags: &off} }), Config{}).Annotations[annotationSpecHash]; after != before {
		t.Error("switching off flags that were never on changed the pod")
	}
}

// A game that is loading is let finish before its pod is replaced, for a
// stop, a restart or new settings: told to stop while it loads, the game
// fails to and is killed a minute later. One that has loaded for too long,
// or is not running at all, is replaced at once.
func TestLoadingGameIsLetFinish(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := avsysv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	running := func(started time.Time, ready bool) corev1.ContainerStatus {
		return corev1.ContainerStatus{Name: MinecraftContainer, Ready: ready, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(started)}}}
	}
	crashed := corev1.ContainerStatus{Name: MinecraftContainer, State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}}
	stop := func(mc *avsysv1.MinecraftServer) { mc.Spec.State = avsysv1.StateStopped }
	restart := func(mc *avsysv1.MinecraftServer) {
		mc.Annotations = map[string]string{avsysv1.AnnotationRestart: "now"}
	}
	newSettings := func(mc *avsysv1.MinecraftServer) { mc.Spec.Properties = map[string]string{"motd": "changed"} }
	now := time.Now()
	for _, c := range []struct {
		name     string
		change   func(*avsysv1.MinecraftServer)
		game     corev1.ContainerStatus
		replaced bool
	}{
		{"stopped while loading", stop, running(now.Add(-10*time.Second), false), false},
		{"restarted while loading", restart, running(now.Add(-10*time.Second), false), false},
		{"new settings while loading", newSettings, running(now.Add(-10*time.Second), false), false},
		{"stopped once loaded", stop, running(now.Add(-time.Minute), true), true},
		{"restarted once loaded", restart, running(now.Add(-time.Minute), true), true},
		{"stopped after loading too long", stop, running(now.Add(-loadGrace-time.Second), false), true},
		{"stopped while crashed", stop, crashed, true},
	} {
		// The pod as made for the server before the change.
		pod := buildPod(server(nil), Config{})
		pod.Status.ContainerStatuses = []corev1.ContainerStatus{c.game}
		mc := server(c.change)
		cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(mc, pod).Build()
		r := &Reconciler{Client: cl, Reader: cl, Scheme: scheme}
		got, _, wait, err := r.ensurePod(context.Background(), mc)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		replaced := !got.DeletionTimestamp.IsZero()
		if replaced != c.replaced {
			t.Errorf("%s: replaced = %v, want %v", c.name, replaced, c.replaced)
		}
		// Looked at again by the time it has had its grace, at the latest.
		if !replaced && (wait <= 0 || wait > loadGrace) {
			t.Errorf("%s: looked at again in %s", c.name, wait)
		}
		if !replaced && c.change != nil {
			phase, message := phaseOf(mc, got)
			if !strings.Contains(message, "still loading") && phase != avsysv1.PhaseStarting {
				t.Errorf("%s: %s %q, want it to say it is let load", c.name, phase, message)
			}
		}
	}
}
