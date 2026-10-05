package auth

import "testing"

func TestPasswordHash(t *testing.T) {
	hash, err := HashPassword("a-strong-password-123")
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyPassword(hash, "a-strong-password-123") || VerifyPassword(hash, "wrong") || VerifyPassword("malformed", "wrong") {
		t.Fatal("password verification failed")
	}
	if _, err := HashPassword("short"); err == nil {
		t.Fatal("short password accepted")
	}
}
