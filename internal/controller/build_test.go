package controller

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
)

func gameContainer(pod *corev1.Pod) *corev1.Container {
	for i := range pod.Spec.Containers {
		if pod.Spec.Containers[i].Name == MinecraftContainer {
			return &pod.Spec.Containers[i]
		}
	}
	return nil
}

// Only Paper and Folia, run by the image, start through keepBuild. Every
// other server is started as the image starts itself.
func TestOnlyPaperKeepsItsBuild(t *testing.T) {
	for name, c := range map[string]struct {
		mutate func(*avsysv1.MinecraftServer)
		keeps  bool
	}{
		"paper":   {func(mc *avsysv1.MinecraftServer) { mc.Spec.Loader = avsysv1.LoaderPaper }, true},
		"folia":   {func(mc *avsysv1.MinecraftServer) { mc.Spec.Loader = avsysv1.LoaderFolia }, true},
		"latest":  {func(mc *avsysv1.MinecraftServer) { mc.Spec.Loader, mc.Spec.Version = avsysv1.LoaderPaper, "LATEST" }, true},
		"purpur":  {func(mc *avsysv1.MinecraftServer) { mc.Spec.Loader = avsysv1.LoaderPurpur }, false},
		"fabric":  {nil, false},
		"vanilla": {func(mc *avsysv1.MinecraftServer) { mc.Spec.Loader = avsysv1.LoaderVanilla }, false},
		"own image": {func(mc *avsysv1.MinecraftServer) {
			mc.Spec.Loader, mc.Spec.Image = avsysv1.LoaderPaper, "example/paper:1"
		}, false},
		"modpack": {func(mc *avsysv1.MinecraftServer) {
			mc.Spec.Loader = avsysv1.LoaderPaper
			mc.Spec.Modpack = &avsysv1.ModpackSpec{Source: avsysv1.SourceModrinth, Project: "pack"}
		}, false},
	} {
		game := gameContainer(buildPod(server(c.mutate), Config{}))
		if c.keeps {
			if want := []string{"/bin/sh", "-c", keepBuild}; strings.Join(game.Command, "\x00") != strings.Join(want, "\x00") {
				t.Errorf("%s: the game is started with %q", name, game.Command)
			}
		} else if len(game.Command) != 0 {
			t.Errorf("%s: the game is started with %q, not as the image starts it", name, game.Command)
		}
	}
}

// Starting on the build it has is not a change a running game is restarted
// for, nor said to be waiting for one: a game running from before it was
// done keeps running, and its next start is through it.
func TestHashLeavesOutKeepingTheBuild(t *testing.T) {
	paper := buildPod(server(func(mc *avsysv1.MinecraftServer) { mc.Spec.Loader = avsysv1.LoaderPaper }), Config{})
	for i := range paper.Spec.Containers {
		paper.Spec.Containers[i].TerminationMessagePolicy = ""
		paper.Spec.Containers[i].Command = nil
	}
	if hashOf(paper.Spec) != paper.Annotations[annotationSpecHash] {
		t.Error("the hash covers starting on the build it has")
	}
}

// keepBuild run as the game's container runs it, on files laid out as the
// image leaves them, with Paper's site answering or not, says what jar the
// image is told to use: "" for none, when the image installs as it always
// has, and whether the default config files are fetched again. A server
// with a build to keep keeps it only while the site does not answer: when
// it does, the image looks for a newer build of the same game version.
func TestKeepBuild(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	paper := func(mutate func(*avsysv1.MinecraftServer)) *avsysv1.MinecraftServer {
		return server(func(mc *avsysv1.MinecraftServer) {
			mc.Spec.Loader = avsysv1.LoaderPaper
			if mutate != nil {
				mutate(mc)
			}
		})
	}
	installed := "TYPE=\"PAPER\"\nVERSION=\"1.21.4\"\nSERVER=\"%s\"\n"
	foliaInstalled := "TYPE=\"FOLIA\"\nVERSION=\"1.21.4\"\nSERVER=\"%s\"\n"
	folia := paper(func(mc *avsysv1.MinecraftServer) { mc.Spec.Loader = avsysv1.LoaderFolia })
	for _, c := range []struct {
		name string
		mc   *avsysv1.MinecraftServer
		// record is the file the image wrote, %s for the jar's path; "" is none.
		record string
		// jar is the jar's name in the files; "" is none.
		jar   string
		extra []string
		// keeps is whether the server has a build it can start on.
		keeps bool
	}{
		{"first start", paper(nil), "", "", nil, false},
		{"restarted", paper(nil), installed, "paper-1.21.4-232.jar", nil, true},
		{"its jar deleted", paper(nil), installed, "", nil, false},
		{"changed version", paper(func(mc *avsysv1.MinecraftServer) { mc.Spec.Version = "1.21.5" }), installed, "paper-1.21.4-232.jar", nil, false},
		{"latest", paper(func(mc *avsysv1.MinecraftServer) { mc.Spec.Version = "LATEST" }), installed, "paper-1.21.4-232.jar", nil, false},
		{"changed to folia", paper(func(mc *avsysv1.MinecraftServer) { mc.Spec.Loader = avsysv1.LoaderFolia }), installed, "paper-1.21.4-232.jar", nil, false},
		{"folia restarted", folia, foliaInstalled, "folia-1.21.4-6.jar", nil, true},
		// Folia's own name for a build comes before Paper's older one, and
		// Paper's newer one before both. Paper does not read Folia's.
		{"folia's build name first", folia, foliaInstalled, "folia-1.21.4-6.jar", []string{"PAPERBUILD=6", "FOLIABUILD=7"}, false},
		{"folia's build name", folia, foliaInstalled, "folia-1.21.4-6.jar", []string{"PAPERBUILD=7", "FOLIABUILD=6"}, true},
		{"paper's newer build name over folia's", folia, foliaInstalled, "folia-1.21.4-6.jar", []string{"PAPER_BUILD=6", "FOLIABUILD=7"}, true},
		{"folia's build name on paper", paper(nil), installed, "paper-1.21.4-232.jar", []string{"FOLIABUILD=7"}, true},
		{"the build it has asked for", paper(nil), installed, "paper-1.21.4-232.jar", []string{"PAPER_BUILD=232"}, true},
		{"another build asked for", paper(nil), installed, "paper-1.21.4-232.jar", []string{"PAPER_BUILD=231"}, false},
		{"a build ending the same asked for", paper(nil), installed, "paper-1.21.4-232.jar", []string{"PAPER_BUILD=32"}, false},
		{"a download of its own", paper(nil), installed, "paper-1.21.4-232.jar", []string{"PAPER_DOWNLOAD_URL=https://example.org/paper.jar"}, false},
		{"another kind in its own environment", paper(nil), installed, "paper-1.21.4-232.jar", []string{"TYPE=VANILLA"}, false},
		// Settings the image loads itself, after this, may ask for another.
		{"settings from a file of its own", paper(nil), installed, "paper-1.21.4-232.jar", []string{"LOAD_ENV_FROM_FILE=/data/server.env"}, false},
		{"settings from an archive of its own", paper(nil), installed, "paper-1.21.4-232.jar", []string{"LOAD_ENV_FROM_ARCHIVE=/data/pack.zip"}, false},
		{"settings from a pack", paper(nil), installed, "paper-1.21.4-232.jar", []string{"LOAD_ENV_FROM_GENERIC_PACK=true"}, false},
		{"no settings from a pack", paper(nil), installed, "paper-1.21.4-232.jar", []string{"LOAD_ENV_FROM_GENERIC_PACK=false"}, true},
	} {
		for _, answers := range []bool{true, false} {
			name := c.name + "/paper down"
			if answers {
				name = c.name + "/paper up"
			}
			t.Run(name, func(t *testing.T) {
				dir := t.TempDir()
				jar := filepath.Join(dir, c.jar)
				if c.jar != "" {
					if err := os.WriteFile(jar, []byte("jar"), 0o644); err != nil {
						t.Fatal(err)
					}
				} else {
					jar = filepath.Join(dir, "paper-1.21.4-232.jar")
				}
				record := filepath.Join(dir, ".paper.env")
				if c.record != "" {
					if err := os.WriteFile(record, []byte(strings.ReplaceAll(c.record, "%s", jar)), 0o644); err != nil {
						t.Fatal(err)
					}
				}
				// A curl that answers as Paper's site does, or fails as one
				// that cannot be reached does, and notes what it was asked.
				bin := filepath.Join(dir, "bin")
				asked := filepath.Join(dir, "asked")
				exit := "7"
				if answers {
					exit = "0"
				}
				if err := os.MkdirAll(bin, 0o755); err != nil {
					t.Fatal(err)
				}
				curl := "#!/bin/sh\nfor a; do last=$a; done\necho \"$last\" >> " + asked + "\nexit " + exit + "\n"
				if err := os.WriteFile(filepath.Join(bin, "curl"), []byte(curl), 0o755); err != nil {
					t.Fatal(err)
				}
				script := strings.ReplaceAll(keepBuild, paperEnv, record)
				script = strings.ReplaceAll(script, "exec "+imageStart, `echo "jar=${PAPER_CUSTOM_JAR:-} defaults=${SKIP_DOWNLOAD_DEFAULTS:-fetched}"`)
				cmd := exec.Command("sh", "-c", script)
				cmd.Env = []string{"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH")}
				env := envOf(c.mc)
				cmd.Env = append(cmd.Env, "TYPE="+env["TYPE"], "VERSION="+env["VERSION"])
				cmd.Env = append(cmd.Env, c.extra...)
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("%v: %s", err, out)
				}
				uses := c.keeps && !answers
				want := "jar= defaults=fetched"
				if uses {
					want = "jar=" + jar + " defaults=true"
				}
				if lines := strings.Split(strings.TrimSpace(string(out)), "\n"); lines[len(lines)-1] != want {
					t.Errorf("got %q, want %s", out, want)
				}
				if said := strings.Contains(string(out), "[avsys] Paper's site does not answer: starting on the installed "+filepath.Base(jar)); said != uses {
					t.Errorf("says it starts on the installed jar: %v, in %q", said, out)
				}
				// The site is asked only by a server with a build to fall
				// back on, and for builds of that server's game version.
				calls, _ := os.ReadFile(asked)
				switch want := "https://fill.papermc.io/v3/projects/" + strings.ToLower(env["TYPE"]) + "/versions/" + env["VERSION"] + "/builds/latest\n"; {
				case !c.keeps && len(calls) > 0:
					t.Errorf("asked Paper's site %q, with nothing to fall back on", calls)
				case c.keeps && string(calls) != want:
					t.Errorf("asked Paper's site %q, want %q", calls, want)
				}
			})
		}
	}
}

// The site asked can be another, named in the server's own environment.
func TestKeepBuildAsksTheSiteNamed(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	dir := t.TempDir()
	jar := filepath.Join(dir, "paper-1.21.4-232.jar")
	record := filepath.Join(dir, ".paper.env")
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	for file, body := range map[string]string{
		jar:                        "jar",
		record:                     "TYPE=\"PAPER\"\nVERSION=\"1.21.4\"\nSERVER=\"" + jar + "\"\n",
		filepath.Join(bin, "curl"): "#!/bin/sh\nfor a; do last=$a; done\necho \"$last\"\nexit 7\n",
	} {
		if err := os.WriteFile(file, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	script := strings.ReplaceAll(keepBuild, paperEnv, record)
	script = strings.ReplaceAll(script, "exec "+imageStart, "true")
	cmd := exec.Command("sh", "-c", script)
	cmd.Env = []string{"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"), "TYPE=PAPER", "VERSION=1.21.4", "AVSYS_PAPER_API=http://127.0.0.1:9"}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if !strings.Contains(string(out), "http://127.0.0.1:9/v3/projects/paper/versions/1.21.4/builds/latest") {
		t.Errorf("asked %q, want the site named", out)
	}
}
