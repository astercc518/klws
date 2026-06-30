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
	cfg        Config
	container  *sqlstore.Container
	bizPool    *pgxpool.Pool
	lockPool   *pgxpool.Pool
	tenantPool *pgxpool.Pool // RLS-constrained role app_tenant (or bizPool fallback)
	systemPool *pgxpool.Pool // BYPASSRLS role app_system (or bizPool fallback)
	sqlDB      *sql.DB
	log        waLog.Logger
	ownership  Ownership // pluggable ownership backend; default: pgOwnership
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

// NewManager constructs a non-singleton Manager (its own pools). It exists for
// tests and multi-instance scenarios; production entrypoints MUST use Init (the
// singleton) to avoid opening duplicate connection pools.
func NewManager(ctx context.Context, cfg Config, logger waLog.Logger) (*Manager, error) {
	return newManager(ctx, cfg, logger)
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

	lockPoolCfg, err := pgxpool.ParseConfig(cfg.DSN)
	if err != nil {
		bizPool.Close()
		_ = sqlDB.Close()
		return nil, fmt.Errorf("parse lock pool config: %w", err)
	}
	lockPoolCfg.MaxConns = cfg.MaxLockConns
	lockPoolCfg.MinConns = 0
	// Use a very large lifetime so pgxpool never lifetime-reaps a pinned lock connection.
	// pgxpool treats MaxConnLifetime=0 as time.Now() (immediately expired), so we use
	// a 100-year sentinel instead of 0 to mean "effectively unlimited".
	lockPoolCfg.MaxConnLifetime = 100 * 365 * 24 * time.Hour

	lockPool, err := pgxpool.NewWithConfig(ctx, lockPoolCfg)
	if err != nil {
		bizPool.Close()
		_ = sqlDB.Close()
		return nil, fmt.Errorf("create lock pool: %w", err)
	}

	// RLS role pools: if DSN is provided build a dedicated pool, else fall back to bizPool.
	tenantPool := bizPool
	if cfg.AppTenantDSN != "" {
		tCfg, err := pgxpool.ParseConfig(cfg.AppTenantDSN)
		if err != nil {
			lockPool.Close()
			bizPool.Close()
			_ = sqlDB.Close()
			return nil, fmt.Errorf("parse tenant pool config: %w", err)
		}
		tCfg.MaxConns = cfg.MaxOpenConns
		tenantPool, err = pgxpool.NewWithConfig(ctx, tCfg)
		if err != nil {
			lockPool.Close()
			bizPool.Close()
			_ = sqlDB.Close()
			return nil, fmt.Errorf("create tenant pool: %w", err)
		}
	}

	systemPool := bizPool
	if cfg.AppSystemDSN != "" {
		sCfg, err := pgxpool.ParseConfig(cfg.AppSystemDSN)
		if err != nil {
			if tenantPool != bizPool {
				tenantPool.Close()
			}
			lockPool.Close()
			bizPool.Close()
			_ = sqlDB.Close()
			return nil, fmt.Errorf("parse system pool config: %w", err)
		}
		sCfg.MaxConns = cfg.MaxOpenConns
		systemPool, err = pgxpool.NewWithConfig(ctx, sCfg)
		if err != nil {
			if tenantPool != bizPool {
				tenantPool.Close()
			}
			lockPool.Close()
			bizPool.Close()
			_ = sqlDB.Close()
			return nil, fmt.Errorf("create system pool: %w", err)
		}
	}

	m := &Manager{
		cfg:        cfg,
		container:  container,
		bizPool:    bizPool,
		lockPool:   lockPool,
		tenantPool: tenantPool,
		systemPool: systemPool,
		sqlDB:      sqlDB,
		log:        logger,
	}
	m.ownership = &pgOwnership{m: m}
	return m, nil
}

func (m *Manager) BizPool() *pgxpool.Pool { return m.bizPool }

// Pool returns the business pgxpool. Alias used by the cmd/wadist wiring layer.
func (m *Manager) Pool() *pgxpool.Pool { return m.bizPool }

func (m *Manager) Close() {
	// Close RLS pools first if they are distinct from bizPool (pointer compare).
	if m.tenantPool != nil && m.tenantPool != m.bizPool {
		m.tenantPool.Close()
	}
	if m.systemPool != nil && m.systemPool != m.bizPool {
		m.systemPool.Close()
	}
	if m.bizPool != nil {
		m.bizPool.Close()
	}
	if m.lockPool != nil {
		m.lockPool.Close()
	}
	if m.sqlDB != nil {
		_ = m.sqlDB.Close()
	}
}
