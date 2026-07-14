# P3 风控监控看板 + 报表中心(含指标快照存储)设计

日期:2026-07-14
状态:已获用户批准(P3 两问定案)
上位:`2026-07-13-platform-reskin-crud-design.md` 模块③⑧(+ ① 大盘趋势)

## 1. 目标

补齐两大缺口:③风控与发送监控(实时看板)、⑧报表中心(历史趋势/排行/导出)。新增指标快照存储(worker 采样),同时喂饱大盘①的真实历史趋势。

## 2. 需求定案(P3 两问)

- **熔断器/限流内存态**:第一版**不做**内存态熔断墙(worker 采样器不接 dispatch 内部态)。风控看板只用 DB 可算指标。熔断/限流可视化标注后续。
- **告警**:第一版做**异常账号明细表**(封禁/隔离/低健康分账号列表+原因),不做阈值告警规则引擎。

## 3. 架构

### 3.1 分层
- **实时看板**(风控):console 直接 DB 聚合(照现有 `handleAdminStats` 模式,SystemPool),无需快照——封号率/健康分布/隔离账号/到达率/到达延迟/队列积压。
- **历史趋势/报表**:读新表 `metric_snapshots`(worker 定时采样落库的时间序列)。
- **worker 采样器**:worker 进程(cmd/wadist)加一个定时器(默认 15min),把瞬时指标 DB 聚合后写 `metric_snapshots`。**第一版只采 DB 可算指标**(不读 dispatch 内存态);采样器本身只读现有表 + 写快照表,不碰引擎业务逻辑。

### 3.2 红线
- 新增 `internal/api` 只读端点 + `internal/metrics`(新叶子包:采样器 + 快照读写)+ 迁移(metric_snapshots 表)。
- **不改** billing/dispatch/store 引擎业务逻辑;采样器只 SELECT 聚合现有表。
- admin 端点 RoleAdmin + SystemPool;导出写审计(照 SP4 CSV 导出 + csvSanitize 防公式注入)。

## 4. 数据模型(新迁移 0023_metric_snapshots.sql)

```sql
CREATE TABLE metric_snapshots (
    id          bigserial PRIMARY KEY,
    captured_at timestamptz NOT NULL DEFAULT now(),
    -- 瞬时快照(全平台聚合;tenant_id NULL=平台级,非空=按租户切片留后续)
    tenant_id       bigint,         -- 第一版只写 NULL(平台级);列预留租户切片
    accounts_total  int NOT NULL,
    accounts_active int NOT NULL,
    accounts_banned int NOT NULL,   -- banned+flagged
    accounts_quarantined int NOT NULL,
    queue_backlog   int NOT NULL,   -- campaign_recipients state=pending
    processed_1h    int NOT NULL,   -- 近1h 处理量(state IN sent/failed/skipped, updated_at 锚点)
    delivered_1h    int NOT NULL,   -- 近1h 到达(delivered_at 锚点,精确)
    failed_1h       int NOT NULL,   -- 近1h 失败(state=failed, updated_at)
    avg_delivery_ms int             -- 近1h 平均到达延迟(delivered_at - created_at, ms, 可空)
);
CREATE INDEX ix_metric_snapshots_captured ON metric_snapshots (captured_at DESC);
-- 保留期:采样器每次采样后 DELETE captured_at < now()-interval '90 days'(幂等清理)。
-- 无 RLS:平台级运维数据,SystemPool 专用(与 audit_log 同款,GRANT/REVOKE 控制)。
```

**数据约束(勘查确认)**:`campaign_recipients` **无 `sent_at`** 列——枚举 `recipient_state_t`=pending/sent/failed/skipped(0006),`delivered_at`/`read_at` 是时间戳非状态(0016)。因此:①发送/处理量的时间锚点用 `updated_at`(state 转 sent/failed 时更新;注意 delivered/read 也会更新 updated_at,故"近1h processed"是近似"近1h 有状态变动的已处理项",可接受运维精度);②**回执延迟无精确发送锚点**,第一版改用 `delivered_at - created_at`(入队→到达,两端精确)记为 `avg_delivery_ms`,UI 标注"到达延迟(创建→到达)"而非"回执延迟"。字段随实现微调;captured_at 按 `Asia/Shanghai` 对齐日/周/月分桶在查询层做(date_trunc AT TIME ZONE)。

## 5. 后端

### 5.1 采样器(internal/metrics/sampler.go)
- `Sampler{pool, interval, retention}`;`RunLoop(ctx)`:每 interval 采样一次(DB 聚合)→ `INSERT metric_snapshots` → 清理过期。
- 采样查询复用 `handleAdminStats` 同款聚合 + 近1h 发送/到达/失败/回执延迟(campaign_recipients + receipts 时间戳)。
- 接线 cmd/wadist(worker 进程)`go sampler.RunLoop(ctx)`;门控 `WADIST_METRICS_SAMPLE_INTERVAL`(默认 15min)、`WADIST_METRICS_RETENTION_DAYS`(默认 90)。**采样失败只 log 不 panic**(不影响发送主流程)。

### 5.2 只读端点(internal/api,新 metrics_api.go)
| 端点 | 动作 |
|---|---|
| `GET /admin/risk/overview` | 实时 DB 聚合:封号率/活跃/隔离数/健康分分布(桶:0-40/40-70/70-100)/队列积压/近1h 到达率+失败率+平均到达延迟(创建→到达) |
| `GET /admin/risk/accounts` | 异常账号明细表:ban_status IN (banned,flagged) OR quarantined_until>now() OR health_score<阈值;分页+筛选(照 SP3a),列 jid/租户/ban_status/health/quarantine_until/原因 |
| `GET /admin/reports/trend` | 历史趋势:读 metric_snapshots,`?metric=<字段>&bucket=hour|day|week&from&to`,date_trunc AT TZ 分桶聚合 → 时间序列 |
| `GET /admin/reports/tenant-consumption` | 租户消耗排行:wallet_ledger settle 按租户聚合(照 SP4 账单口径)+ 分页 |
| `GET /admin/reports/trend.csv` | 趋势 CSV 导出(csvSanitize,审计 reports.trend_export) |
| `GET /admin/reports/tenant-consumption.csv` | 排行 CSV 导出(csvSanitize,审计 reports.consumption_export) |

## 6. 前端

- **风控看板** `/admin/risk-monitor`(nav 策略中心组,`ShieldAlert` 图标):顶部指标卡(封号率/活跃/隔离/到达率/回执延迟/积压)+ 健康分分布图(Tremor,接主题 token)+ 近期趋势小图(读 trend)+ **异常账号明细表**(ProDataTable server,ban/quarantine/低分筛选)。
- **报表中心** `/admin/reports`(nav 系统组或新"报表"组,`BarChart3` 图标):趋势图(指标下拉 + 日/周/月 bucket 切换 + 时间范围)+ 租户消耗排行表 + 两个 CSV 导出按钮。
- **大盘①趋势接入**:现有大盘的趋势卡从 metric_snapshots 取真实历史(替换占位/实时快照)。
- 全文案 i18n dict(`admin.risk.*`/`admin.reports.*`,zh/en 成对);图表配色用 P1 已验证 chart token。

## 7. 分任务(SDD)

- T1 迁移 0023 + `internal/metrics` 快照读写(store 层,真库测试)
- T2 采样器 Sampler.RunLoop + cmd/wadist 接线 + config 门控(采样→落库→清理,真库测试)
- T3 `GET /admin/risk/overview` + `/admin/risk/accounts`(实时聚合 + 异常明细,真库测试)
- T4 `GET /admin/reports/trend` + `/tenant-consumption`(快照趋势 + 消耗排行,date_trunc AT TZ,真库测试)
- T5 两个 CSV 导出(csvSanitize + 审计,真库测试)
- T6 前端风控看板页(指标卡 + 健康分布图 + 异常账号表)
- T7 前端报表中心页(趋势图 + 消耗排行 + CSV 导出)+ 大盘趋势接入快照
- T8 全量终验 + 浏览器冒烟(风控/报表 × 亮暗 × 中英)

## 8. 验收标准

- `/admin/risk-monitor`:实时封号率/健康分布/到达率/到达延迟/队列积压 + 异常账号明细表(筛选分页),数据与 DB 一致。
- `/admin/reports`:趋势图(指标×日/周/月×时间范围)+ 租户消耗排行 + CSV 导出(csvSanitize 防注入,写审计)。
- worker 采样器定时落库 metric_snapshots,90 天保留清理生效;采样失败不影响发送。
- 大盘趋势卡显真实历史(快照)。
- 后端 testcontainers 真库;前端 build+lint(baseline 不回归)+check-i18n-keys 0 missing;亮暗×中英冒烟。

## 9. 计划外(不做)

- 熔断器/限流内存态可视化(需 worker 采样器接 dispatch 内部态,后续);阈值告警规则引擎;租户级快照切片(列已预留,第一版只平台级);限流旋钮的可调节(涉引擎写入,单独立项)。
