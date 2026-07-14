import { describe, it, expect } from 'vitest';
import { EvolutionClient, EvolutionApiError } from '../../src/evolution/evolution-client.js';
import type { Proxy } from '../../src/domain/account.js';

const proxy: Proxy = { host: '1.2.3.4', port: 8000, protocol: 'http', username: 'u', password: 'p' };

function stub(status: number, body: unknown) {
  const calls: { url: string; init: RequestInit }[] = [];
  const fetchFn = (async (url: string | URL, init?: RequestInit) => {
    calls.push({ url: String(url), init: init ?? {} });
    return new Response(JSON.stringify(body), { status });
  }) as unknown as typeof fetch;
  return { fetchFn, calls };
}

describe('EvolutionClient', () => {
  it('createInstance posts to /instance/create with apikey, number and proxy', async () => {
    const { fetchFn, calls } = stub(201, { instance: { instanceName: 'acct-1' } });
    const c = new EvolutionClient({ baseUrl: 'http://e:8080', apiKey: 'k', fetchFn });
    await c.createInstance({ instanceName: 'acct-1', number: '5511999999999', proxy, webhookUrl: 'http://w/hook' });

    expect(calls[0]!.url).toBe('http://e:8080/instance/create');
    const headers = calls[0]!.init.headers as Record<string, string>;
    expect(headers['apikey']).toBe('k');
    const sent = JSON.parse(calls[0]!.init.body as string);
    expect(sent.instanceName).toBe('acct-1');
    expect(sent.number).toBe('5511999999999');
    expect(sent.integration).toBe('WHATSAPP-BAILEYS');
    expect(sent.proxyHost).toBe('1.2.3.4');
    expect(sent.proxyPort).toBe('8000');
    expect(sent.webhook.url).toBe('http://w/hook');
  });

  it('connect returns pairingCode', async () => {
    const { fetchFn } = stub(200, { pairingCode: 'ABCD-1234', code: 'ref' });
    const c = new EvolutionClient({ baseUrl: 'http://e:8080', apiKey: 'k', fetchFn });
    expect(await c.connect('acct-1')).toEqual({ pairingCode: 'ABCD-1234' });
  });

  it('sendText returns providerMessageId from response key.id', async () => {
    const { fetchFn, calls } = stub(201, { key: { id: 'MSGID123' } });
    const c = new EvolutionClient({ baseUrl: 'http://e:8080', apiKey: 'k', fetchFn });
    const r = await c.sendText({ instanceName: 'acct-1', to: '5511888888888', text: 'oi' });
    expect(r).toEqual({ providerMessageId: 'MSGID123' });
    expect(calls[0]!.url).toBe('http://e:8080/message/sendText/acct-1');
    const sent = JSON.parse(calls[0]!.init.body as string);
    expect(sent.number).toBe('5511888888888');
    expect(sent.text).toBe('oi');
  });

  it('throws EvolutionApiError on non-2xx', async () => {
    const { fetchFn } = stub(400, { message: 'bad' });
    const c = new EvolutionClient({ baseUrl: 'http://e:8080', apiKey: 'k', fetchFn });
    await expect(c.connect('acct-1')).rejects.toBeInstanceOf(EvolutionApiError);
  });
});
