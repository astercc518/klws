package node

import (
	"context"
	"testing"
	"time"

	"github.com/hibiken/asynq"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// startRedis spins up a Redis 7 container for integration tests and returns
// the host:port address. The container is terminated when the test ends.
func startRedis(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	req := testcontainers.ContainerRequest{
		Image:        "redis:7",
		ExposedPorts: []string{"6379/tcp"},
		WaitingFor:   wait.ForLog("Ready to accept connections"),
	}
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		t.Fatalf("start redis: %v", err)
	}
	t.Cleanup(func() { _ = c.Terminate(ctx) })
	host, err := c.Host(ctx)
	if err != nil {
		t.Fatalf("redis host: %v", err)
	}
	port, err := c.MappedPort(ctx, "6379")
	if err != nil {
		t.Fatalf("redis port: %v", err)
	}
	return host + ":" + port.Port()
}

func TestTakeoverEnqueuer_Unique(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	redisAddr := startRedis(t)
	client := asynq.NewClient(asynq.RedisClientOpt{Addr: redisAddr})
	defer client.Close()
	enq := NewTakeoverEnqueuer(client, 30*time.Second)
	ctx := context.Background()
	if err := enq.Enqueue(ctx, "jid-t1"); err != nil {
		t.Fatalf("first enqueue: %v", err)
	}
	// second same jid within TTL → deduped, Enqueue swallows ErrDuplicateTask → nil
	if err := enq.Enqueue(ctx, "jid-t1"); err != nil {
		t.Fatalf("duplicate enqueue should be swallowed, got %v", err)
	}
}

func TestRunTakeoverScanner_EnqueuesStale(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	m := newTestManager(t) // NodeID=testManagerNodeID, redis-wired
	ctx := context.Background()
	redisAddr := startRedis(t)
	client := asynq.NewClient(asynq.RedisClientOpt{Addr: redisAddr})
	defer client.Close()

	// Acquire ownership under this manager's node without ever heartbeating —
	// StaleOwned (redis backend) treats an owned account with no live
	// heartbeat key as a takeover candidate.
	seedAccountDevice(t, ctx, m, "jid-stale")
	if _, err := m.AcquireDeviceLock(ctx, "jid-stale"); err != nil {
		t.Fatalf("acquire jid-stale: %v", err)
	}

	o := NewOrchestrator(m, nil, nil, "node-scan", nil, time.Second, nil)
	enq := NewTakeoverEnqueuer(client, 30*time.Second)
	// Direct call to scanOnce — no ticker needed for the unit test
	n, err := o.scanOnce(ctx, 30*time.Second, enq)
	if err != nil {
		t.Fatal(err)
	}
	if n < 1 {
		t.Fatalf("expected >=1 stale enqueued, got %d", n)
	}
}

func TestScanOnce_EnqueuesUnowned(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	m := newTestManager(t) // NodeID=testManagerNodeID, redis-wired
	ctx := context.Background()
	redisAddr := startRedis(t)
	client := asynq.NewClient(asynq.RedisClientOpt{Addr: redisAddr})
	defer client.Close()

	// Seed one owned (live) account — must NOT be picked up by the unowned path.
	seedAccountDevice(t, ctx, m, "jid-owned-scan")
	if _, err := m.AcquireDeviceLock(ctx, "jid-owned-scan"); err != nil {
		t.Fatalf("acquire jid-owned-scan: %v", err)
	}
	if err := m.UpsertNodeHeartbeat(ctx, testManagerNodeID); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}

	// Seed one unowned active account (no redis owner key set).
	seedAccountDevice(t, ctx, m, "jid-unowned-scan")

	o := NewOrchestrator(m, nil, nil, "node-scan2", nil, time.Second, nil)
	enq := NewTakeoverEnqueuer(client, 30*time.Second)
	// staleness large enough that the live node is not stale — only unowned should be enqueued.
	n, err := o.scanOnce(ctx, 10*time.Minute, enq)
	if err != nil {
		t.Fatal(err)
	}
	if n < 1 {
		t.Fatalf("expected >=1 unowned enqueued, got %d", n)
	}
}
