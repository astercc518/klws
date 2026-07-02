package wabadger

import (
	"bytes"
	"context"
	"encoding/gob"

	"go.mau.fi/whatsmeow/store"
)

// gobEnc/gobDec are shared helpers for the handful of app-state record types
// below that are small, internal-only structs (never exposed over the wire),
// so gob's simplicity beats hand-rolled binary encoding.
func gobEnc(v any) []byte {
	var buf bytes.Buffer
	_ = gob.NewEncoder(&buf).Encode(v) // encoding a concrete struct into a bytes.Buffer cannot fail
	return buf.Bytes()
}

func gobDec(b []byte, v any) error { return gob.NewDecoder(bytes.NewReader(b)).Decode(v) }

// --- SenderKeyStore ---
// Sender keys are group-messaging session state; high-churn like 1:1 sessions,
// so writes are async (sync=false). See store_session.go's MigratePNToLID
// TODO(groups-milestone) — sender keys aren't migrated on PN→LID yet because
// M1 has no group-messaging send path.

func (s *badgerStore) PutSenderKey(ctx context.Context, group, user string, session []byte) error {
	return s.db.put(kb("sk", s.jid, group, user), session, false)
}

func (s *badgerStore) GetSenderKey(ctx context.Context, group, user string) ([]byte, error) {
	return s.db.get(kb("sk", s.jid, group, user))
}

// --- AppStateStore (version + mutation MACs) ---

type appStateVersionRec struct {
	Version uint64
	Hash    [128]byte
}

func (s *badgerStore) PutAppStateVersion(ctx context.Context, name string, version uint64, hash [128]byte) error {
	return s.db.put(kb("asv", s.jid, name), gobEnc(appStateVersionRec{Version: version, Hash: hash}), false)
}

func (s *badgerStore) GetAppStateVersion(ctx context.Context, name string) (uint64, [128]byte, error) {
	v, err := s.db.get(kb("asv", s.jid, name))
	if err != nil || v == nil {
		return 0, [128]byte{}, err
	}
	var rec appStateVersionRec
	if err := gobDec(v, &rec); err != nil {
		return 0, [128]byte{}, err
	}
	return rec.Version, rec.Hash, nil
}

func (s *badgerStore) DeleteAppStateVersion(ctx context.Context, name string) error {
	if err := s.db.del(kb("asv", s.jid, name)); err != nil {
		return err
	}
	return s.db.dropPrefix(kp("asm", s.jid, name)) // drop this name's mutation MACs too, mirroring sqlstore's cascading FK delete
}

func (s *badgerStore) PutAppStateMutationMACs(ctx context.Context, name string, version uint64, mutations []store.AppStateMutationMAC) error {
	for _, m := range mutations {
		if err := s.db.put(kb("asm", s.jid, name, string(m.IndexMAC)), m.ValueMAC, false); err != nil {
			return err
		}
	}
	return nil
}

func (s *badgerStore) DeleteAppStateMutationMACs(ctx context.Context, name string, indexMACs [][]byte) error {
	for _, im := range indexMACs {
		if err := s.db.del(kb("asm", s.jid, name, string(im))); err != nil {
			return err
		}
	}
	return nil
}

func (s *badgerStore) GetAppStateMutationMAC(ctx context.Context, name string, indexMAC []byte) ([]byte, error) {
	return s.db.get(kb("asm", s.jid, name, string(indexMAC)))
}

// --- AppStateSyncKeyStore ---
// Sync keys are auth-critical (losing one can force a re-login/history-loss),
// so writes are durable (sync=true).

// getSyncKey is the decode path for GetAppStateSyncKey.
func (s *badgerStore) getSyncKey(id []byte) (*store.AppStateSyncKey, error) {
	v, err := s.db.get(kb("ask", s.jid, string(id)))
	if err != nil || v == nil {
		return nil, err
	}
	var k store.AppStateSyncKey
	if err := gobDec(v, &k); err != nil {
		return nil, err
	}
	return &k, nil
}

// PutAppStateSyncKey mirrors sqlstore's upsert: an existing key_id's record is
// only overwritten when the incoming Timestamp is strictly greater than what's
// stored (sqlstore: `ON CONFLICT ... DO UPDATE ... WHERE excluded.timestamp >
// whatsmeow_app_state_sync_keys.timestamp`); a stale/duplicate write is a
// silent no-op rather than clobbering a newer key.
//
// The read-compare-write runs in a single badger transaction (getPut) so the
// stale-skip is atomic: concurrent Puts for the same id can't let an
// older-timestamp write win a lost-update race, matching sqlstore's atomic
// conditional ON CONFLICT.
func (s *badgerStore) PutAppStateSyncKey(ctx context.Context, id []byte, key store.AppStateSyncKey) error {
	return s.db.getPut(kb("ask", s.jid, string(id)), func(cur []byte) ([]byte, bool) {
		if cur != nil {
			var existing store.AppStateSyncKey
			if err := gobDec(cur, &existing); err == nil && existing.Timestamp > key.Timestamp {
				return nil, false // stored key is newer; skip
			}
		}
		return gobEnc(key), true
	}, true)
}

func (s *badgerStore) GetAppStateSyncKey(ctx context.Context, id []byte) (*store.AppStateSyncKey, error) {
	return s.getSyncKey(id)
}

// GetLatestAppStateSyncKeyID scans all sync keys and returns the id with the
// highest Timestamp, matching sqlstore's `ORDER BY timestamp DESC LIMIT 1`
// (computed on read, not cached in a separate pointer key — there's no
// deletion path for sync keys, but recomputing avoids a second piece of state
// that could drift from the ask\x00 records it's supposed to summarize).
func (s *badgerStore) GetLatestAppStateSyncKeyID(ctx context.Context) ([]byte, error) {
	pfx := kp("ask", s.jid)
	var bestID []byte
	var bestTS int64
	err := s.db.scanPrefix(pfx, func(k, v []byte) error {
		var key store.AppStateSyncKey
		if err := gobDec(v, &key); err != nil {
			return err
		}
		// strictly-greater keeps the first-scanned id on a timestamp tie; the
		// tie-break is non-contractual (sqlstore's ORDER BY timestamp DESC has
		// no secondary key either, so ties are arbitrary there too).
		if bestID == nil || key.Timestamp > bestTS {
			bestTS = key.Timestamp
			bestID = append([]byte(nil), k[len(pfx):]...)
		}
		return nil
	})
	return bestID, err
}

func (s *badgerStore) GetAllAppStateSyncKeys(ctx context.Context) ([]*store.AppStateSyncKey, error) {
	var out []*store.AppStateSyncKey
	err := s.db.scanPrefix(kp("ask", s.jid), func(_, v []byte) error {
		var k store.AppStateSyncKey
		if err := gobDec(v, &k); err != nil {
			return err
		}
		out = append(out, &k)
		return nil
	})
	return out, err
}
