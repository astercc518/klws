# cluster 反指纹增强（presence/typing/优雅 logout + fence-on-send）实现设计

- 日期：2026-06-30
- 状态：实现设计草案，待评审
- 关联：[2026-06-30-高吞吐发送编排-design.md](2026-06-30-高吞吐发送编排-design.md) 第 6.3 节反指纹规则 3/4；[2026-06-30-redis-ownership-B2-impl-design.md](2026-06-30-redis-ownership-B2-impl-design.md) 第 7 节 fence-on-send
- 目标：让每条会话"连接→停留→打字→发送→停留→优雅下线"像真人，降低 WhatsApp 风控评分。
- 红线：**用户已明确授权编辑 `internal/cluster`**。本 spec 改动**全部加性**（新增接口方法 + RoutingSender 发送前序列），**不碰** 锁/registry/supervisor/会话生命周期/防双开逻辑，**不碰** billing/quota/dispatch。

---

## 1. 范围

落地主设计文档第 6.3 节的反指纹规则：

- **规则 3（连接后 dwell）**：warm 成功 → `presence(available)` → 人类停留 → 发送前 `composing(typing)` → 抖动 → 发送。
- **规则 4（优雅下线）**：驱逐时 `presence(unavailable)` → linger 排空回执 → 干净 `Disconnect`。
- 顺带落地 B2 的 **fence-on-send**（默认开）：发送前校验仍持有所有权。

whatsmeow API（v0.0.0-20260622，已核实 `presence.go`）：
```go
func (cli *Client) SendPresence(ctx, state types.Presence) error            // PresenceAvailable / PresenceUnavailable
func (cli *Client) SendChatPresence(ctx, jid types.JID, state types.ChatPresence, media types.ChatPresenceMedia) error // ChatPresenceComposing / ChatPresencePaused
```

---

## 2. ⚠️ 关键陷阱：优雅 logout ≠ whatsmeow `Logout()`

`Logout()` 会**注销 companion 设备**（清掉设备注册），等于销毁账号会话凭证——灾难。**"优雅 logout" 指：`SendPresence(Unavailable)` → linger → `Disconnect()`（仅断连，凭证保留）**。spec/实现/review 必须杜绝任何 `client.Logout()` 调用。

---

## 3. 接口扩展（`internal/cluster`）

### 3.1 `Conn` 接口加两法（types.go）
```go
type Conn interface {
    Connect(ctx context.Context) error
    Disconnect()
    SetPresence(ctx context.Context, available bool) error                 // 新增
    SendTyping(ctx context.Context, chatPhone string, composing bool) error // 新增
}
```

### 3.2 `waConn` 实现（conn_whatsmeow.go）
```go
func (c *waConn) SetPresence(ctx, available bool) error {
    st := types.PresenceUnavailable
    if available { st = types.PresenceAvailable }
    return c.client.SendPresence(ctx, st)   // available 需 push_name 已设；注册时已具备
}
func (c *waConn) SendTyping(ctx, chatPhone string, composing bool) error {
    jid := types.NewJID(chatPhone, types.DefaultUserServer)
    st := types.ChatPresencePaused
    if composing { st = types.ChatPresenceComposing }
    return c.client.SendChatPresence(ctx, jid, st, "")
}
```

> fake-test 友好：`fakeConn` 记录 presence/typing 调用序列供断言（与现有 fakeConn 风格一致）。

### 3.3 `Session.GracefulClose`（types.go）
```go
// GracefulClose 优雅下线：presence-unavailable → linger 排空回执 → Close（断连+释锁）。
// 绝不调用 whatsmeow Logout()。linger<=0 跳过停留。
func (s *Session) GracefulClose(ctx context.Context, linger time.Duration) {
    if s.conn != nil { _ = s.conn.SetPresence(ctx, false) }
    if linger > 0 { select { case <-time.After(linger): case <-ctx.Done(): } }
    s.Close(ctx)   // 复用现有 Close：先 Disconnect 后 Release（顺序不变）
}
```

---

## 4. RoutingSender 发送前序列（sender.go）

把"打字 + fence 校验"内聚进发送路径，控制面无需感知协议细节：

```go
type RoutingSender struct {
    reg        *Registry
    fenceOnSend bool          // WADIST_OWNERSHIP_FENCE_ON_SEND，默认 true
    typing     TypingPolicy   // 抖动配置
}

func (rs *RoutingSender) Send(ctx, jid, phone, body string, media *MediaHandle) (string, error) {
    sess, ok := rs.reg.Get(jid)
    if !ok { return "", errNoActiveSession }
    if sess.sender == nil { return "", errNoSendCap }

    // (A) fence-on-send：失去所有权则不发（B2 第 7 节）
    if rs.fenceOnSend && !sess.Healthy(ctx) {
        return "", ErrLostOwnership   // 上层 requeue，不计费、不双发
    }
    // (B) 打字拟人：composing → 抖动 → 发送 → paused
    if c, ok := sess.conn.(Conn); ok {
        _ = c.SendTyping(ctx, phone, true)
        sleepJitter(ctx, rs.typing.Min, rs.typing.Max)   // 默认 1.2–3.5s，按 body 长度可缩放
    }
    id, err := sess.sender.Send(ctx, phone, body, media)
    if c, ok := sess.conn.(Conn); ok { _ = c.SendTyping(ctx, phone, false) } // paused（best-effort）
    return id, err
}
```

- `ErrLostOwnership` 是新错误；现有 worker 对"send 失败"已 requeue/refund，无需改 dispatch。
- typing/presence 失败一律 best-effort（不阻断发送，记 metric）。

---

## 5. 控制面编排（时序，控制面 spec 落地，本 spec 只提供原语）

```
warm:    StartAccountWithLock → SetPresence(available) → 进入 warming
         停留 dwell ∈ [3s,10s]（控制面计时）→ resident，Send Pump 才可选中
send:    RoutingSender 内：composing → jitter → send → paused（第 4 节）
evict:   GracefulClose(ctx, linger ∈ [10s,30s])（替代直接 Close）
```

dwell/linger 的**计时**属控制面（Working-Set Manager）；presence/typing/GracefulClose 的**协议动作**属本 spec。两者解耦。

---

## 6. 配置项

| env | 默认 | 含义 |
|---|---|---|
| `WADIST_OWNERSHIP_FENCE_ON_SEND` | true | 发送前 fencing 校验（B2 决策） |
| `WADIST_TYPING_MIN_MS` / `WADIST_TYPING_MAX_MS` | 1200 / 3500 | composing 抖动区间 |
| `WADIST_DWELL_MIN_MS` / `WADIST_DWELL_MAX_MS` | 3000 / 10000 | 连接后停留（控制面用） |
| `WADIST_LINGER_MS` | 15000 | 优雅下线停留 |
| `WADIST_ANTIFP` | on | 总开关；off 时退回现状（直发、硬断），作回滚 |

---

## 7. 测试

- `waConn` 活连接无单测（需真机+4G 代理，沿用现状约束）；用 `fakeConn` 记录调用：
  - `TestRoutingSenderSequence`：断言 send 前 `composing`、send 后 `paused`、顺序正确。
  - `TestFenceOnSend`：`Healthy()==false` 时 `Send` 返回 `ErrLostOwnership` 且**未**触达底层 sender。
  - `TestGracefulClose`：断言 `SetPresence(unavailable)` 先于 `Disconnect`，且**从不**调用 Logout（fakeConn 无 Logout 方法即编译期保证）。
  - `TestAntifpOff`：`WADIST_ANTIFP=off` 时无 presence/typing 调用（回滚等价现状）。
- 活体行为（presence/typing 是否真降封号率）由 canary A/B 验证（主设计文档第 11 节）。
- `make gate` / `-race` 全绿；现有 cluster 测试不破。

---

## 8. 红线影响评估

- 改 `internal/cluster`（已授权）：**加性**——`Conn` 加 2 法、`Session.GracefulClose`、`RoutingSender` 发送前序列 + fence。
- **未改**：`AcquireDeviceLock`/`DeviceLock`/`Registry`/`Supervisor`/`Session.Close` 的锁与生命周期逻辑、防双开顺序（GracefulClose 复用 `Close` 的 disconnect-then-release 次序）。
- **未碰**：`billing`/`sendgate`/`dispatch`/`store`（fence 调的是会话已持有的 `DeviceLockHandle.Healthy()`，B2 实现）。
- 杜绝 `client.Logout()`。

---

## 9. 不做（YAGNI）/ 风险

**不做**：已读回执模拟、随机滚动联系人互动等更深拟人——先上 presence/typing/dwell/linger，按 canary 结果再加。

**风险**：
- presence(available) 会让账号对联系人显示"在线"——对群发账号可接受/符合"真人"预期。
- typing 抖动增加每条 send 的尾延迟（默认 1.2–3.5s）——但稳态 τ 远低于硬件上限，吞吐不受影响；`WADIST_ANTIFP=off` 可一键回滚。

---

## 10. 验收标准

- 三条反指纹动作（connect-presence、pre-send-typing、graceful-unavailable）在 fake 测试下序列正确；`-count=1` 稳定。
- fence-on-send 在失主时阻断发送，不双发不计费。
- 全程无 `client.Logout()`；现有 cluster 测试与 `make gate` 绿。
- `WADIST_ANTIFP=off` 行为等价改造前（回滚验证）。
