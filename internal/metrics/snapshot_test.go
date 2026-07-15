package metrics

import (
	"testing"
	"time"
)

// pgPool + applyMigrations are provided by dbcollector_testsupport_test.go
// (shared testcontainers harness for this package).

func intPtr(v int) *int { return &v }

func TestSnapshotInsertAndReadBack(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	pool, ctx := pgPool(t)
	applyMigrations(t, ctx, pool)

	store := NewStore(pool)
	capturedAt := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	snap := Snapshot{
		CapturedAt:          capturedAt,
		AccountsTotal:       100,
		AccountsActive:      80,
		AccountsBanned:      5,
		AccountsQuarantined: 3,
		QueueBacklog:        42,
		Processed1h:         200,
		Delivered1h:         190,
		Failed1h:            10,
		AvgDeliveryMs:       intPtr(1500),
	}
	if err := store.Insert(ctx, snap); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	var (
		gotTenantID                                       *int64
		gotTotal, gotActive, gotBanned, gotQuarantined    int
		gotBacklog, gotProcessed, gotDelivered, gotFailed int
		gotAvgDeliveryMs                                  *int
		gotCapturedAt                                     time.Time
	)
	err := pool.QueryRow(ctx, `
SELECT captured_at, tenant_id, accounts_total, accounts_active, accounts_banned,
       accounts_quarantined, queue_backlog, processed_1h, delivered_1h, failed_1h, avg_delivery_ms
FROM metric_snapshots WHERE captured_at = $1`, capturedAt).
		Scan(&gotCapturedAt, &gotTenantID, &gotTotal, &gotActive, &gotBanned,
			&gotQuarantined, &gotBacklog, &gotProcessed, &gotDelivered, &gotFailed, &gotAvgDeliveryMs)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}

	if gotTenantID != nil {
		t.Fatalf("tenant_id: expected NULL (platform-level), got %v", *gotTenantID)
	}
	if gotTotal != 100 || gotActive != 80 || gotBanned != 5 || gotQuarantined != 3 {
		t.Fatalf("account counts mismatch: total=%d active=%d banned=%d quarantined=%d",
			gotTotal, gotActive, gotBanned, gotQuarantined)
	}
	if gotBacklog != 42 || gotProcessed != 200 || gotDelivered != 190 || gotFailed != 10 {
		t.Fatalf("throughput counts mismatch: backlog=%d processed=%d delivered=%d failed=%d",
			gotBacklog, gotProcessed, gotDelivered, gotFailed)
	}
	if gotAvgDeliveryMs == nil || *gotAvgDeliveryMs != 1500 {
		t.Fatalf("avg_delivery_ms mismatch: got %v", gotAvgDeliveryMs)
	}
	if !gotCapturedAt.Equal(capturedAt) {
		t.Fatalf("captured_at mismatch: got %v want %v", gotCapturedAt, capturedAt)
	}
}

func TestSnapshotInsertNullableAvgDeliveryMs(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	pool, ctx := pgPool(t)
	applyMigrations(t, ctx, pool)

	store := NewStore(pool)
	capturedAt := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	snap := Snapshot{
		CapturedAt:     capturedAt,
		AccountsTotal:  1,
		AccountsActive: 1,
		QueueBacklog:   0,
		AvgDeliveryMs:  nil, // no traffic in the sampling window
	}
	if err := store.Insert(ctx, snap); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	var gotAvgDeliveryMs *int
	if err := pool.QueryRow(ctx,
		`SELECT avg_delivery_ms FROM metric_snapshots WHERE captured_at = $1`, capturedAt).
		Scan(&gotAvgDeliveryMs); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if gotAvgDeliveryMs != nil {
		t.Fatalf("avg_delivery_ms: expected NULL, got %v", *gotAvgDeliveryMs)
	}
}

func TestSnapshotPruneDeletesOldKeepsNew(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	pool, ctx := pgPool(t)
	applyMigrations(t, ctx, pool)

	store := NewStore(pool)
	now := time.Date(2026, 7, 14, 0, 0, 0, 0, time.UTC)
	old := now.Add(-100 * 24 * time.Hour)
	recent := now.Add(-1 * time.Hour)

	for _, ts := range []time.Time{old, recent} {
		if err := store.Insert(ctx, Snapshot{CapturedAt: ts, AccountsTotal: 1, AccountsActive: 1}); err != nil {
			t.Fatalf("seed insert: %v", err)
		}
	}

	cutoff := now.Add(-90 * 24 * time.Hour)
	n, err := store.Prune(ctx, cutoff)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if n != 1 {
		t.Fatalf("Prune: expected 1 row deleted, got %d", n)
	}

	var remaining int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM metric_snapshots`).Scan(&remaining); err != nil {
		t.Fatalf("count: %v", err)
	}
	if remaining != 1 {
		t.Fatalf("expected 1 row remaining, got %d", remaining)
	}

	var gotCapturedAt time.Time
	if err := pool.QueryRow(ctx, `SELECT captured_at FROM metric_snapshots`).Scan(&gotCapturedAt); err != nil {
		t.Fatalf("read remaining: %v", err)
	}
	if !gotCapturedAt.Equal(recent) {
		t.Fatalf("expected surviving row to be the recent one %v, got %v", recent, gotCapturedAt)
	}
}

func TestSnapshotTrendDayBucketAggregation(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	pool, ctx := pgPool(t)
	applyMigrations(t, ctx, pool)

	store := NewStore(pool)

	// Day 1 (Asia/Shanghai): two samples, accounts_active = 10 and 20 → avg 15.
	day1a := time.Date(2026, 7, 10, 1, 0, 0, 0, time.UTC) // 09:00 CST
	day1b := time.Date(2026, 7, 10, 9, 0, 0, 0, time.UTC) // 17:00 CST
	// Day 2: two samples, accounts_active = 40 and 60 → avg 50.
	day2a := time.Date(2026, 7, 11, 1, 0, 0, 0, time.UTC)
	day2b := time.Date(2026, 7, 11, 9, 0, 0, 0, time.UTC)

	seeds := []struct {
		ts     time.Time
		active int
	}{
		{day1a, 10}, {day1b, 20},
		{day2a, 40}, {day2b, 60},
	}
	for _, sd := range seeds {
		if err := store.Insert(ctx, Snapshot{CapturedAt: sd.ts, AccountsTotal: 100, AccountsActive: sd.active}); err != nil {
			t.Fatalf("seed insert: %v", err)
		}
	}

	from := time.Date(2026, 7, 9, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC)
	points, err := store.Trend(ctx, "accounts_active", "day", from, to)
	if err != nil {
		t.Fatalf("Trend: %v", err)
	}
	if len(points) != 2 {
		t.Fatalf("expected 2 day buckets, got %d: %+v", len(points), points)
	}
	if points[0].Value != 15 {
		t.Fatalf("day1 avg: expected 15, got %v", points[0].Value)
	}
	if points[1].Value != 50 {
		t.Fatalf("day2 avg: expected 50, got %v", points[1].Value)
	}
}

func TestSnapshotTrendBucketUsesShanghaiTZ(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	pool, ctx := pgPool(t)
	applyMigrations(t, ctx, pool)

	store := NewStore(pool)

	// Two samples that share the SAME UTC calendar day (2026-07-10) but fall on
	// DIFFERENT Asia/Shanghai (+8) local days:
	//   15:00 UTC → 2026-07-10 23:00 CST → Shanghai day 07-10
	//   20:00 UTC → 2026-07-11 04:00 CST → Shanghai day 07-11
	// If the query bucketed by UTC (missing/wrong AT TIME ZONE), both land in a
	// single 07-10 bucket → len==1, avg==(70+30)/2==50, and this test fails.
	// Correct Asia/Shanghai bucketing yields two buckets with distinct averages.
	shanghaiDay10 := time.Date(2026, 7, 10, 15, 0, 0, 0, time.UTC) // CST 07-10 23:00
	shanghaiDay11 := time.Date(2026, 7, 10, 20, 0, 0, 0, time.UTC) // CST 07-11 04:00

	if err := store.Insert(ctx, Snapshot{CapturedAt: shanghaiDay10, AccountsTotal: 100, AccountsActive: 70}); err != nil {
		t.Fatalf("seed day10: %v", err)
	}
	if err := store.Insert(ctx, Snapshot{CapturedAt: shanghaiDay11, AccountsTotal: 100, AccountsActive: 30}); err != nil {
		t.Fatalf("seed day11: %v", err)
	}

	from := time.Date(2026, 7, 9, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC)
	points, err := store.Trend(ctx, "accounts_active", "day", from, to)
	if err != nil {
		t.Fatalf("Trend: %v", err)
	}
	if len(points) != 2 {
		t.Fatalf("expected 2 Shanghai-local day buckets (UTC bucketing would give 1), got %d: %+v", len(points), points)
	}
	// points are ORDER BY bucket ascending: 07-10 then 07-11.
	if points[0].Value != 70 {
		t.Fatalf("Shanghai 07-10 bucket: expected avg 70 (the 15:00 UTC sample), got %v", points[0].Value)
	}
	if points[1].Value != 30 {
		t.Fatalf("Shanghai 07-11 bucket: expected avg 30 (the 20:00 UTC sample), got %v", points[1].Value)
	}
}

func TestSnapshotTrendSkipsAllNullBucket(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	pool, ctx := pgPool(t)
	applyMigrations(t, ctx, pool)

	store := NewStore(pool)

	// Day 1: both samples fall in a silent sampling window -> avg_delivery_ms
	// NULL for every row, so avg(avg_delivery_ms) over the whole bucket is
	// SQL NULL. Day 2: samples carry a real avg_delivery_ms.
	day1a := time.Date(2026, 7, 10, 1, 0, 0, 0, time.UTC)
	day1b := time.Date(2026, 7, 10, 9, 0, 0, 0, time.UTC)
	day2a := time.Date(2026, 7, 11, 1, 0, 0, 0, time.UTC)
	day2b := time.Date(2026, 7, 11, 9, 0, 0, 0, time.UTC)

	if err := store.Insert(ctx, Snapshot{CapturedAt: day1a, AccountsTotal: 1, AvgDeliveryMs: nil}); err != nil {
		t.Fatalf("seed day1a: %v", err)
	}
	if err := store.Insert(ctx, Snapshot{CapturedAt: day1b, AccountsTotal: 1, AvgDeliveryMs: nil}); err != nil {
		t.Fatalf("seed day1b: %v", err)
	}
	if err := store.Insert(ctx, Snapshot{CapturedAt: day2a, AccountsTotal: 1, AvgDeliveryMs: intPtr(1000)}); err != nil {
		t.Fatalf("seed day2a: %v", err)
	}
	if err := store.Insert(ctx, Snapshot{CapturedAt: day2b, AccountsTotal: 1, AvgDeliveryMs: intPtr(2000)}); err != nil {
		t.Fatalf("seed day2b: %v", err)
	}

	from := time.Date(2026, 7, 9, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC)

	// Must not error (no "cannot scan NULL into *float64") and must skip the
	// all-NULL day-1 bucket entirely rather than emitting a zeroed point.
	points, err := store.Trend(ctx, "avg_delivery_ms", "day", from, to)
	if err != nil {
		t.Fatalf("Trend: %v", err)
	}
	if len(points) != 1 {
		t.Fatalf("expected 1 bucket (day1 NULL bucket skipped), got %d: %+v", len(points), points)
	}
	if points[0].Value != 1500 {
		t.Fatalf("day2 avg: expected 1500, got %v", points[0].Value)
	}
	wantDay := time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)
	if !points[0].Bucket.Equal(wantDay) {
		t.Fatalf("bucket: expected day2 (%v), got %v", wantDay, points[0].Bucket)
	}
}

func TestSnapshotTrendRejectsUnknownMetric(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	pool, ctx := pgPool(t)
	applyMigrations(t, ctx, pool)

	store := NewStore(pool)
	from := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 7, 31, 0, 0, 0, 0, time.UTC)

	if _, err := store.Trend(ctx, "accounts_total; DROP TABLE metric_snapshots;--", "day", from, to); err == nil {
		t.Fatal("expected error for unwhitelisted metric, got nil")
	}
	if _, err := store.Trend(ctx, "accounts_total", "month", from, to); err == nil {
		t.Fatal("expected error for unwhitelisted bucket, got nil")
	}

	// table must still exist and be queryable — the injection attempt must not have executed.
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM metric_snapshots`).Scan(&count); err != nil {
		t.Fatalf("metric_snapshots should still exist: %v", err)
	}
}
