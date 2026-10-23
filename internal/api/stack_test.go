package api

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"io"
	"os"
	"path"
	"slices"
	"testing"

	"github.com/pkg/sftp"
	"sigs.k8s.io/controller-runtime/pkg/client"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
)

// stacked puts a pack of these files in a server's files, as one in a stack
// is kept, and returns it as the stack has it.
func stacked(t *testing.T, sc *sftp.Client, name string, files map[string]string) avsysv1.StackedPack {
	t.Helper()
	f, size := zipWith(t, files)
	data, err := io.ReadAll(io.NewSectionReader(f, 0, size))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha1.Sum(data)
	sha := hex.EncodeToString(sum[:])
	if err := writePack(sc, bytes.NewReader(data), sha); err != nil {
		t.Fatal(err)
	}
	return avsysv1.StackedPack{SHA1: sha, Size: size, Name: name + ".zip"}
}

// packsIn lists the panel's pack files in a server's files.
func packsIn(t *testing.T, sc *sftp.Client) []string {
	t.Helper()
	entries, err := sc.ReadDir(packDir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	slices.Sort(out)
	return out
}

func fileNames(shas ...string) []string {
	out := []string{}
	for _, sha := range shas {
		out = append(out, path.Base(packFile(sha)))
	}
	slices.Sort(out)
	return out
}

// Packs joined into one are the server's pack: kept in its files beside
// those it is joined from, and by the panel, which serves it. Put in another
// order it is joined again; with none it has none, and their files go.
func TestRestack(t *testing.T) {
	p := newPanel(t)
	p.srv.Config.PackDir = t.TempDir()
	ctx := context.Background()
	sc := memFiles(t)
	mc := &avsysv1.MinecraftServer{}
	if err := p.srv.Client.Get(ctx, client.ObjectKey{Namespace: "mc-alpha", Name: "world"}, mc); err != nil {
		t.Fatal(err)
	}
	mc.Spec.ResourcePack = &avsysv1.ResourcePackSpec{URL: "https://example.com/old.zip", Enforce: true, Prompt: "Please"}
	if err := p.srv.Client.Update(ctx, mc); err != nil {
		t.Fatal(err)
	}
	const meta = `{"pack":{"pack_format":46,"description":"x"}}`
	a := stacked(t, sc, "Faithful", map[string]string{"pack.mcmeta": meta, "assets/minecraft/textures/block/stone.png": "a stone", "assets/minecraft/lang/en_us.json": `{"a":"A"}`})
	b := stacked(t, sc, "Sounds", map[string]string{"pack.mcmeta": meta, "assets/minecraft/textures/block/stone.png": "b stone", "assets/minecraft/lang/en_us.json": `{"b":"B"}`})

	got, err := p.srv.restack(ctx, mc, sc, []avsysv1.StackedPack{a, b})
	if err != nil {
		t.Fatal(err)
	}
	rp := got.Spec.ResourcePack
	if rp == nil || rp.Uploaded == nil || len(rp.Stack) != 2 || rp.URL != p.srv.packURL(rp.SHA1) || !rp.Enforce || rp.Prompt != "Please" {
		t.Fatalf("the server's pack is %+v", rp)
	}
	// What the one under has that the top one has too is said.
	if rp.Stack[0].Hidden != 0 || rp.Stack[1].Hidden != 1 || rp.Stack[1].HiddenBy != a.SHA1 {
		t.Errorf("hidden: %+v", rp.Stack)
	}
	// Served from what the panel keeps, which is what is in its files.
	served := readPack(t, p.srv.cachedPack(rp.SHA1))
	if sum := sha1.Sum(served); hex.EncodeToString(sum[:]) != rp.SHA1 {
		t.Error("the pack kept by the panel is not of the SHA-1 the server offers")
	}
	inFiles := readFrom(t, sc, packFile(rp.SHA1))
	if !bytes.Equal(served, inFiles) {
		t.Error("the pack in the server's files is not the one served")
	}
	z, err := zip.NewReader(bytes.NewReader(served), int64(len(served)))
	if err != nil {
		t.Fatal(err)
	}
	if body := zipFile(t, z, "assets/minecraft/textures/block/stone.png"); body != "a stone" {
		t.Errorf("stone.png is %q, want the top pack's", body)
	}
	if body := zipFile(t, z, "assets/minecraft/lang/en_us.json"); body != "{\n  \"a\": \"A\",\n  \"b\": \"B\"\n}" {
		t.Errorf("en_us.json is %q, want both packs' keys", body)
	}
	if want := fileNames(a.SHA1, b.SHA1, rp.SHA1); !slices.Equal(packsIn(t, sc), want) {
		t.Errorf("files %v, want %v", packsIn(t, sc), want)
	}
	first := rp.SHA1

	// The other way up: another pack, and the one before is gone.
	got, err = p.srv.restack(ctx, got, sc, []avsysv1.StackedPack{rp.Stack[1], rp.Stack[0]})
	if err != nil {
		t.Fatal(err)
	}
	rp = got.Spec.ResourcePack
	if rp.SHA1 == first || rp.Stack[0].SHA1 != b.SHA1 || rp.Stack[1].HiddenBy != b.SHA1 {
		t.Errorf("turned over: %+v", rp)
	}
	if want := fileNames(a.SHA1, b.SHA1, rp.SHA1); !slices.Equal(packsIn(t, sc), want) {
		t.Errorf("files %v, want %v", packsIn(t, sc), want)
	}

	// Back again: the same packs in the same order are the same pack.
	got, err = p.srv.restack(ctx, got, sc, []avsysv1.StackedPack{a, b})
	if err != nil {
		t.Fatal(err)
	}
	if got.Spec.ResourcePack.SHA1 != first {
		t.Error("the same packs in the same order were joined into another pack")
	}

	// Saved as the server's.
	saved := &avsysv1.MinecraftServer{}
	if err := p.srv.Client.Get(ctx, client.ObjectKeyFromObject(mc), saved); err != nil {
		t.Fatal(err)
	}
	if saved.Spec.ResourcePack == nil || saved.Spec.ResourcePack.SHA1 != first || len(saved.Spec.ResourcePack.Stack) != 2 {
		t.Errorf("saved %+v", saved.Spec.ResourcePack)
	}

	// None: no pack, and none of their files.
	got, err = p.srv.restack(ctx, got, sc, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Spec.ResourcePack != nil {
		t.Errorf("with no packs, the server has %+v", got.Spec.ResourcePack)
	}
	if left := packsIn(t, sc); len(left) != 0 {
		t.Errorf("files left: %v", left)
	}
}

// A server's one pack, not stacked yet, is the one under: an uploaded one's
// file is there already.
func TestStackOfOnePack(t *testing.T) {
	p := newPanel(t)
	p.srv.Config.PackDir = t.TempDir()
	sha := "0123456789abcdef0123456789abcdef01234567"
	rp := &avsysv1.ResourcePackSpec{URL: p.srv.packURL(sha), SHA1: sha, Uploaded: &avsysv1.UploadedPack{Name: "mine.zip", Size: 9}, Formats: &avsysv1.PackFormats{Min: 46, Max: 46}}
	stack, err := p.srv.stackOf(context.Background(), memFiles(t), rp, packLimit(""))
	if err != nil {
		t.Fatal(err)
	}
	if len(stack) != 1 || stack[0].SHA1 != sha || stack[0].Name != "mine.zip" || stack[0].Size != 9 || stack[0].Formats == nil {
		t.Errorf("stack %+v", stack)
	}
	stack, err = p.srv.stackOf(context.Background(), memFiles(t), nil, packLimit(""))
	if err != nil || len(stack) != 0 {
		t.Errorf("no pack: %+v, %v", stack, err)
	}
}

// What a pack is joined from is changed only by joining it again: a change
// of settings keeps it with the same pack, and lets go of it with another.
func TestKeepStack(t *testing.T) {
	sha := "0123456789abcdef0123456789abcdef01234567"
	was := &avsysv1.MinecraftServerSpec{ResourcePack: &avsysv1.ResourcePackSpec{URL: "https://p/packs/" + sha + ".zip", SHA1: sha, Uploaded: &avsysv1.UploadedPack{Size: 9}, Stack: []avsysv1.StackedPack{{SHA1: "aa"}, {SHA1: "bb"}}}}
	// The prompt changed, and a made-up stack sent with it.
	spec := was.DeepCopy()
	spec.ResourcePack.Prompt = "Please"
	spec.ResourcePack.Stack = []avsysv1.StackedPack{{SHA1: "evil"}}
	if err := keepUploadedPack(spec, was); err != nil {
		t.Fatal(err)
	}
	if got := spec.ResourcePack.Stack; len(got) != 2 || got[0].SHA1 != "aa" {
		t.Errorf("kept %+v", got)
	}
	// Another pack, from an address: not joined from anything.
	spec = was.DeepCopy()
	spec.ResourcePack.URL, spec.ResourcePack.SHA1 = "https://example.com/other.zip", ""
	if err := keepUploadedPack(spec, was); err != nil {
		t.Fatal(err)
	}
	if spec.ResourcePack.Stack != nil || spec.ResourcePack.Uploaded != nil {
		t.Errorf("another pack kept %+v", spec.ResourcePack)
	}
	if got := keptPacks(was.ResourcePack); !slices.Equal(got, []string{sha, "aa", "bb"}) {
		t.Errorf("kept packs %v", got)
	}
}

func readPack(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func readFrom(t *testing.T, sc *sftp.Client, name string) []byte {
	t.Helper()
	f, err := sc.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func zipFile(t *testing.T, z *zip.Reader, name string) string {
	t.Helper()
	f, err := z.Open(name)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
