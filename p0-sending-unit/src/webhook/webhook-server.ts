import Fastify, { type FastifyInstance } from 'fastify';
import type { AccountService } from '../pool/account-service.js';
import type { WebhookPayload } from '../evolution/webhook-mapper.js';

export function buildWebhookServer(
  svc: Pick<AccountService, 'handleWebhook'>,
  webhookSecret: string,
): FastifyInstance {
  const app = Fastify({ logger: false });
  app.post<{ Params: { token: string } }>('/hook/:token', async (req, reply) => {
    if (req.params.token !== webhookSecret) {
      return reply.code(401).send({ ok: false });
    }
    try {
      await svc.handleWebhook(req.body as WebhookPayload);
    } catch (err) {
      // Webhook delivery is at-least-once and providers re-emit events; ack 200 + log so the
      // provider does not retry-storm. The state machine stays strict internally.
      console.error('handleWebhook failed:', err);
    }
    return reply.send({ ok: true });
  });
  return app;
}
