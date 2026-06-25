// cmd/console/rls_guard_test.go
package main

import (
	"context"
	"strings"
	"testing"
)

// run must refuse to start without the RLS role DSNs, so customer tenant
// isolation can never silently degrade to the superuser pool.
func TestRunFailsClosedWithoutRLSDSNs(t *testing.T) {
	t.Setenv("WADIST_POSTGRES_DSN", "postgres://app:app@127.0.0.1:5432/wadist?sslmode=disable")
	t.Setenv("WADIST_CONSOLE_SESSION_KEY", "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	t.Setenv("WADIST_APP_TENANT_DSN", "") // explicitly unset
	t.Setenv("WADIST_APP_SYSTEM_DSN", "")

	_, _, err := run(context.Background())
	if err == nil {
		t.Fatal("run must fail closed when RLS DSNs are unset")
	}
	if !strings.Contains(err.Error(), "WADIST_APP_TENANT_DSN") {
		t.Fatalf("error should name the missing var, got: %v", err)
	}
}
