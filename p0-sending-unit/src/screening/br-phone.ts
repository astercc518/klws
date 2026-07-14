export type NormalizeResult = { ok: true; e164: string } | { ok: false; reason: string };

export function normalizeBrPhone(input: string): NormalizeResult {
  let digits = input.replace(/\D/g, '');
  if (digits.startsWith('00')) digits = digits.slice(2);

  if (!digits.startsWith('55')) {
    if (digits.length === 10 || digits.length === 11) {
      digits = `55${digits}`;
    } else {
      return { ok: false, reason: 'no country code' };
    }
  }

  let national = digits.slice(2);
  if (national.length !== 10 && national.length !== 11) {
    return { ok: false, reason: 'bad length' };
  }

  const ddd = national.slice(0, 2);
  if (Number(ddd) < 11) {
    return { ok: false, reason: 'bad DDD' };
  }

  if (national.length === 10) {
    const firstSubscriber = national.charAt(2);
    if ('6789'.includes(firstSubscriber)) {
      national = `${ddd}9${national.slice(2)}`;
    }
  }

  return { ok: true, e164: `55${national}` };
}
