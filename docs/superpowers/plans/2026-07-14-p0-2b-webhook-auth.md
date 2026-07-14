# P0-2b Webhook 鉴权 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`) syntax.

**Goal:** 给 `/hook` 加密钥令牌鉴权，令伪造的 webhook（如伪造 `connection.update close 401` 杀号、伪造回执）被 401 拒绝且不触发任何状态变更。

**Architecture:** 路径令牌方案——webhook 路由改为 `POST /hook/:token`，服务器用注入的 `webhookSecret` 校验；令牌不匹配返回 401 且不调用 `handleWebhook`。`index.ts` 把密钥拼进传给 Evolution 的 webhook URL（`.../hook/<secret>`），Evolution 回调时自带令牌。不依赖 Evolution 的自定义 header 转发。

**Tech Stack:** 同 P0-1/P0-2（TypeScript strict · Fastify · Vitest）。改既有 `p0-sending-unit/`。

## Global Constraints

- 沿用既有约束：Node ≥ 20、TS `strict:true` 无 `any`（测试桩除外）、所有 I/O 注入、频繁提交。
- 鉴权失败必须**在调用 `handleWebhook` 之前**短路返回 401——绝不能让未鉴权请求触达状态机。
- `WEBHOOK_SECRET` 为必填环境变量，缺失时 `loadConfig` 抛错（与既有必填项一致）。
- 不改 `AccountService`/状态机逻辑；只改 config、webhook-server、index 接线。

---

## Task 1: config 增加 WEBHOOK_SECRET

**Files:**
- Modify: `p0-sending-unit/src/config.ts`
- Modify: `p0-sending-unit/tests/config.test.ts`
- Modify: `p0-sending-unit/.env.example`

**Interfaces:**
- Consumes: 现有 `loadConfig`、`Config`
- Produces: `Config` 增加字段 `webhookSecret: string`

- [ ] **Step 1: 改失败测试**（编辑 `tests/config.test.ts`）

把"reads all fields"用例的输入与断言各加一行 `WEBHOOK_SECRET`/`webhookSecret`，并新增一个缺失用例。用下面这段**替换**现有 `describe('loadConfig', ...)` 整块：

```ts
import { describe, it, expect } from 'vitest';
import { loadConfig } from '../src/config.js';

describe('loadConfig', () => {
  it('reads all fields from env', () => {
    const cfg = loadConfig({
      EVOLUTION_BASE_URL: 'http://x:8080',
      EVOLUTION_API_KEY: 'k',
      DATABASE_URL: 'postgresql://a',
      WEBHOOK_PORT: '3000',
      WEBHOOK_SECRET: 's3cr3t',
    });
    expect(cfg).toEqual({
      evolutionBaseUrl: 'http://x:8080',
      evolutionApiKey: 'k',
      databaseUrl: 'postgresql://a',
      webhookPort: 3000,
      webhookSecret: 's3cr3t',
    });
  });

  it('throws when a required var is missing', () => {
    expect(() => loadConfig({})).toThrow(/EVOLUTION_BASE_URL/);
  });

  it('throws when WEBHOOK_SECRET is missing', () => {
    expect(() =>
      loadConfig({
        EVOLUTION_BASE_URL: 'http://x:8080',
        EVOLUTION_API_KEY: 'k',
        DATABASE_URL: 'postgresql://a',
        WEBHOOK_PORT: '3000',
      }),
    ).toThrow(/WEBHOOK_SECRET/);
  });
});
```

- [ ] **Step 2: 跑测试确认失败**

Run: `npm test tests/config.test.ts`
Expected: FAIL —「reads all fields」期望含 `webhookSecret` 但实际没有，且「WEBHOOK_SECRET missing」未抛。

- [ ] **Step 3: 实现**（编辑 `src/config.ts`）

在 `Config` 接口加字段：
```ts
export interface Config {
  evolutionBaseUrl: string;
  evolutionApiKey: string;
  databaseUrl: string;
  webhookPort: number;
  webhookSecret: string;
}
```
在 `loadConfig` 的 return 对象里加一行：
```ts
    webhookSecret: required(env, 'WEBHOOK_SECRET'),
```

- [ ] **Step 4: 更新 .env.example**（编辑 `.env.example`，追加一行）

```
WEBHOOK_SECRET=dev-webhook-secret
```

- [ ] **Step 5: 跑测试确认通过**

Run: `npm test tests/config.test.ts`
Expected: PASS（3 passed）

- [ ] **Step 6: 提交**

```bash
git add p0-sending-unit/src/config.ts p0-sending-unit/tests/config.test.ts p0-sending-unit/.env.example
git commit -m "feat(p0-2b): add required WEBHOOK_SECRET config field"
```

---

## Task 2: webhook 路由令牌鉴权 + 接线

**Files:**
- Modify: `p0-sending-unit/src/webhook/webhook-server.ts`
- Modify: `p0-sending-unit/tests/webhook/webhook-server.test.ts`
- Modify: `p0-sending-unit/src/index.ts`
- Modify: `p0-sending-unit/E2E-RUNBOOK.md`

**Interfaces:**
- Consumes: `AccountService.handleWebhook`、`WebhookPayload`、`Config.webhookSecret`
- Produces: `buildWebhookServer(svc: Pick<AccountService,'handleWebhook'>, webhookSecret: string): FastifyInstance`（路由 `POST /hook/:token`）

- [ ] **Step 1: 改失败测试**（编辑 `tests/webhook/webhook-server.test.ts`，整块替换 `describe('webhook server', ...)`）

```ts
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
```

- [ ] **Step 2: 跑测试确认失败**

Run: `npm test tests/webhook/webhook-server.test.ts`
Expected: FAIL —`buildWebhookServer` 目前只接受 1 参且路由是 `/hook`，401 用例与新路由不满足。

- [ ] **Step 3: 实现**（整体替换 `src/webhook/webhook-server.ts`）

```ts
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
```

- [ ] **Step 4: 跑测试确认通过**

Run: `npm test tests/webhook/webhook-server.test.ts`
Expected: PASS（3 passed）

- [ ] **Step 5: 接线 index.ts**（编辑 `src/index.ts`）

把 webhook URL 改为带令牌，并把密钥传给服务器。

将 `service`（AccountService）的 `webhookUrl` 依赖由：
```ts
  webhookUrl: `http://host.docker.internal:${cfg.webhookPort}/hook`,
```
改为：
```ts
  webhookUrl: `http://host.docker.internal:${cfg.webhookPort}/hook/${cfg.webhookSecret}`,
```

将 `serve()` 内的：
```ts
  const app = buildWebhookServer(service);
```
改为：
```ts
  const app = buildWebhookServer(service, cfg.webhookSecret);
```

- [ ] **Step 6: 验证全项目编译 + 全量测试**

Run（从 `p0-sending-unit/`）: `node_modules/.bin/tsc --noEmit -p tsconfig.json` → 干净
Run: `npm test` → 全部通过（含 P0-1/P0-2 既有用例）

- [ ] **Step 7: 更新 E2E Runbook**（编辑 `E2E-RUNBOOK.md`，在启动服务那步附近追加一行说明）

在 `## P0-1 真机端到端验证` 的第 2 步后追加：
```markdown
   - 注意：webhook 现在带密钥令牌。`.env` 里设 `WEBHOOK_SECRET`，Evolution 回调地址会自动带上 `/hook/<secret>`；直接 POST `/hook`（无令牌）会被 401 拒绝。
```

- [ ] **Step 8: 提交**

```bash
git add p0-sending-unit/src/webhook/webhook-server.ts p0-sending-unit/tests/webhook/webhook-server.test.ts p0-sending-unit/src/index.ts p0-sending-unit/E2E-RUNBOOK.md
git commit -m "feat(p0-2b): token-authenticated webhook route (/hook/:token), reject spoofed callbacks"
```

---

## 自查（Self-Review）

**Spec 覆盖**：WEBHOOK_SECRET 配置(Task 1) · 令牌鉴权路由 + 401 短路 + 接线(Task 2)。伪造 webhook 现被 401 拦在状态机之前。

**类型一致性**：`buildWebhookServer(svc, webhookSecret)` 两参签名在 test 与 index.ts 一致；`Config.webhookSecret` 在 config、index 一致。

**明确不在本计划**：养号(P0-3)、批量投放(P0-4)、P0-2 筛号的 jid 匹配兜底(P0-4)。

**已知取舍**：令牌走 URL 路径，会出现在 Evolution 侧日志/配置里——可接受（内部服务，密钥可轮换）；比依赖 Evolution 自定义 header 转发更稳。幂等 ack（handleWebhook 抛错仍返 200）语义保留。
