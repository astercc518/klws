import { warmupTransition } from './warmup-state.js';
import { LANE_POLICIES, meetsPromotionCriteria, type WarmupLane } from './warmup-policy.js';
import type { WarmupProfile } from '../domain/warmup-profile.js';
import type { WarmupProfileRepository } from '../ports/warmup-profile-repository.js';

export type Clock = () => string;

export class WarmupService {
  constructor(private deps: { profiles: WarmupProfileRepository; clock: Clock }) {}

  async enroll(accountId: string, lane: WarmupLane): Promise<WarmupProfile> {
    const now = this.deps.clock();
    const profile: WarmupProfile = {
      accountId,
      lane,
      stage: warmupTransition('NEW', { type: 'ENROLL' }),
      warmupMessagesSent: 0,
      repliesReceived: 0,
      onlineSince: now,
      maturedAt: null,
      sentToday: 0,
      sentTodayDate: null,
    };
    await this.deps.profiles.save(profile);
    return profile;
  }

  async recordReply(accountId: string): Promise<void> {
    const p = await this.require(accountId);
    await this.deps.profiles.save({ ...p, repliesReceived: p.repliesReceived + 1 });
  }

  async evaluateAndPromote(accountId: string): Promise<boolean> {
    const p = await this.require(accountId);
    if (p.stage !== 'WARMING') return false;
    const policy = LANE_POLICIES[p.lane];
    const onlineHours = p.onlineSince === null
      ? 0
      : (Date.parse(this.deps.clock()) - Date.parse(p.onlineSince)) / 3600_000;
    const ok = meetsPromotionCriteria(
      { warmupMessagesSent: p.warmupMessagesSent, repliesReceived: p.repliesReceived, onlineHours },
      policy,
    );
    if (!ok) return false;
    await this.deps.profiles.save({
      ...p,
      stage: warmupTransition(p.stage, { type: 'PROMOTE' }),
      maturedAt: this.deps.clock(),
    });
    return true;
  }

  private async require(accountId: string): Promise<WarmupProfile> {
    const p = await this.deps.profiles.findByAccountId(accountId);
    if (!p) throw new Error(`Warmup profile not found: ${accountId}`);
    return p;
  }
}
