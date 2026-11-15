package auth

import (
	"bytes"
	"crypto/rand"
	"encoding/base32"
	"encoding/base64"
	"image/png"
	"strings"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/hotp"
	"github.com/pquerna/otp/totp"
)

// Codes are those of RFC 6238 as every authenticator app reads them: six
// digits, a new one every 30 seconds, from HMAC-SHA1.
const (
	totpPeriod = 30
	// totpSkew is how many steps either side of now a code is taken from,
	// for a phone whose clock is a little off and a code typed as it turns.
	totpSkew = 1
)

// TOTPKey is a new secret for a second factor.
type TOTPKey struct {
	// Secret is the secret in base32, for typing into an app by hand.
	Secret string
	// URL is the otpauth:// address of it, which the QR code holds.
	URL string
	// QR is a PNG of the QR code, as a data: URL.
	QR string
}

// NewTOTPKey makes a secret for account, named for issuer in the app.
func NewTOTPKey(issuer, account string) (*TOTPKey, error) {
	key, err := totp.Generate(totp.GenerateOpts{Issuer: issuer, AccountName: account, Period: totpPeriod, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1})
	if err != nil {
		return nil, err
	}
	img, err := key.Image(240, 240)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return &TOTPKey{Secret: key.Secret(), URL: key.URL(), QR: "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())}, nil
}

// TOTPStep checks a code against a secret at time t. ok is whether it is
// the code of a step within totpSkew of t's; step is which, so that the
// caller can refuse a code of a step already used.
func TOTPStep(secret, code string, t time.Time) (step int64, ok bool) {
	code = strings.ReplaceAll(strings.TrimSpace(code), " ", "")
	now := t.Unix() / totpPeriod
	for s := now - totpSkew; s <= now+totpSkew; s++ {
		valid, err := hotp.ValidateCustom(code, uint64(s), secret, hotp.ValidateOpts{Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1})
		if err == nil && valid {
			return s, true
		}
	}
	return 0, false
}

// TOTPCode is the code of a secret at time t, as an app would show it.
func TOTPCode(secret string, t time.Time) (string, error) {
	return totp.GenerateCodeCustom(secret, t, totp.ValidateOpts{Period: totpPeriod, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1})
}

var recoveryEncoding = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

// NewRecoveryCodes makes n codes to sign in with once each in place of a
// code from the app, as "xxxxx-xxxxx": 50 random bits each.
func NewRecoveryCodes(n int) []string {
	codes := make([]string, n)
	for i := range codes {
		b := make([]byte, 7)
		if _, err := rand.Read(b); err != nil {
			panic(err)
		}
		s := recoveryEncoding.EncodeToString(b)[:10]
		codes[i] = s[:5] + "-" + s[5:]
	}
	return codes
}

// RecoveryCode is a recovery code as typed, in the form NewRecoveryCodes
// makes them, or "" for something that is not one.
func RecoveryCode(typed string) string {
	s := strings.ToLower(strings.NewReplacer("-", "", " ", "").Replace(strings.TrimSpace(typed)))
	if len(s) != 10 {
		return ""
	}
	for _, c := range s {
		if !strings.ContainsRune("abcdefghijklmnopqrstuvwxyz234567", c) {
			return ""
		}
	}
	return s[:5] + "-" + s[5:]
}
