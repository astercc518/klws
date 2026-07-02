// Package metrics holds all Prometheus collectors for the distribution system.
// Label values are STRICTLY bounded enums — never jid/tenant_id/message_id/phone
// (enforced by scripts/check_metric_labels.sh). All collectors register on an
// injected registry so tests stay isolated and there is no global state.
package metrics

import "github.com/prometheus/client_golang/prometheus"

// RegistrySnapshotProvider is satisfied by the M8 account Registry. M7 wires a
// nil provider; the DBCollector treats nil as "0 active sessions".
type RegistrySnapshotProvider interface {
	ActiveSessions() int
}

type Metrics struct {
	reg *prometheus.Registry

	sendOutcomes        *prometheus.CounterVec // label: outcome
	gateDecisions       *prometheus.CounterVec // labels: allow, reason
	healthSignals       *prometheus.CounterVec // label: signal
	billingOps          *prometheus.CounterVec // labels: op, outcome
	batchAssigned       prometheus.Histogram
	noCapacity          prometheus.Counter
	proxyRebind         prometheus.Counter
	scheduleReconciled  prometheus.Counter
	proxyOps            *prometheus.CounterVec // labels: op, outcome
	lockOps             *prometheus.CounterVec // label: outcome
	cohortSends         *prometheus.CounterVec // labels: cohort, outcome
	ownershipDivergence *prometheus.CounterVec // label: op
}

func New(reg *prometheus.Registry) *Metrics {
	m := &Metrics{reg: reg}
	m.sendOutcomes = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "wadist_send_outcomes_total",
		Help: "Send pipeline outcomes by terminal classification.",
	}, []string{"outcome"})
	m.gateDecisions = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "wadist_gate_decisions_total",
		Help: "Admission gate decisions.",
	}, []string{"allow", "reason"})
	m.healthSignals = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "wadist_health_signals_total",
		Help: "Applied account health signals.",
	}, []string{"signal"})
	m.billingOps = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "wadist_billing_ops_total",
		Help: "Billing operations by op and outcome.",
	}, []string{"op", "outcome"})
	m.batchAssigned = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "wadist_dispatch_batch_assigned",
		Help:    "Recipients assigned per dispatchBatch call.",
		Buckets: []float64{0, 1, 5, 10, 25, 50, 100, 250, 500},
	})
	m.noCapacity = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "wadist_dispatch_no_capacity_total",
		Help: "Recipients left pending due to ErrNoCapacity.",
	})
	m.proxyRebind = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "wadist_proxy_rebind_total",
		Help: "Accounts auto-rebound off a dead proxy by the janitor.",
	})
	m.scheduleReconciled = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "wadist_schedule_reconciled_total",
		Help: "Active accounts re-enqueued into the due-ZSet by the reconciler.",
	})
	m.proxyOps = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "wadist_proxy_ops_total",
		Help: "Proxy pool operations by op and outcome.",
	}, []string{"op", "outcome"})
	m.lockOps = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "wadist_lock_ops_total",
		Help: "Advisory device-lock acquisition outcomes.",
	}, []string{"outcome"})
	m.cohortSends = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "wadist_cohort_sends_total",
		Help: "Send outcomes split by canary cohort (cohort: canary|stable).",
	}, []string{"cohort", "outcome"})
	m.ownershipDivergence = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "wadist_ownership_shadow_divergence_total",
		Help: "shadow-mode ownership decision divergences",
	}, []string{"op"})

	reg.MustRegister(
		m.sendOutcomes, m.gateDecisions, m.healthSignals, m.billingOps,
		m.batchAssigned, m.noCapacity, m.proxyRebind, m.scheduleReconciled, m.proxyOps, m.lockOps, m.cohortSends,
		m.ownershipDivergence,
	)
	return m
}

func (m *Metrics) Registry() *prometheus.Registry { return m.reg }

// ---- nil-safe recorders (business packages inject *Metrics via WithMetrics) ----

func (m *Metrics) RecordSend(outcome string) {
	if m == nil {
		return
	}
	m.sendOutcomes.WithLabelValues(outcome).Inc()
}

func (m *Metrics) RecordGate(allow bool, reason string) {
	if m == nil {
		return
	}
	a := "false"
	if allow {
		a = "true"
	}
	m.gateDecisions.WithLabelValues(a, reason).Inc()
}

func (m *Metrics) RecordHealthSignal(signal string) {
	if m == nil {
		return
	}
	m.healthSignals.WithLabelValues(signal).Inc()
}

func (m *Metrics) RecordBilling(op, outcome string) {
	if m == nil {
		return
	}
	m.billingOps.WithLabelValues(op, outcome).Inc()
}

func (m *Metrics) ObserveBatch(assigned int) {
	if m == nil {
		return
	}
	m.batchAssigned.Observe(float64(assigned))
}

func (m *Metrics) IncNoCapacity() {
	if m == nil {
		return
	}
	m.noCapacity.Inc()
}

func (m *Metrics) IncProxyRebind() {
	if m == nil {
		return
	}
	m.proxyRebind.Inc()
}

func (m *Metrics) IncScheduleReconciled(n int) {
	if m == nil || n <= 0 {
		return
	}
	m.scheduleReconciled.Add(float64(n))
}

func (m *Metrics) RecordProxy(op, outcome string) {
	if m == nil {
		return
	}
	m.proxyOps.WithLabelValues(op, outcome).Inc()
}

func (m *Metrics) RecordLock(outcome string) {
	if m == nil {
		return
	}
	m.lockOps.WithLabelValues(outcome).Inc()
}

func (m *Metrics) RecordCohortSend(cohort, outcome string) {
	if m == nil {
		return
	}
	m.cohortSends.WithLabelValues(cohort, outcome).Inc()
}

func (m *Metrics) ObserveOwnershipDivergence(op string) {
	if m == nil {
		return
	}
	m.ownershipDivergence.WithLabelValues(op).Inc()
}
