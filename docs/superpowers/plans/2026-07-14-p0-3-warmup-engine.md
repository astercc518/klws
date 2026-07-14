# P0-3 养号引擎 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`) syntax.

**Goal:** 把新号从"零信任"养到"可安全冷发"。号池内互聊为主轴,**信号达标即毕业**(不看固定天数),快/慢双道,把单号存活 N 拉向 40;只把 MATURE 号放进冷发池。

**Architecture:** 沿用六边形结构。养号维度(WarmupStage: NEW→WARMING→MATURE)独立于 P0-1 的连接状态(号可同时 ONLINE 且 WARMING)。纯逻辑(状态机/策略/毕业判定/日限额)与时间解耦——在线时长、成熟天数由 service 从时间戳算好后传给纯函数。号池内配对互聊经 Evolution `sendText`,daily-cap 守门。

**Tech Stack:** 同前(TypeScript strict · Prisma/Postgres · Vitest · Evolution v2)。改既有 `p0-sending-unit/`。

## Global Constraints

- 沿用既有约束:Node ≥ 20、TS `strict:true` 无 `any`(测试桩除外)、所有 I/O 注入、纯逻辑不碰时间(时间戳/时钟注入或算好传入)、频繁提交。
- 养号状态机是养号维度的唯一真理源;非法转移抛错。
- 养号不改 P0-1 连接状态机/AccountService;通过 accountId 关联。
- 时间:service 用注入的 `clock: () => string`(ISO 时间串)取"现在",便于确定性测试。纯函数只收算好的 `onlineHours`/`matureDays` 数字。
- 所有阈值(毕业门槛、日限额、爬坡)集中在 `LANE_POLICIES` 配置对象,改配置即调参,不动逻辑。

---

## 文件结构(新增/修改)

```
p0-sending-unit/src/
  warmup/
    warmup-state.ts        # 养号状态机（纯）
    warmup-policy.ts       # 车道策略 + 毕业判定 + 日限额（纯）
    warmup-service.ts      # 编排：enroll/记录/毕业评估/配对互聊
  domain/warmup-profile.ts # WarmupProfile 实体（纯）
  ports/warmup-profile-repository.ts
  adapters/in-memory-warmup-profile-repository.ts
  adapters/prisma-warmup-profile-repository.ts
  index.ts                 # CLI: warmup-enroll / warmup-cycle / warmup-promote / warmup-mature（修改）
prisma/schema.prisma       # 追加 WarmupProfile model（修改）
E2E-RUNBOOK.md             # 追加养号章节（修改）
```

---

## Task 1: 养号状态机（纯逻辑）

**Files:**
- Create: `p0-sending-unit/src/warmup/warmup-state.ts`
- Test: `p0-sending-unit/tests/warmup/warmup-state.test.ts`

**Interfaces:**
- Produces:
  - `type WarmupStage = 'NEW' | 'WARMING' | 'MATURE'`
  - `type WarmupEvent = { type: 'ENROLL' } | { type: 'PROMOTE' }`
  - `warmupTransition(stage: WarmupStage, event: WarmupEvent): WarmupStage`（非法转移抛 `WarmupTransitionError`）
  - `class WarmupTransitionError extends Error`

转移:NEW +ENROLL→WARMING;WARMING +PROMOTE→MATURE;其它非法。

- [ ] **Step 1: 写失败测试**

```ts
import { describe, it, expect } from 'vitest';
import { warmupTransition, WarmupTransitionError } from '../../src/warmup/warmup-state.js';

describe('warmup state machine', () => {
  it('NEW +ENROLL -> WARMING', () => {
    expect(warmupTransition('NEW', { type: 'ENROLL' })).toBe('WARMING');
  });
  it('WARMING +PROMOTE -> MATURE', () => {
    expect(warmupTransition('WARMING', { type: 'PROMOTE' })).toBe('MATURE');
  });
  it('rejects illegal transitions', () => {
    expect(() => warmupTransition('NEW', { type: 'PROMOTE' })).toThrow(WarmupTransitionError);
    expect(() => warmupTransition('MATURE', { type: 'ENROLL' })).toThrow(WarmupTransitionError);
    expect(() => warmupTransition('WARMING', { type: 'ENROLL' })).toThrow(WarmupTransitionError);
  });
});
```

- [ ] **Step 2: 跑测试确认失败** — Run: `npm test tests/warmup/warmup-state.test.ts` — Expected: FAIL「Cannot find module」

- [ ] **Step 3: 实现**

```ts
export type WarmupStage = 'NEW' | 'WARMING' | 'MATURE';
export type WarmupEvent = { type: 'ENROLL' } | { type: 'PROMOTE' };

export class WarmupTransitionError extends Error {
  constructor(stage: WarmupStage, event: WarmupEvent['type']) {
    super(`Invalid warmup transition: ${event} from ${stage}`);
    this.name = 'WarmupTransitionError';
  }
}

const TABLE: Record<WarmupStage, Partial<Record<WarmupEvent['type'], WarmupStage>>> = {
  NEW: { ENROLL: 'WARMING' },
  WARMING: { PROMOTE: 'MATURE' },
  MATURE: {},
};

export function warmupTransition(stage: WarmupStage, event: WarmupEvent): WarmupStage {
  const next = TABLE[stage][event.type];
  if (!next) throw new WarmupTransitionError(stage, event.type);
  return next;
}
```

- [ ] **Step 4: 跑测试确认通过** — Run: `npm test tests/warmup/warmup-state.test.ts` — Expected: PASS（3 passed）

- [ ] **Step 5: 提交**

```bash
git add p0-sending-unit/src/warmup/warmup-state.ts p0-sending-unit/tests/warmup/warmup-state.test.ts
git commit -m "feat(p0-3): warmup stage machine (NEW->WARMING->MATURE)"
```

---

## Task 2: 车道策略 + 毕业判定 + 日限额（纯逻辑）

**Files:**
- Create: `p0-sending-unit/src/warmup/warmup-policy.ts`
- Test: `p0-sending-unit/tests/warmup/warmup-policy.test.ts`

**Interfaces:**
- Consumes: `WarmupStage`（Task 1）
- Produces:
  - `type WarmupLane = 'FAST' | 'STANDARD'`
  - `interface WarmupPolicy { minWarmupMessages: number; minReplies: number; minOnlineHours: number; warmingCap: number; matureBaseCap: number; matureMaxCap: number; matureRampStep: number }`
  - `const LANE_POLICIES: Record<WarmupLane, WarmupPolicy>`
  - `meetsPromotionCriteria(signals: { warmupMessagesSent: number; repliesReceived: number; onlineHours: number }, policy: WarmupPolicy): boolean`
  - `dailyCap(policy: WarmupPolicy, stage: WarmupStage, matureDays: number): number`

默认值(可调):FAST = 炮灰道(门槛极低);STANDARD = 标准道(养到 40)。

- [ ] **Step 1: 写失败测试**

```ts
import { describe, it, expect } from 'vitest';
import { LANE_POLICIES, meetsPromotionCriteria, dailyCap } from '../../src/warmup/warmup-policy.js';

describe('warmup policy', () => {
  it('FAST lane has far lower promotion bar than STANDARD', () => {
    expect(LANE_POLICIES.FAST.minWarmupMessages).toBeLessThan(LANE_POLICIES.STANDARD.minWarmupMessages);
    expect(LANE_POLICIES.FAST.minOnlineHours).toBeLessThan(LANE_POLICIES.STANDARD.minOnlineHours);
  });

  it('meetsPromotionCriteria requires all three signals to clear the bar', () => {
    const p = LANE_POLICIES.STANDARD;
    const ok = { warmupMessagesSent: p.minWarmupMessages, repliesReceived: p.minReplies, onlineHours: p.minOnlineHours };
    expect(meetsPromotionCriteria(ok, p)).toBe(true);
    expect(meetsPromotionCriteria({ ...ok, warmupMessagesSent: p.minWarmupMessages - 1 }, p)).toBe(false);
    expect(meetsPromotionCriteria({ ...ok, repliesReceived: p.minReplies - 1 }, p)).toBe(false);
    expect(meetsPromotionCriteria({ ...ok, onlineHours: p.minOnlineHours - 1 }, p)).toBe(false);
  });

  it('dailyCap: NEW=0, WARMING=warmingCap, MATURE ramps from base to max', () => {
    const p = LANE_POLICIES.STANDARD;
    expect(dailyCap(p, 'NEW', 0)).toBe(0);
    expect(dailyCap(p, 'WARMING', 5)).toBe(p.warmingCap);
    expect(dailyCap(p, 'MATURE', 0)).toBe(p.matureBaseCap);
    expect(dailyCap(p, 'MATURE', 1000)).toBe(p.matureMaxCap);
    const mid = dailyCap(p, 'MATURE', 1);
    expect(mid).toBe(Math.min(p.matureBaseCap + p.matureRampStep, p.matureMaxCap));
  });
});
```

- [ ] **Step 2: 跑测试确认失败** — Run: `npm test tests/warmup/warmup-policy.test.ts` — Expected: FAIL「Cannot find module」

- [ ] **Step 3: 实现**

```ts
import type { WarmupStage } from './warmup-state.js';

export type WarmupLane = 'FAST' | 'STANDARD';

export interface WarmupPolicy {
  minWarmupMessages: number;
  minReplies: number;
  minOnlineHours: number;
  warmingCap: number;
  matureBaseCap: number;
  matureMaxCap: number;
  matureRampStep: number;
}

// 默认值均为可调参数（真机 N 值出来后在此调整）。
export const LANE_POLICIES: Record<WarmupLane, WarmupPolicy> = {
  // 炮灰道：几乎不养，补资料+短时在线即毕业，冷发上量、死得快但便宜。
  FAST: {
    minWarmupMessages: 2,
    minReplies: 0,
    minOnlineHours: 2,
    warmingCap: 5,
    matureBaseCap: 15,
    matureMaxCap: 20,
    matureRampStep: 5,
  },
  // 标准道：养到双向对话可信，目标毕业后爬到 40。
  STANDARD: {
    minWarmupMessages: 20,
    minReplies: 5,
    minOnlineHours: 36,
    warmingCap: 8,
    matureBaseCap: 20,
    matureMaxCap: 40,
    matureRampStep: 5,
  },
};

export function meetsPromotionCriteria(
  signals: { warmupMessagesSent: number; repliesReceived: number; onlineHours: number },
  policy: WarmupPolicy,
): boolean {
  return (
    signals.warmupMessagesSent >= policy.minWarmupMessages &&
    signals.repliesReceived >= policy.minReplies &&
    signals.onlineHours >= policy.minOnlineHours
  );
}

export function dailyCap(policy: WarmupPolicy, stage: WarmupStage, matureDays: number): number {
  if (stage === 'NEW') return 0;
  if (stage === 'WARMING') return policy.warmingCap;
  return Math.min(policy.matureBaseCap + matureDays * policy.matureRampStep, policy.matureMaxCap);
}
```

- [ ] **Step 4: 跑测试确认通过** — Run: `npm test tests/warmup/warmup-policy.test.ts` — Expected: PASS（3 passed）

- [ ] **Step 5: 提交**

```bash
git add p0-sending-unit/src/warmup/warmup-policy.ts p0-sending-unit/tests/warmup/warmup-policy.test.ts
git commit -m "feat(p0-3): lane policies, signal-based promotion criteria, ramping daily cap"
```

---

## Task 3: WarmupProfile 实体 + 仓库端口 + 内存实现

**Files:**
- Create: `p0-sending-unit/src/domain/warmup-profile.ts`
- Create: `p0-sending-unit/src/ports/warmup-profile-repository.ts`
- Create: `p0-sending-unit/src/adapters/in-memory-warmup-profile-repository.ts`
- Test: `p0-sending-unit/tests/domain/warmup-profile.test.ts`

**Interfaces:**
- Consumes: `WarmupStage`（Task 1）、`WarmupLane`（Task 2）
- Produces:
  - `interface WarmupProfile { accountId: string; lane: WarmupLane; stage: WarmupStage; warmupMessagesSent: number; repliesReceived: number; onlineSince: string | null; maturedAt: string | null; sentToday: number; sentTodayDate: string | null }`
  - `interface WarmupProfileRepository { save(p: WarmupProfile): Promise<void>; findByAccountId(accountId: string): Promise<WarmupProfile | null>; listByStage(stage: WarmupStage, limit: number): Promise<WarmupProfile[]> }`
  - `class InMemoryWarmupProfileRepository implements WarmupProfileRepository`

- [ ] **Step 1: 写失败测试**

```ts
import { describe, it, expect } from 'vitest';
import { InMemoryWarmupProfileRepository } from '../../src/adapters/in-memory-warmup-profile-repository.js';
import type { WarmupProfile } from '../../src/domain/warmup-profile.js';

function p(accountId: string, stage: WarmupProfile['stage']): WarmupProfile {
  return { accountId, lane: 'STANDARD', stage, warmupMessagesSent: 0, repliesReceived: 0, onlineSince: null, maturedAt: null, sentToday: 0, sentTodayDate: null };
}

describe('WarmupProfile in-memory repo', () => {
  it('saves and finds by accountId', async () => {
    const repo = new InMemoryWarmupProfileRepository();
    const a = p('a1', 'WARMING');
    await repo.save(a);
    expect(await repo.findByAccountId('a1')).toEqual(a);
    expect(await repo.findByAccountId('nope')).toBeNull();
  });

  it('listByStage filters and respects limit', async () => {
    const repo = new InMemoryWarmupProfileRepository();
    await repo.save(p('a', 'WARMING'));
    await repo.save(p('b', 'WARMING'));
    await repo.save(p('c', 'MATURE'));
    expect((await repo.listByStage('WARMING', 10)).map((x) => x.accountId).sort()).toEqual(['a', 'b']);
    expect(await repo.listByStage('WARMING', 1)).toHaveLength(1);
  });
});
```

- [ ] **Step 2: 跑测试确认失败** — Run: `npm test tests/warmup/../domain/warmup-profile.test.ts` → 用 `npm test tests/domain/warmup-profile.test.ts` — Expected: FAIL「Cannot find module」

- [ ] **Step 3: 实现实体**

`src/domain/warmup-profile.ts`:
```ts
import type { WarmupStage } from '../warmup/warmup-state.js';
import type { WarmupLane } from '../warmup/warmup-policy.js';

export interface WarmupProfile {
  accountId: string;
  lane: WarmupLane;
  stage: WarmupStage;
  warmupMessagesSent: number;
  repliesReceived: number;
  onlineSince: string | null;
  maturedAt: string | null;
  sentToday: number;
  sentTodayDate: string | null;
}
```

- [ ] **Step 4: 实现端口**

`src/ports/warmup-profile-repository.ts`:
```ts
import type { WarmupProfile } from '../domain/warmup-profile.js';
import type { WarmupStage } from '../warmup/warmup-state.js';

export interface WarmupProfileRepository {
  save(p: WarmupProfile): Promise<void>;
  findByAccountId(accountId: string): Promise<WarmupProfile | null>;
  listByStage(stage: WarmupStage, limit: number): Promise<WarmupProfile[]>;
}
```

- [ ] **Step 5: 实现内存适配器**

`src/adapters/in-memory-warmup-profile-repository.ts`:
```ts
import type { WarmupProfile } from '../domain/warmup-profile.js';
import type { WarmupProfileRepository } from '../ports/warmup-profile-repository.js';
import type { WarmupStage } from '../warmup/warmup-state.js';

export class InMemoryWarmupProfileRepository implements WarmupProfileRepository {
  private byAccount = new Map<string, WarmupProfile>();

  async save(p: WarmupProfile): Promise<void> {
    this.byAccount.set(p.accountId, { ...p });
  }
  async findByAccountId(accountId: string): Promise<WarmupProfile | null> {
    const p = this.byAccount.get(accountId);
    return p ? { ...p } : null;
  }
  async listByStage(stage: WarmupStage, limit: number): Promise<WarmupProfile[]> {
    const out: WarmupProfile[] = [];
    for (const p of this.byAccount.values()) {
      if (p.stage === stage) out.push({ ...p });
      if (out.length >= limit) break;
    }
    return out;
  }
}
```

- [ ] **Step 6: 跑测试确认通过** — Run: `npm test tests/domain/warmup-profile.test.ts` — Expected: PASS（2 passed）

- [ ] **Step 7: 提交**

```bash
git add p0-sending-unit/src/domain/warmup-profile.ts p0-sending-unit/src/ports/warmup-profile-repository.ts p0-sending-unit/src/adapters/in-memory-warmup-profile-repository.ts p0-sending-unit/tests/domain/warmup-profile.test.ts
git commit -m "feat(p0-3): WarmupProfile entity, repository port, in-memory adapter"
```

---

## Task 4: 养号服务核心（enroll / 记录 / 毕业评估 / 配额）

**Files:**
- Create: `p0-sending-unit/src/warmup/warmup-service.ts`
- Test: `p0-sending-unit/tests/warmup/warmup-service.test.ts`

**Interfaces:**
- Consumes: `warmupTransition`（Task 1）、`LANE_POLICIES`/`meetsPromotionCriteria`/`dailyCap`/`WarmupLane`（Task 2）、`WarmupProfile`（Task 3）、`WarmupProfileRepository`（Task 3）
- Produces:
  - `type Clock = () => string`（返回 ISO 时间串）
  - `class WarmupService` 构造 `{ profiles: WarmupProfileRepository; clock: Clock }`
    - `enroll(accountId: string, lane: WarmupLane): Promise<WarmupProfile>`（NEW→WARMING,onlineSince=now,stage=WARMING）
    - `recordReply(accountId: string): Promise<void>`（repliesReceived+1）
    - `evaluateAndPromote(accountId: string): Promise<boolean>`（算 onlineHours=now-onlineSince;达标→PROMOTE→MATURE,maturedAt=now,返回 true;否则 false）

说明:`enroll` 直接把 NEW 经 ENROLL 落成 WARMING(NEW 仅是初始态,不落库)。onlineHours 由 `(now - onlineSince)/3600000` 计算。

- [ ] **Step 1: 写失败测试**

```ts
import { describe, it, expect } from 'vitest';
import { WarmupService } from '../../src/warmup/warmup-service.js';
import { InMemoryWarmupProfileRepository } from '../../src/adapters/in-memory-warmup-profile-repository.js';
import { LANE_POLICIES } from '../../src/warmup/warmup-policy.js';

function fixedClock(iso: string) {
  return () => iso;
}

describe('WarmupService', () => {
  it('enroll creates a WARMING profile with onlineSince and lane', async () => {
    const profiles = new InMemoryWarmupProfileRepository();
    const svc = new WarmupService({ profiles, clock: fixedClock('2026-07-14T00:00:00.000Z') });
    const p = await svc.enroll('a1', 'STANDARD');
    expect(p.stage).toBe('WARMING');
    expect(p.lane).toBe('STANDARD');
    expect(p.onlineSince).toBe('2026-07-14T00:00:00.000Z');
  });

  it('recordReply increments repliesReceived', async () => {
    const profiles = new InMemoryWarmupProfileRepository();
    const svc = new WarmupService({ profiles, clock: fixedClock('2026-07-14T00:00:00.000Z') });
    await svc.enroll('a1', 'STANDARD');
    await svc.recordReply('a1');
    expect((await profiles.findByAccountId('a1'))!.repliesReceived).toBe(1);
  });

  it('evaluateAndPromote promotes only when all signals clear (incl. online hours)', async () => {
    const profiles = new InMemoryWarmupProfileRepository();
    const std = LANE_POLICIES.STANDARD;
    // enroll at T0; evaluate at T0 + (minOnlineHours) hours
    const t0 = Date.parse('2026-07-14T00:00:00.000Z');
    let now = t0;
    const svc = new WarmupService({ profiles, clock: () => new Date(now).toISOString() });
    const p = await svc.enroll('a1', 'STANDARD');
    // manually set the message/reply signals to exactly the bar
    await profiles.save({ ...p, warmupMessagesSent: std.minWarmupMessages, repliesReceived: std.minReplies });

    // too early -> not promoted
    now = t0 + (std.minOnlineHours - 1) * 3600_000;
    expect(await svc.evaluateAndPromote('a1')).toBe(false);
    expect((await profiles.findByAccountId('a1'))!.stage).toBe('WARMING');

    // enough online hours -> promoted
    now = t0 + std.minOnlineHours * 3600_000;
    expect(await svc.evaluateAndPromote('a1')).toBe(true);
    const matured = await profiles.findByAccountId('a1');
    expect(matured!.stage).toBe('MATURE');
    expect(matured!.maturedAt).toBe(new Date(now).toISOString());
  });
});
```

- [ ] **Step 2: 跑测试确认失败** — Run: `npm test tests/warmup/warmup-service.test.ts` — Expected: FAIL「Cannot find module」

- [ ] **Step 3: 实现**

```ts
import { warmupTransition } from './warmup-state.js';
import { LANE_POLICIES, meetsPromotionCriteria, type WarmupLane } from './warmup-policy.js';
import type { WarmupProfile } from '../domain/warmup-profile.js';
import type { WarmupProfileRepository } from '../ports/warmup-profile-repository.js';

export type Clock = () => string;

export class WarmupService {
  constructor(private deps: { profiles: WarmupProfileRepository; clock: Clock }) {}

  async enroll(accountId: string, lane: WarmupLane): Promise<WarmupProfile> {
    const now = this.deps.clock();
    const profile: WarmupProfile = {
      accountId,
      lane,
      stage: warmupTransition('NEW', { type: 'ENROLL' }),
      warmupMessagesSent: 0,
      repliesReceived: 0,
      onlineSince: now,
      maturedAt: null,
      sentToday: 0,
      sentTodayDate: null,
    };
    await this.deps.profiles.save(profile);
    return profile;
  }

  async recordReply(accountId: string): Promise<void> {
    const p = await this.require(accountId);
    await this.deps.profiles.save({ ...p, repliesReceived: p.repliesReceived + 1 });
  }

  async evaluateAndPromote(accountId: string): Promise<boolean> {
    const p = await this.require(accountId);
    if (p.stage !== 'WARMING') return false;
    const policy = LANE_POLICIES[p.lane];
    const onlineHours = p.onlineSince === null
      ? 0
      : (Date.parse(this.deps.clock()) - Date.parse(p.onlineSince)) / 3600_000;
    const ok = meetsPromotionCriteria(
      { warmupMessagesSent: p.warmupMessagesSent, repliesReceived: p.repliesReceived, onlineHours },
      policy,
    );
    if (!ok) return false;
    await this.deps.profiles.save({
      ...p,
      stage: warmupTransition(p.stage, { type: 'PROMOTE' }),
      maturedAt: this.deps.clock(),
    });
    return true;
  }

  private async require(accountId: string): Promise<WarmupProfile> {
    const p = await this.deps.profiles.findByAccountId(accountId);
    if (!p) throw new Error(`Warmup profile not found: ${accountId}`);
    return p;
  }
}
```

- [ ] **Step 4: 跑测试确认通过** — Run: `npm test tests/warmup/warmup-service.test.ts` — Expected: PASS（3 passed）

- [ ] **Step 5: 提交**

```bash
git add p0-sending-unit/src/warmup/warmup-service.ts p0-sending-unit/tests/warmup/warmup-service.test.ts
git commit -m "feat(p0-3): WarmupService core (enroll, recordReply, signal-based evaluateAndPromote)"
```

---

## Task 5: 号池内配对互聊（pairAndWarm）

**Files:**
- Modify: `p0-sending-unit/src/warmup/warmup-service.ts`
- Test: `p0-sending-unit/tests/warmup/warmup-pairing.test.ts`

**Interfaces:**
- Consumes: 现有 `WarmupService`、`dailyCap`（Task 2）、`EvolutionClient.sendText`（P0-1）、`AccountRepository`（P0-1,取 instanceName + 判断 ONLINE）
- Produces: `WarmupService` 增依赖 `{ accounts: AccountRepository; evolution: Pick<EvolutionClient,'sendText'> }` 与方法
  - `pairAndWarm(batchSize: number): Promise<{ pairs: number; messagesSent: number }>`
    —— 取 WARMING 档 profiles(≤batchSize) → 过滤出对应 account 为 ONLINE 的 → 两两配对 → 每个号给搭档发一条固定养号问候(`'oi, tudo bem?'`),经 evolution.sendText;对每个发送方 warmupMessagesSent+1、按 `dailyCap(policy,'WARMING',0)` 守门(当日已达上限则跳过该号)。奇数个时最后一个不配对。

说明:daily-cap 用 `sentToday`/`sentTodayDate` 跟踪:若 profile.sentTodayDate ≠ 今天(clock 的日期前缀),重置 sentToday=0。发送成功后 sentToday+1。达 `dailyCap` 上限的号本轮跳过。

- [ ] **Step 1: 写失败测试**

```ts
import { describe, it, expect, vi } from 'vitest';
import { WarmupService } from '../../src/warmup/warmup-service.js';
import { InMemoryWarmupProfileRepository } from '../../src/adapters/in-memory-warmup-profile-repository.js';
import { InMemoryAccountRepository } from '../../src/adapters/in-memory-account-repository.js';
import type { Account } from '../../src/domain/account.js';

function onlineAccount(id: string): Account {
  return { id, phoneNumber: `5511${id}`, state: 'ONLINE', instanceName: `acct-${id}`, proxy: null, deadReason: null };
}

async function setup() {
  const profiles = new InMemoryWarmupProfileRepository();
  const accounts = new InMemoryAccountRepository();
  const sendText = vi.fn(async () => ({ providerMessageId: 'M' }));
  const svc = new WarmupService({ profiles, accounts, evolution: { sendText } as never, clock: () => '2026-07-14T09:00:00.000Z' });
  return { profiles, accounts, sendText, svc };
}

describe('WarmupService.pairAndWarm', () => {
  it('pairs two ONLINE warming accounts and each sends one warmup message', async () => {
    const { accounts, svc, sendText } = await setup();
    await accounts.save(onlineAccount('a'));
    await accounts.save(onlineAccount('b'));
    await svc.enroll('a', 'STANDARD');
    await svc.enroll('b', 'STANDARD');

    const r = await svc.pairAndWarm(10);
    expect(r.pairs).toBe(1);
    expect(r.messagesSent).toBe(2);
    expect(sendText).toHaveBeenCalledTimes(2);
  });

  it('skips accounts whose connection is not ONLINE', async () => {
    const { accounts, svc } = await setup();
    await accounts.save(onlineAccount('a'));
    await accounts.save({ ...onlineAccount('b'), state: 'RECONNECTING' });
    await svc.enroll('a', 'STANDARD');
    await svc.enroll('b', 'STANDARD');
    const r = await svc.pairAndWarm(10);
    expect(r.pairs).toBe(0);
    expect(r.messagesSent).toBe(0);
  });

  it('increments warmupMessagesSent for senders', async () => {
    const { accounts, profiles, svc } = await setup();
    await accounts.save(onlineAccount('a'));
    await accounts.save(onlineAccount('b'));
    await svc.enroll('a', 'STANDARD');
    await svc.enroll('b', 'STANDARD');
    await svc.pairAndWarm(10);
    expect((await profiles.findByAccountId('a'))!.warmupMessagesSent).toBe(1);
    expect((await profiles.findByAccountId('b'))!.warmupMessagesSent).toBe(1);
  });
});
```

- [ ] **Step 2: 跑测试确认失败** — Run: `npm test tests/warmup/warmup-pairing.test.ts` — Expected: FAIL（构造参数缺 accounts/evolution 或 pairAndWarm 未定义）

- [ ] **Step 3: 实现**（编辑 `warmup-service.ts`）

更新 import 与构造依赖,并加方法。把顶部 import 补充:
```ts
import { LANE_POLICIES, meetsPromotionCriteria, dailyCap, type WarmupLane } from './warmup-policy.js';
import type { AccountRepository } from '../ports/account-repository.js';
import type { EvolutionClient } from '../evolution/evolution-client.js';
```
构造签名改为:
```ts
  constructor(
    private deps: {
      profiles: WarmupProfileRepository;
      accounts: AccountRepository;
      evolution: Pick<EvolutionClient, 'sendText'>;
      clock: Clock;
    },
  ) {}
```
在类内追加:
```ts
  async pairAndWarm(batchSize: number): Promise<{ pairs: number; messagesSent: number }> {
    const warming = await this.deps.profiles.listByStage('WARMING', batchSize);
    const today = this.deps.clock().slice(0, 10);

    // 只留下连接态 ONLINE 且当日未超额的号
    const eligible: WarmupProfile[] = [];
    for (const p of warming) {
      const account = await this.deps.accounts.findById(p.accountId);
      if (!account || account.state !== 'ONLINE') continue;
      const sentToday = p.sentTodayDate === today ? p.sentToday : 0;
      if (sentToday >= dailyCap(LANE_POLICIES[p.lane], 'WARMING', 0)) continue;
      eligible.push(p);
    }

    let pairs = 0;
    let messagesSent = 0;
    for (let i = 0; i + 1 < eligible.length; i += 2) {
      const a = eligible[i]!;
      const b = eligible[i + 1]!;
      const accA = await this.deps.accounts.findById(a.accountId);
      const accB = await this.deps.accounts.findById(b.accountId);
      if (!accA || !accB) continue;
      await this.deps.evolution.sendText({ instanceName: accA.instanceName, to: accB.phoneNumber, text: 'oi, tudo bem?' });
      await this.deps.evolution.sendText({ instanceName: accB.instanceName, to: accA.phoneNumber, text: 'oi, tudo bem?' });
      await this.bumpSent(a, today);
      await this.bumpSent(b, today);
      pairs += 1;
      messagesSent += 2;
    }
    return { pairs, messagesSent };
  }

  private async bumpSent(p: WarmupProfile, today: string): Promise<void> {
    const sentToday = p.sentTodayDate === today ? p.sentToday : 0;
    await this.deps.profiles.save({
      ...p,
      warmupMessagesSent: p.warmupMessagesSent + 1,
      sentToday: sentToday + 1,
      sentTodayDate: today,
    });
  }
```

- [ ] **Step 4: 跑测试确认通过 + 全量 + tsc** — Run: `npm test tests/warmup/warmup-pairing.test.ts`（3 passed）;`npm test`（全绿）;`node_modules/.bin/tsc --noEmit -p tsconfig.json`（干净）

- [ ] **Step 5: 提交**

```bash
git add p0-sending-unit/src/warmup/warmup-service.ts p0-sending-unit/tests/warmup/warmup-pairing.test.ts
git commit -m "feat(p0-3): intra-pool pairAndWarm (paired warmup messages, ONLINE-gated, daily-cap governed)"
```

---

## Task 6: Prisma WarmupProfile 适配器

**Files:**
- Modify: `p0-sending-unit/prisma/schema.prisma`
- Create: `p0-sending-unit/src/adapters/prisma-warmup-profile-repository.ts`
- Test: `p0-sending-unit/tests/adapters/prisma-warmup-profile-repository.test.ts`

**Interfaces:**
- Consumes: `WarmupProfile`（Task 3）、`WarmupProfileRepository`（Task 3）、`WarmupStage`/`WarmupLane`
- Produces: `class PrismaWarmupProfileRepository implements WarmupProfileRepository`（构造接收 `PrismaClient`）

> 需 Postgres(容器 `p0su-postgres`,55432,`.env` 已指向)。`npx prisma migrate dev --name warmup_profile` 增量迁移;若报 drift/要 reset 则 BLOCKED 上报,勿 reset。

- [ ] **Step 1: 追加 schema model**（`prisma/schema.prisma` 末尾）

```prisma
model WarmupProfile {
  accountId          String  @id
  lane               String
  stage              String
  warmupMessagesSent Int     @default(0)
  repliesReceived    Int     @default(0)
  onlineSince        String?
  maturedAt          String?
  sentToday          Int     @default(0)
  sentTodayDate      String?
}
```

- [ ] **Step 2: 迁移** — Run（`p0-sending-unit/`）: `npx prisma migrate dev --name warmup_profile` — Expected: 增量迁移成功,重新生成 client。

- [ ] **Step 3: 写失败测试**

```ts
import { describe, it, expect, beforeEach, afterAll } from 'vitest';
import { PrismaClient } from '@prisma/client';
import { PrismaWarmupProfileRepository } from '../../src/adapters/prisma-warmup-profile-repository.js';
import type { WarmupProfile } from '../../src/domain/warmup-profile.js';

const prisma = new PrismaClient();
const repo = new PrismaWarmupProfileRepository(prisma);
beforeEach(async () => { await prisma.warmupProfile.deleteMany(); });
afterAll(async () => { await prisma.$disconnect(); });

const base: WarmupProfile = {
  accountId: 'a1', lane: 'STANDARD', stage: 'WARMING', warmupMessagesSent: 3, repliesReceived: 1,
  onlineSince: '2026-07-14T00:00:00.000Z', maturedAt: null, sentToday: 2, sentTodayDate: '2026-07-14',
};

describe('PrismaWarmupProfileRepository', () => {
  it('saves and finds by accountId with full round-trip', async () => {
    await repo.save(base);
    expect(await repo.findByAccountId('a1')).toEqual(base);
    expect(await repo.findByAccountId('nope')).toBeNull();
  });
  it('upserts on repeated save (promotion)', async () => {
    await repo.save(base);
    await repo.save({ ...base, stage: 'MATURE', maturedAt: '2026-07-15T00:00:00.000Z' });
    const f = await repo.findByAccountId('a1');
    expect(f!.stage).toBe('MATURE');
    expect(f!.maturedAt).toBe('2026-07-15T00:00:00.000Z');
  });
  it('listByStage filters + limit', async () => {
    await repo.save({ ...base, accountId: 'a', stage: 'WARMING' });
    await repo.save({ ...base, accountId: 'b', stage: 'WARMING' });
    await repo.save({ ...base, accountId: 'c', stage: 'MATURE' });
    expect(await repo.listByStage('WARMING', 10)).toHaveLength(2);
    expect(await repo.listByStage('WARMING', 1)).toHaveLength(1);
  });
});
```

- [ ] **Step 4: 跑测试确认失败** — Run: `npm test tests/adapters/prisma-warmup-profile-repository.test.ts` — Expected: FAIL「Cannot find module」

- [ ] **Step 5: 实现**

`src/adapters/prisma-warmup-profile-repository.ts`:
```ts
import type { PrismaClient } from '@prisma/client';
import type { WarmupProfile } from '../domain/warmup-profile.js';
import type { WarmupProfileRepository } from '../ports/warmup-profile-repository.js';
import type { WarmupStage } from '../warmup/warmup-state.js';
import type { WarmupLane } from '../warmup/warmup-policy.js';

interface Row {
  accountId: string; lane: string; stage: string; warmupMessagesSent: number; repliesReceived: number;
  onlineSince: string | null; maturedAt: string | null; sentToday: number; sentTodayDate: string | null;
}

function toProfile(r: Row): WarmupProfile {
  return {
    accountId: r.accountId, lane: r.lane as WarmupLane, stage: r.stage as WarmupStage,
    warmupMessagesSent: r.warmupMessagesSent, repliesReceived: r.repliesReceived,
    onlineSince: r.onlineSince, maturedAt: r.maturedAt, sentToday: r.sentToday, sentTodayDate: r.sentTodayDate,
  };
}

export class PrismaWarmupProfileRepository implements WarmupProfileRepository {
  constructor(private prisma: PrismaClient) {}

  async save(p: WarmupProfile): Promise<void> {
    const data = {
      lane: p.lane, stage: p.stage, warmupMessagesSent: p.warmupMessagesSent, repliesReceived: p.repliesReceived,
      onlineSince: p.onlineSince, maturedAt: p.maturedAt, sentToday: p.sentToday, sentTodayDate: p.sentTodayDate,
    };
    await this.prisma.warmupProfile.upsert({
      where: { accountId: p.accountId },
      create: { accountId: p.accountId, ...data },
      update: data,
    });
  }

  async findByAccountId(accountId: string): Promise<WarmupProfile | null> {
    const r = await this.prisma.warmupProfile.findUnique({ where: { accountId } });
    return r ? toProfile(r as unknown as Row) : null;
  }

  async listByStage(stage: WarmupStage, limit: number): Promise<WarmupProfile[]> {
    const rows = await this.prisma.warmupProfile.findMany({ where: { stage }, take: limit });
    return rows.map((r) => toProfile(r as unknown as Row));
  }
}
```

- [ ] **Step 6: 跑测试确认通过 + 全量 + tsc** — Run: `npm test tests/adapters/prisma-warmup-profile-repository.test.ts`（3 passed）;`npm test`（全绿）;`node_modules/.bin/tsc --noEmit -p tsconfig.json`（干净）

- [ ] **Step 7: 提交**

```bash
git add p0-sending-unit/prisma/schema.prisma p0-sending-unit/prisma/migrations p0-sending-unit/src/adapters/prisma-warmup-profile-repository.ts p0-sending-unit/tests/adapters/prisma-warmup-profile-repository.test.ts
git commit -m "feat(p0-3): Prisma WarmupProfile adapter + migration"
```

---

## Task 7: CLI 接线 + E2E Runbook 养号章节

**Files:**
- Modify: `p0-sending-unit/src/index.ts`
- Modify: `p0-sending-unit/E2E-RUNBOOK.md`

**Interfaces:**
- Consumes: `WarmupService`（Task 4/5）、`PrismaWarmupProfileRepository`（Task 6）、现有 `evolution`、`PrismaAccountRepository`、`prisma`

- [ ] **Step 1: 组装 WarmupService 并加 CLI 命令**（编辑 `src/index.ts`）

追加 import:
```ts
import { PrismaWarmupProfileRepository } from './adapters/prisma-warmup-profile-repository.js';
import { WarmupService } from './warmup/warmup-service.js';
```
在 `screening` 实例之后构造(复用已有的 `new PrismaAccountRepository(prisma)`——可新建一个实例):
```ts
const warmup = new WarmupService({
  profiles: new PrismaWarmupProfileRepository(prisma),
  accounts: new PrismaAccountRepository(prisma),
  evolution,
  clock: () => new Date().toISOString(),
});
```
在 `runCli` 的 `screen-run` 分支后、最终 `else` 前追加:
```ts
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
```
CLI 注释块追加这三条用法;`new Date()` 在此入口层可用(非 workflow)。

- [ ] **Step 2: 验证全项目编译** — Run（`p0-sending-unit/`）: `node_modules/.bin/tsc --noEmit -p tsconfig.json` — Expected: 干净。

- [ ] **Step 3: 追加 E2E Runbook 养号章节**（`E2E-RUNBOOK.md` 末尾）

```markdown
## 养号（P0-3）真机验证

前置：一批已上线（ONLINE）的号，用于号池内互聊。

1. 入池（选道）：`node dist/index.js warmup-enroll <accountId> STANDARD`（炮灰用 FAST）。号进 WARMING。
2. 跑养号周期（可挂 cron 反复跑）：`node dist/index.js warmup-cycle 100`
   - WARMING 且 ONLINE 的号两两配对，各发一条 `oi, tudo bem?` 给搭档；受当日上限守门。
   - 让搭档号回一句（真机上收到即算 reply；自动计回复 = P0-3 后续接线，暂可人工调 recordReply）。
3. 评估毕业：`node dist/index.js warmup-promote <accountId>`
   - 达标（互聊够 + 回复够 + 在线时长够）→ 升 MATURE，返回 `{promoted:true}`。
4. 只把 MATURE 号喂给冷发（P0-4 接入点）。**关键校准**：养号默认参数（LANE_POLICIES）是拍的合理值；跑一批真号统计"每号从毕业到被封发了多少条 = 真实 N"，据此回调 STANDARD 道的门槛/爬坡。
5. 自动计回复 & 只发 MATURE 的冷发接线在 P0-3 后续 / P0-4。
```

- [ ] **Step 4: 跑全量测试确认无回归** — Run: `npm test` — Expected: 全绿。

- [ ] **Step 5: 提交**

```bash
git add p0-sending-unit/src/index.ts p0-sending-unit/E2E-RUNBOOK.md
git commit -m "feat(p0-3): wire WarmupService into CLI + E2E warmup runbook"
```

---

## 自查（Self-Review）

**Spec 覆盖**:养号状态机(T1) · 车道/毕业/限额(T2) · WarmupProfile 实体+持久化(T3/T6) · 服务 enroll/记录/毕业(T4) · 号池配对互聊(T5) · CLI+E2E(T7)。信号毕业(非天数)、快慢双道、参数可配置——均落地。

**明确不在本计划**:webhook 自动计 reply(留后续接线,recordReply 方法已就绪) · 只发 MATURE 的冷发投放(P0-4) · P0-2 jid 匹配兜底(P0-4)。

**占位符扫描**:无 TBD;每步含完整代码或精确编辑。

**类型一致性**:`WarmupStage`/`WarmupEvent`/`warmupTransition`、`WarmupLane`/`WarmupPolicy`/`LANE_POLICIES`/`meetsPromotionCriteria`/`dailyCap`、`WarmupProfile`/`WarmupProfileRepository`、`WarmupService.{enroll,recordReply,evaluateAndPromote,pairAndWarm}`、`Clock` 各 Task 间一致。

**已知取舍**:默认 LANE_POLICIES 阈值是合理拍值,非最优——真机 N 值出来后在配置里调,不动逻辑。自动计回复暂缺,毕业的"够回复"信号 v1 需人工/CLI 触发 recordReply(真机验证时说明)。号池互聊话术固定单句,内容多样化留后续。
