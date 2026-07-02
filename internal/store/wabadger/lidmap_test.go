package wabadger

import (
	"context"
	"testing"

	"go.mau.fi/whatsmeow/store"
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

func TestLIDMap_PutMany(t *testing.T) {
	ctx := context.Background()
	m := newLIDMap(openTestDB(t))
	lid1 := types.NewJID("111", types.HiddenUserServer)
	pn1 := types.NewJID("222", types.DefaultUserServer)
	lid2 := types.NewJID("333", types.HiddenUserServer)
	pn2 := types.NewJID("444", types.DefaultUserServer)
	if err := m.PutManyLIDMappings(ctx, []store.LIDMapping{
		{LID: lid1, PN: pn1},
		{LID: lid2, PN: pn2},
	}); err != nil {
		t.Fatalf("PutManyLIDMappings: %v", err)
	}
	for _, tc := range []struct{ lid, pn types.JID }{{lid1, pn1}, {lid2, pn2}} {
		if got, _ := m.GetPNForLID(ctx, tc.lid); got.User != tc.pn.User {
			t.Fatalf("GetPNForLID(%s) = %s; want %s", tc.lid, got, tc.pn)
		}
		if got, _ := m.GetLIDForPN(ctx, tc.pn); got.User != tc.lid.User {
			t.Fatalf("GetLIDForPN(%s) = %s; want %s", tc.pn, got, tc.lid)
		}
	}
}

func TestLIDMap_NotFound(t *testing.T) {
	ctx := context.Background()
	m := newLIDMap(openTestDB(t))
	absentLID := types.NewJID("999", types.HiddenUserServer)
	absentPN := types.NewJID("888", types.DefaultUserServer)
	if got, err := m.GetPNForLID(ctx, absentLID); err != nil || !got.IsEmpty() {
		t.Fatalf("GetPNForLID(absent) = %s,%v; want EmptyJID,nil", got, err)
	}
	if got, err := m.GetLIDForPN(ctx, absentPN); err != nil || !got.IsEmpty() {
		t.Fatalf("GetLIDForPN(absent) = %s,%v; want EmptyJID,nil", got, err)
	}
}

func TestLIDMap_GetManyMixed(t *testing.T) {
	ctx := context.Background()
	m := newLIDMap(openTestDB(t))
	lid := types.NewJID("111", types.HiddenUserServer)
	known := types.NewJID("222", types.DefaultUserServer)
	unknown := types.NewJID("777", types.DefaultUserServer)
	if err := m.PutLIDMapping(ctx, lid, known); err != nil {
		t.Fatalf("PutLIDMapping: %v", err)
	}
	got, err := m.GetManyLIDsForPNs(ctx, []types.JID{known, unknown})
	if err != nil {
		t.Fatalf("GetManyLIDsForPNs: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("GetManyLIDsForPNs len = %d; want 1 (only the known PN)", len(got))
	}
	if got[known].User != lid.User {
		t.Fatalf("GetManyLIDsForPNs[known] = %s; want %s", got[known], lid)
	}
	if _, ok := got[unknown]; ok {
		t.Fatalf("GetManyLIDsForPNs included unknown PN %s", unknown)
	}
}
