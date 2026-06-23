// internal/store/proxy_testsupport_test.go
package store

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// applyMigrations 按文件名顺序应用所有 migrations/*.sql(跳过 *.down.sql)。
func applyMigrations(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
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
		if _, err := pool.Exec(ctx, string(b)); err != nil {
			t.Fatalf("apply %s: %v", f, err)
		}
	}
}

// seedProxy inserts one proxy and returns its id.
func seedProxy(t *testing.T, ctx context.Context, pool *pgxpool.Pool, url, country string, maxBindings int) int64 {
	t.Helper()
	var id int64
	err := pool.QueryRow(ctx,
		`INSERT INTO proxy_pool (proxy_url, proxy_type, country_code, max_bindings)
		 VALUES ($1, 'socks5', $2, $3) RETURNING id`,
		url, country, maxBindings).Scan(&id)
	if err != nil {
		t.Fatalf("seed proxy: %v", err)
	}
	return id
}

// seedAccount inserts one active account_devices row.
func seedAccount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, tenantID int64, jid, phone string) {
	t.Helper()
	_, err := pool.Exec(ctx,
		`INSERT INTO account_devices (tenant_id, account_jid, phone_number, ban_status)
		 VALUES ($1, $2, $3, 'active')`,
		tenantID, jid, phone)
	if err != nil {
		t.Fatalf("seed account: %v", err)
	}
}
