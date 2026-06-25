// internal/console/admin.go
package console

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/acme/wadist/internal/pricing"
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
	var prices []pricing.Price
	if s.pricing != nil {
		prices, _ = s.pricing.ListForTenant(r.Context(), id)
	}
	tpl, err := parsePage("admin_tenant.html")
	if err != nil {
		http.Error(w, "template", http.StatusInternalServerError)
		return
	}
	var salesUsers []SalesUser
	if s.tenants != nil {
		salesUsers, _ = s.tenants.ListSalesUsers(r.Context())
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = render(w, tpl, map[string]any{"Tenant": tenant, "Balance": bal, "Frozen": frozen, "Ledger": ledger, "Prices": prices, "SalesUsers": salesUsers, "CSRF": s.issueCSRFToken(w, r)})
}

func (s *Server) handleAdminRecharge(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad tenant id", http.StatusBadRequest)
		return
	}
	amount, err := strconv.ParseInt(r.FormValue("amount"), 10, 64)
	if err != nil || amount <= 0 {
		http.Error(w, "amount must be a positive integer", http.StatusBadRequest)
		return
	}
	ref := r.FormValue("ref")
	if ref == "" {
		http.Error(w, "ref (payment reference) is required", http.StatusBadRequest)
		return
	}
	if s.billing == nil {
		http.Error(w, "billing not configured", http.StatusInternalServerError)
		return
	}
	if err := s.billing.Topup(r.Context(), id, amount, ref); err != nil {
		http.Error(w, "recharge failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/admin/tenant/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}

func (s *Server) handleAdminSetPrice(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad tenant id", http.StatusBadRequest)
		return
	}
	country := r.FormValue("country")
	if len(country) != 2 {
		http.Error(w, "country must be a 2-letter code", http.StatusBadRequest)
		return
	}
	unit, err := strconv.ParseInt(r.FormValue("unit_price"), 10, 64)
	if err != nil || unit <= 0 {
		http.Error(w, "unit_price must be a positive integer", http.StatusBadRequest)
		return
	}
	if s.pricing == nil {
		http.Error(w, "pricing not configured", http.StatusInternalServerError)
		return
	}
	if err := s.pricing.SetPrice(r.Context(), id, country, unit); err != nil {
		http.Error(w, "set price failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/admin/tenant/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}

func (s *Server) handleAdminSetSalesOwner(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad tenant id", http.StatusBadRequest)
		return
	}
	salesUserID, err := strconv.ParseInt(r.FormValue("sales_user_id"), 10, 64)
	if err != nil {
		http.Error(w, "bad sales_user_id", http.StatusBadRequest)
		return
	}
	if s.tenants == nil {
		http.Error(w, "tenants not configured", http.StatusInternalServerError)
		return
	}
	if err := s.tenants.SetSalesOwner(r.Context(), id, salesUserID); err != nil {
		http.Error(w, "set sales owner failed: "+err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/admin/tenant/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}
