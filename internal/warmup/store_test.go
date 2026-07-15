package warmup

import (
	"sync"
	"testing"
	"time"
)

func TestStoreEnrollGetSave(t *testing.T) {
	pool, ctx := pgPool(t)
	st := NewStore(pool)
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)

	if err := st.EnrollIfAbsent(ctx, "111@s.whatsapp.net", 1, LaneStandard, now); err != nil {
		t.Fatalf("enroll: %v", err)
	}
	// 幂等:二次 enroll 不报错、不重置。
	if err := st.EnrollIfAbsent(ctx, "111@s.whatsapp.net", 1, LaneFast, now.Add(time.Hour)); err != nil {
		t.Fatalf("re-enroll: %v", err)
	}
	p, err := st.Get(ctx, "111@s.whatsapp.net")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if p.Stage != StageWarming || p.Lane != LaneStandard {
		t.Fatalf("want WARMING/STANDARD got %s/%s", p.Stage, p.Lane)
	}
	if p.OnlineSince == nil || !p.OnlineSince.Equal(now) {
		t.Fatalf("online_since not preserved: %v", p.OnlineSince)
	}

	p.WarmupMessagesSent = 20
	p.RepliesReceived = 5
	p.Stage = StageMature
	if err := st.Save(ctx, p, now); err != nil {
		t.Fatalf("save: %v", err)
	}
	p2, _ := st.Get(ctx, "111@s.whatsapp.net")
	if p2.WarmupMessagesSent != 20 || p2.Stage != StageMature {
		t.Fatalf("save not persisted: %+v", p2)
	}
}

func TestStoreListByStageAndPolicy(t *testing.T) {
	pool, ctx := pgPool(t)
	st := NewStore(pool)
	now := time.Now().UTC()
	_ = st.EnrollIfAbsent(ctx, "a@s.whatsapp.net", 1, LaneStandard, now)
	_ = st.EnrollIfAbsent(ctx, "b@s.whatsapp.net", 1, LaneStandard, now)
	list, err := st.ListByStage(ctx, StageWarming, 10)
	if err != nil || len(list) != 2 {
		t.Fatalf("list warming want 2 got %d err=%v", len(list), err)
	}
	pol, err := st.PolicyFor(ctx, LaneStandard)
	if err != nil || pol.MinWarmupMessages != 20 {
		t.Fatalf("policy STANDARD want min 20 got %+v err=%v", pol, err)
	}
}

func TestStoreGetNotFound(t *testing.T) {
	pool, ctx := pgPool(t)
	st := NewStore(pool)
	if _, err := st.Get(ctx, "missing@s.whatsapp.net"); err != ErrNotFound {
		t.Fatalf("want ErrNotFound got %v", err)
	}
}

// TestStoreIncRepliesConcurrent pins the fix for the cross-process
// lost-update race: 20 concurrent IncReplies calls against the same row must
// all land (atomic SQL "replies_received = replies_received + 1"), unlike
// the old Get-mutate-Save pattern where concurrent stale snapshots would
// clobber each other and lose updates.
func TestStoreIncRepliesConcurrent(t *testing.T) {
	pool, ctx := pgPool(t)
	st := NewStore(pool)
	now := time.Now().UTC()
	jid := "concurrent@s.whatsapp.net"
	if err := st.EnrollIfAbsent(ctx, jid, 1, LaneStandard, now); err != nil {
		t.Fatalf("enroll: %v", err)
	}

	const n = 20
	var wg sync.WaitGroup
	errCh := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errCh <- st.IncReplies(ctx, jid)
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatalf("IncReplies: %v", err)
		}
	}

	p, err := st.Get(ctx, jid)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if p.RepliesReceived != n {
		t.Fatalf("want RepliesReceived=%d got %d (lost update)", n, p.RepliesReceived)
	}
}

// TestStoreBumpAndIncRepliesConcurrentDontClobber interleaves concurrent
// BumpWarmupSent (writer: worker tick) and IncReplies (writer: webhook
// console) against the SAME row. Each targeted UPDATE only touches its own
// columns, so neither writer can erase the other's counter — the bug the
// old full-row Save(Get-then-clobber) pattern had (e.g. bumpSent from a
// stale snapshot reverting a concurrent RecordReply's replies_received).
func TestStoreBumpAndIncRepliesConcurrentDontClobber(t *testing.T) {
	pool, ctx := pgPool(t)
	st := NewStore(pool)
	now := time.Now().UTC()
	jid := "interleave@s.whatsapp.net"
	if err := st.EnrollIfAbsent(ctx, jid, 1, LaneStandard, now); err != nil {
		t.Fatalf("enroll: %v", err)
	}
	today := now.Truncate(24 * time.Hour)

	const n = 15
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_ = st.IncReplies(ctx, jid)
		}()
		go func() {
			defer wg.Done()
			_ = st.BumpWarmupSent(ctx, jid, today)
		}()
	}
	wg.Wait()

	p, err := st.Get(ctx, jid)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if p.RepliesReceived != n {
		t.Fatalf("want RepliesReceived=%d got %d (lost update across writers)", n, p.RepliesReceived)
	}
	if p.WarmupMessagesSent != n || p.WarmupSentToday != n {
		t.Fatalf("want WarmupMessagesSent/SentToday=%d/%d got %d/%d",
			n, n, p.WarmupMessagesSent, p.WarmupSentToday)
	}
}

// TestStoreBumpWarmupSentDayReset pins BumpWarmupSent's day-rollover logic:
// same-day bumps accumulate warmup_sent_today, but a bump on a new date
// resets warmup_sent_today to 1 while warmup_messages_sent keeps
// accumulating across days.
func TestStoreBumpWarmupSentDayReset(t *testing.T) {
	pool, ctx := pgPool(t)
	st := NewStore(pool)
	now := time.Now().UTC()
	jid := "dayreset@s.whatsapp.net"
	if err := st.EnrollIfAbsent(ctx, jid, 1, LaneStandard, now); err != nil {
		t.Fatalf("enroll: %v", err)
	}

	day1 := time.Date(2026, 7, 14, 0, 0, 0, 0, time.UTC)
	if err := st.BumpWarmupSent(ctx, jid, day1); err != nil {
		t.Fatalf("bump day1: %v", err)
	}
	if err := st.BumpWarmupSent(ctx, jid, day1); err != nil {
		t.Fatalf("bump day1 #2: %v", err)
	}
	p, _ := st.Get(ctx, jid)
	if p.WarmupSentToday != 2 || p.WarmupMessagesSent != 2 {
		t.Fatalf("want 2/2 after two same-day bumps got today=%d total=%d", p.WarmupSentToday, p.WarmupMessagesSent)
	}

	day2 := day1.Add(24 * time.Hour)
	if err := st.BumpWarmupSent(ctx, jid, day2); err != nil {
		t.Fatalf("bump day2: %v", err)
	}
	p2, _ := st.Get(ctx, jid)
	if p2.WarmupSentToday != 1 {
		t.Fatalf("want warmup_sent_today reset to 1 on new day, got %d", p2.WarmupSentToday)
	}
	if p2.WarmupMessagesSent != 3 {
		t.Fatalf("want warmup_messages_sent to keep accumulating across days, got %d", p2.WarmupMessagesSent)
	}
	if p2.WarmupSentDate == nil || !p2.WarmupSentDate.Equal(day2) {
		t.Fatalf("want warmup_sent_date=day2 got %v", p2.WarmupSentDate)
	}
}
