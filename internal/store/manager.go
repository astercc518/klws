// internal/store/manager.go
package store

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // 注册 "pgx" database/sql 驱动
	"github.com/jackc/pgx/v5/pgxpool"

	"go.mau.fi/whatsmeow/store/sqlstore"
	waLog "go.mau.fi/whatsmeow/util/log"
)

type Manager struct {
	cfg       Config
	container *sqlstore.Container
	bizPool   *pgxpool.Pool
	sqlDB     *sql.DB
	log       waLog.Logger
}

var (
	mgr     *Manager
	mgrOnce sync.Once
	mgrErr  error
)

// Init 幂等初始化全局单例。
func Init(ctx context.Context, cfg Config, logger waLog.Logger) (*Manager, error) {
	mgrOnce.Do(func() {
		mgr, mgrErr = newManager(ctx, cfg, logger)
	})
	return mgr, mgrErr
}

// newManager 构造一个独立 Manager(不走单例),供测试与多实例场景。
func newManager(ctx context.Context, cfg Config, logger waLog.Logger) (*Manager, error) {
	cfg.withDefaults()

	sqlDB, err := sql.Open("pgx", cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("open whatsmeow sql.DB: %w", err)
	}
	sqlDB.SetMaxOpenConns(int(cfg.MaxOpenConns))
	sqlDB.SetMaxIdleConns(int(cfg.MinIdleConns))
	sqlDB.SetConnMaxLifetime(cfg.ConnMaxLifetime)
	sqlDB.SetConnMaxIdleTime(cfg.ConnMaxIdleTime)

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := sqlDB.PingContext(pingCtx); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("ping whatsmeow db: %w", err)
	}

	container := sqlstore.NewWithDB(sqlDB, "postgres", logger)
	upgradeCtx, upgradeCancel := context.WithTimeout(ctx, 30*time.Second)
	defer upgradeCancel()
	if err := container.Upgrade(upgradeCtx); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("upgrade whatsmeow schema: %w", err)
	}

	poolCfg, err := pgxpool.ParseConfig(cfg.DSN)
	if err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("parse biz pool config: %w", err)
	}
	poolCfg.MaxConns = cfg.MaxOpenConns
	poolCfg.MinConns = cfg.MinIdleConns
	poolCfg.MaxConnLifetime = cfg.ConnMaxLifetime
	poolCfg.MaxConnIdleTime = cfg.ConnMaxIdleTime
	poolCfg.HealthCheckPeriod = 30 * time.Second

	bizPool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("create biz pool: %w", err)
	}

	return &Manager{cfg: cfg, container: container, bizPool: bizPool, sqlDB: sqlDB, log: logger}, nil
}

func (m *Manager) BizPool() *pgxpool.Pool { return m.bizPool }

func (m *Manager) Close() {
	if m.bizPool != nil {
		m.bizPool.Close()
	}
	if m.sqlDB != nil {
		_ = m.sqlDB.Close()
	}
}
