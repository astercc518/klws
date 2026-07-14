import { describe, it, expect } from 'vitest';
import { LANE_POLICIES, meetsPromotionCriteria, dailyCap } from '../../src/warmup/warmup-policy.js';

describe('warmup policy', () => {
  it('FAST lane has far lower promotion bar than STANDARD', () => {
    expect(LANE_POLICIES.FAST.minWarmupMessages).toBeLessThan(LANE_POLICIES.STANDARD.minWarmupMessages);
    expect(LANE_POLICIES.FAST.minOnlineHours).toBeLessThan(LANE_POLICIES.STANDARD.minOnlineHours);
  });

  it('meetsPromotionCriteria requires all three signals to clear the bar', () => {
    const p = LANE_POLICIES.STANDARD;
    const ok = { warmupMessagesSent: p.minWarmupMessages, repliesReceived: p.minReplies, onlineHours: p.minOnlineHours };
    expect(meetsPromotionCriteria(ok, p)).toBe(true);
    expect(meetsPromotionCriteria({ ...ok, warmupMessagesSent: p.minWarmupMessages - 1 }, p)).toBe(false);
    expect(meetsPromotionCriteria({ ...ok, repliesReceived: p.minReplies - 1 }, p)).toBe(false);
    expect(meetsPromotionCriteria({ ...ok, onlineHours: p.minOnlineHours - 1 }, p)).toBe(false);
  });

  it('dailyCap: NEW=0, WARMING=warmingCap, MATURE ramps from base to max', () => {
    const p = LANE_POLICIES.STANDARD;
    expect(dailyCap(p, 'NEW', 0)).toBe(0);
    expect(dailyCap(p, 'WARMING', 5)).toBe(p.warmingCap);
    expect(dailyCap(p, 'MATURE', 0)).toBe(p.matureBaseCap);
    expect(dailyCap(p, 'MATURE', 1000)).toBe(p.matureMaxCap);
    const mid = dailyCap(p, 'MATURE', 1);
    expect(mid).toBe(Math.min(p.matureBaseCap + p.matureRampStep, p.matureMaxCap));
  });
});
