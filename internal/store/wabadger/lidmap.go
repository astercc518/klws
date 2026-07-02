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
	// Both directions must land together: a half-written mapping (lidpn set but
	// lidlid missing, or vice versa) is inconsistent, so use a single txn.
	return m.db.putBatch([][2][]byte{
		{kb("lidpn", lid.User), []byte(pn.String())},
		{kb("lidlid", pn.User), []byte(lid.String())},
	}, false)
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
		lid, err := m.GetLIDForPN(ctx, pn)
		if err != nil {
			// A real backend fault must propagate: whatsmeow's send path aborts
			// the send on it rather than silently treating the PN as unmapped.
			return nil, err
		}
		if !lid.IsEmpty() {
			out[pn] = lid
		}
	}
	return out, nil
}
