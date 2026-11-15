// Package auth implements password hashing and the OAuth sign-in providers.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strings"
	"sync"

	"golang.org/x/crypto/argon2"
)

const (
	argonTime    = 2
	argonMemory  = 64 * 1024
	argonThreads = 1
	argonKeyLen  = 32
)

// hashing holds the panel to a few password hashes at a time. Each takes
// argonMemory of memory, so that many sign-ins at once, as from someone
// guessing, would otherwise take all there is.
var hashing = make(chan struct{}, 4)

func idKey(password, salt []byte, time, memory uint32, threads uint8, keyLen uint32) []byte {
	hashing <- struct{}{}
	defer func() { <-hashing }()
	return argon2.IDKey(password, salt, time, memory, threads, keyLen)
}

// HashPassword returns an argon2id hash in the standard PHC string format.
func HashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := idKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

// CheckPassword reports whether password matches a hash from HashPassword.
func CheckPassword(hash, password string) bool {
	parts := strings.Split(hash, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	var memory, time uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &time, &threads); err != nil {
		return false
	}
	salt, err1 := base64.RawStdEncoding.DecodeString(parts[4])
	want, err2 := base64.RawStdEncoding.DecodeString(parts[5])
	if err1 != nil || err2 != nil {
		return false
	}
	got := idKey([]byte(password), salt, time, memory, threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

var (
	noPasswordOnce sync.Once
	noPassword     string
)

// CheckNoPassword does the work of CheckPassword for someone who has no
// password, or is not there at all, and fails: so that a sign-in takes as
// long either way, and how long does not tell whether an account exists.
func CheckNoPassword(password string) {
	noPasswordOnce.Do(func() {
		noPassword, _ = HashPassword("there is no password")
	})
	CheckPassword(noPassword, password)
}
