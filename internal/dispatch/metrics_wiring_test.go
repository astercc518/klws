package dispatch

import (
	"context"
	"testing"
	"time"

	"github.com/acme/wadist/internal/metrics"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/client_golang/prometheus"
)

// setupAdmitDenyCase builds a quarantined-account scenario and returns the worker,
// context, SendPayload, and message body — mirroring TestProcessSend_AdmitDeniedRequeues.
func setupAdmitDenyCase(t *testing.T) (*SendWorker, context.Context, SendPayload, string) {
	t.Helper()
	w, ctx, _, br, _ := newWorker(t)
	mature := time.Now().Add(-30 * 24 * time.Hour)
	seedAccount(t, ctx, w.pool, "deny@s.whatsapp.net", "US", mature, 100, 0)
	// Quarantine so Admit always denies.
	w.pool.Exec(ctx, `UPDATE account_devices SET quarantined_until=now()+interval '1 hour' WHERE account_jid='deny@s.whatsapp.net'`)
	br.Topup(ctx, 1, 1000, "seed")
	cid := seedCampaign(t, ctx, w.pool, "hi")
	rid := seedRecipient(t, ctx, w.pool, cid, "1556", "US")
	w.pool.Exec(ctx, `UPDATE campaign_recipients SET assigned_jid='deny@s.whatsapp.net', state='pending' WHERE id=$1`, rid)
	pl := SendPayload{
		TenantID:   1,
		CampaignID: cid,
		RecipientID: rid,
		JID:        "deny@s.whatsapp.net",
		Phone:      "1556",
		Country:    "US",
		MessageID:  "m-deny",
		Vars:       map[string]any{},
	}
	return w, ctx, pl, "hi"
}

// setupBatchHappyCase builds a campaign with one ready recipient and a healthy account —
// mirroring TestDispatchBatch_AssignsBumpsEnqueues. Returns Dispatcher + campaignID.
func setupBatchHappyCase(t *testing.T) (*Dispatcher, context.Context, int64) {
	t.Helper()
	pool, ctx := pgPool(t)
	q := &fakeQueue{}
	d := NewDispatcher(pool, nil, q, func(string) int64 { return 5 }, time.Second)
	mature := time.Now().Add(-30 * 24 * time.Hour)
	seedAccount(t, ctx, pool, "batch@s.whatsapp.net", "US", mature, 100, 0)
	cid := seedCampaign(t, ctx, pool, "hello {{.name}}")
	seedRecipient(t, ctx, pool, cid, "15559998888", "US")
	return d, ctx, cid
}

// gateDeniedValue returns the sum of wadist_gate_decisions_total where allow="false".
func gateDeniedValue(t *testing.T, reg *prometheus.Registry) float64 {
	t.Helper()
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	var total float64
	for _, mf := range mfs {
		if mf.GetName() != "wadist_gate_decisions_total" {
			continue
		}
		for _, m := range mf.GetMetric() {
			if labelValue(m, "allow") == "false" {
				total += m.GetCounter().GetValue()
			}
		}
	}
	return total
}

// histogramSampleCount returns the sample count for the named histogram.
func histogramSampleCount(t *testing.T, reg *prometheus.Registry, name string) uint64 {
	t.Helper()
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
		for _, m := range mf.GetMetric() {
			if h := m.GetHistogram(); h != nil {
				return h.GetSampleCount()
			}
		}
	}
	return 0
}

func labelValue(m *dto.Metric, name string) string {
	for _, lp := range m.GetLabel() {
		if lp.GetName() == name {
			return lp.GetValue()
		}
	}
	return ""
}

// ---- Integration tests ----

func TestProcessSend_RecordsGateDenied(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	w, ctx, pl, body := setupAdmitDenyCase(t)
	reg := prometheus.NewRegistry()
	m := metrics.New(reg)
	w.WithMetrics(m)

	if err := w.ProcessSend(ctx, pl, body, "", "", nil); err != nil {
		t.Fatalf("ProcessSend: %v", err)
	}
	if v := gateDeniedValue(t, reg); v < 1 {
		t.Fatalf("expected gate_denied recorded, got %v", v)
	}
}

func TestDispatchBatch_ObservesAssigned(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	d, ctx, campaignID := setupBatchHappyCase(t)
	reg := prometheus.NewRegistry()
	m := metrics.New(reg)
	d.WithMetrics(m)

	n, err := d.dispatchBatch(ctx, campaignID, 10)
	if err != nil || n < 1 {
		t.Fatalf("dispatchBatch n=%d err=%v", n, err)
	}
	if c := histogramSampleCount(t, reg, "wadist_dispatch_batch_assigned"); c < 1 {
		t.Fatalf("expected batch histogram sample, got %d", c)
	}
}

// cohortSendValue returns the counter value for wadist_cohort_sends_total{cohort=c, outcome=o}.
func cohortSendValue(t *testing.T, reg *prometheus.Registry, cohort, outcome string) float64 {
	t.Helper()
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, mf := range mfs {
		if mf.GetName() != "wadist_cohort_sends_total" {
			continue
		}
		for _, m := range mf.GetMetric() {
			if labelValue(m, "cohort") == cohort && labelValue(m, "outcome") == outcome {
				return m.GetCounter().GetValue()
			}
		}
	}
	return 0
}

// TestProcessSend_RecordsCohortSend verifies that WithCanary(100) causes every
// successful send to be recorded with cohort="canary" in wadist_cohort_sends_total.
func TestProcessSend_RecordsCohortSend(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	w, ctx, _, br, _ := newWorker(t)
	mature := time.Now().Add(-30 * 24 * time.Hour)
	seedAccount(t, ctx, w.pool, "cohort@s.whatsapp.net", "US", mature, 100, 0)
	br.Topup(ctx, 1, 1000, "seed")
	cid := seedCampaign(t, ctx, w.pool, "hi")
	rid := seedRecipient(t, ctx, w.pool, cid, "15557", "US")
	pl := SendPayload{
		TenantID: 1, CampaignID: cid, RecipientID: rid,
		JID: "cohort@s.whatsapp.net", Phone: "15557", Country: "US",
		MessageID: "m-cohort", Vars: map[string]any{"name": "Ada"},
	}

	reg := prometheus.NewRegistry()
	m := metrics.New(reg)
	w.WithMetrics(m).WithCanary(100) // pct=100 → every JID is canary

	if err := w.ProcessSend(ctx, pl, "hi {{.name}}", "", "", nil); err != nil {
		t.Fatalf("ProcessSend: %v", err)
	}
	if v := cohortSendValue(t, reg, "canary", "sent"); v < 1 {
		t.Fatalf("expected wadist_cohort_sends_total{cohort=canary,outcome=sent}>=1, got %v", v)
	}
}
