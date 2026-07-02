package wabadger

import (
	"bytes"
	"context"
	"testing"

	"go.mau.fi/whatsmeow/types"
)

func TestSession_CRUDAndMany(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	if v, _ := s.GetSession(ctx, "u:1"); v != nil {
		t.Fatalf("missing session must be nil")
	}
	_ = s.PutSession(ctx, "u:1", []byte("s1"))
	_ = s.PutSession(ctx, "u:2", []byte("s2"))
	if has, _ := s.HasSession(ctx, "u:1"); !has {
		t.Fatalf("HasSession false after put")
	}
	// GetManySessions returns one entry per requested address; misses are
	// present with a nil value (u:3 is not stored yet at this point).
	m, err := s.GetManySessions(ctx, []string{"u:1", "u:2", "u:3"})
	if err != nil || len(m) != 3 || !bytes.Equal(m["u:1"], []byte("s1")) {
		t.Fatalf("GetManySessions = %v,%v", m, err)
	}
	if v, ok := m["u:3"]; !ok || v != nil {
		t.Fatalf("GetManySessions must nil-fill misses, got m[u:3]=%v ok=%v", v, ok)
	}
	_ = s.PutManySessions(ctx, map[string][]byte{"u:3": []byte("s3")})
	if v, _ := s.GetSession(ctx, "u:3"); !bytes.Equal(v, []byte("s3")) {
		t.Fatalf("PutManySessions failed")
	}
	_ = s.DeleteSession(ctx, "u:1")
	if has, _ := s.HasSession(ctx, "u:1"); has {
		t.Fatalf("DeleteSession failed")
	}
}

func TestSession_MigratePNToLID(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	pn := types.NewJID("15551230000", types.DefaultUserServer)
	lid := types.NewJID("88887777", types.HiddenUserServer)
	pnUser := pn.SignalAddressUser()   // "15551230000" (phone: no agent suffix)
	lidUser := lid.SignalAddressUser() // "88887777_1"  (LID: "_<agent>" suffix)

	pnAddr := pnUser + ":1"
	lidAddr := lidUser + ":1"
	// Decoy: a different user whose signal address merely shares a byte-prefix
	// with pnUser ("155512300009" starts with "15551230000" but is a distinct
	// account). Must NOT be migrated.
	decoyAddr := pnUser + "9:1"

	var idKey [32]byte
	idKey[0] = 7

	if err := s.PutSession(ctx, pnAddr, []byte("sess-v1")); err != nil {
		t.Fatalf("PutSession: %v", err)
	}
	if err := s.PutIdentity(ctx, pnAddr, idKey); err != nil {
		t.Fatalf("PutIdentity: %v", err)
	}
	if err := s.PutSession(ctx, decoyAddr, []byte("decoy")); err != nil {
		t.Fatalf("PutSession decoy: %v", err)
	}

	if err := s.MigratePNToLID(ctx, pn, lid); err != nil {
		t.Fatalf("MigratePNToLID: %v", err)
	}

	// (a) session migrated to the LID address
	if v, _ := s.GetSession(ctx, lidAddr); !bytes.Equal(v, []byte("sess-v1")) {
		t.Fatalf("session not migrated to LID: got %q", v)
	}
	// (b) identity migrated to the LID address
	if v, _ := s.db.get(kb("idt", s.jid, lidAddr)); !bytes.Equal(v, idKey[:]) {
		t.Fatalf("identity not migrated to LID: got %x", v)
	}
	// (c) PN-address session AND identity are gone after migration
	if has, _ := s.HasSession(ctx, pnAddr); has {
		t.Fatalf("PN session must be deleted after migration")
	}
	if v, _ := s.GetSession(ctx, pnAddr); v != nil {
		t.Fatalf("PN session must be nil after migration, got %q", v)
	}
	if v, _ := s.db.get(kb("idt", s.jid, pnAddr)); v != nil {
		t.Fatalf("PN identity must be deleted after migration, got %x", v)
	}
	// (d) decoy different-user address is untouched
	if v, _ := s.GetSession(ctx, decoyAddr); !bytes.Equal(v, []byte("decoy")) {
		t.Fatalf("decoy session must be untouched: got %q", v)
	}
}
