import Fastify, { type FastifyInstance } from 'fastify';
import type { AccountService } from '../pool/account-service.js';
import type { WebhookPayload } from '../evolution/webhook-mapper.js';

export function buildWebhookServer(svc: Pick<AccountService, 'handleWebhook'>): FastifyInstance {
  const app = Fastify({ logger: false });
  app.post('/hook', async (_req, reply) => {
    try {
      await svc.handleWebhook(_req.body as WebhookPayload);
    } catch (err) {
      // Webhook delivery is at-least-once and providers re-emit events (e.g. a duplicate
      // connection.update on an already-ONLINE account, which the strict state machine
      // rejects). Ack with 200 and log so the provider does not retry-storm; the state
      // machine stays strict internally.
      console.error('handleWebhook failed:', err);
    }
    return reply.send({ ok: true });
  });
  return app;
}
