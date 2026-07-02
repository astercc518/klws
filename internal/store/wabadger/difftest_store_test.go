package wabadger

import (
	"bytes"
	"context"
	"testing"

	waAdv "go.mau.fi/whatsmeow/proto/waAdv"
	"go.mau.fi/whatsmeow/types"
	waLog "go.mau.fi/whatsmeow/util/log"
)

// TestDiff_IdentitySession runs identical ops through the reference sqlstore and
// the Badger adapter and asserts identical observable results.
func TestDiff_IdentitySession(t *testing.T) {
	ctx := context.Background()
	ref := newRefContainer(t) // skips if no docker
	jid := types.NewJID("15559999999", types.DefaultUserServer)

	// account is required for a real PutDevice round-trip: sqlstore's PutDevice
	// dereferences device.Account unconditionally (it assumes pairing already
	// populated it), and the Postgres schema CHECKs the exact signature
	// lengths (64/32/64 bytes) — see sampleRefDevice's doc comment above.
	account := &waAdv.ADVSignedDeviceIdentity{
		Details:             []byte("d"),
		AccountSignature:    bytes.Repeat([]byte{1}, 64),
		AccountSignatureKey: bytes.Repeat([]byte{2}, 32),
		DeviceSignature:     bytes.Repeat([]byte{3}, 64),
	}

	// reference device
	rd := ref.NewDevice()
	rd.ID = &jid
	rd.Account = account
	if err := ref.PutDevice(ctx, rd); err != nil {
		t.Fatalf("ref put: %v", err)
	}
	// subject device (Badger)
	sub := NewContainer(openTestDB(t), waLog.Noop)
	sd := sub.NewDevice()
	sd.ID = &jid
	sd.Account = account
	if err := sub.PutDevice(ctx, sd); err != nil {
		t.Fatalf("sub put: %v", err)
	}

	var key [32]byte
	key[0] = 3
	if err := rd.Identities.PutIdentity(ctx, "x:1", key); err != nil {
		t.Fatalf("ref put identity: %v", err)
	}
	if err := sd.Identities.PutIdentity(ctx, "x:1", key); err != nil {
		t.Fatalf("sub put identity: %v", err)
	}
	_ = rd.Sessions.PutSession(ctx, "x:1", []byte("sess"))
	_ = sd.Sessions.PutSession(ctx, "x:1", []byte("sess"))

	rHas, _ := rd.Sessions.HasSession(ctx, "x:1")
	sHas, _ := sd.Sessions.HasSession(ctx, "x:1")
	if rHas != sHas {
		t.Fatalf("HasSession parity: ref=%v sub=%v", rHas, sHas)
	}
	rSess, _ := rd.Sessions.GetSession(ctx, "x:1")
	sSess, _ := sd.Sessions.GetSession(ctx, "x:1")
	if string(rSess) != string(sSess) {
		t.Fatalf("GetSession parity mismatch")
	}
}
