// internal/api/instances_api.go
package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
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
