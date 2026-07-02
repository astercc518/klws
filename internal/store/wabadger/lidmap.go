package wabadger

import (
	"context"

	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
)

// LIDMap implements store.LIDStore as a GLOBAL (container-level) mapping
// between a user's LID (hidden/linked identity) and their phone-number JID.
// Unlike the per-device stores in this package, keys here do not include a
// device jid segment: a LID<->PN mapping is a property of the WhatsApp
// account, not of any one device session.
type LIDMap struct{ db *DB }

func newLIDMap(db *DB) *LIDMap { return &LIDMap{db: db} }

var _ store.LIDStore = (*LIDMap)(nil)

func (m *LIDMap) PutLIDMapping(ctx context.Context, lid, pn types.JID) error {
	if err := m.db.put(kb("lidpn", lid.User), []byte(pn.String()), false); err != nil {
		return err
	}
	return m.db.put(kb("lidlid", pn.User), []byte(lid.String()), false)
}

func (m *LIDMap) PutManyLIDMappings(ctx context.Context, mappings []store.LIDMapping) error {
	for _, mp := range mappings {
		if err := m.PutLIDMapping(ctx, mp.LID, mp.PN); err != nil {
			return err
		}
	}
	return nil
}

func (m *LIDMap) GetPNForLID(ctx context.Context, lid types.JID) (types.JID, error) {
	v, err := m.db.get(kb("lidpn", lid.User))
	if err != nil || v == nil {
		return types.EmptyJID, err
	}
	return types.ParseJID(string(v))
}

func (m *LIDMap) GetLIDForPN(ctx context.Context, pn types.JID) (types.JID, error) {
	v, err := m.db.get(kb("lidlid", pn.User))
	if err != nil || v == nil {
		return types.EmptyJID, err
	}
	return types.ParseJID(string(v))
}

func (m *LIDMap) GetManyLIDsForPNs(ctx context.Context, pns []types.JID) (map[types.JID]types.JID, error) {
	out := map[types.JID]types.JID{}
	for _, pn := range pns {
		if lid, err := m.GetLIDForPN(ctx, pn); err == nil && !lid.IsEmpty() {
			out[pn] = lid
		}
	}
	return out, nil
}
