package crypto

import (
	"bytes"
	"errors"
	"testing"
)

func TestCipher_EncryptDecrypt_RoundTrip(t *testing.T) {
	pool, ctx := pgPool(t)
	kr, err := NewKeyRepo(pool, testKEK)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := kr.CreateTenantKey(ctx, 100); err != nil {
		t.Fatal(err)
	}
	c := NewCipher(kr)
	plaintext := []byte("hello, envelope encryption")
	blob, err := c.Encrypt(ctx, 100, plaintext)
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.Decrypt(ctx, 100, blob)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("Decrypt returned %q, want %q", got, plaintext)
	}
}

func TestCipher_Rotate_OldCiphertextStillDecrypts(t *testing.T) {
	pool, ctx := pgPool(t)
	kr, err := NewKeyRepo(pool, testKEK)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := kr.CreateTenantKey(ctx, 101); err != nil {
		t.Fatal(err)
	}
	c := NewCipher(kr)
	plaintext := []byte("rotate me")
	oldBlob, err := c.Encrypt(ctx, 101, plaintext)
	if err != nil {
		t.Fatal(err)
	}
	// Rotate: creates a new DEK version
	newVersion, err := c.Rotate(ctx, 101)
	if err != nil {
		t.Fatal(err)
	}
	if newVersion < 2 {
		t.Fatalf("expected version >= 2 after rotate, got %d", newVersion)
	}
	// Old blob must still decrypt with old DEK version embedded in blob
	got, err := c.Decrypt(ctx, 101, oldBlob)
	if err != nil {
		t.Fatalf("old ciphertext should still decrypt after rotate: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("Decrypt returned %q, want %q", got, plaintext)
	}
}

func TestCipher_Shred_DecryptReturnsErrKeyNotFound(t *testing.T) {
	pool, ctx := pgPool(t)
	kr, err := NewKeyRepo(pool, testKEK)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := kr.CreateTenantKey(ctx, 102); err != nil {
		t.Fatal(err)
	}
	c := NewCipher(kr)
	blob, err := c.Encrypt(ctx, 102, []byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	if err := kr.Shred(ctx, 102); err != nil {
		t.Fatal(err)
	}
	_, err = c.Decrypt(ctx, 102, blob)
	if !errors.Is(err, ErrKeyNotFound) {
		t.Fatalf("expected ErrKeyNotFound after shred, got %v", err)
	}
}

func TestCipher_CrossTenantIsolation(t *testing.T) {
	pool, ctx := pgPool(t)
	kr, err := NewKeyRepo(pool, testKEK)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := kr.CreateTenantKey(ctx, 200); err != nil {
		t.Fatal(err)
	}
	if _, err := kr.CreateTenantKey(ctx, 201); err != nil {
		t.Fatal(err)
	}
	c := NewCipher(kr)
	blob, err := c.Encrypt(ctx, 200, []byte("tenant-200-secret"))
	if err != nil {
		t.Fatal(err)
	}
	// Try to decrypt tenant-200's blob as tenant-201: must fail
	_, err = c.Decrypt(ctx, 201, blob)
	if err == nil {
		t.Fatal("cross-tenant decryption must fail")
	}
}
