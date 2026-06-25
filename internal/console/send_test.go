// internal/console/send_test.go
package console

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/acme/wadist/internal/pricing"
	"github.com/acme/wadist/internal/store"
)

// withCustomerSession returns a context carrying a RoleCustomer session for tid.
func withCustomerSession(ctx context.Context, tid int64) context.Context {
	return context.WithValue(ctx, sessionCtxKey, &SessionData{UserID: 1, Role: RoleCustomer, TenantID: &tid})
}

// pricingRepo returns a pricing.Repo backed by the test manager's SystemPool.
func pricingRepo(t *testing.T, mgr *store.Manager) *pricing.Repo {
	t.Helper()
	return pricing.NewRepo(mgr.SystemPool())
}

// mustScanPool executes a query via SystemPool, scanning the returned columns into dests.
func mustScanPool(t *testing.T, mgr *store.Manager, ctx context.Context, q string, arg interface{}, dests ...interface{}) {
	t.Helper()
	if err := mgr.SystemPool().QueryRow(ctx, q, arg).Scan(dests...); err != nil {
		t.Fatalf("mustScanPool(%q): %v", q, err)
	}
}

// fmt0 returns a distinct valid phone string (10 digits) for index i.
func fmt0(i int) string {
	return fmt.Sprintf("1555%06d", i)
}

func TestCreateCampaignRLSAndBalance(t *testing.T) {
	ctx := context.Background()
	mgr := newTestManager(t)
	cfg := testConfig()
	srv, _ := NewServer(cfg, NewUserRepo(mgr.SystemPool()), NewSessionStore(newTestRedis(t), time.Hour), mgr)
	srv.WithPricing(pricingRepo(t, mgr)).WithBlindKey(make([]byte, 32)) // 32-byte zero key OK for test

	var tid int64
	mustScan(t, mgr, ctx, `INSERT INTO tenants (name) VALUES ('A') RETURNING id`, &tid)
	// price US=5; wallet balance 100 → affords 20 messages
	mustExec(t, mgr, ctx, `INSERT INTO tenant_pricing (tenant_id, country_code, unit_price) VALUES ($1,'US',5)`, tid)
	mustExec(t, mgr, ctx, `INSERT INTO tenant_wallets (tenant_id, balance) VALUES ($1, 100)`, tid)

	cctx := withCustomerSession(ctx, tid) // helper: context with a RoleCustomer session for tid

	// 3 phones × 5 = 15 ≤ 100 → created, running, 3 recipients
	id, err := srv.createCampaign(cctx, "US", "{Hi|Hello}", []string{"15551230001", "15551230002", "15551230003"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	var state string
	var total int
	mustScanPool(t, mgr, ctx, `SELECT state::text, total FROM campaigns WHERE id=$1`, id, &state, &total)
	if state != "running" || total != 3 {
		t.Fatalf("campaign: state=%s total=%d", state, total)
	}
	var rc int
	mustScanPool(t, mgr, ctx, `SELECT count(*) FROM campaign_recipients WHERE campaign_id=$1`, id, &rc)
	if rc != 3 {
		t.Fatalf("recipients: want 3, got %d", rc)
	}

	// 30 phones × 5 = 150 > 100 → ErrInsufficientBalance, nothing created
	many := make([]string, 30)
	for i := range many {
		many[i] = fmt0(i)
	}
	if _, err := srv.createCampaign(cctx, "US", "x", many); !errors.Is(err, ErrInsufficientBalance) {
		t.Fatalf("want ErrInsufficientBalance, got %v", err)
	}

	// balance-rejected attempt must have inserted nothing; only the first campaign exists
	var nc int
	mustScanPool(t, mgr, ctx, `SELECT count(*) FROM campaigns WHERE tenant_id=$1`, tid, &nc)
	if nc != 1 {
		t.Fatalf("balance-rejected attempt must create nothing; want 1 campaign, got %d", nc)
	}
}

func TestCreateCampaignFailsClosedWithoutBlindKey(t *testing.T) {
	ctx := context.Background()
	mgr := newTestManager(t)
	cfg := testConfig()
	srv, _ := NewServer(cfg, NewUserRepo(mgr.SystemPool()), NewSessionStore(newTestRedis(t), time.Hour), mgr)
	srv.WithPricing(pricingRepo(t, mgr)) // NOTE: no WithBlindKey

	var tid int64
	mustScan(t, mgr, ctx, `INSERT INTO tenants (name) VALUES ('A') RETURNING id`, &tid)
	cctx := withCustomerSession(ctx, tid)
	if _, err := srv.createCampaign(cctx, "US", "x", []string{"15550000001"}); !errors.Is(err, ErrSendNotConfigured) {
		t.Fatalf("want ErrSendNotConfigured without blind key, got %v", err)
	}
}

func TestCreateCampaignNoPriceConfigured(t *testing.T) {
	ctx := context.Background()
	mgr := newTestManager(t)
	cfg := testConfig()
	srv, _ := NewServer(cfg, NewUserRepo(mgr.SystemPool()), NewSessionStore(newTestRedis(t), time.Hour), mgr)
	srv.WithPricing(pricingRepo(t, mgr)).WithBlindKey(make([]byte, 32))

	var tid int64
	mustScan(t, mgr, ctx, `INSERT INTO tenants (name) VALUES ('A') RETURNING id`, &tid)
	// wallet funded but NO tenant_pricing row for DE
	mustExec(t, mgr, ctx, `INSERT INTO tenant_wallets (tenant_id, balance) VALUES ($1, 1000)`, tid)

	cctx := withCustomerSession(ctx, tid)
	_, err := srv.createCampaign(cctx, "DE", "hello", []string{"4915112345678"})
	if !errors.Is(err, ErrNoPriceConfigured) {
		t.Fatalf("want ErrNoPriceConfigured, got %v", err)
	}
	// nothing created
	var nc int
	mustScanPool(t, mgr, ctx, `SELECT count(*) FROM campaigns WHERE tenant_id=$1`, tid, &nc)
	if nc != 0 {
		t.Fatalf("no-price path must create nothing; want 0 campaigns, got %d", nc)
	}
}
