// internal/console/testsupport_test.go
package console

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	walog "github.com/acme/wadist/internal/log"
	"github.com/acme/wadist/internal/store"
)

// newTestManager starts a throwaway PG16, applies every migration, and returns
// a non-singleton store.Manager wired to it. The container is torn down on cleanup.
func newTestManager(t *testing.T) *store.Manager {
	t.Helper()
	ctx := context.Background()
	c, err := postgres.Run(ctx, "postgres:16",
		postgres.WithDatabase("console_test"),
		postgres.WithUsername("test"),
		postgres.WithPassword("test"),
		testcontainers.WithWaitStrategy(
			wait.ForAll(
				wait.ForListeningPort("5432/tcp"),
				wait.ForLog("database system is ready to accept connections").WithOccurrence(2),
			).WithStartupTimeout(60*time.Second)),
	)
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	t.Cleanup(func() { _ = c.Terminate(ctx) })
	dsn, err := c.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("dsn: %v", err)
	}

	logger, flush, err := walog.Production()
	if err != nil {
		t.Fatalf("logger: %v", err)
	}
	t.Cleanup(flush)

	appTenantDSN := buildAppTenantDSN(t, dsn)
	mgr, err := store.NewManager(ctx, store.Config{DSN: dsn, AppTenantDSN: appTenantDSN, NodeID: "console-test"}, logger)
	if err != nil {
		t.Fatalf("manager: %v", err)
	}
	t.Cleanup(mgr.Close)

	applyAllMigrations(t, ctx, mgr)
	return mgr
}

func applyAllMigrations(t *testing.T, ctx context.Context, mgr *store.Manager) {
	t.Helper()
	files, err := filepath.Glob("../../migrations/*.sql")
	if err != nil {
		t.Fatalf("glob migrations: %v", err)
	}
	sort.Strings(files)
	for _, f := range files {
		if strings.HasSuffix(f, ".down.sql") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		if _, err := mgr.SystemPool().Exec(ctx, string(b)); err != nil {
			t.Fatalf("apply %s: %v", f, err)
		}
	}
}

func ptrInt64(v int64) *int64 { return &v }

// csrfFor GETs path using client (which carries the cookie jar), reads the
// rendered hidden CSRF field, and returns the token value. The cookie jar
// automatically retains the Set-Cookie from the response for subsequent POSTs.
func csrfFor(t *testing.T, client *http.Client, ts *httptest.Server, path string) string {
	t.Helper()
	resp, err := client.Get(ts.URL + path)
	if err != nil {
		t.Fatalf("csrf GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	s := string(b)
	i := strings.Index(s, `name="csrf" value="`)
	if i < 0 {
		t.Fatalf("no csrf field in %s response", path)
	}
	rest := s[i+len(`name="csrf" value="`):]
	tok := rest[:strings.IndexByte(rest, '"')]
	if tok == "" {
		t.Fatalf("csrfFor: empty csrf token extracted from %s", path)
	}
	return tok
}

// buildAppTenantDSN takes the superuser DSN and returns a DSN for the app_tenant role.
// Mirrors the same helper in internal/store/rls_test.go.
func buildAppTenantDSN(t *testing.T, superDSN string) string {
	t.Helper()
	u, err := url.Parse(superDSN)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	u.User = url.UserPassword("app_tenant", "app_tenant_pw")
	return u.String()
}
