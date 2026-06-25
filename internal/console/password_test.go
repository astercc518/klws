// internal/console/password_test.go
package console

import "testing"

func TestHashAndVerifyPassword(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if hash == "" || hash == "correct horse battery staple" {
		t.Fatalf("hash must be non-empty and not plaintext, got %q", hash)
	}
	ok, err := VerifyPassword(hash, "correct horse battery staple")
	if err != nil || !ok {
		t.Fatalf("verify correct: ok=%v err=%v", ok, err)
	}
	ok, err = VerifyPassword(hash, "wrong password")
	if err != nil {
		t.Fatalf("verify wrong returned err: %v", err)
	}
	if ok {
		t.Fatalf("verify wrong must be false")
	}
}

func TestVerifyPasswordRejectsMalformedHash(t *testing.T) {
	if _, err := VerifyPassword("not-a-valid-hash", "x"); err == nil {
		t.Fatalf("expected error for malformed hash")
	}
}
