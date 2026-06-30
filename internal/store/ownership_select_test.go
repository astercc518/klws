// internal/store/ownership_select_test.go
package store

import "testing"

func TestNewOwnershipSelect(t *testing.T) {
	if got := backendName(&pgOwnership{}); got != "pg" {
		t.Fatalf("pg name=%s", got)
	}
	if got := backendName(&redisOwnership{}); got != "redis" {
		t.Fatalf("redis name=%s", got)
	}
	if got := backendName(&shadowOwnership{}); got != "shadow" {
		t.Fatalf("shadow name=%s", got)
	}
}
