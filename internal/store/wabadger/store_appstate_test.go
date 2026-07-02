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
