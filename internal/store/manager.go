// internal/store/manager.go
package store

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib" // 注册 "pgx" database/sql 驱动

	wlog "github.com/acme/wadist/internal/log"
)

type Manager struct {
	cfg        Config
	bizPool    *pgxpool.Pool
	tenantPool *pgxpool.Pool // RLS-constrained role app_tenant (or bizPool fallback)
	systemPool *pgxpool.Pool // BYPASSRLS role app_system (or bizPool fallback)
	sqlDB      *sql.DB
	log        wlog.Logger
	ownership  Ownership            // account ownership backend: redis lease w/ fence tokens
	proxyAlloc *redisProxyAllocator // redis ZSET cooldown allocator; the sole proxy allocation backend
}

var (
	mgr     *Manager
	mgrOnce sync.Once
	mgrErr  error
)

// Init 幂等初始化全局单例。
func Init(ctx context.Context, cfg Config, logger wlog.Logger) (*Manager, error) {
	mgrOnce.Do(func() {
		mgr, mgrErr = newManager(ctx, cfg, logger)
	})
	return mgr, mgrErr
}

// NewManager constructs a non-singleton Manager (its own pools). It exists for
// tests and multi-instance scenarios; production entrypoints MUST use Init (the
// singleton) to avoid opening duplicate connection pools.
func NewManager(ctx context.Context, cfg Config, logger wlog.Logger) (*Manager, error) {
	return newManager(ctx, cfg, logger)
}

// newManager 构造一个独立 Manager(不走单例),供测试与多实例场景。
func newManager(ctx context.Context, cfg Config, logger wlog.Logger) (*Manager, error) {
	cfg.withDefaults()
	if err := cfg.validate(); err != nil {
		return nil, err
	}

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

	// sqlDB is opened for business tables only; the former whatsmeow/badger
	// session store has been removed (Evolution owns the WhatsApp data plane).

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

	// RLS role pools: if DSN is provided build a dedicated pool, else fall back to bizPool.
	tenantPool := bizPool
	if cfg.AppTenantDSN != "" {
		tCfg, err := pgxpool.ParseConfig(cfg.AppTenantDSN)
		if err != nil {
			bizPool.Close()
			_ = sqlDB.Close()
			return nil, fmt.Errorf("parse tenant pool config: %w", err)
		}
		tCfg.MaxConns = cfg.MaxOpenConns
		tenantPool, err = pgxpool.NewWithConfig(ctx, tCfg)
		if err != nil {
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
			bizPool.Close()
			_ = sqlDB.Close()
			return nil, fmt.Errorf("create system pool: %w", err)
		}
	}

	m := &Manager{
		cfg:        cfg,
		bizPool:    bizPool,
		tenantPool: tenantPool,
		systemPool: systemPool,
		sqlDB:      sqlDB,
		log:        logger,
	}
	m.ownership = newOwnership(m)

	if cfg.Redis == nil {
		m.Close()
		return nil, fmt.Errorf("store: redis is required (proxy allocator)")
	}
	m.proxyAlloc = newRedisProxyAllocator(cfg.Redis, cfg.ProxyCooldown)
	rctx, rcancel := context.WithTimeout(ctx, 10*time.Second)
	_, err = m.proxyAlloc.rebuildFromPG(rctx, m.bizPool, time.Now().UnixMilli())
	rcancel()
	if err != nil {
		m.Close()
		return nil, fmt.Errorf("rebuild redis proxy index: %w", err)
	}
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
	if m.sqlDB != nil {
		_ = m.sqlDB.Close()
	}
}
