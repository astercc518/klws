package warmup

import (
	"testing"
	"time"
)

func fixedClock(ts time.Time) Clock { return func() time.Time { return ts } }

func TestServiceEnrollAndPromote(t *testing.T) {
	pool, ctx := pgPool(t)
	st := NewStore(pool)
	base := time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC)
	svc := NewService(st, fixedClock(base))

	if err := svc.Enroll(ctx, "x@s.whatsapp.net", 1, LaneStandard); err != nil {
		t.Fatalf("enroll: %v", err)
	}
	// 信号不足,不该毕业。
	ok, err := svc.EvaluateAndPromote(ctx, "x@s.whatsapp.net")
	if err != nil || ok {
		t.Fatalf("premature promote ok=%v err=%v", ok, err)
	}

	// 灌满信号(直接改库),并让 clock 前进 40h 满足在线时长。
	p, _ := st.Get(ctx, "x@s.whatsapp.net")
	p.WarmupMessagesSent = 20
	p.RepliesReceived = 5
	_ = st.Save(ctx, p, base)
	svc2 := NewService(st, fixedClock(base.Add(40*time.Hour)))
	ok, err = svc2.EvaluateAndPromote(ctx, "x@s.whatsapp.net")
	if err != nil || !ok {
		t.Fatalf("should promote ok=%v err=%v", ok, err)
	}
	p2, _ := st.Get(ctx, "x@s.whatsapp.net")
	if p2.Stage != StageMature || p2.MaturedAt == nil {
		t.Fatalf("want MATURE+maturedAt got %s %v", p2.Stage, p2.MaturedAt)
	}
}

func TestServiceRecordReplyMissingIsNoop(t *testing.T) {
	pool, ctx := pgPool(t)
	svc := NewService(NewStore(pool), fixedClock(time.Now()))
	if err := svc.RecordReply(ctx, "notpool@s.whatsapp.net"); err != nil {
		t.Fatalf("record reply on missing must be noop, got %v", err)
	}
}

func TestServiceDemote(t *testing.T) {
	pool, ctx := pgPool(t)
	st := NewStore(pool)
	base := time.Now().UTC()
	svc := NewService(st, fixedClock(base))
	_ = svc.Enroll(ctx, "d@s.whatsapp.net", 1, LaneStandard)
	p, _ := st.Get(ctx, "d@s.whatsapp.net")
	p.Stage = StageMature
	_ = st.Save(ctx, p, base)
	if err := svc.Demote(ctx, "d@s.whatsapp.net", "device_removed"); err != nil {
		t.Fatalf("demote: %v", err)
	}
	p2, _ := st.Get(ctx, "d@s.whatsapp.net")
	if p2.Stage != StageWarming {
		t.Fatalf("want WARMING after demote got %s", p2.Stage)
	}
}
