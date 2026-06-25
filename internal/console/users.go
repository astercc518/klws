// internal/console/users.go
package console

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Role is a console access role.
type Role string

const (
	RoleAdmin    Role = "admin"
	RoleSales    Role = "sales"
	RoleCustomer Role = "customer"
)

// User is a console login account (password hash deliberately excluded).
type User struct {
	ID       int64
	Email    string
	Role     Role
	TenantID *int64 // non-nil iff Role == RoleCustomer
	Disabled bool
}

var (
	ErrUserNotFound       = errors.New("console: user not found")
	ErrInvalidCredentials = errors.New("console: invalid credentials")
)

// UserRepo reads/writes console_users. It MUST be given the BYPASSRLS system
// pool: login/registry tables are platform metadata, not tenant-scoped.
type UserRepo struct {
	pool *pgxpool.Pool
}

func NewUserRepo(pool *pgxpool.Pool) *UserRepo { return &UserRepo{pool: pool} }

// Create inserts a console_users row and returns its id.
func (r *UserRepo) Create(ctx context.Context, email, passwordHash string, role Role, tenantID *int64) (int64, error) {
	var id int64
	err := r.pool.QueryRow(ctx,
		`INSERT INTO console_users (email, password_hash, role, tenant_id)
		 VALUES ($1, $2, $3, $4) RETURNING id`,
		email, passwordHash, string(role), tenantID,
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("create console user: %w", err)
	}
	return id, nil
}

// Authenticate verifies credentials and returns the user. A missing user,
// wrong password, or disabled account all return ErrInvalidCredentials (no
// account enumeration via differing errors).
func (r *UserRepo) Authenticate(ctx context.Context, email, plain string) (*User, error) {
	var (
		u    User
		hash string
		role string
	)
	err := r.pool.QueryRow(ctx,
		`SELECT id, email, password_hash, role, tenant_id, disabled
		   FROM console_users WHERE email = $1`, email,
	).Scan(&u.ID, &u.Email, &hash, &role, &u.TenantID, &u.Disabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrInvalidCredentials
	}
	if err != nil {
		return nil, fmt.Errorf("query console user: %w", err)
	}
	if u.Disabled {
		return nil, ErrInvalidCredentials
	}
	ok, err := VerifyPassword(hash, plain)
	if err != nil {
		return nil, fmt.Errorf("verify password: %w", err)
	}
	if !ok {
		return nil, ErrInvalidCredentials
	}
	u.Role = Role(role)
	return &u, nil
}
