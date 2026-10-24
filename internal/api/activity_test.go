package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// What was done to a server, newest first, by whom: the files only to those
// who can open them, and nothing of who did it but their name and face.
func TestServerActivity(t *testing.T) {
	p := newPanel(t)
	if code, _ := p.do("operator", "POST", "/orgs/alpha/servers/world/stop", ""); code != http.StatusOK {
		t.Fatalf("stop: %d", code)
	}
	org, err := p.srv.Store.OrgBySlug(t.Context(), "alpha")
	if err != nil {
		t.Fatal(err)
	}
	p.srv.audit(t.Context(), p.users["operator"], org, "files.write", "world", "/server.properties")
	// Another server's, and the organization's own, are not its.
	p.srv.audit(t.Context(), p.users["operator"], org, "server.start", "other", "")
	p.srv.audit(t.Context(), p.users["owner"], org, "org.icon", "world", "")

	seen := func(who string) []activityView {
		t.Helper()
		req := httptest.NewRequest("GET", "/api/v1/orgs/alpha/servers/world/activity", nil)
		req.Header.Set("Authorization", "Bearer "+p.tokens[who])
		rec := httptest.NewRecorder()
		p.h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", who, rec.Code, rec.Body)
		}
		if strings.Contains(rec.Body.String(), "@") {
			t.Errorf("an email address was told: %s", rec.Body)
		}
		var out []activityView
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	actions := func(list []activityView) (out []string) {
		for _, a := range list {
			out = append(out, a.Action)
		}
		return out
	}
	op := seen("operator")
	if got := strings.Join(actions(op), " "); got != "files.write server.stop" {
		t.Errorf("the operator saw %s", got)
	}
	if op[1].User == nil || op[1].User.ID != p.users["operator"].ID || op[1].User.Name == "" {
		t.Errorf("who stopped it: %+v", op[1].User)
	}
	if got := strings.Join(actions(seen("viewer")), " "); got != "server.stop" {
		t.Errorf("the viewer saw %s", got)
	}
}
