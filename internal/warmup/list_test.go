package warmup

import (
	"testing"
	"time"
)

func TestStoreListJoinsDevice(t *testing.T) {
	pool, ctx := pgPool(t)
	st := NewStore(pool)
	now := time.Now().UTC()
	// 造一个 device + proxy(照 dispatch/testsupport seedAccount 的写法)。
	var proxyID int64
	if err := pool.QueryRow(ctx, `INSERT INTO proxy_pool (proxy_url,proxy_type,country_code,max_bindings,current_bindings)
		VALUES ('socks5://w','socks5','US',1,1) RETURNING id`).Scan(&proxyID); err != nil {
		t.Fatalf("seed proxy: %v", err)
	}
	_, err := pool.Exec(ctx, `INSERT INTO account_devices
		(tenant_id,account_jid,phone_number,ban_status,proxy_id,registered_at,health_score,sent_today)
		VALUES (1,'w@s.whatsapp.net','15550001111','active',$1,now()-interval '10 days',80,3)`, proxyID)
	if err != nil {
		t.Fatalf("seed device: %v", err)
	}
	_ = st.EnrollIfAbsent(ctx, "w@s.whatsapp.net", 1, LaneStandard, now)

	rows, total, err := st.List(ctx, ListFilter{Limit: 10})
	if err != nil || total != 1 || len(rows) != 1 {
		t.Fatalf("list want 1 got %d/%d err=%v", len(rows), total, err)
	}
	r := rows[0]
	if r.Health != 80 || r.Stage != StageWarming {
		t.Fatalf("row join wrong: %+v", r)
	}
	if r.BusinessQuotaRemaining <= 0 {
		t.Fatalf("10-day/health80 account should have business quota remaining, got %d", r.BusinessQuotaRemaining)
	}
}

func TestStoreOverviewAndPolicies(t *testing.T) {
	pool, ctx := pgPool(t)
	st := NewStore(pool)
	now := time.Now().UTC()

	if err := st.EnrollIfAbsent(ctx, "ov1@s.whatsapp.net", 1, LaneStandard, now); err != nil {
		t.Fatalf("enroll: %v", err)
	}
	if err := st.EnrollIfAbsent(ctx, "ov2@s.whatsapp.net", 1, LaneFast, now); err != nil {
		t.Fatalf("enroll: %v", err)
	}

	o, err := st.Overview(ctx)
	if err != nil {
		t.Fatalf("overview: %v", err)
	}
	if o.Warming != 2 {
		t.Fatalf("want 2 warming, got %+v", o)
	}

	pols, err := st.ListPolicies(ctx)
	if err != nil {
		t.Fatalf("list policies: %v", err)
	}
	if len(pols) != 2 {
		t.Fatalf("want 2 seeded lanes, got %d", len(pols))
	}

	p := pols[LaneStandard]
	p.WarmingCap = 99
	if err := st.UpsertPolicy(ctx, LaneStandard, p, now); err != nil {
		t.Fatalf("upsert policy: %v", err)
	}
	got, err := st.PolicyFor(ctx, LaneStandard)
	if err != nil {
		t.Fatalf("policy for: %v", err)
	}
	if got.WarmingCap != 99 {
		t.Fatalf("want warming_cap=99, got %+v", got)
	}
}
