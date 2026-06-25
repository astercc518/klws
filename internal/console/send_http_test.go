// internal/console/send_http_test.go
package console

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/acme/wadist/internal/crypto"
)

// blindOf returns the blind index for phone using the 32-byte zero key
// (matches the key passed to WithBlindKey in TestSendFlowEndToEnd).
func blindOf(phone string) []byte {
	return crypto.BlindIndex(make([]byte, 32), phone)
}

func TestSendFlowEndToEnd(t *testing.T) {
	ctx := context.Background()
	mgr := newTestManager(t)
	cfg := testConfig()
	users := NewUserRepo(mgr.SystemPool())
	sessions := NewSessionStore(newTestRedis(t), time.Hour)
	srv, _ := NewServer(cfg, users, sessions, mgr)
	srv.WithPricing(pricingRepo(t, mgr)).WithBlindKey(make([]byte, 32))
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	var tid int64
	mustScan(t, mgr, ctx, `INSERT INTO tenants (name) VALUES ('A') RETURNING id`, &tid)
	mustExec(t, mgr, ctx, `INSERT INTO tenant_pricing (tenant_id, country_code, unit_price) VALUES ($1,'US',2)`, tid)
	mustExec(t, mgr, ctx, `INSERT INTO tenant_wallets (tenant_id, balance) VALUES ($1, 1000)`, tid)
	// suppress one number for tenant A
	mustExec(t, mgr, ctx, `INSERT INTO suppression_list (tenant_id, phone_bidx) VALUES ($1, $2)`, tid, blindOf("15550000099"))

	cust := loginAs(t, ts, users, sessions, cfg, "a@x.test", RoleCustomer, &tid)
	tok := csrfFor(t, cust, ts, "/send") // GET the send page → csrf cookie+token

	// upload 4 numbers incl. a duplicate and the suppressed one → 1 dup + 1 suppressed filtered → 2 kept
	form := url.Values{
		"csrf":    {tok},
		"country": {"US"},
		"body":    {"{Hi|Hello} there"},
		"numbers": {"15550000001\n15550000001\n15550000099\n15550000002"},
	}
	resp, err := cust.PostForm(ts.URL+"/send", form)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther { // 303 to dashboard on success
		t.Fatalf("submit: want 303, got %d", resp.StatusCode)
	}

	// exactly one campaign, running, with 2 recipients (001 + 002; 001 dup collapsed, 099 suppressed)
	var camp, total, rc int64
	mustScanPool(t, mgr, ctx, `SELECT id, total FROM campaigns WHERE tenant_id=$1`, tid, &camp, &total)
	mustScanPool(t, mgr, ctx, `SELECT count(*) FROM campaign_recipients WHERE campaign_id=$1`, camp, &rc)
	if rc != 2 {
		t.Fatalf("recipients: want 2 (dup+suppressed removed), got %d", rc)
	}

	// preview returns variants
	pr, err := cust.PostForm(ts.URL+"/send/preview", url.Values{"csrf": {tok}, "body": {"{A|B|C}"}})
	if err != nil {
		t.Fatal(err)
	}
	pb := readAll(t, pr)
	if pr.StatusCode != 200 || !(strings.Contains(pb, "A") || strings.Contains(pb, "B") || strings.Contains(pb, "C")) {
		t.Fatalf("preview: %d %s", pr.StatusCode, pb)
	}
}

func TestSendPageCustomerOnly(t *testing.T) {
	mgr := newTestManager(t)
	cfg := testConfig()
	users := NewUserRepo(mgr.SystemPool())
	sessions := NewSessionStore(newTestRedis(t), time.Hour)
	srv, _ := NewServer(cfg, users, sessions, mgr)
	srv.WithPricing(pricingRepo(t, mgr)).WithBlindKey(make([]byte, 32))
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// admin hitting /send is forbidden
	admin := loginAs(t, ts, users, sessions, cfg, "admin@x.test", RoleAdmin, nil)
	resp, err := admin.Get(ts.URL + "/send")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("admin /send: want 403, got %d", resp.StatusCode)
	}
}
