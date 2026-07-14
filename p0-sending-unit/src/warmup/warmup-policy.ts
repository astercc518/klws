import type { WarmupStage } from './warmup-state.js';

export type WarmupLane = 'FAST' | 'STANDARD';

export interface WarmupPolicy {
  minWarmupMessages: number;
  minReplies: number;
  minOnlineHours: number;
  warmingCap: number;
  matureBaseCap: number;
  matureMaxCap: number;
  matureRampStep: number;
}

// 默认值均为可调参数（真机 N 值出来后在此调整）。
export const LANE_POLICIES: Record<WarmupLane, WarmupPolicy> = {
  // 炮灰道：几乎不养，补资料+短时在线即毕业，冷发上量、死得快但便宜。
  FAST: {
    minWarmupMessages: 2,
    minReplies: 0,
    minOnlineHours: 2,
    warmingCap: 5,
    matureBaseCap: 15,
    matureMaxCap: 20,
    matureRampStep: 5,
  },
  // 标准道：养到双向对话可信，目标毕业后爬到 40。
  STANDARD: {
    minWarmupMessages: 20,
    minReplies: 5,
    minOnlineHours: 36,
    warmingCap: 8,
    matureBaseCap: 20,
    matureMaxCap: 40,
    matureRampStep: 5,
  },
};

export function meetsPromotionCriteria(
  signals: { warmupMessagesSent: number; repliesReceived: number; onlineHours: number },
  policy: WarmupPolicy,
): boolean {
  return (
    signals.warmupMessagesSent >= policy.minWarmupMessages &&
    signals.repliesReceived >= policy.minReplies &&
    signals.onlineHours >= policy.minOnlineHours
  );
}

export function dailyCap(policy: WarmupPolicy, stage: WarmupStage, matureDays: number): number {
  if (stage === 'NEW') return 0;
  if (stage === 'WARMING') return policy.warmingCap;
  return Math.min(policy.matureBaseCap + matureDays * policy.matureRampStep, policy.matureMaxCap);
}
