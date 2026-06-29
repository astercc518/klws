// Command console is the wadist management web platform (admin/sales/customer).
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/acme/wadist/internal/api"
	"github.com/acme/wadist/internal/billing"
	"github.com/acme/wadist/internal/config"
	"github.com/acme/wadist/internal/console"
	walog "github.com/acme/wadist/internal/log"
	"github.com/acme/wadist/internal/pricing"
	"github.com/acme/wadist/internal/store"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	srv, stop, err := run(ctx)
	if err != nil {
		log.Fatalf("run: %v", err)
	}
	log.Printf("console up on %s", srv.Addr())
	<-ctx.Done()
	log.Printf("shutdown signal received")
	stop()
}

// run assembles the JSON API server and returns it plus a stop func. It is the
// seam the smoke test drives. The HTML console view layer has been replaced by
// the Gin API (internal/api); all security primitives (sessions, users, RLS,
// billing, pricing) are reused unchanged.
func run(ctx context.Context) (*api.Server, func(), error) {
	baseCfg, err := config.Load() // requires WADIST_POSTGRES_DSN; provides Store()+RedisAddr
	if err != nil {
		return nil, nil, err
	}
	if baseCfg.AppTenantDSN == "" || baseCfg.AppSystemDSN == "" {
		return nil, nil, fmt.Errorf("console: WADIST_APP_TENANT_DSN and WADIST_APP_SYSTEM_DSN are required (tenant RLS isolation must not fall back to the superuser pool)")
	}
	webCfg, err := console.LoadConfig()
	if err != nil {
		return nil, nil, err
	}

	logger, flush, err := walog.Production()
	if err != nil {
		return nil, nil, err
	}
	mgr, err := store.Init(ctx, baseCfg.Store(), logger)
	if err != nil {
		flush()
		return nil, nil, err
	}

	rdb := goredis.NewClient(&goredis.Options{Addr: baseCfg.RedisAddr})
	sessions := console.NewSessionStore(rdb, webCfg.SessionTTL)
	users := console.NewUserRepo(mgr.SystemPool())

	bill := billing.NewRepo(mgr.SystemPool())
	price := pricing.NewRepo(mgr.SystemPool())
	tenants := console.NewTenantRepo(mgr.SystemPool())

	// JSON API server — reuses the SAME session/user/tenant/billing/pricing
	// instances and the store.Manager's RLS machinery; no security logic is
	// reinvented and no red-line package is touched.
	srv := api.NewServer(api.Deps{
		Mgr:        mgr,
		Billing:    bill,
		Pricing:    price,
		Users:      users,
		Tenants:    tenants,
		Sessions:   sessions,
		SessionKey: webCfg.SessionKey,               // same HMAC key as the old console cookie
		BlindKey:   baseCfg.BlindIndexKey,           // same blind-index key for recipient dedup
		CORSOrigin: os.Getenv("WADIST_CORS_ORIGIN"), // empty -> defaults to http://localhost:3000
	})
	if len(baseCfg.BlindIndexKey) == 0 {
		log.Printf("api: WADIST_BLIND_INDEX_KEY not set — send endpoints will fail-closed (ErrSendNotConfigured)")
	}
	if err := srv.Start(webCfg.Addr); err != nil {
		mgr.Close()
		flush()
		_ = rdb.Close()
		return nil, nil, err
	}

	var stopOnce sync.Once
	stop := func() {
		stopOnce.Do(func() {
			srv.SetReady(false)
			sctx, c := context.WithTimeout(context.Background(), 5*time.Second)
			defer c()
			_ = srv.Shutdown(sctx)
			mgr.Close()
			flush()
			_ = rdb.Close()
		})
	}
	return srv, stop, nil
}
