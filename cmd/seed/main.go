// Command seed injects a self-contained demo dataset so the login -> dashboard
// -> campaign flow can be exercised end to end without hand-writing SQL.
//
// It is idempotent: re-running reuses the existing tenant/user, the top-up is
// deduplicated by reference, and pricing is upserted. It only writes platform
// metadata + a wallet top-up via the same repos the app uses — it never
// touches the dispatch/cluster send path.
//
// Run (after `make up`):
//
//	go run ./cmd/seed
//
// Override the database with WADIST_POSTGRES_DSN; it defaults to the
// docker-compose Postgres (postgres://app:app@localhost:5432/wadist).
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/acme/wadist/internal/billing"
	"github.com/acme/wadist/internal/console"
	"github.com/acme/wadist/internal/pricing"
)

const (
	defaultDSN = "postgres://app:app@localhost:5432/wadist?sslmode=disable"

	tenantName = "Demo Tenant"
	email      = "demo@wadist.local"
	password   = "demo12345"

	// Super-admin login for the Admin Console (tenant-less).
	adminEmail    = "admin@wadist.local"
	adminPassword = "admin12345"

	// Sales login for the Sales Console (tenant-less; owns the demo tenant).
	salesEmail    = "sales@wadist.local"
	salesPassword = "sales12345"

	// Amounts are in the smallest currency unit (cents). $10,000.00 = 1,000,000.
	topupCents = 10_000 * 100
	// $0.05 per message for the US.
	country   = "US"
	unitCents = 5
)

func main() {
	if err := run(); err != nil {
		log.Fatalf("seed: %v", err)
	}
}

func run() error {
	ctx := context.Background()

	dsn := os.Getenv("WADIST_POSTGRES_DSN")
	if dsn == "" {
		dsn = defaultDSN
		log.Printf("WADIST_POSTGRES_DSN not set, using default %s", dsn)
	}

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		return fmt.Errorf("ping (is Postgres up? try `make up`): %w", err)
	}

	// 1. Tenant (find-or-create by name).
	tenantID, err := findOrCreateTenant(ctx, pool, tenantName)
	if err != nil {
		return err
	}
	log.Printf("✓ tenant #%d %q", tenantID, tenantName)

	// 2. Customer login account (find-or-create by email; argon2id hash).
	users := console.NewUserRepo(pool)
	if existing, err := userIDByEmail(ctx, pool, email); err != nil {
		return err
	} else if existing != 0 {
		log.Printf("✓ user #%d %s (already existed)", existing, email)
	} else {
		hash, err := console.HashPassword(password)
		if err != nil {
			return fmt.Errorf("hash password: %w", err)
		}
		tid := tenantID
		uid, err := users.Create(ctx, email, hash, console.RoleCustomer, &tid)
		if err != nil {
			return fmt.Errorf("create user: %w", err)
		}
		log.Printf("✓ user #%d %s created", uid, email)
	}

	// 2b. Super-admin login account (tenant-less; find-or-create).
	if existing, err := userIDByEmail(ctx, pool, adminEmail); err != nil {
		return err
	} else if existing != 0 {
		log.Printf("✓ admin #%d %s (already existed)", existing, adminEmail)
	} else {
		hash, err := console.HashPassword(adminPassword)
		if err != nil {
			return fmt.Errorf("hash admin password: %w", err)
		}
		uid, err := users.Create(ctx, adminEmail, hash, console.RoleAdmin, nil)
		if err != nil {
			return fmt.Errorf("create admin: %w", err)
		}
		log.Printf("✓ admin #%d %s created", uid, adminEmail)
	}

	// 2c. Sales login account, then make it the demo tenant's sales owner so the
	// Sales Console has a customer to show.
	salesID, err := userIDByEmail(ctx, pool, salesEmail)
	if err != nil {
		return err
	}
	if salesID == 0 {
		hash, err := console.HashPassword(salesPassword)
		if err != nil {
			return fmt.Errorf("hash sales password: %w", err)
		}
		salesID, err = users.Create(ctx, salesEmail, hash, console.RoleSales, nil)
		if err != nil {
			return fmt.Errorf("create sales: %w", err)
		}
		log.Printf("✓ sales #%d %s created", salesID, salesEmail)
	} else {
		log.Printf("✓ sales #%d %s (already existed)", salesID, salesEmail)
	}
	if _, err := pool.Exec(ctx, `UPDATE tenants SET sales_owner_id=$1 WHERE id=$2`, salesID, tenantID); err != nil {
		return fmt.Errorf("assign sales owner: %w", err)
	}
	log.Printf("✓ tenant #%d 归属 sales #%d", tenantID, salesID)

	// 3. Wallet top-up (idempotent by ref; creates the wallet row if missing).
	if err := billing.NewRepo(pool).Topup(ctx, tenantID, topupCents, "seed-demo"); err != nil {
		return fmt.Errorf("topup: %w", err)
	}
	log.Printf("✓ wallet credited $%.2f", float64(topupCents)/100)

	// 4. Per-country unit price (upsert).
	if err := pricing.NewRepo(pool).SetPrice(ctx, tenantID, country, unitCents); err != nil {
		return fmt.Errorf("set price: %w", err)
	}
	log.Printf("✓ price %s = $%.2f / msg", country, float64(unitCents)/100)

	fmt.Print(summary())
	return nil
}

func findOrCreateTenant(ctx context.Context, pool *pgxpool.Pool, name string) (int64, error) {
	var id int64
	err := pool.QueryRow(ctx, `SELECT id FROM tenants WHERE name=$1 ORDER BY id LIMIT 1`, name).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return 0, fmt.Errorf("lookup tenant: %w", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO tenants (name, status) VALUES ($1, 'active') RETURNING id`, name,
	).Scan(&id); err != nil {
		return 0, fmt.Errorf("create tenant: %w", err)
	}
	return id, nil
}

func userIDByEmail(ctx context.Context, pool *pgxpool.Pool, e string) (int64, error) {
	var id int64
	err := pool.QueryRow(ctx, `SELECT id FROM console_users WHERE email=$1`, e).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("lookup user: %w", err)
	}
	return id, nil
}

func summary() string {
	return fmt.Sprintf(`
────────────────────────────────────────────
 演示数据已就绪,可用以下账号登录:

   [客户] 邮箱: %s   密码: %s
   [超管] 邮箱: %s   密码: %s
   [销售] 邮箱: %s   密码: %s

   余额:   $%.2f
   单价:   %s  $%.2f / 条
────────────────────────────────────────────
`, email, password, adminEmail, adminPassword, salesEmail, salesPassword, float64(topupCents)/100, country, float64(unitCents)/100)
}
