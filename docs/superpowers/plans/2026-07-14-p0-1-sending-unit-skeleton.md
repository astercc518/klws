# P0-1 发送单元走骨架 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 拿 1 个协议号 → 绑代理 → 通过 Evolution API 上线 → 发 1 条 1v1 消息 → 收回执，并用号池状态机记录连接态、消息回执、封号即弃。

**Architecture:** 控制服务（本代码库）编排 Evolution API 这个发送微服务。核心是纯逻辑的**号池状态机**与**回执/封号识别**；Evolution 负责底层 WhatsApp 协议。控制服务通过 REST 调 Evolution 建实例/绑代理/发消息，通过 Webhook 接收连接与消息状态事件。所有 I/O（Evolution HTTP、Postgres）都藏在接口后，纯逻辑与编排逻辑走 TDD，真机端到端走手动 runbook。

**Tech Stack:** TypeScript (strict) · Node 20+ · Fastify (webhook) · Prisma + Postgres · Vitest · Docker Compose (Evolution API v2 + Postgres) · Evolution API v2 (基于 Baileys)。

## Global Constraints

- Node 版本 ≥ 20（用全局 `fetch`）。
- TypeScript `strict: true`，禁用 `any`（除测试桩）。
- Evolution API 版本固定 **v2**（`integration: "WHATSAPP-BAILEYS"`）。
- 所有对外 I/O 必须走接口注入（`fetchFn`、`AccountRepository`、`MessageRepository`），单测不碰真实网络/DB。
- 巴西 +55 号码规范化、在网筛号属于 **P0-2**，本计划不实现；E2E 用一个已知在网的真实测试号码。
- 状态机是唯一真理源：任何连接/消息事件都必须经 `transition()`，非法转移抛错，不静默吞。
- 频繁提交：每个 Task 末尾提交一次。

---

## 文件结构

```
p0-sending-unit/
  package.json
  tsconfig.json
  vitest.config.ts
  docker-compose.yml
  .env.example
  prisma/
    schema.prisma
  src/
    config.ts                     # 环境变量加载
    domain/
      account-state.ts            # 状态机（纯）
      account.ts                  # Account 实体 + Proxy 值对象（纯）
      message.ts                  # Message 实体 + 状态枚举（纯）
    ports/
      account-repository.ts       # AccountRepository 接口
      message-repository.ts       # MessageRepository 接口
    adapters/
      in-memory-account-repository.ts
      in-memory-message-repository.ts
      prisma-account-repository.ts
      prisma-message-repository.ts
    evolution/
      evolution-types.ts          # 请求/响应/webhook 类型
      evolution-client.ts         # Evolution REST 客户端（注入 fetch）
      webhook-mapper.ts           # webhook payload -> 领域事件（纯）
    pool/
      account-service.ts          # 编排：import/bringOnline/sendMessage/handleWebhook
    webhook/
      webhook-server.ts           # Fastify 服务，收 Evolution webhook
    index.ts                      # 组装 + CLI 入口
  tests/
    domain/account-state.test.ts
    domain/account.test.ts
    evolution/evolution-client.test.ts
    evolution/webhook-mapper.test.ts
    pool/account-service.test.ts
    adapters/prisma-account-repository.test.ts
  E2E-RUNBOOK.md
```

---

## Task 1: 项目脚手架 + Docker Compose + 配置

**Files:**
- Create: `p0-sending-unit/package.json`
- Create: `p0-sending-unit/tsconfig.json`
- Create: `p0-sending-unit/vitest.config.ts`
- Create: `p0-sending-unit/docker-compose.yml`
- Create: `p0-sending-unit/.env.example`
- Create: `p0-sending-unit/src/config.ts`
- Test: `p0-sending-unit/tests/config.test.ts`

**Interfaces:**
- Consumes: 无
- Produces: `loadConfig(env: NodeJS.ProcessEnv): Config`，`Config = { evolutionBaseUrl: string; evolutionApiKey: string; databaseUrl: string; webhookPort: number }`

- [ ] **Step 1: 初始化 package.json**

```json
{
  "name": "p0-sending-unit",
  "private": true,
  "type": "module",
  "scripts": {
    "test": "vitest run",
    "test:watch": "vitest",
    "build": "tsc -p tsconfig.json",
    "start": "node dist/index.js",
    "prisma:generate": "prisma generate",
    "prisma:migrate": "prisma migrate dev"
  },
  "dependencies": {
    "@prisma/client": "^5.20.0",
    "fastify": "^4.28.0"
  },
  "devDependencies": {
    "@types/node": "^20.14.0",
    "prisma": "^5.20.0",
    "typescript": "^5.5.0",
    "vitest": "^2.0.0"
  }
}
```

- [ ] **Step 2: 加 tsconfig.json 与 vitest.config.ts**

`tsconfig.json`:
```json
{
  "compilerOptions": {
    "target": "ES2022",
    "module": "ES2022",
    "moduleResolution": "bundler",
    "strict": true,
    "noUncheckedIndexedAccess": true,
    "esModuleInterop": true,
    "skipLibCheck": true,
    "outDir": "dist",
    "rootDir": "src"
  },
  "include": ["src"]
}
```

`vitest.config.ts`:
```ts
import { defineConfig } from 'vitest/config';

export default defineConfig({
  test: { include: ['tests/**/*.test.ts'], environment: 'node' },
});
```

- [ ] **Step 3: 加 docker-compose.yml（Evolution API + Postgres）**

```yaml
services:
  postgres:
    image: postgres:16
    environment:
      POSTGRES_USER: klws
      POSTGRES_PASSWORD: klws
      POSTGRES_DB: klws
    ports: ["5432:5432"]
  evolution:
    image: atendai/evolution-api:v2.1.1
    depends_on: [postgres]
    environment:
      AUTHENTICATION_API_KEY: dev-evolution-key
      DATABASE_ENABLED: "true"
      DATABASE_PROVIDER: postgresql
      DATABASE_CONNECTION_URI: postgresql://klws:klws@postgres:5432/evolution
      WEBHOOK_GLOBAL_ENABLED: "false"
    ports: ["8080:8080"]
```

`.env.example`:
```
EVOLUTION_BASE_URL=http://localhost:8080
EVOLUTION_API_KEY=dev-evolution-key
DATABASE_URL=postgresql://klws:klws@localhost:5432/klws
WEBHOOK_PORT=3000
```

- [ ] **Step 4: 写失败测试 tests/config.test.ts**

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
    });
    expect(cfg).toEqual({
      evolutionBaseUrl: 'http://x:8080',
      evolutionApiKey: 'k',
      databaseUrl: 'postgresql://a',
      webhookPort: 3000,
    });
  });

  it('throws when a required var is missing', () => {
    expect(() => loadConfig({})).toThrow(/EVOLUTION_BASE_URL/);
  });
});
```

- [ ] **Step 5: 跑测试确认失败**

Run: `npm install && npm test`
Expected: FAIL —「Cannot find module '../src/config.js'」

- [ ] **Step 6: 实现 src/config.ts**

```ts
export interface Config {
  evolutionBaseUrl: string;
  evolutionApiKey: string;
  databaseUrl: string;
  webhookPort: number;
}

function required(env: NodeJS.ProcessEnv, key: string): string {
  const v = env[key];
  if (!v) throw new Error(`Missing required env var: ${key}`);
  return v;
}

export function loadConfig(env: NodeJS.ProcessEnv): Config {
  return {
    evolutionBaseUrl: required(env, 'EVOLUTION_BASE_URL'),
    evolutionApiKey: required(env, 'EVOLUTION_API_KEY'),
    databaseUrl: required(env, 'DATABASE_URL'),
    webhookPort: Number(required(env, 'WEBHOOK_PORT')),
  };
}
```

- [ ] **Step 7: 跑测试确认通过**

Run: `npm test`
Expected: PASS（2 passed）

- [ ] **Step 8: 提交**

```bash
git add p0-sending-unit
git commit -m "chore(p0-1): scaffold project, docker-compose, config loader"
```

---

## Task 2: 号池状态机（纯逻辑）

**Files:**
- Create: `p0-sending-unit/src/domain/account-state.ts`
- Test: `p0-sending-unit/tests/domain/account-state.test.ts`

**Interfaces:**
- Consumes: 无
- Produces:
  - `type AccountState = 'IMPORTED' | 'PROXY_BOUND' | 'CONNECTING' | 'ONLINE' | 'RECONNECTING' | 'DEAD'`
  - `type AccountEvent = { type: 'BIND_PROXY' } | { type: 'CONNECT' } | { type: 'CONNECTED' } | { type: 'DISCONNECTED' } | { type: 'LOGGED_OUT' } | { type: 'RETIRE' }`
  - `transition(state: AccountState, event: AccountEvent): AccountState`（非法转移抛 `InvalidTransitionError`）
  - `class InvalidTransitionError extends Error`

- [ ] **Step 1: 写失败测试**

```ts
import { describe, it, expect } from 'vitest';
import { transition, InvalidTransitionError } from '../../src/domain/account-state.js';

describe('account state machine', () => {
  it('happy path: IMPORTED -> ... -> ONLINE', () => {
    let s = transition('IMPORTED', { type: 'BIND_PROXY' });
    expect(s).toBe('PROXY_BOUND');
    s = transition(s, { type: 'CONNECT' });
    expect(s).toBe('CONNECTING');
    s = transition(s, { type: 'CONNECTED' });
    expect(s).toBe('ONLINE');
  });

  it('ONLINE + DISCONNECTED -> RECONNECTING -> CONNECTED -> ONLINE', () => {
    expect(transition('ONLINE', { type: 'DISCONNECTED' })).toBe('RECONNECTING');
    expect(transition('RECONNECTING', { type: 'CONNECTED' })).toBe('ONLINE');
  });

  it('LOGGED_OUT from any live state -> DEAD (封号即弃)', () => {
    expect(transition('CONNECTING', { type: 'LOGGED_OUT' })).toBe('DEAD');
    expect(transition('ONLINE', { type: 'LOGGED_OUT' })).toBe('DEAD');
    expect(transition('RECONNECTING', { type: 'LOGGED_OUT' })).toBe('DEAD');
  });

  it('RETIRE from any state -> DEAD', () => {
    expect(transition('ONLINE', { type: 'RETIRE' })).toBe('DEAD');
  });

  it('rejects illegal transition', () => {
    expect(() => transition('DEAD', { type: 'CONNECT' })).toThrow(InvalidTransitionError);
    expect(() => transition('IMPORTED', { type: 'CONNECTED' })).toThrow(InvalidTransitionError);
  });
});
```

- [ ] **Step 2: 跑测试确认失败**

Run: `npm test tests/domain/account-state.test.ts`
Expected: FAIL —「Cannot find module」

- [ ] **Step 3: 实现状态机**

```ts
export type AccountState =
  | 'IMPORTED' | 'PROXY_BOUND' | 'CONNECTING' | 'ONLINE' | 'RECONNECTING' | 'DEAD';

export type AccountEvent =
  | { type: 'BIND_PROXY' }
  | { type: 'CONNECT' }
  | { type: 'CONNECTED' }
  | { type: 'DISCONNECTED' }
  | { type: 'LOGGED_OUT' }
  | { type: 'RETIRE' };

export class InvalidTransitionError extends Error {
  constructor(state: AccountState, event: AccountEvent['type']) {
    super(`Invalid transition: ${event} from ${state}`);
    this.name = 'InvalidTransitionError';
  }
}

const TABLE: Record<AccountState, Partial<Record<AccountEvent['type'], AccountState>>> = {
  IMPORTED:     { BIND_PROXY: 'PROXY_BOUND', RETIRE: 'DEAD' },
  PROXY_BOUND:  { CONNECT: 'CONNECTING', RETIRE: 'DEAD' },
  CONNECTING:   { CONNECTED: 'ONLINE', DISCONNECTED: 'RECONNECTING', LOGGED_OUT: 'DEAD', RETIRE: 'DEAD' },
  ONLINE:       { DISCONNECTED: 'RECONNECTING', LOGGED_OUT: 'DEAD', RETIRE: 'DEAD' },
  RECONNECTING: { CONNECTED: 'ONLINE', DISCONNECTED: 'RECONNECTING', LOGGED_OUT: 'DEAD', RETIRE: 'DEAD' },
  DEAD:         {},
};

export function transition(state: AccountState, event: AccountEvent): AccountState {
  const next = TABLE[state][event.type];
  if (!next) throw new InvalidTransitionError(state, event.type);
  return next;
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `npm test tests/domain/account-state.test.ts`
Expected: PASS（5 passed）

- [ ] **Step 5: 提交**

```bash
git add p0-sending-unit/src/domain/account-state.ts p0-sending-unit/tests/domain/account-state.test.ts
git commit -m "feat(p0-1): account pool state machine with 封号即弃 semantics"
```

---

## Task 3: 领域实体 + 仓库接口 + 内存实现

**Files:**
- Create: `p0-sending-unit/src/domain/account.ts`
- Create: `p0-sending-unit/src/domain/message.ts`
- Create: `p0-sending-unit/src/ports/account-repository.ts`
- Create: `p0-sending-unit/src/ports/message-repository.ts`
- Create: `p0-sending-unit/src/adapters/in-memory-account-repository.ts`
- Create: `p0-sending-unit/src/adapters/in-memory-message-repository.ts`
- Test: `p0-sending-unit/tests/domain/account.test.ts`

**Interfaces:**
- Consumes: `AccountState`（Task 2）
- Produces:
  - `interface Proxy { host: string; port: number; protocol: 'http' | 'socks5'; username?: string; password?: string }`
  - `interface Account { id: string; phoneNumber: string; state: AccountState; instanceName: string; proxy: Proxy | null; deadReason: string | null }`
  - `type MessageStatus = 'PENDING' | 'SENT' | 'DELIVERED' | 'READ' | 'FAILED'`
  - `interface Message { id: string; accountId: string; to: string; providerMessageId: string | null; status: MessageStatus }`
  - `interface AccountRepository { save(a: Account): Promise<void>; findById(id: string): Promise<Account | null>; findByInstanceName(name: string): Promise<Account | null> }`
  - `interface MessageRepository { save(m: Message): Promise<void>; findByProviderMessageId(pid: string): Promise<Message | null>; updateStatus(id: string, s: MessageStatus): Promise<void> }`
  - `class InMemoryAccountRepository implements AccountRepository`
  - `class InMemoryMessageRepository implements MessageRepository`
  - `newInstanceName(phoneNumber: string): string`（生成 Evolution 实例名，形如 `acct-<phoneNumber>`）

- [ ] **Step 1: 写失败测试**

```ts
import { describe, it, expect } from 'vitest';
import { newInstanceName } from '../../src/domain/account.js';
import { InMemoryAccountRepository } from '../../src/adapters/in-memory-account-repository.js';
import type { Account } from '../../src/domain/account.js';

const sample: Account = {
  id: 'a1', phoneNumber: '5511999999999', state: 'IMPORTED',
  instanceName: 'acct-5511999999999', proxy: null, deadReason: null,
};

describe('account domain', () => {
  it('newInstanceName derives a stable instance name', () => {
    expect(newInstanceName('5511999999999')).toBe('acct-5511999999999');
  });

  it('in-memory repo saves and finds by id and instance name', async () => {
    const repo = new InMemoryAccountRepository();
    await repo.save(sample);
    expect(await repo.findById('a1')).toEqual(sample);
    expect(await repo.findByInstanceName('acct-5511999999999')).toEqual(sample);
    expect(await repo.findById('nope')).toBeNull();
  });
});
```

- [ ] **Step 2: 跑测试确认失败**

Run: `npm test tests/domain/account.test.ts`
Expected: FAIL —「Cannot find module」

- [ ] **Step 3: 实现实体与消息类型**

`src/domain/account.ts`:
```ts
import type { AccountState } from './account-state.js';

export interface Proxy {
  host: string;
  port: number;
  protocol: 'http' | 'socks5';
  username?: string;
  password?: string;
}

export interface Account {
  id: string;
  phoneNumber: string;
  state: AccountState;
  instanceName: string;
  proxy: Proxy | null;
  deadReason: string | null;
}

export function newInstanceName(phoneNumber: string): string {
  return `acct-${phoneNumber}`;
}
```

`src/domain/message.ts`:
```ts
export type MessageStatus = 'PENDING' | 'SENT' | 'DELIVERED' | 'READ' | 'FAILED';

export interface Message {
  id: string;
  accountId: string;
  to: string;
  providerMessageId: string | null;
  status: MessageStatus;
}
```

- [ ] **Step 4: 实现仓库接口**

`src/ports/account-repository.ts`:
```ts
import type { Account } from '../domain/account.js';

export interface AccountRepository {
  save(a: Account): Promise<void>;
  findById(id: string): Promise<Account | null>;
  findByInstanceName(name: string): Promise<Account | null>;
}
```

`src/ports/message-repository.ts`:
```ts
import type { Message, MessageStatus } from '../domain/message.js';

export interface MessageRepository {
  save(m: Message): Promise<void>;
  findByProviderMessageId(pid: string): Promise<Message | null>;
  updateStatus(id: string, status: MessageStatus): Promise<void>;
}
```

- [ ] **Step 5: 实现内存适配器**

`src/adapters/in-memory-account-repository.ts`:
```ts
import type { Account } from '../domain/account.js';
import type { AccountRepository } from '../ports/account-repository.js';

export class InMemoryAccountRepository implements AccountRepository {
  private byId = new Map<string, Account>();

  async save(a: Account): Promise<void> {
    this.byId.set(a.id, { ...a });
  }
  async findById(id: string): Promise<Account | null> {
    const a = this.byId.get(id);
    return a ? { ...a } : null;
  }
  async findByInstanceName(name: string): Promise<Account | null> {
    for (const a of this.byId.values()) if (a.instanceName === name) return { ...a };
    return null;
  }
}
```

`src/adapters/in-memory-message-repository.ts`:
```ts
import type { Message, MessageStatus } from '../domain/message.js';
import type { MessageRepository } from '../ports/message-repository.js';

export class InMemoryMessageRepository implements MessageRepository {
  private byId = new Map<string, Message>();

  async save(m: Message): Promise<void> {
    this.byId.set(m.id, { ...m });
  }
  async findByProviderMessageId(pid: string): Promise<Message | null> {
    for (const m of this.byId.values()) if (m.providerMessageId === pid) return { ...m };
    return null;
  }
  async updateStatus(id: string, status: MessageStatus): Promise<void> {
    const m = this.byId.get(id);
    if (m) this.byId.set(id, { ...m, status });
  }
}
```

- [ ] **Step 6: 跑测试确认通过**

Run: `npm test tests/domain/account.test.ts`
Expected: PASS（2 passed）

- [ ] **Step 7: 提交**

```bash
git add p0-sending-unit/src/domain p0-sending-unit/src/ports p0-sending-unit/src/adapters/in-memory-*.ts p0-sending-unit/tests/domain/account.test.ts
git commit -m "feat(p0-1): domain entities, repository ports, in-memory adapters"
```

---

## Task 4: Evolution API 客户端（注入 fetch）

**Files:**
- Create: `p0-sending-unit/src/evolution/evolution-types.ts`
- Create: `p0-sending-unit/src/evolution/evolution-client.ts`
- Test: `p0-sending-unit/tests/evolution/evolution-client.test.ts`

**Interfaces:**
- Consumes: `Proxy`（Task 3）
- Produces:
  - `type FetchFn = typeof fetch`
  - `class EvolutionClient` 构造 `{ baseUrl: string; apiKey: string; fetchFn?: FetchFn }`
    - `createInstance(p: { instanceName: string; number: string; proxy: Proxy; webhookUrl: string }): Promise<void>`
    - `connect(instanceName: string): Promise<{ pairingCode: string | null }>`
    - `sendText(p: { instanceName: string; to: string; text: string }): Promise<{ providerMessageId: string }>`
  - `class EvolutionApiError extends Error`

- [ ] **Step 1: 写失败测试（桩 fetch，断言请求与解析）**

```ts
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
```

- [ ] **Step 2: 跑测试确认失败**

Run: `npm test tests/evolution/evolution-client.test.ts`
Expected: FAIL —「Cannot find module」

- [ ] **Step 3: 实现类型**

`src/evolution/evolution-types.ts`:
```ts
export interface CreateInstancePayload {
  instanceName: string;
  number: string;
  integration: 'WHATSAPP-BAILEYS';
  proxyHost: string;
  proxyPort: string;
  proxyProtocol: 'http' | 'socks5';
  proxyUsername?: string;
  proxyPassword?: string;
  webhook: { url: string; events: string[] };
}

export interface ConnectResponse { pairingCode?: string | null; code?: string }
export interface SendTextResponse { key?: { id?: string } }
```

- [ ] **Step 4: 实现客户端**

`src/evolution/evolution-client.ts`:
```ts
import type { Proxy } from '../domain/account.js';
import type { CreateInstancePayload, ConnectResponse, SendTextResponse } from './evolution-types.js';

export type FetchFn = typeof fetch;

export class EvolutionApiError extends Error {
  constructor(public status: number, message: string) {
    super(`Evolution API ${status}: ${message}`);
    this.name = 'EvolutionApiError';
  }
}

export class EvolutionClient {
  private baseUrl: string;
  private apiKey: string;
  private fetchFn: FetchFn;

  constructor(opts: { baseUrl: string; apiKey: string; fetchFn?: FetchFn }) {
    this.baseUrl = opts.baseUrl.replace(/\/$/, '');
    this.apiKey = opts.apiKey;
    this.fetchFn = opts.fetchFn ?? fetch;
  }

  private async request<T>(method: string, path: string, body?: unknown): Promise<T> {
    const res = await this.fetchFn(`${this.baseUrl}${path}`, {
      method,
      headers: { 'Content-Type': 'application/json', apikey: this.apiKey },
      body: body === undefined ? undefined : JSON.stringify(body),
    });
    if (!res.ok) {
      const text = await res.text().catch(() => '');
      throw new EvolutionApiError(res.status, text);
    }
    return (await res.json()) as T;
  }

  async createInstance(p: {
    instanceName: string; number: string; proxy: Proxy; webhookUrl: string;
  }): Promise<void> {
    const payload: CreateInstancePayload = {
      instanceName: p.instanceName,
      number: p.number,
      integration: 'WHATSAPP-BAILEYS',
      proxyHost: p.proxy.host,
      proxyPort: String(p.proxy.port),
      proxyProtocol: p.proxy.protocol,
      proxyUsername: p.proxy.username,
      proxyPassword: p.proxy.password,
      webhook: {
        url: p.webhookUrl,
        events: ['CONNECTION_UPDATE', 'MESSAGES_UPDATE'],
      },
    };
    await this.request('POST', '/instance/create', payload);
  }

  async connect(instanceName: string): Promise<{ pairingCode: string | null }> {
    const r = await this.request<ConnectResponse>('GET', `/instance/connect/${instanceName}`);
    return { pairingCode: r.pairingCode ?? null };
  }

  async sendText(p: { instanceName: string; to: string; text: string }): Promise<{ providerMessageId: string }> {
    const r = await this.request<SendTextResponse>('POST', `/message/sendText/${p.instanceName}`, {
      number: p.to,
      text: p.text,
    });
    const id = r.key?.id;
    if (!id) throw new EvolutionApiError(200, 'sendText response missing key.id');
    return { providerMessageId: id };
  }
}
```

- [ ] **Step 5: 跑测试确认通过**

Run: `npm test tests/evolution/evolution-client.test.ts`
Expected: PASS（4 passed）

- [ ] **Step 6: 提交**

```bash
git add p0-sending-unit/src/evolution/evolution-types.ts p0-sending-unit/src/evolution/evolution-client.ts p0-sending-unit/tests/evolution/evolution-client.test.ts
git commit -m "feat(p0-1): typed Evolution API client with injectable fetch"
```

---

## Task 5: Webhook 事件映射（纯逻辑）

**Files:**
- Create: `p0-sending-unit/src/evolution/webhook-mapper.ts`
- Test: `p0-sending-unit/tests/evolution/webhook-mapper.test.ts`

**Interfaces:**
- Consumes: `AccountEvent`（Task 2），`MessageStatus`（Task 3）
- Produces:
  - `interface WebhookPayload { event: string; instance: string; data: Record<string, unknown> }`
  - `type WebhookResult = { kind: 'account_event'; instanceName: string; event: AccountEvent } | { kind: 'message_status'; instanceName: string; providerMessageId: string; status: MessageStatus } | { kind: 'ignored' }`
  - `mapWebhook(p: WebhookPayload): WebhookResult`

映射规则（Evolution v2）：
- `connection.update` `data.state==='open'` → `CONNECTED`
- `connection.update` `data.state==='close'` 且 `data.statusReason===401` → `LOGGED_OUT`（封号）
- `connection.update` `data.state==='close'` 其它原因 → `DISCONNECTED`
- `connection.update` `data.state==='connecting'` → `ignored`
- `messages.update` `data.status`：`SERVER_ACK→SENT`、`DELIVERY_ACK→DELIVERED`、`READ→READ`、`PLAYED→READ`，其它 → `ignored`

- [ ] **Step 1: 写失败测试**

```ts
import { describe, it, expect } from 'vitest';
import { mapWebhook } from '../../src/evolution/webhook-mapper.js';

describe('mapWebhook', () => {
  it('connection open -> CONNECTED', () => {
    expect(mapWebhook({ event: 'connection.update', instance: 'acct-1', data: { state: 'open' } }))
      .toEqual({ kind: 'account_event', instanceName: 'acct-1', event: { type: 'CONNECTED' } });
  });

  it('connection close 401 -> LOGGED_OUT', () => {
    expect(mapWebhook({ event: 'connection.update', instance: 'acct-1', data: { state: 'close', statusReason: 401 } }))
      .toEqual({ kind: 'account_event', instanceName: 'acct-1', event: { type: 'LOGGED_OUT' } });
  });

  it('connection close other -> DISCONNECTED', () => {
    expect(mapWebhook({ event: 'connection.update', instance: 'acct-1', data: { state: 'close', statusReason: 500 } }))
      .toEqual({ kind: 'account_event', instanceName: 'acct-1', event: { type: 'DISCONNECTED' } });
  });

  it('connecting -> ignored', () => {
    expect(mapWebhook({ event: 'connection.update', instance: 'acct-1', data: { state: 'connecting' } }))
      .toEqual({ kind: 'ignored' });
  });

  it('messages.update SERVER_ACK -> SENT', () => {
    expect(mapWebhook({ event: 'messages.update', instance: 'acct-1', data: { keyId: 'M1', status: 'SERVER_ACK' } }))
      .toEqual({ kind: 'message_status', instanceName: 'acct-1', providerMessageId: 'M1', status: 'SENT' });
  });

  it('messages.update READ -> READ', () => {
    expect(mapWebhook({ event: 'messages.update', instance: 'acct-1', data: { keyId: 'M1', status: 'READ' } }))
      .toEqual({ kind: 'message_status', instanceName: 'acct-1', providerMessageId: 'M1', status: 'READ' });
  });

  it('unknown event -> ignored', () => {
    expect(mapWebhook({ event: 'presence.update', instance: 'acct-1', data: {} })).toEqual({ kind: 'ignored' });
  });
});
```

- [ ] **Step 2: 跑测试确认失败**

Run: `npm test tests/evolution/webhook-mapper.test.ts`
Expected: FAIL —「Cannot find module」

- [ ] **Step 3: 实现映射器**

```ts
import type { AccountEvent } from '../domain/account-state.js';
import type { MessageStatus } from '../domain/message.js';

export interface WebhookPayload {
  event: string;
  instance: string;
  data: Record<string, unknown>;
}

export type WebhookResult =
  | { kind: 'account_event'; instanceName: string; event: AccountEvent }
  | { kind: 'message_status'; instanceName: string; providerMessageId: string; status: MessageStatus }
  | { kind: 'ignored' };

const STATUS_MAP: Record<string, MessageStatus> = {
  SERVER_ACK: 'SENT',
  DELIVERY_ACK: 'DELIVERED',
  READ: 'READ',
  PLAYED: 'READ',
};

export function mapWebhook(p: WebhookPayload): WebhookResult {
  const event = p.event.toLowerCase();

  if (event === 'connection.update') {
    const state = p.data['state'];
    if (state === 'open') {
      return { kind: 'account_event', instanceName: p.instance, event: { type: 'CONNECTED' } };
    }
    if (state === 'close') {
      const reason = p.data['statusReason'];
      const type = reason === 401 ? 'LOGGED_OUT' : 'DISCONNECTED';
      return { kind: 'account_event', instanceName: p.instance, event: { type } };
    }
    return { kind: 'ignored' };
  }

  if (event === 'messages.update') {
    const pid = p.data['keyId'];
    const status = STATUS_MAP[String(p.data['status'])];
    if (typeof pid === 'string' && status) {
      return { kind: 'message_status', instanceName: p.instance, providerMessageId: pid, status };
    }
    return { kind: 'ignored' };
  }

  return { kind: 'ignored' };
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `npm test tests/evolution/webhook-mapper.test.ts`
Expected: PASS（7 passed）

- [ ] **Step 5: 提交**

```bash
git add p0-sending-unit/src/evolution/webhook-mapper.ts p0-sending-unit/tests/evolution/webhook-mapper.test.ts
git commit -m "feat(p0-1): pure webhook payload -> domain event mapper"
```

---

## Task 6: 账号服务编排

**Files:**
- Create: `p0-sending-unit/src/pool/account-service.ts`
- Test: `p0-sending-unit/tests/pool/account-service.test.ts`

**Interfaces:**
- Consumes: `transition`（Task 2）、`Account`/`Proxy`/`newInstanceName`（Task 3）、`Message`（Task 3）、`AccountRepository`/`MessageRepository`（Task 3）、`EvolutionClient`（Task 4）、`mapWebhook`/`WebhookPayload`（Task 5）
- Produces:
  - `interface IdGen { next(): string }`
  - `class AccountService` 构造 `{ accounts: AccountRepository; messages: MessageRepository; evolution: EvolutionClient; webhookUrl: string; ids: IdGen }`
    - `importAccount(phoneNumber: string): Promise<Account>`（状态 IMPORTED）
    - `bringOnline(accountId: string, proxy: Proxy): Promise<{ pairingCode: string | null }>`（BIND_PROXY→CONNECT，调 createInstance+connect）
    - `sendMessage(accountId: string, to: string, text: string): Promise<Message>`（要求状态 ONLINE，调 sendText，落 Message=SENT... 实为 PENDING→providerMessageId）
    - `handleWebhook(p: WebhookPayload): Promise<void>`（连接事件走 transition；DEAD 时清空 proxy 记 deadReason；消息事件更新 Message 状态）

说明：`sendMessage` 落库时 Message 初始 `status:'PENDING'`，写入 `providerMessageId`，真正的 SENT/DELIVERED/READ 由 webhook 异步回填。`bringOnline` 只把状态推进到 `CONNECTING`；真正 `ONLINE` 由 webhook 的 `CONNECTED` 事件驱动（真机配对后到达）。

- [ ] **Step 1: 写失败测试（用内存仓库 + 桩 Evolution）**

```ts
import { describe, it, expect, vi } from 'vitest';
import { AccountService } from '../../src/pool/account-service.js';
import { InMemoryAccountRepository } from '../../src/adapters/in-memory-account-repository.js';
import { InMemoryMessageRepository } from '../../src/adapters/in-memory-message-repository.js';
import type { Proxy } from '../../src/domain/account.js';

const proxy: Proxy = { host: '1.2.3.4', port: 8000, protocol: 'http' };

function makeIds() {
  let n = 0;
  return { next: () => `id-${++n}` };
}

function makeService() {
  const accounts = new InMemoryAccountRepository();
  const messages = new InMemoryMessageRepository();
  const evolution = {
    createInstance: vi.fn(async () => {}),
    connect: vi.fn(async () => ({ pairingCode: 'PAIR-1234' })),
    sendText: vi.fn(async () => ({ providerMessageId: 'PMID-1' })),
  };
  const svc = new AccountService({
    accounts, messages,
    evolution: evolution as never,
    webhookUrl: 'http://w/hook',
    ids: makeIds(),
  });
  return { svc, accounts, messages, evolution };
}

describe('AccountService', () => {
  it('importAccount creates an IMPORTED account with derived instance name', async () => {
    const { svc } = makeService();
    const a = await svc.importAccount('5511999999999');
    expect(a.state).toBe('IMPORTED');
    expect(a.instanceName).toBe('acct-5511999999999');
    expect(a.proxy).toBeNull();
  });

  it('bringOnline binds proxy, creates instance, connects, moves to CONNECTING', async () => {
    const { svc, accounts, evolution } = makeService();
    const a = await svc.importAccount('5511999999999');
    const r = await svc.bringOnline(a.id, proxy);

    expect(r.pairingCode).toBe('PAIR-1234');
    expect(evolution.createInstance).toHaveBeenCalledOnce();
    expect(evolution.connect).toHaveBeenCalledWith('acct-5511999999999');
    const saved = await accounts.findById(a.id);
    expect(saved!.state).toBe('CONNECTING');
    expect(saved!.proxy).toEqual(proxy);
  });

  it('sendMessage requires ONLINE state', async () => {
    const { svc } = makeService();
    const a = await svc.importAccount('5511999999999');
    await expect(svc.sendMessage(a.id, '5511888888888', 'oi')).rejects.toThrow(/ONLINE/);
  });

  it('CONNECTED webhook -> account ONLINE, then sendMessage persists PENDING message with providerMessageId', async () => {
    const { svc, messages, evolution } = makeService();
    const a = await svc.importAccount('5511999999999');
    await svc.bringOnline(a.id, proxy);
    await svc.handleWebhook({ event: 'connection.update', instance: 'acct-5511999999999', data: { state: 'open' } });

    const m = await svc.sendMessage(a.id, '5511888888888', 'oi');
    expect(evolution.sendText).toHaveBeenCalledWith({ instanceName: 'acct-5511999999999', to: '5511888888888', text: 'oi' });
    expect(m.status).toBe('PENDING');
    expect(m.providerMessageId).toBe('PMID-1');
    const found = await messages.findByProviderMessageId('PMID-1');
    expect(found!.id).toBe(m.id);
  });

  it('messages.update webhook updates message status', async () => {
    const { svc, messages } = makeService();
    const a = await svc.importAccount('5511999999999');
    await svc.bringOnline(a.id, proxy);
    await svc.handleWebhook({ event: 'connection.update', instance: 'acct-5511999999999', data: { state: 'open' } });
    await svc.sendMessage(a.id, '5511888888888', 'oi');

    await svc.handleWebhook({ event: 'messages.update', instance: 'acct-5511999999999', data: { keyId: 'PMID-1', status: 'DELIVERY_ACK' } });
    const found = await messages.findByProviderMessageId('PMID-1');
    expect(found!.status).toBe('DELIVERED');
  });

  it('LOGGED_OUT webhook -> account DEAD, proxy released, deadReason set (封号即弃)', async () => {
    const { svc, accounts } = makeService();
    const a = await svc.importAccount('5511999999999');
    await svc.bringOnline(a.id, proxy);
    await svc.handleWebhook({ event: 'connection.update', instance: 'acct-5511999999999', data: { state: 'open' } });
    await svc.handleWebhook({ event: 'connection.update', instance: 'acct-5511999999999', data: { state: 'close', statusReason: 401 } });

    const saved = await accounts.findById(a.id);
    expect(saved!.state).toBe('DEAD');
    expect(saved!.proxy).toBeNull();
    expect(saved!.deadReason).toBe('LOGGED_OUT');
  });
});
```

- [ ] **Step 2: 跑测试确认失败**

Run: `npm test tests/pool/account-service.test.ts`
Expected: FAIL —「Cannot find module」

- [ ] **Step 3: 实现服务**

```ts
import { transition } from '../domain/account-state.js';
import { newInstanceName, type Account, type Proxy } from '../domain/account.js';
import type { Message } from '../domain/message.js';
import type { AccountRepository } from '../ports/account-repository.js';
import type { MessageRepository } from '../ports/message-repository.js';
import type { EvolutionClient } from '../evolution/evolution-client.js';
import { mapWebhook, type WebhookPayload } from '../evolution/webhook-mapper.js';

export interface IdGen { next(): string }

export class AccountService {
  constructor(
    private deps: {
      accounts: AccountRepository;
      messages: MessageRepository;
      evolution: EvolutionClient;
      webhookUrl: string;
      ids: IdGen;
    },
  ) {}

  async importAccount(phoneNumber: string): Promise<Account> {
    const account: Account = {
      id: this.deps.ids.next(),
      phoneNumber,
      state: 'IMPORTED',
      instanceName: newInstanceName(phoneNumber),
      proxy: null,
      deadReason: null,
    };
    await this.deps.accounts.save(account);
    return account;
  }

  async bringOnline(accountId: string, proxy: Proxy): Promise<{ pairingCode: string | null }> {
    const account = await this.require(accountId);
    const bound: Account = { ...account, proxy, state: transition(account.state, { type: 'BIND_PROXY' }) };
    await this.deps.accounts.save(bound);

    await this.deps.evolution.createInstance({
      instanceName: bound.instanceName,
      number: bound.phoneNumber,
      proxy,
      webhookUrl: this.deps.webhookUrl,
    });
    const connectResult = await this.deps.evolution.connect(bound.instanceName);

    const connecting: Account = { ...bound, state: transition(bound.state, { type: 'CONNECT' }) };
    await this.deps.accounts.save(connecting);
    return connectResult;
  }

  async sendMessage(accountId: string, to: string, text: string): Promise<Message> {
    const account = await this.require(accountId);
    if (account.state !== 'ONLINE') {
      throw new Error(`Account ${accountId} not ONLINE (state=${account.state})`);
    }
    const { providerMessageId } = await this.deps.evolution.sendText({
      instanceName: account.instanceName, to, text,
    });
    const message: Message = {
      id: this.deps.ids.next(),
      accountId,
      to,
      providerMessageId,
      status: 'PENDING',
    };
    await this.deps.messages.save(message);
    return message;
  }

  async handleWebhook(payload: WebhookPayload): Promise<void> {
    const result = mapWebhook(payload);
    if (result.kind === 'ignored') return;

    if (result.kind === 'message_status') {
      const msg = await this.deps.messages.findByProviderMessageId(result.providerMessageId);
      if (msg) await this.deps.messages.updateStatus(msg.id, result.status);
      return;
    }

    const account = await this.deps.accounts.findByInstanceName(result.instanceName);
    if (!account) return;
    const nextState = transition(account.state, result.event);
    const updated: Account = {
      ...account,
      state: nextState,
      proxy: nextState === 'DEAD' ? null : account.proxy,
      deadReason: nextState === 'DEAD' ? result.event.type : account.deadReason,
    };
    await this.deps.accounts.save(updated);
  }

  private async require(accountId: string): Promise<Account> {
    const account = await this.deps.accounts.findById(accountId);
    if (!account) throw new Error(`Account not found: ${accountId}`);
    return account;
  }
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `npm test tests/pool/account-service.test.ts`
Expected: PASS（6 passed）

- [ ] **Step 5: 跑全量测试**

Run: `npm test`
Expected: PASS（全部 passed）

- [ ] **Step 6: 提交**

```bash
git add p0-sending-unit/src/pool/account-service.ts p0-sending-unit/tests/pool/account-service.test.ts
git commit -m "feat(p0-1): account service orchestration (import/online/send/webhook + 封号即弃)"
```

---

## Task 7: Prisma 持久化适配器

**Files:**
- Create: `p0-sending-unit/prisma/schema.prisma`
- Create: `p0-sending-unit/src/adapters/prisma-account-repository.ts`
- Create: `p0-sending-unit/src/adapters/prisma-message-repository.ts`
- Test: `p0-sending-unit/tests/adapters/prisma-account-repository.test.ts`

**Interfaces:**
- Consumes: `Account`/`Proxy`（Task 3）、`Message`/`MessageStatus`（Task 3）、`AccountRepository`/`MessageRepository`（Task 3）
- Produces: `class PrismaAccountRepository implements AccountRepository`、`class PrismaMessageRepository implements MessageRepository`（构造接收 `PrismaClient`）

> 此 Task 需要 Postgres：先 `docker compose up -d postgres`，再 `npx prisma migrate dev`。测试是集成测试，每个用例前清表。

- [ ] **Step 1: 写 prisma schema**

`prisma/schema.prisma`:
```prisma
generator client { provider = "prisma-client-js" }
datasource db { provider = "postgresql"; url = env("DATABASE_URL") }

model Account {
  id           String  @id
  phoneNumber  String
  state        String
  instanceName String  @unique
  proxyJson    Json?
  deadReason   String?
  messages     Message[]
}

model Message {
  id                String  @id
  accountId         String
  to                String
  providerMessageId String? @unique
  status            String
  account           Account @relation(fields: [accountId], references: [id])
}
```

- [ ] **Step 2: 生成 client 并迁移**

Run:
```bash
cd p0-sending-unit
docker compose up -d postgres
cp .env.example .env
npx prisma migrate dev --name init
```
Expected: 迁移成功，生成 `@prisma/client`。

- [ ] **Step 3: 写失败测试**

`tests/adapters/prisma-account-repository.test.ts`:
```ts
import { describe, it, expect, beforeEach, afterAll } from 'vitest';
import { PrismaClient } from '@prisma/client';
import { PrismaAccountRepository } from '../../src/adapters/prisma-account-repository.js';
import type { Account } from '../../src/domain/account.js';

const prisma = new PrismaClient();
const repo = new PrismaAccountRepository(prisma);

const acct: Account = {
  id: 'a1', phoneNumber: '5511999999999', state: 'CONNECTING',
  instanceName: 'acct-5511999999999',
  proxy: { host: '1.2.3.4', port: 8000, protocol: 'http' }, deadReason: null,
};

beforeEach(async () => {
  await prisma.message.deleteMany();
  await prisma.account.deleteMany();
});
afterAll(async () => { await prisma.$disconnect(); });

describe('PrismaAccountRepository', () => {
  it('saves and finds by id with proxy round-trip', async () => {
    await repo.save(acct);
    expect(await repo.findById('a1')).toEqual(acct);
  });

  it('finds by instance name', async () => {
    await repo.save(acct);
    expect(await repo.findByInstanceName('acct-5511999999999')).toEqual(acct);
  });

  it('upserts on repeated save (state change)', async () => {
    await repo.save(acct);
    await repo.save({ ...acct, state: 'DEAD', proxy: null, deadReason: 'LOGGED_OUT' });
    const found = await repo.findById('a1');
    expect(found!.state).toBe('DEAD');
    expect(found!.proxy).toBeNull();
    expect(found!.deadReason).toBe('LOGGED_OUT');
  });
});
```

- [ ] **Step 4: 跑测试确认失败**

Run: `npm test tests/adapters/prisma-account-repository.test.ts`
Expected: FAIL —「Cannot find module '../../src/adapters/prisma-account-repository.js'」

- [ ] **Step 5: 实现 Prisma 适配器**

`src/adapters/prisma-account-repository.ts`:
```ts
import type { PrismaClient } from '@prisma/client';
import type { Account, Proxy } from '../domain/account.js';
import type { AccountState } from '../domain/account-state.js';
import type { AccountRepository } from '../ports/account-repository.js';

interface Row {
  id: string; phoneNumber: string; state: string; instanceName: string;
  proxyJson: unknown; deadReason: string | null;
}

function toAccount(r: Row): Account {
  return {
    id: r.id,
    phoneNumber: r.phoneNumber,
    state: r.state as AccountState,
    instanceName: r.instanceName,
    proxy: (r.proxyJson as Proxy | null) ?? null,
    deadReason: r.deadReason,
  };
}

export class PrismaAccountRepository implements AccountRepository {
  constructor(private prisma: PrismaClient) {}

  async save(a: Account): Promise<void> {
    const data = {
      phoneNumber: a.phoneNumber,
      state: a.state,
      instanceName: a.instanceName,
      proxyJson: a.proxy ?? undefined,
      deadReason: a.deadReason,
    };
    await this.prisma.account.upsert({
      where: { id: a.id },
      create: { id: a.id, ...data },
      update: { ...data, proxyJson: a.proxy ?? null },
    });
  }

  async findById(id: string): Promise<Account | null> {
    const r = await this.prisma.account.findUnique({ where: { id } });
    return r ? toAccount(r as unknown as Row) : null;
  }

  async findByInstanceName(name: string): Promise<Account | null> {
    const r = await this.prisma.account.findUnique({ where: { instanceName: name } });
    return r ? toAccount(r as unknown as Row) : null;
  }
}
```

`src/adapters/prisma-message-repository.ts`:
```ts
import type { PrismaClient } from '@prisma/client';
import type { Message, MessageStatus } from '../domain/message.js';
import type { MessageRepository } from '../ports/message-repository.js';

export class PrismaMessageRepository implements MessageRepository {
  constructor(private prisma: PrismaClient) {}

  async save(m: Message): Promise<void> {
    await this.prisma.message.upsert({
      where: { id: m.id },
      create: { id: m.id, accountId: m.accountId, to: m.to, providerMessageId: m.providerMessageId, status: m.status },
      update: { status: m.status, providerMessageId: m.providerMessageId },
    });
  }

  async findByProviderMessageId(pid: string): Promise<Message | null> {
    const r = await this.prisma.message.findUnique({ where: { providerMessageId: pid } });
    return r
      ? { id: r.id, accountId: r.accountId, to: r.to, providerMessageId: r.providerMessageId, status: r.status as MessageStatus }
      : null;
  }

  async updateStatus(id: string, status: MessageStatus): Promise<void> {
    await this.prisma.message.update({ where: { id }, data: { status } });
  }
}
```

- [ ] **Step 6: 跑测试确认通过**

Run: `npm test tests/adapters/prisma-account-repository.test.ts`
Expected: PASS（3 passed）

- [ ] **Step 7: 提交**

```bash
git add p0-sending-unit/prisma p0-sending-unit/src/adapters/prisma-*.ts p0-sending-unit/tests/adapters/prisma-account-repository.test.ts
git commit -m "feat(p0-1): Prisma Postgres adapters for account & message repos"
```

---

## Task 8: Webhook 服务 + 组装 + 真机 E2E Runbook

**Files:**
- Create: `p0-sending-unit/src/webhook/webhook-server.ts`
- Create: `p0-sending-unit/src/index.ts`
- Create: `p0-sending-unit/E2E-RUNBOOK.md`
- Test: `p0-sending-unit/tests/webhook/webhook-server.test.ts`

**Interfaces:**
- Consumes: `AccountService`（Task 6）、`WebhookPayload`（Task 5）、`Config`（Task 1）、Prisma 适配器（Task 7）、`EvolutionClient`（Task 4）
- Produces: `buildWebhookServer(svc: AccountService): FastifyInstance`（`POST /hook` 收 Evolution webhook，200 返回 `{ ok: true }`）；`src/index.ts` 组装真实依赖并起服务

- [ ] **Step 1: 写失败测试（inject 方式验证 webhook 打到 service）**

`tests/webhook/webhook-server.test.ts`:
```ts
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
```

- [ ] **Step 2: 跑测试确认失败**

Run: `npm test tests/webhook/webhook-server.test.ts`
Expected: FAIL —「Cannot find module」

- [ ] **Step 3: 实现 webhook 服务**

`src/webhook/webhook-server.ts`:
```ts
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
```

- [ ] **Step 4: 跑测试确认通过**

Run: `npm test tests/webhook/webhook-server.test.ts`
Expected: PASS（1 passed）

- [ ] **Step 5: 实现组装入口 src/index.ts**

```ts
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
```

- [ ] **Step 6: 写真机 E2E Runbook**

`E2E-RUNBOOK.md`:
```markdown
# P0-1 真机端到端验证

前置：1 个真实协议号（已注册 WhatsApp）、1 个可用住宅代理、1 个已知在网的接收测试号。

1. 起依赖：`docker compose up -d`，`npx prisma migrate deploy`
2. 起控制服务：`npm run build && npm start`（监听 3000，Evolution 通过 host.docker.internal 回调）
3. 导号：调 `service.importAccount('<协议号>')`，记录返回 id
4. 上线：`service.bringOnline(id, { host, port, protocol:'http', username, password })`
   - 拿到 `pairingCode`，在该协议号的 WhatsApp「已连接的设备 → 连接设备 → 用号码连接」输入配对码
   - 观察控制台 webhook：应收到 `connection.update state=open`，DB 中账号变 `ONLINE`
5. 发送：`service.sendMessage(id, '<接收测试号>', 'teste de conexão')`
   - 接收号应收到消息；DB 中 Message 由 `PENDING`→`SENT`→`DELIVERED`（→`READ` 若已读）
6. 封号验证（可选）：在手机端主动登出该设备，应收到 `connection.update state=close 401`，DB 账号变 `DEAD`、proxy 清空、deadReason=LOGGED_OUT
7. 记录：本轮该号从上线到被封共发出多少条 → 这就是真实 N 值的第一个数据点，回填给 P0-3 养号试验。
```

- [ ] **Step 7: 跑全量测试**

Run: `npm test`
Expected: PASS（全部 passed）

- [ ] **Step 8: 提交**

```bash
git add p0-sending-unit/src/webhook p0-sending-unit/src/index.ts p0-sending-unit/E2E-RUNBOOK.md p0-sending-unit/tests/webhook/webhook-server.test.ts
git commit -m "feat(p0-1): Fastify webhook server, wiring entrypoint, real-device E2E runbook"
```

---

## 自查（Self-Review）

**Spec 覆盖度**：
- 号池状态机（导入/上线/掉线/封号即弃）→ Task 2 + Task 6 ✅
- 代理绑定 → Task 3（Proxy）+ Task 6（bringOnline 绑定、DEAD 释放）✅
- Evolution 发送（1v1）→ Task 4 + Task 6 ✅
- 回执/连接事件 → Task 5（mapper）+ Task 6（handleWebhook）+ Task 8（server）✅
- 封号即弃（proxy 释放 + deadReason）→ Task 6 测试覆盖 ✅
- 持久化 → Task 7 ✅
- 真机 E2E + 拿真实 N 数据点（喂给 P0-3）→ Task 8 Runbook ✅
- **明确不在本计划**：巴西号码规范化、在网筛号（P0-2）；养号自动化（P0-3）；批量投放/控频（P0-4）。

**占位符扫描**：无 TBD/TODO；每个改码步骤都有完整代码。

**类型一致性**：`transition`、`AccountState`/`AccountEvent`、`Account`/`Proxy`、`Message`/`MessageStatus`、`AccountRepository`/`MessageRepository`、`EvolutionClient.{createInstance,connect,sendText}`、`mapWebhook`/`WebhookResult`、`AccountService.{importAccount,bringOnline,sendMessage,handleWebhook}` 在各 Task 间签名一致。

**已知取舍**：Evolution v2 具体 payload/webhook 字段可能随小版本微调；客户端刻意做薄，真机 Runbook（Task 8）是校准这些字段的关口，若字段不符在 Task 4/5 就地调整。
