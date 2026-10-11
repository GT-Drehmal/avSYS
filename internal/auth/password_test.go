package auth

import "testing"

func TestPasswordRoundTrip(t *testing.T) {
	hash, err := HashPassword("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if !CheckPassword(hash, "correct horse") {
		t.Error("the right password was rejected")
	}
	if CheckPassword(hash, "wrong horse") {
		t.Error("the wrong password was accepted")
	}
	if CheckPassword("not-a-hash", "anything") || CheckPassword("", "") {
		t.Error("a malformed hash was accepted")
	}
}
