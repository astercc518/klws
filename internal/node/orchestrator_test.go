package node

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/acme/wadist/internal/cluster"
	"github.com/acme/wadist/internal/store"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	waLog "go.mau.fi/whatsmeow/util/log"
)

// ---------------------------------------------------------------------------
// Fakes (cluster's internal fakes are not exported)
// ---------------------------------------------------------------------------

type fakeConn struct{ connected bool }

func (f *fakeConn) Connect(_ context.Context) error { f.connected = true; return nil }
func (f *fakeConn) Disconnect()                     { f.connected = false }

// fakeLock is a DeviceLockHandle for the guard test; healthy can be flipped
// from the test goroutine while the guard goroutine reads it concurrently.
type fakeLock struct {
	mu       sync.Mutex
	healthy  bool
	released bool
}

func (l *fakeLock) Healthy(_ context.Context) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.healthy
}
func (l *fakeLock) Release(_ context.Context) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.released = true
}
func (l *fakeLock) setHealthy(v bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.healthy = v
}
func (l *fakeLock) isReleased() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.released
}

// ---------------------------------------------------------------------------
// testcontainers PG bootstrap (inlined — self-contained)
// ---------------------------------------------------------------------------

func newTestManager(t *testing.T) *store.Manager {
	t.Helper()
	ctx := context.Background()
	c, err := postgres.Run(ctx, "postgres:16",
		postgres.WithDatabase("app_test"),
		postgres.WithUsername("test"),
		postgres.WithPassword("test"),
		testcontainers.WithWaitStrategy(
			wait.ForAll(
				wait.ForListeningPort("5432/tcp"),
				wait.ForLog("database system is ready to accept connections").WithOccurrence(2),
			).WithStartupTimeout(60*time.Second)),
	)
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	t.Cleanup(func() { _ = c.Terminate(ctx) })
	dsn, err := c.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("dsn: %v", err)
	}

	m, err := store.NewManager(ctx, store.Config{DSN: dsn}, waLog.Noop)
	if err != nil {
		t.Fatalf("newManager: %v", err)
	}
	t.Cleanup(m.Close)

	// Apply all business migrations in order (glob ../../migrations/*.sql).
	files, err := filepath.Glob("../../migrations/*.sql")
	if err != nil {
		t.Fatalf("glob migrations: %v", err)
	}
	sort.Strings(files)
	for _, f := range files {
		if strings.HasSuffix(f, ".down.sql") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		if _, err := m.BizPool().Exec(ctx, string(b)); err != nil {
			t.Fatalf("apply %s: %v", f, err)
		}
	}
	return m
}

func seedAccountDevice(t *testing.T, ctx context.Context, m *store.Manager, jid string) {
	t.Helper()
	_, err := m.BizPool().Exec(ctx,
		`INSERT INTO account_devices (tenant_id, account_jid, phone_number, ban_status)
		 VALUES (1, $1, '+100', 'active')
		 ON CONFLICT (account_jid) DO NOTHING`,
		jid,
	)
	if err != nil {
		t.Fatalf("seedAccountDevice(%q): %v", jid, err)
	}
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestStartAccountWithLock_ClaimsAndRegisters(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	m := newTestManager(t)
	ctx := context.Background()
	seedAccountDevice(t, ctx, m, "jid-s1")
	reg := cluster.NewRegistry()
	sup := cluster.NewSupervisor(reg, cluster.SupervisorOpts{Limit: 4, ShutdownTimeout: time.Second})
	factory := func(_ context.Context, jid string, lock cluster.DeviceLockHandle) (*cluster.Session, error) {
		return cluster.NewSession(jid, &fakeConn{connected: true}, lock), nil
	}
	o := NewOrchestrator(m, reg, sup, "node-1", factory, time.Second)
	sess, err := o.StartAccountWithLock(ctx, "jid-s1")
	if err != nil || sess == nil {
		t.Fatalf("start: sess=%v err=%v", sess, err)
	}
	if _, ok := reg.Get("jid-s1"); !ok {
		t.Fatal("not registered")
	}
	var owner string
	m.BizPool().QueryRow(ctx, `SELECT owner_node FROM account_devices WHERE account_jid='jid-s1'`).Scan(&owner)
	if owner != "node-1" {
		t.Fatalf("owner_node=%q", owner)
	}
	sup.Shutdown()
}

func TestStartAccountWithLock_SkipsWhenLockedByPeer(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	m := newTestManager(t)
	ctx := context.Background()
	seedAccountDevice(t, ctx, m, "jid-s2")
	// Pre-acquire the lock from the same manager — a second AcquireDeviceLock on
	// the same JID returns ErrDeviceLocked (different pinned conn, same PG advisory key).
	peerLock, err := m.AcquireDeviceLock(ctx, "jid-s2")
	if err != nil {
		t.Fatal(err)
	}
	defer peerLock.Release(ctx)
	reg := cluster.NewRegistry()
	sup := cluster.NewSupervisor(reg, cluster.SupervisorOpts{Limit: 4, ShutdownTimeout: time.Second})
	factory := func(_ context.Context, jid string, lock cluster.DeviceLockHandle) (*cluster.Session, error) {
		t.Fatal("factory must not be called when lock is held by a peer")
		return nil, nil
	}
	o := NewOrchestrator(m, reg, sup, "node-2", factory, time.Second)
	sess, err := o.StartAccountWithLock(ctx, "jid-s2")
	if err != nil || sess != nil {
		t.Fatalf("expected (nil,nil) skip, got sess=%v err=%v", sess, err)
	}
	if reg.Len() != 0 {
		t.Fatal("must not register when locked by peer")
	}
}

func TestGuardSession_RemovesOnUnhealthy(t *testing.T) {
	reg := cluster.NewRegistry()
	sup := cluster.NewSupervisor(reg, cluster.SupervisorOpts{Limit: 4, ShutdownTimeout: time.Second})
	fl := &fakeLock{healthy: true}
	sess := cluster.NewSession("jid-g", &fakeConn{connected: true}, fl)
	reg.Add(sess)
	o := NewOrchestrator(nil, reg, sup, "node-g", nil, 5*time.Millisecond)
	go o.guardSession(context.Background(), sess)
	time.Sleep(20 * time.Millisecond)
	fl.setHealthy(false) // simulate lost ownership
	deadline := time.After(time.Second)
	for {
		if _, ok := reg.Get("jid-g"); !ok {
			break
		}
		select {
		case <-deadline:
			t.Fatal("guard did not remove unhealthy session")
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
	if !fl.isReleased() {
		t.Fatal("lock must be released on guard close")
	}
}
