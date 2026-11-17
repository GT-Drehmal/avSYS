package api

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
	"github.com/GT-Drehmal/avSYS/internal/sshgw"
	"github.com/GT-Drehmal/avSYS/internal/store"
)

// nbt writes the game's NBT, for level.dat files to read.
type nbt struct{ bytes.Buffer }

func (n *nbt) name(t byte, name string) {
	n.WriteByte(t)
	n.str(name)
}

func (n *nbt) str(s string) {
	_ = binary.Write(n, binary.BigEndian, uint16(len(s)))
	n.WriteString(s)
}

func (n *nbt) long(name string, v int64) {
	n.name(nbtLong, name)
	_ = binary.Write(n, binary.BigEndian, v)
}

func (n *nbt) string(name, v string) {
	n.name(nbtString, name)
	n.str(v)
}

// levelDat is a level.dat as the game writes it, gzipped, with the values
// read out of it among others that are not.
func levelDat(t *testing.T, name, version string, seed int64, older bool, lastPlayed int64) []byte {
	t.Helper()
	var n nbt
	n.name(nbtCompound, "")
	n.name(nbtCompound, "Data")
	n.name(nbtByte, "hardcore")
	n.WriteByte(0)
	n.name(nbtInt, "DataVersion")
	_ = binary.Write(&n, binary.BigEndian, int32(4189))
	n.name(nbtDouble, "BorderSize")
	_ = binary.Write(&n, binary.BigEndian, float64(6e7))
	n.name(nbtList, "ServerBrands")
	n.WriteByte(nbtString)
	_ = binary.Write(&n, binary.BigEndian, int32(1))
	n.str("vanilla")
	n.name(nbtIntArray, "WanderingTraderId")
	_ = binary.Write(&n, binary.BigEndian, int32(2))
	_ = binary.Write(&n, binary.BigEndian, [2]int32{1, 2})
	n.name(nbtList, "Empty")
	n.WriteByte(nbtEnd)
	_ = binary.Write(&n, binary.BigEndian, int32(0))
	n.string("LevelName", name)
	n.long("LastPlayed", lastPlayed)
	n.name(nbtCompound, "Version")
	n.string("Name", version)
	n.name(nbtByte, "Snapshot")
	n.WriteByte(0)
	n.WriteByte(nbtEnd)
	if older {
		n.long("RandomSeed", seed)
	} else {
		n.name(nbtCompound, "WorldGenSettings")
		n.long("seed", seed)
		n.name(nbtCompound, "dimensions")
		n.WriteByte(nbtEnd)
		n.WriteByte(nbtEnd)
	}
	n.WriteByte(nbtEnd)
	n.WriteByte(nbtEnd)
	var out bytes.Buffer
	z := gzip.NewWriter(&out)
	if _, err := z.Write(n.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

// A world's level.dat says what it is called, the game version it was last
// opened in, its seed and when it was last played: in WorldGenSettings, or
// as worlds from before 1.16 keep it.
func TestReadLevelDat(t *testing.T) {
	played := time.Date(2026, 11, 2, 10, 30, 0, 0, time.UTC)
	for _, older := range []bool{false, true} {
		info, err := readLevelDat(bytes.NewReader(levelDat(t, "Drehmal 2.2", "1.21.4", -4172144997902289642, older, played.UnixMilli())))
		if err != nil {
			t.Fatalf("older %v: %v", older, err)
		}
		if info.Name != "Drehmal 2.2" || info.Version != "1.21.4" || info.Seed != "-4172144997902289642" || info.LastPlayed == nil || !info.LastPlayed.Equal(played) {
			t.Errorf("older %v: %+v", older, info)
		}
	}
	for _, bad := range [][]byte{nil, []byte("not a world"), {0x1f, 0x8b, 1, 2}, {nbtCompound, 0, 0, nbtList, 0, 1, 'x', 99, 0, 0, 0, 1}} {
		if _, err := readLevelDat(bytes.NewReader(bad)); err == nil {
			t.Errorf("%q was read as a world", bad)
		}
	}
}

// What a server's world says of itself is read from its files: in the
// folder server.properties names, with how large it is. One with no world
// yet says so, and one whose files cannot be reached is a 502 (see
// TestOrganizationRoutes).
func TestServerWorldFacts(t *testing.T) {
	p := newPanel(t)
	sc := p.withFiles("alpha", "world")
	get := func() (int, worldFacts) {
		t.Helper()
		req := httptest.NewRequest("GET", "/api/v1/orgs/alpha/servers/world/world", nil)
		req.Header.Set("Authorization", "Bearer "+p.tokens["viewer"])
		rec := httptest.NewRecorder()
		p.h.ServeHTTP(rec, req)
		var f worldFacts
		_ = json.Unmarshal(rec.Body.Bytes(), &f)
		return rec.Code, f
	}
	if code, f := get(); code != 200 || f.Exists || f.Level != "world" || f.Size != nil {
		t.Errorf("with no world yet: %d %+v", code, f)
	}
	played := time.Date(2026, 11, 2, 10, 30, 0, 0, time.UTC)
	dat := levelDat(t, "Island", "1.21.4", 1234, false, played.UnixMilli())
	for file, content := range map[string]string{
		"/data/server.properties":                    "motd=hi\r\nlevel-name=island\r\n",
		"/data/island/level.dat":                     string(dat),
		"/data/island/region/r.0.0.mca":              strings.Repeat("r", 1000),
		"/data/island_nether/DIM-1/region/r.0.0.mca": strings.Repeat("n", 500),
		"/data/world/level.dat":                      "another world, not the one opened",
	} {
		if err := sc.MkdirAll(filepath.Dir(file)); err != nil {
			t.Fatal(err)
		}
		put(t, sc, file, content)
	}
	code, f := get()
	if code != 200 || !f.Exists || f.Level != "island" || f.Name != "Island" || f.Version != "1.21.4" || f.Seed != "1234" || f.LastPlayed == nil || !f.LastPlayed.Equal(played) {
		t.Errorf("the world: %d %+v", code, f)
	}
	if want := int64(len(dat) + 1500); f.Size == nil || *f.Size != want {
		t.Errorf("its size: %v, want %d", f.Size, want)
	}
	// A level.dat that cannot be read still has a world, of a size.
	put(t, sc, "/data/island/level.dat", "broken")
	if code, f := get(); code != 200 || !f.Exists || f.Name != "" || f.Size == nil || *f.Size != 1506 {
		t.Errorf("an unreadable level.dat: %d %+v", code, f)
	}
	// Not while files are being put in.
	mc := &avsysv1.MinecraftServer{}
	if err := p.srv.Client.Get(context.Background(), client.ObjectKey{Namespace: "mc-alpha", Name: "world"}, mc); err != nil {
		t.Fatal(err)
	}
	mc.Annotations = map[string]string{avsysv1.AnnotationImporting: "true"}
	if err := p.srv.Client.Update(context.Background(), mc); err != nil {
		t.Fatal(err)
	}
	if code, _ := get(); code != 409 {
		t.Errorf("while importing: %d", code)
	}
}

// Every way an import puts a world in says where it came from.
func TestImportedOrigin(t *testing.T) {
	seed := "  42 "
	lib := &store.World{ID: "w1", Name: "Drehmal 2.2.2f"}
	tmpl := &store.Template{ID: "t1", Name: "Event"}
	for _, c := range []struct {
		name     string
		query    string
		library  *store.World
		template *store.Template
		own      *ownWorld
		made     map[string]*string
		want     avsysv1.WorldOrigin
	}{
		{"a new world in its place", "world=new&replace=true", nil, nil, nil, map[string]*string{"level-seed": &seed}, avsysv1.WorldOrigin{Kind: avsysv1.WorldNew, Seed: "42"}},
		{"a new random world", "world=new&replace=true", nil, nil, nil, map[string]*string{"level-seed": nil}, avsysv1.WorldOrigin{Kind: avsysv1.WorldNew}},
		{"a server's archive without its world", "world=none&from=upload&file=old.zip", nil, nil, nil, nil, avsysv1.WorldOrigin{Kind: avsysv1.WorldNew}},
		{"one from the library", "world=true&replace=true&library=w1", lib, nil, nil, nil, avsysv1.WorldOrigin{Kind: avsysv1.WorldLibrary, Library: &avsysv1.WorldRef{ID: "w1", Name: "Drehmal 2.2.2f"}}},
		{"a template's own, uploaded", "world=true&template=t1", nil, tmpl, &ownWorld{File: "w.zip"}, nil, avsysv1.WorldOrigin{Kind: avsysv1.WorldTemplate, Template: &avsysv1.WorldRef{ID: "t1", Name: "Event"}}},
		{"a template's own, from the library", "world=true&template=t1", nil, tmpl, &ownWorld{Library: &avsysv1.WorldRef{ID: "w1", Name: "Drehmal"}}, nil, avsysv1.WorldOrigin{Kind: avsysv1.WorldLibrary, Library: &avsysv1.WorldRef{ID: "w1", Name: "Drehmal"}, Template: &avsysv1.WorldRef{ID: "t1", Name: "Event"}}},
		{"one uploaded", "world=true&replace=true&from=upload&file=My%20World.zip", nil, nil, nil, nil, avsysv1.WorldOrigin{Kind: avsysv1.WorldUpload, File: "My World.zip"}},
		{"one uploaded from a folder", "world=true&from=upload&file=C:%5Cusers%5Cme%5Cw.zip", nil, nil, nil, nil, avsysv1.WorldOrigin{Kind: avsysv1.WorldUpload, File: "w.zip"}},
		{"a server's archive with its world", "from=upload&file=server.tar.gz", nil, nil, nil, nil, avsysv1.WorldOrigin{Kind: avsysv1.WorldArchive, File: "server.tar.gz"}},
		{"an archive with no name", "", nil, nil, nil, nil, avsysv1.WorldOrigin{Kind: avsysv1.WorldArchive}},
	} {
		q, err := url.ParseQuery(c.query)
		if err != nil {
			t.Fatal(err)
		}
		got := importedOrigin(q, c.library, c.template, c.own, c.made)
		if got.Time.IsZero() {
			t.Errorf("%s: no time", c.name)
		}
		got.Time = metav1.Time{}
		if !equalOrigin(got, &c.want) {
			t.Errorf("%s: got %s, want %s", c.name, show(got), show(&c.want))
		}
	}
}

func equalOrigin(a, b *avsysv1.WorldOrigin) bool {
	return show(a) == show(b)
}

func show(o *avsysv1.WorldOrigin) string {
	if o == nil {
		return "none"
	}
	o = o.DeepCopy()
	o.Time = metav1.Time{}
	b, _ := json.Marshal(o)
	return string(b)
}

// A world that comes with files from elsewhere keeps where it came from
// before, with what it came by way of; where that was not known, what it
// came by way of is all there is.
func TestCarriedOrigin(t *testing.T) {
	lib := &avsysv1.WorldOrigin{Kind: avsysv1.WorldLibrary, Library: &avsysv1.WorldRef{ID: "w1", Name: "Drehmal"}, Server: "older", Time: metav1.NewTime(time.Now().Add(-time.Hour))}
	copyOf := func(server string) avsysv1.WorldOrigin {
		return avsysv1.WorldOrigin{Kind: avsysv1.WorldCopy, Server: server}
	}
	for _, c := range []struct {
		name string
		was  *avsysv1.WorldOrigin
		via  avsysv1.WorldOrigin
		want string
	}{
		{"none known", nil, copyOf("survival"), `{"kind":"copy","server":"survival","time":null}`},
		{"a library world copied", lib, copyOf("survival"), `{"kind":"library","library":{"id":"w1","name":"Drehmal"},"server":"survival","time":null}`},
		{"a library world from a snapshot", lib, avsysv1.WorldOrigin{Kind: avsysv1.WorldSnapshot, Server: "survival", Snapshot: "abcd1234"}, `{"kind":"library","library":{"id":"w1","name":"Drehmal"},"server":"survival","snapshot":"abcd1234","time":null}`},
		{"a copy of a copy", &avsysv1.WorldOrigin{Kind: avsysv1.WorldCopy, Server: "first"}, copyOf("second"), `{"kind":"copy","server":"second","time":null}`},
		{"a template's world copied", &avsysv1.WorldOrigin{Kind: avsysv1.WorldTemplate, Template: &avsysv1.WorldRef{ID: "t1"}}, copyOf("second"), `{"kind":"copy","server":"second","time":null}`},
		{"a new world copied", &avsysv1.WorldOrigin{Kind: avsysv1.WorldNew, Seed: "7"}, avsysv1.WorldOrigin{Kind: avsysv1.WorldTemplate, Template: &avsysv1.WorldRef{ID: "t1", Name: "Base"}}, `{"kind":"new","seed":"7","template":{"id":"t1","name":"Base"},"time":null}`},
	} {
		got := carriedOrigin(c.was, c.via)
		if time.Since(got.Time.Time) > time.Minute {
			t.Errorf("%s: put in at %v, not now", c.name, got.Time)
		}
		if show(got) != c.want {
			t.Errorf("%s: got %s, want %s", c.name, show(got), c.want)
		}
	}
	if lib.Server != "older" {
		t.Error("the original's origin was changed")
	}
}

// server reads a server as it is now.
func (p *panel) server(name string) *avsysv1.MinecraftServer {
	p.t.Helper()
	mc := &avsysv1.MinecraftServer{}
	if err := p.srv.Client.Get(context.Background(), client.ObjectKey{Namespace: "mc-alpha", Name: name}, mc); err != nil {
		p.t.Fatal(err)
	}
	return mc
}

// A new server's world is one the game makes, with the seed it is given.
// Where a world came from is the panel's to say: a request cannot say it,
// on a new server or a change of settings.
func TestANewServerHasANewWorld(t *testing.T) {
	p := newPanel(t)
	if code, msg := p.do("owner", "POST", "/orgs/alpha/servers", `{"name":"seeded","spec":{"eula":true,"version":"1.21.4","properties":{"level-seed":"1234"},"worldOrigin":{"kind":"library","library":{"id":"w1"}}}}`); code != 201 {
		t.Fatalf("made: %d %s", code, msg)
	}
	o := p.server("seeded").Spec.WorldOrigin
	if o == nil || o.Kind != avsysv1.WorldNew || o.Seed != "1234" || o.Library != nil || time.Since(o.Time.Time) > time.Minute {
		t.Errorf("a new server's world: %s", show(o))
	}
	if code, msg := p.do("owner", "POST", "/orgs/alpha/servers", `{"name":"random","spec":{"eula":true,"version":"1.21.4"}}`); code != 201 {
		t.Fatalf("made: %d %s", code, msg)
	}
	if o := p.server("random").Spec.WorldOrigin; o == nil || o.Kind != avsysv1.WorldNew || o.Seed != "" {
		t.Errorf("a random world: %s", show(o))
	}
	was := show(p.server("seeded").Spec.WorldOrigin)
	for _, patch := range []string{`{"spec":{"worldOrigin":{"kind":"upload","file":"x.zip"}}}`, `{"spec":{"worldOrigin":null}}`, `{"spec":{"displayName":"Seeded","properties":{"level-seed":"9"}}}`} {
		if code, msg := p.do("owner", "PATCH", "/orgs/alpha/servers/seeded", patch); code != 200 {
			t.Fatalf("%s: %d %s", patch, code, msg)
		}
		if now := show(p.server("seeded").Spec.WorldOrigin); now != was {
			t.Errorf("%s changed where its world came from: %s", patch, now)
		}
	}
}

// A copy of a server keeps the original's world, and so where it came from,
// copied from the original: a copy of a server made from the library can be
// put back to that world as the original can. With a new world, it is new.
func TestACopysWorldOrigin(t *testing.T) {
	p := newPanel(t)
	p.srv.Backups.Restic = "true"
	held := dialHeld{dialed: make(chan struct{}, 4)}
	p.srv.Gateway = &sshgw.Gateway{Dialer: held}
	copyOf := func(name, extra string) *avsysv1.WorldOrigin {
		t.Helper()
		if code, msg := p.do("owner", "POST", "/orgs/alpha/servers", `{"name":"`+name+`","fromServer":"world"`+extra+`}`); code != 201 {
			t.Fatalf("copy %s: %d %s", name, code, msg)
		}
		o := p.server(name).Spec.WorldOrigin
		if code, msg := p.do("owner", "DELETE", "/orgs/alpha/servers/"+name, ""); code != 204 {
			t.Fatalf("deleting %s: %d %s", name, code, msg)
		}
		deadline := time.Now().Add(10 * time.Second)
		for p.srv.ops.current("mc-alpha", "world") != nil {
			if time.Now().After(deadline) {
				t.Fatal("the original is still held")
			}
			time.Sleep(20 * time.Millisecond)
		}
		return o
	}
	// Nothing known of the original's world.
	if o := copyOf("copy1", ""); show(o) != `{"kind":"copy","server":"world","time":null}` {
		t.Errorf("a copy of a server with no origin: %s", show(o))
	}
	mc := p.server("world")
	mc.Spec.WorldOrigin = &avsysv1.WorldOrigin{Kind: avsysv1.WorldLibrary, Library: &avsysv1.WorldRef{ID: "w1", Name: "Drehmal"}, Time: metav1.NewTime(time.Now().Add(-time.Hour))}
	if err := p.srv.Client.Update(context.Background(), mc); err != nil {
		t.Fatal(err)
	}
	if o := copyOf("copy2", ""); show(o) != `{"kind":"library","library":{"id":"w1","name":"Drehmal"},"server":"world","time":null}` || time.Since(o.Time.Time) > time.Minute {
		t.Errorf("a copy of a library world: %s at %v", show(o), o.Time)
	}
	if o := copyOf("copy3", `,"world":"new","spec":{"properties":{"level-seed":"77"}}`); show(o) != `{"kind":"new","seed":"77","time":null}` {
		t.Errorf("a copy with a new world: %s", show(o))
	}
	if o := copyOf("copy4", `,"world":"new"`); show(o) != `{"kind":"new","time":null}` {
		t.Errorf("a copy with a new random world: %s", show(o))
	}
}

// withSnapshots gives a panel a stand-in for restic that lists the
// snapshots given and reads each one's record from record-<id>.json.
func (p *panel) withSnapshots(snaps []map[string]any, records map[string]snapshotRecord) {
	t := p.t
	t.Helper()
	repo := filepath.Join(p.srv.Backups.Dir, "alpha")
	if err := os.MkdirAll(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "config"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	list, _ := json.Marshal(snaps)
	if err := os.WriteFile(filepath.Join(repo, "snapshots.json"), list, 0o600); err != nil {
		t.Fatal(err)
	}
	for id, rec := range records {
		raw, _ := json.Marshal(rec)
		if err := os.WriteFile(filepath.Join(repo, "record-"+id+".json"), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	restic := filepath.Join(t.TempDir(), "restic")
	script := "#!/bin/sh\nfor a in \"$@\"; do case \"$a\" in\n" +
		"snapshots) cat " + filepath.Join(repo, "snapshots.json") + "; exit $? ;;\n" +
		"dump) for b in \"$@\"; do if [ -f " + repo + "/record-$b.json ]; then cat " + repo + "/record-$b.json; exit 0; fi; done; exit 1 ;;\n" +
		"esac; done\nexit 1\n"
	if err := os.WriteFile(restic, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	p.srv.Backups.Restic = restic
	p.srv.Gateway = &sshgw.Gateway{Dialer: dialHeld{dialed: make(chan struct{}, 8)}}
}

// A server made from a snapshot has the snapshot's world, from where it came
// then, by way of the snapshot; brought back under its own name, it is the
// server as it was. Made from a template, by way of the template. A new
// world is new.
func TestASnapshotsWorldOrigin(t *testing.T) {
	library := &avsysv1.WorldOrigin{Kind: avsysv1.WorldLibrary, Library: &avsysv1.WorldRef{ID: "w1", Name: "Drehmal"}, Time: metav1.NewTime(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))}
	spec := func(o *avsysv1.WorldOrigin) avsysv1.MinecraftServerSpec {
		return avsysv1.MinecraftServerSpec{EULA: true, Version: "1.21.4", Loader: avsysv1.LoaderVanilla, Expose: avsysv1.ExposeSpec{Type: avsysv1.ExposeRouter}, WorldOrigin: o}
	}
	id := func(c string) string { return strings.Repeat(c, 8) + "0123456789abcdef" + strings.Repeat(c, 40) }
	snap := func(c, host, trigger string) map[string]any {
		return map[string]any{"id": id(c), "time": time.Now().Add(-time.Hour), "hostname": host, "tags": []string{"avsys", "trigger=" + trigger}}
	}
	p := newPanel(t)
	// A template whose version is the third.
	tmpl := &store.Template{OrgID: p.orgID("alpha"), Name: "Event", CreatedBy: "owner"}
	if err := p.srv.Store.CreateTemplate(context.Background(), tmpl); err != nil {
		t.Fatal(err)
	}
	p.withSnapshots([]map[string]any{snap("a", "survival", "final"), snap("b", "plain", "manual"), snap("c", templateHost(tmpl.ID), "template")}, map[string]snapshotRecord{
		id("a"): {Format: 1, Org: "alpha", Server: "survival", Spec: spec(library)},
		id("b"): {Format: 1, Org: "alpha", Server: "plain", Spec: spec(nil)},
		id("c"): {Format: 1, Org: "alpha", Server: "base", Spec: spec(library)},
	})
	made := func(body string) *avsysv1.WorldOrigin {
		t.Helper()
		var in struct{ Name string }
		_ = json.Unmarshal([]byte(body), &in)
		if code, msg := p.do("owner", "POST", "/orgs/alpha/servers", body); code != 201 {
			t.Fatalf("%s: %d %s", body, code, msg)
		}
		return p.server(in.Name).Spec.WorldOrigin
	}
	// Brought back: as it was.
	if o := made(`{"name":"survival","fromSnapshot":"` + id("a")[:8] + `"}`); show(o) != show(library) || !o.Time.Equal(&library.Time) {
		t.Errorf("brought back: %s at %v", show(o), o.Time)
	}
	if o := made(`{"name":"other","fromSnapshot":"` + id("a")[:8] + `"}`); show(o) != `{"kind":"library","library":{"id":"w1","name":"Drehmal"},"server":"survival","snapshot":"`+id("a")+`","time":null}` {
		t.Errorf("from another's snapshot: %s", show(o))
	}
	if o := made(`{"name":"other2","fromSnapshot":"` + id("b")[:8] + `"}`); show(o) != `{"kind":"snapshot","server":"plain","snapshot":"`+id("b")+`","time":null}` {
		t.Errorf("from a snapshot with nothing known: %s", show(o))
	}
	if o := made(`{"name":"other3","fromSnapshot":"` + id("a")[:8] + `","world":"new","spec":{"properties":{"level-seed":"5"}}}`); show(o) != `{"kind":"new","seed":"5","time":null}` {
		t.Errorf("from a snapshot with a new world: %s", show(o))
	}
	if o := made(`{"name":"event","fromTemplate":"` + tmpl.ID + `"}`); show(o) != `{"kind":"library","library":{"id":"w1","name":"Drehmal"},"template":{"id":"`+tmpl.ID+`","name":"Event"},"time":null}` {
		t.Errorf("from a template: %s", show(o))
	}
}

// orgID is an organization's ID in the panel's store.
func (p *panel) orgID(slug string) string {
	p.t.Helper()
	org, err := p.srv.Store.OrgBySlug(context.Background(), slug)
	if err != nil {
		p.t.Fatal(err)
	}
	return org.ID
}

// A restore puts back where the world came from as it was then, or that
// nothing was known of it.
func TestARestorePutsBackTheWorldsOrigin(t *testing.T) {
	now := avsysv1.MinecraftServerSpec{WorldOrigin: &avsysv1.WorldOrigin{Kind: avsysv1.WorldUpload, File: "new.zip"}}
	then := avsysv1.MinecraftServerSpec{WorldOrigin: &avsysv1.WorldOrigin{Kind: avsysv1.WorldLibrary, Library: &avsysv1.WorldRef{ID: "w1"}}}
	restoreSoftware(&now, &then)
	if show(now.WorldOrigin) != show(then.WorldOrigin) || now.WorldOrigin == then.WorldOrigin {
		t.Errorf("restored: %s", show(now.WorldOrigin))
	}
	restoreSoftware(&now, &avsysv1.MinecraftServerSpec{})
	if now.WorldOrigin != nil {
		t.Errorf("a snapshot from before origins were kept left %s", show(now.WorldOrigin))
	}
	// And a snapshot keeps it, as part of the server's settings.
	var rec snapshotRecord
	raw, _ := json.Marshal(snapshotRecord{Spec: then})
	if err := json.Unmarshal(raw, &rec); err != nil || show(rec.Spec.WorldOrigin) != show(then.WorldOrigin) {
		t.Errorf("recorded: %s, %v", raw, err)
	}
}
