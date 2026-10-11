package api

import "testing"

func TestDataPathStaysInsideServerDirectory(t *testing.T) {
	for in, want := range map[string]string{
		"":                            "/data",
		"/":                           "/data",
		"config/paper.yml":            "/data/config/paper.yml",
		"/world/../server.properties": "/data/server.properties",
		"../../etc/passwd":            "/data/etc/passwd",
		"/../..":                      "/data",
	} {
		got, err := dataPath(in)
		if err != nil || got != want {
			t.Errorf("dataPath(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}

func TestSafeNext(t *testing.T) {
	for in, want := range map[string]string{
		"/o/friends":           "/o/friends",
		"":                     "/",
		"https://evil.example": "/",
		"//evil.example":       "/",
		"/\\evil.example":      "/",
	} {
		if got := safeNext(in); got != want {
			t.Errorf("safeNext(%q) = %q, want %q", in, got, want)
		}
	}
}
