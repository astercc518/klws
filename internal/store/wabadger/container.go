package wabadger

import (
	"context"
	"errors"

	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	waLog "go.mau.fi/whatsmeow/util/log"
)

// ErrDeviceIDMustBeSet mirrors sqlstore.ErrDeviceIDMustBeSet: PutDevice/
// DeleteDevice require a known JID before touching the database.
var ErrDeviceIDMustBeSet = errors.New("wabadger: device JID must be known before accessing database")

// Container implements store.DeviceContainer backed by BadgerDB. The LID map is
// global (container-level), matching sqlstore where device.LIDs = c.LIDMap.
type Container struct {
	db     *DB
	log    waLog.Logger
	lidMap *LIDMap
}

// NewContainer wraps an already-open *DB in a Container. The caller owns the
// DB's lifetime (Open/Close); Container never closes it.
func NewContainer(db *DB, log waLog.Logger) *Container {
	if log == nil {
		log = waLog.Noop
	}
	c := &Container{db: db, log: log}
	c.lidMap = newLIDMap(db)
	return c
}

var _ store.DeviceContainer = (*Container)(nil)

// initializeDevice wires all per-device stores + the global LID store, mirroring
// sqlstore.Container.initializeDevice.
func (c *Container) initializeDevice(d *store.Device) {
	bs := newBadgerStore(c.db, *d.ID)
	d.SetAllStores(bs)
	d.LIDs = c.lidMap
	d.Container = c
	d.Initialized = true
}

// NewDevice creates a new, not-yet-persisted device (no JID yet — assigned
// during pairing). Mirrors sqlstore.Container.NewDevice.
func (c *Container) NewDevice() *store.Device {
	d := &store.Device{
		Log:            c.log,
		Container:      c,
		NoiseKey:       newKeyPair(),
		IdentityKey:    newKeyPair(),
		RegistrationID: randUint32(),
		AdvSecretKey:   randBytes32(),
	}
	d.SignedPreKey = d.IdentityKey.CreateSignedPreKey(1)
	return d
}

// GetDevice finds the device with the specified JID. Returns (nil, nil) if not found.
func (c *Container) GetDevice(ctx context.Context, jid types.JID) (*store.Device, error) {
	b, err := c.db.get(kb("dev", jid.String()))
	if err != nil || b == nil {
		return nil, err
	}
	d, err := decodeDevice(b, c.log)
	if err != nil {
		return nil, err
	}
	c.initializeDevice(d)
	return d, nil
}

// GetAllDevices returns every persisted device.
func (c *Container) GetAllDevices(ctx context.Context) ([]*store.Device, error) {
	var out []*store.Device
	err := c.db.scanPrefix(kp("dev"), func(_, v []byte) error {
		d, err := decodeDevice(v, c.log)
		if err != nil {
			return err
		}
		c.initializeDevice(d)
		out = append(out, d)
		return nil
	})
	return out, err
}

// PutDevice stores the given device. Device rows are low-frequency but
// critical (losing one forces re-pairing), so the write is durable (sync=true).
func (c *Container) PutDevice(ctx context.Context, d *store.Device) error {
	if d.ID == nil {
		return ErrDeviceIDMustBeSet
	}
	b, err := encodeDevice(d)
	if err != nil {
		return err
	}
	if err := c.db.put(kb("dev", d.ID.String()), b, true); err != nil { // dev = sync
		return err
	}
	if !d.Initialized {
		c.initializeDevice(d)
	}
	return nil
}

// DeleteDevice removes the device row and every one of its per-device
// sub-store keys.
func (c *Container) DeleteDevice(ctx context.Context, d *store.Device) error {
	if d.ID == nil {
		return ErrDeviceIDMustBeSet
	}
	jid := d.ID.String()
	// Delete all per-device sub-store keys (every code\x00jid\x00…) then the
	// device row itself. Most codes store multi-segment keys
	// (code\x00jid\x00subkey), which dropPrefix(kp(code,jid)) — prefix
	// "code\x00jid\x00" — removes correctly. A couple of codes ("nct", "pkm")
	// store a single per-device value with NO further segment
	// (kb(code,jid), see PutNCTSalt/prekey counter), so the key itself is
	// exactly "code\x00jid" — one byte shorter than kp's prefix, meaning
	// dropPrefix alone can never match it (a string can't have a strictly
	// longer string as its own prefix). del(kb(code,jid)) below closes that
	// gap; for the multi-segment codes, kb(code,jid) is never itself a stored
	// key, so the extra del is a harmless no-op.
	for _, code := range perDeviceCodes {
		if err := c.db.dropPrefix(kp(code, jid)); err != nil {
			return err
		}
		if err := c.db.del(kb(code, jid)); err != nil {
			return err
		}
	}
	return c.db.del(kb("dev", jid))
}

// perDeviceCodes enumerates every key namespace scoped to a single device, used
// by DeleteDevice. Verified against every kb("code", jid, ...)/kp("code", jid,
// ...) call site in store_session.go, store_prekey.go, store_appstate.go and
// store_misc.go — this list must stay exhaustive or DeleteDevice will leak
// state for a deleted device. LID mappings (lidpn/lidlid) are intentionally
// excluded: they're global (per-account), not per-device, matching sqlstore
// where whatsmeow_lid_map has no device-scoping column either.
var perDeviceCodes = []string{
	"idt", "ses", "pk", "pkm", "sk", "ask", "asv", "asm",
	"ctc", "chs", "msec", "ptk", "nct", "evb", "oge",
}
