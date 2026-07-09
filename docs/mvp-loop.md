# MVP 业务闭环 · 数据流线（已核对源码）

> **目标**：跑通一次 建客户 → 绑设备 → 充值 → 发送扣费。
> 本文只描述这条 happy path 每一步动了哪张表、调了哪个函数。审计/分页/CRUD 联动/高密度重构/真机扫码 **均不在 MVP 范围**。

## 核心架构事实（记住这个，"乱"就散了）
**整个系统以 `tenant` 为轴，一切按 `tenant_id` 串联。没有 `customers`/`customer_wallets` 表。**
- **tenant**（`tenants` 表，`0009_tenants.sql`）= 工作区 / 钱包主体 / 计费实体。
- **客户登录** = `console_users` 里 `role='customer'` 且带 `tenant_id` 的一行；它只是"谁能登录进这个 tenant"。
- 钱包 `tenant_wallets`、设备 `account_devices`、定价 `tenant_pricing`、扣费 `billing_charges` —— **全部按 `tenant_id` 键**。

> ⚠️ 曾经有一版 doc 说按 `customer_id`/`customer_wallets`——**那是错的**，是某个 worktree 的旧命名幻觉。源码里全是 `tenant_id`。

---

## ① 建客户（= 两次写入：先 tenant，再登录账号）
1. **建 tenant**：`POST /api/v1/admin/tenants {name}` → `handleAdminCreateTenant`（`internal/api/admin_api.go:134`）→ `INSERT INTO tenants (name,status) VALUES ($1,'active') RETURNING id` → 得到 `tenant_id`
2. **建客户登录**：`POST /api/v1/admin/users {email,password,role:"customer",tenant_id}` → `handleAdminCreateUser`（`admin_api.go:294`）→ `UserRepo.Create(...)`（`internal/console/users.go:51`）
   - `tenant_id` 对 customer **必填且 >0**；建登录**不会**自动建钱包。

## ② 绑设备
- **表**：`account_devices`（`0001_account_devices.sql`），键 `tenant_id`（`idx_acc_tenant`）
- **状态**：`ban_status` 枚举 `init/active/flagged/banned/logged_out`，在线 = `'active'`
- **⚠️ MVP 陷阱**：真机配对的写入函数在**已封存的 QR 分支**上，main 上没有。MVP 要一台 stub 在线设备 → 走 admin 导入路径，或直接 `INSERT INTO account_devices (tenant_id, account_jid, ban_status) VALUES (..., 'active')`。这是本阶段唯一要现拆的小活。

## ③ 充值（SP1 资金线，已并入 main）
- **表**：`tenant_wallets.balance`（BIGINT 最小单位，主键 `tenant_id`）+ `wallet_ledger` 记 `kind='topup'`
- **函数**：`billing.Repo.Topup(ctx, tenantID, amount, ref)` → `internal/billing/billing.go:261`
  - 内部 `INSERT INTO tenant_wallets (tenant_id) ... ON CONFLICT DO NOTHING`——**钱包在这步惰性创建**；`idem_key="topup:"+ref` 幂等
- **HTTP**：`POST /api/v1/admin/finance/topup` → `handleAdminTopup`（`admin_api.go:344`）

## ④ 发送扣费（两段式冻结扣费 —— 逻辑震中）
1. **Hold（下发前冻结）** `billing.Repo.Hold(ctx, HoldRequest)` → `internal/billing/billing.go:77`
   - 插 `billing_charges (tenant_id, message_id, amount, state='held')`（唯一键 `(tenant_id,message_id)` 幂等）
   - `tenant_wallets`：`balance -= amount`，`frozen += amount`；余额不足 → `ErrInsufficientFunds`，发送被拦
2. **Settle（送达成功才真扣）** `billing.Repo.Settle(ctx, tenantID, messageID)` → `billing.go:182`
   - `billing_charges.state='settled'`，`frozen -= amount`（balance 不动 = 平台收入）
3. **失败** → `RequestRefund`，钱继续冻结等人工审
- **单价**：`tenant_pricing.unit_price` 按 `(tenant_id, country_code)` 查（`pricing.Repo.PriceFor`）；查不到默认 `1`
- **入口**：`dispatch.SendWorker.ProcessSend(...)` → `internal/dispatch/worker.go`（`sendgate` 仅限流准入，不碰钱）

---

## 已有的现成资产（别重写）
- **`cmd/seed/main.go`** 已幂等跑通 ①③ + 设价：建 tenant → 建 admin/sales/customer 登录 → `Topup` → `SetPrice`。演示账号：`admin@wadist.local`/`admin12345`。
- 端点 ①③ 全部存在；④ 的 Hold/Settle 全部存在。

## 记住这 3 句话就算过关
1. 系统以 **tenant** 为轴，一切按 `tenant_id`；"客户登录"只是 `console_users(role=customer, tenant_id)` 的一行。
2. 钱在 `tenant_wallets`（惰性创建），不在任何 user 行上。
3. 发送扣费是"先 Hold 冻结、送达后 Settle 才真扣"。
