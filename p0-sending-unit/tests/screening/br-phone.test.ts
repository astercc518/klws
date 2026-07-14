import { describe, it, expect } from 'vitest';
import { normalizeBrPhone } from '../../src/screening/br-phone.js';

describe('normalizeBrPhone', () => {
  it('strips formatting and keeps a full mobile with 55 + DDD + 9 digits', () => {
    expect(normalizeBrPhone('+55 (11) 98765-4321')).toEqual({ ok: true, e164: '5511987654321' });
  });

  it('prepends 55 when given a domestic 11-digit mobile', () => {
    expect(normalizeBrPhone('11987654321')).toEqual({ ok: true, e164: '5511987654321' });
  });

  it('inserts the ninth digit for an 8-digit mobile (subscriber starts 6-9)', () => {
    // 55 + DDD 11 + 8-digit mobile 87654321 -> insert 9 -> 5511987654321
    expect(normalizeBrPhone('551187654321')).toEqual({ ok: true, e164: '5511987654321' });
  });

  it('keeps a landline (subscriber starts 2-5) at 10 national digits', () => {
    expect(normalizeBrPhone('551132654321')).toEqual({ ok: true, e164: '551132654321' });
  });

  it('drops a leading 00 international prefix', () => {
    expect(normalizeBrPhone('005511987654321')).toEqual({ ok: true, e164: '5511987654321' });
  });

  it('rejects too-short input', () => {
    expect(normalizeBrPhone('12345')).toEqual({ ok: false, reason: 'no country code' });
  });

  it('rejects a bad DDD (< 11)', () => {
    // 55 + DDD 09 + 9 digits -> DDD 09 invalid
    expect(normalizeBrPhone('5509987654321')).toEqual({ ok: false, reason: 'bad DDD' });
  });

  it('rejects wrong national length after 55', () => {
    expect(normalizeBrPhone('55119')).toEqual({ ok: false, reason: 'bad length' });
  });
});
