// internal/api/campaign.go — Campaign controller (customer bulk send + history).
//
// Create mirrors console's createCampaign exactly: price lookup -> balance guard
// -> single RLS transaction that inserts template + campaign(state='running') +
// recipients (each carrying phone_bidx for dedup and the dispatcher's
// suppression fence). The background dispatcher then takes over untouched.
package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"

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
	ID        int64  `json:"id"`
	State     string `json:"state"`
	Total     int    `json:"total"`
	Sent      int    `json:"sent"`
	Failed    int    `json:"failed"`
	CreatedAt string `json:"created_at"`
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
		`SELECT id, state::text, total, sent, failed, created_at::text FROM campaigns ORDER BY id DESC LIMIT 50`)
	if err != nil {
		fail(c, http.StatusInternalServerError, "list campaigns failed")
		return
	}
	defer rows.Close()

	out := make([]campaignSummary, 0)
	for rows.Next() {
		var cs campaignSummary
		if err := rows.Scan(&cs.ID, &cs.State, &cs.Total, &cs.Sent, &cs.Failed, &cs.CreatedAt); err != nil {
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

// ---------------------------------------------------------------------------
// Recipient drill-through — per-number delivery detail with a status funnel.
//
// Honest mapping to what the engine actually records (no fabricated receipts):
//   recipient_state_t pending -> "queued", and sent/failed/skipped pass through.
//   There is NO delivered/read tracking (the node does not ingest WhatsApp
//   receipts yet), so the funnel's success node is "sent" (left our system),
//   not "delivered". error_reason is a best-effort classification of the free
//   text last_error; the raw text is also returned as error_detail.
// Shared by the customer (RLS tx) and admin (SystemPool) handlers — both pass a
// rowQuerier, so ownership is enforced by RLS for customers and by the explicit
// admin role for super-admins.
// ---------------------------------------------------------------------------

// rowQuerier is satisfied by both pgx.Tx (customer RLS) and *pgxpool.Pool (admin).
type rowQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type recipientDetail struct {
	ID          int64   `json:"id"`
	Phone       string  `json:"phone"`
	Status      string  `json:"status"` // queued | sent | delivered | read | failed | skipped
	ErrorReason *string `json:"error_reason"`
	ErrorDetail *string `json:"error_detail"`
	SentAt      *string `json:"sent_at"`      // set for sent rows (state-change time)
	DeliveredAt *string `json:"delivered_at"` // WA delivery receipt time (null until ingested)
	ReadAt      *string `json:"read_at"`      // WA read receipt time (null until ingested)
	UpdatedAt   string  `json:"updated_at"`   // last state-change time
}

type recipientsResponse struct {
	CampaignID int64             `json:"campaign_id"`
	Total      int               `json:"total"`
	Counts     map[string]int    `json:"counts"` // keyed by mapped status
	Page       int               `json:"page"`
	PageSize   int               `json:"page_size"`
	Recipients []recipientDetail `json:"recipients"`
}

// mapStatus maps the DB enum to the API status vocabulary.
func mapStatus(state string) string {
	if state == "pending" {
		return "queued"
	}
	return state // sent | failed | skipped
}

// classifyError turns a free-text last_error into a structured reason code while
// preserving the raw text. Patterns for device_banned mirror dispatch's
// isBanSignal (keep in sync). Returns (reason, detail).
func classifyError(lastError string) (*string, *string) {
	if lastError == "" {
		return nil, nil
	}
	detail := lastError
	s := strings.ToLower(lastError)
	var code string
	switch {
	case strings.Contains(s, "wa_warning"), strings.Contains(s, "banned"), strings.Contains(s, "403"):
		code = "device_banned"
	case strings.Contains(s, "invalid"), strings.Contains(s, "not on whatsapp"),
		strings.Contains(s, "no such"), strings.Contains(s, "unregistered"):
		code = "number_invalid"
	case strings.Contains(s, "billing"), strings.Contains(s, "insufficient"),
		strings.Contains(s, "balance"), strings.Contains(s, "wallet"):
		code = "billing_error"
	case strings.Contains(s, "admit"), strings.Contains(s, "rate"),
		strings.Contains(s, "pacing"), strings.Contains(s, "quota"):
		code = "rate_limited"
	case strings.Contains(s, "media"):
		code = "media_error"
	default:
		code = "unknown"
	}
	return &code, &detail
}

// parsePageParams reads ?page=&page_size= (1-based; page_size capped at 200).
func parsePageParams(c *gin.Context) (page, pageSize int) {
	page = 1
	if v, err := strconv.Atoi(c.Query("page")); err == nil && v > 0 {
		page = v
	}
	pageSize = 50
	if v, err := strconv.Atoi(c.Query("page_size")); err == nil && v > 0 {
		pageSize = v
	}
	if pageSize > 200 {
		pageSize = 200
	}
	return page, pageSize
}

// loadRecipients returns the funnel counts + one page of recipient details for a
// campaign. The second return is false when the campaign does not exist (or, for
// an RLS tx, is not visible to this tenant) so the caller can 404.
func loadRecipients(ctx context.Context, q rowQuerier, campaignID int64, page, pageSize int) (recipientsResponse, bool, error) {
	var exists bool
	err := q.QueryRow(ctx, `SELECT true FROM campaigns WHERE id=$1`, campaignID).Scan(&exists)
	if errors.Is(err, pgx.ErrNoRows) {
		return recipientsResponse{}, false, nil
	}
	if err != nil {
		return recipientsResponse{}, false, fmt.Errorf("check campaign: %w", err)
	}

	resp := recipientsResponse{
		CampaignID: campaignID,
		Page:       page,
		PageSize:   pageSize,
		Recipients: make([]recipientDetail, 0, pageSize),
	}

	// Cumulative funnel counts in one aggregate row. sent/delivered/read are
	// nested (read ⊆ delivered ⊆ sent): delivered/read are derived from the
	// receipt timestamps, which stay 0 until receipt ingestion is wired.
	var queued, sent, delivered, read, failed, skipped int
	err = q.QueryRow(ctx, `
SELECT count(*),
       count(*) FILTER (WHERE state='pending'),
       count(*) FILTER (WHERE state='sent'),
       count(*) FILTER (WHERE delivered_at IS NOT NULL),
       count(*) FILTER (WHERE read_at IS NOT NULL),
       count(*) FILTER (WHERE state='failed'),
       count(*) FILTER (WHERE state='skipped')
  FROM campaign_recipients WHERE campaign_id=$1`, campaignID).
		Scan(&resp.Total, &queued, &sent, &delivered, &read, &failed, &skipped)
	if err != nil {
		return resp, true, fmt.Errorf("count recipients: %w", err)
	}
	resp.Counts = map[string]int{
		"queued": queued, "sent": sent, "delivered": delivered,
		"read": read, "failed": failed, "skipped": skipped,
	}

	offset := (page - 1) * pageSize
	rows, err := q.Query(ctx, `
SELECT id, phone, state::text, last_error, updated_at::text,
       delivered_at::text, read_at::text
  FROM campaign_recipients
 WHERE campaign_id=$1
 ORDER BY id
 LIMIT $2 OFFSET $3`, campaignID, pageSize, offset)
	if err != nil {
		return resp, true, fmt.Errorf("list recipients: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var d recipientDetail
		var rawState string
		var lastError *string
		if err := rows.Scan(&d.ID, &d.Phone, &rawState, &lastError, &d.UpdatedAt,
			&d.DeliveredAt, &d.ReadAt); err != nil {
			return resp, true, fmt.Errorf("scan recipient: %w", err)
		}
		// Milestone status: read > delivered > sent > (queued/failed/skipped).
		switch {
		case d.ReadAt != nil:
			d.Status = "read"
		case d.DeliveredAt != nil:
			d.Status = "delivered"
		default:
			d.Status = mapStatus(rawState)
		}
		if lastError != nil {
			d.ErrorReason, d.ErrorDetail = classifyError(*lastError)
		}
		if rawState == "sent" {
			ts := d.UpdatedAt
			d.SentAt = &ts
		}
		resp.Recipients = append(resp.Recipients, d)
	}
	return resp, true, rows.Err()
}

// handleListCampaignRecipients: GET /api/v1/campaigns/:id/recipients (customer).
// RLS scopes both the existence check and the rows to the session tenant.
func (s *Server) handleListCampaignRecipients(c *gin.Context) {
	tid, ok2 := tenantID(c)
	if !ok2 {
		fail(c, http.StatusForbidden, "no tenant in session")
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		fail(c, http.StatusBadRequest, "bad campaign id")
		return
	}
	page, pageSize := parsePageParams(c)
	ctx := c.Request.Context()
	tx, err := s.deps.Mgr.WithTenant(ctx, tid)
	if err != nil {
		fail(c, http.StatusInternalServerError, "could not open transaction")
		return
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	resp, found, err := loadRecipients(ctx, tx, id, page, pageSize)
	if err != nil {
		fail(c, http.StatusInternalServerError, "list recipients failed")
		return
	}
	if !found {
		fail(c, http.StatusNotFound, "campaign not found")
		return
	}
	ok(c, resp)
}
