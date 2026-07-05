package metrics

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
)

func TestNew_RegistersAndRecords(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := New(reg)
	if m.Registry() != reg {
		t.Fatal("Registry() mismatch")
	}
	// 有界 label 记录，不得 panic
	m.RecordSend("sent")
	m.RecordGate(false, "daily_quota")
	m.RecordHealthSignal("wa_warning")
	m.RecordBilling("hold", "insufficient_funds")
	m.ObserveBatch(7)
	m.IncNoCapacity()
	m.RecordProxy("bind", "ok")
	m.RecordLock("contended")

	mfs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, mf := range mfs {
		names[mf.GetName()] = true
	}
	// Exercise the new cohort metric.
	m.RecordCohortSend("canary", "sent")
	m.RecordCohortSend("stable", "send_failed")

	mfs, _ = reg.Gather() // re-gather after new recordings
	names = map[string]bool{}
	for _, mf := range mfs {
		names[mf.GetName()] = true
	}

	for _, want := range []string{
		"wadist_send_outcomes_total",
		"wadist_gate_decisions_total",
		"wadist_health_signals_total",
		"wadist_billing_ops_total",
		"wadist_dispatch_batch_assigned",
		"wadist_dispatch_no_capacity_total",
		"wadist_proxy_rebind_total",
		"wadist_schedule_reconciled_total",
		"wadist_proxy_ops_total",
		"wadist_lock_ops_total",
		"wadist_cohort_sends_total",
	} {
		if !names[want] {
			t.Errorf("missing metric %q", want)
		}
	}
	_ = dto.MetricType_COUNTER
	_ = strings.TrimSpace
}

func TestNilSafe(t *testing.T) {
	var m *Metrics // nil
	// 所有记录方法在 nil receiver 下必须安全 no-op
	m.RecordSend("sent")
	m.RecordGate(true, "ok")
	m.RecordHealthSignal("delivered")
	m.RecordBilling("settle", "ok")
	m.ObserveBatch(1)
	m.IncNoCapacity()
	m.IncProxyRebind()
	m.IncScheduleReconciled(1)
	m.RecordProxy("release", "ok")
	m.RecordLock("acquired")
	m.RecordCohortSend("canary", "sent")
}

func TestIncProxyRebind(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := New(reg)
	m.IncProxyRebind()
	m.IncProxyRebind()
	if got := testutil.ToFloat64(m.proxyRebind); got != 2 {
		t.Fatalf("proxy_rebind=%v want 2", got)
	}
}

func TestIncScheduleReconciled(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := New(reg)
	m.IncScheduleReconciled(3)
	m.IncScheduleReconciled(2)
	if got := testutil.ToFloat64(m.scheduleReconciled); got != 5 {
		t.Fatalf("schedule_reconciled=%v want 5", got)
	}
}

func TestIncGhostReaped(t *testing.T) {
	m := New(prometheus.NewRegistry())
	m.IncGhostReaped("transient")
	m.IncGhostReaped("transient")
	m.IncGhostReaped("permanent")
	if got := testutil.ToFloat64(m.ghostReaped.WithLabelValues("transient")); got != 2 {
		t.Fatalf("transient=%v; want 2", got)
	}
	if got := testutil.ToFloat64(m.ghostReaped.WithLabelValues("permanent")); got != 1 {
		t.Fatalf("permanent=%v; want 1", got)
	}
}

func TestSetGhostInFlight(t *testing.T) {
	m := New(prometheus.NewRegistry())
	m.SetGhostInFlight(7)
	if got := testutil.ToFloat64(m.ghostInFlight); got != 7 {
		t.Fatalf("in-flight=%v; want 7", got)
	}
	m.SetGhostInFlight(0)
	if got := testutil.ToFloat64(m.ghostInFlight); got != 0 {
		t.Fatalf("in-flight=%v; want 0", got)
	}
}
