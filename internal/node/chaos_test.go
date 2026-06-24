package node

import (
	"context"
	"testing"
	"time"

	"github.com/acme/wadist/internal/cluster"
	"github.com/hibiken/asynq"
)

// TestTakeoverChaos simulates node death and asserts a peer takes over the
// account: node A directly holds the advisory lock for jid; A's lock connection
// is forcibly closed via KillConnForTest (simulating abrupt process death, which
// causes PG to release the advisory lock); A's heartbeat is made stale; node B's
// scanner enqueues a takeover task; B then calls StartAccountWithLock in a retry
// loop and claims owner_node='node-B'.
func TestTakeoverChaos(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	ctx := context.Background()
	m := newTestManager(t)
	redisAddr := startRedis(t)

	const jid = "chaos-jid-1"
	seedAccountDevice(t, ctx, m, jid)

	// --- Node A: acquire lock directly, set heartbeat and claim ownership ---
	if err := m.UpsertNodeHeartbeat(ctx, "node-A"); err != nil {
		t.Fatalf("upsert node-A heartbeat: %v", err)
	}
	lockA, err := m.AcquireDeviceLock(ctx, jid)
	if err != nil {
		t.Fatalf("node A acquire lock: %v", err)
	}
	if err := m.ClaimAccount(ctx, jid, "node-A"); err != nil {
		t.Fatalf("node A claim: %v", err)
	}

	// --- CHAOS: forcibly close node A's lock connection (simulates process
	// death). PG releases the session-level advisory lock automatically when
	// the backend TCP connection drops. ---
	lockA.KillConnForTest(ctx)

	// Make node-A's heartbeat stale so scanOnce picks the account up.
	if _, err := m.BizPool().Exec(ctx,
		`UPDATE cluster_nodes SET last_heartbeat_at = now() - interval '10 minutes' WHERE node_id='node-A'`); err != nil {
		t.Fatalf("stale node-A heartbeat: %v", err)
	}

	// --- Node B ---
	regB := cluster.NewRegistry()
	supB := cluster.NewSupervisor(regB, cluster.SupervisorOpts{Limit: 4, ShutdownTimeout: time.Second})
	t.Cleanup(supB.Shutdown)

	factoryB := func(_ context.Context, jid string, lock cluster.DeviceLockHandle) (*cluster.Session, error) {
		return cluster.NewSession(jid, &fakeConn{connected: true}, lock), nil
	}
	orchB := NewOrchestrator(m, regB, supB, "node-B", factoryB, time.Second, nil)

	// Node B scanner: should find the stale-owned account and enqueue a takeover.
	client := asynq.NewClient(asynq.RedisClientOpt{Addr: redisAddr})
	t.Cleanup(func() { _ = client.Close() })
	enq := NewTakeoverEnqueuer(client, 30*time.Second)

	n, err := orchB.scanOnce(ctx, 30*time.Second, enq)
	if err != nil {
		t.Fatalf("node B scanOnce: %v", err)
	}
	if n < 1 {
		t.Fatalf("node B scan: expected >=1 enqueued, got %d", n)
	}

	// Drive the takeover: retry StartAccountWithLock until the killed backend's
	// advisory lock is released. The lock is released as soon as the TCP
	// connection drops, so the first attempt typically succeeds.
	deadline := time.Now().Add(10 * time.Second)
	var sessB *cluster.Session
	for {
		sessB, err = orchB.StartAccountWithLock(ctx, jid)
		if err == nil && sessB != nil {
			break // node B took over
		}
		if time.Now().After(deadline) {
			t.Fatalf("node B failed to take over within deadline (last err=%v)", err)
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Assert that the database reflects node-B as the new owner.
	var owner string
	if err := m.BizPool().QueryRow(ctx,
		`SELECT owner_node FROM account_devices WHERE account_jid=$1`, jid).Scan(&owner); err != nil {
		t.Fatalf("query owner_node: %v", err)
	}
	if owner != "node-B" {
		t.Fatalf("owner_node=%q, want node-B", owner)
	}

	// Session must be registered in node B's registry.
	if _, ok := regB.Get(jid); !ok {
		t.Fatal("session not found in node-B registry after takeover")
	}
}
