package wabadger

import (
	"bytes"
	"testing"

	waAdv "go.mau.fi/whatsmeow/proto/waAdv"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/util/keys"
	waLog "go.mau.fi/whatsmeow/util/log"
)

func sampleDevice() *store.Device {
	jid := types.NewJID("15551234567", types.DefaultUserServer)
	jid.Device = 3
	d := &store.Device{
		Log:            waLog.Noop,
		NoiseKey:       keys.NewKeyPair(),
		IdentityKey:    keys.NewKeyPair(),
		RegistrationID: 42,
		AdvSecretKey:   bytes.Repeat([]byte{7}, 32),
		ID:             &jid,
		Platform:       "test", BusinessName: "biz", PushName: "pn",
		Account: &waAdv.ADVSignedDeviceIdentity{
			Details: []byte("d"), AccountSignature: []byte("as"),
			AccountSignatureKey: []byte("ask"), DeviceSignature: []byte("ds"),
		},
	}
	d.SignedPreKey = d.IdentityKey.CreateSignedPreKey(1)
	return d
}

func TestDevice_RoundTrip(t *testing.T) {
	d := sampleDevice()
	b, err := encodeDevice(d)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	got, err := decodeDevice(b, waLog.Noop)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ID.String() != d.ID.String() || got.RegistrationID != d.RegistrationID {
		t.Fatalf("id/regid mismatch")
	}
	if *got.NoiseKey.Priv != *d.NoiseKey.Priv || *got.IdentityKey.Priv != *d.IdentityKey.Priv {
		t.Fatalf("key priv mismatch")
	}
	if got.SignedPreKey.KeyID != d.SignedPreKey.KeyID || *got.SignedPreKey.Signature != *d.SignedPreKey.Signature {
		t.Fatalf("signed prekey mismatch")
	}
	if got.PushName != "pn" || !bytes.Equal(got.Account.Details, []byte("d")) {
		t.Fatalf("scalar/account mismatch")
	}
}
