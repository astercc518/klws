package warmup

import (
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
