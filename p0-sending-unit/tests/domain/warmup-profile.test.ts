import { describe, it, expect } from 'vitest';
import { InMemoryWarmupProfileRepository } from '../../src/adapters/in-memory-warmup-profile-repository.js';
import type { WarmupProfile } from '../../src/domain/warmup-profile.js';

function p(accountId: string, stage: WarmupProfile['stage']): WarmupProfile {
  return { accountId, lane: 'STANDARD', stage, warmupMessagesSent: 0, repliesReceived: 0, onlineSince: null, maturedAt: null, sentToday: 0, sentTodayDate: null };
}

describe('WarmupProfile in-memory repo', () => {
  it('saves and finds by accountId', async () => {
    const repo = new InMemoryWarmupProfileRepository();
    const a = p('a1', 'WARMING');
    await repo.save(a);
    expect(await repo.findByAccountId('a1')).toEqual(a);
    expect(await repo.findByAccountId('nope')).toBeNull();
  });

  it('listByStage filters and respects limit', async () => {
    const repo = new InMemoryWarmupProfileRepository();
    await repo.save(p('a', 'WARMING'));
    await repo.save(p('b', 'WARMING'));
    await repo.save(p('c', 'MATURE'));
    expect((await repo.listByStage('WARMING', 10)).map((x) => x.accountId).sort()).toEqual(['a', 'b']);
    expect(await repo.listByStage('WARMING', 1)).toHaveLength(1);
  });
});
