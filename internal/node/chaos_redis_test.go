// internal/node/chaos_redis_test.go
package node

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/acme/wadist/internal/cluster"
	"github.com/acme/wadist/internal/store"
	"github.com/hibiken/asynq"
	"github.com/jackc/pgx/v5/pgxpool"
	goredis "github.com/redis/go-redis/v9"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	waLog "go.mau.fi/whatsmeow/util/log"
)

func TestTakeoverChaos_Redis(t *testing.T) {
	if testing.Short() { t.Skip("integration") }
	ctx := context.Background()
	redisAddr := startRedis(t)
	m := newTestManagerRedis(t, redisAddr) // backend=redis 的测试 Manager（见 Step 3）

	const jid = "chaos-redis-1"
	seedAccountDevice(t, ctx, m, jid)

	_ = m.UpsertNodeHeartbeat(ctx, "node-A")
	if _, err := m.AcquireDeviceLock(ctx, jid); err != nil { t.Fatalf("A acquire: %v", err) }
	// CHAOS：node-A 猝死
	if err := m.ExpireNodeForTest(ctx, "node-A"); err != nil { t.Fatal(err) }

	regB := cluster.NewRegistry()
	supB := cluster.NewSupervisor(regB, cluster.SupervisorOpts{Limit: 4, ShutdownTimeout: time.Second})
	t.Cleanup(supB.Shutdown)
	factoryB := func(_ context.Context, jid string, lock cluster.DeviceLockHandle) (*cluster.Session, error) {
		return cluster.NewSession(jid, &fakeConn{connected: true}, lock), nil
	}
	// node-B 必须有自己的活心跳，否则 acquire 后 StillOwner 立即 false
	mB := newTestManagerRedisAs(t, redisAddr, "node-B")
	_ = mB.UpsertNodeHeartbeat(ctx, "node-B")
	orchB := NewOrchestrator(mB, regB, supB, "node-B", factoryB, time.Second, nil)

	client := asynq.NewClient(asynq.RedisClientOpt{Addr: redisAddr})
	t.Cleanup(func() { _ = client.Close() })
	enq := NewTakeoverEnqueuer(client, 30*time.Second)
	if n, err := orchB.scanOnce(ctx, 30*time.Second, enq); err != nil || n < 1 {
		t.Fatalf("scanOnce n=%d err=%v", n, err)
	}

	deadline := time.Now().Add(10 * time.Second)
	for {
		sess, err := orchB.StartAccountWithLock(ctx, jid)
		if err == nil && sess != nil { break }
		if time.Now().After(deadline) { t.Fatalf("B takeover failed: %v", err) }
		time.Sleep(100 * time.Millisecond)
	}

	owner, _ := redisGet(t, redisAddr, "owner:"+jid)
	if want := "node-B:"; len(owner) < len(want) || owner[:len(want)] != want {
		t.Fatalf("redis owner=%q want prefix node-B:", owner)
	}
	if _, ok := regB.Get(jid); !ok { t.Fatal("session missing in node-B registry") }
}

func newTestManagerRedis(t *testing.T, redisAddr string) *store.Manager {
	return newTestManagerRedisAs(t, redisAddr, "node-A")
}

func newTestManagerRedisAs(t *testing.T, redisAddr, nodeID string) *store.Manager {
	t.Helper()
	ctx := context.Background()
	c, err := postgres.Run(ctx, "postgres:16",
		postgres.WithDatabase("app_test"), postgres.WithUsername("test"), postgres.WithPassword("test"),
		testcontainers.WithWaitStrategy(wait.ForAll(
			wait.ForListeningPort("5432/tcp"),
			wait.ForLog("database system is ready to accept connections").WithOccurrence(2),
		).WithStartupTimeout(60*time.Second)))
	if err != nil { t.Fatalf("start postgres: %v", err) }
	t.Cleanup(func() { _ = c.Terminate(ctx) })
	dsn, err := c.ConnectionString(ctx, "sslmode=disable")
	if err != nil { t.Fatalf("dsn: %v", err) }

	// Migrations must be applied BEFORE constructing the Manager: newManager's
	// boot-time redis proxy-index rebuild queries proxy_pool immediately.
	migPool, err := pgxpool.New(ctx, dsn)
	if err != nil { t.Fatalf("migration pool: %v", err) }
	files, _ := filepath.Glob("../../migrations/*.sql")
	sort.Strings(files)
	for _, f := range files {
		if strings.HasSuffix(f, ".down.sql") { continue }
		b, err := os.ReadFile(f)
		if err != nil { t.Fatalf("read %s: %v", f, err) }
		if _, err := migPool.Exec(ctx, string(b)); err != nil { t.Fatalf("apply %s: %v", f, err) }
	}
	migPool.Close()

	rdb := goredis.NewClient(&goredis.Options{Addr: redisAddr})
	t.Cleanup(func() { _ = rdb.Close() })
	m, err := store.NewManager(ctx, store.Config{
		DSN: dsn, Redis: rdb, NodeID: nodeID, BadgerDir: t.TempDir(),
	}, waLog.Noop)
	if err != nil { t.Fatalf("newManager: %v", err) }
	t.Cleanup(m.Close)
	return m
}

func redisGet(t *testing.T, redisAddr, key string) (string, error) {
	t.Helper()
	rdb := goredis.NewClient(&goredis.Options{Addr: redisAddr})
	defer rdb.Close()
	return rdb.Get(context.Background(), key).Result()
}
