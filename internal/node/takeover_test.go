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
	m := newTestManager(t)
	ctx := context.Background()
	redisAddr := startRedis(t)
	client := asynq.NewClient(asynq.RedisClientOpt{Addr: redisAddr})
	defer client.Close()

	// Insert a dead node and claim an account under it
	m.BizPool().Exec(ctx, `INSERT INTO cluster_nodes (node_id, last_heartbeat_at) VALUES ('dead', now() - interval '10 minutes')`)
	seedAccountDevice(t, ctx, m, "jid-stale")
	m.ClaimAccount(ctx, "jid-stale", "dead")

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
	m := newTestManager(t)
	ctx := context.Background()
	redisAddr := startRedis(t)
	client := asynq.NewClient(asynq.RedisClientOpt{Addr: redisAddr})
	defer client.Close()

	// Seed one owned (live) account — must NOT be picked up by unowned path.
	m.BizPool().Exec(ctx, `INSERT INTO cluster_nodes (node_id, last_heartbeat_at) VALUES ('live', now())`)
	seedAccountDevice(t, ctx, m, "jid-owned-scan")
	m.ClaimAccount(ctx, "jid-owned-scan", "live")

	// Seed one unowned active account (owner_node IS NULL).
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
