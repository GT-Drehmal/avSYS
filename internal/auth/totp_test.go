package auth

import (
	"strings"
	"testing"
	"time"
)

// The test vectors of RFC 6238 (SHA-1), to six digits.
func TestTOTPStepMatchesRFC6238(t *testing.T) {
	const secret = "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ" // "12345678901234567890"
	for unix, code := range map[int64]string{59: "287082", 1111111109: "081804", 1234567890: "005924", 2000000000: "279037"} {
		at := time.Unix(unix, 0)
		if got, _ := TOTPCode(secret, at); got != code {
			t.Errorf("code at %d: %s, want %s", unix, got, code)
		}
		step, ok := TOTPStep(secret, code, at)
		if !ok || step != unix/30 {
			t.Errorf("checking the code at %d: step %d, %v", unix, step, ok)
		}
		// A step either side is taken; two away is not.
		if _, ok := TOTPStep(secret, code, at.Add(30*time.Second)); !ok {
			t.Errorf("the code at %d, a step later, was refused", unix)
		}
		if _, ok := TOTPStep(secret, code, at.Add(90*time.Second)); ok {
			t.Errorf("the code at %d, three steps later, was taken", unix)
		}
	}
	for _, bad := range []string{"", "28708", "2870820", "abcdef", "287 082x"} {
		if _, ok := TOTPStep(secret, bad, time.Unix(59, 0)); ok {
			t.Errorf("%q was taken", bad)
		}
	}
	if _, ok := TOTPStep(secret, " 287 082 ", time.Unix(59, 0)); !ok {
		t.Error("a code typed with spaces was refused")
	}
}

func TestNewTOTPKey(t *testing.T) {
	key, err := NewTOTPKey("avSYS", "steve@panel.example")
	if err != nil {
		t.Fatal(err)
	}
	if len(key.Secret) != 32 || !strings.HasPrefix(key.URL, "otpauth://totp/avSYS:steve@panel.example?") || !strings.Contains(key.URL, "secret="+key.Secret) {
		t.Errorf("key %+v", key)
	}
	if !strings.HasPrefix(key.QR, "data:image/png;base64,iVBOR") {
		t.Errorf("QR %.40s", key.QR)
	}
	other, _ := NewTOTPKey("avSYS", "steve@panel.example")
	if other.Secret == key.Secret {
		t.Error("two keys have one secret")
	}
}

func TestRecoveryCodes(t *testing.T) {
	codes := NewRecoveryCodes(10)
	seen := map[string]bool{}
	for _, c := range codes {
		if len(c) != 11 || c[5] != '-' || RecoveryCode(c) != c || seen[c] {
			t.Errorf("code %q", c)
		}
		seen[c] = true
	}
	for typed, want := range map[string]string{
		"ABCDE-FGHIJ": "abcde-fghij", " abcdefghij ": "abcde-fghij", "abcde fghij": "abcde-fghij",
		"abcde-fghi": "", "abcde-fghi1": "", "123456": "", "": "",
	} {
		if got := RecoveryCode(typed); got != want {
			t.Errorf("RecoveryCode(%q) = %q, want %q", typed, got, want)
		}
	}
}
