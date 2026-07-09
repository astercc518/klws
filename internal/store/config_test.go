// internal/store/config_test.go
package store

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestConfig_withDefaults(t *testing.T) {
	c := Config{DSN: "postgres://x"}
	c.withDefaults()
	if c.MaxOpenConns != 50 {
		t.Fatalf("MaxOpenConns = %d, want 50", c.MaxOpenConns)
	}
	if c.MinIdleConns != 5 {
		t.Fatalf("MinIdleConns = %d, want 5", c.MinIdleConns)
	}
	if c.ConnMaxLifetime != 30*time.Minute {
		t.Fatalf("ConnMaxLifetime = %v, want 30m", c.ConnMaxLifetime)
	}
	if c.ConnMaxIdleTime != 5*time.Minute {
		t.Fatalf("ConnMaxIdleTime = %v, want 5m", c.ConnMaxIdleTime)
	}
}

func TestConfig_withDefaults_keepsExplicit(t *testing.T) {
	c := Config{DSN: "x", MaxOpenConns: 80}
	c.withDefaults()
	if c.MaxOpenConns != 80 {
		t.Fatalf("explicit MaxOpenConns overwritten: %d", c.MaxOpenConns)
	}
}

func TestConfig_ProxyDefaults(t *testing.T) {
	var c Config
	c.withDefaults()
	if c.ProxyCooldown != 60*time.Second {
		t.Fatalf("ProxyCooldown default = %v; want 60s", c.ProxyCooldown)
	}
}

func TestConfigValidate_PgbouncerOK(t *testing.T) {
	c := Config{PoolMode: "pgbouncer"}
	c.withDefaults()
	if err := c.validate(); err != nil {
		t.Fatalf("pgbouncer should validate; got %v", err)
	}
}

func TestConfigValidate_CapsMaxConns(t *testing.T) {
	c := Config{PoolMode: "pgbouncer", MaxOpenConns: 500}
	c.withDefaults()
	if err := c.validate(); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if c.MaxOpenConns != 200 {
		t.Fatalf("MaxOpenConns=%d; want capped to 200", c.MaxOpenConns)
	}
}

func TestApplyQueryMode(t *testing.T) {
	base := func() *pgxpool.Config {
		c, err := pgxpool.ParseConfig("postgres://u:p@localhost:5432/db")
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	// direct: leave pgx default untouched (QueryExecModeCacheStatement == 0 value)
	d := base()
	applyQueryMode(d, "direct", "simple")
	if d.ConnConfig.DefaultQueryExecMode != pgx.QueryExecModeCacheStatement {
		t.Fatalf("direct changed exec mode to %v; want default CacheStatement", d.ConnConfig.DefaultQueryExecMode)
	}
	// pgbouncer + exec
	e := base()
	applyQueryMode(e, "pgbouncer", "exec")
	if e.ConnConfig.DefaultQueryExecMode != pgx.QueryExecModeExec {
		t.Fatalf("pgbouncer+exec got %v; want Exec", e.ConnConfig.DefaultQueryExecMode)
	}
	// pgbouncer + simple
	s := base()
	applyQueryMode(s, "pgbouncer", "simple")
	if s.ConnConfig.DefaultQueryExecMode != pgx.QueryExecModeSimpleProtocol {
		t.Fatalf("pgbouncer+simple got %v; want SimpleProtocol", s.ConnConfig.DefaultQueryExecMode)
	}
}

func TestConfigDefaults_DirectUnchanged(t *testing.T) {
	c := Config{}
	c.withDefaults()
	if c.PoolMode != "direct" || c.QueryMode != "exec" {
		t.Fatalf("defaults PoolMode=%q QueryMode=%q; want direct/exec", c.PoolMode, c.QueryMode)
	}
	if err := c.validate(); err != nil {
		t.Fatalf("direct mode must validate; got %v", err)
	}
}
