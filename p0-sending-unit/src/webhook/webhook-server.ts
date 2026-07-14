import Fastify, { type FastifyInstance } from 'fastify';
import type { AccountService } from '../pool/account-service.js';
import type { WebhookPayload } from '../evolution/webhook-mapper.js';

export function buildWebhookServer(svc: Pick<AccountService, 'handleWebhook'>): FastifyInstance {
  const app = Fastify({ logger: false });
  app.post('/hook', async (req, reply) => {
    await svc.handleWebhook(req.body as WebhookPayload);
    return reply.send({ ok: true });
  });
  return app;
}
