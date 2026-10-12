package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/client"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
)

// offline answers the panel's lookups of other sites from what is written
// here, and fails any other, as a site that is down would.
type offline map[string]string

func (o offline) RoundTrip(r *http.Request) (*http.Response, error) {
	body, ok := o[r.URL.String()]
	if !ok {
		return nil, errors.New("offline: " + r.URL.Host)
	}
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
}

// The game as of 26.3, which Folia and NeoForge's first 26.1 builds lag
// behind in the ways the real ones have.
var upstream = offline{
	mojangManifest: `{"latest":{"release":"26.3","snapshot":"26.4-snapshot-1"},"versions":[
		{"id":"26.4-snapshot-1","type":"snapshot"},
		{"id":"26.3","type":"release"},{"id":"26.3-rc-1","type":"snapshot"},
		{"id":"26.2","type":"release"},{"id":"26.1.1","type":"release"},
		{"id":"1.21.4","type":"release"},{"id":"1.21","type":"release"}]}`,
	builds[avsysv1.LoaderFolia].url:    `{"versions":{"26.2":["26.2"],"1.21":["1.21.4"]}}`,
	builds[avsysv1.LoaderPaper].url:    `{"versions":{"26.3":["26.3","26.3-rc-1"],"26.2":["26.2"],"1.21":["1.21.4","1.21"]}}`,
	builds[avsysv1.LoaderPurpur].url:   `{"versions":["1.21.4","26.2","26.3"]}`,
	builds[avsysv1.LoaderFabric].url:   `[{"version":"26.4-snapshot-1","stable":false},{"version":"26.3","stable":true},{"version":"1.21.4","stable":true}]`,
	builds[avsysv1.LoaderForge].url:    `{"promos":{"1.21.4-latest":"54.1.0","1.21.4-recommended":"54.1.0","26.3-latest":"66.0.10"}}`,
	builds[avsysv1.LoaderNeoForge].url: `{"versions":["0.25w14craftmine.3-beta","21.0.167","21.4.38","26.1.1.14-beta","26.2.0.5-beta","26.2.0.34","26.3.0.76"]}`,
}

func TestNeoForgeGame(t *testing.T) {
	for build, want := range map[string]string{
		"21.4.38":                 "1.21.4",
		"21.0.167":                "1.21",
		"20.2.88":                 "1.20.2",
		"21.11.3":                 "1.21.11",
		"26.1.2.46":               "26.1.2",
		"26.3.0.76":               "26.3",
		"26.3.0.75-beta":          "",
		"21.4.0-beta":             "",
		"0.25w14craftmine.3-beta": "",
		"26.3":                    "",
		"nonsense":                "",
	} {
		if got := neoforgeGame(build); got != want {
			t.Errorf("neoforgeGame(%q) = %q, want %q", build, got, want)
		}
	}
}

func TestSupportedReleases(t *testing.T) {
	p := newPanel(t)
	p.srv.catalog.http.Transport = upstream
	for loader, want := range map[avsysv1.Loader]string{
		avsysv1.LoaderVanilla:  "26.3 26.2 26.1.1 1.21.4 1.21",
		avsysv1.LoaderPaper:    "26.3 26.2 1.21.4 1.21",
		avsysv1.LoaderFolia:    "26.2 1.21.4",
		avsysv1.LoaderPurpur:   "26.3 26.2 1.21.4",
		avsysv1.LoaderFabric:   "26.3 1.21.4",
		avsysv1.LoaderForge:    "26.3 1.21.4",
		avsysv1.LoaderNeoForge: "26.3 26.2 1.21.4 1.21",
	} {
		sup, err := p.srv.supportedBy(context.Background(), loader)
		if err != nil {
			t.Fatalf("%s: %v", loader, err)
		}
		var got []string
		for _, v := range sup.releases {
			got = append(got, v.ID)
		}
		if strings.Join(got, " ") != want {
			t.Errorf("%s runs %q, want %q", loader, strings.Join(got, " "), want)
		}
	}
}

// fetch reads a route as the owner and decodes what it answers.
func (p *panel) fetch(path string, into any) {
	p.t.Helper()
	req := httptest.NewRequest("GET", "/api/v1"+path, nil)
	req.Header.Set("Authorization", "Bearer "+p.tokens["owner"])
	rec := httptest.NewRecorder()
	p.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		p.t.Fatalf("GET %s: %d %s", path, rec.Code, rec.Body)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), into); err != nil {
		p.t.Fatal(err)
	}
}

func (p *panel) versionOf(name string) string {
	p.t.Helper()
	mc := &avsysv1.MinecraftServer{}
	if err := p.srv.Client.Get(context.Background(), client.ObjectKey{Namespace: p.srv.Namespace("alpha"), Name: name}, mc); err != nil {
		p.t.Fatal(err)
	}
	return mc.Spec.Version
}

func TestVersionListFollowsTheLoader(t *testing.T) {
	p := newPanel(t)
	p.srv.catalog.http.Transport = upstream
	var list manifest
	p.fetch("/catalog/minecraft/versions?loader=folia", &list)
	if list.Latest["release"] != "26.2" || len(list.Versions) != 2 {
		t.Errorf("Folia's list is %+v, want 26.2 and 1.21.4 with 26.2 the latest", list)
	}
	list = manifest{}
	p.fetch("/catalog/minecraft/versions", &list)
	if list.Latest["release"] != "26.3" || len(list.Versions) != 7 {
		t.Errorf("the list for no loader is %+v, want Mojang's own", list)
	}
	if got, code := p.do("owner", "GET", "/catalog/minecraft/versions?loader=spigot", ""); got != 400 || code != "invalid" {
		t.Errorf("an unknown loader: %d %s, want 400 invalid", got, code)
	}

	// Folia's own list down: every version is offered rather than none.
	down := offline{mojangManifest: upstream[mojangManifest]}
	p = newPanel(t)
	p.srv.catalog.http.Transport = down
	list = manifest{}
	p.fetch("/catalog/minecraft/versions?loader=folia", &list)
	if list.Latest["release"] != "26.3" || len(list.Versions) != 7 {
		t.Errorf("with Folia's list unreachable the list is %+v, want Mojang's own", list)
	}
}

func TestCreateChecksTheVersion(t *testing.T) {
	p := newPanel(t)
	p.srv.catalog.http.Transport = upstream
	create := func(name, spec string) (int, string) {
		return p.do("owner", "POST", "/orgs/alpha/servers", `{"name":"`+name+`","spec":{"eula":true,`+spec+`}}`)
	}

	// "Latest" is the newest the software has, not the newest there is.
	if got, _ := create("folia", `"loader":"folia"`); got != 201 {
		t.Fatalf("creating a Folia server: %d", got)
	}
	if v := p.versionOf("folia"); v != "26.2" {
		t.Errorf("a new Folia server got %s, want 26.2", v)
	}
	if got, _ := create("paper", `"loader":"paper"`); got != 201 || p.versionOf("paper") != "26.3" {
		t.Errorf("a new Paper server: %d, want 201 with 26.3", got)
	}

	for name, c := range map[string]struct {
		spec string
		want int
	}{
		"folia-ahead":    {`"loader":"folia","version":"26.3"`, 400},
		"folia-ok":       {`"loader":"folia","version":"1.21.4"`, 201},
		"neoforge-beta":  {`"loader":"neoforge","version":"26.1.1"`, 400},
		"neoforge-build": {`"loader":"neoforge","version":"26.1.1","loaderVersion":"26.1.1.14-beta"`, 201},
		"neoforge-ok":    {`"loader":"neoforge","version":"1.21"`, 201},
		"forge-latest":   {`"loader":"forge","version":"26.3"`, 201},
		"forge-none":     {`"loader":"forge","version":"26.2"`, 400},
		"snapshot":       {`"version":"26.4-snapshot-1"`, 201},
		"fabric-snap":    {`"loader":"fabric","version":"26.4-snapshot-1"`, 201},
		"made-up":        {`"version":"99.9"`, 400},
		"paper-rc":       {`"loader":"paper","version":"26.3-rc-1"`, 201},
	} {
		if got, code := create(name, c.spec); got != c.want || (got == 400 && code != "invalid") {
			t.Errorf("%s (%s): %d %s, want %d", name, c.spec, got, code, c.want)
		}
	}
}

// With a site down nothing can be checked, and nobody is stopped from
// making a server over it.
func TestCreateWithoutTheLists(t *testing.T) {
	p := newPanel(t)
	if got, _ := p.do("owner", "POST", "/orgs/alpha/servers", `{"name":"folia","spec":{"eula":true,"loader":"folia","version":"26.3"}}`); got != 201 {
		t.Errorf("with every list unreachable: %d, want 201", got)
	}
	if got, _ := p.do("owner", "POST", "/orgs/alpha/servers", `{"name":"latest","spec":{"eula":true,"loader":"folia"}}`); got != 201 || p.versionOf("latest") != "LATEST" {
		t.Errorf("latest with every list unreachable: %d %s, want 201 and left as LATEST", got, p.versionOf("latest"))
	}
}

func TestUpdateChecksAChangeOfSoftware(t *testing.T) {
	p := newPanel(t)
	// Made while nothing could be checked, as a server from before this
	// check, or one whose version has since been withdrawn, would be.
	if got, _ := p.do("owner", "POST", "/orgs/alpha/servers", `{"name":"folia","spec":{"eula":true,"loader":"folia","version":"26.3"}}`); got != 201 {
		t.Fatalf("creating: %d", got)
	}
	p.srv.catalog.http.Transport = upstream
	patch := func(spec string) (int, string) {
		return p.do("owner", "PATCH", "/orgs/alpha/servers/folia", `{"spec":{`+spec+`}}`)
	}
	if got, code := patch(`"displayName":"Folia"`); got != 200 {
		t.Errorf("renaming a server whose version is not supported: %d %s, want 200", got, code)
	}
	if got, code := patch(`"version":"26.1.1"`); got != 400 || code != "invalid" {
		t.Errorf("moving to another unsupported version: %d %s, want 400 invalid", got, code)
	}
	if got, code := patch(`"loader":"forge"`); got != 200 {
		t.Errorf("moving to software that has the version: %d %s, want 200", got, code)
	}
	if got, code := patch(`"loader":"folia"`); got != 400 || code != "invalid" {
		t.Errorf("moving back: %d %s, want 400 invalid", got, code)
	}
	if got, code := patch(`"loader":"folia","version":"26.2"`); got != 200 {
		t.Errorf("moving to a supported pair: %d %s, want 200", got, code)
	}
}
