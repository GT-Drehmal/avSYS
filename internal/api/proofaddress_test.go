package api

import "testing"

// The address to join to link an account is named on the page: under the
// router's domain where there is one, else the panel's own host.
func TestVerifyAddress(t *testing.T) {
	for _, c := range []struct{ domain, public, want string }{
		{"mc.example.org", "https://panel.example.org", "verify.mc.example.org"},
		{"", "https://avsys.broyojo.com", "avsys.broyojo.com"},
		{"", "https://AVSYS.broyojo.com:8443/", "avsys.broyojo.com"},
		{"", "http://localhost:8080", ""},
		{"", "http://127.0.0.1:8080", ""},
		{"", "", ""},
	} {
		s := &Server{Config: Config{RouterDomain: c.domain, PublicURL: c.public}}
		if got := s.verifyAddress(); got != c.want {
			t.Errorf("domain %q, public %q: %q, want %q", c.domain, c.public, got, c.want)
		}
	}
}
