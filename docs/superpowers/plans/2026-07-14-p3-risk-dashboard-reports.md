# P3 风控监控看板 + 报表中心(指标快照)— 实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** admin 风控看板(实时 DB 聚合指标 + 健康分布 + 异常账号明细)+ 报表中心(历史趋势 + 租户消耗排行 + CSV 导出),新增 worker 指标采样器落库 `metric_snapshots` 喂饱历史趋势与大盘。

**Architecture:** 新叶子包 `internal/metrics`(采样器 + 快照读写),worker 进程定时采样 DB 聚合落库;console 新 `internal/api/metrics_api.go` 只读端点——实时看板直接 DB 聚合(照 handleAdminStats),历史/报表读快照。前端风控看板 + 报表中心两页。

**Tech Stack:** Go(pgx/testcontainers)、Next.js 16 魔改版 + React 19 + Tailwind 4 + ProDataTable(P1)+ Tremor 图表 + i18n dicts(P1b)。

**Spec:** `docs/superpowers/specs/2026-07-14-p3-risk-dashboard-reports-design.md`

## Global Constraints

- **红线**:新增 `internal/metrics`(叶子)+ `internal/api` 只读端点 + 迁移 0023;**不改** billing/dispatch/store/sendgate/cluster 引擎业务逻辑。采样器只 SELECT 聚合现有表 + INSERT/DELETE metric_snapshots。
- admin 端点:`admin` 组(requireAuth+requireRole(RoleAdmin)),SystemPool(BYPASSRLS 跨租户)。导出写审计(照 SP4 `writeCSV`+`csvSanitize` 防公式注入,复用不重造)。
- **数据约束(spec §4)**:`campaign_recipients` 无 `sent_at`——处理量锚 `updated_at`,到达锚 `delivered_at`,到达延迟 = `delivered_at - created_at`(记 `avg_delivery_ms`,UI 标"到达延迟(创建→到达)")。state 枚举 pending/sent/failed/skipped(0006);delivered_at/read_at 是时间戳(0016)。
- 金额/消耗口径复用 SP4 `wallet_ledger` settle(net=delta_balance+delta_frozen,`AT TIME ZONE 'Asia/Shanghai'` 分桶),不重造。
- 后端 testcontainers 真库;前端无 JS 测试框架,门槛 = `cd frontend && npx tsc --noEmit` + `node scripts/check-i18n-keys.mjs`(0 missing)+ 改动文件无新 lint 错误 + `npm run build`。前端文案入 i18n dict(`admin.risk.*`/`admin.reports.*`,zh/en 成对);图表用 P1 chart token。
- 后端聚焦测试前台跑(`go test ./internal/... -run TestXxx`),别起后台 make gate/Monitor;控制器在任务间跑 gate。
- git 在执行时 worktree 根;每任务至少一 commit。

---

### Task 1: 迁移 0023 + internal/metrics 快照读写

**Files:**
- Create: `migrations/0023_metric_snapshots.sql`
- Create: `internal/metrics/snapshot.go`(Snapshot 结构 + Insert + 读查询)
- Create: `internal/metrics/snapshot_test.go`(testcontainers)

**Interfaces:**
- Produces:
```go
package metrics
type Snapshot struct {
    CapturedAt time.Time
    AccountsTotal, AccountsActive, AccountsBanned, AccountsQuarantined int
    QueueBacklog, Processed1h, Delivered1h, Failed1h int
    AvgDeliveryMs *int // nullable
}
type Store struct { /* wraps *pgxpool.Pool (SystemPool) */ }
func NewStore(pool *pgxpool.Pool) *Store
func (s *Store) Insert(ctx, Snapshot) error
func (s *Store) Prune(ctx, olderThan time.Time) (int64, error)   // DELETE captured_at < olderThan
// trend read (T4 uses): bucket aggregation
type TrendPoint struct { Bucket time.Time; Value float64 }
func (s *Store) Trend(ctx, metric, bucket string, from, to time.Time) ([]TrendPoint, error)
```

- [ ] **Step 1: 勘查** — 读 migrations/0022(最新迁移号确认 0023 可用)+ 一个无 RLS 平台表迁移(audit_log 的 GRANT/REVOKE 模式)+ `internal/store/manager.go` 的 SystemPool 类型。确认 metric 字段列名与 spec §4 一致。
- [ ] **Step 2: 写迁移 0023** — CREATE TABLE metric_snapshots(spec §4 列)+ 索引 + GRANT/REVOKE(照 audit_log:REVOKE ALL from app_tenant/app_customer,SystemPool 走 owner)。幂等(IF NOT EXISTS)。
- [ ] **Step 3: 写失败测试** — snapshot_test.go(testcontainers,applyMigrations):Insert 一条 → 读回断言字段;Prune(now) 删旧留新;Trend 按 day bucket 聚合 2 天数据断言点数/值。`go test ./internal/metrics/ -run TestSnapshot -v` → FAIL。
- [ ] **Step 4: 实现 snapshot.go** — Insert(INSERT ... RETURNING 或 Exec)、Prune(DELETE captured_at<$1)、Trend(`SELECT date_trunc($bucket, captured_at AT TIME ZONE 'Asia/Shanghai') AS b, avg(<metric>) FROM metric_snapshots WHERE captured_at BETWEEN $from AND $to GROUP BY b ORDER BY b`;metric 列名**白名单校验**防注入——metric 是列名不能参数化,用 `map[string]string` 白名单映射到真实列名,非法 metric→error)。AvgDeliveryMs 指针 nullable。
- [ ] **Step 5: 测试 PASS + gate + 提交** — `go test ./internal/metrics/ -run TestSnapshot -v` PASS;`go build ./... && go vet ./internal/metrics/...`;`git commit -m "feat(p3): metric_snapshots migration + internal/metrics snapshot store"`。

---

### Task 2: 采样器 Sampler.RunLoop + worker 接线 + config

**Files:**
- Create: `internal/metrics/sampler.go` + `sampler_test.go`
- Modify: `cmd/wadist/main.go`(接线 `go sampler.RunLoop(ctx)`)
- Modify: `internal/config/config.go`(WADIST_METRICS_SAMPLE_INTERVAL 默认 15m、WADIST_METRICS_RETENTION_DAYS 默认 90)

**Interfaces:**
- Consumes:T1 的 `metrics.Store`。
- Produces:
```go
func NewSampler(store *Store, pool *pgxpool.Pool, interval time.Duration, retentionDays int) *Sampler
func (s *Sampler) SampleOnce(ctx) (Snapshot, error)  // 采一次:DB 聚合 → Snapshot(不落库,便于测)
func (s *Sampler) RunLoop(ctx)                        // ticker: SampleOnce → store.Insert → store.Prune; 错误只 log
```

- [ ] **Step 1: 勘查** — 读 `handleAdminStats`(internal/api/admin_api.go:52,聚合 SQL 抄它)+ config.go 的 getenv/getdur 助手 + cmd/wadist/main.go 的 goroutine 启动模式(找现有 `go xxx.RunLoop(ctx)` 先例)。
- [ ] **Step 2: 写失败测试** — sampler_test.go(testcontainers):seed account_devices(不同 ban_status/quarantine/health)+ campaign_recipients(pending/sent/failed + delivered_at/created_at 时间戳,含近1h 与更早的)→ `SampleOnce` 断言各计数正确(accounts_*/queue_backlog/processed_1h/delivered_1h/failed_1h/avg_delivery_ms;边界:无近1h 数据时 avg_delivery_ms=nil)。`go test ./internal/metrics/ -run TestSampler -v` → FAIL。
- [ ] **Step 3: 实现 SampleOnce** — 聚合查询(account_devices 计数照 handleAdminStats;campaign_recipients:queue_backlog=count(state=pending),processed_1h=count(state IN(sent,failed,skipped) AND updated_at>now()-1h),delivered_1h=count(delivered_at>now()-1h),failed_1h=count(state=failed AND updated_at>now()-1h),avg_delivery_ms=avg(EXTRACT(EPOCH FROM delivered_at-created_at)*1000) FILTER(WHERE delivered_at>now()-1h))。RunLoop:ticker(interval)→SampleOnce→Insert→Prune(now-retention);ctx.Done 退出;每步错误 log 不 panic。
- [ ] **Step 4: 测试 PASS** — `go test ./internal/metrics/ -run TestSampler -v` PASS。
- [ ] **Step 5: 接线 + config** — config 加两项(getdur/getint 默认);cmd/wadist 构建 Sampler + `go sampler.RunLoop(ctx)`(在 worker 主循环启动处,照现有 goroutine 先例)。`go build ./...`。
- [ ] **Step 6: gate + 提交** — `go vet ./...`;`git commit -m "feat(p3): metrics sampler RunLoop + worker wiring + config gates"`。

---

### Task 3: GET /admin/risk/overview + /admin/risk/accounts

**Files:**
- Create: `internal/api/metrics_api.go`
- Modify: `internal/api/router.go`
- Test: `internal/api/metrics_db_test.go`

**Interfaces:**
- Produces:
  - `GET /admin/risk/overview` → `{ban_rate, accounts_total, accounts_active, accounts_quarantined, health_buckets:{low,mid,high}, queue_backlog, delivered_rate_1h, failed_rate_1h, avg_delivery_ms, processed_1h}`(实时 DB 聚合)
  - `GET /admin/risk/accounts` → `{rows,total,stats}`:异常账号(ban_status IN(banned,flagged) OR quarantined_until>now() OR health_score<`RISK_HEALTH_THRESHOLD`(常量 70));列 jid/tenant_id+name/ban_status/health_score/quarantined_until/reason;`buildRiskAccountWhere`(reason 类型筛选 + q)+ 分页(照 SP3a)。

- [ ] **Step 1: 勘查** — 读 handleAdminStats(聚合复用)+ ListInstances(P2 的 {rows,total,stats}+buildWhere 分页模式,抄)+ account_devices 列(jid/tenant_id/ban_status/health_score/quarantined_until;确认 jid 列名与 tenant join)。
- [ ] **Step 2: 写失败测试** — metrics_db_test.go:seed 账号(各 ban_status/quarantine/health)→ overview 断言 ban_rate/health_buckets/counts;accounts 断言只返异常账号 + reason 分类 + 分页。`go test ./internal/api/ -run TestRisk -v` → FAIL。
- [ ] **Step 3: 实现** — handleAdminRiskOverview(单/多 query 聚合;delivered_rate=delivered_1h/processed_1h 保护除零)+ handleAdminRiskAccounts(buildRiskAccountWhere:reason=ban|quarantine|low_health,SystemPool,LEFT JOIN tenants,分页 stats)+ router 注册 `admin.GET("/risk/overview")`/`admin.GET("/risk/accounts")`。reason 派生列(CASE)。
- [ ] **Step 4: 测试 PASS + gate + 提交** — `go test ./internal/api/ -run TestRisk -v` PASS;`go build && go vet ./internal/api/...`;`git commit -m "feat(p3): risk overview + anomalous-accounts endpoints"`。

---

### Task 4: GET /admin/reports/trend + /tenant-consumption

**Files:**
- Modify: `internal/api/metrics_api.go` + `router.go`
- Test: `internal/api/metrics_db_test.go`

**Interfaces:**
- Consumes:T1 `metrics.Store.Trend`(需 Deps 加 `Metrics *metrics.Store` 或 handler 内 `metrics.NewStore(s.systemPool())`——后者更轻,选它)。
- Produces:
  - `GET /admin/reports/trend?metric=&bucket=hour|day|week&from=&to=` → `[{bucket, value}]`(metric 白名单:accounts_banned/queue_backlog/delivered_1h/failed_1h/avg_delivery_ms 等)
  - `GET /admin/reports/tenant-consumption?from=&to=&limit=&offset=` → `{rows,total}`:租户 settle 消耗排行(照 SP4 账单口径,wallet_ledger)

- [ ] **Step 1: 勘查** — 读 SP4 的账单/消耗查询(`handleAdminLedger`/finance_query.go 的 settle 聚合 + AT TIME ZONE)确认消耗口径;读 T1 Trend 签名。
- [ ] **Step 2: 写失败测试** — trend:seed metric_snapshots 跨天 → 按 day bucket 断言序列;非法 metric→400;tenant-consumption:seed wallet_ledger settle 多租户 → 断言排行降序 + 分页。`go test ./internal/api/ -run TestReports -v` → FAIL。
- [ ] **Step 3: 实现** — handleAdminReportsTrend(解析 metric(白名单 400)/bucket(hour|day|week 白名单)/from/to(默认近30天)→ Store.Trend)+ handleAdminReportsTenantConsumption(wallet_ledger settle 按 tenant_id 聚合 JOIN tenants,ORDER BY 消耗降序,分页)+ router。
- [ ] **Step 4: 测试 PASS + gate + 提交** — `go test ./internal/api/ -run TestReports -v` PASS;gate;`git commit -m "feat(p3): reports trend + tenant-consumption endpoints"`。

---

### Task 5: 两个 CSV 导出

**Files:**
- Modify: `internal/api/metrics_api.go` + `router.go`
- Test: `internal/api/metrics_db_test.go`

**Interfaces:**
- Produces:`GET /admin/reports/trend.csv`(+审计 reports.trend_export)、`GET /admin/reports/tenant-consumption.csv`(+审计 reports.consumption_export)。

- [ ] **Step 1: 勘查** — 读 SP4 的 `writeCSV`/`csvSanitize`(finance CSV 导出,防 `=+-@\t\r` 公式注入)+ 审计助手,复用。
- [ ] **Step 2: 写失败测试** — 断言 CSV header/行内容;**csvSanitize**:构造租户名以 `=`/`@` 开头 → 断言导出被前置撇号中和;审计行写入。`go test ./internal/api/ -run TestReportsCSV -v` → FAIL。
- [ ] **Step 3: 实现** — 两 handler 复用 trend/consumption 查询 → writeCSV(csvSanitize 每格)+ Content-Disposition attachment + recordAudit + router。
- [ ] **Step 4: 测试 PASS + gate + 提交** — PASS;gate;`git commit -m "feat(p3): reports CSV exports w/ csvSanitize + audit"`。

---

### Task 6: 前端风控看板页

**Files:**
- Create: `frontend/app/admin/risk-monitor/page.tsx` + `frontend/components/admin-risk-monitor.tsx`
- Modify: `frontend/components/admin/nav.ts`(策略中心组加 /admin/risk-monitor)、`frontend/lib/i18n/dicts/admin.ts`(admin.risk.*)

**Interfaces:** Consumes `GET /admin/risk/overview`、`/admin/risk/accounts`、`/admin/reports/trend`(小趋势图)。

- [ ] **Step 1: 勘查** — 读 admin-metrics.tsx(大盘指标卡 + Tremor 图范式)+ P2 admin-instances.tsx(ProDataTable server + stats 卡)+ 一个 Tremor 图组件(billing-trend-chart.tsx,主题 token 接法)。
- [ ] **Step 2: 页面** — admin-risk-monitor.tsx:顶部指标卡(封号率/活跃/隔离/到达率/到达延迟/积压)+ 健康分分布图(Tremor Bar/Donut,low/mid/high)+ 近期趋势小图(trend metric=accounts_banned 或 delivered_1h)+ 异常账号 ProDataTable(server,reason Tab 筛选 banned/quarantine/low_health,列 号码/租户/状态/健康分/隔离至/原因)。全文案 t()。
- [ ] **Step 3: nav + 字典 + 门槛** — nav 策略中心组加项;admin.risk.* zh/en 成对;tsc + check-i18n-keys(0 missing)+ eslint 改动文件 + build。
- [ ] **Step 4: 提交** — `git commit -m "feat(p3): admin risk-monitor page (metrics/health/anomalous accounts)"`。

---

### Task 7: 前端报表中心页 + 大盘趋势接入

**Files:**
- Create: `frontend/app/admin/reports/page.tsx` + `frontend/components/admin-reports.tsx`
- Modify: `frontend/components/admin/nav.ts`(报表入口)、`frontend/lib/i18n/dicts/admin.ts`(admin.reports.*)、大盘趋势组件(接 metric_snapshots trend)

**Interfaces:** Consumes trend/tenant-consumption + 两 CSV。

- [ ] **Step 1: 勘查** — 读 admin-billing.tsx(SP4 CSV 导出按钮 + 图表 + 范围选择范式)+ 大盘趋势卡当前数据源(替换为 /admin/reports/trend)。
- [ ] **Step 2: 报表页** — admin-reports.tsx:趋势图(指标下拉 + 日/周/月 bucket 切换 + 时间范围)+ 租户消耗排行表(ProDataTable server)+ 两 CSV 导出按钮(下载 + 错误 toast)。全文案 t()。
- [ ] **Step 3: 大盘趋势接入** — 大盘现有趋势卡改从 `/admin/reports/trend` 取真实历史(若当前是占位/实时,替换)。
- [ ] **Step 4: nav + 字典 + 门槛 + 提交** — nav 加报表入口;admin.reports.* zh/en;tsc+check-i18n-keys+eslint+build;`git commit -m "feat(p3): admin reports page (trend/consumption/CSV) + dashboard trend from snapshots"`。

---

### Task 8: 全量终验 + 浏览器冒烟

- [ ] **Step 1: 全量残余中文扫描**(排除 landing/app/en/nav.ts/字典)→ 第④类遗漏归零。
- [ ] **Step 2: 门槛** — 后端 `make gate`(tidy/vet/test-race/labels;预存 vuln GO-2026-5856 不阻断)+ 前端 tsc/check-i18n-keys(0 missing)/lint(baseline 不增)/build。
- [ ] **Step 3: 浏览器冒烟**(控制器 playwright)— /admin/risk-monitor + /admin/reports × 亮暗 × 中英;确认指标卡/图表/异常表/CSV 按钮渲染,图表随主题切换,EN 态无中文残留。**注:此实例 metric_snapshots 初期空**——趋势图会空到采样器跑够时间;风控 overview/accounts 用现有 account_devices 数据即时有值。
- [ ] **Step 4: 提交** — `git commit -m "chore(p3): final sweep + smoke verification"`(若有修)。

---

## 计划外(不做)

- 熔断/限流内存态可视化;阈值告警规则引擎;租户级快照切片(列预留);限流可调节;报表更多维度(留后续)。
