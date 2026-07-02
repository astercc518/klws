package wabadger

import (
	"context"

	"go.mau.fi/whatsmeow/types"
)

func (s *badgerStore) GetSession(ctx context.Context, address string) ([]byte, error) {
	return s.db.get(kb("ses", s.jid, address))
}

func (s *badgerStore) HasSession(ctx context.Context, address string) (bool, error) {
	v, err := s.db.get(kb("ses", s.jid, address))
	return v != nil, err
}

func (s *badgerStore) GetManySessions(ctx context.Context, addresses []string) (map[string][]byte, error) {
	if len(addresses) == 0 {
		return nil, nil
	}
	out := make(map[string][]byte, len(addresses))
	for _, a := range addresses {
		v, err := s.db.get(kb("ses", s.jid, a))
		if err != nil {
			return nil, err
		}
		if v != nil {
			out[a] = v
		}
	}
	return out, nil
}

func (s *badgerStore) PutSession(ctx context.Context, address string, session []byte) error {
	return s.db.put(kb("ses", s.jid, address), session, false) // high-churn → async
}

func (s *badgerStore) PutManySessions(ctx context.Context, sessions map[string][]byte) error {
	for a, sess := range sessions {
		if err := s.db.put(kb("ses", s.jid, a), sess, false); err != nil {
			return err
		}
	}
	return nil
}

func (s *badgerStore) DeleteSession(ctx context.Context, address string) error {
	return s.db.del(kb("ses", s.jid, address))
}

func (s *badgerStore) DeleteAllSessions(ctx context.Context, phone string) error {
	pfx := kp("ses", s.jid)
	var toDel [][]byte
	err := s.db.scanPrefix(pfx, func(k, _ []byte) error {
		if addr := string(k[len(pfx):]); addr == phone || hasSignalUserPrefix(addr, phone) {
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

// MigratePNToLID copies sessions AND identities whose address is under the phone
// JID's user to the corresponding LID user (mirrors sqlstore's PN→LID migration).
func (s *badgerStore) MigratePNToLID(ctx context.Context, pn, lid types.JID) error {
	pnUser, lidUser := pn.User, lid.User
	for _, code := range []string{"ses", "idt"} {
		pfx := kp(code, s.jid)
		type kv struct{ k, v []byte }
		var copies []kv
		err := s.db.scanPrefix(pfx, func(k, v []byte) error {
			addr := string(k[len(pfx):])
			if hasSignalUserPrefix(addr, pnUser) {
				newAddr := lidUser + addr[len(pnUser):]
				copies = append(copies, kv{k: kb(code, s.jid, newAddr), v: append([]byte(nil), v...)})
			}
			return nil
		})
		if err != nil {
			return err
		}
		for _, c := range copies {
			if err := s.db.put(c.k, c.v, false); err != nil {
				return err
			}
		}
	}
	return nil
}
