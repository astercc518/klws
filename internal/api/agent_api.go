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
	"fmt"
	"log"
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

// isSalesAgent reports whether id refers to an existing console_users row with
// role='sales'. A non-existent id returns (false, nil), so callers can treat
// "missing" and "wrong role" identically as a 400.
func (s *Server) isSalesAgent(ctx context.Context, id int64) (bool, error) {
	var ok bool
	err := s.systemPool().QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM console_users WHERE id=$1 AND role='sales')`,
		id).Scan(&ok)
	return ok, err
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
	// The target must itself be an agent (role='sales'); setting a parent on
	// an admin/customer is meaningless and would corrupt the tree.
	if isSales, err := s.isSalesAgent(ctx, id); err != nil {
		fail(c, http.StatusInternalServerError, "target lookup failed")
		return
	} else if !isSales {
		fail(c, http.StatusBadRequest, "target is not a sales agent")
		return
	}
	if req.ParentID != nil {
		if *req.ParentID == id {
			fail(c, http.StatusBadRequest, "agent cannot be its own parent")
			return
		}
		// The new parent must exist AND be a sales agent — otherwise a bad id
		// hits the FK (500) or silently roots the subtree under a non-sales
		// node, breaking the subtree/rebate invariants.
		if isSales, err := s.isSalesAgent(ctx, *req.ParentID); err != nil {
			fail(c, http.StatusInternalServerError, "parent lookup failed")
			return
		} else if !isSales {
			fail(c, http.StatusBadRequest, "new parent is not an existing sales agent")
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
	// rebate/credit_limit are only meaningful for agents (role='sales').
	if isSales, err := s.isSalesAgent(ctx, id); err != nil {
		fail(c, http.StatusInternalServerError, "target lookup failed")
		return
	} else if !isSales {
		fail(c, http.StatusBadRequest, "target is not a sales agent")
		return
	}
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

// --- Credit allocation (T5) -------------------------------------------------
//
// An agent tops up a downline customer's tenant_wallet ON CREDIT, guarded by
// the agent's own credit_limit. THIS IS MONEY-CRITICAL.
//
// billing.Topup opens and COMMITS ITS OWN transaction internally and is
// idempotent on `ref` (a replayed ref credits nothing) — it is a red-line
// package and must not be modified, so it cannot be joined into the
// agent_allocations INSERT transaction. "One atomic tx across both" is not
// possible; instead every allocation follows this safe ORDERED-WRITES
// pattern (see allocateOne):
//
//  1. One tx (systemPool): SELECT ... FOR UPDATE the agent's console_users
//     row (serializes concurrent allocations by the SAME agent, closing the
//     TOCTOU window where two racing requests both read the same available
//     credit and both get approved), recompute available credit under that
//     lock, verify the tenant is in the agent's subtree, INSERT
//     agent_allocations RETURNING id, record the audit row, COMMIT.
//  2. THEN billing.Topup(ctx, tenant, amount, "alloc:<id>") — ref is the
//     allocation id, so a retry of just this step after a crash is safe
//     (Topup is idempotent on ref).
//  3. If Topup fails: best-effort compensating DELETE of the allocation row
//     (so outstanding isn't left permanently inflated by a topup that never
//     landed) and report a failure. If the DELETE itself also fails, this is
//     logged but still reported as the original Topup error — the alloc-id
//     ref keeps a later retry idempotent either way, so nothing is lost.
//
// Allocation-first (rather than Topup-first) is the safe failure direction:
// if step 2 fails, the agent's outstanding is briefly OVER-counted, which is
// platform-safe (the agent simply can't allocate further until it's
// resolved) — vs Topup-first, where a failure after crediting the customer
// would leave the platform with no record of what consumed the agent's
// credit limit.

// queryRower is satisfied by both *pgxpool.Pool and pgx.Tx. It lets the
// credit-availability and subtree-membership queries run either as a plain
// read (batch pre-check, post-success reporting) or inside an existing
// transaction (the per-item locked check in allocateOne), sharing one SQL
// text either way.
type queryRower interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// agentAvailableCreditQ computes agentID's available allocation credit:
//
//	credit_limit − Σ(agent_allocations.amount already given out)
//	             + Σ(settled retail consumption across the whole subtree)
//
// The settled-consumption term is a credit-back: as subtree customers
// actually spend allocated credit (billing_charges reaches 'settled'), that
// amount is no longer "outstanding" against the agent's limit and becomes
// available again. Runs against q, which may be the plain systemPool (a
// point-in-time read) or an open tx (a consistent read taken under the row
// lock held on console_users in that same tx — see allocateOne).
func (s *Server) agentAvailableCreditQ(ctx context.Context, q queryRower, agentID int64) (int64, error) {
	var available int64
	err := q.QueryRow(ctx,
		subtreeCTE("$1")+`
		SELECT cu.credit_limit
		     - COALESCE((SELECT SUM(a.amount) FROM agent_allocations a WHERE a.agent_id=$1),0)
		     + COALESCE((SELECT SUM(bc.amount) FROM billing_charges bc
		                  WHERE bc.state='settled'
		                    AND bc.tenant_id IN (SELECT t.id FROM tenants t
		                         WHERE t.sales_owner_id IN (SELECT id FROM agent_tree))),0)
		  FROM console_users cu WHERE cu.id=$1`,
		agentID).Scan(&available)
	return available, err
}

// agentAvailableCredit is agentAvailableCreditQ against the system pool (a
// non-locked, point-in-time read). Used for the batch pre-check and for
// reporting `available` back to the caller after a successful allocation.
// The money-critical per-item ENFORCEMENT instead runs agentAvailableCreditQ
// against a tx holding the agent's row lock — see allocateOne — so it can't
// race with a concurrent allocation by the same agent.
func (s *Server) agentAvailableCredit(ctx context.Context, agentID int64) (int64, error) {
	return s.agentAvailableCreditQ(ctx, s.systemPool(), agentID)
}

// tenantInSubtreeQ mirrors tenantInSubtree but runs against an arbitrary
// queryRower, so allocateOne can verify subtree membership INSIDE the same
// locked tx as its credit check, with no second round trip and no TOCTOU gap
// between the two checks.
func (s *Server) tenantInSubtreeQ(ctx context.Context, q queryRower, rootAgentID, tenantID int64) (bool, error) {
	var in bool
	err := q.QueryRow(ctx,
		subtreeCTE("$1")+`
		SELECT EXISTS (SELECT 1 FROM tenants t
		  WHERE t.id = $2 AND t.sales_owner_id IN (SELECT id FROM agent_tree))`,
		rootAgentID, tenantID).Scan(&in)
	return in, err
}

// allocOneResult is one item's outcome in handleAgentAllocate's batch report.
type allocOneResult struct {
	TenantID int64  `json:"tenant_id"`
	Status   string `json:"status"` // "ok" | "error"
	AllocID  int64  `json:"alloc_id,omitempty"`
	Error    string `json:"error,omitempty"`
}

// allocateOne executes ONE tenant's allocation using the safe ordered-writes
// pattern documented above handleAgentAllocate. Returns (allocID,
// httpStatus, errMsg); httpStatus==200 means both the agent_allocations row
// and the wallet topup succeeded.
func (s *Server) allocateOne(ctx context.Context, agentID, actor, tenantID, amount int64) (allocID int64, status int, errMsg string) {
	var overLimit, outOfSubtree bool
	txErr := pgx.BeginTxFunc(ctx, s.systemPool(), pgx.TxOptions{}, func(tx pgx.Tx) error {
		// Lock the agent row FIRST: serializes concurrent allocateOne calls
		// for the same agent so the available-credit read below can't race
		// with another in-flight allocation.
		var creditLimit int64
		if err := tx.QueryRow(ctx,
			`SELECT credit_limit FROM console_users WHERE id=$1 FOR UPDATE`, agentID).Scan(&creditLimit); err != nil {
			return fmt.Errorf("lock agent: %w", err)
		}
		// Subtree (authorization) check FIRST: an out-of-subtree tenant is
		// forbidden (403) regardless of amount, so it must win over the
		// credit-limit (402) check — otherwise an out-of-subtree + over-limit
		// request would misleadingly report 402.
		inSubtree, err := s.tenantInSubtreeQ(ctx, tx, agentID, tenantID)
		if err != nil {
			return fmt.Errorf("subtree check: %w", err)
		}
		if !inSubtree {
			outOfSubtree = true
			return nil // clean rollback, no writes yet
		}
		available, err := s.agentAvailableCreditQ(ctx, tx, agentID)
		if err != nil {
			return fmt.Errorf("available credit: %w", err)
		}
		if amount > available {
			overLimit = true
			return nil // clean rollback, no writes yet
		}
		if err := tx.QueryRow(ctx,
			`INSERT INTO agent_allocations (agent_id, tenant_id, amount, actor_id)
			 VALUES ($1,$2,$3,$4) RETURNING id`,
			agentID, tenantID, amount, actor).Scan(&allocID); err != nil {
			return fmt.Errorf("insert allocation: %w", err)
		}
		return s.recordAuditTx(ctx, tx, auditEvent{TenantID: tenantID, ActorID: actor,
			Action: "agent.allocate", ResourceType: "agent_allocation", ResourceID: allocID,
			Details: map[string]any{"agent_id": agentID, "amount": amount}})
	})
	switch {
	case txErr != nil:
		return 0, http.StatusInternalServerError, "allocation failed: " + txErr.Error()
	case overLimit:
		return 0, http.StatusPaymentRequired, "amount exceeds available credit"
	case outOfSubtree:
		return 0, http.StatusForbidden, "tenant is not in agent's subtree"
	}

	// The allocation row is committed; now perform the wallet topup OUTSIDE
	// that tx (billing.Topup self-commits — see the package doc comment
	// above). ref = the allocation id makes a retry of just this call safe.
	if err := s.deps.Billing.Topup(ctx, tenantID, amount, fmt.Sprintf("alloc:%d", allocID)); err != nil {
		if _, delErr := s.systemPool().Exec(ctx, `DELETE FROM agent_allocations WHERE id=$1`, allocID); delErr != nil {
			log.Printf("[agent.allocate] compensating delete of allocation %d failed after topup error: %v (topup error: %v)",
				allocID, delErr, err)
		}
		return 0, http.StatusInternalServerError, "topup failed: " + err.Error()
	}
	return allocID, http.StatusOK, ""
}

// handleAgentAllocate: POST /api/v1/sales/allocate {items:[{tenant_id,amount}]}.
//
// A single-item request returns the item's own outcome at the top level
// (200 with {alloc_id,tenant_id,amount,available}, 402 over-limit, or 403
// out-of-subtree) — this is what the credit-guard/subtree tests exercise.
//
// A multi-item request cannot be all-or-nothing: each item's billing.Topup
// self-commits its own transaction (see the package doc comment above), so
// there is no way to roll a later item's failure back over an earlier item's
// already-committed topup. Instead: (a) the SUM of all requested amounts is
// pre-checked against available credit once, as a coarse up-front guard; (b)
// items are then processed sequentially, each through allocateOne's own
// locked per-item check; (c) the response is always 200 with a per-item
// report {results:[{tenant_id,status,alloc_id?,error?}]} — partial failure
// is reported, not rolled back.
func (s *Server) handleAgentAllocate(c *gin.Context) {
	me := sessionFrom(c)
	if me == nil {
		fail(c, http.StatusUnauthorized, "no session")
		return
	}
	var req struct {
		Items []struct {
			TenantID int64 `json:"tenant_id" binding:"required"`
			Amount   int64 `json:"amount" binding:"required"`
		} `json:"items" binding:"required,min=1"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid request body (items:[{tenant_id,amount}] required)")
		return
	}
	ctx := c.Request.Context()
	var sum int64
	for _, it := range req.Items {
		if it.Amount <= 0 {
			fail(c, http.StatusBadRequest, "amount must be positive")
			return
		}
		sum += it.Amount
	}

	if len(req.Items) == 1 {
		item := req.Items[0]
		allocID, status, msg := s.allocateOne(ctx, me.UserID, actorID(c), item.TenantID, item.Amount)
		if status != http.StatusOK {
			fail(c, status, msg)
			return
		}
		available, err := s.agentAvailableCredit(ctx, me.UserID)
		if err != nil {
			// The allocation+topup already succeeded; a failure reporting the
			// new available balance is not itself a money error.
			log.Printf("[agent.allocate] post-success available-credit read failed: %v", err)
		}
		ok(c, gin.H{"alloc_id": allocID, "tenant_id": item.TenantID, "amount": item.Amount, "available": available})
		return
	}

	available, err := s.agentAvailableCredit(ctx, me.UserID)
	if err != nil {
		fail(c, http.StatusInternalServerError, "credit lookup failed")
		return
	}
	if sum > available {
		fail(c, http.StatusPaymentRequired, "batch total exceeds available credit")
		return
	}
	results := make([]allocOneResult, 0, len(req.Items))
	for _, it := range req.Items {
		allocID, status, msg := s.allocateOne(ctx, me.UserID, actorID(c), it.TenantID, it.Amount)
		r := allocOneResult{TenantID: it.TenantID}
		if status == http.StatusOK {
			r.Status = "ok"
			r.AllocID = allocID
		} else {
			r.Status = "error"
			r.Error = msg
		}
		results = append(results, r)
	}
	ok(c, gin.H{"results": results})
}
