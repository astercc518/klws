import { describe, it, expect, vi } from 'vitest';
import { buildWebhookServer } from '../../src/webhook/webhook-server.js';

describe('webhook server', () => {
  it('POST /hook forwards payload to service.handleWebhook and returns ok', async () => {
    const handleWebhook = vi.fn(async () => {});
    const app = buildWebhookServer({ handleWebhook } as never);
    const payload = { event: 'connection.update', instance: 'acct-1', data: { state: 'open' } };

    const res = await app.inject({ method: 'POST', url: '/hook', payload });

    expect(res.statusCode).toBe(200);
    expect(JSON.parse(res.body)).toEqual({ ok: true });
    expect(handleWebhook).toHaveBeenCalledWith(payload);
    await app.close();
  });
});
