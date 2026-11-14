package release

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestNewer(t *testing.T) {
	for _, c := range []struct {
		current, latest string
		want            bool
	}{
		{"v0.1.0", "v0.2.0", true},
		{"v0.1.0", "v0.1.1", true},
		{"v0.9.0", "v0.10.0", true},
		{"v0.2.0", "v0.2.0", false},
		{"v0.3.0", "v0.2.0", false},
		{"v1.0.0", "v0.9.9", false},
		// A release candidate is older than its release.
		{"v0.2.0-rc.1", "v0.2.0", true},
		{"v0.2.0-rc.1", "v0.1.0", false},
		// Builds of a commit are not releases: nothing to compare with.
		{"sha-1234567", "v0.2.0", false},
		{"edge", "v0.2.0", false},
		{"dev", "v0.2.0", false},
		{"e2e", "v0.2.0", false},
		{"", "v0.2.0", false},
		{"v0.1", "v0.2.0", false},
		// Nor is a tag that is not a version.
		{"v0.1.0", "nightly", false},
		{"v0.1.0", "", false},
		{"v0.1.0", "0.2.0", false},
	} {
		if got := Newer(c.current, c.latest); got != c.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", c.current, c.latest, got, c.want)
		}
	}
}

// github stands in for GitHub's latest release: answer says what it
// answers, and asked counts the asks.
type github struct {
	asked  atomic.Int32
	answer atomic.Value // func(http.ResponseWriter)
	*httptest.Server
}

func newGitHub(t *testing.T) *github {
	g := &github{}
	g.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.asked.Add(1)
		if r.Header.Get("User-Agent") == "" {
			t.Error("GitHub refuses a request with no User-Agent")
		}
		g.answer.Load().(func(http.ResponseWriter))(w)
	}))
	t.Cleanup(g.Close)
	g.release("v0.2.0")
	return g
}

func (g *github) release(tag string) {
	g.answer.Store(func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tag_name":"` + tag + `","html_url":"https://github.com/GT-Drehmal/avSYS/releases/tag/` + tag + `","draft":false,"prerelease":false}`))
	})
}

func (g *github) down(status int) {
	g.answer.Store(func(w http.ResponseWriter) { http.Error(w, "no", status) })
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time       { return c.t }
func (c *clock) pass(d time.Duration) { c.t = c.t.Add(d) }
func checker(g *github, current string, c *clock) *Checker {
	return &Checker{Current: current, URL: g.URL, Client: g.Client(), Now: c.now}
}

func TestNewerReleaseIsFoundAndKeptForHours(t *testing.T) {
	g := newGitHub(t)
	at := &clock{time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)}
	c := checker(g, "v0.1.0", at)
	ctx := context.Background()

	if c.Newer() != nil {
		t.Fatal("a newer release before asking")
	}
	c.Refresh(ctx)
	got := c.Newer()
	if got == nil || got.Version != "v0.2.0" || got.Notes != "https://github.com/GT-Drehmal/avSYS/releases/tag/v0.2.0" {
		t.Fatalf("Newer() = %+v, want v0.2.0 with its notes", got)
	}

	// Kept: the page asking every few seconds does not ask GitHub.
	g.release("v0.3.0")
	for range 5 {
		at.pass(time.Hour)
		c.Refresh(ctx)
	}
	if n := g.asked.Load(); n != 1 {
		t.Fatalf("GitHub asked %d times in five hours, want once", n)
	}
	if c.Newer().Version != "v0.2.0" {
		t.Fatal("the answer changed before it was asked again")
	}
	at.pass(time.Hour)
	c.Refresh(ctx)
	if n := g.asked.Load(); n != 2 {
		t.Fatalf("GitHub asked %d times after six hours, want twice", n)
	}
	if c.Newer().Version != "v0.3.0" {
		t.Fatalf("Newer() = %+v after six hours, want v0.3.0", c.Newer())
	}
}

func TestNoNoticeWhenUpToDate(t *testing.T) {
	g := newGitHub(t)
	at := &clock{time.Now()}
	c := checker(g, "v0.2.0", at)
	c.Refresh(context.Background())
	if c.Newer() != nil {
		t.Fatalf("Newer() = %+v for the release that runs", c.Newer())
	}
	// Upgraded past it, to a candidate of the next.
	c = checker(g, "v0.3.0-rc.1", at)
	c.Refresh(context.Background())
	if c.Newer() != nil {
		t.Fatalf("Newer() = %+v, older than what runs", c.Newer())
	}
}

// A build of a commit never asks, and never says there is a newer release.
func TestBuildsOfCommitsDoNotAsk(t *testing.T) {
	g := newGitHub(t)
	for _, v := range []string{"sha-1234567", "edge", "dev", "e2e", ""} {
		c := checker(g, v, &clock{time.Now()})
		c.Refresh(context.Background())
		if c.Newer() != nil {
			t.Errorf("%q: Newer() = %+v", v, c.Newer())
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		c.Run(ctx)
	}
	if n := g.asked.Load(); n != 0 {
		t.Fatalf("GitHub asked %d times by builds of commits", n)
	}
}

func TestGitHubDown(t *testing.T) {
	g := newGitHub(t)
	at := &clock{time.Now()}
	c := checker(g, "v0.1.0", at)
	ctx := context.Background()

	// Down from the start: nothing is said, and it is asked again in an
	// hour, not on every refresh.
	g.down(http.StatusServiceUnavailable)
	c.Refresh(ctx)
	if c.Newer() != nil {
		t.Fatal("a newer release from a failed ask")
	}
	at.pass(30 * time.Minute)
	c.Refresh(ctx)
	if n := g.asked.Load(); n != 1 {
		t.Fatalf("asked %d times within the hour after failing, want once", n)
	}
	at.pass(30 * time.Minute)
	g.release("v0.2.0")
	c.Refresh(ctx)
	if c.Newer() == nil || c.Newer().Version != "v0.2.0" {
		t.Fatalf("Newer() = %+v once GitHub answers again", c.Newer())
	}

	// Down later, as rate limited: what was known is kept.
	at.pass(6 * time.Hour)
	g.down(http.StatusForbidden)
	c.Refresh(ctx)
	if c.Newer() == nil || c.Newer().Version != "v0.2.0" {
		t.Fatalf("Newer() = %+v after a failed ask, want what was known", c.Newer())
	}

	// Not answering at all, and answering with something else.
	for _, answer := range []func(http.ResponseWriter){
		func(w http.ResponseWriter) { _, _ = w.Write([]byte("<html>")) },
		func(w http.ResponseWriter) { _, _ = w.Write([]byte(`{"tag_name":"v9.0.0","draft":true}`)) },
		func(w http.ResponseWriter) { _, _ = w.Write([]byte(`{"tag_name":"v9.0.0-rc.1","prerelease":true}`)) },
	} {
		g.answer.Store(answer)
		at.pass(6 * time.Hour)
		c.Refresh(ctx)
		if v := c.Newer(); v != nil && v.Version == "v9.0.0" {
			t.Fatal("a draft taken for a release")
		}
	}
	gone := checker(g, "v0.1.0", at)
	gone.URL = "http://127.0.0.1:1/"
	gone.Refresh(ctx)
	if gone.Newer() != nil {
		t.Fatal("a newer release from nowhere")
	}
}

// No release at all yet answers 404.
func TestNoReleaseYet(t *testing.T) {
	g := newGitHub(t)
	g.down(http.StatusNotFound)
	c := checker(g, "v0.1.0", &clock{time.Now()})
	c.Refresh(context.Background())
	if c.Newer() != nil {
		t.Fatal("a newer release where there is none")
	}
}

func TestCommand(t *testing.T) {
	l := Latest{Version: "v0.2.0"}
	helm := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "games", Namespace: "games",
		Annotations: map[string]string{"meta.helm.sh/release-name": "games", "meta.helm.sh/release-namespace": "games"},
		Labels:      map[string]string{"helm.sh/chart": "avsys-0.1.0"}}}
	if got, want := l.Command(InstallOf(helm)), "helm upgrade games oci://ghcr.io/gt-drehmal/charts/avsys -n games --reset-then-reuse-values --version 0.2.0"; got != want {
		t.Errorf("installed with Helm: %q, want %q", got, want)
	}
	manifest := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "avsys", Namespace: "avsys", Labels: map[string]string{"helm.sh/chart": "avsys-0.1.0"}}}
	if got := l.Command(InstallOf(manifest)); got != "kubectl apply -f install.yaml" {
		t.Errorf("installed from install.yaml: %q", got)
	}
	if got := l.Manifest(); got != "https://github.com/GT-Drehmal/avSYS/releases/download/v0.2.0/install.yaml" {
		t.Errorf("Manifest() = %q", got)
	}
	// By hand, or outside the cluster: not known.
	for _, svc := range []*corev1.Service{nil, {ObjectMeta: metav1.ObjectMeta{Name: "avsys"}}} {
		if got := l.Command(InstallOf(svc)); got != "" {
			t.Errorf("not installed with the chart: %q", got)
		}
	}
}
