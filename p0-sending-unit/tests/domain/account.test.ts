import { describe, it, expect } from 'vitest';
import { newInstanceName } from '../../src/domain/account.js';
import { InMemoryAccountRepository } from '../../src/adapters/in-memory-account-repository.js';
import type { Account } from '../../src/domain/account.js';

const sample: Account = {
  id: 'a1', phoneNumber: '5511999999999', state: 'IMPORTED',
  instanceName: 'acct-5511999999999', proxy: null, deadReason: null,
};

describe('account domain', () => {
  it('newInstanceName derives a stable instance name', () => {
    expect(newInstanceName('5511999999999')).toBe('acct-5511999999999');
  });

  it('in-memory repo saves and finds by id and instance name', async () => {
    const repo = new InMemoryAccountRepository();
    await repo.save(sample);
    expect(await repo.findById('a1')).toEqual(sample);
    expect(await repo.findByInstanceName('acct-5511999999999')).toEqual(sample);
    expect(await repo.findById('nope')).toBeNull();
  });
});
