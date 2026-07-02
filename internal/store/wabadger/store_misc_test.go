package wabadger

import (
	"bytes"
	"context"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
)

func TestMisc_ContactChatNCTSecrets(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	u := types.NewJID("15550000009", types.DefaultUserServer)

	if _, _, err := s.PutPushName(ctx, u, "Alice"); err != nil {
		t.Fatalf("PutPushName: %v", err)
	}
	ci, err := s.GetContact(ctx, u)
	if err != nil || ci.PushName != "Alice" {
		t.Fatalf("GetContact = %+v,%v", ci, err)
	}
	_ = s.PutNCTSalt(ctx, []byte("salt"))
	if v, _ := s.GetNCTSalt(ctx); !bytes.Equal(v, []byte("salt")) {
		t.Fatalf("NCT salt round-trip failed")
	}
	if err := s.DeleteNCTSalt(ctx); err != nil {
		t.Fatalf("DeleteNCTSalt: %v", err)
	}
	if v, err := s.GetNCTSalt(ctx); err != nil || v != nil {
		t.Fatalf("NCT salt after delete = %v,%v; want nil,nil", v, err)
	}
}

// TestMisc_ContactPushBusinessNameDiffSemantics verifies the (changed, prev)
// return contract: unchanged writes report changed=false, and the previous
// value is snapshotted before the write lands, matching sqlstore.
func TestMisc_ContactPushBusinessNameDiffSemantics(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	u := types.NewJID("15550000010", types.DefaultUserServer)

	changed, prev, err := s.PutPushName(ctx, u, "Alice")
	if err != nil || !changed || prev != "" {
		t.Fatalf("first PutPushName = %v,%q,%v; want true,\"\",nil", changed, prev, err)
	}
	changed, prev, err = s.PutPushName(ctx, u, "Alice")
	if err != nil || changed || prev != "" {
		t.Fatalf("repeat PutPushName = %v,%q,%v; want false,\"\" (sqlstore returns empty prev on no-op)", changed, prev, err)
	}
	changed, prev, err = s.PutPushName(ctx, u, "Alicia")
	if err != nil || !changed || prev != "Alice" {
		t.Fatalf("changed PutPushName = %v,%q,%v; want true,\"Alice\",nil", changed, prev, err)
	}

	changed, prev, err = s.PutBusinessName(ctx, u, "Acme")
	if err != nil || !changed || prev != "" {
		t.Fatalf("PutBusinessName = %v,%q,%v", changed, prev, err)
	}
	ci, err := s.GetContact(ctx, u)
	if err != nil || !ci.Found || ci.PushName != "Alicia" || ci.BusinessName != "Acme" {
		t.Fatalf("GetContact after diffs = %+v,%v", ci, err)
	}
}

func TestMisc_ContactNamesAndRedactedPhones(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	u1 := types.NewJID("15550000011", types.DefaultUserServer)
	u2 := types.NewJID("15550000012", types.DefaultUserServer)

	if err := s.PutContactName(ctx, u1, "Bob", "Bob Builder"); err != nil {
		t.Fatalf("PutContactName: %v", err)
	}
	ci, _ := s.GetContact(ctx, u1)
	if ci.FirstName != "Bob" || ci.FullName != "Bob Builder" {
		t.Fatalf("PutContactName mismatch: %+v", ci)
	}

	if err := s.PutAllContactNames(ctx, []store.ContactEntry{
		{JID: u2, FirstName: "Carl", FullName: "Carl Carlson"},
	}); err != nil {
		t.Fatalf("PutAllContactNames: %v", err)
	}
	all, err := s.GetAllContacts(ctx)
	if err != nil || len(all) != 2 {
		t.Fatalf("GetAllContacts = %v,%v; want 2 entries", all, err)
	}
	if all[u2].FirstName != "Carl" {
		t.Fatalf("GetAllContacts[u2] = %+v", all[u2])
	}

	if err := s.PutManyRedactedPhones(ctx, []store.RedactedPhoneEntry{
		{JID: u1, RedactedPhone: "+1∙∙∙∙∙80"},
	}); err != nil {
		t.Fatalf("PutManyRedactedPhones: %v", err)
	}
	ci, _ = s.GetContact(ctx, u1)
	if ci.RedactedPhone != "+1∙∙∙∙∙80" {
		t.Fatalf("redacted phone not persisted: %+v", ci)
	}
	// unrelated fields on u1 must survive the redacted-phone-only write.
	if ci.FirstName != "Bob" {
		t.Fatalf("PutManyRedactedPhones clobbered unrelated field: %+v", ci)
	}
}

func TestMisc_ChatSettings(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	chat := types.NewJID("grp1", "g.us")

	if got, _ := s.GetChatSettings(ctx, chat); got.Found {
		t.Fatalf("unknown chat must report Found=false, got %+v", got)
	}
	if err := s.PutPinned(ctx, chat, true); err != nil {
		t.Fatalf("PutPinned: %v", err)
	}
	if err := s.PutArchived(ctx, chat, true); err != nil {
		t.Fatalf("PutArchived: %v", err)
	}
	if err := s.PutMutedUntil(ctx, chat, store.MutedForever); err != nil {
		t.Fatalf("PutMutedUntil: %v", err)
	}
	got, err := s.GetChatSettings(ctx, chat)
	if err != nil || !got.Found || !got.Pinned || !got.Archived {
		t.Fatalf("GetChatSettings = %+v,%v", got, err)
	}
	if !got.MutedUntil.Equal(store.MutedForever) {
		t.Fatalf("MutedUntil = %v; want store.MutedForever sentinel round-trip", got.MutedUntil)
	}
}

// TestMisc_MessageSecretWriteOnce verifies PutMessageSecret/PutMessageSecrets
// mirror sqlstore's ON CONFLICT DO NOTHING: a secret already stored for a
// given (chat,sender,id) is immutable.
func TestMisc_MessageSecretWriteOnce(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	chat := types.NewJID("15550000020", types.DefaultUserServer)
	sender := types.NewJID("15550000021", types.DefaultUserServer)

	if err := s.PutMessageSecret(ctx, chat, sender, "msg1", []byte("secret-v1")); err != nil {
		t.Fatalf("PutMessageSecret: %v", err)
	}
	if err := s.PutMessageSecret(ctx, chat, sender, "msg1", []byte("secret-v2")); err != nil {
		t.Fatalf("PutMessageSecret (dup): %v", err)
	}
	got, gotSender, err := s.GetMessageSecret(ctx, chat, sender, "msg1")
	if err != nil || !bytes.Equal(got, []byte("secret-v1")) {
		t.Fatalf("GetMessageSecret = %q,%v; want secret-v1 (first write wins)", got, err)
	}
	if gotSender.String() != sender.ToNonAD().String() {
		t.Fatalf("GetMessageSecret sender = %v; want %v", gotSender, sender.ToNonAD())
	}

	if v, sndr, err := s.GetMessageSecret(ctx, chat, sender, "missing"); err != nil || v != nil || !sndr.IsEmpty() {
		t.Fatalf("miss = %v,%v,%v; want nil,empty,nil", v, sndr, err)
	}

	if err := s.PutMessageSecrets(ctx, []store.MessageSecretInsert{
		{Chat: chat, Sender: sender, ID: "msg2", Secret: []byte("s2")},
	}); err != nil {
		t.Fatalf("PutMessageSecrets: %v", err)
	}
	got, _, err = s.GetMessageSecret(ctx, chat, sender, "msg2")
	if err != nil || !bytes.Equal(got, []byte("s2")) {
		t.Fatalf("PutMessageSecrets round-trip = %q,%v", got, err)
	}
}

// TestMisc_PrivacyTokens verifies the stale-timestamp skip and
// SenderTimestamp-COALESCE semantics from sqlstore's conditional upsert.
func TestMisc_PrivacyTokens(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	user := types.NewJID("15550000030", types.DefaultUserServer)

	ts1 := time.Unix(1000, 0)
	sts1 := time.Unix(500, 0)
	if err := s.PutPrivacyTokens(ctx, store.PrivacyToken{User: user, Token: []byte("t1"), Timestamp: ts1, SenderTimestamp: sts1}); err != nil {
		t.Fatalf("PutPrivacyTokens: %v", err)
	}
	got, err := s.GetPrivacyToken(ctx, user)
	if err != nil || got == nil || !bytes.Equal(got.Token, []byte("t1")) || got.Timestamp.Unix() != 1000 || got.SenderTimestamp.Unix() != 500 {
		t.Fatalf("GetPrivacyToken after first put = %+v,%v", got, err)
	}

	// stale write (older timestamp) must be a no-op.
	staleTS := time.Unix(100, 0)
	if err := s.PutPrivacyTokens(ctx, store.PrivacyToken{User: user, Token: []byte("stale"), Timestamp: staleTS}); err != nil {
		t.Fatalf("PutPrivacyTokens (stale): %v", err)
	}
	got, _ = s.GetPrivacyToken(ctx, user)
	if !bytes.Equal(got.Token, []byte("t1")) {
		t.Fatalf("stale write clobbered token: %+v", got)
	}

	// newer write with zero SenderTimestamp must COALESCE (keep old sender ts).
	ts2 := time.Unix(2000, 0)
	if err := s.PutPrivacyTokens(ctx, store.PrivacyToken{User: user, Token: []byte("t2"), Timestamp: ts2}); err != nil {
		t.Fatalf("PutPrivacyTokens (newer): %v", err)
	}
	got, _ = s.GetPrivacyToken(ctx, user)
	if !bytes.Equal(got.Token, []byte("t2")) || got.Timestamp.Unix() != 2000 || got.SenderTimestamp.Unix() != 500 {
		t.Fatalf("coalesce semantics broken: %+v", got)
	}

	if got, err := s.GetPrivacyToken(ctx, types.NewJID("nobody", types.DefaultUserServer)); err != nil || got != nil {
		t.Fatalf("miss = %v,%v; want nil,nil", got, err)
	}

	n, err := s.DeleteExpiredPrivacyTokens(ctx, time.Unix(3000, 0))
	if err != nil || n != 1 {
		t.Fatalf("DeleteExpiredPrivacyTokens = %d,%v; want 1,nil", n, err)
	}
	if got, _ := s.GetPrivacyToken(ctx, user); got != nil {
		t.Fatalf("token survived expiry delete: %+v", got)
	}
}

func TestMisc_EventBuffer(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	var hash [32]byte
	hash[0] = 7

	if got, err := s.GetBufferedEvent(ctx, hash); err != nil || got != nil {
		t.Fatalf("miss = %v,%v; want nil,nil", got, err)
	}
	serverTS := time.Unix(1_700_000_000, 0)
	if err := s.PutBufferedEvent(ctx, hash, []byte("plaintext"), serverTS); err != nil {
		t.Fatalf("PutBufferedEvent: %v", err)
	}
	got, err := s.GetBufferedEvent(ctx, hash)
	if err != nil || got == nil || !bytes.Equal(got.Plaintext, []byte("plaintext")) || !got.ServerTime.Equal(serverTS) {
		t.Fatalf("GetBufferedEvent = %+v,%v", got, err)
	}

	var txnRan bool
	if err := s.DoDecryptionTxn(ctx, func(txnCtx context.Context) error {
		txnRan = true
		return s.PutBufferedEvent(txnCtx, hash, []byte("updated"), serverTS)
	}); err != nil || !txnRan {
		t.Fatalf("DoDecryptionTxn = %v; ran=%v", err, txnRan)
	}

	if err := s.ClearBufferedEventPlaintext(ctx, hash); err != nil {
		t.Fatalf("ClearBufferedEventPlaintext: %v", err)
	}
	got, err = s.GetBufferedEvent(ctx, hash)
	if err != nil || got == nil || got.Plaintext != nil {
		t.Fatalf("plaintext not cleared: %+v,%v", got, err)
	}

	if err := s.DeleteOldBufferedHashes(ctx); err != nil {
		t.Fatalf("DeleteOldBufferedHashes: %v", err)
	}
}

func TestMisc_OutgoingEvents(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	chat := types.NewJID("15550000040", types.DefaultUserServer)
	altChat := types.NewJID("deadbeef", types.HiddenUserServer)

	if err := s.AddOutgoingEvent(ctx, chat, "m1", "wa", []byte("payload")); err != nil {
		t.Fatalf("AddOutgoingEvent: %v", err)
	}
	format, payload, err := s.GetOutgoingEvent(ctx, chat, altChat, "m1")
	if err != nil || format != "wa" || !bytes.Equal(payload, []byte("payload")) {
		t.Fatalf("GetOutgoingEvent = %q,%q,%v", format, payload, err)
	}
	// lookup via the alternate (LID/PN) chat JID must also resolve.
	format, payload, err = s.GetOutgoingEvent(ctx, types.NewJID("nonexistent", types.DefaultUserServer), chat, "m1")
	if err != nil || format != "wa" || !bytes.Equal(payload, []byte("payload")) {
		t.Fatalf("GetOutgoingEvent via altChatJID = %q,%q,%v", format, payload, err)
	}

	if format, payload, err := s.GetOutgoingEvent(ctx, chat, altChat, "missing"); err != nil || format != "" || payload != nil {
		t.Fatalf("miss = %q,%q,%v; want empty,nil,nil", format, payload, err)
	}

	if err := s.DeleteOldOutgoingEvents(ctx); err != nil {
		t.Fatalf("DeleteOldOutgoingEvents: %v", err)
	}
}
