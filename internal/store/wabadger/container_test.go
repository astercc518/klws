package wabadger

import (
	"context"
	"testing"

	waLog "go.mau.fi/whatsmeow/util/log"
)

func TestContainer_DevicePersistence(t *testing.T) {
	ctx := context.Background()
	c := NewContainer(openTestDB(t), waLog.Noop)

	nd := c.NewDevice()
	if nd.ID != nil {
		t.Fatalf("NewDevice must have nil ID before pairing")
	}
	d := sampleDevice() // has an ID
	if err := c.PutDevice(ctx, d); err != nil {
		t.Fatalf("PutDevice: %v", err)
	}
	got, err := c.GetDevice(ctx, *d.ID)
	if err != nil || got == nil {
		t.Fatalf("GetDevice = %v,%v", got, err)
	}
	if got.RegistrationID != d.RegistrationID {
		t.Fatalf("regid mismatch after reload")
	}
	all, err := c.GetAllDevices(ctx)
	if err != nil || len(all) != 1 {
		t.Fatalf("GetAllDevices = %d,%v; want 1", len(all), err)
	}
	if err := c.DeleteDevice(ctx, d); err != nil {
		t.Fatalf("DeleteDevice: %v", err)
	}
	if got, _ := c.GetDevice(ctx, *d.ID); got != nil {
		t.Fatalf("device present after delete")
	}
}

// TestContainer_DeleteDevicePurgesSubStores guards against a real leak found
// during implementation: "nct" and "pkm" store a single per-device value with
// no further sub-key segment (kb(code,jid), see PutNCTSalt / the prekey id
// counter), so dropPrefix(kp(code,jid)) — which requires a trailing
// separator the stored key doesn't have — never matches them on its own.
func TestContainer_DeleteDevicePurgesSubStores(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	c := NewContainer(db, waLog.Noop)
	d := sampleDevice()
	if err := c.PutDevice(ctx, d); err != nil {
		t.Fatalf("PutDevice: %v", err)
	}

	bs := newBadgerStore(db, *d.ID)
	if err := bs.PutIdentity(ctx, "peer:1", [32]byte{1}); err != nil {
		t.Fatalf("PutIdentity: %v", err)
	}
	if err := bs.PutSenderKey(ctx, "g1", "u1", []byte("sess")); err != nil {
		t.Fatalf("PutSenderKey: %v", err)
	}
	if err := bs.PutNCTSalt(ctx, []byte("salt")); err != nil {
		t.Fatalf("PutNCTSalt: %v", err)
	}

	if err := c.DeleteDevice(ctx, d); err != nil {
		t.Fatalf("DeleteDevice: %v", err)
	}

	if v, _ := db.get(kb("idt", d.ID.String(), "peer:1")); v != nil {
		t.Fatalf("idt key survived DeleteDevice: %q", v)
	}
	if v, _ := db.get(kb("sk", d.ID.String(), "g1", "u1")); v != nil {
		t.Fatalf("sk key survived DeleteDevice: %q", v)
	}
	if v, err := bs.GetNCTSalt(ctx); err != nil || v != nil {
		t.Fatalf("nct key survived DeleteDevice: %q, err=%v", v, err)
	}
}
