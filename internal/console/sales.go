// internal/console/sales.go
package console

import (
	"context"
	"errors"
	"fmt"
	"net/http"
)

// SalesCustomerRow is a tenant row enriched with billing balances for the sales dashboard.
type SalesCustomerRow struct {
	TenantRow
	Balance int64
	Frozen  int64
}

// salesOwns reports whether the session's sales user owns the given tenant.
// Returns an error (not false) if the context does not hold a sales session.
func (s *Server) salesOwns(ctx context.Context, tenantID int64) (bool, error) {
	data, ok := sessionFrom(ctx)
	if !ok || data.Role != RoleSales {
		return false, errors.New("console: not a sales session")
	}
	var exists bool
	if err := s.mgr.SystemPool().QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM tenants WHERE id=$1 AND sales_owner_id=$2)`,
		tenantID, data.UserID).Scan(&exists); err != nil {
		return false, fmt.Errorf("sales ownership check: %w", err)
	}
	return exists, nil
}

// salesOwnedCustomers returns all tenants owned by the session's sales user,
// each enriched with its billing balance. Rows are drained fully before any
// billing call so the pool connection is not held open across Balance queries.
func (s *Server) salesOwnedCustomers(ctx context.Context) ([]SalesCustomerRow, error) {
	data, ok := sessionFrom(ctx)
	if !ok || data.Role != RoleSales {
		return nil, errors.New("console: not a sales session")
	}
	rows, err := s.mgr.SystemPool().Query(ctx,
		`SELECT id, name, status, sales_owner_id FROM tenants WHERE sales_owner_id=$1 ORDER BY id`,
		data.UserID)
	if err != nil {
		return nil, fmt.Errorf("sales owned tenants: %w", err)
	}
	defer rows.Close()
	// Drain rows into a slice FIRST, before calling billing.Balance,
	// so the pool connection is released before we make further queries.
	var tenants []TenantRow
	for rows.Next() {
		var t TenantRow
		if err := rows.Scan(&t.ID, &t.Name, &t.Status, &t.SalesOwnerID); err != nil {
			return nil, fmt.Errorf("scan tenant: %w", err)
		}
		tenants = append(tenants, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()

	out := make([]SalesCustomerRow, 0, len(tenants))
	for _, t := range tenants {
		bal, frozen, err := s.billing.Balance(ctx, t.ID)
		if err != nil {
			return nil, fmt.Errorf("balance for tenant %d: %w", t.ID, err)
		}
		out = append(out, SalesCustomerRow{TenantRow: t, Balance: bal, Frozen: frozen})
	}
	return out, nil
}

// handleSalesDashboard renders the sales dashboard listing owned customers.
func (s *Server) handleSalesDashboard(w http.ResponseWriter, r *http.Request) {
	rows, err := s.salesOwnedCustomers(r.Context())
	if err != nil {
		http.Error(w, "load customers", http.StatusInternalServerError)
		return
	}
	t, err := parsePage("sales_dashboard.html")
	if err != nil {
		http.Error(w, "template", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = render(w, t, map[string]any{"Customers": rows})
}
