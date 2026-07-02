package wabadger

import (
	"context"
	"testing"
)

func TestPreKey_GenGetMarkCount(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	ks, err := s.GetOrGenPreKeys(ctx, 5)
	if err != nil || len(ks) != 5 {
		t.Fatalf("GetOrGenPreKeys = %d,%v; want 5", len(ks), err)
	}
	// idempotent-ish: unuploaded keys are reused, count stays 5
	ks2, _ := s.GetOrGenPreKeys(ctx, 5)
	if len(ks2) != 5 || ks2[0].KeyID != ks[0].KeyID {
		t.Fatalf("second GetOrGen must reuse unuploaded keys")
	}
	got, err := s.GetPreKey(ctx, ks[0].KeyID)
	if err != nil || got == nil || got.KeyID != ks[0].KeyID {
		t.Fatalf("GetPreKey = %v,%v", got, err)
	}
	if err := s.MarkPreKeysAsUploaded(ctx, ks[2].KeyID); err != nil {
		t.Fatalf("MarkUploaded: %v", err)
	}
	if n, _ := s.UploadedPreKeyCount(ctx); n != 3 {
		t.Fatalf("UploadedPreKeyCount = %d; want 3", n)
	}
	if err := s.RemovePreKey(ctx, ks[0].KeyID); err != nil {
		t.Fatalf("RemovePreKey: %v", err)
	}
	if got, _ := s.GetPreKey(ctx, ks[0].KeyID); got != nil {
		t.Fatalf("prekey present after remove")
	}
	one, err := s.GenOnePreKey(ctx)
	if err != nil || one == nil {
		t.Fatalf("GenOnePreKey: %v,%v", one, err)
	}
}
