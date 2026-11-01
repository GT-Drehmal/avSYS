package api

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GT-Drehmal/avSYS/internal/backup"
	"github.com/GT-Drehmal/avSYS/internal/store"
)

func TestTemplateVersions(t *testing.T) {
	at := func(h int) time.Time { return time.Date(2026, 10, 1, h, 0, 0, 0, time.UTC) }
	// Newest first, as the repository lists them.
	list := []backup.Snapshot{
		{ID: "c3", Lineage: "l3", Server: "template-t1", Trigger: backup.TriggerTemplate, Time: at(3)},
		{ID: "x1", Lineage: "x1", Server: "template-t1", Trigger: backup.TriggerManual, Time: at(3)},
		{ID: "o1", Lineage: "o1", Server: "template-t2", Trigger: backup.TriggerTemplate, Time: at(2)},
		{ID: "b2", Lineage: "l2", Server: "template-t1", Trigger: backup.TriggerTemplate, Time: at(2)},
		{ID: "s1", Lineage: "s1", Server: "lobby", Trigger: backup.TriggerManual, Time: at(1)},
		{ID: "a1", Lineage: "l1", Server: "template-t1", Trigger: backup.TriggerTemplate, Time: at(1)},
	}
	tpl := &store.Template{ID: "t1", Name: "Lobby"}
	v := templateOf(tpl, list)
	var ids []string
	for _, s := range v.Versions {
		ids = append(ids, s.ID)
	}
	// Its own, and only those taken as its versions.
	if len(ids) != 3 || ids[0] != "c3" || ids[1] != "b2" || ids[2] != "a1" {
		t.Fatalf("versions: %v", ids)
	}
	if v.Current == nil || v.Current.ID != "c3" {
		t.Errorf("with none chosen the newest is used: %+v", v.Current)
	}
	// One chosen by its lineage, which outlasts its ID.
	tpl.Current = "l2"
	if v := templateOf(tpl, list); v.Current == nil || v.Current.ID != "b2" {
		t.Errorf("chosen: %+v", v.Current)
	}
	// One chosen that is gone: the newest.
	tpl.Current = "gone"
	if v := templateOf(tpl, list); v.Current == nil || v.Current.ID != "c3" {
		t.Errorf("chosen and gone: %+v", v.Current)
	}
	if v := templateOf(&store.Template{ID: "t9"}, list); v.Current != nil || len(v.Versions) != 0 {
		t.Errorf("one with no versions yet: %+v", v)
	}
}

func TestTemplateWords(t *testing.T) {
	for _, tc := range []struct {
		name, description string
		ok                bool
	}{
		{"  Lobby ", "Where everyone starts.\nWith a shop.", true},
		{"", "", false},
		{"   ", "", false},
		{"Two\nlines", "", false},
		{"Bell\a", "", false},
		{string(make([]rune, 61)), "", false},
		{"Long", string(make([]byte, 501)), false},
	} {
		name, _, err := templateWords(tc.name, tc.description)
		if (err == nil) != tc.ok {
			t.Errorf("%q: %v", tc.name, err)
		}
		if tc.ok && name != "Lobby" {
			t.Errorf("not tidied: %q", name)
		}
	}
}

func TestOneSaveOrDeletionOfATemplateAtATime(t *testing.T) {
	a := &access{Org: &store.Org{ID: "o1"}}
	release, err := claimTemplate(a, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := claimTemplate(a, "t1"); err != errTemplateBusy {
		t.Errorf("claimed twice: %v", err)
	}
	// Another template, or another organization's, is not held up.
	if r, err := claimTemplate(a, "t2"); err != nil {
		t.Errorf("another template: %v", err)
	} else {
		r()
	}
	if r, err := claimTemplate(&access{Org: &store.Org{ID: "o2"}}, "t1"); err != nil {
		t.Errorf("another organization's: %v", err)
	} else {
		r()
	}
	release()
	if r, err := claimTemplate(a, "t1"); err != nil {
		t.Errorf("once let go: %v", err)
	} else {
		r()
	}
}

// TestTemplateOfSettingsAlone makes, shows, changes and deletes a template
// with no server behind it, and checks that what is a server's own is not
// kept in it.
func TestTemplateOfSettingsAlone(t *testing.T) {
	p := newPanel(t)
	call := func(who, method, path, body string) (int, map[string]any) {
		t.Helper()
		req := httptest.NewRequest(method, "/api/v1"+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+p.tokens[who])
		rec := httptest.NewRecorder()
		p.h.ServeHTTP(rec, req)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	const settings = `{"loader":"fabric","version":"LATEST","resources":{"memory":"4Gi"},"storage":{"size":"10Gi"},` +
		`"eula":true,"state":"Running","displayName":"Not this","image":"someone/else","nodeSelector":{"disk":"fast"},` +
		`"expose":{"type":"Router","hostname":"play.example.com"},"jvm":{"extraFlags":"-XX:+UseZGC"},` +
		`"mods":[{"source":"modrinth","kind":"mod","project":"lithium"}],"properties":{"difficulty":"hard"},` +
		`"ports":[{"name":"voice-chat","port":24454,"protocol":"UDP"}]}`

	code, made := call("admin", "POST", "/orgs/alpha/templates", `{"name":"Survival base","description":"Hard, with Lithium.","spec":`+settings+`}`)
	if code != http.StatusCreated || made["settingsOnly"] != true {
		t.Fatalf("made: %d %v", code, made)
	}
	id := made["id"].(string)
	spec := made["spec"].(map[string]any)
	for _, own := range []string{"state", "displayName", "image", "nodeSelector"} {
		if _, kept := spec[own]; kept {
			t.Errorf("%s kept: %v", own, spec[own])
		}
	}
	// Agreed to for each server made from it.
	if spec["eula"] != false {
		t.Errorf("the EULA kept: %v", spec["eula"])
	}
	if spec["expose"].(map[string]any)["hostname"] != nil {
		t.Errorf("its address kept: %v", spec["expose"])
	}
	if spec["version"] != "LATEST" || len(spec["mods"].([]any)) != 1 || spec["properties"].(map[string]any)["difficulty"] != "hard" {
		t.Errorf("not kept as given: %v", spec)
	}
	// What its mods listen on is kept: each server made from it is given
	// public ports of its own.
	if ports := spec["ports"]; fmt.Sprint(ports) != "[map[name:voice-chat port:24454 protocol:UDP]]" {
		t.Errorf("its ports: %v", ports)
	}

	// Seen by everyone in the organization, the launch flags by admins only.
	code, shown := call("viewer", "GET", "/orgs/alpha/templates/"+id, "")
	if code != http.StatusOK || shown["settingsOnly"] != true {
		t.Fatalf("shown: %d %v", code, shown)
	}
	if jvm, _ := shown["spec"].(map[string]any)["jvm"].(map[string]any); jvm["extraFlags"] != nil {
		t.Errorf("a viewer is shown its launch flags: %v", jvm)
	}
	if code, shown := call("admin", "GET", "/orgs/alpha/templates/"+id, ""); code != http.StatusOK || shown["spec"].(map[string]any)["jvm"].(map[string]any)["extraFlags"] != "-XX:+UseZGC" {
		t.Errorf("an admin is not shown its launch flags: %d %v", code, shown)
	}

	for _, bad := range []string{
		`{"name":"Uploads","spec":{"loader":"paper","resources":{"memory":"4Gi"},"storage":{"size":"10Gi"},"mods":[{"source":"upload","kind":"plugin","project":"own.jar"}]}}`,
		`{"name":"No type","spec":{"resources":{"memory":"4Gi"},"storage":{"size":"10Gi"}}}`,
		`{"name":"Tiny","spec":{"loader":"paper","resources":{"memory":"64Mi"},"storage":{"size":"10Gi"}}}`,
		`{"name":"Both","server":"world","spec":{"loader":"paper","resources":{"memory":"4Gi"},"storage":{"size":"10Gi"}}}`,
	} {
		if code, out := call("admin", "POST", "/orgs/alpha/templates", bad); code != http.StatusBadRequest {
			t.Errorf("%s: %d %v", bad, code, out)
		}
	}
	if code, _ := call("admin", "POST", "/orgs/alpha/templates", `{"name":"survival BASE","spec":`+settings+`}`); code != http.StatusConflict {
		t.Errorf("a name taken: %d", code)
	}

	code, changed := call("admin", "PATCH", "/orgs/alpha/templates/"+id, `{"spec":{"loader":"paper","version":"1.21.4","resources":{"memory":"6Gi"},"storage":{"size":"10Gi"}}}`)
	if code != http.StatusOK || changed["spec"].(map[string]any)["loader"] != "paper" || changed["name"] != "Survival base" {
		t.Errorf("changed: %d %v", code, changed)
	}

	// There are no files to copy: a server is made with its settings.
	if code, out := call("admin", "POST", "/orgs/alpha/servers", `{"name":"made","fromTemplate":"`+id+`"}`); code != http.StatusConflict || out["code"] != "settings_only" {
		t.Errorf("made from it: %d %v", code, out)
	}

	// One saved from a server is changed by saving a server into it.
	alpha, err := p.srv.Store.OrgBySlug(t.Context(), "alpha")
	if err != nil {
		t.Fatal(err)
	}
	saved := &store.Template{OrgID: alpha.ID, Name: "Lobby"}
	if err := p.srv.Store.CreateTemplate(t.Context(), saved); err != nil {
		t.Fatal(err)
	}
	if code, _ := call("admin", "PATCH", "/orgs/alpha/templates/"+saved.ID, `{"spec":{"loader":"paper","resources":{"memory":"4Gi"},"storage":{"size":"10Gi"}}}`); code != http.StatusBadRequest {
		t.Errorf("settings given to one saved from a server: %d", code)
	}

	if code, _ := call("admin", "DELETE", "/orgs/alpha/templates/"+id, ""); code != http.StatusNoContent {
		t.Errorf("deleted: %d", code)
	}
	if code, _ := call("viewer", "GET", "/orgs/alpha/templates/"+id, ""); code != http.StatusNotFound {
		t.Errorf("still there: %d", code)
	}
}

func TestATemplateSavedIntoIsMadeFromItsVersions(t *testing.T) {
	tpl := &store.Template{ID: "t1", Settings: `{"loader":"paper"}`}
	if v := templateOf(tpl, nil); !v.SettingsOnly || v.Spec == nil || v.Spec.Loader != "paper" {
		t.Errorf("settings alone: %+v", v)
	}
	list := []backup.Snapshot{{ID: "a1", Lineage: "l1", Server: "template-t1", Trigger: backup.TriggerTemplate}}
	if v := templateOf(tpl, list); v.SettingsOnly || v.Spec != nil || v.Current == nil {
		t.Errorf("with a version: %+v", v)
	}
}

// A template is made from a snapshot, or given one as its newest version, as
// a copy of it under the template's own name: the snapshot stays as it was.
// One of a template's versions is a snapshot too, which is how a template is
// duplicated.
func TestTemplateFromASnapshot(t *testing.T) {
	if _, err := exec.LookPath("restic"); err != nil {
		t.Skip("restic is not installed")
	}
	p := newPanel(t)
	call := func(who, method, path, body string) (int, map[string]any) {
		t.Helper()
		req := httptest.NewRequest(method, "/api/v1"+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+p.tokens[who])
		rec := httptest.NewRecorder()
		p.h.ServeHTTP(rec, req)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	dir := t.TempDir()
	p.srv.Backups = &backup.Manager{Dir: dir, Restic: "restic", Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if err := os.WriteFile(filepath.Join(dir, "alpha.key"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	restic := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("restic", append([]string{"--repo", filepath.Join(dir, "alpha"), "--password-file", filepath.Join(dir, "alpha.key")}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("restic %v: %v: %s", args, err, out)
		}
	}
	// A server's files as a snapshot has them, under /data with the record
	// of the server in them.
	root := t.TempDir()
	record := `{"format":1,"org":"alpha","server":"survival","spec":{"eula":true,"loader":"paper","version":"1.21.4",` +
		`"resources":{"memory":"4Gi"},"storage":{"size":"10Gi"},"properties":{"difficulty":"hard","motd":"hi"},` +
		`"expose":{"type":"Router","hostname":"survival.alpha.test"}}}`
	for path, content := range map[string]string{"data/level.dat": "a world", "data/.avsys/snapshot.json": record} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, path)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, path), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	restic("init")
	backup := exec.Command("restic", "--repo", filepath.Join(dir, "alpha"), "--password-file", filepath.Join(dir, "alpha.key"),
		"backup", "data", "--host", "survival", "--tag", "avsys", "--tag", "trigger=scheduled", "--tag", "version=1.21.4", "--tag", "loader=paper")
	backup.Dir = root
	if out, err := backup.CombinedOutput(); err != nil {
		t.Fatalf("backup: %v: %s", err, out)
	}
	list, err := p.srv.Backups.List(t.Context(), "alpha")
	if err != nil || len(list) != 1 {
		t.Fatalf("snapshots: %v %v", list, err)
	}
	src := list[0].ID

	if code, _ := call("viewer", "POST", "/orgs/alpha/templates", `{"name":"Nope","snapshot":"`+src+`"}`); code != http.StatusForbidden {
		t.Errorf("a viewer made one: %d", code)
	}
	if code, _ := call("admin", "POST", "/orgs/alpha/templates", `{"name":"Both","server":"world","snapshot":"`+src+`"}`); code != http.StatusBadRequest {
		t.Errorf("from a server and a snapshot: %d", code)
	}
	if code, _ := call("admin", "POST", "/orgs/alpha/templates", `{"name":"Missing","snapshot":"`+strings.Repeat("0", 64)+`"}`); code != http.StatusNotFound {
		t.Errorf("from a snapshot that is not there: %d", code)
	}

	code, made := call("admin", "POST", "/orgs/alpha/templates", `{"name":"Survival","snapshot":"`+src+`"}`)
	if code != http.StatusCreated {
		t.Fatalf("made: %d %v", code, made)
	}
	current := made["current"].(map[string]any)
	if current["server"] != "template-"+made["id"].(string) || current["trigger"] != "template" || current["by"] != "admin" || current["version"] != "1.21.4" {
		t.Errorf("its version: %v", current)
	}
	id := made["id"].(string)

	// Duplicated, from the version it is made from.
	code, copied := call("admin", "POST", "/orgs/alpha/templates", `{"name":"Survival copy","snapshot":"`+current["id"].(string)+`"}`)
	if code != http.StatusCreated || copied["current"] == nil || copied["current"].(map[string]any)["server"] != "template-"+copied["id"].(string) {
		t.Fatalf("duplicated: %d %v", code, copied)
	}

	// A newer version, from the same snapshot.
	code, again := call("admin", "POST", "/orgs/alpha/templates/"+id+"/versions", `{"snapshot":"`+src+`"}`)
	if code != http.StatusCreated || len(again["versions"].([]any)) != 2 || again["current"].(map[string]any)["id"] == current["id"] {
		t.Errorf("a newer version: %d %v", code, again)
	}

	// Its version, copied as a newer one of the same template.
	if code, out := call("admin", "POST", "/orgs/alpha/templates/"+id+"/versions", `{"snapshot":"`+current["id"].(string)+`"}`); code != http.StatusCreated || len(out["versions"].([]any)) != 3 {
		t.Errorf("its own version again: %d %v", code, out)
	}

	// The snapshot is as it was, and the failed one left no template behind.
	if got, err := p.srv.Backups.Get(t.Context(), "alpha", src); err != nil || got.Server != "survival" || got.Trigger != "scheduled" {
		t.Errorf("the snapshot is now %+v, %v", got, err)
	}
	if code, all := callList(t, p, "admin", "/orgs/alpha/templates"); code != http.StatusOK || len(all) != 2 {
		t.Errorf("templates: %d %v", code, all)
	}

	// Made with changes: a server made from it has them over its version's
	// settings, but for what is each server's own.
	spec := func(id string) map[string]any {
		t.Helper()
		code, got := call("admin", "GET", "/orgs/alpha/templates/"+id, "")
		if code != http.StatusOK {
			t.Fatalf("get: %d %v", code, got)
		}
		return got["spec"].(map[string]any)
	}
	code, changed := call("admin", "POST", "/orgs/alpha/templates", `{"name":"Peaceful","snapshot":"`+src+`","changes":`+
		`{"displayName":"Not kept","resources":{"memory":"8Gi"},"properties":{"difficulty":"peaceful"},"expose":{"type":"Router","hostname":"not-kept.test"}}}`)
	if code != http.StatusCreated {
		t.Fatalf("made with changes: %d %v", code, changed)
	}
	peaceful := changed["id"].(string)
	got := spec(peaceful)
	if got["resources"].(map[string]any)["memory"] != "8Gi" || got["properties"].(map[string]any)["difficulty"] != "peaceful" || got["properties"].(map[string]any)["motd"] != "hi" {
		t.Errorf("with its changes: %v", got)
	}
	if got["displayName"] == "Not kept" || got["expose"].(map[string]any)["hostname"] == "not-kept.test" {
		t.Errorf("a server's own kept: %v", got)
	}
	// Changed again, over what it had.
	if code, out := call("admin", "PATCH", "/orgs/alpha/templates/"+peaceful, `{"changes":{"properties":{"pvp":"false"},"ports":[{"name":"map","port":8100,"protocol":"TCP"}]}}`); code != http.StatusOK {
		t.Fatalf("changed again: %d %v", code, out)
	}
	if ports := spec(peaceful)["ports"]; fmt.Sprint(ports) != "[map[name:map port:8100 protocol:TCP]]" {
		t.Errorf("its ports: %v", ports)
	}
	if props := spec(peaceful)["properties"].(map[string]any); props["difficulty"] != "peaceful" || props["pvp"] != "false" {
		t.Errorf("changed again: %v", props)
	}
	// Launch flags it is given are said apart, to admins, to be changed.
	if code, out := call("admin", "PATCH", "/orgs/alpha/templates/"+peaceful, `{"changes":{"jvm":{"extraFlags":"-Xss1m"}}}`); code != http.StatusOK {
		t.Fatalf("given flags: %d %v", code, out)
	}
	if _, got := call("admin", "GET", "/orgs/alpha/templates/"+peaceful, ""); got["extraFlags"] != "-Xss1m" {
		t.Errorf("its flags: %v", got["extraFlags"])
	}
	if _, got := call("viewer", "GET", "/orgs/alpha/templates/"+peaceful, ""); got["extraFlags"] != nil {
		t.Errorf("its flags to a viewer: %v", got["extraFlags"])
	}
	if code, out := call("admin", "PATCH", "/orgs/alpha/templates/"+peaceful, `{"changes":{"jvm":{"extraFlags":""}}}`); code != http.StatusOK {
		t.Fatalf("flags taken off: %d %v", code, out)
	}
	if _, got := call("admin", "GET", "/orgs/alpha/templates/"+peaceful, ""); got["extraFlags"] != nil {
		t.Errorf("its flags taken off: %v", got["extraFlags"])
	} else if jvm, _ := got["spec"].(map[string]any)["jvm"].(map[string]any); jvm["extraFlags"] != nil {
		t.Errorf("its flags taken off, on its servers: %v", jvm)
	}
	// Duplicated with changes of its own, over the other's.
	code, again2 := call("admin", "POST", "/orgs/alpha/templates", `{"name":"Peaceful big","template":"`+peaceful+`","changes":{"resources":{"memory":"6Gi"}}}`)
	if code != http.StatusCreated {
		t.Fatalf("duplicated with changes: %d %v", code, again2)
	}
	if got := spec(again2["id"].(string)); got["resources"].(map[string]any)["memory"] != "6Gi" || got["properties"].(map[string]any)["pvp"] != "false" {
		t.Errorf("duplicated with changes: %v", got)
	}
	// Changes that would make no server are refused.
	if code, _ := call("admin", "PATCH", "/orgs/alpha/templates/"+peaceful, `{"changes":{"resources":{"memory":"1Mi"}}}`); code != http.StatusBadRequest {
		t.Errorf("too little memory: %d", code)
	}
	if code, _ := call("admin", "POST", "/orgs/alpha/templates", `{"name":"Alone","changes":{"resources":{"memory":"8Gi"}}}`); code != http.StatusBadRequest {
		t.Errorf("changes to nothing: %d", code)
	}
	// Its servers can start a world of their own, which is said of it.
	world := func() any {
		t.Helper()
		_, got := call("admin", "GET", "/orgs/alpha/templates/"+peaceful, "")
		return got["world"]
	}
	if code, out := call("admin", "PATCH", "/orgs/alpha/templates/"+peaceful, `{"changes":{"world":"old"}}`); code != http.StatusOK || world() != nil {
		t.Errorf("a world that is not new: %d %v %v", code, out, world())
	}
	if code, out := call("admin", "PATCH", "/orgs/alpha/templates/"+peaceful, `{"changes":{"world":"new"}}`); code != http.StatusOK || world() != "new" {
		t.Errorf("a new world: %d %v %v", code, out, world())
	}
	// A new version is as it was saved, but for that.
	if code, out := call("admin", "POST", "/orgs/alpha/templates/"+peaceful+"/versions", `{"snapshot":"`+src+`"}`); code != http.StatusCreated {
		t.Fatalf("new version: %d %v", code, out)
	}
	if got := spec(peaceful); got["resources"].(map[string]any)["memory"] != "4Gi" || got["properties"].(map[string]any)["difficulty"] != "hard" {
		t.Errorf("a new version kept the changes: %v", got)
	}
	if world() != "new" {
		t.Error("a new version forgot that its servers start a new world")
	}
	if code, out := call("admin", "PATCH", "/orgs/alpha/templates/"+peaceful, `{"changes":{"world":null}}`); code != http.StatusOK || world() != nil {
		t.Errorf("its world kept again: %d %v %v", code, out, world())
	}
}

// Changes laid over others: what the newer names replaces the older's, but
// game settings and launch flags are added to them one by one.
func TestLayered(t *testing.T) {
	got, err := layered(
		json.RawMessage(`{"version":"1.21.4","properties":{"difficulty":"hard","pvp":"false"},"jvm":{"aikarFlags":true},"mods":[{"project":"a"}]}`),
		json.RawMessage(`{"properties":{"difficulty":null,"motd":"hi"},"jvm":{"extraFlags":"-Xss1m"},"mods":[]}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"jvm":{"aikarFlags":true,"extraFlags":"-Xss1m"},"mods":[],"properties":{"difficulty":null,"motd":"hi","pvp":"false"},"version":"1.21.4"}`
	if string(got) != want {
		t.Errorf("layered:\n%s\nwant\n%s", got, want)
	}
	if got, err := layered(nil, nil); err != nil || got != nil {
		t.Errorf("nothing: %s %v", got, err)
	}
	if got, err := layered(nil, json.RawMessage(`{"version":"1.21.5"}`)); err != nil || string(got) != `{"version":"1.21.5"}` {
		t.Errorf("only newer: %s %v", got, err)
	}
}

func callList(t *testing.T, p *panel, who, path string) (int, []any) {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/v1"+path, nil)
	req.Header.Set("Authorization", "Bearer "+p.tokens[who])
	rec := httptest.NewRecorder()
	p.h.ServeHTTP(rec, req)
	var out []any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

// A template is given a world of its own, kept as a snapshot beside it: one
// at a time, copied to another template, taken away, and gone with it.
func TestTemplateOwnWorld(t *testing.T) {
	if _, err := exec.LookPath("restic"); err != nil {
		t.Skip("restic is not installed")
	}
	p := newPanel(t)
	call := func(who, method, path, body string) (int, map[string]any) {
		t.Helper()
		req := httptest.NewRequest(method, "/api/v1"+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+p.tokens[who])
		rec := httptest.NewRecorder()
		p.h.ServeHTTP(rec, req)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	p.srv.Backups = &backup.Manager{Dir: t.TempDir(), Restic: "restic", Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	worlds := func(id string) int {
		t.Helper()
		list, err := p.srv.Backups.List(t.Context(), "alpha")
		if err != nil {
			t.Fatal(err)
		}
		return len(worldsOf(list, id))
	}
	const settings = `{"loader":"paper","version":"1.21.4","resources":{"memory":"4Gi"},"storage":{"size":"10Gi"}}`
	_, made := call("admin", "POST", "/orgs/alpha/templates", `{"name":"Island","spec":`+settings+`}`)
	id := made["id"].(string)

	if code, _ := call("viewer", "PUT", "/orgs/alpha/templates/"+id+"/world?root=Island", "a world"); code != http.StatusForbidden {
		t.Errorf("a viewer gave it a world: %d", code)
	}
	if code, _ := call("admin", "PUT", "/orgs/alpha/templates/"+id+"/world?root=../etc", "a world"); code != http.StatusBadRequest {
		t.Errorf("a folder outside the archive: %d", code)
	}
	code, got := call("admin", "PUT", "/orgs/alpha/templates/"+id+"/world?root=saves%2FIsland&name=Island&version=1.21.4&file=island.zip", "a world")
	if code != http.StatusOK || got["world"] != "own" {
		t.Fatalf("given a world: %d %v", code, got)
	}
	if own := got["ownWorld"].(map[string]any); own["root"] != "saves/Island" || own["name"] != "Island" || own["version"] != "1.21.4" || own["file"] != "island.zip" || own["size"] != float64(len("a world")) {
		t.Errorf("its world: %v", own)
	}
	// One at a time.
	org, err := p.srv.Store.OrgBySlug(t.Context(), "alpha")
	if err != nil {
		t.Fatal(err)
	}
	release, err := claimTemplate(&access{Org: org}, worldHost(id))
	if err != nil {
		t.Fatal(err)
	}
	if code, _ := call("admin", "PUT", "/orgs/alpha/templates/"+id+"/world?root=Other", "another world"); code != http.StatusConflict {
		t.Errorf("given one while another is: %d", code)
	}
	if code, _ := call("admin", "DELETE", "/orgs/alpha/templates/"+id+"/world", ""); code != http.StatusConflict {
		t.Errorf("taken away while one is given: %d", code)
	}
	release()
	// Given another, the one before goes.
	if code, _ := call("admin", "PUT", "/orgs/alpha/templates/"+id+"/world?root=Other", "another world"); code != http.StatusOK || worlds(id) != 1 {
		t.Errorf("given another: %d, %d kept", code, worlds(id))
	}
	// Not among a server's or the organization's snapshots.
	if code, list := call("admin", "GET", "/orgs/alpha/snapshots", ""); code != http.StatusOK || len(list["snapshots"].([]any)) != 0 {
		t.Errorf("listed with the snapshots: %d %v", code, list)
	}

	// Copied to another template.
	_, other := call("admin", "POST", "/orgs/alpha/templates", `{"name":"Island copy","spec":`+settings+`}`)
	otherID := other["id"].(string)
	if code, got := call("admin", "PUT", "/orgs/alpha/templates/"+otherID+"/world?from="+id, ""); code != http.StatusOK || got["ownWorld"].(map[string]any)["root"] != "Other" || worlds(otherID) != 1 {
		t.Errorf("copied: %d %v", code, got)
	}
	if code, out := call("admin", "PUT", "/orgs/alpha/templates/"+id+"/world?from="+otherID+"x", ""); code != http.StatusNotFound {
		t.Errorf("from a template that is not there: %d %v", code, out)
	}

	// Taken away: none, and its servers have a new one again.
	if code, got := call("admin", "DELETE", "/orgs/alpha/templates/"+id+"/world", ""); code != http.StatusOK || got["world"] != nil || got["ownWorld"] != nil || worlds(id) != 0 {
		t.Errorf("taken away: %d %v, %d kept", code, got, worlds(id))
	}
	// Gone with the template.
	if code, _ := call("admin", "DELETE", "/orgs/alpha/templates/"+otherID, ""); code != http.StatusNoContent || worlds(otherID) != 0 {
		t.Errorf("deleted: %d, %d kept", code, worlds(otherID))
	}
}
