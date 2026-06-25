// internal/console/sales_assign_test.go
package console

import (
	"context"
	"testing"
)

func TestSetSalesOwner(t *testing.T) {
	ctx := context.Background()
	mgr := newTestManager(t)
	repo := NewTenantRepo(mgr.SystemPool())
	users := NewUserRepo(mgr.SystemPool())
	hash, _ := HashPassword("pw")

	var tid int64
	mustScan(t, mgr, ctx, `INSERT INTO tenants (name) VALUES ('Acme') RETURNING id`, &tid)
	salesID, err := users.Create(ctx, "sales@x.test", hash, RoleSales, nil)
	if err != nil {
		t.Fatalf("create sales: %v", err)
	}
	adminID, err := users.Create(ctx, "admin@x.test", hash, RoleAdmin, nil)
	if err != nil {
		t.Fatalf("create admin: %v", err)
	}

	// assign a real sales user → ok
	if err := repo.SetSalesOwner(ctx, tid, salesID); err != nil {
		t.Fatalf("assign sales: %v", err)
	}
	got, err := repo.Get(ctx, tid)
	if err != nil || got.SalesOwnerID == nil || *got.SalesOwnerID != salesID {
		t.Fatalf("owner not set: %+v err=%v", got, err)
	}
	// assigning a non-sales user (admin) → error
	if err := repo.SetSalesOwner(ctx, tid, adminID); err == nil {
		t.Fatalf("expected error assigning a non-sales user as owner")
	}
	// list sales users includes our sales, not the admin
	list, err := repo.ListSalesUsers(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var sawSales, sawAdmin bool
	for _, u := range list {
		if u.ID == salesID {
			sawSales = true
		}
		if u.ID == adminID {
			sawAdmin = true
		}
	}
	if !sawSales || sawAdmin {
		t.Fatalf("ListSalesUsers wrong: sawSales=%v sawAdmin=%v", sawSales, sawAdmin)
	}
}
