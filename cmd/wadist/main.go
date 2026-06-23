// Command wadist is the distribution node entrypoint. M7 scope: dependency
// assembly, /metrics exposure, and the asynq send worker. Full graceful
// shutdown (Supervisor, session draining, SetLimit) lands in M8.
package main

import (
	"context"
	"errors"
	"log"
	"os/signal"
	"syscall"
	"time"

	"github.com/hibiken/asynq"
	"github.com/prometheus/client_golang/prometheus"
	goredis "github.com/redis/go-redis/v9"

	"github.com/acme/wadist/internal/billing"
	"github.com/acme/wadist/internal/config"
	"github.com/acme/wadist/internal/dispatch"
	walog "github.com/acme/wadist/internal/log"
	"github.com/acme/wadist/internal/metrics"
	"github.com/acme/wadist/internal/sendgate"
	"github.com/acme/wadist/internal/store"
)

// placeholderSender is a stub Sender until the whatsmeow adapter lands in M-send.
type placeholderSender struct{}

func (placeholderSender) Send(_ context.Context, _, _, _ string, _ *dispatch.MediaHandle) (string, error) {
	return "", errors.New("sender: not wired (pre-M-send)")
}

// placeholderUploader is a stub Uploader until the whatsmeow adapter lands in M-send.
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
	pool := mgr.Pool()

	reg := prometheus.NewRegistry()
	m := metrics.New(reg)
	reg.MustRegister(metrics.NewDBCollector(pool, 10*time.Second, nil))

	billingRepo := billing.NewRepo(pool)

	rdb := goredis.NewClient(&goredis.Options{Addr: cfg.RedisAddr})
	adm := sendgate.NewAdmission(rdb)
	gate := sendgate.NewSendGate(pool, adm, 3*time.Second)

	asynqClient := asynq.NewClient(asynq.RedisClientOpt{Addr: cfg.RedisAddr})
	enqueuer := dispatch.NewAsynqEnqueuer(asynqClient, "default", 3)

	// TODO(M8): wire the dispatch scheduling loop here (constructed now for metrics wiring).
	_ = dispatch.NewDispatcher(pool, billingRepo, enqueuer, priceFor, 3*time.Second).WithMetrics(m)

	worker := dispatch.NewSendWorker(pool, gate, billingRepo, placeholderSender{}, placeholderUploader{}).WithMetrics(m)

	asynqSrv := asynq.NewServer(
		asynq.RedisClientOpt{Addr: cfg.RedisAddr},
		asynq.Config{Concurrency: 32},
	)
	mux := asynq.NewServeMux()
	dispatch.RegisterSendHandler(mux, worker, func(_ context.Context, _ int64) (string, string, string, []byte, error) {
		return "", "", "", nil, errors.New("resolver: not wired (pre-M-send)")
	})
	go func() { _ = asynqSrv.Run(mux) }()

	srv := metrics.NewServer(cfg.MetricsAddr, reg)
	if err := srv.Start(); err != nil {
		asynqSrv.Shutdown()
		_ = asynqClient.Close()
		_ = rdb.Close()
		mgr.Close()
		flush()
		return nil, nil, err
	}

	stop := func() {
		sctx, c := context.WithTimeout(context.Background(), 5*time.Second)
		defer c()
		_ = srv.Shutdown(sctx)
		asynqSrv.Shutdown()
		_ = asynqClient.Close()
		_ = rdb.Close()
		mgr.Close()
		flush()
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

// ensure placeholders satisfy the dispatch interfaces at compile time.
var _ dispatch.Sender = placeholderSender{}
var _ dispatch.Uploader = placeholderUploader{}
