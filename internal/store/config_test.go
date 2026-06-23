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
