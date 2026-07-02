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
	// Return one entry per requested address (nil for misses): whatsmeow's
	// session cache / existingSessions map relies on every queried address
	// being present in the result map, matching sqlstore's behaviour.
	out := make(map[string][]byte, len(addresses))
	for _, a := range addresses {
		out[a] = nil
	}
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

// MigratePNToLID moves sessions AND identities whose address is under the phone
// JID's signal user to the corresponding LID signal user (mirrors sqlstore's
// PN→LID migration: copy each entry forward, then delete the original). The
// whole copy+delete is committed in a single badger transaction so the
// migration is all-or-nothing.
//
// pn.SignalAddressUser()/lid.SignalAddressUser() (not the raw .User) are used
// for both prefix-matching and the address rewrite, matching sqlstore — for a
// LID the signal user carries an "_<agent>" suffix that the raw .User lacks.
func (s *badgerStore) MigratePNToLID(ctx context.Context, pn, lid types.JID) error {
	pnUser := pn.SignalAddressUser()
	lidUser := lid.SignalAddressUser()
	var sets [][2][]byte
	var dels [][]byte
	// TODO(groups-milestone): if group messaging is added, extend
	// MigratePNToLID to also migrate sender keys ("sk") like sqlstore does.
	for _, code := range []string{"ses", "idt"} {
		pfx := kp(code, s.jid)
		err := s.db.scanPrefix(pfx, func(k, v []byte) error {
			addr := string(k[len(pfx):])
			if hasSignalUserPrefix(addr, pnUser) {
				newAddr := lidUser + addr[len(pnUser):]
				sets = append(sets, [2][]byte{kb(code, s.jid, newAddr), append([]byte(nil), v...)})
				dels = append(dels, append([]byte(nil), k...))
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	if len(sets) == 0 && len(dels) == 0 {
		return nil
	}
	return s.db.writeBatch(sets, dels, false)
}
