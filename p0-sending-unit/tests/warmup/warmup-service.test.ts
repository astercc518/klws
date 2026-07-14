import { describe, it, expect } from 'vitest';
import { WarmupService } from '../../src/warmup/warmup-service.js';
import { InMemoryWarmupProfileRepository } from '../../src/adapters/in-memory-warmup-profile-repository.js';
import { LANE_POLICIES } from '../../src/warmup/warmup-policy.js';

function fixedClock(iso: string) {
  return () => iso;
}

describe('WarmupService', () => {
  it('enroll creates a WARMING profile with onlineSince and lane', async () => {
    const profiles = new InMemoryWarmupProfileRepository();
    const svc = new WarmupService({ profiles, clock: fixedClock('2026-07-14T00:00:00.000Z') });
    const p = await svc.enroll('a1', 'STANDARD');
    expect(p.stage).toBe('WARMING');
    expect(p.lane).toBe('STANDARD');
    expect(p.onlineSince).toBe('2026-07-14T00:00:00.000Z');
  });

  it('recordReply increments repliesReceived', async () => {
    const profiles = new InMemoryWarmupProfileRepository();
    const svc = new WarmupService({ profiles, clock: fixedClock('2026-07-14T00:00:00.000Z') });
    await svc.enroll('a1', 'STANDARD');
    await svc.recordReply('a1');
    expect((await profiles.findByAccountId('a1'))!.repliesReceived).toBe(1);
  });

  it('evaluateAndPromote promotes only when all signals clear (incl. online hours)', async () => {
    const profiles = new InMemoryWarmupProfileRepository();
    const std = LANE_POLICIES.STANDARD;
    // enroll at T0; evaluate at T0 + (minOnlineHours) hours
    const t0 = Date.parse('2026-07-14T00:00:00.000Z');
    let now = t0;
    const svc = new WarmupService({ profiles, clock: () => new Date(now).toISOString() });
    const p = await svc.enroll('a1', 'STANDARD');
    // manually set the message/reply signals to exactly the bar
    await profiles.save({ ...p, warmupMessagesSent: std.minWarmupMessages, repliesReceived: std.minReplies });

    // too early -> not promoted
    now = t0 + (std.minOnlineHours - 1) * 3600_000;
    expect(await svc.evaluateAndPromote('a1')).toBe(false);
    expect((await profiles.findByAccountId('a1'))!.stage).toBe('WARMING');

    // enough online hours -> promoted
    now = t0 + std.minOnlineHours * 3600_000;
    expect(await svc.evaluateAndPromote('a1')).toBe(true);
    const matured = await profiles.findByAccountId('a1');
    expect(matured!.stage).toBe('MATURE');
    expect(matured!.maturedAt).toBe(new Date(now).toISOString());
  });
});
