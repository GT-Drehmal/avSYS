package controller

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
)

// A server that installs from CurseForge is given the panel's key from the
// Secret it is kept in, never as a value in its pod; one that does not is
// not given it at all.
func TestCurseForgeKeyIsFromASecret(t *testing.T) {
	keyOf := func(mc *avsysv1.MinecraftServer) *corev1.EnvVar {
		for _, e := range minecraftEnv(mc, Config{}) {
			if e.Name == "CF_API_KEY" {
				return &e
			}
		}
		return nil
	}
	if e := keyOf(server(nil)); e != nil {
		t.Errorf("a server with nothing from CurseForge: %+v", e)
	}
	for name, mutate := range map[string]func(*avsysv1.MinecraftServer){
		"a mod": func(mc *avsysv1.MinecraftServer) {
			mc.Spec.Mods = []avsysv1.ContentRef{{Source: avsysv1.SourceCurseForge, Kind: avsysv1.KindMod, Project: "238222", Version: "4593548"}}
		},
		"a modpack": func(mc *avsysv1.MinecraftServer) {
			mc.Spec.Modpack = &avsysv1.ModpackSpec{Source: avsysv1.SourceCurseForge, Project: "all-the-mods-9", Version: "5000001"}
		},
	} {
		e := keyOf(server(mutate))
		if e == nil || e.Value != "" || e.ValueFrom == nil || e.ValueFrom.SecretKeyRef == nil {
			t.Fatalf("%s: %+v", name, e)
		}
		ref := e.ValueFrom.SecretKeyRef
		if ref.Name != CurseForgeSecret || ref.Key != CurseForgeSecretKey || ref.Optional == nil || !*ref.Optional {
			t.Errorf("%s: %+v", name, ref)
		}
	}
}

// A CurseForge modpack is installed by the image, which looks for the files
// that have to be put in by hand in a folder on the server's volume.
func TestCurseForgeModpackEnv(t *testing.T) {
	env := envOf(server(func(mc *avsysv1.MinecraftServer) {
		mc.Spec.Modpack = &avsysv1.ModpackSpec{Source: avsysv1.SourceCurseForge, Project: "all-the-mods-9", Version: "5000001", GameVersion: "1.20.1", Loader: "forge"}
	}))
	for name, want := range map[string]string{
		"MODPACK_PLATFORM":  "AUTO_CURSEFORGE",
		"CF_SLUG":           "all-the-mods-9",
		"CF_FILE_ID":        "5000001",
		"CF_DOWNLOADS_REPO": "/data/downloads",
	} {
		if env[name] != want {
			t.Errorf("%s = %q, want %q", name, env[name], want)
		}
	}
}

// The image does not install datapacks from CurseForge: one is fetched from
// where CurseForge keeps its file, as one from a URL is.
func TestCurseForgeDatapackEnv(t *testing.T) {
	env := envOf(server(func(mc *avsysv1.MinecraftServer) {
		mc.Spec.Mods = []avsysv1.ContentRef{
			{Source: avsysv1.SourceCurseForge, Kind: avsysv1.KindMod, Project: "238222", Version: "4593548"},
			{Source: avsysv1.SourceCurseForge, Kind: avsysv1.KindDatapack, Project: "500", Version: "4012345", File: "Better Trees.zip"},
		}
	}))
	if env["CURSEFORGE_FILES"] != "238222:4593548" {
		t.Errorf("CURSEFORGE_FILES = %q", env["CURSEFORGE_FILES"])
	}
	if env["DATAPACKS"] != "https://mediafilez.forgecdn.net/files/4012/345/Better%20Trees.zip" {
		t.Errorf("DATAPACKS = %q", env["DATAPACKS"])
	}
}

// The key is put in a namespace's Secret, changed there, and taken out
// again without deleting the Secret, which the panel may not.
func TestEnsureCurseForgeSecret(t *testing.T) {
	ctx := context.Background()
	cl := fake.NewClientBuilder().Build()
	read := func() map[string][]byte {
		t.Helper()
		s := &corev1.Secret{}
		if err := cl.Get(ctx, client.ObjectKey{Namespace: "mc-test", Name: CurseForgeSecret}, s); err != nil {
			t.Fatal(err)
		}
		return s.Data
	}
	// None, and no Secret: nothing is made.
	if err := EnsureCurseForgeSecret(ctx, cl, "mc-test", ""); err != nil {
		t.Fatal(err)
	}
	if err := cl.Get(ctx, client.ObjectKey{Namespace: "mc-test", Name: CurseForgeSecret}, &corev1.Secret{}); err == nil {
		t.Error("a Secret was made for no key")
	}
	for _, key := range []string{"one", "two"} {
		if err := EnsureCurseForgeSecret(ctx, cl, "mc-test", key); err != nil {
			t.Fatal(err)
		}
		if got := string(read()[CurseForgeSecretKey]); got != key {
			t.Errorf("got %q, want %q", got, key)
		}
	}
	if err := EnsureCurseForgeSecret(ctx, cl, "mc-test", ""); err != nil {
		t.Fatal(err)
	}
	if got := read(); len(got) != 0 {
		t.Errorf("after taking it out: %v", got)
	}
}
