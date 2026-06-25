// internal/store/campaign_send_test.go
package store

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// newManagerForCampaignSend creates a Manager with all migrations applied,
// matching the pattern used in suppression_test.go.
func newManagerForCampaignSend(t *testing.T) (*Manager, *pgxpool.Pool, context.Context) {
	t.Helper()
	ctx := context.Background()
	superDSN := testDSN(t)
	pool, err := pgxpool.New(ctx, superDSN)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	applyAllMigrationsRLS(t, ctx, pool)
	m := &Manager{bizPool: pool}
	return m, pool, ctx
}

func TestCampaignSendable(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	ctx := context.Background()
	m, pool, _ := newManagerForCampaignSend(t)

	// seed a template + campaign
	var tplID, campID int64
	if err := pool.QueryRow(ctx,
		`INSERT INTO campaign_templates (tenant_id, kind, body) VALUES (1, 'text', '{Hi|Hello} {{.name}}') RETURNING id`).
		Scan(&tplID); err != nil {
		t.Fatalf("seed template: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO campaigns (tenant_id, template_id, state) VALUES (1, $1, 'running') RETURNING id`, tplID).
		Scan(&campID); err != nil {
		t.Fatalf("seed campaign: %v", err)
	}

	body, mediaSha, mime, err := m.CampaignSendable(ctx, campID)
	if err != nil {
		t.Fatalf("CampaignSendable: %v", err)
	}
	if body != "{Hi|Hello} {{.name}}" || mediaSha != "" || mime != "" {
		t.Fatalf("unexpected: body=%q mediaSha=%q mime=%q", body, mediaSha, mime)
	}

	if _, _, _, err := m.CampaignSendable(ctx, 999999); !errors.Is(err, ErrCampaignNotFound) {
		t.Fatalf("unknown campaign: want ErrCampaignNotFound, got %v", err)
	}
}
