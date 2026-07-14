import { describe, it, expect } from 'vitest';
import { EvolutionClient } from '../../src/evolution/evolution-client.js';

function stub(status: number, body: unknown) {
  const calls: { url: string; init: RequestInit }[] = [];
  const fetchFn = (async (url: string | URL, init?: RequestInit) => {
    calls.push({ url: String(url), init: init ?? {} });
    return new Response(JSON.stringify(body), { status });
  }) as unknown as typeof fetch;
  return { fetchFn, calls };
}

describe('EvolutionClient.checkNumbers', () => {
  it('posts numbers to /chat/whatsappNumbers/{instance} and maps the response', async () => {
    const { fetchFn, calls } = stub(200, [
      { number: '5511987654321', exists: true, jid: '5511987654321@s.whatsapp.net' },
      { number: '5511000000000', exists: false, jid: null },
    ]);
    const c = new EvolutionClient({ baseUrl: 'http://e:8080', apiKey: 'k', fetchFn });
    const res = await c.checkNumbers({ instanceName: 'checker-1', numbers: ['5511987654321', '5511000000000'] });

    expect(calls[0]!.url).toBe('http://e:8080/chat/whatsappNumbers/checker-1');
    const sent = JSON.parse(calls[0]!.init.body as string);
    expect(sent.numbers).toEqual(['5511987654321', '5511000000000']);
    expect((calls[0]!.init.headers as Record<string, string>)['apikey']).toBe('k');
    expect(res).toEqual([
      { number: '5511987654321', exists: true, jid: '5511987654321@s.whatsapp.net' },
      { number: '5511000000000', exists: false, jid: null },
    ]);
  });

  it('coerces missing jid to null', async () => {
    const { fetchFn } = stub(200, [{ number: '5511987654321', exists: true }]);
    const c = new EvolutionClient({ baseUrl: 'http://e:8080', apiKey: 'k', fetchFn });
    const res = await c.checkNumbers({ instanceName: 'checker-1', numbers: ['5511987654321'] });
    expect(res).toEqual([{ number: '5511987654321', exists: true, jid: null }]);
  });
});
