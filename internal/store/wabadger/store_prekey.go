package wabadger

import (
	"context"
	"encoding/binary"
	"sync"

	"go.mau.fi/whatsmeow/util/keys"
)

// preKeyLock serializes prekey-id allocation. It is a single global lock
// (not per-jid) because allocation is rare and cheap; correctness matters
// far more than throughput here.
var preKeyLock sync.Mutex

// preKeyVal encodes a prekey's private half plus its uploaded flag:
// priv[32] || uploaded(1 byte). The public key is re-derived on read via
// keys.NewKeyPairFromPrivateKey, so it's never stored.
func preKeyVal(k *keys.PreKey, uploaded bool) []byte {
	v := make([]byte, 33)
	copy(v[:32], k.Priv[:])
	if uploaded {
		v[32] = 1
	}
	return v
}

// idKey renders a prekey id as its big-endian byte encoding, so that
// scanning "pk\x00jid\x00" prefix yields ids in ascending numeric order.
func idKey(id uint32) string {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], id)
	return string(b[:])
}

// nextPreKeyID returns the next id to allocate: one past the last id ever
// handed out for this jid (0 if none yet). Callers must hold preKeyLock.
func (s *badgerStore) nextPreKeyID() (uint32, error) {
	v, err := s.db.get(kb("pkm", s.jid))
	if err != nil {
		return 0, err
	}
	var last uint32
	if len(v) == 4 {
		last = binary.BigEndian.Uint32(v)
	}
	return last + 1, nil
}

// setLastPreKeyID persists the highest id allocated so far. Durability
// matters here (sync=true): losing this write could hand out a
// already-used id again after a crash.
func (s *badgerStore) setLastPreKeyID(id uint32) error {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], id)
	return s.db.put(kb("pkm", s.jid), b[:], true)
}

// genOnePreKey creates a fresh keypair for id and persists it. prekeys are
// high-churn, so the write is async (sync=false); only the id counter needs
// durability.
func (s *badgerStore) genOnePreKey(id uint32, uploaded bool) (*keys.PreKey, error) {
	k := keys.NewPreKey(id)
	if err := s.db.put(kb("pk", s.jid, idKey(id)), preKeyVal(k, uploaded), false); err != nil {
		return nil, err
	}
	return k, nil
}

func (s *badgerStore) GenOnePreKey(ctx context.Context) (*keys.PreKey, error) {
	preKeyLock.Lock()
	defer preKeyLock.Unlock()
	id, err := s.nextPreKeyID()
	if err != nil {
		return nil, err
	}
	k, err := s.genOnePreKey(id, true)
	if err != nil {
		return nil, err
	}
	return k, s.setLastPreKeyID(id)
}

// idk pairs a decoded prekey with its id, for sorting scan results by id.
// Hoisted to package scope (rather than a generic over an anonymous struct
// literal, which Go's type inference cannot resolve at the call site).
type idk struct {
	id uint32
	k  *keys.PreKey
}

func (s *badgerStore) GetOrGenPreKeys(ctx context.Context, count uint32) ([]*keys.PreKey, error) {
	preKeyLock.Lock()
	defer preKeyLock.Unlock()

	// collect existing unuploaded keys, ordered by id.
	var existing []idk
	pfx := kp("pk", s.jid)
	err := s.db.scanPrefix(pfx, func(k, v []byte) error {
		if len(v) != 33 || v[32] == 1 {
			return nil // skip malformed or already-uploaded
		}
		id := binary.BigEndian.Uint32(k[len(pfx):])
		existing = append(existing, idk{id: id, k: &keys.PreKey{
			KeyPair: *keys.NewKeyPairFromPrivateKey(*(*[32]byte)(v[:32])),
			KeyID:   id,
		}})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sortByID(existing)

	out := make([]*keys.PreKey, 0, count)
	for _, e := range existing {
		if uint32(len(out)) >= count {
			break
		}
		out = append(out, e.k)
	}
	if uint32(len(out)) < count {
		id, err := s.nextPreKeyID()
		if err != nil {
			return nil, err
		}
		for uint32(len(out)) < count {
			k, err := s.genOnePreKey(id, false)
			if err != nil {
				return nil, err
			}
			out = append(out, k)
			id++
		}
		if err := s.setLastPreKeyID(id - 1); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// sortByID insertion-sorts by ascending id in place. n is small (a prekey
// batch is tens of keys), so an O(n^2) insertion sort is simplest and fine.
func sortByID(s []idk) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1].id > s[j].id; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}

func (s *badgerStore) GetPreKey(ctx context.Context, id uint32) (*keys.PreKey, error) {
	v, err := s.db.get(kb("pk", s.jid, idKey(id)))
	if err != nil || v == nil {
		return nil, err
	}
	if len(v) < 32 {
		return nil, nil
	}
	return &keys.PreKey{
		KeyPair: *keys.NewKeyPairFromPrivateKey(*(*[32]byte)(v[:32])),
		KeyID:   id,
	}, nil
}

func (s *badgerStore) RemovePreKey(ctx context.Context, id uint32) error {
	return s.db.del(kb("pk", s.jid, idKey(id)))
}

func (s *badgerStore) MarkPreKeysAsUploaded(ctx context.Context, upToID uint32) error {
	pfx := kp("pk", s.jid)
	type kv struct{ k, v []byte }
	var ups []kv
	err := s.db.scanPrefix(pfx, func(k, v []byte) error {
		if len(v) != 33 {
			return nil
		}
		id := binary.BigEndian.Uint32(k[len(pfx):])
		if id <= upToID && v[32] == 0 {
			nv := append([]byte(nil), v...)
			nv[32] = 1
			ups = append(ups, kv{k: append([]byte(nil), k...), v: nv})
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, u := range ups {
		if err := s.db.put(u.k, u.v, false); err != nil {
			return err
		}
	}
	return nil
}

func (s *badgerStore) UploadedPreKeyCount(ctx context.Context) (int, error) {
	n := 0
	err := s.db.scanPrefix(kp("pk", s.jid), func(_, v []byte) error {
		if len(v) == 33 && v[32] == 1 {
			n++
		}
		return nil
	})
	return n, err
}
