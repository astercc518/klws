// internal/console/users_test.go
package console

import (
	"context"
	"errors"
	"testing"
)

func TestUserRepoAuthenticate(t *testing.T) {
	ctx := context.Background()
	mgr := newTestManager(t)
	repo := NewUserRepo(mgr.SystemPool())

	hash, err := HashPassword("s3cret-pw")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	// admin staff user (no tenant).
	if _, err := repo.Create(ctx, "admin@acme.test", hash, RoleAdmin, nil); err != nil {
		t.Fatalf("create admin: %v", err)
	}

	u, err := repo.Authenticate(ctx, "admin@acme.test", "s3cret-pw")
	if err != nil {
		t.Fatalf("authenticate ok: %v", err)
	}
	if u.Role != RoleAdmin || u.TenantID != nil {
		t.Fatalf("unexpected user: %+v", u)
	}

	if _, err := repo.Authenticate(ctx, "admin@acme.test", "wrong"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("wrong password: want ErrInvalidCredentials, got %v", err)
	}
	if _, err := repo.Authenticate(ctx, "nobody@acme.test", "x"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("unknown user: want ErrInvalidCredentials, got %v", err)
	}
}

func TestUserRepoCustomerRequiresTenant(t *testing.T) {
	ctx := context.Background()
	mgr := newTestManager(t)
	repo := NewUserRepo(mgr.SystemPool())
	hash, _ := HashPassword("pw")

	// customer WITHOUT tenant violates the CHECK constraint.
	if _, err := repo.Create(ctx, "c1@acme.test", hash, RoleCustomer, nil); err == nil {
		t.Fatalf("expected constraint error for customer without tenant")
	}

	var tenantID int64
	if err := mgr.SystemPool().QueryRow(ctx,
		`INSERT INTO tenants (name) VALUES ('Acme') RETURNING id`).Scan(&tenantID); err != nil {
		t.Fatalf("insert tenant: %v", err)
	}
	id, err := repo.Create(ctx, "c1@acme.test", hash, RoleCustomer, ptrInt64(tenantID))
	if err != nil {
		t.Fatalf("create customer: %v", err)
	}
	u, err := repo.Authenticate(ctx, "c1@acme.test", "pw")
	if err != nil {
		t.Fatalf("auth customer: %v", err)
	}
	if u.ID != id || u.TenantID == nil || *u.TenantID != tenantID {
		t.Fatalf("unexpected customer: %+v", u)
	}
}
