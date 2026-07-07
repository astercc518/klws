package api

import (
	"testing"
	"time"
)

func TestBuildLedgerWhere(t *testing.T) {
	// empty filter → no WHERE
	if w, args := buildLedgerWhere(ledgerFilter{}); w != "" || len(args) != 0 {
		t.Errorf("empty: where=%q args=%v", w, args)
	}
	// kind + tenant
	w, args := buildLedgerWhere(ledgerFilter{Kind: "topup", TenantID: 7})
	if w != " WHERE l.kind::text = $1 AND l.tenant_id = $2" {
		t.Errorf("where = %q", w)
	}
	if len(args) != 2 || args[0] != "topup" || args[1] != int64(7) {
		t.Errorf("args = %v", args)
	}
	// date range uses >= from and < to
	from := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 7, 8, 0, 0, 0, 0, time.UTC)
	w, args = buildLedgerWhere(ledgerFilter{From: from, To: to})
	if w != " WHERE l.created_at >= $1 AND l.created_at < $2" {
		t.Errorf("range where = %q", w)
	}
	if len(args) != 2 {
		t.Errorf("range args = %v", args)
	}
}

func TestParseBillRange(t *testing.T) {
	cn, _ := time.LoadLocation("Asia/Shanghai")

	// explicit range: To is exclusive (start of day after `to`)
	r, err := parseBillRange("2026-07-01", "2026-07-07")
	if err != nil {
		t.Fatalf("valid range err: %v", err)
	}
	wantFrom := time.Date(2026, 7, 1, 0, 0, 0, 0, cn)
	wantTo := time.Date(2026, 7, 8, 0, 0, 0, 0, cn)
	if !r.From.Equal(wantFrom) || !r.To.Equal(wantTo) {
		t.Errorf("range = %v..%v want %v..%v", r.From, r.To, wantFrom, wantTo)
	}
	// reversed → error
	if _, err := parseBillRange("2026-07-08", "2026-07-01"); err == nil {
		t.Error("reversed range should error")
	}
	// bad format → error
	if _, err := parseBillRange("07/01/2026", ""); err == nil {
		t.Error("bad format should error")
	}
	// oversized → error
	if _, err := parseBillRange("2020-01-01", "2026-01-01"); err == nil {
		t.Error("oversized range should error")
	}
	// empty defaults to a 30-day window (no error)
	if _, err := parseBillRange("", ""); err != nil {
		t.Errorf("default range err: %v", err)
	}
}

func TestBuildCampaignWhere(t *testing.T) {
	if w, _ := buildCampaignWhere(campaignFilter{}); w != "" {
		t.Errorf("empty where = %q", w)
	}
	w, args := buildCampaignWhere(campaignFilter{State: "running"})
	if w != " WHERE c.state::text = $1" || len(args) != 1 || args[0] != "running" {
		t.Errorf("state where=%q args=%v", w, args)
	}
	// numeric q matches id or tenant_id
	w, args = buildCampaignWhere(campaignFilter{Q: "42"})
	if w != " WHERE (c.id = $1 OR c.tenant_id = $1)" || args[0] != int64(42) {
		t.Errorf("numeric q where=%q args=%v", w, args)
	}
	// text q matches tenant name
	w, args = buildCampaignWhere(campaignFilter{Q: "acme"})
	if w != " WHERE t.name ILIKE $1" || args[0] != "%acme%" {
		t.Errorf("text q where=%q args=%v", w, args)
	}
}

func TestBuildRecipientWhere(t *testing.T) {
	if w, args := buildRecipientWhere(recipientFilter{}); w != "" || len(args) != 0 {
		t.Errorf("empty: where=%q args=%v", w, args)
	}
	w, args := buildRecipientWhere(recipientFilter{State: "sent"})
	if w != " WHERE r.state::text = $1" || len(args) != 1 || args[0] != "sent" {
		t.Errorf("state where=%q args=%v", w, args)
	}
	w, args = buildRecipientWhere(recipientFilter{TenantID: 7, CampaignID: 42})
	if w != " WHERE r.tenant_id = $1 AND r.campaign_id = $2" || len(args) != 2 || args[0] != int64(7) || args[1] != int64(42) {
		t.Errorf("tenant+campaign where=%q args=%v", w, args)
	}
	w, args = buildRecipientWhere(recipientFilter{Q: "555"})
	if w != " WHERE r.phone ILIKE $1" || len(args) != 1 || args[0] != "%555%" {
		t.Errorf("q where=%q args=%v", w, args)
	}
}
