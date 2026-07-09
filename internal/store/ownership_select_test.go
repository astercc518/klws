// internal/store/ownership_select_test.go
package store

import "testing"

// TestNewOwnershipSelect asserts that newOwnership always yields the redis
// backend, regardless of config — redis is the sole ownership backend now
// that pg advisory locks and the pg/redis shadow backend have been deleted.
func TestNewOwnershipSelect(t *testing.T) {
	m := &Manager{cfg: Config{}}
	if got := backendName(newOwnership(m)); got != "redis" {
		t.Fatalf("backendName(newOwnership(m)) = %s, want redis", got)
	}
}
