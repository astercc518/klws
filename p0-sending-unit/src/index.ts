import { PrismaClient } from '@prisma/client';
import { randomUUID } from 'node:crypto';
import { loadConfig } from './config.js';
import { EvolutionClient } from './evolution/evolution-client.js';
import { PrismaAccountRepository } from './adapters/prisma-account-repository.js';
import { PrismaMessageRepository } from './adapters/prisma-message-repository.js';
import { PrismaTargetNumberRepository } from './adapters/prisma-target-number-repository.js';
import { AccountService } from './pool/account-service.js';
import { ScreeningService } from './screening/screening-service.js';
import { PrismaWarmupProfileRepository } from './adapters/prisma-warmup-profile-repository.js';
import { WarmupService } from './warmup/warmup-service.js';
import { buildWebhookServer } from './webhook/webhook-server.js';
import type { Proxy } from './domain/account.js';

const cfg = loadConfig(process.env);
const prisma = new PrismaClient();
const evolution = new EvolutionClient({ baseUrl: cfg.evolutionBaseUrl, apiKey: cfg.evolutionApiKey });

const service = new AccountService({
  accounts: new PrismaAccountRepository(prisma),
  messages: new PrismaMessageRepository(prisma),
  evolution,
  webhookUrl: `http://host.docker.internal:${cfg.webhookPort}/hook/${cfg.webhookSecret}`,
  ids: { next: () => randomUUID() },
});

const screening = new ScreeningService({
  targets: new PrismaTargetNumberRepository(prisma),
  evolution,
  ids: { next: () => randomUUID() },
});

const warmup = new WarmupService({
  profiles: new PrismaWarmupProfileRepository(prisma),
  accounts: new PrismaAccountRepository(prisma),
  evolution,
  clock: () => new Date().toISOString(),
});

async function serve(): Promise<void> {
  const app = buildWebhookServer(service, cfg.webhookSecret);
  const addr = await app.listen({ port: cfg.webhookPort, host: '0.0.0.0' });
  console.log(`webhook listening on ${addr}`);
}

// One-shot CLI to drive the lifecycle during E2E (does NOT start the server):
//   node dist/index.js import <phone>
//   node dist/index.js online <id> <proxyHost> <proxyPort> [username] [password]
//   node dist/index.js send <id> <to> <text...>
//   node dist/index.js screen-import <num> [num...]
//   node dist/index.js screen-run <checkerInstanceName> [batchSize]
//   node dist/index.js warmup-enroll <accountId> <FAST|STANDARD>
//   node dist/index.js warmup-cycle [batchSize]
//   node dist/index.js warmup-promote <accountId>
//   node dist/index.js warmup-reply <accountId>   (manual reply signal; STANDARD-lane promotion)
//   node dist/index.js serve            (or no args) -> start webhook server
async function runCli(cmd: string, rest: string[]): Promise<void> {
  try {
    if (cmd === 'import') {
      const phone = rest[0];
      if (!phone) throw new Error('usage: import <phone>');
      console.log(JSON.stringify(await service.importAccount(phone)));
    } else if (cmd === 'online') {
      const [id, host, port, username, password] = rest;
      if (!id || !host || !port) throw new Error('usage: online <id> <proxyHost> <proxyPort> [user] [pass]');
      const proxy: Proxy = { host, port: Number(port), protocol: 'http', username, password };
      console.log(JSON.stringify(await service.bringOnline(id, proxy)));
    } else if (cmd === 'send') {
      const [id, to, ...text] = rest;
      if (!id || !to || text.length === 0) throw new Error('usage: send <id> <to> <text...>');
      console.log(JSON.stringify(await service.sendMessage(id, to, text.join(' '))));
    } else if (cmd === 'screen-import') {
      const nums = rest;
      if (nums.length === 0) throw new Error('usage: screen-import <num> [num...]');
      console.log(JSON.stringify(await screening.importNumbers(nums)));
    } else if (cmd === 'screen-run') {
      const [checker, batch] = rest;
      if (!checker) throw new Error('usage: screen-run <checkerInstanceName> [batchSize]');
      console.log(JSON.stringify(await screening.runScreening(checker, batch ? Number(batch) : 100)));
    } else if (cmd === 'warmup-enroll') {
      const [id, lane] = rest;
      if (!id || (lane !== 'FAST' && lane !== 'STANDARD')) throw new Error('usage: warmup-enroll <accountId> <FAST|STANDARD>');
      console.log(JSON.stringify(await warmup.enroll(id, lane)));
    } else if (cmd === 'warmup-cycle') {
      const [batch] = rest;
      console.log(JSON.stringify(await warmup.pairAndWarm(batch ? Number(batch) : 100)));
    } else if (cmd === 'warmup-promote') {
      const [id] = rest;
      if (!id) throw new Error('usage: warmup-promote <accountId>');
      console.log(JSON.stringify({ promoted: await warmup.evaluateAndPromote(id) }));
    } else if (cmd === 'warmup-reply') {
      // Manual reply signal until inbound-message auto-counting (MESSAGES_UPSERT) is wired.
      // STANDARD lane needs repliesReceived >= minReplies to promote; this is the only path today.
      const [id] = rest;
      if (!id) throw new Error('usage: warmup-reply <accountId>');
      await warmup.recordReply(id);
      console.log(JSON.stringify({ ok: true }));
    } else {
      throw new Error(`unknown command: ${cmd}`);
    }
  } finally {
    await prisma.$disconnect();
  }
}

const [cmd, ...rest] = process.argv.slice(2);
const main = !cmd || cmd === 'serve' ? serve() : runCli(cmd, rest);
main.catch((e) => {
  console.error(e);
  process.exit(1);
});

export { service, screening };
