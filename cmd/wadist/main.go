// Command wadist is the distribution node entrypoint.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/hibiken/asynq"
	"github.com/prometheus/client_golang/prometheus"
	goredis "github.com/redis/go-redis/v9"

	"github.com/acme/wadist/internal/audit"
	"github.com/acme/wadist/internal/billing"
	"github.com/acme/wadist/internal/cluster"
	"github.com/acme/wadist/internal/config"
	"github.com/acme/wadist/internal/control"
	"github.com/acme/wadist/internal/crypto"
	"github.com/acme/wadist/internal/dispatch"
	walog "github.com/acme/wadist/internal/log"
	"github.com/acme/wadist/internal/metrics"
	"github.com/acme/wadist/internal/node"
	"github.com/acme/wadist/internal/pricing"
	"github.com/acme/wadist/internal/receipt"
	"github.com/acme/wadist/internal/riskbreaker"
	"github.com/acme/wadist/internal/sendgate"
	"github.com/acme/wadist/internal/store"
)

// takeoverConcurrency is the asynq worker concurrency for the (low-throughput)
// takeover queue. Fixed — the send path no longer runs on asynq, so this no
// longer needs to scale with send worker count.
const takeoverConcurrency = 8

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

// run assembles all dependencies, starts the /metrics server and the pump
// send worker, and returns a stop func. It is the seam the smoke test drives.
func run(ctx context.Context, cfg *config.Config) (*metrics.Server, func(), error) {
	logger, flush, err := walog.Production()
	if err != nil {
		return nil, nil, err
	}

	promReg := prometheus.NewRegistry()
	m := metrics.New(promReg)

	rdb := goredis.NewClient(&goredis.Options{Addr: cfg.RedisAddr})
	sc := cfg.Store()
	sc.Redis = rdb
	sc.OwnershipTTL = cfg.NodeStaleness
	if cfg.HeartbeatInterval >= cfg.NodeStaleness {
		log.Printf("warn: HeartbeatInterval(%s) >= NodeStaleness(%s); ownership leases may expire before refresh", cfg.HeartbeatInterval, cfg.NodeStaleness)
	}
	mgr, err := store.Init(ctx, sc, logger)
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

	promReg.MustRegister(metrics.NewDBCollector(pool, 10*time.Second, reg))

	adm := sendgate.NewAdmission(rdb, sendgate.BackoffParams{On: cfg.BackoffOn, Factor: cfg.BackoffFactor, Max: cfg.BackoffMax, TTL: cfg.BackoffTTL})
	gate := sendgate.NewSendGate(pool, adm, 3*time.Second)
	if cfg.BackoffOn {
		log.Printf("admission backoff on (factor=%d max=%d ttl=%s)", cfg.BackoffFactor, cfg.BackoffMax, cfg.BackoffTTL)
	}
	asynqClient := asynq.NewClient(asynq.RedisClientOpt{Addr: cfg.RedisAddr})

	// Send driver: the in-memory token-bucket Rolling-Wave pump (sole driver).
	// asynq is kept around for the takeover queue only (see mux/asynqSrv below).
	//
	// Boot recovery FIRST: reclaim recipients orphaned by a prior crash
	// (assigned to an account but never sent) before any new dispatch begins.
	if n, err := dispatch.ReclaimOrphanedAssignments(ctx, pool); err != nil {
		log.Printf("warn: reclaim orphaned assignments: %v", err)
	} else if n > 0 {
		log.Printf("pump boot: reclaimed %d orphaned assignments", n)
	}
	pe := dispatch.NewPumpEnqueuer(cfg.PumpBuffer)
	dispatcher := dispatch.NewDispatcher(pool, billingRepo, pe, priceFor, 3*time.Second).WithMetrics(m)

	// cluster.NewRoutingSender routes sends to live sessions; returns a clear
	// error when no active session exists rather than silently succeeding.
	// When AntifpOn, upgrade to policy-based sender with typing pacing + fence.
	var routing *cluster.RoutingSender
	if cfg.AntifpOn {
		routing = cluster.NewRoutingSenderWithPolicy(reg, cfg.FenceOnSend,
			cluster.TypingPolicy{Min: cfg.TypingMin, Max: cfg.TypingMax})
	} else {
		routing = cluster.NewRoutingSender(reg)
	}
	priceRepo := pricing.NewRepo(pool)
	// priceFor falls back to the existing per-country default table when a tenant
	// has no explicit price configured.
	worker := dispatch.NewSendWorker(pool, gate, billingRepo, routing, placeholderUploader{}).
		WithMetrics(m).
		WithCanary(cfg.CanaryPercent).
		WithPricing(func(ctx context.Context, tenantID int64, country string) int64 {
			return priceRepo.PriceFor(ctx, tenantID, country, priceFor(country))
		})

	// sendResolver loads the campaign body/media for a send. Fed to the Pump
	// below so it drives the resolution logic for every send.
	sendResolver := func(ctx context.Context, campaignID int64) (string, string, string, []byte, error) {
		body, mediaSha, mime, err := mgr.CampaignSendable(ctx, campaignID)
		if err != nil {
			return "", "", "", nil, err
		}
		// Media bytes are out of scope for Module A (text-first). mediaSha/mime
		// are surfaced so the Send adapter can fail loud on media campaigns.
		return body, mediaSha, mime, nil, nil
	}

	// asynqSrv/mux exist solely for the takeover queue (handler registered
	// below, once `orch` exists); no send task is ever routed through asynq —
	// the Pump owns the send path exclusively.
	asynqSrv := asynq.NewServer(
		asynq.RedisClientOpt{Addr: cfg.RedisAddr},
		asynq.Config{Concurrency: takeoverConcurrency, ShutdownTimeout: cfg.ShutdownTimeout, Queues: map[string]int{"default": 6, "takeover": 1}},
	)
	mux := asynq.NewServeMux()

	pump := dispatch.NewPump(pe, worker, sendResolver, cfg.SendRate, cfg.SendWorkers)

	// Delivery/read receipt recorder. Wired into each WA connection unless
	// WADIST_RECEIPTS=off (rollback knob; the send path is unaffected either way).
	receiptRec := receipt.New(mgr.SystemPool())
	receiptsOn := os.Getenv("WADIST_RECEIPTS") != "off"

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
		// Receipt callback for THIS account; jid is the assigned_jid we match on.
		// Receipts arrive asynchronously, so use a background context.
		var onReceipt cluster.ReceiptFunc
		if receiptsOn {
			onReceipt = func(ids []string, kind string, at time.Time) {
				if err := receiptRec.Record(context.Background(), receipt.Event{
					MessageIDs: ids, SenderJID: jid, Kind: receipt.Kind(kind), At: at,
				}); err != nil {
					log.Printf("receipt record (%s): %v", jid, err)
				}
			}
		}
		// proxy may be nil if no proxy is bound — that's acceptable
		conn := cluster.NewWAConn(device, logger, proxyBinding, onReceipt, cfg.GhostReaper == "on")
		if err := conn.Connect(fctx); err != nil {
			return nil, err
		}
		if cfg.AntifpOn {
			if pc, ok := any(conn).(cluster.PresenceConn); ok {
				_ = pc.SetPresence(fctx, true)
			}
		}
		return cluster.NewSessionWithSender(jid, conn, lock, conn), nil
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

	// Control-plane: scheduler + sticky proxy + working-set.
	sched := control.NewScheduler(rdb)
	sticky := control.StickyBindProxy(
		func(ctx context.Context, jid string) (bool, error) {
			_, err := mgr.GetBoundProxy(ctx, jid)
			if errors.Is(err, store.ErrProxyNotBound) {
				return false, nil
			}
			return err == nil, err
		},
		func(ctx context.Context, jid, cc string) error {
			_, e := mgr.BindProxy(ctx, jid, cc)
			return e
		},
	)
	deps := control.Deps{
		Warm: func(ctx context.Context, jid string) (bool, error) {
			sess, err := orch.StartAccountWithLock(ctx, jid)
			return sess != nil, err
		},
		Evict: func(ctx context.Context, jid string, linger time.Duration) {
			if s, ok := reg.Get(jid); ok {
				s.GracefulClose(ctx, linger)
			}
		},
		IsWarm:    func(jid string) bool { _, ok := reg.Get(jid); return ok },
		BindProxy: sticky,
	}

	// activeCountries: prefer WADIST_COUNTRIES env (comma-separated), else query
	// distinct non-null country_code from proxy_pool. Falls back to empty slice on
	// any error (WorkingSet active-warm loop is skipped when ccs is empty).
	activeCountries := loadActiveCountries(ctx, mgr)

	ws := control.NewWorkingSet(rdb, sched, activeCountries, control.Config{
		Target:          cfg.WarmTarget,
		WarmReqBatch:    cfg.WarmReqBatch,
		KeepWarmHorizon: cfg.KeepWarmHorizon,
		Linger:          cfg.Linger,
		BootRamp:        cfg.BootRamp,
	}, deps)

	// Wire RoutingSender warm-request hook: on a send miss, push jid|cc to warm:req.
	routing = routing.WithWarmRequest(func(jid string) {
		cc := ""
		if pb, err := mgr.GetBoundProxy(context.Background(), jid); err == nil {
			cc = pb.Country
		}
		rdb.RPush(context.Background(), "warm:req", jid+"|"+cc)
	})

	// Kick off initial account startup with bounded concurrency.
	jids, err := mgr.ListActiveAccounts(ctx)
	if err != nil {
		log.Printf("warn: list active accounts at startup: %v — starting with zero accounts", err)
		jids = nil
	}

	// Seed the scheduler with per-account next-eligible times, then start the
	// WorkingSet loop. This REPLACES the previous full-startup StartAccounts call.
	sup.Go(func(lctx context.Context) error {
		accts := make([]control.AccountCC, 0, len(jids))
		now := time.Now().UnixMilli()
		rng := rand.New(rand.NewSource(now)) //nolint:gosec
		for _, jid := range jids {
			cc := countryOf(lctx, mgr, jid)
			quota := randQuota(rng, cfg.DailyQuotaMin, cfg.DailyQuotaMax)
			accts = append(accts, control.AccountCC{
				JID:    jid,
				CC:     cc,
				NextMs: control.NextEligibleMs(now, quota, control.Window{StartHour: 9, EndHour: 22}, rng),
			})
		}
		return sched.SeedDue(lctx, accts)
	})
	sup.Go(func(lctx context.Context) error { return ws.Run(lctx, cfg.WSTick) })

	// Ghost Reaper (M6): bounded reclamation of dead whatsmeow sockets. Only when
	// WADIST_GHOST_REAPER=on — default off keeps auto-reconnect on and runs no
	// reaper (zero blast radius). Transient ghosts are re-warmed via warm:req
	// (empty cc reuses the sticky proxy binding); terminal LoggedOut/StreamReplaced
	// ghosts are marked logged_out (excluded from the active set, not re-warmed).
	if cfg.GhostReaper == "on" {
		reaper := node.NewGhostReaper(
			node.GhostReaperConfig{TTL: cfg.GhostTTL, Max: cfg.GhostMax},
			node.GhostReaperDeps{
				Now:      time.Now,
				Snapshot: func() []node.GhostCandidate { return node.GhostCandidatesFrom(reg.Snapshot()) },
				Reap: func(ctx context.Context, jid string) error {
					if s, ok := reg.Remove(jid); ok {
						s.Close(ctx)
					}
					return nil
				},
				RequestWarm:   func(ctx context.Context, jid string) error { return rdb.RPush(ctx, "warm:req", jid+"|").Err() },
				MarkLoggedOut: mgr.MarkAccountLoggedOut,
				Metrics:       m,
			},
		)
		sup.Go(func(lctx context.Context) error { return reaper.Run(lctx, cfg.GhostTick) })
		log.Printf("ghost reaper on (ttl=%s max=%d tick=%s)", cfg.GhostTTL, cfg.GhostMax, cfg.GhostTick)
	}
	if cfg.BootRamp > 0 {
		log.Printf("boot warm ramp on (%s)", cfg.BootRamp)
	}

	// Proxy janitor: periodic sweep that auto-rebinds accounts stuck on dead
	// proxies (Release → Evict(linger=0) → RequestWarm) so the next WorkingSet
	// tick's StickyBindProxy picks a live proxy.
	janitor := control.NewJanitor(
		control.JanitorConfig{Batch: cfg.ProxyJanitorBatch},
		control.JanitorDeps{
			ListDeadProxyAccounts: func(ctx context.Context, limit int) ([]control.DeadProxyAccount, error) {
				rows, err := mgr.ListDeadProxyAccountsAll(ctx, limit)
				if err != nil {
					return nil, err
				}
				out := make([]control.DeadProxyAccount, len(rows))
				for i, r := range rows {
					out[i] = control.DeadProxyAccount{JID: r.JID, CC: r.CountryCode}
				}
				return out, nil
			},
			Release: func(ctx context.Context, jid string) error { return mgr.ReleaseProxy(ctx, jid) },
			Evict: func(ctx context.Context, jid string, linger time.Duration) {
				if s, ok := reg.Get(jid); ok {
					s.GracefulClose(ctx, linger)
				}
			},
			RequestWarm: func(ctx context.Context, jid, cc string) error {
				return rdb.RPush(ctx, "warm:req", jid+"|"+cc).Err()
			},
			OnRebind: func() { m.IncProxyRebind() },
		},
	)
	sup.Go(func(lctx context.Context) error { return janitor.Run(lctx, cfg.ProxyJanitorInterval) })

	// Reconciler: periodic sweep that re-enqueues active accounts missing from
	// the scheduler's due set (closes the "scheduling loop" self-heal gap —
	// e.g. accounts created while this node was down, or dropped from due:{cc}
	// by an unrelated bug). Resident (already-warm) and already-queued accounts
	// are skipped; only fresh accounts get a NextDue score.
	reconciler := control.NewReconciler(rdb, sched, activeCountries, control.ReconcileDeps{
		ListActive: mgr.ListActiveAccounts,
		CountryOf:  func(ctx context.Context, jid string) string { return countryOf(ctx, mgr, jid) },
		NextDue: func(cc string) int64 {
			now := time.Now().UnixMilli()
			// Reuse the same seed-time formula as startup seeding; rng is built
			// fresh per call to avoid sharing *rand.Rand across goroutines.
			rng := rand.New(rand.NewSource(now)) //nolint:gosec
			q := randQuota(rng, cfg.DailyQuotaMin, cfg.DailyQuotaMax)
			return control.NextEligibleMs(now, q, control.Window{StartHour: 9, EndHour: 22}, rng)
		},
	})
	sup.Go(func(lctx context.Context) error {
		if cfg.ReconcileInterval <= 0 {
			<-lctx.Done()
			return lctx.Err()
		}
		t := time.NewTicker(cfg.ReconcileInterval)
		defer t.Stop()
		for {
			select {
			case <-lctx.Done():
				return lctx.Err()
			case <-t.C:
				n, err := reconciler.Tick(lctx, time.Now().UnixMilli())
				if err != nil {
					log.Printf("reconciler: %v", err)
					continue
				}
				m.IncScheduleReconciled(n)
			}
		}
	})

	// pump.Run drains PumpEnqueuer's channel and runs the send pipeline; the
	// filler below is the sole producer feeding it.
	sup.Go(func(lctx context.Context) error { return pump.Run(lctx) })

	// Risk Governor: adaptive-τ AIMD loop over the pump's send rate, driven
	// by the fleet-wide ban rate. Only active when explicitly enabled —
	// default is off, which leaves the pump at fixed cfg.SendRate (M3
	// behavior, zero blast radius).
	//
	// Wired with mgr.SystemPool() (BYPASSRLS), matching riskbreaker below:
	// FleetBanRate queries campaign_recipients, which has FORCE ROW LEVEL
	// SECURITY. With a tenant-scoped (RLS) pool the fleet-wide aggregate
	// would silently narrow to one tenant, so mgr.Pool() must never be
	// used here.
	if cfg.RiskGovernor == "on" {
		gov := dispatch.NewGovernor(mgr.SystemPool(), pump.SetRate,
			dispatch.GovParams{SLO: cfg.GovSLO, Step: cfg.GovStep, Factor: cfg.GovFactor, MinRate: cfg.GovMinRate, MaxRate: cfg.SendRate},
			cfg.GovWindowSec, cfg.GovMinSample, cfg.SendRate /*start τ at the ceiling*/)

		// Segment Governor (L3): per-country slowdown layered on top of the
		// fleet-wide L4 governor above. Only enabled when BOTH RiskGovernor
		// and SegmentGovernor are "on" — default off leaves gov.setSegRate
		// nil, so the segment pass in gov.Run is a no-op (M4 behavior,
		// zero blast radius).
		if cfg.SegmentGovernor == "on" {
			gov.WithSegments(pump.SetSegmentRate, dispatch.SegParams{
				Mult: cfg.SegMult, SegSLO: cfg.SegSLO, SlowRate: cfg.SegSlowRate, MinSample: cfg.SegMinSample,
			})
			log.Printf("segment governor on (mult=%.1f segSLO=%.3f slow=%.1f/s minSample=%d)",
				cfg.SegMult, cfg.SegSLO, cfg.SegSlowRate, cfg.SegMinSample)
		}

		sup.Go(func(lctx context.Context) error {
			return gov.Run(lctx, time.Duration(cfg.GovIntervalMs)*time.Millisecond)
		})
		log.Printf("risk governor on (SLO=%.3f step=%.1f factor=%.2f min=%.1f max=%.1f window=%ds interval=%dms)",
			cfg.GovSLO, cfg.GovStep, cfg.GovFactor, cfg.GovMinRate, cfg.SendRate, cfg.GovWindowSec, cfg.GovIntervalMs)
	}

	// Filler: backpressure-driven top-up (no 1s pulse) — tops the buffer up
	// to its capacity every 50ms via Dispatcher.DispatchRunningBudget, which
	// caps enqueues to `room` IN TOTAL across all running campaigns. This is
	// required: with ≥2 running campaigns, an uncapped dispatch would hand
	// each campaign a full batch, and a second campaign's dispatchBatch could
	// push more payloads than there are free channel slots, hitting
	// ErrPumpFull mid-transaction — rolling back the DB assignment while
	// the already-pushed channel payloads survive (channel ops aren't
	// transactional), orphaning them (sent with sent_today never bumped)
	// and double-assigning the rolled-back recipient. Budget-capping the
	// total sidesteps this: a single dispatchBatch pushing at most its
	// budget always fits, since the filler is the sole producer and only
	// workers ever free slots.
	//
	// SHUTDOWN ORDERING: this goroutine is the SOLE producer of
	// EnqueueSend (via DispatchRunningBudget) AND the SOLE caller of pe.Close(),
	// and it calls Close() only AFTER its own select loop has returned
	// (same goroutine, strictly sequential) — so a send-after-close race is
	// structurally impossible: no other goroutine ever writes to `pe` or
	// closes it. Do not add pe.Close() to StopIntake or any other
	// goroutine; StopIntake stays asynqSrv.Shutdown() (it only stops
	// takeover intake).
	sup.Go(func(lctx context.Context) error {
		t := time.NewTicker(50 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-lctx.Done():
				pe.Close() // stop pump workers after drain; sole closer, called post-loop
				return lctx.Err()
			case <-t.C:
				room := pe.Cap() - pe.Len()
				if room <= 0 {
					continue
				}
				if _, err := dispatcher.DispatchRunningBudget(lctx, room); err != nil {
					log.Printf("pump filler: %v", err)
				}
			}
		}
	})
	// Side-car ban-rate circuit breaker: auto-pauses campaigns over the
	// admin-configured threshold. Decoupled from the dispatch engine — it only
	// flips campaign state, which the dispatch loop above already honors. Ships
	// disabled-by-default (system_risk_config.circuit_breaker_enabled).
	breaker := riskbreaker.New(mgr.SystemPool())
	sup.Go(func(lctx context.Context) error {
		return breaker.Run(lctx)
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

	// Node is fully booted: mark ready so /readyz returns 200.
	srv.SetReady(true)

	// stop() drain sequence for zero-downtime rolling deploy:
	//   1. /readyz → 503 so k8s stops routing new traffic
	//   2. sleep PreStopDelay to let k8s remove the endpoint
	//   3. supervisor drains in-flight work, deregisters, closes store+logger
	//   4. metrics HTTP server last (so scrapes still work during drain)
	preStopDelay := cfg.PreStopDelay
	stop := func() {
		srv.SetReady(false)          // /readyz → 503: k8s stops routing new work
		time.Sleep(preStopDelay)     // give k8s time to remove the endpoint
		sup.Shutdown()               // intake → loops → sessions → store → flush
		sctx, c := context.WithTimeout(context.Background(), 5*time.Second)
		defer c()
		_ = srv.Shutdown(sctx)       // metrics http (last, so scrapes still work during shutdown)
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

// loadActiveCountries returns the list of active proxy countries. It first
// checks WADIST_COUNTRIES (comma-separated, e.g. "US,IN,BR"); if unset it
// queries distinct non-null country_code from proxy_pool. Falls back to an
// empty slice on any DB error so the WorkingSet active-warm loop is safely
// skipped rather than crashing startup.
func loadActiveCountries(ctx context.Context, mgr *store.Manager) []string {
	if v := os.Getenv("WADIST_COUNTRIES"); v != "" {
		var ccs []string
		for _, s := range strings.Split(v, ",") {
			if s = strings.TrimSpace(s); s != "" {
				ccs = append(ccs, s)
			}
		}
		return ccs
	}
	rows, err := mgr.Pool().Query(ctx,
		`SELECT DISTINCT country_code FROM proxy_pool WHERE country_code IS NOT NULL ORDER BY country_code`)
	if err != nil {
		log.Printf("warn: load active countries: %v — control-plane active-warm disabled", err)
		return nil
	}
	defer rows.Close()
	var ccs []string
	for rows.Next() {
		var cc string
		if err := rows.Scan(&cc); err == nil {
			ccs = append(ccs, cc)
		}
	}
	return ccs
}

// countryOf returns the country code bound to jid by consulting GetBoundProxy.
// Accounts with no bound proxy or an unresolvable country are seeded with cc=""
// (they land in the due:"" shard and will be skipped by the active-warm loop
// since "" is not in activeCountries; reactive warm:req still works for them).
func countryOf(ctx context.Context, mgr *store.Manager, jid string) string {
	pb, err := mgr.GetBoundProxy(ctx, jid)
	if err != nil {
		return ""
	}
	return pb.Country
}

// randQuota returns a random integer in [min, max]. If min >= max it returns min.
func randQuota(rng *rand.Rand, min, max int) int {
	if min >= max {
		return min
	}
	return min + rng.Intn(max-min+1)
}
