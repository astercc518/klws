// internal/api/campaign.go — Campaign controller (customer bulk send + history).
//
// Create mirrors console's createCampaign exactly: price lookup -> balance guard
// -> single RLS transaction that inserts template + campaign(state='running') +
// recipients (each carrying phone_bidx for dedup and the dispatcher's
// suppression fence). The background dispatcher then takes over untouched.
package api

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/acme/wadist/internal/crypto"
	"github.com/acme/wadist/internal/pricing"
)

// createCampaignRequest is the POST /api/v1/campaigns body.
type createCampaignRequest struct {
	Country string   `json:"country" binding:"required,len=2"` // ISO-3166 alpha-2, for pricing
	Body    string   `json:"body" binding:"required"`          // Spintax template
	Phones  []string `json:"phones" binding:"required"`        // already-normalized recipients
}

// createCampaignResponse is returned on successful submission.
type createCampaignResponse struct {
	CampaignID int64 `json:"campaign_id"`
	Total      int   `json:"total"`    // recipients submitted
	Estimate   int64 `json:"estimate"` // pre-flight cost estimate (smallest currency unit)
}

// campaignSummary is one row in GET /api/v1/campaigns.
type campaignSummary struct {
	ID     int64  `json:"id"`
	State  string `json:"state"`
	Total  int    `json:"total"`
	Sent   int    `json:"sent"`
	Failed int    `json:"failed"`
}

// handleCreateCampaign: POST /api/v1/campaigns (customer only).
func (s *Server) handleCreateCampaign(c *gin.Context) {
	tid, ok2 := tenantID(c)
	if !ok2 {
		fail(c, http.StatusForbidden, "no tenant in session")
		return
	}
	var req createCampaignRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(s.deps.BlindKey) != 32 {
		// Matches console's fail-closed when the blind-index key is unset.
		fail(c, http.StatusServiceUnavailable, "send not configured")
		return
	}
	if len(req.Phones) == 0 {
		fail(c, http.StatusBadRequest, "no recipients")
		return
	}
	ctx := c.Request.Context()

	unit, err := s.deps.Pricing.GetPrice(ctx, tid, req.Country)
	if errors.Is(err, pricing.ErrNoPrice) {
		fail(c, http.StatusBadRequest, "no unit price configured for this country")
		return
	}
	if err != nil {
		fail(c, http.StatusInternalServerError, "price lookup failed")
		return
	}
	estimate := int64(len(req.Phones)) * unit

	tx, err := s.deps.Mgr.WithTenant(ctx, tid)
	if err != nil {
		fail(c, http.StatusInternalServerError, "could not open transaction")
		return
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Pre-flight balance guard (the real per-message freeze happens downstream
	// in the dispatcher via billing.Hold — not here).
	bal, _, err := scanWallet(ctx, tx)
	if err != nil {
		fail(c, http.StatusInternalServerError, "balance read failed")
		return
	}
	if estimate > bal {
		fail(c, http.StatusPaymentRequired, "余额不足,无法提交本次群发")
		return
	}

	var templateID int64
	if err := tx.QueryRow(ctx,
		`INSERT INTO campaign_templates (tenant_id, kind, body) VALUES ($1,'text',$2) RETURNING id`,
		tid, req.Body).Scan(&templateID); err != nil {
		fail(c, http.StatusInternalServerError, fmt.Sprintf("insert template: %v", err))
		return
	}
	var campaignID int64
	if err := tx.QueryRow(ctx,
		`INSERT INTO campaigns (tenant_id, template_id, state, total) VALUES ($1,$2,'running',$3) RETURNING id`,
		tid, templateID, len(req.Phones)).Scan(&campaignID); err != nil {
		fail(c, http.StatusInternalServerError, fmt.Sprintf("insert campaign: %v", err))
		return
	}
	for _, p := range req.Phones {
		bidx := crypto.BlindIndex(s.deps.BlindKey, p)
		if _, err := tx.Exec(ctx,
			`INSERT INTO campaign_recipients (tenant_id, campaign_id, phone, country_code, phone_bidx)
			 VALUES ($1,$2,$3,$4,$5)
			 ON CONFLICT (campaign_id, phone_bidx) WHERE phone_bidx IS NOT NULL DO NOTHING`,
			tid, campaignID, p, req.Country, bidx); err != nil {
			fail(c, http.StatusInternalServerError, fmt.Sprintf("insert recipient: %v", err))
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		fail(c, http.StatusInternalServerError, "commit failed")
		return
	}

	ok(c, createCampaignResponse{CampaignID: campaignID, Total: len(req.Phones), Estimate: estimate})
}

// handleListCampaigns: GET /api/v1/campaigns (customer only). RLS auto-scopes
// results to the session tenant.
func (s *Server) handleListCampaigns(c *gin.Context) {
	tid, ok2 := tenantID(c)
	if !ok2 {
		fail(c, http.StatusForbidden, "no tenant in session")
		return
	}
	ctx := c.Request.Context()
	tx, err := s.deps.Mgr.WithTenant(ctx, tid)
	if err != nil {
		fail(c, http.StatusInternalServerError, "could not open transaction")
		return
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	rows, err := tx.Query(ctx,
		`SELECT id, state::text, total, sent, failed FROM campaigns ORDER BY id DESC LIMIT 50`)
	if err != nil {
		fail(c, http.StatusInternalServerError, "list campaigns failed")
		return
	}
	defer rows.Close()

	out := make([]campaignSummary, 0)
	for rows.Next() {
		var cs campaignSummary
		if err := rows.Scan(&cs.ID, &cs.State, &cs.Total, &cs.Sent, &cs.Failed); err != nil {
			fail(c, http.StatusInternalServerError, "scan campaign failed")
			return
		}
		out = append(out, cs)
	}
	if err := rows.Err(); err != nil {
		fail(c, http.StatusInternalServerError, "iterate campaigns failed")
		return
	}
	ok(c, out)
}
