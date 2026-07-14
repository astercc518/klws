import { describe, it, expect, beforeEach, afterAll } from 'vitest';
import { PrismaClient } from '@prisma/client';
import { PrismaWarmupProfileRepository } from '../../src/adapters/prisma-warmup-profile-repository.js';
import type { WarmupProfile } from '../../src/domain/warmup-profile.js';

const prisma = new PrismaClient();
const repo = new PrismaWarmupProfileRepository(prisma);
beforeEach(async () => { await prisma.warmupProfile.deleteMany(); });
afterAll(async () => { await prisma.$disconnect(); });

const base: WarmupProfile = {
  accountId: 'a1', lane: 'STANDARD', stage: 'WARMING', warmupMessagesSent: 3, repliesReceived: 1,
  onlineSince: '2026-07-14T00:00:00.000Z', maturedAt: null, sentToday: 2, sentTodayDate: '2026-07-14',
};

describe('PrismaWarmupProfileRepository', () => {
  it('saves and finds by accountId with full round-trip', async () => {
    await repo.save(base);
    expect(await repo.findByAccountId('a1')).toEqual(base);
    expect(await repo.findByAccountId('nope')).toBeNull();
  });
  it('upserts on repeated save (promotion)', async () => {
    await repo.save(base);
    await repo.save({ ...base, stage: 'MATURE', maturedAt: '2026-07-15T00:00:00.000Z' });
    const f = await repo.findByAccountId('a1');
    expect(f!.stage).toBe('MATURE');
    expect(f!.maturedAt).toBe('2026-07-15T00:00:00.000Z');
  });
  it('listByStage filters + limit', async () => {
    await repo.save({ ...base, accountId: 'a', stage: 'WARMING' });
    await repo.save({ ...base, accountId: 'b', stage: 'WARMING' });
    await repo.save({ ...base, accountId: 'c', stage: 'MATURE' });
    expect(await repo.listByStage('WARMING', 10)).toHaveLength(2);
    expect(await repo.listByStage('WARMING', 1)).toHaveLength(1);
  });
});
