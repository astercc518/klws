package cluster

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSupervisor_ShutdownOrder(t *testing.T) {
	var seq []string
	var mu sync.Mutex
	rec := func(s string) { mu.Lock(); seq = append(seq, s); mu.Unlock() }

	reg := NewRegistry()
	conn := &fakeConn{connected: true}
	reg.Add(NewSession("jid-1", conn, &fakeLock{healthy: true}))

	sup := NewSupervisor(reg, SupervisorOpts{
		StopIntake:      func() { rec("intake") },
		CloseStore:      func() { rec("store") },
		Flush:           func() { rec("flush") },
		Limit:           4,
		ShutdownTimeout: 2 * time.Second,
	})
	// 一个后台循环，Shutdown 必须先取消它（在断会话/关池之前 loop 已退出）
	loopStopped := make(chan struct{})
	sup.Go(func(ctx context.Context) error {
		<-ctx.Done()
		close(loopStopped)
		return ctx.Err()
	})

	sup.Shutdown()

	select {
	case <-loopStopped:
	default:
		t.Fatal("loop not stopped by shutdown")
	}
	mu.Lock()
	defer mu.Unlock()
	// 期望顺序：intake -> store -> flush（session 断开通过 conn.connected 验证，夹在 intake 与 store 之间）
	if len(seq) != 3 || seq[0] != "intake" || seq[1] != "store" || seq[2] != "flush" {
		t.Fatalf("shutdown order wrong: %v", seq)
	}
	if conn.connected {
		t.Fatal("sessions not disconnected during shutdown")
	}
	if reg.Len() != 0 {
		t.Fatal("registry not drained")
	}
}

func TestSupervisor_ShutdownIdempotent(t *testing.T) {
	reg := NewRegistry()
	calls := int32(0)
	sup := NewSupervisor(reg, SupervisorOpts{
		StopIntake: func() { atomic.AddInt32(&calls, 1) },
		CloseStore: func() {}, Flush: func() {}, Limit: 2, ShutdownTimeout: time.Second,
	})
	sup.Shutdown()
	sup.Shutdown() // 第二次必须 no-op
	if atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("StopIntake called %d times, want 1", calls)
	}
}

func TestSupervisor_StartAccounts_RespectsLimit(t *testing.T) {
	reg := NewRegistry()
	sup := NewSupervisor(reg, SupervisorOpts{
		StopIntake: func() {}, CloseStore: func() {}, Flush: func() {},
		Limit: 4, ShutdownTimeout: time.Second,
	})
	var cur, max int32
	jids := make([]string, 40)
	for i := range jids {
		jids[i] = fmt.Sprintf("jid-%d", i)
	}
	err := sup.StartAccounts(context.Background(), jids, func(_ context.Context, jid string) (*Session, error) {
		n := atomic.AddInt32(&cur, 1)
		for {
			m := atomic.LoadInt32(&max)
			if n <= m || atomic.CompareAndSwapInt32(&max, m, n) {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
		atomic.AddInt32(&cur, -1)
		return NewSession(jid, &fakeConn{}, &fakeLock{healthy: true}), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if max > 4 {
		t.Fatalf("concurrency %d exceeded limit 4", max)
	}
	if reg.Len() != 40 {
		t.Fatalf("expected 40 registered, got %d", reg.Len())
	}
}

func TestSupervisor_StartAccounts_StartErrorDoesNotRegister(t *testing.T) {
	reg := NewRegistry()
	sup := NewSupervisor(reg, SupervisorOpts{
		StopIntake: func() {}, CloseStore: func() {}, Flush: func() {},
		Limit: 4, ShutdownTimeout: time.Second,
	})
	_ = sup.StartAccounts(context.Background(), []string{"a", "b"}, func(_ context.Context, jid string) (*Session, error) {
		if jid == "a" {
			return nil, fmt.Errorf("boom")
		}
		return NewSession(jid, &fakeConn{}, &fakeLock{healthy: true}), nil
	})
	// 失败的不入册；成功的入册
	if _, ok := reg.Get("a"); ok {
		t.Fatal("failed start must not register")
	}
	if _, ok := reg.Get("b"); !ok {
		t.Fatal("successful start should register")
	}
}
