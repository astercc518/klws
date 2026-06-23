// internal/sendgate/admission_test.go
package sendgate

import (
	"context"
	"testing"
	"time"
)

func TestAdmission_DailyQuotaAndPacing(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	ctx := context.Background()
	adm := NewAdmission(redisClient(t))
	now := time.Date(2026, 1, 30, 12, 0, 0, 0, time.UTC)

	// quota=2, minGap=0 so pacing never blocks; 2 admits ok, 3rd hits daily_quota
	tk1, r1, err := adm.Admit(ctx, "acc1", 2, 0, now)
	if err != nil || tk1 == nil || r1 != "ok" {
		t.Fatalf("admit 1: tk=%v r=%q err=%v", tk1, r1, err)
	}
	if _, r2, _ := adm.Admit(ctx, "acc1", 2, 0, now.Add(time.Second)); r2 != "ok" {
		t.Fatalf("admit 2 reason = %q, want ok", r2)
	}
	if tk3, r3, _ := adm.Admit(ctx, "acc1", 2, 0, now.Add(2*time.Second)); tk3 != nil || r3 != "daily_quota" {
		t.Fatalf("admit 3 = (%v,%q), want (nil, daily_quota)", tk3, r3)
	}
}

func TestAdmission_PacingBlocks(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	ctx := context.Background()
	adm := NewAdmission(redisClient(t))
	now := time.Date(2026, 1, 30, 12, 0, 0, 0, time.UTC)

	if _, r, _ := adm.Admit(ctx, "acc2", 100, 5*time.Second, now); r != "ok" {
		t.Fatalf("first admit reason = %q, want ok", r)
	}
	// 1s later, min gap 5s → pacing blocks
	if tk, r, _ := adm.Admit(ctx, "acc2", 100, 5*time.Second, now.Add(time.Second)); tk != nil || r != "pacing" {
		t.Fatalf("paced admit = (%v,%q), want (nil, pacing)", tk, r)
	}
	// 6s later → allowed again
	if _, r, _ := adm.Admit(ctx, "acc2", 100, 5*time.Second, now.Add(6*time.Second)); r != "ok" {
		t.Fatalf("after gap reason = %q, want ok", r)
	}
}

func TestTicket_ReleaseRefundsQuota(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	ctx := context.Background()
	adm := NewAdmission(redisClient(t))
	now := time.Date(2026, 1, 30, 12, 0, 0, 0, time.UTC)

	tk, _, _ := adm.Admit(ctx, "acc3", 1, 0, now)
	if tk == nil {
		t.Fatal("expected ticket")
	}
	tk.Release(ctx) // give back the quota slot
	// quota=1 again available after release
	if tk2, r, _ := adm.Admit(ctx, "acc3", 1, 0, now.Add(time.Second)); tk2 == nil || r != "ok" {
		t.Fatalf("after release admit = (%v,%q), want ok", tk2, r)
	}
}
