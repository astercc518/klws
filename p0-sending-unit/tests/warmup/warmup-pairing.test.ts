import { describe, it, expect, vi } from 'vitest';
import { WarmupService } from '../../src/warmup/warmup-service.js';
import { InMemoryWarmupProfileRepository } from '../../src/adapters/in-memory-warmup-profile-repository.js';
import { InMemoryAccountRepository } from '../../src/adapters/in-memory-account-repository.js';
import type { Account } from '../../src/domain/account.js';

function onlineAccount(id: string): Account {
  return { id, phoneNumber: `5511${id}`, state: 'ONLINE', instanceName: `acct-${id}`, proxy: null, deadReason: null };
}

async function setup() {
  const profiles = new InMemoryWarmupProfileRepository();
  const accounts = new InMemoryAccountRepository();
  const sendText = vi.fn(async () => ({ providerMessageId: 'M' }));
  const svc = new WarmupService({ profiles, accounts, evolution: { sendText } as never, clock: () => '2026-07-14T09:00:00.000Z' });
  return { profiles, accounts, sendText, svc };
}

describe('WarmupService.pairAndWarm', () => {
  it('pairs two ONLINE warming accounts and each sends one warmup message', async () => {
    const { accounts, svc, sendText } = await setup();
    await accounts.save(onlineAccount('a'));
    await accounts.save(onlineAccount('b'));
    await svc.enroll('a', 'STANDARD');
    await svc.enroll('b', 'STANDARD');

    const r = await svc.pairAndWarm(10);
    expect(r.pairs).toBe(1);
    expect(r.messagesSent).toBe(2);
    expect(sendText).toHaveBeenCalledTimes(2);
  });

  it('skips accounts whose connection is not ONLINE', async () => {
    const { accounts, svc } = await setup();
    await accounts.save(onlineAccount('a'));
    await accounts.save({ ...onlineAccount('b'), state: 'RECONNECTING' });
    await svc.enroll('a', 'STANDARD');
    await svc.enroll('b', 'STANDARD');
    const r = await svc.pairAndWarm(10);
    expect(r.pairs).toBe(0);
    expect(r.messagesSent).toBe(0);
  });

  it('increments warmupMessagesSent for senders', async () => {
    const { accounts, profiles, svc } = await setup();
    await accounts.save(onlineAccount('a'));
    await accounts.save(onlineAccount('b'));
    await svc.enroll('a', 'STANDARD');
    await svc.enroll('b', 'STANDARD');
    await svc.pairAndWarm(10);
    expect((await profiles.findByAccountId('a'))!.warmupMessagesSent).toBe(1);
    expect((await profiles.findByAccountId('b'))!.warmupMessagesSent).toBe(1);
  });
});
