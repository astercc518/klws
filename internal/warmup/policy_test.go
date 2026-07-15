package warmup

import "testing"

var stdPolicy = Policy{MinWarmupMessages: 20, MinReplies: 5, MinOnlineHours: 36,
	WarmingCap: 8, MatureBaseCap: 20, MatureMaxCap: 40, MatureRampStep: 5}

func TestMeetsPromotion(t *testing.T) {
	if !MeetsPromotion(20, 5, 36, stdPolicy) {
		t.Fatal("exact thresholds should promote")
	}
	if MeetsPromotion(19, 5, 36, stdPolicy) {
		t.Fatal("insufficient messages must not promote")
	}
	if MeetsPromotion(20, 4, 36, stdPolicy) {
		t.Fatal("insufficient replies must not promote")
	}
	if MeetsPromotion(20, 5, 35.9, stdPolicy) {
		t.Fatal("insufficient online hours must not promote")
	}
}

func TestDailyCap(t *testing.T) {
	if got := DailyCap(stdPolicy, StageNew, 0); got != 0 {
		t.Fatalf("NEW cap want 0 got %d", got)
	}
	if got := DailyCap(stdPolicy, StageWarming, 0); got != 8 {
		t.Fatalf("WARMING cap want 8 got %d", got)
	}
	if got := DailyCap(stdPolicy, StageMature, 0); got != 20 {
		t.Fatalf("MATURE day0 cap want 20 got %d", got)
	}
	if got := DailyCap(stdPolicy, StageMature, 3); got != 35 {
		t.Fatalf("MATURE day3 cap want 35 got %d", got)
	}
	if got := DailyCap(stdPolicy, StageMature, 100); got != 40 {
		t.Fatalf("MATURE cap must clamp to max 40 got %d", got)
	}
}
