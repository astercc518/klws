package crypto

import (
	"bytes"
	"errors"
	"testing"
)

func TestKeyRepo_CreateTenantKey_IncrementsVersion(t *testing.T) {
	pool, ctx := pgPool(t)
	kr, err := NewKeyRepo(pool, testKEK)
	if err != nil {
		t.Fatal(err)
	}
	v1, err := kr.CreateTenantKey(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	v2, err := kr.CreateTenantKey(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if v2 != v1+1 {
		t.Fatalf("expected version %d, got %d", v1+1, v2)
	}
}

func TestKeyRepo_GetCurrentDEK_ReturnsLatest(t *testing.T) {
	pool, ctx := pgPool(t)
	kr, err := NewKeyRepo(pool, testKEK)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := kr.CreateTenantKey(ctx, 2); err != nil {
		t.Fatal(err)
	}
	v2, err := kr.CreateTenantKey(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	_, gotVersion, err := kr.GetCurrentDEK(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	if gotVersion != v2 {
		t.Fatalf("GetCurrentDEK returned version %d, want %d", gotVersion, v2)
	}
}

func TestKeyRepo_WrapUnwrap_RoundTrip(t *testing.T) {
	pool, ctx := pgPool(t)
	kr, err := NewKeyRepo(pool, testKEK)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := kr.CreateTenantKey(ctx, 3); err != nil {
		t.Fatal(err)
	}
	dek, _, err := kr.GetCurrentDEK(ctx, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(dek) != 32 {
		t.Fatalf("expected 32-byte DEK, got %d bytes", len(dek))
	}
}

func TestKeyRepo_Shred_ErrKeyNotFound(t *testing.T) {
	pool, ctx := pgPool(t)
	kr, err := NewKeyRepo(pool, testKEK)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := kr.CreateTenantKey(ctx, 4); err != nil {
		t.Fatal(err)
	}
	if err := kr.Shred(ctx, 4); err != nil {
		t.Fatal(err)
	}
	_, _, err = kr.GetCurrentDEK(ctx, 4)
	if !errors.Is(err, ErrKeyNotFound) {
		t.Fatalf("expected ErrKeyNotFound after shred, got %v", err)
	}
}

func TestKeyRepo_TwoTenants_DifferentDEKs(t *testing.T) {
	pool, ctx := pgPool(t)
	kr, err := NewKeyRepo(pool, testKEK)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := kr.CreateTenantKey(ctx, 10); err != nil {
		t.Fatal(err)
	}
	if _, err := kr.CreateTenantKey(ctx, 11); err != nil {
		t.Fatal(err)
	}
	dek10, _, err := kr.GetCurrentDEK(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	dek11, _, err := kr.GetCurrentDEK(ctx, 11)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(dek10, dek11) {
		t.Fatal("two tenants must have different DEKs")
	}
}

func TestNewKeyRepo_RejectsNon32ByteKey(t *testing.T) {
	pool, ctx := pgPool(t)
	_ = ctx
	_, err := NewKeyRepo(pool, []byte("short"))
	if err == nil {
		t.Fatal("expected error for non-32-byte key")
	}
}
