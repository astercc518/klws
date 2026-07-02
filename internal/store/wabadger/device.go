package wabadger

import (
	"bytes"
	"crypto/rand"
	"encoding/gob"
	"fmt"

	waAdv "go.mau.fi/whatsmeow/proto/waAdv"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/util/keys"
	waLog "go.mau.fi/whatsmeow/util/log"
)

// deviceRecord mirrors the whatsmeow_device columns exactly (see sqlstore
// container.go scanDevice/insertDeviceQuery). Fixed-length key material is
// stored as raw bytes; JIDs as their canonical string form.
type deviceRecord struct {
	JID              string
	LID              string
	RegistrationID   uint32
	NoisePriv        [32]byte
	IdentityPriv     [32]byte
	SignedPreKeyPriv [32]byte
	SignedPreKeyID   uint32
	SignedPreKeySig  [64]byte
	AdvSecretKey     []byte
	AdvDetails       []byte
	AdvAccountSig    []byte
	AdvAccountSigKey []byte
	AdvDeviceSig     []byte
	Platform         string
	BusinessName     string
	PushName         string
	FacebookUUID     string
	LIDMigrationTS   int64
}

func encodeDevice(d *store.Device) ([]byte, error) {
	if d.ID == nil {
		return nil, fmt.Errorf("wabadger: device JID must be set before save")
	}
	rec := deviceRecord{
		JID:              d.ID.String(),
		LID:              d.LID.String(),
		RegistrationID:   d.RegistrationID,
		NoisePriv:        *d.NoiseKey.Priv,
		IdentityPriv:     *d.IdentityKey.Priv,
		SignedPreKeyPriv: *d.SignedPreKey.Priv,
		SignedPreKeyID:   d.SignedPreKey.KeyID,
		SignedPreKeySig:  *d.SignedPreKey.Signature,
		AdvSecretKey:     d.AdvSecretKey,
		Platform:         d.Platform,
		BusinessName:     d.BusinessName,
		PushName:         d.PushName,
		FacebookUUID:     d.FacebookUUID.String(),
		LIDMigrationTS:   d.LIDMigrationTimestamp,
	}
	if d.Account != nil {
		rec.AdvDetails = d.Account.Details
		rec.AdvAccountSig = d.Account.AccountSignature
		rec.AdvAccountSigKey = d.Account.AccountSignatureKey
		rec.AdvDeviceSig = d.Account.DeviceSignature
	}
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(rec); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func decodeDevice(b []byte, log waLog.Logger) (*store.Device, error) {
	var rec deviceRecord
	if err := gob.NewDecoder(bytes.NewReader(b)).Decode(&rec); err != nil {
		return nil, err
	}
	jid, err := types.ParseJID(rec.JID)
	if err != nil {
		return nil, fmt.Errorf("parse device jid %q: %w", rec.JID, err)
	}
	lid, _ := types.ParseJID(rec.LID) // empty LID parses to empty JID; ignore err
	d := &store.Device{
		Log:                   log,
		RegistrationID:        rec.RegistrationID,
		AdvSecretKey:          rec.AdvSecretKey,
		ID:                    &jid,
		LID:                   lid,
		Platform:              rec.Platform,
		BusinessName:          rec.BusinessName,
		PushName:              rec.PushName,
		LIDMigrationTimestamp: rec.LIDMigrationTS,
		Account: &waAdv.ADVSignedDeviceIdentity{
			Details:             rec.AdvDetails,
			AccountSignature:    rec.AdvAccountSig,
			AccountSignatureKey: rec.AdvAccountSigKey,
			DeviceSignature:     rec.AdvDeviceSig,
		},
	}
	d.NoiseKey = keys.NewKeyPairFromPrivateKey(rec.NoisePriv)
	d.IdentityKey = keys.NewKeyPairFromPrivateKey(rec.IdentityPriv)
	d.SignedPreKey = &keys.PreKey{
		KeyPair:   *keys.NewKeyPairFromPrivateKey(rec.SignedPreKeyPriv),
		KeyID:     rec.SignedPreKeyID,
		Signature: &rec.SignedPreKeySig,
	}
	return d, nil
}

// newKeyPair generates a fresh Curve25519 key pair, matching whatsmeow's own
// clamping so signatures/ECDH interop with the rest of the protocol stack.
func newKeyPair() *keys.KeyPair {
	return keys.NewKeyPair()
}

// randUint32 returns a cryptographically random uint32, used for values like
// a freshly generated registration ID.
func randUint32() uint32 {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}

// randBytes32 returns 32 cryptographically random bytes, used e.g. for a
// freshly generated AdvSecretKey.
func randBytes32() []byte {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return b
}
