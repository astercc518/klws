package metrics

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
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
	for _, want := range []string{
		"wadist_send_outcomes_total",
		"wadist_gate_decisions_total",
		"wadist_health_signals_total",
		"wadist_billing_ops_total",
		"wadist_dispatch_batch_assigned",
		"wadist_dispatch_no_capacity_total",
		"wadist_proxy_ops_total",
		"wadist_lock_ops_total",
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
	m.RecordProxy("release", "ok")
	m.RecordLock("acquired")
}
