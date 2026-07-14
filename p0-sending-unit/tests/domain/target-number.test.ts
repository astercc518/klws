import { describe, it, expect } from 'vitest';
import { InMemoryTargetNumberRepository } from '../../src/adapters/in-memory-target-number-repository.js';
import type { TargetNumber } from '../../src/domain/target-number.js';

function t(id: string, e164: string | null, status: TargetNumber['status']): TargetNumber {
  return { id, raw: `raw-${id}`, e164, status, jid: null };
}

describe('TargetNumber in-memory repo', () => {
  it('saves and finds by e164', async () => {
    const repo = new InMemoryTargetNumberRepository();
    const a = t('1', '5511987654321', 'PENDING_CHECK');
    await repo.save(a);
    expect(await repo.findByE164('5511987654321')).toEqual(a);
    expect(await repo.findByE164('0000')).toBeNull();
  });

  it('findByE164 ignores rows with null e164', async () => {
    const repo = new InMemoryTargetNumberRepository();
    await repo.save(t('2', null, 'INVALID'));
    expect(await repo.findByE164('5511987654321')).toBeNull();
  });

  it('listByStatus returns matching rows up to limit', async () => {
    const repo = new InMemoryTargetNumberRepository();
    await repo.save(t('1', '5511111111111', 'PENDING_CHECK'));
    await repo.save(t('2', '5511222222222', 'PENDING_CHECK'));
    await repo.save(t('3', '5511333333333', 'ON_WHATSAPP'));
    const pending = await repo.listByStatus('PENDING_CHECK', 10);
    expect(pending.map((r) => r.id).sort()).toEqual(['1', '2']);
    expect(await repo.listByStatus('PENDING_CHECK', 1)).toHaveLength(1);
  });
});
