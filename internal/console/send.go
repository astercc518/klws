// internal/console/send.go
package console

import (
	"context"
	"errors"
	"fmt"

	"github.com/acme/wadist/internal/crypto"
)

var ErrInsufficientBalance = errors.New("console: insufficient balance for campaign")

func (s *Server) estimateCost(ctx context.Context, country string, n int) int64 {
	if n <= 0 {
		return 0
	}
	data, ok := sessionFrom(ctx)
	if !ok || data.TenantID == nil {
		return 0
	}
	unit := s.pricing.PriceFor(ctx, *data.TenantID, country, 1)
	return int64(n) * unit
}

// createCampaign validates funds then atomically creates a running campaign with
// its template + recipients, all tenant-scoped by RLS WITH CHECK. Recipients carry
// phone_bidx for dedup + the dispatcher's suppression fence.
func (s *Server) createCampaign(ctx context.Context, country, body string, phones []string) (int64, error) {
	if len(s.blindKey) != 32 {
		return 0, ErrSendNotConfigured
	}
	if len(phones) == 0 {
		return 0, fmt.Errorf("console: no recipients")
	}
	data, ok := sessionFrom(ctx)
	if !ok || data.Role != RoleCustomer || data.TenantID == nil {
		return 0, errors.New("console: createCampaign requires a customer session")
	}
	tenantID := *data.TenantID

	estimate := s.estimateCost(ctx, country, len(phones))
	bal, _, err := s.customerBalance(ctx)
	if err != nil {
		return 0, err
	}
	if estimate > bal {
		return 0, ErrInsufficientBalance
	}

	tx, err := s.withTenantTx(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var templateID int64
	if err := tx.QueryRow(ctx,
		`INSERT INTO campaign_templates (tenant_id, kind, body) VALUES ($1,'text',$2) RETURNING id`,
		tenantID, body).Scan(&templateID); err != nil {
		return 0, fmt.Errorf("insert template: %w", err)
	}
	var campaignID int64
	if err := tx.QueryRow(ctx,
		`INSERT INTO campaigns (tenant_id, template_id, state, total) VALUES ($1,$2,'running',$3) RETURNING id`,
		tenantID, templateID, len(phones)).Scan(&campaignID); err != nil {
		return 0, fmt.Errorf("insert campaign: %w", err)
	}
	for _, p := range phones {
		bidx := crypto.BlindIndex(s.blindKey, p)
		if _, err := tx.Exec(ctx,
			`INSERT INTO campaign_recipients (tenant_id, campaign_id, phone, country_code, phone_bidx)
			 VALUES ($1,$2,$3,$4,$5)
			 ON CONFLICT (campaign_id, phone_bidx) WHERE phone_bidx IS NOT NULL DO NOTHING`,
			tenantID, campaignID, p, country, bidx); err != nil {
			return 0, fmt.Errorf("insert recipient: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit campaign: %w", err)
	}
	return campaignID, nil
}
