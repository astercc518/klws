package wabadger

import (
	"context"
	"testing"

	"go.mau.fi/whatsmeow/types"
)

func TestLIDMap_Bidirectional(t *testing.T) {
	ctx := context.Background()
	m := newLIDMap(openTestDB(t))
	lid := types.NewJID("111", types.HiddenUserServer)
	pn := types.NewJID("222", types.DefaultUserServer)
	if err := m.PutLIDMapping(ctx, lid, pn); err != nil {
		t.Fatalf("PutLIDMapping: %v", err)
	}
	if got, _ := m.GetPNForLID(ctx, lid); got.User != pn.User {
		t.Fatalf("GetPNForLID = %s; want %s", got, pn)
	}
	if got, _ := m.GetLIDForPN(ctx, pn); got.User != lid.User {
		t.Fatalf("GetLIDForPN = %s; want %s", got, lid)
	}
}
