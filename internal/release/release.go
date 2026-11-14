// Package release finds out whether a newer release of avSYS is out than
// the one running, for the instance administrator to be told, with the
// command that upgrades to it.
//
// GitHub is asked by the panel, not by anyone's browser, at most once every
// few hours, and only by a panel that is itself a release: a build of a
// commit (sha-1234567, edge, dev) has no release to compare with.
package release

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/mod/semver"
	corev1 "k8s.io/api/core/v1"
)

// Repository is where avSYS is released.
const Repository = "GT-Drehmal/avSYS"

// LatestURL answers with the newest release that is not a prerelease.
const LatestURL = "https://api.github.com/repos/" + Repository + "/releases/latest"

// Chart is where the Helm chart of each release is.
const Chart = "oci://ghcr.io/gt-drehmal/charts/avsys"

// Latest is the newest release.
type Latest struct {
	// Version is its tag, as v1.2.3.
	Version string
	// Notes is the page that says what changed in it.
	Notes string
}

// IsRelease is whether v is the version of a release, as v1.2.3 or
// v1.2.3-rc.1, and not of a build of a commit.
func IsRelease(v string) bool {
	core := v
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		core = v[:i]
	}
	// semver also takes v1 and v1.2, which no release is called.
	return semver.IsValid(v) && strings.Count(core, ".") == 2
}

// Newer is whether latest is a newer release than current. Never for a
// current that is not a release.
func Newer(current, latest string) bool {
	return IsRelease(current) && IsRelease(latest) && semver.Compare(latest, current) > 0
}

// Checker keeps the newest release, asking for it again now and then.
type Checker struct {
	// Current is the version running.
	Current string
	// URL answers as GitHub's latest release does. Empty is LatestURL.
	URL string
	// Client asks it. Nil is http.DefaultClient's transport, with a time
	// limit.
	Client *http.Client
	// Every is how long an answer is kept; Retry how long after a failed
	// ask the next is. Zero is six hours and one hour.
	Every, Retry time.Duration
	// Now is the time. Nil is time.Now.
	Now func() time.Time
	Log *slog.Logger

	mu     sync.Mutex
	latest *Latest
	next   time.Time
	failed bool
}

func (c *Checker) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// Newer is the newest release if it is newer than the one running.
func (c *Checker) Newer() *Latest {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.latest == nil || !Newer(c.Current, c.latest.Version) {
		return nil
	}
	l := *c.latest
	return &l
}

// Refresh asks for the newest release if the last answer is old enough,
// or the last ask failed long enough ago. A failure keeps what was known.
func (c *Checker) Refresh(ctx context.Context) {
	if !IsRelease(c.Current) {
		return
	}
	c.mu.Lock()
	due := !c.now().Before(c.next)
	c.mu.Unlock()
	if !due {
		return
	}
	latest, err := c.ask(ctx)
	c.mu.Lock()
	defer c.mu.Unlock()
	if err != nil {
		c.next = c.now().Add(or(c.Retry, time.Hour))
		// Said once, not every hour while it lasts.
		if !c.failed && c.Log != nil {
			c.Log.Info("could not look for a newer release", "error", err)
		}
		c.failed = true
		return
	}
	c.latest, c.failed = latest, false
	c.next = c.now().Add(or(c.Every, 6*time.Hour))
}

// Run refreshes until ctx is done.
func (c *Checker) Run(ctx context.Context) {
	if !IsRelease(c.Current) {
		return
	}
	for {
		c.Refresh(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(10 * time.Minute):
		}
	}
}

func (c *Checker) ask(ctx context.Context) (*Latest, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	url := c.URL
	if url == "" {
		url = LatestURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "avSYS/"+c.Current)
	client := c.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	// No release yet.
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s answered %s", url, resp.Status)
	}
	var body struct {
		Tag        string `json:"tag_name"`
		URL        string `json:"html_url"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return nil, fmt.Errorf("reading the latest release: %w", err)
	}
	if body.Draft || body.Prerelease || !IsRelease(body.Tag) {
		return nil, nil
	}
	notes := body.URL
	if !strings.HasPrefix(notes, "https://github.com/") {
		notes = "https://github.com/" + Repository + "/releases/tag/" + body.Tag
	}
	return &Latest{Version: body.Tag, Notes: notes}, nil
}

func or(d, fallback time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return fallback
}

// Install is how the panel was installed, which says how it is upgraded.
type Install struct {
	// Release and Namespace are the Helm release's, if it was installed
	// with the chart.
	Release, Namespace string
	// Manifest is whether it was installed from install.yaml.
	Manifest bool
}

// InstallOf tells how the panel was installed from its own Service. Helm
// marks what it installs with the release's name; install.yaml, which is
// the chart rendered, carries the chart's label but not that.
func InstallOf(svc *corev1.Service) Install {
	if svc == nil {
		return Install{}
	}
	if name := svc.Annotations["meta.helm.sh/release-name"]; name != "" {
		ns := svc.Annotations["meta.helm.sh/release-namespace"]
		if ns == "" {
			ns = svc.Namespace
		}
		return Install{Release: name, Namespace: ns}
	}
	if strings.HasPrefix(svc.Labels["helm.sh/chart"], "avsys-") {
		return Install{Manifest: true}
	}
	return Install{}
}

// Command is what upgrades an install made this way to the release, or ""
// where how it was installed is not known.
func (l Latest) Command(in Install) string {
	switch {
	case in.Release != "":
		return fmt.Sprintf("helm upgrade %s %s -n %s --reset-then-reuse-values --version %s", in.Release, Chart, in.Namespace, strings.TrimPrefix(l.Version, "v"))
	case in.Manifest:
		return "kubectl apply -f install.yaml"
	}
	return ""
}

// Manifest is the release's install.yaml.
func (l Latest) Manifest() string {
	return "https://github.com/" + Repository + "/releases/download/" + l.Version + "/install.yaml"
}
