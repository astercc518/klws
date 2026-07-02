package wabadger

import (
	"bytes"
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	_ "github.com/jackc/pgx/v5/stdlib"
	waAdv "go.mau.fi/whatsmeow/proto/waAdv"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/util/keys"
	waLog "go.mau.fi/whatsmeow/util/log"
)

// sampleRefDevice is like sampleDevice() but with correctly-sized Account
// fields: unlike Badger, sqlstore's Postgres schema CHECKs
// length(adv_account_sig)=64, length(adv_account_sig_key)=32,
// length(adv_device_sig)=64 (see upgrades/00-latest-schema.sql), so the
// short placeholder byte slices sampleDevice() uses for Badger-only tests
// would violate those constraints against a real PG reference.
func sampleRefDevice() *store.Device {
	jid := types.NewJID("15551234568", types.DefaultUserServer)
	jid.Device = 4
	d := &store.Device{
		Log:            waLog.Noop,
		NoiseKey:       keys.NewKeyPair(),
		IdentityKey:    keys.NewKeyPair(),
		RegistrationID: 42,
		AdvSecretKey:   bytes.Repeat([]byte{7}, 32),
		ID:             &jid,
		Platform:       "test", BusinessName: "biz", PushName: "pn",
		Account: &waAdv.ADVSignedDeviceIdentity{
			Details:             []byte("d"),
			AccountSignature:    bytes.Repeat([]byte{1}, 64),
			AccountSignatureKey: bytes.Repeat([]byte{2}, 32),
			DeviceSignature:     bytes.Repeat([]byte{3}, 64),
		},
	}
	d.SignedPreKey = d.IdentityKey.CreateSignedPreKey(1)
	return d
}

// newRefContainer spins a real PG (testcontainers) and returns whatsmeow's own
// sqlstore.Container as the behavioural reference for differential tests
// against the Badger-backed Container in this package. It is self-contained
// (no dependency on internal/store's test helpers, since that's a different
// package) and skips gracefully when Docker isn't available.
func newRefContainer(t *testing.T) *sqlstore.Container {
	t.Helper()
	ctx := context.Background()
	pg, err := postgres.Run(ctx, "postgres:16",
		postgres.WithDatabase("wm"),
		postgres.WithUsername("wm"),
		postgres.WithPassword("wm"),
		testcontainers.WithWaitStrategy(
			wait.ForAll(
				wait.ForListeningPort("5432/tcp"),
				wait.ForLog("database system is ready to accept connections").WithOccurrence(2),
			).WithStartupTimeout(60*time.Second)),
	)
	if err != nil {
		t.Skipf("no docker for reference PG: %v", err)
	}
	t.Cleanup(func() { _ = pg.Terminate(ctx) })

	dsn, err := pg.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("dsn: %v", err)
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	c := sqlstore.NewWithDB(db, "postgres", waLog.Noop)
	if err := c.Upgrade(ctx); err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	return c
}

// TestDiffContainer_DevicePersistence exercises the same Put/Get/GetAll/Delete
// device round-trip against the real sqlstore.Container (backed by Postgres)
// to prove newRefContainer works end-to-end and stays a usable scaffold for
// future differential assertions between wabadger and sqlstore. Skips (via
// newRefContainer) when Docker is unavailable.
func TestDiffContainer_DevicePersistence(t *testing.T) {
	ctx := context.Background()
	ref := newRefContainer(t)

	nd := ref.NewDevice()
	if nd.ID != nil {
		t.Fatalf("NewDevice must have nil ID before pairing")
	}

	d := sampleRefDevice()
	if err := ref.PutDevice(ctx, d); err != nil {
		t.Fatalf("PutDevice: %v", err)
	}
	got, err := ref.GetDevice(ctx, *d.ID)
	if err != nil || got == nil {
		t.Fatalf("GetDevice = %v,%v", got, err)
	}
	if got.RegistrationID != d.RegistrationID {
		t.Fatalf("regid mismatch after reload")
	}
	all, err := ref.GetAllDevices(ctx)
	if err != nil || len(all) != 1 {
		t.Fatalf("GetAllDevices = %d,%v; want 1", len(all), err)
	}
	if err := ref.DeleteDevice(ctx, d); err != nil {
		t.Fatalf("DeleteDevice: %v", err)
	}
	if got, _ := ref.GetDevice(ctx, *d.ID); got != nil {
		t.Fatalf("device present after delete")
	}
}
