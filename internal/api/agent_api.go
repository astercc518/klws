// internal/api/agent_api.go — agent-subtree access control + tree management.
//
// Agents are STAFF console_users (role='sales'), not tenants: their handlers
// use the BYPASSRLS SystemPool plus explicit subtree scoping via subtreeCTE
// (see agent_query.go), not WithTenant/RLS. Mirrors the pattern in
// sales_api.go (SystemPool + WHERE t.sales_owner_id = $1 with the session's
// UserID as the agent id).
package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"

	"github.com/acme/wadist/internal/console"
)

// errAgentNotFound signals a target agent id that doesn't exist (or isn't a
// sales-role agent), so the transactional handlers below can report 404
// instead of a generic 500.
var errAgentNotFound = errors.New("agent not found")

// agentInSubtree reports whether targetAgentID is rootAgentID itself or one
// of its descendants (via console_users.parent_id). Used to reject parent
// reassignments that would create a cycle.
func (s *Server) agentInSubtree(ctx context.Context, rootAgentID, targetAgentID int64) (bool, error) {
	var in bool
	err := s.systemPool().QueryRow(ctx,
		subtreeCTE("$1")+`
		SELECT EXISTS (SELECT 1 FROM agent_tree WHERE id = $2)`,
		rootAgentID, targetAgentID).Scan(&in)
	return in, err
}

// tenantInSubtree reports whether tenantID's sales_owner_id is rootAgentID or
// one of rootAgentID's descendant agents.
func (s *Server) tenantInSubtree(ctx context.Context, rootAgentID, tenantID int64) (bool, error) {
	var in bool
	err := s.systemPool().QueryRow(ctx,
		subtreeCTE("$1")+`
		SELECT EXISTS (SELECT 1 FROM tenants t
		  WHERE t.id = $2 AND t.sales_owner_id IN (SELECT id FROM agent_tree))`,
		rootAgentID, tenantID).Scan(&in)
	return in, err
}

// handleAgentCreateSubAgent: POST /api/v1/sales/sub-agents {email,password}.
// Creates a role='sales' console_user (tenant_id NULL, per the
// console_users_tenant_role CHECK) with parent_id = the caller's own id.
func (s *Server) handleAgentCreateSubAgent(c *gin.Context) {
	me := sessionFrom(c)
	if me == nil {
		fail(c, http.StatusUnauthorized, "no session")
		return
	}
	var req struct {
		Email    string `json:"email" binding:"required"`
		Password string `json:"password" binding:"required,min=8"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid request body (email, password>=8 required)")
		return
	}
	hash, err := console.HashPassword(req.Password)
	if err != nil {
		fail(c, http.StatusInternalServerError, "hash password failed")
		return
	}
	ctx := c.Request.Context()
	var id int64
	err = pgx.BeginTxFunc(ctx, s.systemPool(), pgx.TxOptions{}, func(tx pgx.Tx) error {
		if e := tx.QueryRow(ctx,
			`INSERT INTO console_users (email, password_hash, role, tenant_id, parent_id)
			 VALUES ($1, $2, 'sales', NULL, $3) RETURNING id`,
			req.Email, hash, me.UserID).Scan(&id); e != nil {
			return e
		}
		return s.recordAuditTx(ctx, tx, auditEvent{TenantID: 0, ActorID: actorID(c),
			Action: "agent.sub_agent_create", ResourceType: "agent", ResourceID: id,
			Details: map[string]any{"email": req.Email, "parent_id": me.UserID}})
	})
	if err != nil {
		fail(c, http.StatusConflict, "create sub-agent failed (email may already exist)")
		return
	}
	ok(c, gin.H{"id": id, "email": req.Email, "parent_id": me.UserID})
}

// handleAdminSetAgentParent: POST /api/v1/admin/agents/:id/parent {parent_id}.
// parent_id may be null to detach the agent to a root. Cycle-safe: rejects
// (400) if the requested new parent already sits inside the target agent's
// own subtree, which would otherwise create a cycle.
func (s *Server) handleAdminSetAgentParent(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		fail(c, http.StatusBadRequest, "invalid agent id")
		return
	}
	var req struct {
		ParentID *int64 `json:"parent_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid body")
		return
	}
	ctx := c.Request.Context()
	if req.ParentID != nil {
		if *req.ParentID == id {
			fail(c, http.StatusBadRequest, "agent cannot be its own parent")
			return
		}
		cyclic, err := s.agentInSubtree(ctx, id, *req.ParentID)
		if err != nil {
			fail(c, http.StatusInternalServerError, "cycle check failed")
			return
		}
		if cyclic {
			fail(c, http.StatusBadRequest, "would create a cycle: new parent is in this agent's own subtree")
			return
		}
	}
	err = pgx.BeginTxFunc(ctx, s.systemPool(), pgx.TxOptions{}, func(tx pgx.Tx) error {
		tag, e := tx.Exec(ctx,
			`UPDATE console_users SET parent_id=$1 WHERE id=$2 AND role='sales'`,
			req.ParentID, id)
		if e != nil {
			return e
		}
		if tag.RowsAffected() == 0 {
			return errAgentNotFound
		}
		return s.recordAuditTx(ctx, tx, auditEvent{TenantID: 0, ActorID: actorID(c),
			Action: "agent.set_parent", ResourceType: "agent", ResourceID: id,
			Details: map[string]any{"parent_id": req.ParentID}})
	})
	switch {
	case errors.Is(err, errAgentNotFound):
		fail(c, http.StatusNotFound, "agent not found")
		return
	case err != nil:
		fail(c, http.StatusInternalServerError, "set parent failed")
		return
	}
	ok(c, gin.H{"id": id, "parent_id": req.ParentID})
}

// handleAdminSetAgentTerms: POST /api/v1/admin/agents/:id/terms
// {commission_rate?, credit_limit?} — sets the agent's rebate rate
// (commission_rate) and/or credit_limit. Either field may be omitted to
// leave it unchanged.
func (s *Server) handleAdminSetAgentTerms(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		fail(c, http.StatusBadRequest, "invalid agent id")
		return
	}
	var req struct {
		CommissionRate *float64 `json:"commission_rate"`
		CreditLimit    *int64   `json:"credit_limit"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid body")
		return
	}
	if req.CommissionRate != nil && (*req.CommissionRate < 0 || *req.CommissionRate > 1) {
		fail(c, http.StatusBadRequest, "commission_rate must be between 0 and 1")
		return
	}
	if req.CreditLimit != nil && *req.CreditLimit < 0 {
		fail(c, http.StatusBadRequest, "credit_limit must be >= 0")
		return
	}
	ctx := c.Request.Context()
	err = pgx.BeginTxFunc(ctx, s.systemPool(), pgx.TxOptions{}, func(tx pgx.Tx) error {
		tag, e := tx.Exec(ctx,
			`UPDATE console_users SET
			   commission_rate = COALESCE($1, commission_rate),
			   credit_limit    = COALESCE($2, credit_limit)
			 WHERE id=$3 AND role='sales'`,
			req.CommissionRate, req.CreditLimit, id)
		if e != nil {
			return e
		}
		if tag.RowsAffected() == 0 {
			return errAgentNotFound
		}
		return s.recordAuditTx(ctx, tx, auditEvent{TenantID: 0, ActorID: actorID(c),
			Action: "agent.set_terms", ResourceType: "agent", ResourceID: id,
			Details: map[string]any{"commission_rate": req.CommissionRate, "credit_limit": req.CreditLimit}})
	})
	switch {
	case errors.Is(err, errAgentNotFound):
		fail(c, http.StatusNotFound, "agent not found")
		return
	case err != nil:
		fail(c, http.StatusInternalServerError, "set terms failed")
		return
	}
	ok(c, gin.H{"id": id})
}
