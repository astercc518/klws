import { describe, it, expect, vi } from 'vitest';
import { buildWebhookServer } from '../../src/webhook/webhook-server.js';

const SECRET = 's3cr3t';

describe('webhook server', () => {
  it('POST /hook/:token with correct token forwards payload and returns ok', async () => {
    const handleWebhook = vi.fn(async () => {});
    const app = buildWebhookServer({ handleWebhook } as never, SECRET);
    const payload = { event: 'connection.update', instance: 'acct-1', data: { state: 'open' } };

    const res = await app.inject({ method: 'POST', url: `/hook/${SECRET}`, payload });

    expect(res.statusCode).toBe(200);
    expect(JSON.parse(res.body)).toEqual({ ok: true });
    expect(handleWebhook).toHaveBeenCalledWith(payload);
    await app.close();
  });

  it('rejects a wrong token with 401 and does NOT call handleWebhook', async () => {
    const handleWebhook = vi.fn(async () => {});
    const app = buildWebhookServer({ handleWebhook } as never, SECRET);

    const res = await app.inject({
      method: 'POST',
      url: '/hook/wrong-token',
      payload: { event: 'connection.update', instance: 'acct-1', data: { state: 'close', statusReason: 401 } },
    });

    expect(res.statusCode).toBe(401);
    expect(handleWebhook).not.toHaveBeenCalled();
    await app.close();
  });

  it('returns 200 ok even when handleWebhook throws (idempotent ack)', async () => {
    const handleWebhook = vi.fn(async () => { throw new Error('illegal transition'); });
    const app = buildWebhookServer({ handleWebhook } as never, SECRET);
    const res = await app.inject({
      method: 'POST',
      url: `/hook/${SECRET}`,
      payload: { event: 'connection.update', instance: 'x', data: { state: 'open' } },
    });
    expect(res.statusCode).toBe(200);
    expect(JSON.parse(res.body)).toEqual({ ok: true });
    expect(handleWebhook).toHaveBeenCalledOnce();
    await app.close();
  });
});
