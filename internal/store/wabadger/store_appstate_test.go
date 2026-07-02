package wabadger

import (
	"bytes"
	"context"
	"testing"

	"go.mau.fi/whatsmeow/store"
)

func TestAppState_VersionMutationSenderKey(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	// sender key
	_ = s.PutSenderKey(ctx, "grp@g.us", "u:1", []byte("sk"))
	if v, _ := s.GetSenderKey(ctx, "grp@g.us", "u:1"); !bytes.Equal(v, []byte("sk")) {
		t.Fatalf("sender key round-trip failed")
	}
	// app state version
	var hash [128]byte
	hash[0] = 5
	_ = s.PutAppStateVersion(ctx, "critical_block", 7, hash)
	gotV, gotH, _ := s.GetAppStateVersion(ctx, "critical_block")
	if gotV != 7 || gotH != hash {
		t.Fatalf("app state version mismatch")
	}
	// mutation macs
	_ = s.PutAppStateMutationMACs(ctx, "critical_block", 7, []store.AppStateMutationMAC{
		{IndexMAC: []byte("i1"), ValueMAC: []byte("v1")},
	})
	if vm, _ := s.GetAppStateMutationMAC(ctx, "critical_block", []byte("i1")); !bytes.Equal(vm, []byte("v1")) {
		t.Fatalf("mutation MAC round-trip failed")
	}
	// sync key + latest
	_ = s.PutAppStateSyncKey(ctx, []byte("kid1"), store.AppStateSyncKey{Data: []byte("d"), Timestamp: 100})
	_ = s.PutAppStateSyncKey(ctx, []byte("kid2"), store.AppStateSyncKey{Data: []byte("d2"), Timestamp: 200})
	latest, _ := s.GetLatestAppStateSyncKeyID(ctx)
	if !bytes.Equal(latest, []byte("kid2")) {
		t.Fatalf("latest sync key id = %q; want kid2", latest)
	}
}

// TestAppState_SyncKeyStaleSkip verifies a stale (older-timestamp) write for an
// existing id is a no-op, matching sqlstore's conditional ON CONFLICT upsert.
func TestAppState_SyncKeyStaleSkip(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	if err := s.PutAppStateSyncKey(ctx, []byte("kid"), store.AppStateSyncKey{Data: []byte("new"), Timestamp: 200}); err != nil {
		t.Fatalf("put newer: %v", err)
	}
	if err := s.PutAppStateSyncKey(ctx, []byte("kid"), store.AppStateSyncKey{Data: []byte("stale"), Timestamp: 100}); err != nil {
		t.Fatalf("put stale: %v", err)
	}
	got, err := s.GetAppStateSyncKey(ctx, []byte("kid"))
	if err != nil || got == nil {
		t.Fatalf("get sync key = %v,%v", got, err)
	}
	if got.Timestamp != 200 || !bytes.Equal(got.Data, []byte("new")) {
		t.Fatalf("stale write clobbered newer key: got ts=%d data=%q; want ts=200 data=new", got.Timestamp, got.Data)
	}
}

// TestAppState_DeleteVersionCascadesMutationMACs verifies DeleteAppStateVersion
// also drops that name's mutation MACs, mirroring sqlstore's FK cascade.
func TestAppState_DeleteVersionCascadesMutationMACs(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	var hash [128]byte
	if err := s.PutAppStateVersion(ctx, "regular", 3, hash); err != nil {
		t.Fatalf("put version: %v", err)
	}
	if err := s.PutAppStateMutationMACs(ctx, "regular", 3, []store.AppStateMutationMAC{
		{IndexMAC: []byte("idx"), ValueMAC: []byte("val")},
	}); err != nil {
		t.Fatalf("put mutation macs: %v", err)
	}
	if err := s.DeleteAppStateVersion(ctx, "regular"); err != nil {
		t.Fatalf("delete version: %v", err)
	}
	vm, err := s.GetAppStateMutationMAC(ctx, "regular", []byte("idx"))
	if err != nil {
		t.Fatalf("get mutation mac: %v", err)
	}
	if vm != nil {
		t.Fatalf("mutation MAC survived version delete: %q", vm)
	}
}
