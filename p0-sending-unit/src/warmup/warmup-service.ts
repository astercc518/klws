import { warmupTransition } from './warmup-state.js';
import { LANE_POLICIES, meetsPromotionCriteria, dailyCap, type WarmupLane } from './warmup-policy.js';
import type { WarmupProfile } from '../domain/warmup-profile.js';
import type { WarmupProfileRepository } from '../ports/warmup-profile-repository.js';
import type { AccountRepository } from '../ports/account-repository.js';
import type { EvolutionClient } from '../evolution/evolution-client.js';

export type Clock = () => string;

export class WarmupService {
  constructor(
    private deps: {
      profiles: WarmupProfileRepository;
      accounts: AccountRepository;
      evolution: Pick<EvolutionClient, 'sendText'>;
      clock: Clock;
    },
  ) {}

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

  async pairAndWarm(batchSize: number): Promise<{ pairs: number; messagesSent: number }> {
    const warming = await this.deps.profiles.listByStage('WARMING', batchSize);
    const today = this.deps.clock().slice(0, 10);

    // 只留下连接态 ONLINE 且当日未超额的号
    const eligible: WarmupProfile[] = [];
    for (const p of warming) {
      const account = await this.deps.accounts.findById(p.accountId);
      if (!account || account.state !== 'ONLINE') continue;
      const sentToday = p.sentTodayDate === today ? p.sentToday : 0;
      if (sentToday >= dailyCap(LANE_POLICIES[p.lane], 'WARMING', 0)) continue;
      eligible.push(p);
    }

    let pairs = 0;
    let messagesSent = 0;
    for (let i = 0; i + 1 < eligible.length; i += 2) {
      const a = eligible[i]!;
      const b = eligible[i + 1]!;
      const accA = await this.deps.accounts.findById(a.accountId);
      const accB = await this.deps.accounts.findById(b.accountId);
      if (!accA || !accB) continue;
      await this.deps.evolution.sendText({ instanceName: accA.instanceName, to: accB.phoneNumber, text: 'oi, tudo bem?' });
      await this.deps.evolution.sendText({ instanceName: accB.instanceName, to: accA.phoneNumber, text: 'oi, tudo bem?' });
      await this.bumpSent(a, today);
      await this.bumpSent(b, today);
      pairs += 1;
      messagesSent += 2;
    }
    return { pairs, messagesSent };
  }

  private async bumpSent(p: WarmupProfile, today: string): Promise<void> {
    const sentToday = p.sentTodayDate === today ? p.sentToday : 0;
    await this.deps.profiles.save({
      ...p,
      warmupMessagesSent: p.warmupMessagesSent + 1,
      sentToday: sentToday + 1,
      sentTodayDate: today,
    });
  }
}
