// Command wadist is the distribution node entrypoint.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os/signal"
	"syscall"
	"time"

	"github.com/hibiken/asynq"
	"github.com/prometheus/client_golang/prometheus"
	goredis "github.com/redis/go-redis/v9"

	"github.com/acme/wadist/internal/audit"
	"github.com/acme/wadist/internal/billing"
	"github.com/acme/wadist/internal/cluster"
	"github.com/acme/wadist/internal/config"
	"github.com/acme/wadist/internal/crypto"
	"github.com/acme/wadist/internal/dispatch"
	walog "github.com/acme/wadist/internal/log"
	"github.com/acme/wadist/internal/metrics"
	"github.com/acme/wadist/internal/node"
	"github.com/acme/wadist/internal/sendgate"
	"github.com/acme/wadist/internal/store"
)

// placeholderUploader is a stub Uploader until the whatsmeow media adapter lands in a later milestone.
type placeholderUploader struct{}

func (placeholderUploader) Upload(_ context.Context, _ string, _ []byte, _, _ string) (*dispatch.MediaHandle, error) {
	return nil, errors.New("uploader: not wired (pre-M-send)")
}

// priceFor returns a basic per-country price in minor units.
// Real pricing table is introduced in a later milestone.
var priceTable = map[string]int64{
	"IN": 1,
	"US": 5,
	"GB": 4,
	"BR": 2,
	"ID": 1,
}

func priceFor(country string) int64 {
	if p, ok := priceTable[country]; ok {
		return p
	}
	return 3 // default minor-unit price
}

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	srv, stop, err := run(ctx, cfg)
	if err != nil {
		log.Fatalf("run: %v", err)
	}
	log.Printf("wadist up; metrics on %s", srv.Addr())
	<-ctx.Done()
	log.Printf("shutdown signal received")
	stop()
}

// run assembles all dependencies, starts the /metrics server and the asynq
// send worker, and returns a stop func. It is the seam the smoke test drives.
func run(ctx context.Context, cfg *config.Config) (*metrics.Server, func(), error) {
	logger, flush, err := walog.Production()
	if err != nil {
		return nil, nil, err
	}

	mgr, err := store.Init(ctx, cfg.Store(), logger)
	if err != nil {
		flush()
		return nil, nil, err
	}

	// Register this node in the cluster.
	if err := mgr.UpsertNodeHeartbeat(ctx, cfg.NodeID); err != nil {
		mgr.Close()
		flush()
		return nil, nil, fmt.Errorf("upsert node heartbeat: %w", err)
	}

	pool := mgr.Pool()

	// Security wiring: Cipher + KeyRepo + AuditWriter + data-residency Router.
	// When MasterKey is empty the node starts without encryption (dev/CI mode).
	billingRepo := billing.NewRepo(pool)
	if len(cfg.MasterKey) > 0 {
		kr, err := crypto.NewKeyRepo(mgr.SystemPool(), cfg.MasterKey)
		if err != nil {
			mgr.Close()
			flush()
			return nil, nil, fmt.Errorf("crypto keyrepo: %w", err)
		}
		cipher := crypto.NewCipher(kr)
		aw := audit.NewAuditWriter(mgr.SystemPool())
		router := crypto.NewRouter(mgr.Pool())
		router.Register(cfg.NodeRegion, mgr.Pool())
		// Wire billing with RLS + audit.
		billingRepo.UseTenantRLS(mgr).UseAudit(aw)
		// TODO(PII import path): pass cipher/router to campaign import handler in M11.
		log.Printf("security enabled (region=%s cipher=%T router=%T)", cfg.NodeRegion, cipher, router)
	} else {
		log.Printf("security disabled (no WADIST_MASTER_KEY)")
	}

	reg := cluster.NewRegistry()

	promReg := prometheus.NewRegistry()
	m := metrics.New(promReg)
	promReg.MustRegister(metrics.NewDBCollector(pool, 10*time.Second, reg))

	rdb := goredis.NewClient(&goredis.Options{Addr: cfg.RedisAddr})
	adm := sendgate.NewAdmission(rdb)
	gate := sendgate.NewSendGate(pool, adm, 3*time.Second)

	asynqClient := asynq.NewClient(asynq.RedisClientOpt{Addr: cfg.RedisAddr})
	enqueuer := dispatch.NewAsynqEnqueuer(asynqClient, "default", 3)

	dispatcher := dispatch.NewDispatcher(pool, billingRepo, enqueuer, priceFor, 3*time.Second).WithMetrics(m)

	// cluster.NewRoutingSender routes sends to live sessions; returns a clear
	// error when no active session exists rather than silently succeeding.
	worker := dispatch.NewSendWorker(pool, gate, billingRepo, cluster.NewRoutingSender(reg), placeholderUploader{}).WithMetrics(m)

	asynqSrv := asynq.NewServer(
		asynq.RedisClientOpt{Addr: cfg.RedisAddr},
		asynq.Config{Concurrency: 32, ShutdownTimeout: cfg.ShutdownTimeout, Queues: map[string]int{"default": 6, "takeover": 1}},
	)
	mux := asynq.NewServeMux()
	dispatch.RegisterSendHandler(mux, worker, func(_ context.Context, _ int64) (string, string, string, []byte, error) {
		return "", "", "", nil, errors.New("resolver: not wired (pre-M-send)")
	})

	// Build a real SessionFactory: acquire device store + optional proxy + whatsmeow conn.
	factory := node.SessionFactory(func(fctx context.Context, jid string, lock cluster.DeviceLockHandle) (*cluster.Session, error) {
		device, err := mgr.GetDeviceStore(fctx, jid)
		if err != nil {
			return nil, err
		}
		proxy, err := mgr.GetBoundProxy(fctx, jid)
		var proxyBinding *store.ProxyBinding
		if err == nil {
			proxyBinding = proxy
		} else if !errors.Is(err, store.ErrProxyNotBound) {
			return nil, err
		}
		// proxy may be nil if no proxy is bound — that's acceptable
		conn := cluster.NewWAConn(device, logger, proxyBinding)
		if err := conn.Connect(fctx); err != nil {
			return nil, err
		}
		return cluster.NewSession(jid, conn, lock), nil
	})

	sup := cluster.NewSupervisor(reg, cluster.SupervisorOpts{
		StopIntake:      func() { asynqSrv.Shutdown() },
		AfterSessions:   func(c context.Context) { _ = mgr.DeregisterNode(c, cfg.NodeID) },
		CloseStore:      mgr.Close,
		Flush:           flush,
		Limit:           cfg.MaxConcurrentStarts,
		ShutdownTimeout: cfg.ShutdownTimeout,
	})

	orch := node.NewOrchestrator(mgr, reg, sup, cfg.NodeID, factory, cfg.HeartbeatInterval, logger)

	takeEnq := node.NewTakeoverEnqueuer(asynqClient, cfg.NodeStaleness)
	node.RegisterTakeoverHandler(mux, orch)

	// Kick off initial account startup with bounded concurrency.
	jids, err := mgr.ListActiveAccounts(ctx)
	if err != nil {
		log.Printf("warn: list active accounts at startup: %v — starting with zero accounts", err)
		jids = nil
	}
	sup.Go(func(lctx context.Context) error {
		return sup.StartAccounts(lctx, jids, orch.StartAccountWithLock)
	})

	// Start the supervised dispatch loop: ticks every second, dispatches running
	// campaigns. Per-tick errors are logged but do not exit the loop (transient
	// DB errors recover on the next tick). The loop exits when lctx is cancelled.
	sup.Go(func(lctx context.Context) error {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-lctx.Done():
				return lctx.Err()
			case <-t.C:
				if _, err := dispatcher.DispatchRunning(lctx, 100); err != nil {
					log.Printf("dispatch loop: %v", err)
					// log and continue — transient DB error; next tick retries
				}
			}
		}
	})
	sup.Go(func(lctx context.Context) error {
		return orch.RunHeartbeat(lctx, cfg.HeartbeatInterval)
	})
	sup.Go(func(lctx context.Context) error {
		return orch.RunTakeoverScanner(lctx, cfg.TakeoverScanInterval, cfg.NodeStaleness, takeEnq)
	})

	go func() { _ = asynqSrv.Run(mux) }()

	srv := metrics.NewServer(cfg.MetricsAddr, promReg)
	if err := srv.Start(); err != nil {
		// Supervisor owns asynq/store/flush; call Shutdown then close redis/asynq client only.
		sup.Shutdown()
		_ = asynqClient.Close()
		_ = rdb.Close()
		return nil, nil, err
	}

	// stop() delegates asynq intake, store close, and logger flush to the
	// Supervisor (via StopIntake/CloseStore/Flush opts). It only adds the
	// metrics HTTP server and redis/asynq client handles not owned by Supervisor.
	stop := func() {
		sup.Shutdown() // intake → loops → sessions → store → flush
		sctx, c := context.WithTimeout(context.Background(), 5*time.Second)
		defer c()
		_ = srv.Shutdown(sctx) // metrics http (last, so scrapes still work during shutdown)
		_ = asynqClient.Close()
		_ = rdb.Close()
	}
	return srv, stop, nil
}

// loadForTest is a helper used by main_smoke_test.go to build a *config.Config
// with a random metrics port so parallel tests don't collide.
func loadForTest(t interface {
	Helper()
	Fatalf(string, ...any)
}) *config.Config {
	t.Helper()
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("loadForTest: %v", err)
	}
	cfg.MetricsAddr = "127.0.0.1:0" // bind ephemeral port
	return cfg
}

// ensure placeholderUploader satisfies the dispatch interface at compile time.
var _ dispatch.Uploader = placeholderUploader{}
