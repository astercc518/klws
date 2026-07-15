// internal/api/instances_api.go
package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/acme/wadist/internal/cluster"
	"github.com/acme/wadist/internal/nodering"
	"github.com/acme/wadist/internal/store"
)

// ---------------------------------------------------------------------------
// Global instances (cross-tenant Evolution routing table, god view)
// ---------------------------------------------------------------------------

// adminInstanceRow is the JSON view of store.InstanceListRow.
type adminInstanceRow struct {
	InstanceName string  `json:"instance_name"`
	JID          *string `json:"jid"`
	TenantID     int64   `json:"tenant_id"`
	TenantName   string  `json:"tenant_name"`
	EvoNode      string  `json:"evo_node"`
	ProxyID      *int64  `json:"proxy_id"`
	State        string  `json:"state"`
	UpdatedAt    string  `json:"updated_at"`
}

// parseInstanceFilter reads the admin instance-list query params.
func parseInstanceFilter(c *gin.Context) store.InstanceFilter {
	f := store.InstanceFilter{State: c.Query("state"), Node: c.Query("node"), Q: c.Query("q")}
	if n, err := strconv.ParseInt(c.Query("tenant_id"), 10, 64); err == nil && n > 0 {
		f.TenantID = n
	}
	if n, err := strconv.Atoi(c.Query("limit")); err == nil {
		f.Limit = n
	}
	if n, err := strconv.Atoi(c.Query("offset")); err == nil && n > 0 {
		f.Offset = n
	}
	return f
}

// handleAdminListInstances: GET /admin/instances (all tenants, admin god
// view). Read-only routing-table listing over account_instances — it never
// reaches the Evolution cluster or any of the five engines.
func (s *Server) handleAdminListInstances(c *gin.Context) {
	ctx := c.Request.Context()
	f := parseInstanceFilter(c)
	rows, total, stats, err := s.deps.Mgr.ListInstances(ctx, f)
	if err != nil {
		fail(c, http.StatusInternalServerError, "list instances")
		return
	}
	out := make([]adminInstanceRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, adminInstanceRow{
			InstanceName: r.InstanceName,
			JID:          r.JID,
			TenantID:     r.TenantID,
			TenantName:   r.TenantName,
			EvoNode:      r.EvoNode,
			ProxyID:      r.ProxyID,
			State:        r.State,
			UpdatedAt:    r.UpdatedAt.UTC().Format(time.RFC3339),
		})
	}
	ok(c, gin.H{"rows": out, "total": total, "stats": stats})
}

// ---------------------------------------------------------------------------
// POST /admin/instances — create (zero-jid onboarding: auto-proxy + sharding)
// ---------------------------------------------------------------------------

// instanceEvoAPI is the narrow slice of *cluster.EvoClient that
// handleAdminCreateInstance needs (fake-testable without a real Evolution
// cluster/HTTP round-trip). Production is satisfied structurally by
// *cluster.EvoClient, reached via Deps.EvoCluster.For(node); tests inject a
// fake via Server.evoForFn.
type instanceEvoAPI interface {
	CreateInstance(ctx context.Context, instanceName, webhookURL string) error
	SetProxy(ctx context.Context, instanceName string, proxy *store.ProxyBinding) error
	DeleteInstance(ctx context.Context, instanceName string) error
	ConnectInstance(ctx context.Context, instanceName string) (string, error)
	FetchState(ctx context.Context, instanceName string) (string, error)
	LogoutInstance(ctx context.Context, instanceName string) error
}

// *cluster.EvoClient must satisfy instanceEvoAPI so production wiring
// compiles; asserted here so any EvoClient signature drift fails the build.
var _ instanceEvoAPI = (*cluster.EvoClient)(nil)

// evoNodes returns the set of Evolution node names to shard across.
// evoNodesFn, when set, overrides this — test-only (mirrors sysPool), so a
// fake single/multi-node topology can be exercised without a real EvoCluster.
func (s *Server) evoNodes() []string {
	if s.evoNodesFn != nil {
		return s.evoNodesFn()
	}
	if s.deps.EvoCluster == nil {
		return nil
	}
	return s.deps.EvoCluster.Nodes()
}

// evoClientFor resolves node to an instanceEvoAPI. evoForFn, when set,
// overrides this — test-only (mirrors sysPool/evoNodesFn). Production reaches
// Deps.EvoCluster.For(node).
func (s *Server) evoClientFor(node string) (instanceEvoAPI, bool) {
	if s.evoForFn != nil {
		return s.evoForFn(node)
	}
	if s.deps.EvoCluster == nil {
		return nil, false
	}
	return s.deps.EvoCluster.For(node)
}

// createInstanceRequest is the POST /admin/instances body.
type createInstanceRequest struct {
	TenantID    int64  `json:"tenant_id"`
	CountryCode string `json:"country_code"`
}

// randInstanceName returns "inst_<tenantID>_<8 hex chars>" — an
// Evolution-facing instance name that carries no PII and cannot collide with
// a real WhatsApp jid (account_instances.jid stays NULL until QR pairing
// backfills it via BindInstanceJID). Uses crypto/rand: this name is a durable
// routing key, not a display token, so a predictable generator is unacceptable.
func randInstanceName(tenantID int64) (string, error) {
	buf := make([]byte, 4)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate instance name: %w", err)
	}
	return fmt.Sprintf("inst_%d_%s", tenantID, hex.EncodeToString(buf)), nil
}

// handleAdminCreateInstance: POST /admin/instances {tenant_id, country_code}
// → {instance_name, evo_node, proxy_id, state}.
//
// This is the system's first "zero-jid" onboarding path: unlike the
// jid-keyed sticky-proxy flow the worker's SessionFactory uses
// (buildEvoSession in cmd/wadist/main.go, backed by store.BindProxy/
// GetBoundProxy against account_devices), there is no account row yet here —
// so proxy allocation goes through store.BindInstanceProxy/ReleaseInstanceProxy
// (pool-only, account_instances-scoped) instead of BindProxy/ReleaseProxy
// (account_devices-scoped; see internal/store/instances.go doc comment for
// why BindProxy cannot be reused here — it structurally requires a
// pre-existing account_devices row and returns ErrAccountMissing otherwise).
//
// Fail-closed at every step: no proxy, no node capacity, no Evolution client,
// or a failed Evolution call all abort BEFORE any account_instances row is
// written, and a failure AFTER the proxy was allocated (SetProxy) rolls back
// via best-effort DeleteInstance + ReleaseInstanceProxy — no proxyless
// instance is ever left registered.
func (s *Server) handleAdminCreateInstance(c *gin.Context) {
	ctx := c.Request.Context()

	var req createInstanceRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.TenantID <= 0 || req.CountryCode == "" {
		fail(c, http.StatusBadRequest, "tenant_id and country_code are required")
		return
	}

	if _, err := s.deps.Tenants.Get(ctx, req.TenantID); err != nil {
		fail(c, http.StatusBadRequest, "tenant not found")
		return
	}

	name, err := randInstanceName(req.TenantID)
	if err != nil {
		fail(c, http.StatusInternalServerError, "generate instance name")
		return
	}

	binding, err := s.deps.Mgr.BindInstanceProxy(ctx, req.CountryCode)
	if err != nil {
		if errors.Is(err, store.ErrNoProxyAvailable) {
			fail(c, http.StatusPaymentRequired, "no available proxy for country "+req.CountryCode)
			return
		}
		fail(c, http.StatusInternalServerError, "allocate proxy")
		return
	}

	counts, err := s.deps.Mgr.NodeCounts(ctx)
	if err != nil {
		_ = s.deps.Mgr.ReleaseInstanceProxy(ctx, binding.ProxyID, binding.Country)
		fail(c, http.StatusInternalServerError, "load node counts")
		return
	}
	nodes := s.evoNodes()
	ring := nodering.New(200, nodes...)
	node, assigned := nodering.AssignNode(ring, counts, name, s.deps.EvoCapPerNode)
	if !assigned {
		_ = s.deps.Mgr.ReleaseInstanceProxy(ctx, binding.ProxyID, binding.Country)
		msg := "all Evolution nodes are at capacity"
		if len(nodes) == 0 {
			msg = "no Evolution nodes configured"
		}
		fail(c, http.StatusConflict, msg)
		return
	}

	client, found := s.evoClientFor(node)
	if !found {
		_ = s.deps.Mgr.ReleaseInstanceProxy(ctx, binding.ProxyID, binding.Country)
		fail(c, http.StatusServiceUnavailable, "no Evolution client configured for node "+node)
		return
	}

	if err := client.CreateInstance(ctx, name, s.deps.EvolutionWebhookURL); err != nil {
		_ = s.deps.Mgr.ReleaseInstanceProxy(ctx, binding.ProxyID, binding.Country)
		fail(c, http.StatusBadGateway, "evolution create instance failed")
		return
	}

	if err := client.SetProxy(ctx, name, binding); err != nil {
		_ = client.DeleteInstance(ctx, name) // best-effort: instance was just created, nothing else references it yet
		_ = s.deps.Mgr.ReleaseInstanceProxy(ctx, binding.ProxyID, binding.Country)
		fail(c, http.StatusBadGateway, "evolution set proxy failed, refusing to leave instance proxyless")
		return
	}

	if err := s.deps.Mgr.UpsertInstance(ctx, store.InstanceRow{
		InstanceName: name,
		TenantID:     req.TenantID,
		EvoNode:      node,
		ProxyID:      binding.ProxyID,
		State:        "created",
	}); err != nil {
		_ = client.DeleteInstance(ctx, name) // best-effort
		_ = s.deps.Mgr.ReleaseInstanceProxy(ctx, binding.ProxyID, binding.Country)
		fail(c, http.StatusInternalServerError, "persist instance")
		return
	}

	s.recordAudit(ctx, auditEvent{
		TenantID:     req.TenantID,
		ActorID:      actorID(c),
		Action:       "instance.create",
		ResourceType: "account_instance",
		Details: gin.H{
			"instance_name": name,
			"evo_node":      node,
			"proxy_id":      binding.ProxyID,
			"country_code":  req.CountryCode,
		},
	})

	ok(c, gin.H{
		"instance_name": name,
		"evo_node":      node,
		"proxy_id":      binding.ProxyID,
		"state":         "created",
	})
}

// ---------------------------------------------------------------------------
// Instance lifecycle: qr / state / reconnect / logout / delete
// ---------------------------------------------------------------------------

// getInstanceRow looks up account_instances by :name and writes a 404 when
// absent (or a 500 on a store error), returning ok=false either way so
// callers can `return` immediately. Shared 404 guard for all five lifecycle
// endpoints below.
func (s *Server) getInstanceRow(c *gin.Context, name string) (store.InstanceRow, bool) {
	row, found, err := s.deps.Mgr.GetInstance(c.Request.Context(), name)
	if err != nil {
		fail(c, http.StatusInternalServerError, "load instance")
		return store.InstanceRow{}, false
	}
	if !found {
		fail(c, http.StatusNotFound, "instance not found")
		return store.InstanceRow{}, false
	}
	return row, true
}

// evoClientForRow resolves row.EvoNode to an instanceEvoAPI, writing a 503
// when no client is configured for that node. Shared by all five lifecycle
// endpoints after the getInstanceRow 404 guard.
func (s *Server) evoClientForRow(c *gin.Context, row store.InstanceRow) (instanceEvoAPI, bool) {
	client, found := s.evoClientFor(row.EvoNode)
	if !found {
		fail(c, http.StatusServiceUnavailable, "no Evolution client configured for node "+row.EvoNode)
		return nil, false
	}
	return client, true
}

// handleAdminInstanceQR: GET /admin/instances/:name/qr → {base64}. Read-only
// (no audit).
//
// Evolution v2.3.7's synchronous GET /instance/connect/{name} response
// carries NO base64 (verified live: it returns only
// {"count":N,"pairingCode":null}) — the actual QR image arrives later,
// asynchronously, via the qrcode.updated webhook event, which
// webhook_evolution.go writes into s.deps.QRCache. So this handler still
// calls ConnectInstance to (re-)trigger Evolution's pairing/QR generation as
// a side effect, but the base64 it returns comes from the cache, not from
// ConnectInstance's own return value — EXCEPT as a best-effort fallback: if
// ConnectInstance itself ever does return a non-empty base64 (e.g. a future
// Evolution version, or a race where Evolution answers synchronously), that
// value is seeded into the cache too, so it's not lost.
func (s *Server) handleAdminInstanceQR(c *gin.Context) {
	ctx := c.Request.Context()
	row, ok1 := s.getInstanceRow(c, c.Param("name"))
	if !ok1 {
		return
	}
	client, ok2 := s.evoClientForRow(c, row)
	if !ok2 {
		return
	}
	b64, err := client.ConnectInstance(ctx, row.InstanceName)
	if err != nil {
		fail(c, http.StatusBadGateway, "evolution connect instance failed")
		return
	}
	if b64 != "" {
		s.deps.QRCache.Set(row.InstanceName, b64)
	}
	ok(c, gin.H{"base64": s.deps.QRCache.Get(row.InstanceName)})
}

// handleAdminInstanceState: GET /admin/instances/:name/state → {state}.
// Read-only passthrough of Evolution's live connection state (no audit).
func (s *Server) handleAdminInstanceState(c *gin.Context) {
	ctx := c.Request.Context()
	row, ok1 := s.getInstanceRow(c, c.Param("name"))
	if !ok1 {
		return
	}
	client, ok2 := s.evoClientForRow(c, row)
	if !ok2 {
		return
	}
	state, err := client.FetchState(ctx, row.InstanceName)
	if err != nil {
		fail(c, http.StatusBadGateway, "evolution fetch state failed")
		return
	}
	ok(c, gin.H{"state": state})
}

// handleAdminInstanceReconnect: POST /admin/instances/:name/reconnect →
// {base64}. Re-triggers pairing (same Evolution call as QR) — audited because,
// unlike the read-only QR fetch, this is an explicit admin action to force a
// reconnect attempt on a possibly-connected instance.
func (s *Server) handleAdminInstanceReconnect(c *gin.Context) {
	ctx := c.Request.Context()
	row, ok1 := s.getInstanceRow(c, c.Param("name"))
	if !ok1 {
		return
	}
	client, ok2 := s.evoClientForRow(c, row)
	if !ok2 {
		return
	}
	b64, err := client.ConnectInstance(ctx, row.InstanceName)
	if err != nil {
		fail(c, http.StatusBadGateway, "evolution connect instance failed")
		return
	}
	s.recordAudit(ctx, auditEvent{
		TenantID:     row.TenantID,
		ActorID:      actorID(c),
		Action:       "instance.reconnect",
		ResourceType: "account_instance",
		Details:      gin.H{"instance_name": row.InstanceName, "evo_node": row.EvoNode},
	})
	ok(c, gin.H{"base64": b64})
}

// handleAdminInstanceLogout: POST /admin/instances/:name/logout. Ends the WA
// session via Evolution (terminal), then marks the routing row loggedOut via
// UpsertInstance (mirrors handleAdminCreateInstance's write path) and audits.
func (s *Server) handleAdminInstanceLogout(c *gin.Context) {
	ctx := c.Request.Context()
	row, ok1 := s.getInstanceRow(c, c.Param("name"))
	if !ok1 {
		return
	}
	client, ok2 := s.evoClientForRow(c, row)
	if !ok2 {
		return
	}
	if err := client.LogoutInstance(ctx, row.InstanceName); err != nil {
		fail(c, http.StatusBadGateway, "evolution logout instance failed")
		return
	}
	if err := s.deps.Mgr.UpsertInstance(ctx, store.InstanceRow{
		InstanceName: row.InstanceName,
		JID:          row.JID,
		TenantID:     row.TenantID,
		EvoNode:      row.EvoNode,
		ProxyID:      row.ProxyID,
		State:        "loggedOut",
	}); err != nil {
		fail(c, http.StatusInternalServerError, "persist logged-out state")
		return
	}
	s.recordAudit(ctx, auditEvent{
		TenantID:     row.TenantID,
		ActorID:      actorID(c),
		Action:       "instance.logout",
		ResourceType: "account_instance",
		Details:      gin.H{"instance_name": row.InstanceName, "evo_node": row.EvoNode},
	})
	ok(c, gin.H{"instance_name": row.InstanceName, "state": "loggedOut"})
}

// handleAdminInstanceDelete: DELETE /admin/instances/:name. Ordering is
// load-bearing: Evolution DeleteInstance MUST succeed before the
// account_instances row is removed or the proxy released — a failed
// Evolution call aborts here (500) and leaves the row + proxy binding
// intact, so a retry is always possible and no Evolution-side instance is
// ever orphaned (deleted from our routing table while still live upstream).
//
// Exception: Evolution returning 404 means the instance is already gone
// upstream (a prior delete succeeded there but our row survived, or it was
// cleaned up manually) — that is the terminal state a delete is trying to
// reach, so it's treated as success and we fall through to release the
// proxy + remove our row, instead of leaving an un-deletable row forever.
func (s *Server) handleAdminInstanceDelete(c *gin.Context) {
	ctx := c.Request.Context()
	row, ok1 := s.getInstanceRow(c, c.Param("name"))
	if !ok1 {
		return
	}
	client, ok2 := s.evoClientForRow(c, row)
	if !ok2 {
		return
	}
	if err := client.DeleteInstance(ctx, row.InstanceName); err != nil {
		var he *cluster.EvoHTTPError
		if !(errors.As(err, &he) && he.StatusCode == http.StatusNotFound) {
			fail(c, http.StatusInternalServerError, "evolution delete instance failed, refusing to remove routing row")
			return
		}
	}
	if err := s.deps.Mgr.ReleaseInstanceProxyByID(ctx, row.ProxyID); err != nil {
		fail(c, http.StatusInternalServerError, "release instance proxy")
		return
	}
	if err := s.deps.Mgr.DeleteInstanceRow(ctx, row.InstanceName); err != nil {
		fail(c, http.StatusInternalServerError, "delete instance row")
		return
	}
	s.recordAudit(ctx, auditEvent{
		TenantID:     row.TenantID,
		ActorID:      actorID(c),
		Action:       "instance.delete",
		ResourceType: "account_instance",
		Details:      gin.H{"instance_name": row.InstanceName, "evo_node": row.EvoNode, "proxy_id": row.ProxyID},
	})
	ok(c, gin.H{"instance_name": row.InstanceName, "deleted": true})
}

// ---------------------------------------------------------------------------
// GET /admin/nodes — per-node Evolution capacity view (read-only, admin god
// view). Never touches the Evolution cluster or any of the five engines —
// purely a JSON view over store.NodeCounts (account_instances GROUP BY
// evo_node) plus the fleet-wide cap already threaded into Deps by T1
// (Deps.EvoCapPerNode = cfg.EvolutionCapPerNode).
// ---------------------------------------------------------------------------

// adminNodeRow is the JSON view of one node's capacity: how many instances
// are pinned to it, the shared per-node cap, and the resulting utilization
// percentage.
type adminNodeRow struct {
	Node  string  `json:"node"`
	Count int     `json:"count"`
	Cap   int     `json:"cap"`
	Pct   float64 `json:"pct"`
}

// nodePct computes count/cap*100 rounded to one decimal place. cap<=0 (unset
// or misconfigured) returns 0 rather than dividing by zero — nodering.AssignNode
// treats cap<=0 the same way (capacity-unlimited-is-not-the-default; an
// unconfigured cap must never look like 0% headroom via a NaN/Inf render).
func nodePct(count, capacity int) float64 {
	if capacity <= 0 {
		return 0
	}
	return math.Round(float64(count)/float64(capacity)*1000) / 10
}

// handleAdminListNodes: GET /admin/nodes → [{node, count, cap, pct}], sorted
// by node name for a stable/testable ordering. Read-only: store.NodeCounts is
// a plain GROUP BY over account_instances (SystemPool, fleet-wide — no tenant
// scoping, same as the sharding path it also backs in
// handleAdminCreateInstance).
func (s *Server) handleAdminListNodes(c *gin.Context) {
	ctx := c.Request.Context()
	counts, err := s.deps.Mgr.NodeCounts(ctx)
	if err != nil {
		fail(c, http.StatusInternalServerError, "load node counts")
		return
	}
	nodeCap := s.deps.EvoCapPerNode
	out := make([]adminNodeRow, 0, len(counts))
	for node, count := range counts {
		out = append(out, adminNodeRow{Node: node, Count: count, Cap: nodeCap, Pct: nodePct(count, nodeCap)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Node < out[j].Node })
	ok(c, out)
}
