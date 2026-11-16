package api

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	avsysv1 "github.com/GT-Drehmal/avSYS/api/v1alpha1"
	"github.com/GT-Drehmal/avSYS/internal/store"
)

// withOwnLaunch gives the test panel's server "world" an image, extra
// environment and extra JVM flags of its own, as an admin may set them.
func withOwnLaunch(t *testing.T, p *panel) *avsysv1.MinecraftServer {
	t.Helper()
	ctx := context.Background()
	mc := &avsysv1.MinecraftServer{}
	if err := p.srv.Client.Get(ctx, client.ObjectKey{Namespace: p.srv.Namespace("alpha"), Name: "world"}, mc); err != nil {
		t.Fatal(err)
	}
	mc.Spec.Image = "example.com/own:1"
	mc.Spec.Env = []corev1.EnvVar{{Name: "RCON_PASSWORD", Value: "hunter2"}}
	mc.Spec.JVM.ExtraFlags = "-Dsecret=hunter2"
	if err := p.srv.Client.Update(ctx, mc); err != nil {
		t.Fatal(err)
	}
	return mc
}

// launchShown says which of the image, extra environment and extra JVM
// flags a server as the API gave it has.
func launchShown(t *testing.T, raw []byte) (image, env, flags bool) {
	t.Helper()
	var v struct {
		Spec avsysv1.MinecraftServerSpec `json:"spec"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("%v: %s", err, raw)
	}
	return v.Spec.Image != "", len(v.Spec.Env) > 0, v.Spec.JVM.ExtraFlags != ""
}

// A server's image, extra environment and extra JVM flags can hold secrets
// an admin set. Below admin they are left out wherever the server is
// shown, as they are of a snapshot's or template's settings.
func TestServerLaunchSettingsBelowAdminLeftOut(t *testing.T) {
	p := newPanel(t)
	withOwnLaunch(t, p)

	check := func(what, who string, raw []byte) {
		t.Helper()
		want := who == "root" || store.Role(who).AtLeast(store.RoleAdmin)
		image, env, flags := launchShown(t, raw)
		if image != want || env != want || flags != want {
			t.Errorf("%s as %s: image %v, env %v, extra flags %v; want %v", what, who, image, env, flags, want)
		}
		if !want && strings.Contains(string(raw), "hunter2") {
			t.Errorf("%s as %s carries the secret: %s", what, who, raw)
		}
	}
	for _, who := range []string{"viewer", "operator", "admin", "owner", "root"} {
		rec := p.send(who, "GET", "/orgs/alpha/servers", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("list as %s: %d", who, rec.Code)
		}
		var list []json.RawMessage
		if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list) != 1 {
			t.Fatalf("list as %s: %v %s", who, err, rec.Body)
		}
		check("the list", who, list[0])

		rec = p.send(who, "GET", "/orgs/alpha/servers/world", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("get as %s: %d", who, rec.Code)
		}
		check("the server", who, rec.Body.Bytes())
	}
	// What a start, stop or restart answers, which an operator may ask.
	for _, who := range []string{"operator", "admin"} {
		rec := p.send(who, "POST", "/orgs/alpha/servers/world/stop", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("stop as %s: %d %s", who, rec.Code, rec.Body)
		}
		check("a stop", who, rec.Body.Bytes())
	}
}

// The live events carry a server as the one watching may see it.
func TestServerEventsBelowAdminLeaveLaunchSettingsOut(t *testing.T) {
	p := newPanel(t)
	mc := withOwnLaunch(t, p)
	ts := httptest.NewServer(p.h)
	defer ts.Close()

	subscribers := func() int {
		p.srv.Events.mu.Lock()
		defer p.srv.Events.mu.Unlock()
		return len(p.srv.Events.subs)
	}
	for _, who := range []string{"viewer", "operator", "admin", "root"} {
		t.Run(who, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			// The one before has stopped listening.
			for subscribers() != 0 {
				if ctx.Err() != nil {
					t.Fatal("the stream before never ended")
				}
				time.Sleep(5 * time.Millisecond)
			}
			req, _ := http.NewRequestWithContext(ctx, "GET", ts.URL+"/api/v1/events", nil)
			req.Header.Set("Authorization", "Bearer "+p.tokens[who])
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer res.Body.Close()
			if res.StatusCode != http.StatusOK {
				t.Fatalf("event stream: %d", res.StatusCode)
			}
			for subscribers() != 1 {
				if ctx.Err() != nil {
					t.Fatal("the stream never subscribed")
				}
				time.Sleep(5 * time.Millisecond)
			}
			// As WatchServers publishes a change it sees.
			p.srv.Events.Publish(Event{Type: "server.updated", Namespace: mc.Namespace, Data: p.srv.view(store.RoleOwner, mc)})

			lines := bufio.NewScanner(res.Body)
			lines.Buffer(nil, 1<<20)
			event := ""
			for lines.Scan() {
				if e, ok := strings.CutPrefix(lines.Text(), "event: "); ok {
					event = e
					continue
				}
				data, ok := strings.CutPrefix(lines.Text(), "data: ")
				if !ok || event != "server.updated" {
					continue
				}
				want := who == "root" || store.Role(who).AtLeast(store.RoleAdmin)
				image, env, flags := launchShown(t, []byte(data))
				if image != want || env != want || flags != want {
					t.Errorf("image %v, env %v, extra flags %v; want %v", image, env, flags, want)
				}
				if !want && strings.Contains(data, "hunter2") {
					t.Errorf("the event carries the secret: %s", data)
				}
				return
			}
			t.Fatalf("no event arrived: %v", lines.Err())
		})
	}
}
