package api

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
)

// datapackWorld is a world with datapacks of every kind there is, under
// prefix: a .zip and a folder with a pack.mcmeta and a pack.png, one of
// each without a picture, one whose pack.mcmeta is broken, Bukkit's own
// folder, a folder that is not a datapack, a hidden file and a file that is
// not a .zip.
func datapackWorld(t *testing.T, prefix string) map[string]string {
	t.Helper()
	icon := aPicture(t)
	return map[string]string{
		prefix + "level.dat":                    "the world",
		prefix + "datapacks/bern.zip":           string(jarOf(t, map[string]string{"pack.mcmeta": `{"pack":{"pack_format":15,"description":"Bern's\ntrades"}}`, "pack.png": icon})),
		prefix + "datapacks/hi_drehmal.zip":     string(jarOf(t, map[string]string{"pack.mcmeta": `{"pack":{"description":[{"text":"Hi, "},{"text":"§aDrehmal"}]}}`})),
		prefix + "datapacks/broken.zip":         string(jarOf(t, map[string]string{"pack.mcmeta": `{"pack":`})),
		prefix + "datapacks/mael/pack.mcmeta":   `{"pack":{"description":"Mael's"}}`,
		prefix + "datapacks/mael/pack.png":      icon,
		prefix + "datapacks/mael/data/x/f.json": "{}",
		prefix + "datapacks/emis/pack.mcmeta":   `{"pack":{"description":"Emi's"}}`,
		prefix + "datapacks/bukkit/pack.mcmeta": `{"pack":{"description":"Data pack for resources provided by Bukkit plugins"}}`,
		prefix + "datapacks/loose/readme.txt":   "not a datapack",
		prefix + "datapacks/.hidden.zip":        "hidden",
		prefix + "datapacks/notes.txt":          "not a datapack",
	}
}

// What datapackWorld's datapacks are, as read.
var datapackWorldRead = []worldDatapack{
	{Name: "bern.zip", Title: "bern", About: "Bern's trades", Picture: true},
	{Name: "broken.zip", Title: "broken"},
	{Name: "emis", Title: "emis", About: "Emi's", Folder: true},
	{Name: "hi_drehmal.zip", Title: "hi_drehmal", About: "Hi, Drehmal"},
	{Name: "mael", Title: "mael", About: "Mael's", Folder: true, Picture: true},
}

func sameDatapacks(t *testing.T, what string, got []worldDatapack) {
	t.Helper()
	if jsonOf(t, got) != jsonOf(t, datapackWorldRead) {
		t.Errorf("%s: %s\nwant %s", what, jsonOf(t, got), jsonOf(t, datapackWorldRead))
	}
}

// A world's datapacks are read as a server's are found: .zip files and
// folders with a pack.mcmeta, each with what its pack.mcmeta says, on one
// line, and its picture; not Bukkit's own, nor anything else. A world with
// no datapacks folder has none.
func TestReadDatapacks(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, datapackWorld(t, "realm/"))
	list, pictures, err := readDatapacks(context.Background(), DirFiles(root), "realm")
	if err != nil {
		t.Fatal(err)
	}
	sameDatapacks(t, "read", list)
	if len(pictures) != 2 || pictures["bern.zip"] == nil || pictures["mael"] == nil {
		t.Errorf("pictures of %v", keysOf(bytesKeys(pictures)))
	}
	if list, _, err := readDatapacks(context.Background(), DirFiles(root), "elsewhere"); err != nil || list == nil || len(list) != 0 {
		t.Errorf("no datapacks folder: %v, %v", list, err)
	}
}

func bytesKeys(m map[string][]byte) map[string]string {
	out := map[string]string{}
	for k := range m {
		out[k] = ""
	}
	return out
}

// worldDatapacksOf are a library world's datapacks, as listed.
func worldDatapacksOf(t *testing.T, call func(who, method, path, body string) (int, []byte), id string) []worldDatapack {
	t.Helper()
	code, raw := call("viewer", "GET", "/orgs/alpha/worlds/"+id+"/datapacks", "")
	if code != http.StatusOK {
		t.Fatalf("its datapacks: %d %s", code, raw)
	}
	var out []worldDatapack
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// A world's datapacks are read as it is kept, uploaded or saved from a
// snapshot, and kept with it: its row says how many, its datapacks are
// listed with what each says of itself, and their pictures are served to
// its organization alone. Taken out of the library, they go with it.
func TestLibraryWorldDatapacks(t *testing.T) {
	p, repo, call := libraryPanel(t)
	zipped := worldZip(t, datapackWorld(t, "Realm/"))
	code, raw := call("operator", "POST", "/orgs/alpha/worlds/upload?name=Realm&root=Realm", string(zipped))
	if code != http.StatusCreated {
		t.Fatalf("uploaded: %d %s", code, raw)
	}
	id := objectOf(raw)["id"].(string)
	if got := objectOf(raw)["datapacks"]; got != float64(5) {
		t.Errorf("uploaded with %v datapacks", got)
	}
	_, raw = call("viewer", "GET", "/orgs/alpha/worlds", "")
	var list []map[string]any
	_ = json.Unmarshal(raw, &list)
	if len(list) != 1 || list[0]["datapacks"] != float64(5) || list[0]["readingDatapacks"] != nil {
		t.Errorf("listed: %v", list)
	}
	sameDatapacks(t, "uploaded", worldDatapacksOf(t, call, id))

	// Pictures: those it has, to its own organization.
	for name, want := range map[string]int{"bern.zip": 200, "mael": 200, "hi_drehmal.zip": 404, "emis": 404, "nothing.zip": 404, "../x": 404, "": 404} {
		if code, _ := call("viewer", "GET", "/orgs/alpha/worlds/"+id+"/datapacks/icon?name="+name, ""); code != want {
			t.Errorf("%q's picture: %d, want %d", name, code, want)
		}
	}
	if code, got := call("viewer", "GET", "/orgs/alpha/worlds/"+id+"/datapacks/icon?name=bern.zip", ""); code != 200 || !strings.HasPrefix(string(got), "\x89PNG") {
		t.Errorf("a picture: %d %q", code, got[:min(len(got), 8)])
	}
	if code, _ := call("outsider", "GET", "/orgs/alpha/worlds/"+id+"/datapacks/icon?name=bern.zip", ""); code == http.StatusOK {
		t.Error("an outsider was given a picture")
	}
	if code, _ := call("outsider", "GET", "/orgs/alpha/worlds/"+id+"/datapacks", ""); code == http.StatusOK {
		t.Error("an outsider was given the datapacks")
	}
	// Not through another organization, its own though it is.
	if code, _ := call("outsider", "GET", "/orgs/beta/worlds/"+id+"/datapacks/icon?name=bern.zip", ""); code != http.StatusNotFound {
		t.Errorf("through another organization: %d", code)
	}
	if code, _ := call("outsider", "GET", "/orgs/beta/worlds/"+id+"/datapacks", ""); code != http.StatusNotFound {
		t.Errorf("listed through another organization: %d", code)
	}

	// Saved from a server's snapshot, its world named otherwise: read from
	// what was kept.
	server := t.TempDir()
	files := map[string]string{
		"data/server.properties":    "level-name=drehmal\n",
		"data/.avsys/snapshot.json": `{"format":1,"org":"alpha","server":"survival","spec":{"eula":true,"loader":"paper","version":"1.21.4","resources":{"memory":"4Gi"},"storage":{"size":"10Gi"},"expose":{"type":"Router"},"env":[{"name":"LEVEL","value":"drehmal"}]}}`,
	}
	for name, content := range datapackWorld(t, "data/drehmal/") {
		files[name] = content
	}
	writeFiles(t, server, files)
	resticIn(t, repo, server, "backup", "data", "--host", "survival", "--tag", "avsys", "--tag", "trigger=manual")
	snaps, _ := p.srv.Backups.List(t.Context(), "alpha")
	var snap string
	for _, s := range snaps {
		if s.Server == "survival" {
			snap = s.ID
		}
	}
	code, raw = call("operator", "POST", "/orgs/alpha/worlds?wait=true", `{"name":"Drehmal","snapshot":"`+snap+`"}`)
	if code != http.StatusCreated {
		t.Fatalf("saved: %d %s", code, raw)
	}
	saved := objectOf(raw)
	if saved["root"] != "drehmal" || saved["datapacks"] != float64(5) {
		t.Errorf("saved as %v", saved)
	}
	sameDatapacks(t, "saved", worldDatapacksOf(t, call, saved["id"].(string)))
	if code, _ := call("viewer", "GET", "/orgs/alpha/worlds/"+saved["id"].(string)+"/datapacks/icon?name=mael", ""); code != 200 {
		t.Errorf("a saved world's datapack's picture: %d", code)
	}
	if left, _ := os.ReadDir(p.srv.Backups.WorkDir("alpha")); len(left) != 0 {
		t.Errorf("left taken out: %v", left)
	}

	// Taken out of the library, its pictures go with it.
	if code, _ := call("admin", "DELETE", "/orgs/alpha/worlds/"+id, ""); code != http.StatusNoContent {
		t.Fatalf("deleted: %d", code)
	}
	if _, _, err := p.srv.Store.Image(t.Context(), "world-datapack:"+id+"/bern.zip"); err == nil {
		t.Error("a picture stayed with its world gone")
	}
	if code, _ := call("viewer", "GET", "/orgs/alpha/worlds/"+id+"/datapacks/icon?name=bern.zip", ""); code != http.StatusNotFound {
		t.Errorf("a deleted world's picture: %d", code)
	}
}

// A world kept before its datapacks were read has them read as the panel
// starts; and before then, in the background, as soon as the library is
// looked at, which is answered at once, saying they are being read.
func TestLibraryDatapacksReadLater(t *testing.T) {
	p, _, call := libraryPanel(t)
	ctx := context.Background()
	org, err := p.srv.Store.OrgBySlug(ctx, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	orgID := org.ID
	upload := func(name string) string {
		code, raw := call("operator", "POST", "/orgs/alpha/worlds/upload?name="+name+"&root=Realm", string(worldZip(t, datapackWorld(t, "Realm/"))))
		if code != http.StatusCreated {
			t.Fatalf("uploaded: %d %s", code, raw)
		}
		id := objectOf(raw)["id"].(string)
		// As one kept before they were read.
		if err := p.srv.Store.SetWorldDatapacks(ctx, orgID, id, "", nil); err != nil {
			t.Fatal(err)
		}
		return id
	}
	started, viewed := upload("Started"), upload("Viewed")

	// Looked at: said to be read, and read.
	_, raw := call("viewer", "GET", "/orgs/alpha/worlds/"+viewed, "")
	if got := objectOf(raw); got["datapacks"] != nil || got["readingDatapacks"] != true {
		t.Errorf("before: %v", got)
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		_, raw := call("viewer", "GET", "/orgs/alpha/worlds/"+viewed, "")
		if got := objectOf(raw); got["datapacks"] == float64(5) && got["readingDatapacks"] == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("never read: %s", raw)
		}
		time.Sleep(50 * time.Millisecond)
	}
	sameDatapacks(t, "read once looked at", worldDatapacksOf(t, call, viewed))

	// As the panel starts.
	p.srv.ReadLibraryDatapacks(ctx)
	sameDatapacks(t, "read as the panel starts", worldDatapacksOf(t, call, started))
	if code, _ := call("viewer", "GET", "/orgs/alpha/worlds/"+started+"/datapacks/icon?name=bern.zip", ""); code != 200 {
		t.Errorf("its picture: %d", code)
	}
	_, raw = call("viewer", "GET", "/orgs/alpha/worlds", "")
	var list []map[string]any
	_ = json.Unmarshal(raw, &list)
	for _, w := range list {
		if w["datapacks"] != float64(5) || w["readingDatapacks"] != nil {
			t.Errorf("listed: %v", w)
		}
	}
	if left, _ := os.ReadDir(p.srv.Backups.WorkDir("alpha")); len(left) != 0 {
		t.Errorf("left taken out: %v", left)
	}
}

// A library world put in a server, as a new server, Replace the world and
// Put back do: its datapacks are on the server's Content list straight
// away, as regular entries, each as the library showed it.
func TestLibraryWorldDatapacksOnAServer(t *testing.T) {
	p, repo, call := libraryPanel(t)
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh is not installed")
	}
	code, raw := call("operator", "POST", "/orgs/alpha/worlds/upload?name=Realm&root=Realm", string(worldZip(t, datapackWorld(t, "Realm/"))))
	if code != http.StatusCreated {
		t.Fatalf("uploaded: %d %s", code, raw)
	}
	id := objectOf(raw)["id"].(string)
	library := worldDatapacksOf(t, call, id)
	kept, _ := libraryOf(t, p, id)
	if len(kept) != 1 {
		t.Fatalf("kept as %+v", kept)
	}

	// The server's files as its workspace has them once the world is put
	// back where an archive would be unpacked, as RestoreFolders puts it,
	// and the import's script has put it in place.
	data := filepath.Join(t.TempDir(), "data")
	writeFiles(t, data, map[string]string{"server.properties": "motd=hi\n"})
	resticIn(t, repo, filepath.Dir(data), "restore", kept[0].ID+":/data", "--target", filepath.Join(data, ".avsys", "import"))
	script := regexp.MustCompile(regexp.QuoteMeta(dataRoot)+`\b`).ReplaceAllString(worldScript, data)
	cmd := exec.Command("sh", "-c", script)
	cmd.Stdin = strings.NewReader(keptWorld + "\n\n\n\n\n\nrestored\n")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("script: %v: %s", err, out)
	}
	p.fileServer(data)
	p.srv.catalog.http.Transport = &hashModrinth{}
	// As the import ends.
	key := client.ObjectKey{Namespace: p.srv.Namespace("alpha"), Name: "world"}
	if err := p.srv.markImporting(context.Background(), key, ""); err != nil {
		t.Fatal(err)
	}
	p.srv.lookAtImported(context.Background(), key)

	var got []string
	for _, m := range p.serverOf("world").Spec.Mods {
		got = append(got, string(m.Source)+" "+string(m.Kind)+" "+m.Project+" "+m.Description)
	}
	var want []string
	for _, d := range library {
		want = append(want, string(avsysv1.SourceUpload)+" "+string(avsysv1.KindDatapack)+" "+d.Name+" "+d.About)
	}
	if strings.Join(sorted(got), "\n") != strings.Join(sorted(want), "\n") {
		t.Errorf("on the server:\n%s\nwant\n%s", strings.Join(sorted(got), "\n"), strings.Join(sorted(want), "\n"))
	}
	if found := p.foundOn("world"); len(found) != 0 {
		t.Errorf("left found: %v", found)
	}
	for _, d := range library {
		want := http.StatusNotFound
		if d.Picture {
			want = http.StatusOK
		}
		if rec := p.send("viewer", "GET", "/orgs/alpha/servers/world/content/icon?kind=datapack&name="+d.Name, nil); rec.Code != want {
			t.Errorf("%s's picture on the server: %d, want %d", d.Name, rec.Code, want)
		}
	}
}

func sorted(list []string) []string {
	out := slices.Clone(list)
	slices.Sort(out)
	return out
}
