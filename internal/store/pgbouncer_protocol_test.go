// internal/store/pgbouncer_protocol_test.go
package store

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	waLog "go.mau.fi/whatsmeow/util/log"
)

// newPgbouncerTestManager builds a schema-applied Manager configured for
// PoolMode=="pgbouncer" with the given pgx QueryMode ("simple" or "exec").
// Ownership is unconditionally redis now, so this mirrors
// newManagerWithSchemaProxyRedis's redis testcontainer wiring.
func newPgbouncerTestManager(t *testing.T, queryMode string) (*Manager, context.Context) {
	t.Helper()
	ctx := context.Background()
	dsn := testDSN(t)

	migPool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("migration pool: %v", err)
	}
	applyMigrations(t, ctx, migPool)
	migPool.Close()

	rdb := newTestRedis(t)
	m, err := newManager(ctx, Config{
		DSN:       dsn,
		PoolMode:  "pgbouncer",
		QueryMode: queryMode,
		Redis:     rdb,
		BadgerDir: t.TempDir(),
	}, waLog.Noop)
	if err != nil {
		t.Fatalf("manager: %v", err)
	}
	t.Cleanup(m.Close)
	return m, ctx
}

// runProtocolSmoke builds a pgbouncer-mode Manager with the given query mode
// and exercises a representative set of high-risk queries (enum cast via
// ban_status='active'/'logged_out', parameterized last_connected_at update) to
// prove they succeed under the simple/exec protocol — which is what
// PgBouncer transaction mode requires. Simple protocol works against plain
// Postgres too, so no real PgBouncer is needed here: if a query has an
// implicit-cast/unnamed-prepared-statement problem, running it under
// QueryExecModeSimpleProtocol locally reproduces the failure.
func runProtocolSmoke(t *testing.T, queryMode string) {
	t.Helper()
	if testing.Short() {
		t.Skip("integration")
	}
	m, ctx := newPgbouncerTestManager(t, queryMode)

	// enum-cast + parameterized select (SendGate-style load: ban_status='active').
	seedAccountDevice(t, ctx, m, "jid-proto")
	if _, err := m.ListActiveAccounts(ctx); err != nil {
		t.Fatalf("[%s] ListActiveAccounts: %v", queryMode, err)
	}

	// parameterized UPDATE (last_connected_at claim touch).
	if err := m.ClaimAccount(ctx, "jid-proto"); err != nil {
		t.Fatalf("[%s] ClaimAccount: %v", queryMode, err)
	}

	// enum-write UPDATE (ban_status='logged_out').
	if err := m.MarkAccountLoggedOut(ctx, "jid-proto"); err != nil {
		t.Fatalf("[%s] MarkAccountLoggedOut: %v", queryMode, err)
	}

	jid := "jid-proto"

	// High-risk parameterized ::type casts — these are what can break under the
	// simple protocol (text-encoded params + server-side cast), unlike the literal
	// enum comparison above. Mirror the exact patterns used in production
	// (sendgate quarantine ::interval, governor ::interval, parameterized enum cast).
	if _, err := m.BizPool().Exec(ctx,
		`UPDATE account_devices SET quarantined_until = now() + $2::interval WHERE account_jid = $1`,
		jid, "3600 seconds"); err != nil {
		t.Fatalf("[%s] ::interval param cast (quarantine): %v", queryMode, err)
	}
	if _, err := m.BizPool().Exec(ctx,
		`UPDATE account_devices SET ban_status = $2::ban_status_t WHERE account_jid = $1`,
		jid, "flagged"); err != nil {
		t.Fatalf("[%s] ::ban_status_t param cast: %v", queryMode, err)
	}
	var cnt int
	if err := m.BizPool().QueryRow(ctx,
		`SELECT count(*) FROM account_devices WHERE updated_at >= now() - $1::interval`,
		"900 seconds").Scan(&cnt); err != nil {
		t.Fatalf("[%s] ::interval param cast (governor-style select): %v", queryMode, err)
	}
}

func TestPgbouncerProtocol_Simple(t *testing.T) { runProtocolSmoke(t, "simple") }
func TestPgbouncerProtocol_Exec(t *testing.T)   { runProtocolSmoke(t, "exec") }
