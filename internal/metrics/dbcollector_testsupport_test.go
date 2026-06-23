package metrics

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
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
	if err != nil {
		t.Fatalf("pg: %v", err)
	}
	t.Cleanup(func() { _ = c.Terminate(ctx) })
	dsn, _ := c.ConnectionString(ctx, "sslmode=disable")
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool, ctx
}

func applyMigrations(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	files, _ := filepath.Glob("../../migrations/*.sql")
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

// seedCollectorFixture inserts the minimum rows needed to satisfy FK constraints
// and give each gauge at least one row. Insert order: proxy_pool → account_devices
// (proxy_id FK), tenant_wallets, campaign_templates → campaigns → campaign_recipients.
func seedCollectorFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()

	// proxy_pool must come before account_devices (FK: account_devices.proxy_id)
	var proxyID int64
	if err := pool.QueryRow(ctx,
		`INSERT INTO proxy_pool (proxy_url, proxy_type, country_code, max_bindings, current_bindings, is_alive)
		 VALUES ('socks5://seed-proxy:1080','socks5','US',5,2,TRUE) RETURNING id`).Scan(&proxyID); err != nil {
		t.Fatalf("seed proxy: %v", err)
	}

	// account_devices: one active, healthy account
	if _, err := pool.Exec(ctx, `
INSERT INTO account_devices
    (tenant_id, account_jid, phone_number, ban_status, proxy_id, registered_at, health_score, sent_today)
VALUES (1,'seed@s.whatsapp.net','15550000001','active',$1,now(),100,0)`, proxyID); err != nil {
		t.Fatalf("seed account_device: %v", err)
	}

	// tenant_wallets
	if _, err := pool.Exec(ctx, `
INSERT INTO tenant_wallets (tenant_id, balance, frozen, locked)
VALUES (1, 10000, 500, FALSE)
ON CONFLICT (tenant_id) DO NOTHING`); err != nil {
		t.Fatalf("seed tenant_wallets: %v", err)
	}

	// campaign_templates → campaigns → campaign_recipients (pending by default)
	var templateID, campaignID int64
	if err := pool.QueryRow(ctx,
		`INSERT INTO campaign_templates (tenant_id, kind, body) VALUES (1,'text','hello seed') RETURNING id`).
		Scan(&templateID); err != nil {
		t.Fatalf("seed template: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO campaigns (tenant_id, template_id, state) VALUES (1,$1,'running') RETURNING id`,
		templateID).Scan(&campaignID); err != nil {
		t.Fatalf("seed campaign: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO campaign_recipients (campaign_id, tenant_id, phone, country_code)
		 VALUES ($1, 1, '15550000001', 'US')`, campaignID); err != nil {
		t.Fatalf("seed campaign_recipient: %v", err)
	}
}

// activeAccountsProbe returns the gauge metric for ban_status="active" from wadist_accounts.
func activeAccountsProbe(t *testing.T, reg *prometheus.Registry) prometheus.Gauge {
	t.Helper()
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, mf := range mfs {
		if mf.GetName() == "wadist_accounts" {
			for _, m := range mf.GetMetric() {
				for _, lp := range m.GetLabel() {
					if lp.GetName() == "ban_status" && lp.GetValue() == "active" {
						g := prometheus.NewGauge(prometheus.GaugeOpts{Name: "tmp_probe"})
						g.Set(m.GetGauge().GetValue())
						return g
					}
				}
			}
		}
	}
	t.Log("activeAccountsProbe: no wadist_accounts{ban_status='active'} found; returning 0")
	g := prometheus.NewGauge(prometheus.GaugeOpts{Name: "tmp_probe"})
	g.Set(0)
	return g
}

// gaugeValue returns the first gauge value whose metric family name matches name,
// summing all label combinations (useful for no-label metrics like wadist_active_sessions).
// For wadist_recipients_state it returns the value for state="pending".
func gaugeValue(t *testing.T, reg *prometheus.Registry, name string) float64 {
	t.Helper()
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
		metrics := mf.GetMetric()
		if len(metrics) == 0 {
			return 0
		}
		// For recipients_state, prefer state=pending; otherwise sum all.
		if name == "wadist_recipients_state" {
			for _, m := range metrics {
				for _, lp := range m.GetLabel() {
					if lp.GetName() == "state" && lp.GetValue() == "pending" {
						return gaugeOrHistVal(m)
					}
				}
			}
			// fallback: return sum
			var sum float64
			for _, m := range metrics {
				sum += gaugeOrHistVal(m)
			}
			return sum
		}
		// default: sum all time series under this name
		var sum float64
		for _, m := range metrics {
			sum += gaugeOrHistVal(m)
		}
		return sum
	}
	return 0
}

func gaugeOrHistVal(m *dto.Metric) float64 {
	if g := m.GetGauge(); g != nil {
		return g.GetValue()
	}
	if c := m.GetCounter(); c != nil {
		return c.GetValue()
	}
	return 0
}
