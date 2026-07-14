import { describe, it, expect, vi } from 'vitest';
import { ScreeningService } from '../../src/screening/screening-service.js';
import { InMemoryTargetNumberRepository } from '../../src/adapters/in-memory-target-number-repository.js';

function makeIds() {
  let n = 0;
  return { next: () => `id-${++n}` };
}

describe('ScreeningService', () => {
  it('importNumbers normalizes, marks invalid, and dedupes by e164', async () => {
    const targets = new InMemoryTargetNumberRepository();
    const svc = new ScreeningService({ targets, evolution: { checkNumbers: vi.fn() } as never, ids: makeIds() });

    const r = await svc.importNumbers(['+55 (11) 98765-4321', '11987654321', '12345']);
    // first two normalize to the same 5511987654321 -> 1 imported + 1 duplicate; '12345' invalid
    expect(r).toEqual({ imported: 1, invalid: 1, duplicates: 1 });

    const found = await targets.findByE164('5511987654321');
    expect(found!.status).toBe('PENDING_CHECK');
  });

  it('runScreening checks a pending batch and updates statuses by e164', async () => {
    const targets = new InMemoryTargetNumberRepository();
    const checkNumbers = vi.fn(async () => [
      { number: '5511987654321', exists: true, jid: '5511987654321@s.whatsapp.net' },
      { number: '5511222222222', exists: false, jid: null },
    ]);
    const svc = new ScreeningService({ targets, evolution: { checkNumbers } as never, ids: makeIds() });
    await svc.importNumbers(['5511987654321', '5511222222222']);

    const r = await svc.runScreening('checker-1', 100);
    expect(r).toEqual({ checked: 2, onWhatsApp: 1, notOnWhatsApp: 1 });
    expect(checkNumbers).toHaveBeenCalledWith({ instanceName: 'checker-1', numbers: expect.arrayContaining(['5511987654321', '5511222222222']) });

    const on = await targets.findByE164('5511987654321');
    expect(on!.status).toBe('ON_WHATSAPP');
    expect(on!.jid).toBe('5511987654321@s.whatsapp.net');
    const off = await targets.findByE164('5511222222222');
    expect(off!.status).toBe('NOT_ON_WHATSAPP');
  });

  it('runScreening with no pending numbers is a no-op', async () => {
    const targets = new InMemoryTargetNumberRepository();
    const checkNumbers = vi.fn(async () => []);
    const svc = new ScreeningService({ targets, evolution: { checkNumbers } as never, ids: makeIds() });
    const r = await svc.runScreening('checker-1', 100);
    expect(r).toEqual({ checked: 0, onWhatsApp: 0, notOnWhatsApp: 0 });
    expect(checkNumbers).not.toHaveBeenCalled();
  });
});
