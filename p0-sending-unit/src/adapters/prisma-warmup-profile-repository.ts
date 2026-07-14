import type { PrismaClient } from '@prisma/client';
import type { WarmupProfile } from '../domain/warmup-profile.js';
import type { WarmupProfileRepository } from '../ports/warmup-profile-repository.js';
import type { WarmupStage } from '../warmup/warmup-state.js';
import type { WarmupLane } from '../warmup/warmup-policy.js';

interface Row {
  accountId: string; lane: string; stage: string; warmupMessagesSent: number; repliesReceived: number;
  onlineSince: string | null; maturedAt: string | null; sentToday: number; sentTodayDate: string | null;
}

function toProfile(r: Row): WarmupProfile {
  return {
    accountId: r.accountId, lane: r.lane as WarmupLane, stage: r.stage as WarmupStage,
    warmupMessagesSent: r.warmupMessagesSent, repliesReceived: r.repliesReceived,
    onlineSince: r.onlineSince, maturedAt: r.maturedAt, sentToday: r.sentToday, sentTodayDate: r.sentTodayDate,
  };
}

export class PrismaWarmupProfileRepository implements WarmupProfileRepository {
  constructor(private prisma: PrismaClient) {}

  async save(p: WarmupProfile): Promise<void> {
    const data = {
      lane: p.lane, stage: p.stage, warmupMessagesSent: p.warmupMessagesSent, repliesReceived: p.repliesReceived,
      onlineSince: p.onlineSince, maturedAt: p.maturedAt, sentToday: p.sentToday, sentTodayDate: p.sentTodayDate,
    };
    await this.prisma.warmupProfile.upsert({
      where: { accountId: p.accountId },
      create: { accountId: p.accountId, ...data },
      update: data,
    });
  }

  async findByAccountId(accountId: string): Promise<WarmupProfile | null> {
    const r = await this.prisma.warmupProfile.findUnique({ where: { accountId } });
    return r ? toProfile(r as unknown as Row) : null;
  }

  async listByStage(stage: WarmupStage, limit: number): Promise<WarmupProfile[]> {
    const rows = await this.prisma.warmupProfile.findMany({ where: { stage }, take: limit });
    return rows.map((r) => toProfile(r as unknown as Row));
  }
}
