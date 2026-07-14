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
	"github.com/acme/wadist/internal/audit"
	"github.com/acme/wadist/internal/billing"
	"github.com/acme/wadist/internal/cluster"
	"github.com/acme/wadist/internal/config"
	"github.com/acme/wadist/internal/console"
	walog "github.com/acme/wadist/internal/log"
	"github.com/acme/wadist/internal/pricing"
	"github.com/acme/wadist/internal/receipt"
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
	// Redis is a hard dependency for store.Init (newManager fails closed with
	// "store: redis is required (proxy allocator)" when cfg.Redis == nil —
	// see internal/store/manager.go) and the admin ownership reads
	// (Mgr.OwnedJIDs/OwnersFor) need it too. Create the client and fail fast
	// with a clear message, mirroring cmd/wadist/main.go's redis-injection
	// block, instead of dying deeper inside store.Init. This is the ONLY
	// redis client the console process creates — it is reused below for the
	// web session store so we never open two connections to the same redis.
	rdb := goredis.NewClient(&goredis.Options{Addr: baseCfg.RedisAddr})
	pingCtx, pingCancel := context.WithTimeout(ctx, 5*time.Second)
	err = rdb.Ping(pingCtx).Err()
	pingCancel()
	if err != nil {
		flush()
		_ = rdb.Close()
		return nil, nil, fmt.Errorf("redis required but unreachable: %w", err)
	}

	sc := baseCfg.Store()
	sc.Redis = rdb
	// Distinct BadgerDir: the console never sends WhatsApp messages, so the
	// Badger session store newManager opens (badger is now the sole session
	// backend, unconditionally opened) is unused here — but it still takes
	// an exclusive on-disk dir-lock. It must NOT reuse the send node's dir:
	// docker-compose.worker.yml's `wadist` service shares the SAME .env as
	// this `backend` service, so baseCfg.Store()'s WADIST_BADGER_DIR (which
	// points at the send node's persistent volume, /var/lib/wadist/badger)
	// would otherwise collide if the two are ever co-located. Use a
	// console-specific override (WADIST_CONSOLE_BADGER_DIR) and default to a
	// container-local ephemeral path — verified writable by the distroless
	// nonroot image (/var/lib is root-owned 0755 and NOT writable by
	// nonroot; /tmp is 1777 and is) — since the store is unused, no
	// persistent volume is needed.
	if v := os.Getenv("WADIST_CONSOLE_BADGER_DIR"); v != "" {
		sc.BadgerDir = v
	} else {
		sc.BadgerDir = "/tmp/wadist-console-badger"
	}

	mgr, err := store.Init(ctx, sc, logger)
	if err != nil {
		flush()
		_ = rdb.Close()
		return nil, nil, err
	}

	sessions := console.NewSessionStore(rdb, webCfg.SessionTTL)
	users := console.NewUserRepo(mgr.SystemPool())

	bill := billing.NewRepo(mgr.SystemPool())
	price := pricing.NewRepo(mgr.SystemPool())
	tenants := console.NewTenantRepo(mgr.SystemPool())

	// EvoCluster: same construction as cmd/wadist/main.go's worker-side
	// wiring (baseCfg is the SAME config.Load() the worker reads, so
	// EvolutionNodes/APIKey/WebhookSecret are already populated from env —
	// no config changes needed here). DORMANT: injected into Deps only, no
	// route reads it yet (see internal/api/router.go Deps.EvoCluster doc).
	// NewEvoCluster tolerates an empty node map (returns an empty registry,
	// never panics) though in practice config.parseNodes always falls back
	// to a "default" node.
	evoCluster := cluster.NewEvoCluster(baseCfg.EvolutionNodes, baseCfg.EvolutionAPIKey, baseCfg.EvolutionWebhookSecret)

	// JSON API server — reuses the SAME session/user/tenant/billing/pricing
	// instances and the store.Manager's RLS machinery; no security logic is
	// reinvented and no red-line package is touched.
	srv := api.NewServer(api.Deps{
		Mgr:      mgr,
		Billing:  bill,
		Pricing:  price,
		Users:    users,
		Tenants:  tenants,
		Sessions: sessions,
		Audit:    audit.NewAuditWriter(mgr.SystemPool()),
		Receipt:  receipt.New(mgr.SystemPool()),
		// Super/bootstrap admins hidden from the console + write-protected (403).
		ProtectedAdmins: api.ParseProtectedAdmins(os.Getenv("WADIST_PROTECTED_ADMINS")),
		SessionKey:      webCfg.SessionKey,               // same HMAC key as the old console cookie
		BlindKey:        baseCfg.BlindIndexKey,           // same blind-index key for recipient dedup
		CORSOrigin:      os.Getenv("WADIST_CORS_ORIGIN"), // empty -> defaults to http://localhost:3000

		EvolutionWebhookSecret: baseCfg.EvolutionWebhookSecret,
		EvoCluster:             evoCluster,
		EvoCapPerNode:          baseCfg.EvolutionCapPerNode,
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
