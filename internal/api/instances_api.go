// internal/api/instances_api.go
package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

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
