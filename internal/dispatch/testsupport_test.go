package dispatch

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	goredis "github.com/redis/go-redis/v9"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"
	"github.com/testcontainers/testcontainers-go/wait"
)

func pgPool(t *testing.T) (*pgxpool.Pool, context.Context) {
	t.Helper()
	ctx := context.Background()
	c, err := postgres.Run(ctx, "postgres:16",
		postgres.WithDatabase("app_test"), postgres.WithUsername("test"), postgres.WithPassword("test"),
		testcontainers.WithWaitStrategy(wait.ForAll(
			wait.ForListeningPort("5432/tcp"),
			wait.ForLog("database system is ready to accept connections").WithOccurrence(2),
		).WithStartupTimeout(60*time.Second)))
	if err != nil { t.Fatalf("pg: %v", err) }
	t.Cleanup(func() { _ = c.Terminate(ctx) })
	dsn, _ := c.ConnectionString(ctx, "sslmode=disable")
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil { t.Fatalf("pool: %v", err) }
	t.Cleanup(pool.Close)
	applyMigrations(t, ctx, pool)
	return pool, ctx
}

func redisClient(t *testing.T) *goredis.Client {
	t.Helper()
	ctx := context.Background()
	c, err := tcredis.Run(ctx, "redis:7")
	if err != nil { t.Fatalf("redis: %v", err) }
	t.Cleanup(func() { _ = c.Terminate(ctx) })
	url, _ := c.ConnectionString(ctx)
	opt, _ := goredis.ParseURL(url)
	rdb := goredis.NewClient(opt)
	t.Cleanup(func() { _ = rdb.Close() })
	return rdb
}

func applyMigrations(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	files, _ := filepath.Glob("../../migrations/*.sql")
	sort.Strings(files)
	for _, f := range files {
		if strings.HasSuffix(f, ".down.sql") { continue }
		b, err := os.ReadFile(f)
		if err != nil { t.Fatalf("read %s: %v", f, err) }
		if _, err := pool.Exec(ctx, string(b)); err != nil { t.Fatalf("apply %s: %v", f, err) }
	}
}

// seedAccount inserts an active account bound to a seeded proxy in `country`,
// with given registered_at, health, sent_today. Returns the jid.
func seedAccount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, jid, country string, reg time.Time, health, sentToday int) {
	t.Helper()
	var proxyID int64
	if err := pool.QueryRow(ctx,
		`INSERT INTO proxy_pool (proxy_url, proxy_type, country_code, max_bindings, current_bindings)
		 VALUES ($1,'socks5',$2,1,1) RETURNING id`, "socks5://"+jid, country).Scan(&proxyID); err != nil {
		t.Fatalf("seed proxy: %v", err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO account_devices (tenant_id, account_jid, phone_number, ban_status, proxy_id, registered_at, health_score, sent_today)
VALUES (1,$1,'15550000000','active',$2,$3,$4,$5)`,
		jid, proxyID, reg, health, sentToday); err != nil {
		t.Fatalf("seed account: %v", err)
	}
}

func seedCampaign(t *testing.T, ctx context.Context, pool *pgxpool.Pool, body string) int64 {
	t.Helper()
	var tid, cid int64
	if err := pool.QueryRow(ctx, `INSERT INTO campaign_templates (tenant_id, kind, body) VALUES (1,'text',$1) RETURNING id`, body).Scan(&tid); err != nil {
		t.Fatalf("seed template: %v", err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO campaigns (tenant_id, template_id, state) VALUES (1,$1,'running') RETURNING id`, tid).Scan(&cid); err != nil {
		t.Fatalf("seed campaign: %v", err)
	}
	return cid
}

func seedRecipient(t *testing.T, ctx context.Context, pool *pgxpool.Pool, campaignID int64, phone, country string) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(ctx, `
INSERT INTO campaign_recipients (campaign_id, tenant_id, phone, country_code) VALUES ($1,1,$2,$3) RETURNING id`,
		campaignID, phone, country).Scan(&id); err != nil {
		t.Fatalf("seed recipient: %v", err)
	}
	return id
}
