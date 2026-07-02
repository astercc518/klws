package wabadger

import (
	"context"

	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
)

// badgerStore implements store.AllSessionSpecificStores for a single device.
type badgerStore struct {
	db  *DB
	jid string
}

func newBadgerStore(db *DB, jid types.JID) *badgerStore {
	return &badgerStore{db: db, jid: jid.String()}
}

// compile-time proof the adapter satisfies every per-device store interface.
var _ store.AllSessionSpecificStores = (*badgerStore)(nil)

func (s *badgerStore) PutIdentity(ctx context.Context, address string, key [32]byte) error {
	return s.db.put(kb("idt", s.jid, address), key[:], true) // idt = sync
}

func (s *badgerStore) DeleteIdentity(ctx context.Context, address string) error {
	return s.db.del(kb("idt", s.jid, address))
}

func (s *badgerStore) DeleteAllIdentities(ctx context.Context, phone string) error {
	pfx := kp("idt", s.jid)
	var toDel [][]byte
	err := s.db.scanPrefix(pfx, func(k, _ []byte) error {
		addr := string(k[len(pfx):])
		if addr == phone || hasSignalUserPrefix(addr, phone) {
			toDel = append(toDel, k)
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

// hasSignalUserPrefix reports whether a signal address belongs to user `phone`
// (address form "<user>:<device>" or "<user>.<device>").
func hasSignalUserPrefix(addr, phone string) bool {
	return len(addr) > len(phone) && addr[:len(phone)] == phone &&
		(addr[len(phone)] == ':' || addr[len(phone)] == '.')
}

func (s *badgerStore) IsTrustedIdentity(ctx context.Context, address string, key [32]byte) (bool, error) {
	v, err := s.db.get(kb("idt", s.jid, address))
	if err != nil {
		return false, err
	}
	if v == nil {
		return true, nil // unknown → trusted (matches sqlstore)
	}
	if len(v) != 32 {
		return false, nil
	}
	return *(*[32]byte)(v) == key, nil
}
