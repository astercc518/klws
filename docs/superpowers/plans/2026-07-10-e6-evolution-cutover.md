# E6 Evolution Cutover Implementation Plan（全量切透 + 删 whatsmeow）

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax.

**Goal:** 把 E0–E5 的休眠件组合成 live 发送/连接链、门控翻默认到 Evolution、删除 whatsmeow/wabadger，使 Evolution API 成为唯一数据面。

**Architecture（model A — 复用 Conn 接缝）:** 今天 live 路径持有本地 whatsmeow socket（Registry of `waConn` + 温驻留 + ghost reaper + 所有权租约）。Evolution 模型下**连接由 Evolution 服务端持有**，故 `SessionFactory` 改建 `evoInstance`（E1 已实现 `cluster.Conn`/`PresenceConn`/`LivenessConn`）：Registry/orchestrator/reaper 语义从「持 socket」变为「管 Evolution 实例（create/connect/logout/delete）」，liveness 来自 webhook 回填的 state。发送不走 Registry 的 per-session sender，而走 SendWorker 的组合 `Sender` 链（evoSender→retry→limiter→breaker），按 jid→instance→node→`EvoClient.SendText` HTTP 直发。

**顺序（安全优先）:** Phase 1 接线全部门控在 `WADIST_SENDER`/`WADIST_CONN`（默认仍 whatsmeow，**可回滚**）→ Phase 2 翻默认 → Phase 3 删 whatsmeow（不可逆）。中途任一 Phase 结束都是编译/gate 绿的连贯状态。

**Tech Stack:** Go、cmd/wadist 接线、node.Orchestrator/SessionFactory、dispatch 组合、internal/cluster EvoCluster/evoInstance、go.mod。

## ⚠️ Go-live 风险（用户已知并授权全量盲切，此处存档）

- **真机 0 验证**：11 处 `TODO(evo-verify)`（REST 路径/字段/webhook 事件名/ack/状态码）**至今未对真机**。见 `docs/evolution-real-machine-verification-runbook.md`。
- **V0 webhook 鉴权几乎必错**：`webhook_evolution.go:verify` 假设 `X-Evolution-Signature=hex(HMAC-SHA256(secret,body))`，Evolution v2 原生不发此签名。**缓解：本 E6 让 `WADIST_EVOLUTION_WEBHOOK_SECRET` 默认空（=接受，dev-bypass），生产在真机确认鉴权机制前保持空 + 用网络隔离作信任边界**。切勿在未确认前设 secret，否则回执全断（401）。
- V1 登录后本账号 jid 字段、V2 messages.update data 形状、V3 403-as-permanent 同 runbook——上线后首个真机回环必须逐条核对，错则改对应 file:line。

## Global Constraints

- **红线正确性不变式存活**：balance=Σledger、frozen=Σ未结charge、message_id 幂等、sent_today 对称、单账号单会话（Evolution 侧 + Go 所有权租约双保）。
- **顺序**：Phase 1（门控接线）先做且 whatsmeow 默认+保留；Phase 3 删除放最后。
- **`make gate` 每 task 后绿**（除预存在 `GO-2026-5856`）；gofmt 干净；提交带 `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>` trailer。
- **删除只在 Phase 3**：Phase 1/2 不得删 whatsmeow/wabadger（回滚路径）。

**执行前置**：从 `main`（现 `cb49265`）新建分支 `feat/evolution-e6-cutover`，先确认基线编译。

---

# Phase 1 — 接线（门控，可回滚）

## Task 1: 配置门控 + Evolution 集群配置

**Files:** Modify `internal/config/config.go` + test。

**Interfaces / Produces:** `Config` 增：
- `Sender string`（`WADIST_SENDER`，默认 `whatsmeow`）
- `Conn string`（`WADIST_CONN`，默认 `whatsmeow`）
- `EvolutionNodes map[string]string`（`WADIST_EVOLUTION_NODES`，格式 `node1=http://h1:8080,node2=http://h2:8080`；空则回退单节点 `{default: EvolutionBaseURL}`）
- `EvolutionCapPerNode int`（`WADIST_EVOLUTION_CAP_PER_NODE`，默认 `800`）
- `EvolutionWebhookURL string`（`WADIST_EVOLUTION_WEBHOOK_URL`，控制面回调地址，传给 CreateInstance）
- `EvoLimiterMax int`（`WADIST_EVO_LIMITER_MAX`，默认 `1`）、`EvoBreakerThreshold int`（默认 `5`）、`EvoBreakerCooloffSec int`（默认 `30`）、`EvoRetryAttempts int`（默认 `4`）

- [ ] Step 1: 写测试——默认值 + `WADIST_EVOLUTION_NODES` 解析（`a=u1,b=u2`→map；空→`{default:BaseURL}`）。RED。
- [ ] Step 2: 跑测试确认 FAIL。
- [ ] Step 3: 加字段 + `getenv`/`intEnv` 加载 + 一个 `parseNodes(s, fallbackURL) map[string]string` helper（`strings.Split` on `,` then `=`；空/畸形项跳过；空结果→`{"default": fallbackURL}`）。
- [ ] Step 4: 跑测试 PASS。
- [ ] Step 5: `git commit -m "feat(config): evolution cutover gates + cluster/node config"`

## Task 2: 发送侧组合 + SendWorker 选择（门控 WADIST_SENDER）

**Files:** Create `internal/store/instance_node.go`（`NodeForInstance`）+ test；Modify `cmd/wadist/main.go`（构造 evo Sender 链 + 按门控选 sender）。

**Interfaces / Produces:**
- `store`: `func (m *Manager) NodeForInstance(ctx, instanceName string) (string, bool, error)`（`SELECT evo_node FROM account_instances WHERE instance_name=$1`，SystemPool）。
- `cmd/wadist`（非红线逻辑，纯接线）：
  - `evoCluster := cluster.NewEvoCluster(cfg.EvolutionNodes, cfg.EvolutionAPIKey)`
  - 一个 `evoSendAdapter`（实现 `dispatch` 的 `evoSendAPI` 即 `SendText(ctx, instance, phone, body)(string,error)`）：instance→`mgr.NodeForInstance`→`evoCluster.For(node)`→`EvoClient.SendText`→返回 `.RemoteID`；节点缺失→error。
  - `route := func(ctx, jid)(string,bool,error){ return mgr.InstanceForJID(ctx, jid) }`
  - `evoSender := dispatch.NewEvoSender(evoSendAdapter, route)`
  - `evoChain := dispatch.NewCircuitBreakerSender(dispatch.NewPerInstanceLimiter(dispatch.NewRetrySender(evoSender, cfg.EvoRetryAttempts, 200*ms, 5*s).WithPermanent(cluster.IsPermanent), cfg.EvoLimiterMax), cfg.EvoBreakerThreshold, cooloff)`
    - **注意组合顺序**：外层 breaker→limiter→retry→evoSender。retry.WithPermanent 用 `cluster.IsPermanent`（cmd 桥接，dispatch 不 import cluster）。
  - `var sender dispatch.Sender = routing; if cfg.Sender == "evolution" { sender = evoChain }`；`NewSendWorker(..., sender, ...)`。
  - **throttle→governor**：在 evoSendAdapter 里，若 `cluster.IsThrottle(err)` 则调 `governor.Nudge`（若 governor 暴露了乘性减 hook；否则记 TODO 接线点，不阻塞）。

- [ ] Step 1: 写 `NodeForInstance` DB 测试（seed 一行断言节点；未知→ok=false）。RED。
- [ ] Step 2: FAIL。
- [ ] Step 3: 实现 `NodeForInstance`。
- [ ] Step 4: PASS。
- [ ] Step 5: cmd/wadist 接线（上述），`go build ./...` 通过；默认 `WADIST_SENDER=whatsmeow` 时 `sender==routing` 零行为变更。
- [ ] Step 6: `git commit -m "feat(dispatch): compose evolution send chain, gate SendWorker by WADIST_SENDER"`

## Task 3: 连接侧——SessionFactory 建 evoInstance（门控 WADIST_CONN）+ webhook liveness

**Files:** Modify `cmd/wadist/main.go`（factory 分支）；Modify `internal/api/webhook_evolution.go`（把 `UpdateState` 路由进 live evoInstance——经一个注入的 `stateSink`）；可能 Modify `internal/cluster`（暴露 registry 查 evoInstance 的方法，或 factory 侧持 map）。

**设计:**
- 当 `cfg.Conn == "evolution"`，`SessionFactory` 改为：
  1. `insts, _ := mgr.NodeCounts(ctx)`；`node, ok := nodering.AssignNode(ring, insts, jid, cfg.EvolutionCapPerNode)`（ring=`nodering.New(200, evoCluster.Nodes()...)`）；`!ok`→error（all-full/empty-ring）。
  2. `instanceName := instanceNameFor(jid)`（稳定，如 `wa_<tenant>_<node>_<hash>`；或复用已存 `mgr.InstanceForJID`——已建过则取旧 instance+node，**不重分配**，遵守 spill 粘性）。
  3. `mgr.UpsertInstance(ctx, InstanceRow{InstanceName, JID: jid, TenantID, EvoNode: node, State:"created"})`。
  4. `client, _ := evoCluster.For(node)`；`ei := cluster.NewEvoInstance(client, instanceName, cfg.EvolutionWebhookURL, proxyBinding)`；`ei.Connect(ctx)`（=create+connect on Evolution）。
  5. `return cluster.NewSessionWithSender(jid, ei, lock, nil)`——sender 传 nil（Evolution 发送不走 session sender，走 SendWorker 组合链）。**确认 `NewSessionWithSender` 接受 nil sender**；若不接受，用 `NewSession`。
- **webhook liveness 回填**：`connection.update` 要把 state 更新到 live 的 `evoInstance`（`UpdateState`），使 reaper 的 Liveness 生效。经一个 `stateSink interface{ UpdateInstanceState(instanceName, state string) }` 注入 webhook（默认实现：查 registry by jid（需 instance→jid）→ session.conn.(*evoInstance).UpdateState）。**这是 E6 最微妙的接线**；若 registry 无按 instance 反查，则先只更 DB（`SetInstanceState` 已在），evoInstance 的内存 state 由 reaper 容忍「不可探测=假设健康」兜底，`UpdateInstanceState` 内存回填标 TODO 后续。

- [ ] Step 1: 先加一个可测的纯 helper `instanceNameFor(tenantID int64, node, jid string) string`（确定性）+ 单测。RED→GREEN。
- [ ] Step 2: cmd/wadist factory 加 evolution 分支（上述），`go build ./...` 通过；`WADIST_CONN=whatsmeow`（默认）时走原 waConn 分支零变更。
- [ ] Step 3: webhook `SetInstanceState`（DB）已在 E3；内存 `UpdateInstanceState` 若接不上则记 TODO，不阻塞。
- [ ] Step 4: `go build ./...` + `make gate`。
- [ ] Step 5: `git commit -m "feat(node): SessionFactory builds evoInstance under WADIST_CONN=evolution (sharded)"`

## Task 4: healthSink→真 sendgate + E3 carry-forwards

**Files:** Modify `cmd/wadist`（webhook 若在 wadist 起则接 sendgate；**注意 webhook 实际在 cmd/console 起**——见下）；Modify `internal/api/webhook_evolution.go`（Record 非 nil DB error→500；BindInstanceJID first-see-only）；Modify `cmd/console/main.go`（Deps 传 health=真 sendgate 或保持 nil）。

**决策:** webhook 由 cmd/console（api server）提供，sendgate 由 cmd/wadist（worker）构造。二者经**共享 Redis** 通信。E6 让 cmd/console 也构造一个 `sendgate.SendGate`（Redis 已在 console），传入 `Deps.Health`，webhook 注册时传给 `NewEvolutionWebhook` 第 4 参。若 console 构造 sendgate 成本过高，退化：health 仍 nil，健康信号接线记为 E6 后续（不阻塞发送主链）。

- [ ] Step 1: webhook handler：`messages.update` 的 `rec.Record` 返回非 nil error 时 `c.Status(500)`（0 行匹配非 error，不触发风暴）；`BindInstanceJID` 改为「仅当前 jid 为空才绑」（store 层加 `BindInstanceJIDIfUnset` 或 handler 先 `JIDForInstance` 判空）。加/改单测。
- [ ] Step 2: healthSink 接线（console 构造 sendgate → Deps.Health → webhook 第 4 参），或记 nil + TODO。
- [ ] Step 3: `make gate`。
- [ ] Step 4: `git commit -m "feat(api): webhook 500-on-db-error + first-see jid bind; wire health sink"`

---

# Phase 2 — 翻默认（config 默认变更）

## Task 5: 翻默认到 Evolution

- [ ] Step 1: `internal/config/config.go`：`Sender` 默认 `whatsmeow`→`evolution`；`Conn` 默认 `whatsmeow`→`evolution`。改对应测试断言。
- [ ] Step 2: `make gate` 绿（此刻默认路径=Evolution，但代码仍双栈可回滚 via `WADIST_SENDER=whatsmeow`）。
- [ ] Step 3: `git commit -m "feat(config): default WADIST_SENDER/WADIST_CONN to evolution (flip)"`

---

# Phase 3 — 删 whatsmeow（不可逆，机械）

> 删除只在此 Phase。目标：`grep -r whatsmeow internal/ cmd/ = 0`，go.mod 去 whatsmeow/badger。

## Task 6: 引入本地 Logger 接口替 waLog.Logger

**Files:** Modify `internal/log/log.go`（wrap zap 成本地 `Logger` 接口而非 waLog.Logger）；sweep 27 处 `waLog.Logger` 签名 → `log.Logger`（本地接口，方法集 `Warnf/Infof/Errorf/Debugf/Sub(string) Logger`，匹配现用法）。

- [ ] Step 1: 在 `internal/log`（或 `internal/cluster`）定义 `type Logger interface { Warnf/Infof/Errorf/Debugf string-fmt; Sub(string) Logger }`（照 waLog.Logger 实际被调方法收敛）。
- [ ] Step 2: `internal/log/log.go` 提供 zap→Logger 适配（`Noop` 也提供）。
- [ ] Step 3: 逐文件 s/`waLog.Logger`/`log.Logger`/，删 `waLog` import（orchestrator.go、manager.go 等）。`go build ./...`。
- [ ] Step 4: `make gate`。
- [ ] Step 5: `git commit -m "refactor(log): local Logger interface replacing whatsmeow waLog"`

## Task 7: 删 waConn/RoutingSender/wabadger/device store + go.mod

**Files:** Delete `internal/cluster/conn_whatsmeow.go`、`internal/cluster/sender.go`（RoutingSender）、`internal/store/wabadger/`（整包）、`internal/store/device.go`；Modify `internal/store/manager.go`（去 badgerDB 构造/字段）、`internal/store/proxy.go`（删 `ApplyProxy(*whatsmeow.Client)`）、`cmd/wadist/main.go`（删 whatsmeow factory 分支 + routing sender——此时默认已 evolution，删 whatsmeow 分支后 factory 只剩 evolution）；`go.mod`（`go mod tidy` 去 whatsmeow/badger）。

- [ ] Step 1: 删 whatsmeow-only 分支/文件；把 factory 收敛为只建 evoInstance；SendWorker sender 收敛为只 evoChain（去 WADIST_SENDER 门控或留作 no-op）。
- [ ] Step 2: `go build ./...`——修所有编译错（受影响：node、store、cmd）。
- [ ] Step 3: `go mod tidy`；确认 `grep -r "go.mau.fi/whatsmeow\|dgraph-io/badger" go.mod = 空`。
- [ ] Step 4: `grep -rn whatsmeow internal/ cmd/ --include=*.go | grep -v _test = 空`（wabadger difftest 若依赖 whatsmeow 也删）。
- [ ] Step 5: `make gate`。
- [ ] Step 6: `git commit -m "feat!: remove whatsmeow/wabadger — evolution is the only data plane"`

## Task 8: 死代码清理 + 全量 gate + runbook 回填提醒

- [ ] Step 1: 删随之死掉的：`internal/store/wabadger` difftest、`internal/capacity`（若过时）、ghost reaper 中 whatsmeow-specific 探测（若有）、`GetDeviceStore` 残留引用。谨慎——reaper/registry/ownership 仍服务 evoInstance 生命周期，**不要误删**。
- [ ] Step 2: `make gate` 全绿（除 GO-2026-5856）。
- [ ] Step 3: 更新 `docs/superpowers/specs/2026-07-...-design.md` §9 与记忆：标注「E6 已合并，账号需全量重扫码，真机验证仍待做」。
- [ ] Step 4: `git commit -m "chore(evolution): dead-code sweep after whatsmeow removal"`

---

## Self-Review（对 spec §7 E6）

- **live 组合 evoSender→retry→limiter→breaker 塞 SendWorker + WADIST_SENDER** → T2。
- **创建路径 NodeCounts→AssignNode→evo_node→EvoCluster.For** → T3。
- **throttle→governor** → T2（暴露则接，否则 TODO）。**healthSink→真 sendgate** → T4。
- **门控翻默认** → T5。**删 whatsmeow/wabadger + go.mod + 全量重扫码** → T6/T7/T8。
- **E3 carry-forwards** → T4。**E4/E5 carry** → runbook/后续（403 真机确认、limiter sems 淘汰、分片 TOCTOU 串行化）。
- **不变式**：所有 task 后 `make gate` 绿守护。
- **占位符**：T3/T6/T7 标注了「实现者需读现码适配」的点（factory 分支、Logger 方法集收敛、编译错修复）——这是大型删除任务的正常形态，非占位。

**执行注意**：Phase 1 每 task 后默认路径仍 whatsmeow、可回滚；Phase 2 翻默认后仍可 `WADIST_SENDER=whatsmeow` 回滚；Phase 3 后不可逆。**Go-live 前务必先跑真机验证 runbook 钉死 11 个 `TODO(evo-verify)`**——本 E6 只保证代码切换正确，不保证协议假设正确。
