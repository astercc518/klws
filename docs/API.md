# wadist 接口参考(API Reference)

各 `internal/*` 包的导出接口、语义、错误哨兵与调用示例。签名逐字取自源码。架构总览见 [`ARCHITECTURE.md`](ARCHITECTURE.md)。

> 约定:所有 DB 方法首参为 `ctx context.Context`;错误均 `%w` 包裹,可用 `errors.Is` 匹配下列哨兵;金额单位为最小货币单位(整数)。

---

## 目录

[config](#config) · [store](#store) · [crypto](#crypto) · [audit](#audit) · [billing](#billing) · [sendgate](#sendgate) · [dispatch](#dispatch) · [cluster](#cluster) · [node](#node) · [metrics](#metrics) · [capacity](#capacity) · [canary](#canary) · [关键流程示例](#关键流程示例)

---

## config

```go
func Load() (*Config, error)         // 从 WADIST_* env 装载;缺 PostgresDSN 报错;密钥空→nil(security disabled)
func (c *Config) Store() store.Config // 投影出 store 层配置
```
`Config` 字段(env 见 [README](../README.md#配置环境变量)):`PostgresDSN/RedisAddr/NodeID/NodeRegion`、`MaxOpenConns/MaxLockConns int32`、`MaxConcurrentStarts/AsynqConcurrency int`、`MetricsAddr`、`ShutdownTimeout/PreStopDelay/HeartbeatInterval/NodeStaleness/TakeoverScanInterval time.Duration`、`MasterKey/BlindIndexKey []byte`、`AppTenantDSN/AppSystemDSN`、`CanaryPercent uint8`。

---

## store

PG 双池(biz/lock)+ whatsmeow sqlstore;advisory-lock 互斥/fencing;代理绑定;RLS;PII 双写。

**构造 / 池**
```go
func Init(ctx, cfg Config, logger waLog.Logger) (*Manager, error)       // 单例(生产入口)
func NewManager(ctx, cfg Config, logger waLog.Logger) (*Manager, error) // 非单例(测试/多实例)
func (m *Manager) Pool() *pgxpool.Pool        // = BizPool()
func (m *Manager) BizPool() *pgxpool.Pool
func (m *Manager) TenantPool() *pgxpool.Pool  // app_tenant(受 RLS);未配 DSN 时别名 bizPool
func (m *Manager) SystemPool() *pgxpool.Pool  // app_system(BYPASSRLS,跨租户作业)
func (m *Manager) Close()                     // 关 biz/lock/sqlDB(幂等)
```

**会话锁(fencing)**
```go
func (m *Manager) AcquireDeviceLock(ctx, accountJID string) (*DeviceLock, error) // ErrDeviceLocked=他节点持有
func (l *DeviceLock) Healthy(ctx) bool   // 查 pg_locks 确认本 backend 仍持锁(非仅 Ping)→ guardSession 据此防双开
func (l *DeviceLock) Release(ctx)        // pg_advisory_unlock + 归还连接
func (l *DeviceLock) BackendPID() uint32 // 测试辅助:锁连接的 PG backend pid
```

**RLS 租户事务**
```go
func (m *Manager) WithTenant(ctx, tenantID int64) (pgx.Tx, error) // 开 tenantPool 事务 + SET LOCAL;调用方负责 Commit/Rollback
```

**代理池**
```go
func (m *Manager) BindProxy(ctx, accountJID, countryCode string) (*ProxyBinding, error) // 原子绑定;ErrNoProxyAvailable
func (m *Manager) GetBoundProxy(ctx, accountJID string) (*ProxyBinding, error)          // 读回缓存代理;ErrProxyNotBound
func (m *Manager) ReleaseProxy(ctx, accountJID string) error
func (m *Manager) ReportProxyFailure(ctx, proxyID int64) (dead bool, err error)         // 失败计数;dead=自动停用
func (m *Manager) ReportProxySuccess(ctx, proxyID int64, latencyMs int) error
func ApplyProxy(client *whatsmeow.Client, b *ProxyBinding) error                        // 连接前注入代理
```
`ProxyBinding{ ProxyID int64; ProxyURL, ProxyType, Country string }`

**设备/账号/集群**
```go
func (m *Manager) GetDeviceStore(ctx, accountJID string) (*waproto.Device, error)       // ErrDeviceNotFound
func (m *Manager) ListActiveAccounts(ctx) ([]string, error)                             // ban_status='active'
func (m *Manager) ListUnownedActiveAccounts(ctx) ([]string, error)                      // owner_node IS NULL
func (m *Manager) ClaimAccount(ctx, accountJID, nodeID string) error                    // 写 owner_node(抢锁后)
func (m *Manager) StaleOwnedAccounts(ctx, staleness time.Duration) ([]string, error)    // 过期节点名下账号
func (m *Manager) UpsertNodeHeartbeat(ctx, nodeID string) error
func (m *Manager) DeregisterNode(ctx, nodeID string) error                              // 清 owner_node + 删节点行
```

**PII / 压制**(需 `crypto.Cipher` + 32 字节盲索引键)
```go
func (m *Manager) AddRecipientPII(ctx, c *crypto.Cipher, blindKey []byte, tenantID, campaignID int64, phone, country string, vars []byte) (recipientID int64, err error) // 双写 + 盲索引去重
func (m *Manager) SetDevicePhonePII(ctx, c *crypto.Cipher, tenantID int64, accountJID, phone string) error
func (m *Manager) AddSuppression(ctx, blindKey []byte, tenantID int64, phone, reason string) error
```

**错误哨兵**:`ErrDeviceNotFound` `ErrDeviceLocked` `ErrProxyNotBound` `ErrNoProxyAvailable` `ErrAccountMissing`

---

## crypto

信封加密(KEK 包裹分租户 DEK)+ HMAC 盲索引 + 驻留 Router。

```go
func NewKeyRepo(pool *pgxpool.Pool, masterKey []byte) (*KeyRepo, error) // masterKey 必须 32 字节
func (k *KeyRepo) CreateTenantKey(ctx, tenantID int64) (version int32, err error) // 生成新版本 DEK
func (k *KeyRepo) GetCurrentDEK(ctx, tenantID int64) (dek []byte, version int32, err error) // ErrKeyNotFound
func (k *KeyRepo) GetDEK(ctx, tenantID int64, version int32) ([]byte, error)
func (k *KeyRepo) Shred(ctx, tenantID int64) error  // 删 DEK→密文不可恢复;⚠️ 明文列暂留,单独不满足 GDPR 擦除

func NewCipher(kr *KeyRepo) *Cipher
func (c *Cipher) Encrypt(ctx, tenantID int64, plaintext []byte) ([]byte, error) // 输出 version||nonce||ct
func (c *Cipher) Decrypt(ctx, tenantID int64, blob []byte) ([]byte, error)      // 按 blob 内 version 取 DEK
func (c *Cipher) Rotate(ctx, tenantID int64) (int32, error)                     // = CreateTenantKey;旧密文仍可解

func BlindIndex(key []byte, value string) []byte // HMAC-SHA256,32 字节,确定性;key 须独立于 KEK

func NewRouter(def *pgxpool.Pool) *Router
func (r *Router) Register(region string, pool *pgxpool.Pool)
func (r *Router) PoolFor(region string) *pgxpool.Pool // 未知 region → default
```
**错误哨兵**:`ErrKeyNotFound`

---

## audit

append-only 审计写入(支持同事务原子)。

```go
func NewAuditWriter(pool *pgxpool.Pool) *AuditWriter // pool 需对 audit_log 有 INSERT(app_system/superuser)
func (w *AuditWriter) Record(ctx, e AuditEntry) error                    // 独立写
func (w *AuditWriter) RecordTx(ctx, tx pgx.Tx, e AuditEntry) error       // 同业务事务写(原子)
```
`AuditEntry{ TenantID, ActorID, ResourceID int64; Action, ResourceType string; Details []byte /*jsonb*/ }`

---

## billing

冻结资金计费 + 审核退款 + 对账。可选注入 RLS / 审计(nil→回退,零行为变化)。

```go
func NewRepo(pool *pgxpool.Pool) *Repo
func (r *Repo) UseTenantRLS(b TenantTxBeginner) *Repo // 接入 RLS(store.Manager 满足);链式返回
func (r *Repo) UseAudit(aw *audit.AuditWriter) *Repo  // 接入审计(退款审批同事务写)

func (r *Repo) Hold(ctx, h HoldRequest) (*Charge, error)              // 余额→冻结;ErrInsufficientFunds/ErrWalletLocked;message_id 幂等
func (r *Repo) Settle(ctx, tenantID int64, messageID string) error    // 冻结→消费(发送成功);幂等
func (r *Repo) RequestRefund(ctx, tenantID int64, messageID, reason string) error // 冻结保留→审核队列
func (r *Repo) ApproveRefund(ctx, refundID, adminID int64, note string) error // 冻结→余额;写审计;ErrRefundConflict
func (r *Repo) RejectRefund(ctx, refundID, adminID int64, note string) error  // 冻结→消费;写审计
func (r *Repo) Topup(ctx, tenantID, amount int64, ref string) error           // 充值(写 ledger)

func (r *Repo) ReconcileTenant(ctx, tenantID int64) (*Report, error) // 单租户对账(单快照)
func (r *Repo) ReconcileAll(ctx) ([]Report, error)
func (rep Report) Healthy() bool                                     // 无漂移
func NewDriftHandler(repo *Repo, autoLock bool, notify func(context.Context, Report)) *DriftHandler
func (h *DriftHandler) HandleDrift(ctx, rep Report) error            // 告警 + 可选锁钱包
func RunNightlyReconciliation(ctx, repo *Repo, h *DriftHandler) (int, error)
```
`HoldRequest{ TenantID int64; AccountJID, MessageID, CountryCode string; Amount int64 }` · `Charge{ ID int64; State string }`
**不变式**(Report):`WalletBalance==LedgerBalance` 且 `WalletFrozen==ChargesFrozen`。
**错误哨兵**:`ErrInsufficientFunds` `ErrChargeConflict` `ErrRefundConflict` `ErrNotFound` `ErrWalletLocked`

---

## sendgate

防封号准入:温号配额曲线 × 健康度 × Redis Lua 原子准入 × 熔断隔离。

```go
func NewAdmission(rdb *goredis.Client) *Admission
func NewSendGate(pool *pgxpool.Pool, adm *Admission, baseGap time.Duration) *SendGate

func (g *SendGate) Admit(ctx, jid string, now time.Time) (Decision, error) // 综合配额/隔离/pacing 判定
func (g *SendGate) ApplyHealthSignal(ctx, jid, signal string, cooloff time.Duration) error // 健康度评分;跌破阈值→隔离
func (g *SendGate) Heal(ctx) error // 回血作业

func EffectiveQuota(registeredAt time.Time, health int, now time.Time) int // 温号曲线×健康折扣(Go 份)
```
`Decision{ Allow bool; Reason string; Ticket *Ticket }` — `Reason ∈ {ok, not_active, quarantined, daily_quota, pacing}`。被拒(`!Allow`)无 Ticket;通过时 `Ticket.Release(ctx)` 在发送失败需回退配额时调用。
`signal ∈ {delivered(+1), undelivered(-2), conn_churn(-5), recipient_block(-10), wa_warning(-30)}`;`const QuarantineThreshold = 40`。
**错误哨兵**:`ErrAccountNotFound`

---

## dispatch

分发编排 + 发送 worker。`WithMetrics`/`WithCanary` 为可选 setter(默认 nil/0)。

```go
func NewDispatcher(pool, b *billing.Repo, q Enqueuer, priceFor func(string) int64, baseGap time.Duration) *Dispatcher
func (d *Dispatcher) WithMetrics(m *metrics.Metrics) *Dispatcher
func (d *Dispatcher) DispatchRunning(ctx, batch int) (int, error) // 扫 running 活动批量派发;返回总入队数

func NewSendWorker(pool, g *sendgate.SendGate, b *billing.Repo, s Sender, u Uploader) *SendWorker
func (w *SendWorker) WithMetrics(m *metrics.Metrics) *SendWorker
func (w *SendWorker) WithCanary(pct uint8) *SendWorker
func (w *SendWorker) ProcessSend(ctx, pl SendPayload, body, mediaSha, mime string, raw []byte) error // 终态幂等→Admit→Hold→Send→Settle/refund

func NewAsynqEnqueuer(client *asynq.Client, queue string, maxRetry int) *AsynqEnqueuer
func RegisterSendHandler(mux *asynq.ServeMux, w *SendWorker, resolve SendBodyResolver)
const TypeSend = "dispatch:send"
```
**接口**(便于 fake 测试 / 路由):
```go
type Sender   interface{ Send(ctx, jid, phone, body string, media *MediaHandle) (msgID string, err error) }
type Uploader interface{ Upload(ctx, jid string, data []byte, mediaSha, mime string) (*MediaHandle, error) }
type Enqueuer interface{ EnqueueSend(ctx, p SendPayload, delay time.Duration) error }
```
`SendPayload{ TenantID,CampaignID,RecipientID int64; JID,Phone,Country,MessageID string; Vars map[string]any }`
**错误哨兵**:`ErrNoCapacity`(无可用配额账号)

---

## cluster

会话注册表 / 有序停机 Supervisor / 按 JID 路由发送。

```go
func NewRegistry() *Registry
func (r *Registry) Add(s *Session)              // 同 JID 旧会话先关(防双开)
func (r *Registry) Get(jid string) (*Session, bool)
func (r *Registry) Remove(jid string) (*Session, bool)
func (r *Registry) Len() int
func (r *Registry) ActiveSessions() int         // 满足 metrics.RegistrySnapshotProvider
func (r *Registry) CloseAll(ctx)

func NewSession(jid string, conn Conn, lock DeviceLockHandle) *Session
func (s *Session) JID() string
func (s *Session) Healthy(ctx) bool             // 委托 lock.Healthy
func (s *Session) Close(ctx)                     // 先断连后释锁(幂等)

func NewSupervisor(reg *Registry, opts SupervisorOpts) *Supervisor
func (s *Supervisor) Go(fn func(ctx) error)     // 受管 goroutine(Shutdown 取消并等待)
func (s *Supervisor) StartAccounts(ctx, jids []string, start func(ctx, jid string) (*Session, error)) error // SetLimit 限并发;start 返 nil→跳过
func (s *Supervisor) Shutdown()                 // 有序:StopIntake→loops→CloseAll→AfterSessions→CloseStore→Flush(幂等)

func NewRoutingSender(reg *Registry) *RoutingSender // 满足 dispatch.Sender
func (rs *RoutingSender) Send(ctx, jid, phone, body string, media *dispatch.MediaHandle) (string, error) // 按 jid 路由活跃会话;无会话→明确错误

func NewWAConn(device *waproto.Device, logger waLog.Logger, proxy *store.ProxyBinding) Conn // 真实 whatsmeow 连接(仅此处 import whatsmeow)
```
`SupervisorOpts{ StopIntake, CloseStore, Flush func(); AfterSessions func(ctx); Limit int; ShutdownTimeout time.Duration }`
**接口**:`Conn{Connect/Disconnect}` · `DeviceLockHandle{Healthy/Release}`(`*store.DeviceLock` 满足) · `SessionSender{Send}`

---

## node

分布式接管编排(整合 store+cluster+asynq)。

```go
func NewOrchestrator(mgr *store.Manager, reg *cluster.Registry, sup *cluster.Supervisor, nodeID string, factory SessionFactory, guardInterval time.Duration, logger waLog.Logger) *Orchestrator
func (o *Orchestrator) StartAccountWithLock(ctx, jid string) (*cluster.Session, error) // 抢锁→建会话→claim→入册→guard;锁被占→(nil,nil) 跳过
func (o *Orchestrator) RunHeartbeat(ctx, interval time.Duration) error                 // 受管循环
func (o *Orchestrator) RunTakeoverScanner(ctx, scanInterval, staleness time.Duration, enq *TakeoverEnqueuer) error

func NewTakeoverEnqueuer(client *asynq.Client, uniqueTTL time.Duration) *TakeoverEnqueuer
func (e *TakeoverEnqueuer) Enqueue(ctx, jid string) error // asynq Unique 集群级去重;ErrDuplicateTask 吞掉
func RegisterTakeoverHandler(mux *asynq.ServeMux, o *Orchestrator)
const TypeTakeover = "cluster:takeover"

type SessionFactory func(ctx, jid string, lock cluster.DeviceLockHandle) (*cluster.Session, error) // 构建+连接会话(注入以便 fake)
```

---

## metrics

有界 label 指标 + DB 聚合采集 + HTTP 端点。

```go
func New(reg *prometheus.Registry) *Metrics // 全部 collector 注册到注入的 reg
func (m *Metrics) RecordSend(outcome string)            // nil-safe(所有 Record* 均 nil-safe)
func (m *Metrics) RecordGate(allow bool, reason string)
func (m *Metrics) RecordHealthSignal(signal string)
func (m *Metrics) RecordBilling(op, outcome string)
func (m *Metrics) RecordProxy(op, outcome string)
func (m *Metrics) RecordLock(outcome string)
func (m *Metrics) RecordCohortSend(cohort, outcome string)
func (m *Metrics) ObserveBatch(assigned int)
func (m *Metrics) IncNoCapacity()

func NewDBCollector(pool *pgxpool.Pool, ttl time.Duration, snap RegistrySnapshotProvider) *DBCollector // 实现 prometheus.Collector,TTL 缓存

func NewServer(addr string, reg *prometheus.Registry) *Server
func (s *Server) Start() error      // 绑定后后台 serve
func (s *Server) Addr() string
func (s *Server) SetReady(ready bool) // 翻转 /readyz(排空时 false→503)
func (s *Server) Shutdown(ctx) error
func (s *Server) Handler() http.Handler // /metrics /healthz /readyz(供 httptest)
```
`type RegistrySnapshotProvider interface{ ActiveSessions() int }`(`cluster.Registry` 满足)
> label 值只能取有界枚举,**绝不**含 jid/tenant_id/message_id/phone(`check_metric_labels.sh` 硬门禁)。

---

## capacity

```go
func Compute(in Inputs) Plan // 纯函数:账号→节点→连接→max_connections→Redis 内存
```
`Inputs{ Accounts, MaxLockConns, MaxOpenConns, AsynqConcurrency, HeadroomConns int; DualRLSPools bool; DailyQuotaKeyBytes, PacingKeyBytes, AsynqQueueDepth, AvgTaskBytes int }`
`Plan{ AccountsPerNode, NodesNeeded, ConnsPerNode, RequiredMaxConnections, RedisMemoryMB int }`(公式见 [deploy/README](../deploy/README.md#1-容量模型internalcapacitycompute))

---

## canary

```go
func InCohort(jid string, pct uint8) bool // fnv64a(jid)%100 < pct;0→false,100→true,确定性
func Cohort(jid string, pct uint8) string // "canary" | "stable"(有界 metric label)
```

---

## 关键流程示例

### 发送一条(worker 内,简化)
```go
dec, err := gate.Admit(ctx, pl.JID, time.Now())          // 准入
if err != nil { return err }
if !dec.Allow { /* requeue,不扣费 */ return nil }
ch, err := bill.Hold(ctx, billing.HoldRequest{TenantID: pl.TenantID, AccountJID: pl.JID,
    MessageID: pl.MessageID, CountryCode: pl.Country, Amount: priceFor(pl.Country)})
if errors.Is(err, billing.ErrInsufficientFunds) { dec.Ticket.Release(ctx); return nil }
msgID, err := sender.Send(ctx, pl.JID, pl.Phone, body, media) // 路由到活跃会话
if err != nil {
    gate.ApplyHealthSignal(ctx, pl.JID, "wa_warning", 6*time.Hour) // 封号信号
    _ = bill.RequestRefund(ctx, pl.TenantID, pl.MessageID, err.Error())
    return err
}
_ = bill.Settle(ctx, pl.TenantID, pl.MessageID)            // 消费冻结
_ = ch                                                     // markSent...
```

### 起一个账号(接管/启动)
```go
sess, err := orch.StartAccountWithLock(ctx, jid)
// err==nil && sess==nil → 锁被他节点占用,跳过(正常)
// err==nil && sess!=nil → 本节点接管成功(owner_node 已写本节点)
```

### 租户隔离查询(RLS)
```go
tx, err := mgr.WithTenant(ctx, tenantID) // SET LOCAL app.current_tenant_id
if err != nil { return err }
defer tx.Rollback(ctx)
// tx 上的查询只见该租户行;越租户写被 WITH CHECK 拒绝
if err := tx.Commit(ctx); err != nil { return err }
```

### 加密 PII + crypto-shred
```go
_, _ = keyRepo.CreateTenantKey(ctx, tenantID)              // 开户:provision DEK
rid, err := mgr.AddRecipientPII(ctx, cipher, blindKey, tenantID, campaignID, phone, "US", varsJSON)
// ... 删除权:
err = keyRepo.Shred(ctx, tenantID)                         // DEK 删除→密文不可恢复(明文列收缩见 M11 债)
```
