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

// Properties are handed to the server as name=value lines, so an entry that
// spans lines would set a second property the caller never listed.
func TestValidateProperties(t *testing.T) {
	for _, ok := range []map[string]string{
		nil,
		{"difficulty": "hard", "level-seed": "-4172144997902289642", "motd": "Hello = world"},
		{"level-type": "flat", "generate-structures": "false", "query.port": "25565"},
	} {
		if err := validateProperties(ok); err != nil {
			t.Errorf("validateProperties(%v) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []map[string]string{
		{"motd": "hi\nonline-mode=false"},
		{"motd": "hi\ronline-mode=false"},
		{"motd\nonline-mode": "false"},
		{"": "x"},
		{"enable rcon": "true"},
		{"a=b": "c"},
	} {
		if err := validateProperties(bad); err == nil {
			t.Errorf("validateProperties(%q) = nil, want an error", bad)
		}
	}
}
