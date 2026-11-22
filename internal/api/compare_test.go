package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
	"github.com/GT-Drehmal/avSYS/internal/controller"
)

// comparePlan is one content set for hack/compare-dependencies.sh: what the
// panel does with it, against the real Modrinth, step by step.
type comparePlan struct {
	Name    string `json:"name"`
	Loader  string `json:"loader"`
	Version string `json:"version"`
	// Mods are its own: "project" or "project:version".
	Mods []string `json:"mods"`
	// Steps are done to it in turn: {"do": "add"|"update"|"remove"|
	// "removeFile"|"addDown", "project", "version", "path"}.
	Steps []struct {
		Do      string `json:"do"`
		Project string `json:"project"`
		Version string `json:"version"`
		Path    string `json:"path"`
	} `json:"steps"`
}

// compareState is what a server's image is given after a step, and what
// the panel says of it.
type compareState struct {
	Set  string `json:"set"`
	Step int    `json:"step"`
	Do   string `json:"do"`
	// Code and Message are the answer to the step's request.
	Code    int    `json:"code"`
	Message string `json:"message,omitempty"`
	// Own are the server's own Modrinth entries, as the image is given
	// them, for the image to work out what they need itself.
	Own []string `json:"own"`
	// Projects and Dependencies are what the image is given by the panel.
	Projects     []string `json:"projects"`
	Dependencies string   `json:"dependencies"`
	Unmet        []string `json:"unmet"`
	Entries      []string `json:"entries"`
}

// failing is Modrinth not answering.
type failing struct{}

func (failing) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("modrinth is down")
}

// TestCompareWithImage is run by hack/compare-dependencies.sh, against the
// real Modrinth: AVSYS_COMPARE_PLAN is a plan's JSON, AVSYS_COMPARE_DIR the
// server's folder, AVSYS_COMPARE_OUT where its states are written, a line
// each.
func TestCompareWithImage(t *testing.T) {
	raw := os.Getenv("AVSYS_COMPARE_PLAN")
	if raw == "" {
		t.Skip("run by hack/compare-dependencies.sh")
	}
	var plan comparePlan
	if err := json.Unmarshal([]byte(raw), &plan); err != nil {
		t.Fatal(err)
	}
	dir := os.Getenv("AVSYS_COMPARE_DIR")
	out, err := os.Create(os.Getenv("AVSYS_COMPARE_OUT"))
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	p := newPanel(t)
	p.srv.catalog = newCatalog("avsys-compare")
	p.srv.ContentFiles = func(context.Context, string, string) (ServerFiles, error) { return DirFiles(dir), nil }
	name := "compare"
	key := client.ObjectKey{Namespace: p.srv.Namespace("alpha"), Name: name}
	entry := func(ref string) avsysv1.ContentRef {
		project, version, _ := strings.Cut(ref, ":")
		kind := avsysv1.KindMod
		if plan.Loader == "paper" || plan.Loader == "purpur" || plan.Loader == "folia" {
			kind = avsysv1.KindPlugin
		}
		return avsysv1.ContentRef{Source: avsysv1.SourceModrinth, Kind: kind, Project: project, Version: version}
	}
	look := func() {
		st := p.srv.foundStateOf(key)
		st.mu.Lock()
		st.last = time.Time{}
		st.mu.Unlock()
		p.call("viewer", "POST", "/orgs/alpha/servers/"+name+"/content/look?wait=true", "")
	}
	emit := func(step int, do string, code int, msg string) {
		mc := p.serverOf(name)
		s := compareState{Set: plan.Name, Step: step, Do: do, Code: code, Message: msg, Own: []string{}, Unmet: []string{}}
		for _, m := range mc.Spec.Mods {
			if m.Source == avsysv1.SourceModrinth && !m.Dependency {
				s.Own = append(s.Own, imageRef(m))
			}
			line := string(m.Source) + " " + m.Project + "@" + m.Version + " " + m.VersionName
			if m.Dependency {
				line = "+" + line
			}
			if len(m.NeededBy) > 0 {
				line += " <- " + strings.Join(m.NeededBy, ",")
			}
			s.Entries = append(s.Entries, line)
			for _, u := range m.Unmet {
				raw, _ := json.Marshal(u)
				s.Unmet = append(s.Unmet, m.Project+": "+string(raw))
			}
		}
		env := envOf(mc)
		s.Projects = strings.Split(env["MODRINTH_PROJECTS"], "\n")
		s.Dependencies = env[controller.DependencyEnv]
		raw, _ := json.Marshal(s)
		_, _ = out.Write(append(raw, '\n'))
	}
	patch := func(mods []avsysv1.ContentRef) (int, string) {
		raw, _ := json.Marshal(map[string]any{"spec": map[string]any{"mods": mods}})
		code, body := p.do("owner", "PATCH", "/orgs/alpha/servers/"+name, string(raw))
		return code, body
	}

	var mods []avsysv1.ContentRef
	for _, ref := range plan.Mods {
		mods = append(mods, entry(ref))
	}
	body, _ := json.Marshal(map[string]any{"name": name, "spec": map[string]any{"eula": true, "loader": plan.Loader, "version": plan.Version, "state": "Stopped", "mods": mods}})
	code, msg := p.do("owner", "POST", "/orgs/alpha/servers", string(body))
	if code != http.StatusCreated {
		t.Fatalf("create: %d %s", code, msg)
	}
	// Its files looked at, as the panel does a moment after: twice, as a
	// file can be taken in only once what it stands in for has gone.
	look()
	look()
	emit(0, "create", code, msg)
	for i, step := range plan.Steps {
		current := p.serverOf(name).Spec.Mods
		var code int
		var msg string
		switch step.Do {
		case "add", "addDown":
			if step.Do == "addDown" {
				p.srv.catalog = newCatalog("avsys-compare")
				p.srv.catalog.http.Transport = failing{}
				p.srv.Downloads = failing{}
			}
			code, msg = patch(append(current, entry(step.Project)))
			if step.Do == "addDown" {
				if got := p.serverOf(name).Spec.Mods; len(got) != len(current) {
					t.Errorf("%s: changed while Modrinth was down", plan.Name)
				}
				p.srv.catalog = newCatalog("avsys-compare")
				p.srv.Downloads = nil
			}
		case "update":
			for j := range current {
				if current[j].Project == step.Project {
					current[j].Version, current[j].VersionName, current[j].Java = step.Version, "", 0
				}
			}
			code, msg = patch(current)
		case "remove":
			var kept []avsysv1.ContentRef
			for _, m := range current {
				if m.Project != step.Project {
					kept = append(kept, m)
				}
			}
			code, msg = patch(kept)
		case "removeFile":
			if err := os.Remove(filepath.Join(dir, step.Path)); err != nil {
				t.Fatal(err)
			}
			look()
			code = http.StatusOK
		default:
			t.Fatalf("no step %q", step.Do)
		}
		emit(i+1, step.Do+" "+step.Project+step.Path, code, msg)
	}
}
