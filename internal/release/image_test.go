package release

import "testing"

func TestIsFix(t *testing.T) {
	for _, c := range []struct {
		current, latest string
		want            bool
	}{
		{"v0.1.0", "v0.1.1", true},
		{"v1.2.3", "v1.2.10", true},
		{"v0.2.0-rc.1", "v0.2.0", true},
		{"v0.1.0", "v0.2.0", false},
		{"v1.2.3", "v2.0.0", false},
		{"v0.1.1", "v0.1.1", false},
		{"v0.1.2", "v0.1.1", false},
		{"v0.1.0", "v0.1.1-rc.1", false},
		{"sha-1234567", "v0.1.1", false},
		{"dev", "v0.1.1", false},
	} {
		if got := IsFix(c.current, c.latest); got != c.want {
			t.Errorf("IsFix(%q, %q) = %v, want %v", c.current, c.latest, got, c.want)
		}
	}
}

func TestImageTags(t *testing.T) {
	for _, c := range []struct{ image, tag, moved string }{
		{"ghcr.io/gt-drehmal/avsys:v0.1.0", "v0.1.0", "ghcr.io/gt-drehmal/avsys:v0.2.0"},
		{"ghcr.io/gt-drehmal/avsys:latest", "latest", "ghcr.io/gt-drehmal/avsys:v0.2.0"},
		{"ghcr.io/gt-drehmal/avsys", "", "ghcr.io/gt-drehmal/avsys:v0.2.0"},
		{"ghcr.io/gt-drehmal/avsys:v0.1.0@sha256:abc", "v0.1.0", "ghcr.io/gt-drehmal/avsys:v0.2.0"},
		{"ghcr.io/gt-drehmal/avsys@sha256:abc", "", "ghcr.io/gt-drehmal/avsys:v0.2.0"},
		{"registry.lan:5000/avsys:v0.1.0", "v0.1.0", "registry.lan:5000/avsys:v0.2.0"},
		{"registry.lan:5000/avsys", "", "registry.lan:5000/avsys:v0.2.0"},
	} {
		if got := TagOf(c.image); got != c.tag {
			t.Errorf("TagOf(%q) = %q, want %q", c.image, got, c.tag)
		}
		if got := WithTag(c.image, "v0.2.0"); got != c.moved {
			t.Errorf("WithTag(%q) = %q, want %q", c.image, got, c.moved)
		}
	}
}
