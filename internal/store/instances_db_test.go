// internal/store/instances_db_test.go
package store

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestMigration0020_AccountInstances_Idempotent(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, testDSN(t))
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	defer pool.Close()

	applyMigrations(t, ctx, pool) // pass 1
	applyMigrations(t, ctx, pool) // pass 2 — must not error: proves replay-all idempotency

	var reg bool
	err = pool.QueryRow(ctx,
		`SELECT to_regclass('public.account_instances') IS NOT NULL`).Scan(&reg)
	if err != nil || !reg {
		t.Fatalf("account_instances not present after migrate-twice: reg=%v err=%v", reg, err)
	}
}

func TestInstanceResolver_RoundTrip(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	ctx := context.Background()
	m := newTestManager(t)
	if err := m.UpsertInstance(ctx, InstanceRow{
		InstanceName: "wa_1_default_1", TenantID: 1, EvoNode: "default", State: "created",
	}); err != nil {
		t.Fatal(err)
	}
	if err := m.BindInstanceJID(ctx, "wa_1_default_1", "123@s.whatsapp.net"); err != nil {
		t.Fatal(err)
	}
	jid, ok, err := m.JIDForInstance(ctx, "wa_1_default_1")
	if err != nil || !ok || jid != "123@s.whatsapp.net" {
		t.Fatalf("JIDForInstance = %q ok=%v err=%v", jid, ok, err)
	}
	inst, ok, err := m.InstanceForJID(ctx, "123@s.whatsapp.net")
	if err != nil || !ok || inst != "wa_1_default_1" {
		t.Fatalf("InstanceForJID = %q ok=%v err=%v", inst, ok, err)
	}
}

func TestBindInstanceJID_EmptyIsNull(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	ctx := context.Background()
	m := newTestManager(t)
	if err := m.UpsertInstance(ctx, InstanceRow{
		InstanceName: "wa_1_default_empty", TenantID: 1, EvoNode: "default", State: "created",
	}); err != nil {
		t.Fatal(err)
	}
	// Binding an empty jid must store NULL (not a literal ''), so the instance
	// stays unbound and JIDForInstance reports ok=false.
	if err := m.BindInstanceJID(ctx, "wa_1_default_empty", ""); err != nil {
		t.Fatal(err)
	}
	jid, ok, err := m.JIDForInstance(ctx, "wa_1_default_empty")
	if err != nil || ok || jid != "" {
		t.Fatalf("JIDForInstance after empty bind = %q ok=%v err=%v; want ok=false", jid, ok, err)
	}
}

func TestNodeCounts(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	ctx := context.Background()
	m := newTestManager(t)
	// seed: 2 on nodeA, 1 on nodeB
	mustUpsert := func(name, node string) {
		if err := m.UpsertInstance(ctx, InstanceRow{
			InstanceName: name, TenantID: 1, EvoNode: node, State: "created",
		}); err != nil {
			t.Fatal(err)
		}
	}
	mustUpsert("i1", "nodeA")
	mustUpsert("i2", "nodeA")
	mustUpsert("i3", "nodeB")

	counts, err := m.NodeCounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if counts["nodeA"] != 2 || counts["nodeB"] != 1 {
		t.Fatalf("counts=%v want nodeA:2 nodeB:1", counts)
	}
}

func TestNodeForInstance(t *testing.T) {
	ctx := context.Background()
	m := newTestManager(t)
	if err := m.UpsertInstance(ctx, InstanceRow{InstanceName: "i1", TenantID: 1, EvoNode: "nodeX", State: "created"}); err != nil {
		t.Fatal(err)
	}
	node, ok, err := m.NodeForInstance(ctx, "i1")
	if err != nil || !ok || node != "nodeX" {
		t.Fatalf("NodeForInstance = %q ok=%v err=%v", node, ok, err)
	}
	if _, ok, _ := m.NodeForInstance(ctx, "ghost"); ok {
		t.Fatal("unknown instance must be ok=false")
	}
}

func TestNodeCounts_EmptyIsNonNil(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	counts, err := newTestManager(t).NodeCounts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if counts == nil {
		t.Fatal("empty table must return non-nil empty map")
	}
}
