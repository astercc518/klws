# P0-2 筛号服务 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax.

**Goal:** 把一批原始号码 → 巴西 +55 规范化 → 去重 → Evolution 在网检测 → 只把"确认注册 WhatsApp"的号落为可发送目标；无效/不在网号明确标记，保护主发送号寿命。

**Architecture:** 延续 P0-1 的六边形结构（纯域逻辑 + 端口 + 适配器 + 注入式 Evolution 客户端）。新增：纯函数 `normalizeBrPhone`、`TargetNumber` 域实体与仓库、`EvolutionClient.checkNumbers`、`ScreeningService` 编排。筛号用的检测号与主发送号隔离（由调用方传入检测实例名）。

**Tech Stack:** 同 P0-1 —— TypeScript strict · Node 20 · Prisma/Postgres · Vitest · Evolution API v2。代码位于既有 `p0-sending-unit/`。

## Global Constraints

- 沿用 P0-1 全部约束：Node ≥ 20、TS `strict:true` 且无 `any`（测试桩除外）、所有 I/O 走注入接口、Evolution 固定 v2、频繁提交。
- `normalizeBrPhone` 是纯函数，无副作用；无效输入返回 `{ok:false, reason}`，绝不抛。
- 筛号只读/写 `TargetNumber`，不碰 P0-1 的 Account/Message 逻辑。
- Evolution 在网检测端点的确切线格（路径/字段）随 v2 小版本可能变动；客户端做薄，E2E runbook 为校准关口（同 P0-1 处理方式）。
- 内存适配器与 Prisma 适配器必须有相同可观察语义（去重、状态更新）。

---

## 文件结构

```
p0-sending-unit/
  src/
    screening/
      br-phone.ts              # normalizeBrPhone（纯）
      screening-service.ts     # 编排：import/runScreening
    domain/
      target-number.ts         # TargetNumber 实体 + TargetStatus（纯）
    ports/
      target-number-repository.ts
    adapters/
      in-memory-target-number-repository.ts
      prisma-target-number-repository.ts
    evolution/
      evolution-client.ts      # 追加 checkNumbers 方法（修改）
      evolution-types.ts       # 追加 CheckNumbersResponse 类型（修改）
    index.ts                   # 追加 CLI: screen-import / screen-run（修改）
  prisma/schema.prisma         # 追加 TargetNumber model（修改）
  tests/
    screening/br-phone.test.ts
    domain/target-number.test.ts
    evolution/evolution-client-check.test.ts
    screening/screening-service.test.ts
    adapters/prisma-target-number-repository.test.ts
  E2E-RUNBOOK.md               # 追加筛号章节（修改）
```

---

## Task 1: 巴西 +55 号码规范化（纯逻辑）

**Files:**
- Create: `p0-sending-unit/src/screening/br-phone.ts`
- Test: `p0-sending-unit/tests/screening/br-phone.test.ts`

**Interfaces:**
- Consumes: 无
- Produces: `type NormalizeResult = { ok: true; e164: string } | { ok: false; reason: string }`；`normalizeBrPhone(input: string): NormalizeResult`

规则（按顺序）：
1. 删除所有非数字字符。
2. 去掉前导国际拨号前缀 `00`（若有）。
3. 若不以 `55` 开头：仅当剩余长度为 10 或 11（DDD2 + 8/9）时前置 `55`；否则 `{ok:false, reason:'no country code'}`。
4. 取国内号 = `55` 之后的数字。长度必须为 10 或 11，否则 `{ok:false, reason:'bad length'}`。
5. DDD = 国内号前 2 位，必须 ≥ 11（首位 1-9），否则 `{ok:false, reason:'bad DDD'}`。
6. **第九位**：若国内号为 10 位（DDD + 8）且订户首位 ∈ {6,7,8,9}（移动号段），在 DDD 后插入 `9` 补成 11 位；订户首位 2-5（固话）保持 10 位。
7. 返回 `{ok:true, e164:'55'+国内号}`。

- [ ] **Step 1: 写失败测试**

```ts
import { describe, it, expect } from 'vitest';
import { normalizeBrPhone } from '../../src/screening/br-phone.js';

describe('normalizeBrPhone', () => {
  it('strips formatting and keeps a full mobile with 55 + DDD + 9 digits', () => {
    expect(normalizeBrPhone('+55 (11) 98765-4321')).toEqual({ ok: true, e164: '5511987654321' });
  });

  it('prepends 55 when given a domestic 11-digit mobile', () => {
    expect(normalizeBrPhone('11987654321')).toEqual({ ok: true, e164: '5511987654321' });
  });

  it('inserts the ninth digit for an 8-digit mobile (subscriber starts 6-9)', () => {
    // 55 + DDD 11 + 8-digit mobile 87654321 -> insert 9 -> 5511987654321
    expect(normalizeBrPhone('551187654321')).toEqual({ ok: true, e164: '5511987654321' });
  });

  it('keeps a landline (subscriber starts 2-5) at 10 national digits', () => {
    expect(normalizeBrPhone('551132654321')).toEqual({ ok: true, e164: '551132654321' });
  });

  it('drops a leading 00 international prefix', () => {
    expect(normalizeBrPhone('005511987654321')).toEqual({ ok: true, e164: '5511987654321' });
  });

  it('rejects too-short input', () => {
    expect(normalizeBrPhone('12345')).toEqual({ ok: false, reason: 'no country code' });
  });

  it('rejects a bad DDD (< 11)', () => {
    // 55 + DDD 09 + 9 digits -> DDD 09 invalid
    expect(normalizeBrPhone('5509987654321')).toEqual({ ok: false, reason: 'bad DDD' });
  });

  it('rejects wrong national length after 55', () => {
    expect(normalizeBrPhone('55119')).toEqual({ ok: false, reason: 'bad length' });
  });
});
```

- [ ] **Step 2: 跑测试确认失败**

Run: `npm test tests/screening/br-phone.test.ts`
Expected: FAIL —「Cannot find module」

- [ ] **Step 3: 实现**

```ts
export type NormalizeResult = { ok: true; e164: string } | { ok: false; reason: string };

export function normalizeBrPhone(input: string): NormalizeResult {
  let digits = input.replace(/\D/g, '');
  if (digits.startsWith('00')) digits = digits.slice(2);

  if (!digits.startsWith('55')) {
    if (digits.length === 10 || digits.length === 11) {
      digits = `55${digits}`;
    } else {
      return { ok: false, reason: 'no country code' };
    }
  }

  let national = digits.slice(2);
  if (national.length !== 10 && national.length !== 11) {
    return { ok: false, reason: 'bad length' };
  }

  const ddd = national.slice(0, 2);
  if (Number(ddd) < 11) {
    return { ok: false, reason: 'bad DDD' };
  }

  if (national.length === 10) {
    const firstSubscriber = national.charAt(2);
    if ('6789'.includes(firstSubscriber)) {
      national = `${ddd}9${national.slice(2)}`;
    }
  }

  return { ok: true, e164: `55${national}` };
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `npm test tests/screening/br-phone.test.ts`
Expected: PASS（8 passed）

- [ ] **Step 5: 提交**

```bash
git add p0-sending-unit/src/screening/br-phone.ts p0-sending-unit/tests/screening/br-phone.test.ts
git commit -m "feat(p0-2): Brazil +55 phone normalization with ninth-digit rule"
```

---

## Task 2: TargetNumber 实体 + 仓库端口 + 内存实现

**Files:**
- Create: `p0-sending-unit/src/domain/target-number.ts`
- Create: `p0-sending-unit/src/ports/target-number-repository.ts`
- Create: `p0-sending-unit/src/adapters/in-memory-target-number-repository.ts`
- Test: `p0-sing-unit/tests/domain/target-number.test.ts` → 用 `p0-sending-unit/tests/domain/target-number.test.ts`

**Interfaces:**
- Consumes: 无
- Produces:
  - `type TargetStatus = 'PENDING_CHECK' | 'ON_WHATSAPP' | 'NOT_ON_WHATSAPP' | 'INVALID'`
  - `interface TargetNumber { id: string; raw: string; e164: string | null; status: TargetStatus; jid: string | null }`
  - `interface TargetNumberRepository { save(t: TargetNumber): Promise<void>; findByE164(e164: string): Promise<TargetNumber | null>; listByStatus(status: TargetStatus, limit: number): Promise<TargetNumber[]> }`
  - `class InMemoryTargetNumberRepository implements TargetNumberRepository`

- [ ] **Step 1: 写失败测试**

```ts
import { describe, it, expect } from 'vitest';
import { InMemoryTargetNumberRepository } from '../../src/adapters/in-memory-target-number-repository.js';
import type { TargetNumber } from '../../src/domain/target-number.js';

function t(id: string, e164: string | null, status: TargetNumber['status']): TargetNumber {
  return { id, raw: `raw-${id}`, e164, status, jid: null };
}

describe('TargetNumber in-memory repo', () => {
  it('saves and finds by e164', async () => {
    const repo = new InMemoryTargetNumberRepository();
    const a = t('1', '5511987654321', 'PENDING_CHECK');
    await repo.save(a);
    expect(await repo.findByE164('5511987654321')).toEqual(a);
    expect(await repo.findByE164('0000')).toBeNull();
  });

  it('findByE164 ignores rows with null e164', async () => {
    const repo = new InMemoryTargetNumberRepository();
    await repo.save(t('2', null, 'INVALID'));
    expect(await repo.findByE164('5511987654321')).toBeNull();
  });

  it('listByStatus returns matching rows up to limit', async () => {
    const repo = new InMemoryTargetNumberRepository();
    await repo.save(t('1', '5511111111111', 'PENDING_CHECK'));
    await repo.save(t('2', '5511222222222', 'PENDING_CHECK'));
    await repo.save(t('3', '5511333333333', 'ON_WHATSAPP'));
    const pending = await repo.listByStatus('PENDING_CHECK', 10);
    expect(pending.map((r) => r.id).sort()).toEqual(['1', '2']);
    expect(await repo.listByStatus('PENDING_CHECK', 1)).toHaveLength(1);
  });
});
```

- [ ] **Step 2: 跑测试确认失败**

Run: `npm test tests/domain/target-number.test.ts`
Expected: FAIL —「Cannot find module」

- [ ] **Step 3: 实现实体**

`src/domain/target-number.ts`:
```ts
export type TargetStatus = 'PENDING_CHECK' | 'ON_WHATSAPP' | 'NOT_ON_WHATSAPP' | 'INVALID';

export interface TargetNumber {
  id: string;
  raw: string;
  e164: string | null;
  status: TargetStatus;
  jid: string | null;
}
```

- [ ] **Step 4: 实现端口**

`src/ports/target-number-repository.ts`:
```ts
import type { TargetNumber, TargetStatus } from '../domain/target-number.js';

export interface TargetNumberRepository {
  save(t: TargetNumber): Promise<void>;
  findByE164(e164: string): Promise<TargetNumber | null>;
  listByStatus(status: TargetStatus, limit: number): Promise<TargetNumber[]>;
}
```

- [ ] **Step 5: 实现内存适配器**

`src/adapters/in-memory-target-number-repository.ts`:
```ts
import type { TargetNumber, TargetStatus } from '../domain/target-number.js';
import type { TargetNumberRepository } from '../ports/target-number-repository.js';

export class InMemoryTargetNumberRepository implements TargetNumberRepository {
  private byId = new Map<string, TargetNumber>();

  async save(t: TargetNumber): Promise<void> {
    this.byId.set(t.id, { ...t });
  }

  async findByE164(e164: string): Promise<TargetNumber | null> {
    for (const t of this.byId.values()) {
      if (t.e164 !== null && t.e164 === e164) return { ...t };
    }
    return null;
  }

  async listByStatus(status: TargetStatus, limit: number): Promise<TargetNumber[]> {
    const out: TargetNumber[] = [];
    for (const t of this.byId.values()) {
      if (t.status === status) out.push({ ...t });
      if (out.length >= limit) break;
    }
    return out;
  }
}
```

- [ ] **Step 6: 跑测试确认通过**

Run: `npm test tests/domain/target-number.test.ts`
Expected: PASS（3 passed）

- [ ] **Step 7: 提交**

```bash
git add p0-sending-unit/src/domain/target-number.ts p0-sending-unit/src/ports/target-number-repository.ts p0-sending-unit/src/adapters/in-memory-target-number-repository.ts p0-sending-unit/tests/domain/target-number.test.ts
git commit -m "feat(p0-2): TargetNumber entity, repository port, in-memory adapter"
```

---

## Task 3: Evolution 在网检测客户端方法

**Files:**
- Modify: `p0-sending-unit/src/evolution/evolution-types.ts`
- Modify: `p0-sending-unit/src/evolution/evolution-client.ts`
- Test: `p0-sending-unit/tests/evolution/evolution-client-check.test.ts`

**Interfaces:**
- Consumes: 现有 `EvolutionClient`（P0-1）、注入式 `fetchFn`
- Produces: `EvolutionClient.checkNumbers(p: { instanceName: string; numbers: string[] }): Promise<Array<{ number: string; exists: boolean; jid: string | null }>>`

Evolution v2 端点：`POST /chat/whatsappNumbers/{instance}`，body `{ numbers: string[] }`，返回数组，每项含 `{ number, exists, jid }`（字段名以 v2 为准，E2E 校准）。

- [ ] **Step 1: 写失败测试（桩 fetch）**

```ts
import { describe, it, expect } from 'vitest';
import { EvolutionClient } from '../../src/evolution/evolution-client.js';

function stub(status: number, body: unknown) {
  const calls: { url: string; init: RequestInit }[] = [];
  const fetchFn = (async (url: string | URL, init?: RequestInit) => {
    calls.push({ url: String(url), init: init ?? {} });
    return new Response(JSON.stringify(body), { status });
  }) as unknown as typeof fetch;
  return { fetchFn, calls };
}

describe('EvolutionClient.checkNumbers', () => {
  it('posts numbers to /chat/whatsappNumbers/{instance} and maps the response', async () => {
    const { fetchFn, calls } = stub(200, [
      { number: '5511987654321', exists: true, jid: '5511987654321@s.whatsapp.net' },
      { number: '5511000000000', exists: false, jid: null },
    ]);
    const c = new EvolutionClient({ baseUrl: 'http://e:8080', apiKey: 'k', fetchFn });
    const res = await c.checkNumbers({ instanceName: 'checker-1', numbers: ['5511987654321', '5511000000000'] });

    expect(calls[0]!.url).toBe('http://e:8080/chat/whatsappNumbers/checker-1');
    const sent = JSON.parse(calls[0]!.init.body as string);
    expect(sent.numbers).toEqual(['5511987654321', '5511000000000']);
    expect((calls[0]!.init.headers as Record<string, string>)['apikey']).toBe('k');
    expect(res).toEqual([
      { number: '5511987654321', exists: true, jid: '5511987654321@s.whatsapp.net' },
      { number: '5511000000000', exists: false, jid: null },
    ]);
  });

  it('coerces missing jid to null', async () => {
    const { fetchFn } = stub(200, [{ number: '5511987654321', exists: true }]);
    const c = new EvolutionClient({ baseUrl: 'http://e:8080', apiKey: 'k', fetchFn });
    const res = await c.checkNumbers({ instanceName: 'checker-1', numbers: ['5511987654321'] });
    expect(res).toEqual([{ number: '5511987654321', exists: true, jid: null }]);
  });
});
```

- [ ] **Step 2: 跑测试确认失败**

Run: `npm test tests/evolution/evolution-client-check.test.ts`
Expected: FAIL —「checkNumbers is not a function」

- [ ] **Step 3: 追加类型**（编辑 `evolution-types.ts`，在文件末尾追加）

```ts
export interface CheckNumberEntry { number?: string; exists?: boolean; jid?: string | null }
export type CheckNumbersResponse = CheckNumberEntry[];
```

- [ ] **Step 4: 追加客户端方法**（编辑 `evolution-client.ts`）

在 `evolution-client.ts` 顶部 import 处追加类型引用：
```ts
import type { CreateInstancePayload, ConnectResponse, SendTextResponse, CheckNumbersResponse } from './evolution-types.js';
```
（若原来只 import 了前三个，替换为上面这行。）

在 `EvolutionClient` 类内、`sendText` 方法之后追加：
```ts
  async checkNumbers(p: { instanceName: string; numbers: string[] }): Promise<Array<{ number: string; exists: boolean; jid: string | null }>> {
    const r = await this.request<CheckNumbersResponse>('POST', `/chat/whatsappNumbers/${p.instanceName}`, {
      numbers: p.numbers,
    });
    return r.map((e) => ({
      number: e.number ?? '',
      exists: e.exists === true,
      jid: e.jid ?? null,
    }));
  }
```

- [ ] **Step 5: 跑测试确认通过**

Run: `npm test tests/evolution/evolution-client-check.test.ts`
Expected: PASS（2 passed）；再跑 `node_modules/.bin/tsc --noEmit -p tsconfig.json` 确认干净。

- [ ] **Step 6: 提交**

```bash
git add p0-sending-unit/src/evolution/evolution-types.ts p0-sending-unit/src/evolution/evolution-client.ts p0-sending-unit/tests/evolution/evolution-client-check.test.ts
git commit -m "feat(p0-2): EvolutionClient.checkNumbers (on-WhatsApp existence check)"
```

---

## Task 4: 筛号编排服务

**Files:**
- Create: `p0-sending-unit/src/screening/screening-service.ts`
- Test: `p0-sending-unit/tests/screening/screening-service.test.ts`

**Interfaces:**
- Consumes: `normalizeBrPhone`（Task 1）、`TargetNumber`/`TargetStatus`（Task 2）、`TargetNumberRepository`（Task 2）、`EvolutionClient.checkNumbers`（Task 3）
- Produces:
  - `interface IdGen { next(): string }`
  - `class ScreeningService` 构造 `{ targets: TargetNumberRepository; evolution: Pick<EvolutionClient, 'checkNumbers'>; ids: IdGen }`
    - `importNumbers(raws: string[]): Promise<{ imported: number; invalid: number; duplicates: number }>`
      —— 逐个 `normalizeBrPhone`；失败→存 INVALID(e164 null)记 invalid；成功→按 e164 去重（已存在则记 duplicate 不重存）；新号存 PENDING_CHECK。
    - `runScreening(checkerInstanceName: string, batchSize: number): Promise<{ checked: number; onWhatsApp: number; notOnWhatsApp: number }>`
      —— 取 PENDING_CHECK 一批（≤batchSize）→ `checkNumbers` → 按 e164 匹配结果，exists→ON_WHATSAPP(带 jid)，否则→NOT_ON_WHATSAPP；未在结果中的号保持 PENDING（不误改）。

- [ ] **Step 1: 写失败测试**

```ts
import { describe, it, expect, vi } from 'vitest';
import { ScreeningService } from '../../src/screening/screening-service.js';
import { InMemoryTargetNumberRepository } from '../../src/adapters/in-memory-target-number-repository.js';

function makeIds() {
  let n = 0;
  return { next: () => `id-${++n}` };
}

describe('ScreeningService', () => {
  it('importNumbers normalizes, marks invalid, and dedupes by e164', async () => {
    const targets = new InMemoryTargetNumberRepository();
    const svc = new ScreeningService({ targets, evolution: { checkNumbers: vi.fn() } as never, ids: makeIds() });

    const r = await svc.importNumbers(['+55 (11) 98765-4321', '11987654321', '12345']);
    // first two normalize to the same 5511987654321 -> 1 imported + 1 duplicate; '12345' invalid
    expect(r).toEqual({ imported: 1, invalid: 1, duplicates: 1 });

    const found = await targets.findByE164('5511987654321');
    expect(found!.status).toBe('PENDING_CHECK');
  });

  it('runScreening checks a pending batch and updates statuses by e164', async () => {
    const targets = new InMemoryTargetNumberRepository();
    const checkNumbers = vi.fn(async () => [
      { number: '5511987654321', exists: true, jid: '5511987654321@s.whatsapp.net' },
      { number: '5511222222222', exists: false, jid: null },
    ]);
    const svc = new ScreeningService({ targets, evolution: { checkNumbers } as never, ids: makeIds() });
    await svc.importNumbers(['5511987654321', '5511222222222']);

    const r = await svc.runScreening('checker-1', 100);
    expect(r).toEqual({ checked: 2, onWhatsApp: 1, notOnWhatsApp: 1 });
    expect(checkNumbers).toHaveBeenCalledWith({ instanceName: 'checker-1', numbers: expect.arrayContaining(['5511987654321', '5511222222222']) });

    const on = await targets.findByE164('5511987654321');
    expect(on!.status).toBe('ON_WHATSAPP');
    expect(on!.jid).toBe('5511987654321@s.whatsapp.net');
    const off = await targets.findByE164('5511222222222');
    expect(off!.status).toBe('NOT_ON_WHATSAPP');
  });

  it('runScreening with no pending numbers is a no-op', async () => {
    const targets = new InMemoryTargetNumberRepository();
    const checkNumbers = vi.fn(async () => []);
    const svc = new ScreeningService({ targets, evolution: { checkNumbers } as never, ids: makeIds() });
    const r = await svc.runScreening('checker-1', 100);
    expect(r).toEqual({ checked: 0, onWhatsApp: 0, notOnWhatsApp: 0 });
    expect(checkNumbers).not.toHaveBeenCalled();
  });
});
```

- [ ] **Step 2: 跑测试确认失败**

Run: `npm test tests/screening/screening-service.test.ts`
Expected: FAIL —「Cannot find module」

- [ ] **Step 3: 实现**

```ts
import { normalizeBrPhone } from './br-phone.js';
import type { TargetNumber } from '../domain/target-number.js';
import type { TargetNumberRepository } from '../ports/target-number-repository.js';
import type { EvolutionClient } from '../evolution/evolution-client.js';

export interface IdGen { next(): string }

export class ScreeningService {
  constructor(
    private deps: {
      targets: TargetNumberRepository;
      evolution: Pick<EvolutionClient, 'checkNumbers'>;
      ids: IdGen;
    },
  ) {}

  async importNumbers(raws: string[]): Promise<{ imported: number; invalid: number; duplicates: number }> {
    let imported = 0;
    let invalid = 0;
    let duplicates = 0;

    for (const raw of raws) {
      const norm = normalizeBrPhone(raw);
      if (!norm.ok) {
        await this.deps.targets.save({
          id: this.deps.ids.next(), raw, e164: null, status: 'INVALID', jid: null,
        });
        invalid += 1;
        continue;
      }
      const existing = await this.deps.targets.findByE164(norm.e164);
      if (existing) {
        duplicates += 1;
        continue;
      }
      await this.deps.targets.save({
        id: this.deps.ids.next(), raw, e164: norm.e164, status: 'PENDING_CHECK', jid: null,
      });
      imported += 1;
    }

    return { imported, invalid, duplicates };
  }

  async runScreening(checkerInstanceName: string, batchSize: number): Promise<{ checked: number; onWhatsApp: number; notOnWhatsApp: number }> {
    const pending = await this.deps.targets.listByStatus('PENDING_CHECK', batchSize);
    if (pending.length === 0) {
      return { checked: 0, onWhatsApp: 0, notOnWhatsApp: 0 };
    }

    const numbers = pending.map((p) => p.e164).filter((e): e is string => e !== null);
    const results = await this.deps.evolution.checkNumbers({ instanceName: checkerInstanceName, numbers });
    const byNumber = new Map(results.map((r) => [r.number, r]));

    let onWhatsApp = 0;
    let notOnWhatsApp = 0;
    let checked = 0;

    for (const target of pending) {
      if (target.e164 === null) continue;
      const result = byNumber.get(target.e164);
      if (!result) continue;
      checked += 1;
      const updated: TargetNumber = result.exists
        ? { ...target, status: 'ON_WHATSAPP', jid: result.jid }
        : { ...target, status: 'NOT_ON_WHATSAPP', jid: null };
      await this.deps.targets.save(updated);
      if (result.exists) onWhatsApp += 1; else notOnWhatsApp += 1;
    }

    return { checked, onWhatsApp, notOnWhatsApp };
  }
}
```

- [ ] **Step 4: 跑测试确认通过 + 全量**

Run: `npm test tests/screening/screening-service.test.ts` → PASS（3 passed）
Run: `npm test` → 全部通过
Run: `node_modules/.bin/tsc --noEmit -p tsconfig.json` → 干净

- [ ] **Step 5: 提交**

```bash
git add p0-sending-unit/src/screening/screening-service.ts p0-sending-unit/tests/screening/screening-service.test.ts
git commit -m "feat(p0-2): ScreeningService (import/normalize/dedupe/on-WhatsApp screen)"
```

---

## Task 5: TargetNumber 的 Prisma 适配器

**Files:**
- Modify: `p0-sending-unit/prisma/schema.prisma`
- Create: `p0-sending-unit/src/adapters/prisma-target-number-repository.ts`
- Test: `p0-sending-unit/tests/adapters/prisma-target-number-repository.test.ts`

**Interfaces:**
- Consumes: `TargetNumber`/`TargetStatus`、`TargetNumberRepository`
- Produces: `class PrismaTargetNumberRepository implements TargetNumberRepository`（构造接收 `PrismaClient`）

> 需 Postgres：`docker compose up -d postgres`（就绪后），`npx prisma migrate dev --name target_number`。测试每例前清表。

- [ ] **Step 1: 追加 schema model**（在 `prisma/schema.prisma` 末尾追加）

```prisma
model TargetNumber {
  id     String  @id
  raw    String
  e164   String? @unique
  status String
  jid    String?
}
```

- [ ] **Step 2: 迁移**

Run（从 `p0-sending-unit/`，Postgres 就绪后）:
```bash
npx prisma migrate dev --name target_number
```
Expected: 迁移成功并重新生成 client。

- [ ] **Step 3: 写失败测试**

```ts
import { describe, it, expect, beforeEach, afterAll } from 'vitest';
import { PrismaClient } from '@prisma/client';
import { PrismaTargetNumberRepository } from '../../src/adapters/prisma-target-number-repository.js';
import type { TargetNumber } from '../../src/domain/target-number.js';

const prisma = new PrismaClient();
const repo = new PrismaTargetNumberRepository(prisma);

beforeEach(async () => { await prisma.targetNumber.deleteMany(); });
afterAll(async () => { await prisma.$disconnect(); });

const row: TargetNumber = { id: 'n1', raw: '+55 11 98765-4321', e164: '5511987654321', status: 'PENDING_CHECK', jid: null };

describe('PrismaTargetNumberRepository', () => {
  it('saves and finds by e164', async () => {
    await repo.save(row);
    expect(await repo.findByE164('5511987654321')).toEqual(row);
    expect(await repo.findByE164('0000')).toBeNull();
  });

  it('upserts status + jid on repeated save', async () => {
    await repo.save(row);
    await repo.save({ ...row, status: 'ON_WHATSAPP', jid: '5511987654321@s.whatsapp.net' });
    const found = await repo.findByE164('5511987654321');
    expect(found!.status).toBe('ON_WHATSAPP');
    expect(found!.jid).toBe('5511987654321@s.whatsapp.net');
  });

  it('listByStatus filters and respects limit', async () => {
    await repo.save({ ...row, id: 'a', e164: '5511111111111', status: 'PENDING_CHECK' });
    await repo.save({ ...row, id: 'b', e164: '5511222222222', status: 'PENDING_CHECK' });
    await repo.save({ ...row, id: 'c', e164: '5511333333333', status: 'ON_WHATSAPP' });
    expect(await repo.listByStatus('PENDING_CHECK', 10)).toHaveLength(2);
    expect(await repo.listByStatus('PENDING_CHECK', 1)).toHaveLength(1);
  });

  it('saves a row with null e164 (invalid number)', async () => {
    await repo.save({ id: 'x', raw: 'junk', e164: null, status: 'INVALID', jid: null });
    expect(await repo.listByStatus('INVALID', 10)).toHaveLength(1);
  });
});
```

- [ ] **Step 4: 跑测试确认失败**

Run: `npm test tests/adapters/prisma-target-number-repository.test.ts`
Expected: FAIL —「Cannot find module '../../src/adapters/prisma-target-number-repository.js'」

- [ ] **Step 5: 实现**

`src/adapters/prisma-target-number-repository.ts`:
```ts
import type { PrismaClient } from '@prisma/client';
import type { TargetNumber, TargetStatus } from '../domain/target-number.js';
import type { TargetNumberRepository } from '../ports/target-number-repository.js';

interface Row { id: string; raw: string; e164: string | null; status: string; jid: string | null }

function toTarget(r: Row): TargetNumber {
  return { id: r.id, raw: r.raw, e164: r.e164, status: r.status as TargetStatus, jid: r.jid };
}

export class PrismaTargetNumberRepository implements TargetNumberRepository {
  constructor(private prisma: PrismaClient) {}

  async save(t: TargetNumber): Promise<void> {
    const data = { raw: t.raw, e164: t.e164, status: t.status, jid: t.jid };
    await this.prisma.targetNumber.upsert({
      where: { id: t.id },
      create: { id: t.id, ...data },
      update: data,
    });
  }

  async findByE164(e164: string): Promise<TargetNumber | null> {
    const r = await this.prisma.targetNumber.findUnique({ where: { e164 } });
    return r ? toTarget(r as unknown as Row) : null;
  }

  async listByStatus(status: TargetStatus, limit: number): Promise<TargetNumber[]> {
    const rows = await this.prisma.targetNumber.findMany({ where: { status }, take: limit });
    return rows.map((r) => toTarget(r as unknown as Row));
  }
}
```

- [ ] **Step 6: 跑测试确认通过 + 全量 + tsc**

Run: `npm test tests/adapters/prisma-target-number-repository.test.ts` → PASS（4 passed）
Run: `npm test` → 全部通过
Run: `node_modules/.bin/tsc --noEmit -p tsconfig.json` → 干净

- [ ] **Step 7: 提交**

```bash
git add p0-sending-unit/prisma/schema.prisma p0-sending-unit/prisma/migrations p0-sending-unit/src/adapters/prisma-target-number-repository.ts p0-sending-unit/tests/adapters/prisma-target-number-repository.test.ts
git commit -m "feat(p0-2): Prisma TargetNumber adapter + migration"
```

---

## Task 6: CLI 接线 + E2E Runbook 筛号章节

**Files:**
- Modify: `p0-sending-unit/src/index.ts`
- Modify: `p0-sending-unit/E2E-RUNBOOK.md`
- Test: 无新单测（接线层，tsc 为门槛；筛号逻辑已被 Task 1-5 覆盖）

**Interfaces:**
- Consumes: `ScreeningService`（Task 4）、`PrismaTargetNumberRepository`（Task 5）、现有 `EvolutionClient`、`randomUUID`

- [ ] **Step 1: 在 index.ts 组装 ScreeningService 并加 CLI 命令**

在 `src/index.ts` 的 import 区追加：
```ts
import { PrismaTargetNumberRepository } from './adapters/prisma-target-number-repository.js';
import { ScreeningService } from './screening/screening-service.js';
```

在 `service`（AccountService 实例）定义之后追加：
```ts
const screening = new ScreeningService({
  targets: new PrismaTargetNumberRepository(prisma),
  evolution,
  ids: { next: () => randomUUID() },
});
```

在 `runCli` 的命令分支里，`send` 分支之后、`else` 之前追加：
```ts
    } else if (cmd === 'screen-import') {
      const nums = rest;
      if (nums.length === 0) throw new Error('usage: screen-import <num> [num...]');
      console.log(JSON.stringify(await screening.importNumbers(nums)));
    } else if (cmd === 'screen-run') {
      const [checker, batch] = rest;
      if (!checker) throw new Error('usage: screen-run <checkerInstanceName> [batchSize]');
      console.log(JSON.stringify(await screening.runScreening(checker, batch ? Number(batch) : 100)));
```

同时把顶部命令用法注释补充这两条（在既有 CLI 注释块内追加）：
```ts
//   node dist/index.js screen-import <num> [num...]
//   node dist/index.js screen-run <checkerInstanceName> [batchSize]
```

并把 `export { service }` 改为 `export { service, screening }`。

- [ ] **Step 2: 验证全项目编译**

Run（从 `p0-sending-unit/`）: `node_modules/.bin/tsc --noEmit -p tsconfig.json`
Expected: 干净（index.ts 接入真实 ScreeningService + Prisma 适配器）。

- [ ] **Step 3: 追加 E2E Runbook 筛号章节**（在 `E2E-RUNBOOK.md` 末尾追加）

```markdown
## 筛号（P0-2）真机验证

前置：一个已上线的"检测号"（专用于筛号，与主发送号隔离），其 Evolution 实例名记为 <checker>。

1. 导入原始号码（可来自号码库）：
   `node dist/index.js screen-import 1187654321 +55 11 91234-5678 5511222222222`
   - 输出 `{imported, invalid, duplicates}`；无效号入库标 INVALID，重复按 e164 去重。
2. 跑在网检测：
   `node dist/index.js screen-run <checker> 200`
   - 输出 `{checked, onWhatsApp, notOnWhatsApp}`；在网号状态变 ON_WHATSAPP 并记录 jid。
3. 校准点：确认 Evolution v2 的 `/chat/whatsappNumbers/{instance}` 返回字段（number/exists/jid）与客户端映射一致；若字段名不同，改 `evolution-client.ts` 的 checkNumbers 映射即可。
4. 只把 ON_WHATSAPP 的号喂给发送队列（P0-4 批量投放接入点）——这一步把筛号的保号价值真正兑现。
```

- [ ] **Step 4: 跑全量测试确认无回归**

Run: `npm test`
Expected: 全部通过（Task 1-5 新增用例 + P0-1 既有用例）。

- [ ] **Step 5: 提交**

```bash
git add p0-sending-unit/src/index.ts p0-sending-unit/E2E-RUNBOOK.md
git commit -m "feat(p0-2): wire ScreeningService into CLI + E2E screening runbook"
```

---

## 自查（Self-Review）

**Spec 覆盖**：巴西 +55 规范化(Task 1) · TargetNumber 域与持久化(Task 2/5) · on-WhatsApp 检测客户端(Task 3) · 筛号编排(Task 4) · CLI+E2E(Task 6)。全部命中 P0-2 边界。

**明确不在本计划**：webhook 鉴权（P0-2b backlog）、养号（P0-3）、批量投放/回执聚合（P0-4）。筛号结果喂给发送队列的接线在 P0-4。

**占位符扫描**：无 TBD；每个改码步骤含完整代码或精确编辑指令。

**类型一致性**：`NormalizeResult`/`normalizeBrPhone`、`TargetStatus`/`TargetNumber`、`TargetNumberRepository.{save,findByE164,listByStatus}`、`EvolutionClient.checkNumbers`、`ScreeningService.{importNumbers,runScreening}` 在各 Task 间签名一致；`checkNumbers` 返回项 `{number,exists,jid}` 与 ScreeningService 消费一致。

**已知取舍**：Evolution 检测端点线格随 v2 版本可能变，客户端做薄，E2E 为校准关口（同 P0-1）。第九位补位用"订户首位 6-9 判移动"的标准启发式，边缘号段可能误判——真实筛号会由 on-WhatsApp 检测兜底纠正（不在网即淘汰）。
```
