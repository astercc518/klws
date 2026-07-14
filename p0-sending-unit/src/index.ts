import { PrismaClient } from '@prisma/client';
import { loadConfig } from './config.js';
import { EvolutionClient } from './evolution/evolution-client.js';
import { PrismaAccountRepository } from './adapters/prisma-account-repository.js';
import { PrismaMessageRepository } from './adapters/prisma-message-repository.js';
import { AccountService } from './pool/account-service.js';
import { buildWebhookServer } from './webhook/webhook-server.js';
import { randomUUID } from 'node:crypto';

const cfg = loadConfig(process.env);
const prisma = new PrismaClient();
const evolution = new EvolutionClient({ baseUrl: cfg.evolutionBaseUrl, apiKey: cfg.evolutionApiKey });

const service = new AccountService({
  accounts: new PrismaAccountRepository(prisma),
  messages: new PrismaMessageRepository(prisma),
  evolution,
  webhookUrl: `http://host.docker.internal:${cfg.webhookPort}/hook`,
  ids: { next: () => randomUUID() },
});

const app = buildWebhookServer(service);
app.listen({ port: cfg.webhookPort, host: '0.0.0.0' })
  .then((addr) => console.log(`webhook listening on ${addr}`))
  .catch((e) => { console.error(e); process.exit(1); });

// CLI 冒烟：node dist/index.js import <phone> | online <id> <proxyHost> <proxyPort> | send <id> <to> <text>
export { service };
