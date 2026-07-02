package wabadger

import (
	"context"
	"testing"

	"go.mau.fi/whatsmeow/types"
)

func newTestStore(t *testing.T) *badgerStore {
	return newBadgerStore(openTestDB(t), types.NewJID("15550000001", types.DefaultUserServer))
}

func TestIdentity_PutTrustDelete(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	var key [32]byte
	key[0] = 9

	// unknown identity is trusted
	if ok, err := s.IsTrustedIdentity(ctx, "u1:1", key); err != nil || !ok {
		t.Fatalf("unknown identity should be trusted: %v,%v", ok, err)
	}
	if err := s.PutIdentity(ctx, "u1:1", key); err != nil {
		t.Fatalf("PutIdentity: %v", err)
	}
	if ok, _ := s.IsTrustedIdentity(ctx, "u1:1", key); !ok {
		t.Fatalf("stored identity should be trusted")
	}
	var other [32]byte
	other[0] = 1
	if ok, _ := s.IsTrustedIdentity(ctx, "u1:1", other); ok {
		t.Fatalf("different key must be untrusted")
	}
	_ = s.PutIdentity(ctx, "u1:2", key)
	if err := s.DeleteAllIdentities(ctx, "u1"); err != nil {
		t.Fatalf("DeleteAllIdentities: %v", err)
	}
	if ok, _ := s.IsTrustedIdentity(ctx, "u1:1", other); !ok {
		t.Fatalf("after delete-all, identity should be unknown→trusted")
	}
}
