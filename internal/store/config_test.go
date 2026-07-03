// internal/store/config_test.go
package store

import (
	"testing"
	"time"
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
	if c.ProxyBackend != "pg" {
		t.Fatalf("ProxyBackend default = %q; want pg", c.ProxyBackend)
	}
	if c.ProxyCooldown != 60*time.Second {
		t.Fatalf("ProxyCooldown default = %v; want 60s", c.ProxyCooldown)
	}
}

func TestConfig_withDefaults_lockConns(t *testing.T) {
	// Zero value should default to 300.
	c := Config{DSN: "postgres://x"}
	c.withDefaults()
	if c.MaxLockConns != 300 {
		t.Fatalf("MaxLockConns = %d, want 300", c.MaxLockConns)
	}

	// Explicit value must be preserved.
	c2 := Config{DSN: "postgres://x", MaxLockConns: 200}
	c2.withDefaults()
	if c2.MaxLockConns != 200 {
		t.Fatalf("explicit MaxLockConns overwritten: %d", c2.MaxLockConns)
	}
}
