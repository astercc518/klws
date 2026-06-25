// internal/console/admin.go
package console

import (
	"errors"
	"net/http"
	"strconv"
)

type tenantBalanceRow struct {
	TenantRow
	Balance int64
	Frozen  int64
}

func (s *Server) handleAdminTenants(w http.ResponseWriter, r *http.Request) {
	list, err := s.tenants.List(r.Context())
	if err != nil {
		http.Error(w, "load tenants", http.StatusInternalServerError)
		return
	}
	rows := make([]tenantBalanceRow, 0, len(list))
	for _, t := range list {
		bal, frozen, err := s.billing.Balance(r.Context(), t.ID)
		if err != nil {
			http.Error(w, "load balance", http.StatusInternalServerError)
			return
		}
		rows = append(rows, tenantBalanceRow{TenantRow: t, Balance: bal, Frozen: frozen})
	}
	tpl, err := parsePage("admin_tenants.html")
	if err != nil {
		http.Error(w, "template", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = render(w, tpl, map[string]any{"Tenants": rows})
}

func (s *Server) handleAdminTenant(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad tenant id", http.StatusBadRequest)
		return
	}
	tenant, err := s.tenants.Get(r.Context(), id)
	if errors.Is(err, ErrTenantNotFound) {
		http.Error(w, "tenant not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	bal, frozen, err := s.billing.Balance(r.Context(), id)
	if err != nil {
		http.Error(w, "load balance", http.StatusInternalServerError)
		return
	}
	ledger, err := s.billing.Ledger(r.Context(), id, 50)
	if err != nil {
		http.Error(w, "load ledger", http.StatusInternalServerError)
		return
	}
	tpl, err := parsePage("admin_tenant.html")
	if err != nil {
		http.Error(w, "template", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = render(w, tpl, map[string]any{"Tenant": tenant, "Balance": bal, "Frozen": frozen, "Ledger": ledger})
}
