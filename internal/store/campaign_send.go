// internal/store/campaign_send.go
package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// ErrCampaignNotFound is returned when a campaign id has no row.
var ErrCampaignNotFound = errors.New("store: campaign not found")

// CampaignSendable returns the template body and (nullable) media metadata for
// a campaign, joining campaigns → campaign_templates. mediaSha/mime are "" when
// the template is text-only.
func (m *Manager) CampaignSendable(ctx context.Context, campaignID int64) (body, mediaSha, mime string, err error) {
	var sha, mm *string
	err = m.bizPool.QueryRow(ctx, `
SELECT t.body, t.media_sha, t.media_mime
  FROM campaigns c JOIN campaign_templates t ON t.id = c.template_id
 WHERE c.id = $1`, campaignID).Scan(&body, &sha, &mm)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", "", ErrCampaignNotFound
	}
	if err != nil {
		return "", "", "", fmt.Errorf("load campaign sendable: %w", err)
	}
	if sha != nil {
		mediaSha = *sha
	}
	if mm != nil {
		mime = *mm
	}
	return body, mediaSha, mime, nil
}
