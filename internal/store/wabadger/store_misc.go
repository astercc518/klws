package wabadger

import (
	"context"
	"time"

	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
)

// --- ContactStore --- (values gob-encoded types.ContactInfo)

func (s *badgerStore) getContact(u types.JID) (types.ContactInfo, error) {
	var ci types.ContactInfo
	v, err := s.db.get(kb("ctc", s.jid, u.String()))
	if err != nil || v == nil {
		return ci, err
	}
	return ci, gobDec(v, &ci)
}

func (s *badgerStore) putContact(u types.JID, ci types.ContactInfo) error {
	return s.db.put(kb("ctc", s.jid, u.String()), gobEnc(ci), false)
}

// putContactDiff runs a read-modify-write on a contact record in a SINGLE
// badger transaction (db.getPut), matching sqlstore's contactCacheLock which
// serializes read-compare-write for the same purpose: two concurrent
// PutPushName calls on the same jid can't race between "read previous value"
// and "persist new value".
func (s *badgerStore) putContactDiff(u types.JID, fn func(ci *types.ContactInfo) (changed bool, prev string)) (bool, string, error) {
	var changed bool
	var prev string
	var decErr error
	err := s.db.getPut(kb("ctc", s.jid, u.String()), func(cur []byte) ([]byte, bool) {
		var ci types.ContactInfo
		if cur != nil {
			if decErr = gobDec(cur, &ci); decErr != nil {
				return nil, false
			}
		}
		changed, prev = fn(&ci)
		if !changed {
			return nil, false
		}
		return gobEnc(ci), true
	}, false)
	if decErr != nil {
		return false, "", decErr
	}
	if err != nil {
		return false, "", err
	}
	if !changed {
		// sqlstore returns ("") for the previous-value slot on a no-op write,
		// not the actual previous value (see PutPushName/PutBusinessName in
		// sqlstore/store.go: `return false, "", nil`).
		return false, "", nil
	}
	return changed, prev, nil
}

func (s *badgerStore) PutPushName(ctx context.Context, u types.JID, pushName string) (bool, string, error) {
	return s.putContactDiff(u, func(ci *types.ContactInfo) (bool, string) {
		prev := ci.PushName
		if prev == pushName {
			return false, prev
		}
		ci.PushName, ci.Found = pushName, true
		return true, prev
	})
}

func (s *badgerStore) PutBusinessName(ctx context.Context, u types.JID, businessName string) (bool, string, error) {
	return s.putContactDiff(u, func(ci *types.ContactInfo) (bool, string) {
		prev := ci.BusinessName
		if prev == businessName {
			return false, prev
		}
		ci.BusinessName, ci.Found = businessName, true
		return true, prev
	})
}

// PutContactName's positional args are (firstName, fullName): the interface
// declaration in $WM/store/store.go names them (fullName, firstName), but
// that's a naming slip upstream — the one real call site
// (appstate.go: `PutContactName(ctx, jid, act.GetFirstName(), act.GetFullName())`)
// and sqlstore's own implementation both treat arg 3 as firstName and arg 4
// as fullName. Go interface satisfaction only checks types, not parameter
// names, so this order is what actually matters.
func (s *badgerStore) PutContactName(ctx context.Context, u types.JID, firstName, fullName string) error {
	ci, err := s.getContact(u)
	if err != nil {
		return err
	}
	ci.FirstName, ci.FullName, ci.Found = firstName, fullName, true
	return s.putContact(u, ci)
}

// PutAllContactNames unconditionally overwrites first/full name for every
// entry (matching sqlstore's mass-insert upsert; no per-field diffing here).
func (s *badgerStore) PutAllContactNames(ctx context.Context, contacts []store.ContactEntry) error {
	for _, e := range contacts {
		ci, err := s.getContact(e.JID)
		if err != nil {
			return err
		}
		ci.FirstName, ci.FullName, ci.Found = e.FirstName, e.FullName, true
		if err := s.putContact(e.JID, ci); err != nil {
			return err
		}
	}
	return nil
}

// PutManyRedactedPhones persists RedactedPhone into the same per-jid contact
// record (sqlstore stores it as another column of whatsmeow_contacts, not a
// separate table) — an unconditional upsert, same as sqlstore.
func (s *badgerStore) PutManyRedactedPhones(ctx context.Context, entries []store.RedactedPhoneEntry) error {
	for _, e := range entries {
		ci, err := s.getContact(e.JID)
		if err != nil {
			return err
		}
		ci.RedactedPhone, ci.Found = e.RedactedPhone, true
		if err := s.putContact(e.JID, ci); err != nil {
			return err
		}
	}
	return nil
}

func (s *badgerStore) GetContact(ctx context.Context, u types.JID) (types.ContactInfo, error) {
	return s.getContact(u)
}

func (s *badgerStore) GetAllContacts(ctx context.Context) (map[types.JID]types.ContactInfo, error) {
	out := map[types.JID]types.ContactInfo{}
	pfx := kp("ctc", s.jid)
	err := s.db.scanPrefix(pfx, func(k, v []byte) error {
		j, perr := types.ParseJID(string(k[len(pfx):]))
		if perr != nil {
			return nil
		}
		var ci types.ContactInfo
		if err := gobDec(v, &ci); err != nil {
			return err
		}
		out[j] = ci
		return nil
	})
	return out, err
}

// --- ChatSettingsStore ---
// MutedUntil is stored as unix seconds, with -1 as a sentinel for
// store.MutedForever (mirroring sqlstore's putChatSettingQuery, which stores
// -1 rather than the literal (huge) Unix() value of the year-9999 sentinel;
// storing -1 lets GetChatSettings reconstruct the exact store.MutedForever
// value rather than a nanosecond-truncated approximation of it).
type chatSettingsRec struct {
	MutedUntil int64
	Pinned     bool
	Archived   bool
}

func (s *badgerStore) getChat(chat types.JID) (chatSettingsRec, bool, error) {
	var r chatSettingsRec
	v, err := s.db.get(kb("chs", s.jid, chat.String()))
	if err != nil {
		return r, false, err
	}
	if v == nil {
		return r, false, nil
	}
	if err := gobDec(v, &r); err != nil {
		return r, false, err
	}
	return r, true, nil
}

// putChatField runs a read-modify-write on a chat-settings record in a single
// badger transaction, so concurrent PutPinned/PutArchived/PutMutedUntil calls
// on the same chat (independent "columns" in sqlstore terms) can't lose one
// field to a lost-update race the way a plain get-then-put would.
func (s *badgerStore) putChatField(chat types.JID, mutate func(r *chatSettingsRec)) error {
	var decErr error
	err := s.db.getPut(kb("chs", s.jid, chat.String()), func(cur []byte) ([]byte, bool) {
		var r chatSettingsRec
		if cur != nil {
			if decErr = gobDec(cur, &r); decErr != nil {
				return nil, false
			}
		}
		mutate(&r)
		return gobEnc(r), true
	}, false)
	if decErr != nil {
		return decErr
	}
	return err
}

func (s *badgerStore) PutMutedUntil(ctx context.Context, chat types.JID, mutedUntil time.Time) error {
	return s.putChatField(chat, func(r *chatSettingsRec) {
		switch {
		case mutedUntil.Equal(store.MutedForever):
			r.MutedUntil = -1
		case !mutedUntil.IsZero():
			r.MutedUntil = mutedUntil.Unix()
		default:
			r.MutedUntil = 0
		}
	})
}

func (s *badgerStore) PutPinned(ctx context.Context, chat types.JID, pinned bool) error {
	return s.putChatField(chat, func(r *chatSettingsRec) { r.Pinned = pinned })
}

func (s *badgerStore) PutArchived(ctx context.Context, chat types.JID, archived bool) error {
	return s.putChatField(chat, func(r *chatSettingsRec) { r.Archived = archived })
}

func (s *badgerStore) GetChatSettings(ctx context.Context, chat types.JID) (types.LocalChatSettings, error) {
	r, found, err := s.getChat(chat)
	if err != nil {
		return types.LocalChatSettings{}, err
	}
	out := types.LocalChatSettings{Found: found, Pinned: r.Pinned, Archived: r.Archived}
	switch {
	case r.MutedUntil < 0:
		out.MutedUntil = store.MutedForever
	case r.MutedUntil > 0:
		out.MutedUntil = time.Unix(r.MutedUntil, 0)
	}
	return out, nil
}

// --- MsgSecretStore ---
// Keys are normalized with ToNonAD() (drop the device suffix) before storing
// or looking up, matching sqlstore's insert.Chat.ToNonAD()/insert.Sender.ToNonAD().
//
// KNOWN GAP vs sqlstore: sqlstore's getMsgSecret query also matches the
// LID<->PN alternate address for both chat and sender (via a join against
// whatsmeow_lid_map) when the exact address misses. This adapter does a
// single exact-key lookup only; see the task report for why that's an
// accepted gap for M1 (no group-messaging send path yet).

func (s *badgerStore) putMessageSecret(chat, sender types.JID, id types.MessageID, secret []byte) error {
	chat, sender = chat.ToNonAD(), sender.ToNonAD()
	// ON CONFLICT DO NOTHING semantics: a secret already stored for this
	// (chat,sender,id) is immutable, so a duplicate write is a silent no-op.
	return s.db.getPut(kb("msec", s.jid, chat.String(), sender.String(), string(id)), func(cur []byte) ([]byte, bool) {
		if cur != nil {
			return nil, false
		}
		return secret, true
	}, false)
}

func (s *badgerStore) PutMessageSecrets(ctx context.Context, inserts []store.MessageSecretInsert) error {
	for _, in := range inserts {
		if err := s.putMessageSecret(in.Chat, in.Sender, in.ID, in.Secret); err != nil {
			return err
		}
	}
	return nil
}

func (s *badgerStore) PutMessageSecret(ctx context.Context, chat, sender types.JID, id types.MessageID, secret []byte) error {
	return s.putMessageSecret(chat, sender, id, secret)
}

func (s *badgerStore) GetMessageSecret(ctx context.Context, chat, sender types.JID, id types.MessageID) ([]byte, types.JID, error) {
	chat, sender = chat.ToNonAD(), sender.ToNonAD()
	v, err := s.db.get(kb("msec", s.jid, chat.String(), sender.String(), string(id)))
	if err != nil {
		return nil, types.JID{}, err
	}
	if v == nil {
		return nil, types.JID{}, nil
	}
	return v, sender, nil
}

// --- PrivacyTokenStore ---
// Mirrors sqlstore's conditional upsert:
//   ON CONFLICT ... DO UPDATE SET token=excluded.token, timestamp=excluded.timestamp,
//     sender_timestamp=COALESCE(excluded.sender_timestamp, existing.sender_timestamp)
//   WHERE excluded.timestamp >= existing.timestamp
// i.e. a write with an older Timestamp than what's stored is dropped, and a
// write with a zero SenderTimestamp keeps whatever SenderTimestamp was
// already stored rather than clobbering it with zero.
type privacyTokenRec struct {
	Token           []byte
	Timestamp       int64
	SenderTimestamp int64
}

func (s *badgerStore) PutPrivacyTokens(ctx context.Context, tokens ...store.PrivacyToken) error {
	for _, t := range tokens {
		user := t.User.ToNonAD()
		var decErr error
		err := s.db.getPut(kb("ptk", s.jid, user.String()), func(cur []byte) ([]byte, bool) {
			rec := privacyTokenRec{Token: t.Token, Timestamp: t.Timestamp.Unix()}
			if !t.SenderTimestamp.IsZero() {
				rec.SenderTimestamp = t.SenderTimestamp.Unix()
			}
			if cur != nil {
				var old privacyTokenRec
				if decErr = gobDec(cur, &old); decErr != nil {
					return nil, false
				}
				if rec.Timestamp < old.Timestamp {
					return nil, false // stale write: existing timestamp wins
				}
				if t.SenderTimestamp.IsZero() {
					rec.SenderTimestamp = old.SenderTimestamp // COALESCE
				}
			}
			return gobEnc(rec), true
		}, false)
		if decErr != nil {
			return decErr
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *badgerStore) GetPrivacyToken(ctx context.Context, user types.JID) (*store.PrivacyToken, error) {
	user = user.ToNonAD()
	v, err := s.db.get(kb("ptk", s.jid, user.String()))
	if err != nil || v == nil {
		return nil, err
	}
	var rec privacyTokenRec
	if err := gobDec(v, &rec); err != nil {
		return nil, err
	}
	tok := &store.PrivacyToken{User: user, Token: rec.Token, Timestamp: time.Unix(rec.Timestamp, 0)}
	if rec.SenderTimestamp != 0 {
		tok.SenderTimestamp = time.Unix(rec.SenderTimestamp, 0)
	}
	return tok, nil
}

func (s *badgerStore) DeleteExpiredPrivacyTokens(ctx context.Context, cutoff time.Time) (int64, error) {
	var n int64
	pfx := kp("ptk", s.jid)
	var toDel [][]byte
	err := s.db.scanPrefix(pfx, func(k, v []byte) error {
		var rec privacyTokenRec
		if gobDec(v, &rec) == nil && time.Unix(rec.Timestamp, 0).Before(cutoff) {
			toDel = append(toDel, append([]byte(nil), k...))
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	for _, k := range toDel {
		if err := s.db.del(k); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// --- NCTSaltStore ---
// The NCT salt seeds phone-number-privacy tokens; losing it forces
// re-derivation server-side, so writes are durable (sync=true), same as the
// identity/app-state-sync-key stores.
func (s *badgerStore) PutNCTSalt(ctx context.Context, salt []byte) error {
	return s.db.put(kb("nct", s.jid), salt, true)
}

func (s *badgerStore) GetNCTSalt(ctx context.Context) ([]byte, error) { return s.db.get(kb("nct", s.jid)) }

func (s *badgerStore) DeleteNCTSalt(ctx context.Context) error { return s.db.del(kb("nct", s.jid)) }

// --- EventBuffer ---
// Two independent record kinds share this section: buffered inbound events
// (dedup + at-least-once decrypt retry, keyed by ciphertext hash) and
// outgoing "retry buffer" events (keyed by chat+id, used to answer retry
// receipts). Both track their own insert/write timestamp so DeleteOld* can
// age entries out, matching sqlstore's `insert_timestamp`/`timestamp` columns.

type bufferedEventRec struct {
	Plaintext  []byte
	InsertTime int64 // unix millis
	ServerTime int64 // unix seconds
}

func (s *badgerStore) GetBufferedEvent(ctx context.Context, ciphertextHash [32]byte) (*store.BufferedEvent, error) {
	v, err := s.db.get(kb("evb", s.jid, string(ciphertextHash[:])))
	if err != nil || v == nil {
		return nil, err
	}
	var rec bufferedEventRec
	if err := gobDec(v, &rec); err != nil {
		return nil, err
	}
	return &store.BufferedEvent{
		Plaintext:  rec.Plaintext,
		InsertTime: time.UnixMilli(rec.InsertTime),
		ServerTime: time.Unix(rec.ServerTime, 0),
	}, nil
}

func (s *badgerStore) PutBufferedEvent(ctx context.Context, ciphertextHash [32]byte, plaintext []byte, serverTimestamp time.Time) error {
	rec := bufferedEventRec{Plaintext: plaintext, InsertTime: time.Now().UnixMilli(), ServerTime: serverTimestamp.Unix()}
	return s.db.put(kb("evb", s.jid, string(ciphertextHash[:])), gobEnc(rec), false)
}

// DoDecryptionTxn is a coarse pass-through: badger's per-call operations are
// each individually atomic, but this does not wrap the whole callback in one
// enclosing badger transaction the way sqlstore wraps it in one SQL
// transaction (s.db.DoTxn). Acceptable for M1: the only caller
// (message.go's decryptMessages) uses this to make "buffer the event" +
// "process it" atomic against a crash, and re-processing an idempotent
// decrypt on retry is safe; see the task report for the full tradeoff.
func (s *badgerStore) DoDecryptionTxn(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

func (s *badgerStore) ClearBufferedEventPlaintext(ctx context.Context, ciphertextHash [32]byte) error {
	k := kb("evb", s.jid, string(ciphertextHash[:]))
	var decErr error
	err := s.db.getPut(k, func(cur []byte) ([]byte, bool) {
		if cur == nil {
			return nil, false
		}
		var rec bufferedEventRec
		if decErr = gobDec(cur, &rec); decErr != nil {
			return nil, false
		}
		rec.Plaintext = nil
		return gobEnc(rec), true
	}, false)
	if decErr != nil {
		return decErr
	}
	return err
}

// DeleteOldBufferedHashes mirrors sqlstore's 14-day retention (the WhatsApp
// servers only buffer/retry events for 14 days).
func (s *badgerStore) DeleteOldBufferedHashes(ctx context.Context) error {
	cutoff := time.Now().Add(-14 * 24 * time.Hour).UnixMilli()
	pfx := kp("evb", s.jid)
	var toDel [][]byte
	err := s.db.scanPrefix(pfx, func(k, v []byte) error {
		var rec bufferedEventRec
		if gobDec(v, &rec) == nil && rec.InsertTime < cutoff {
			toDel = append(toDel, append([]byte(nil), k...))
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, k := range toDel {
		if err := s.db.del(k); err != nil {
			return err
		}
	}
	return nil
}

type outgoingEventRec struct {
	Format    string
	Plaintext []byte
	Timestamp int64 // unix millis, set at write time; ages entries out
}

// GetOutgoingEvent tries chatJID first, then altChatJID (the LID<->PN
// counterpart), mirroring sqlstore's `WHERE chat_jid=$2 OR chat_jid=$3`.
func (s *badgerStore) GetOutgoingEvent(ctx context.Context, chatJID, altChatJID types.JID, id types.MessageID) (string, []byte, error) {
	v, err := s.db.get(kb("oge", s.jid, chatJID.String(), string(id)))
	if err != nil {
		return "", nil, err
	}
	if v == nil && !altChatJID.IsEmpty() {
		v, err = s.db.get(kb("oge", s.jid, altChatJID.String(), string(id)))
		if err != nil {
			return "", nil, err
		}
	}
	if v == nil {
		return "", nil, nil
	}
	var rec outgoingEventRec
	if err := gobDec(v, &rec); err != nil {
		return "", nil, err
	}
	return rec.Format, rec.Plaintext, nil
}

func (s *badgerStore) AddOutgoingEvent(ctx context.Context, chatJID types.JID, id types.MessageID, format string, plaintext []byte) error {
	rec := outgoingEventRec{Format: format, Plaintext: plaintext, Timestamp: time.Now().UnixMilli()}
	return s.db.put(kb("oge", s.jid, chatJID.String(), string(id)), gobEnc(rec), false)
}

// DeleteOldOutgoingEvents mirrors sqlstore's 7-day retention.
func (s *badgerStore) DeleteOldOutgoingEvents(ctx context.Context) error {
	cutoff := time.Now().Add(-7 * 24 * time.Hour).UnixMilli()
	pfx := kp("oge", s.jid)
	var toDel [][]byte
	err := s.db.scanPrefix(pfx, func(k, v []byte) error {
		var rec outgoingEventRec
		if gobDec(v, &rec) == nil && rec.Timestamp < cutoff {
			toDel = append(toDel, append([]byte(nil), k...))
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, k := range toDel {
		if err := s.db.del(k); err != nil {
			return err
		}
	}
	return nil
}
