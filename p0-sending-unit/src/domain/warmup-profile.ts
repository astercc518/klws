import type { WarmupStage } from '../warmup/warmup-state.js';
import type { WarmupLane } from '../warmup/warmup-policy.js';

export interface WarmupProfile {
  accountId: string;
  lane: WarmupLane;
  stage: WarmupStage;
  warmupMessagesSent: number;
  repliesReceived: number;
  onlineSince: string | null;
  maturedAt: string | null;
  sentToday: number;
  sentTodayDate: string | null;
}
