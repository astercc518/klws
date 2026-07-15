// internal/store/devices_enroll_test.go
package store

import (
	"context"
	"testing"
)

// TestEnrollDeviceForInstance_CreatesRow verifies pairing a scanned account
// (webhook jid backfill) creates the account_devices row the send path reads
// (dispatch/selectaccount.go, sendgate.Admit — both read ONLY
// account_devices), reusing account_instances' tenant_id + proxy_id verbatim
// (proxy-key unification: no new BindProxy call, proxy_pool counters
// untouched by enrollment).
func TestEnrollDeviceForInstance_CreatesRow(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	ctx := context.Background()
	m := newTestManager(t)

	proxyID := seedProxy(t, ctx, m.bizPool, "socks5://p1", "US", 1)
	if err := m.UpsertInstance(ctx, InstanceRow{
		InstanceName: "wa_enroll_1", TenantID: 7, EvoNode: "default", State: "connecting", ProxyID: proxyID,
	}); err != nil {
		t.Fatalf("seed instance: %v", err)
	}

	jid := "85266188676@s.whatsapp.net"
	if err := m.EnrollDeviceForInstance(ctx, "wa_enroll_1", jid); err != nil {
		t.Fatalf("EnrollDeviceForInstance: %v", err)
	}

	var tenantID, gotProxyID int64
	var phone, banStatus string
	err := m.bizPool.QueryRow(ctx,
		`SELECT tenant_id, phone_number, ban_status::text, proxy_id FROM account_devices WHERE account_jid=$1`, jid).
		Scan(&tenantID, &phone, &banStatus, &gotProxyID)
	if err != nil {
		t.Fatalf("query account_devices: %v", err)
	}
	if tenantID != 7 {
		t.Fatalf("tenant_id = %d, want 7 (reused from account_instances)", tenantID)
	}
	if phone != "85266188676" {
		t.Fatalf("phone_number = %q, want the jid's local part", phone)
	}
	if banStatus != "active" {
		t.Fatalf("ban_status = %q, want active", banStatus)
	}
	if gotProxyID != proxyID {
		t.Fatalf("proxy_id = %d, want %d (reused, not rebound)", gotProxyID, proxyID)
	}
}

// TestEnrollDeviceForInstance_Idempotent: re-enrolling (e.g. a reconnect
// after conn_churn re-fires connection.update) must not duplicate the row,
// and must reset ban_status back to active / refresh last_connected_at via
// ON CONFLICT DO UPDATE.
func TestEnrollDeviceForInstance_Idempotent(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	ctx := context.Background()
	m := newTestManager(t)

	proxyID := seedProxy(t, ctx, m.bizPool, "socks5://p2", "US", 1)
	if err := m.UpsertInstance(ctx, InstanceRow{
		InstanceName: "wa_enroll_2", TenantID: 7, EvoNode: "default", State: "connecting", ProxyID: proxyID,
	}); err != nil {
		t.Fatalf("seed instance: %v", err)
	}
	jid := "111@s.whatsapp.net"
	if err := m.EnrollDeviceForInstance(ctx, "wa_enroll_2", jid); err != nil {
		t.Fatalf("first enroll: %v", err)
	}
	// Force the row into a state the second enroll must reset, so the test
	// actually exercises ON CONFLICT DO UPDATE rather than a same-value no-op.
	if _, err := m.bizPool.Exec(ctx,
		`UPDATE account_devices SET ban_status='banned' WHERE account_jid=$1`, jid); err != nil {
		t.Fatalf("force banned: %v", err)
	}
	if err := m.EnrollDeviceForInstance(ctx, "wa_enroll_2", jid); err != nil {
		t.Fatalf("second enroll: %v", err)
	}

	var count int
	if err := m.bizPool.QueryRow(ctx,
		`SELECT count(*) FROM account_devices WHERE account_jid=$1`, jid).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected exactly one row after re-enroll, got %d", count)
	}
	var banStatus string
	if err := m.bizPool.QueryRow(ctx,
		`SELECT ban_status::text FROM account_devices WHERE account_jid=$1`, jid).Scan(&banStatus); err != nil {
		t.Fatalf("query ban_status: %v", err)
	}
	if banStatus != "active" {
		t.Fatalf("ban_status = %q after re-enroll, want active (ON CONFLICT resets it)", banStatus)
	}
}

// TestEnrollDeviceForInstance_NoProxyID_NoRow: an instance with no proxy_id
// yet can never pass selectAccount's JOIN proxy_pool anyway, so enrollment
// must skip it (SELECT ... WHERE proxy_id IS NOT NULL) rather than create a
// row selectAccount could never pick.
func TestEnrollDeviceForInstance_NoProxyID_NoRow(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	ctx := context.Background()
	m := newTestManager(t)
	if err := m.UpsertInstance(ctx, InstanceRow{
		InstanceName: "wa_enroll_noproxy", TenantID: 7, EvoNode: "default", State: "connecting",
	}); err != nil {
		t.Fatalf("seed instance: %v", err)
	}
	jid := "222@s.whatsapp.net"
	if err := m.EnrollDeviceForInstance(ctx, "wa_enroll_noproxy", jid); err != nil {
		t.Fatalf("EnrollDeviceForInstance (no proxy): %v", err)
	}
	var count int
	if err := m.bizPool.QueryRow(ctx,
		`SELECT count(*) FROM account_devices WHERE account_jid=$1`, jid).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected no account_devices row when instance has no proxy_id, got %d", count)
	}
}

// TestEnrollDeviceForInstance_SelectAccountPicksIt is the whole point of
// FIX-3: after enrollment the account must actually be selectable to send.
// internal/dispatch/selectaccount.go's selectAccount is unexported in a
// different package, so this pins its SELECT verbatim (kept identical to
// that file's query) rather than reaching across the package boundary.
func TestEnrollDeviceForInstance_SelectAccountPicksIt(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	ctx := context.Background()
	m := newTestManager(t)

	proxyID := seedProxy(t, ctx, m.bizPool, "socks5://p3", "HK", 1)
	if err := m.UpsertInstance(ctx, InstanceRow{
		InstanceName: "wa_enroll_select", TenantID: 9, EvoNode: "default", State: "connecting", ProxyID: proxyID,
	}); err != nil {
		t.Fatalf("seed instance: %v", err)
	}
	jid := "85266188676@s.whatsapp.net"
	if err := m.EnrollDeviceForInstance(ctx, "wa_enroll_select", jid); err != nil {
		t.Fatalf("EnrollDeviceForInstance: %v", err)
	}

	var picked string
	err := m.bizPool.QueryRow(ctx, `
SELECT a.account_jid
  FROM account_devices a
  JOIN proxy_pool p ON p.id = a.proxy_id
 WHERE a.tenant_id = $1
   AND a.ban_status = 'active'
   AND (a.quarantined_until IS NULL OR a.quarantined_until < now())
   AND p.country_code = $2
   AND (effective_quota(a.registered_at, a.health_score) - a.sent_today) > 0
 ORDER BY (effective_quota(a.registered_at, a.health_score) - a.sent_today) DESC, random()
 LIMIT 1`, int64(9), "HK").Scan(&picked)
	if err != nil {
		t.Fatalf("selectAccount-equivalent query: %v", err)
	}
	if picked != jid {
		t.Fatalf("selectAccount would pick %q, want enrolled %q", picked, jid)
	}
}
