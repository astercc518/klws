// Command console is the wadist management web platform (admin/sales/customer).
package main

import (
	"context"
	"log"
	"os/signal"
	"syscall"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/acme/wadist/internal/config"
	"github.com/acme/wadist/internal/console"
	walog "github.com/acme/wadist/internal/log"
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

// run assembles the console server and returns it plus a stop func. It is the
// seam the smoke test drives.
func run(ctx context.Context) (*console.Server, func(), error) {
	baseCfg, err := config.Load() // requires WADIST_POSTGRES_DSN; provides Store()+RedisAddr
	if err != nil {
		return nil, nil, err
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

	srv, err := console.NewServer(webCfg, users, sessions, mgr)
	if err != nil {
		mgr.Close()
		flush()
		_ = rdb.Close()
		return nil, nil, err
	}
	if err := srv.Start(); err != nil {
		mgr.Close()
		flush()
		_ = rdb.Close()
		return nil, nil, err
	}

	stop := func() {
		srv.SetReady(false)
		sctx, c := context.WithTimeout(context.Background(), 5*time.Second)
		defer c()
		_ = srv.Shutdown(sctx)
		mgr.Close()
		flush()
		_ = rdb.Close()
	}
	return srv, stop, nil
}
