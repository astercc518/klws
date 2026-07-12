// internal/api/campaign_from_segment.go — resolve a saved segment into
// sendable recipients for the POST /api/v1/campaigns segment_id branch.
//
// This file only produces a []segmentPhone; it never touches campaign
// creation, pricing, the balance guard, or billing.Hold — those stay exactly
// as handleCreateCampaign already implements them in campaign.go.
package api

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
)

// segmentPhone is one recipient resolved from a saved segment.
type segmentPhone struct {
	Phone   string
	Country string
}

// resolveSegmentPhones expands a saved segment into sendable recipients
// within the caller's RLS tx (same tx handleCreateCampaign uses for the
// campaign insert, so tenant scoping matches). Only status='active' contacts
// are eligible, and anyone on the suppression list (matched by phone_bidx) is
// excluded. suppression_list is NOT RLS-protected (see
// TestListSuppression_TenantIsolation), so the exclusion subquery filters
// tenant_id explicitly off the joined contact row rather than relying on RLS.
// Returns a nil slice (not an error) when the segment yields nobody.
func (s *Server) resolveSegmentPhones(ctx context.Context, tx pgx.Tx, segID int64) ([]segmentPhone, error) {
	var raw []byte
	if err := tx.QueryRow(ctx, `SELECT filter FROM contact_segments WHERE id=$1`, segID).Scan(&raw); err != nil {
		return nil, err
	}
	var f segmentFilter
	_ = json.Unmarshal(raw, &f) // mirrors handleSegmentPreview's tolerant parse
	if f.Status == "" {
		f.Status = "active" // a campaign only ever sends to active contacts
	}
	where, args := segmentToWhere(f)
	const suppressionGuard = `NOT EXISTS (
		SELECT 1 FROM suppression_list sl
		 WHERE sl.tenant_id = c.tenant_id AND sl.phone_bidx = c.phone_bidx
	)`
	if where == "" {
		where = " WHERE " + suppressionGuard
	} else {
		where += " AND " + suppressionGuard
	}
	q := `SELECT DISTINCT c.phone, COALESCE(c.country_code, '')
	        FROM contacts c LEFT JOIN contact_tag_map m ON m.contact_id = c.id` + where

	rows, err := tx.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []segmentPhone
	for rows.Next() {
		var p segmentPhone
		if err := rows.Scan(&p.Phone, &p.Country); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
