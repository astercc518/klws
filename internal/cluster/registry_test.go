package cluster

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/acme/wadist/internal/metrics"
)

// 编译期断言：Registry 满足 metrics.RegistrySnapshotProvider（断言放测试，生产 cluster 不 import metrics）。
var _ metrics.RegistrySnapshotProvider = (*Registry)(nil)

func TestRegistry_AddGetRemoveLen(t *testing.T) {
	r := NewRegistry()
	if r.ActiveSessions() != 0 {
		t.Fatal("empty registry should be 0")
	}
	s := NewSession("jid-1", &fakeConn{}, &fakeLock{healthy: true})
	r.Add(s)
	if r.Len() != 1 || r.ActiveSessions() != 1 {
		t.Fatalf("len=%d active=%d", r.Len(), r.ActiveSessions())
	}
	got, ok := r.Get("jid-1")
	if !ok || got != s {
		t.Fatal("get failed")
	}
	removed, ok := r.Remove("jid-1")
	if !ok || removed != s || r.Len() != 0 {
		t.Fatal("remove failed")
	}
}

func TestRegistry_AddReplacesAndClosesOld(t *testing.T) {
	r := NewRegistry()
	oldConn := &fakeConn{connected: true}
	r.Add(NewSession("jid-1", oldConn, &fakeLock{healthy: true}))
	// 重复 Add 同 JID：旧会话必须被 Close（防双开），新会话占位
	newConn := &fakeConn{connected: true}
	r.Add(NewSession("jid-1", newConn, &fakeLock{healthy: true}))
	if r.Len() != 1 {
		t.Fatalf("len=%d", r.Len())
	}
	if oldConn.connected {
		t.Fatal("old session must be disconnected when replaced")
	}
}

func TestRegistry_CloseAll(t *testing.T) {
	r := NewRegistry()
	conns := make([]*fakeConn, 5)
	for i := range conns {
		conns[i] = &fakeConn{connected: true}
		r.Add(NewSession(fmt.Sprintf("jid-%d", i), conns[i], &fakeLock{healthy: true}))
	}
	r.CloseAll(context.Background())
	if r.Len() != 0 {
		t.Fatalf("registry not drained: %d", r.Len())
	}
	for i, c := range conns {
		if c.connected {
			t.Fatalf("session %d not disconnected", i)
		}
	}
}

func TestRegistry_ConcurrentRace(t *testing.T) {
	r := NewRegistry()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			jid := fmt.Sprintf("jid-%d", i%10)
			r.Add(NewSession(jid, &fakeConn{}, &fakeLock{healthy: true}))
			_, _ = r.Get(jid)
			_ = r.ActiveSessions()
			_ = r.Snapshot()
			r.Remove(jid)
		}(i)
	}
	wg.Wait()
}
