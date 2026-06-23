package config

import "testing"

func TestLoad_RequiresDSN(t *testing.T) {
	t.Setenv("WADIST_POSTGRES_DSN", "")
	if _, err := Load(); err == nil {
		t.Fatal("expected error when WADIST_POSTGRES_DSN is unset")
	}
}

func TestLoad_DefaultsAndStoreMapping(t *testing.T) {
	t.Setenv("WADIST_POSTGRES_DSN", "postgres://x")
	t.Setenv("WADIST_REDIS_ADDR", "")
	t.Setenv("WADIST_NODE_ID", "node-7")
	t.Setenv("WADIST_MAX_OPEN_CONNS", "")
	t.Setenv("WADIST_MAX_LOCK_CONNS", "200")

	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.RedisAddr != "localhost:6379" {
		t.Fatalf("RedisAddr default = %q", c.RedisAddr)
	}
	if c.MaxOpenConns != 50 {
		t.Fatalf("MaxOpenConns default = %d, want 50", c.MaxOpenConns)
	}
	if c.MaxLockConns != 200 {
		t.Fatalf("MaxLockConns = %d, want 200", c.MaxLockConns)
	}

	sc := c.Store()
	if sc.DSN != "postgres://x" || sc.NodeID != "node-7" || sc.MaxLockConns != 200 {
		t.Fatalf("Store() mapping wrong: %+v", sc)
	}
}
