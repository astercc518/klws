# Evolution API 真机验证 Runbook（E6 cutover 前置）

**日期**: 2026-07-10（2026-07-12 更新：文档/源码核对结论已回填并修码）

---

## ⚠️ 2026-07-12 更新：源码核对结论 + 已落地的修正

在起真机之前，先对 **Evolution API v2 官方文档 + `EvolutionAPI/evolution-api` 源码**（`whatsapp.baileys.service.ts`、`proxy.dto.ts`、`proxy.router.ts`、`openapi-v2.json`）逐条核对了 11 处假设。**其中 4 处被源码坐实为真 bug 并已就地修复**（TDD，`internal/api` + `internal/cluster` 单测全绿）：

| 命门 | 我们原来的错误假设 | 源码确认的真实值 | 已改 |
|---|---|---|---|
| **V0 webhook 鉴权** | header `X-Evolution-Signature` = HMAC-SHA256(body) | Evolution v2 **不签名 body**；它把 create 时配置的 `webhook.headers.authorization` 原样回发为 `Authorization` 头 | `webhook_evolution.go:verify` 改比对 `Authorization`（常量时间）；`CreateInstance` 配 `webhook.headers.authorization`；`EvoClient`/`EvoCluster` 加 `webhookAuth` 线 |
| **V1 本账号 jid** | `connection.update` 的 `data.remoteJid` | 本号 jid 在 **`data.wuid`**（`remoteJid` 是对端） | `webhook_evolution_types.go` 加 `wuid` + `ownJID()`；handler 用之绑定 |
| **V2 回执形状** | `messages.update` 嵌套 `data.key.id` + 数字 `data.ack` | **扁平** `data.keyId` / `data.fromMe` / `data.status`（无嵌套 key、无数字 ack） | 加扁平字段 + `msgID()/fromMe()` 双形状容错；`status` 串 `DELIVERY_ACK/READ/PLAYED/SERVER_ACK` 语义已对 |
| **C2 代理绑定** | `/instance/create` body 平铺 `proxyHost/proxyPort/...` | v2 **无视 create 里的 proxy**；须独立 `POST /proxy/set/{name}` body `{enabled,host,port:字符串,protocol,username,password}`，且要**先 create 后 setProxy 再 connect** | 新 `EvoClient.SetProxy`；`evoInstance.Connect` 改 create→setProxy→connect，**fail-closed**（proxy 设失败绝不 connect 直连） |

**仍需真机确认（文档不足以定，代码保持容错/保守默认，不再是"必错"阻塞项）**：
- **V3 状态码**：`IsThrottle=429/503`、`IsPermanent=400/401/403/404/422`。403 当 permanent 仍需真机验登出/未连接的真实码（3.5）。
- **QR 字段**：`ConnectInstance` 读 `base64`（3.1 C4），需真机确认字段名。
- **presence 端点** P1/P2（3.4）。
- **`Authorization` 头大小写/前缀**：Evolution 回发 `webhook.headers.authorization` 时的确切头名（我们读 `Authorization`，直接比对 token 无 `Bearer ` 前缀）。

下面原始 runbook 保留，真机跑时重点验剩余项即可；已修的 4 处对真实 payload 起沙盒对拍一遍确认无回归。**真机沙盒见 `docs/four-phase-demo/evo-verify/`**。

---

## ✅ 2026-07-15 沙盒对拍结果(P2-T8,部分完成)

起真沙盒(`docs/four-phase-demo/evo-verify` docker compose)对拍。**沙盒本身修了 2 处过期配置**:①镜像名 `atendai/evolution-api`→`evoapicloud/evolution-api`(项目迁移);②v2.1.1 用 Prisma **强制 PostgreSQL**(原 `DATABASE_ENABLED=false`+纯 Redis 会 "Database provider invalid" 退出,已加 postgres 服务 + `DATABASE_PROVIDER=postgresql`)。修后 Evolution v2.1.1 正常起。

**已真机坐实(不需 WhatsApp 登录即可验)**:

| 项 | 真机观测 | 代码假设 | 结论 |
|---|---|---|---|
| **V0 webhook 鉴权** | webhook-capture 实收头 `Authorization: wh-secret-abc`(首字母大写、**无 `Bearer ` 前缀**),= create 时配的 `webhook.headers.authorization` 原样回发 | `webhook_evolution.go:verify` 常量时间比对 `Authorization` 头无前缀 | ✅ **完全一致**(最高危项坐实,生产设 secret 不会误拒回执) |
| **webhook 事件形状** | 事件名 `connection.update`(点号)、`data.state`(close/connecting)、`data.statusReason`、`apikey` 回显、`server_url`/`date_time` | `normalizeEvent` 处理点号事件、读 `data.state` | ✅ 一致 |
| **create 形状** | 接受并回显 `webhook.webhookHeaders.authorization`;`instance.status:"close"` 初始 | `CreateInstance` 发 `webhook.headers.authorization` | ✅ 一致 |
| **V3 错误码(部分)** | 不存在实例发消息→**404**;错误 apikey→**401**;logout 未连接实例→**200 幂等** | `IsPermanent` 含 401/404;`LogoutInstance` 期望幂等 | ✅ 401/404 坐实 |
| **setProxy 坏代理** | Evolution 真去测代理活性,坏代理请求**超时挂起**(非快速 400) | `evoInstance.Connect` fail-closed(setProxy 失败不 connect) | ⚠️ 超时也须当失败处理(fail-closed 依据成立,但超时非明确码) |

**新发现(需成功握手才能最终定,但形状提示重要)**:`GET /instance/connect/{name}` 在**未握手时返回 `{"count":N}` 而非 `{base64:...}`**。强烈提示 **QR 可能不在 connect 同步响应里,而经 `QRCODE_UPDATED` webhook 事件异步推送**——这与代码 `ConnectInstance` 读同步 `base64` 的假设(C4)可能冲突,登录成功后必须确认 QR 到底在哪。

**⛔ 被环境挡住(数据中心 IP 被 WhatsApp 拒)**:沙盒出口是云数据中心 IP,Baileys 握手被 WhatsApp 反复 **`connection.update state=close statusReason=405`** 拒绝(HTTP 到 web.whatsapp.com 通,但 Baileys WebSocket 握手被拒),实例死循环 `connecting↔close`,QR 永远 `{count:0}`,从无 `QRCODE_UPDATED`。**这正是生产必须住宅/4G 代理的铁证(直连数据中心 IP 不可用)**。因此以下项在此环境**无法完成,需一个 WhatsApp 不拒的出口(住宅/4G/socks5 代理配到实例 `POST /proxy/set`)+ 手机备用号扫码**:QR base64 最终形状(C4/上述新发现)、V1 wuid、V2 回执扁平形状、V3 剩余码(400/403/422/429/503)、presence 端点、代理键迁移的 worker 时序确认。

### 🔴 2026-07-15 续:代理 + v2.3.7 后 QR 成功产出,再验一批(重大生产发现)

配了住宅/移动 http 代理(`POST /proxy/set` protocol=http)后,**v2.1.1 仍 405 握手失败**(Evolution 日志确认 `Proxy enabled` 代理生效了,但 WhatsApp 拒绝其 Baileys 客户端版本)。**换 `evoapicloud/evolution-api:v2.3.7` 立刻握手成功、产出真 QR。**

| 命门 | 真机观测 | 代码影响 | 处置 |
|---|---|---|---|
| **🔴 Evolution 版本** | **v2.1.1 的 Baileys 被 WhatsApp 淘汰(握手 405,即使走代理)**;v2.3.7 正常 | 生产/p0/沙盒全部标注 v2.1.1 | **已升级** `docker-compose.evolution.yml`/`p0-sending-unit`/`evo-verify` → `evoapicloud/evolution-api:v2.3.7`;生产 overlay 补 evolution-postgres(v2 强制 DB) |
| **🔴 QR 传递机制** | QR **不在** `/instance/connect` 同步响应(返回 `{count:N}`),而在 **`qrcode.updated` webhook 事件 `data.qrcode.base64`**,base64 **带 `data:image/png;base64,` 前缀**;另有 `data.qrcode.code`(WA 配对串) | `EvoClient.ConnectInstance` 读同步 `base64` **是错的**——上线前必须改为从 webhook `qrcode.updated` 取 QR | **待改代码**(P2 上线 gate);前端 `toDataUri` 前缀兼容已被坐实为对 |
| **V0 secret 可启用** | Evolution 回发 `Authorization: <配的值>`(无 Bearer),verify() 比对正确 | 原 `.env` 注释"先留空否则全 401"已过时 | **已更新** `.env.example`:secret 可安全启用 |

**QR 传递机制修正细节(给改代码的人)**:`GET /instance/connect/{name}` 返回 `{"pairingCode":null,"count":N}`(count=已推 QR 次数),QR 本体在 webhook。`qrcode.updated` 事件形状:
```json
{"event":"qrcode.updated","instance":"verify1","data":{"qrcode":{
  "instance":"verify1","pairingCode":null,
  "code":"2@Dpbb...",  "base64":"data:image/png;base64,iVBOR..."}}}
```
Evolution 每 ~40s 重推新 QR(count+1)直到扫码成功。

**仍待扫码后验(手机在环)**:V1 本号 jid 在 `data.wuid`?V2 回执扁平 `keyId/fromMe/status`?V3 登出/未连接真实码?presence 端点?代理键迁移 worker 时序?

**给后续执行者**:沙盒已可复现且升级到 v2.3.7。备好住宅/移动 http/socks5 代理 → `POST /proxy/set/verify1` → `GET /instance/connect/verify1` 触发 → 从 webhook `qrcode.updated` 取 `data.qrcode.base64` 渲染 → 手机扫 → 验剩余项。

---

**状态**: 待执行——**这是 E6 cutover 的硬前置**。E0–E5 已把整套 Evolution 客户端/webhook/发送/分片造好并合并 main（32 提交本地未推送），但**所有 REST 路径、JSON 字段名、webhook 事件形状、ack 数值、错误状态码至今 0 真机验证**（代码里 11 处 `TODO(evo-verify)`）。本 runbook 起一个真实 Evolution 实例，逐条对拍这些假设，把每个偏差就地修掉，然后才能上 E6。

**为什么必须先做**：E6 会一次性把这 11 个假设接到 live 生产 + 删 whatsmeow + 账号全量重扫码——**不可逆**。字段名错一个 = 回执全断 / 状态不更新 / 误判封号，且回滚成本灾难级。

---

## 0. 前置

- Docker + docker-compose。
- 一个可扫码的**备用** WhatsApp 号（会真登录，别用生产号）。
- 一个能收 HTTP POST 并打印原始 body 的东西（下面用一个 20 行 Go/netcat logger；也可用 webhook.site，但**别把真实 message 内容发到第三方**）。
- 可选：一个 4G/socks5 代理（验证代理绑定；无代理可跳过该项，但 E6 生产必须有）。

---

## 1. 起 Evolution + Redis（一次性沙盒）

`docs/four-phase-demo/` 或新建 `evo-verify/docker-compose.yml`：

```yaml
services:
  evolution:
    image: atendai/evolution-api:v2.1.1   # ← 确认你要对的版本；本仓库假设 v2
    ports: ["8080:8080"]
    environment:
      - AUTHENTICATION_API_KEY=verify-key-123    # = 我们 EvoClient 的 apikey
      - DATABASE_ENABLED=false
      - CACHE_REDIS_ENABLED=true
      - CACHE_REDIS_URI=redis://redis:6379/0
      - CACHE_REDIS_PREFIX_KEY=evo
    depends_on: [redis]
  redis:
    image: redis:7-alpine
```

```bash
docker compose -f evo-verify/docker-compose.yml up -d
export EVO=http://localhost:8080
export KEY=verify-key-123
curl -s $EVO/ -H "apikey: $KEY" | head   # 冒烟：能连上
```

**验证点 A（apikey 头）**：我们所有请求用头 `apikey: <key>`（`evolution_client.go:doJSON`）。确认 Evolution 认这个头名。若它要 `Authorization: Bearer` 或别的 → 改 `doJSON` 的 `req.Header.Set("apikey", ...)`。

---

## 2. 起一个 webhook 捕获器（对拍 payload 的关键）

我们的 webhook 信封假设（`internal/api/webhook_evolution_types.go`）：
```json
{"event":"...","instance":"...","data":{"key":{"id":"","fromMe":true,"remoteJid":""},"status":"","ack":0,"state":"","remoteJid":""}}
```
**真机大概率和这个不一样**——必须抓真实 payload 对照。起一个原始 body logger：

```bash
# 简易：nc 循环打印（够看结构）
while true; do printf 'HTTP/1.1 200 OK\r\n\r\n' | nc -l -p 9999 -q1; echo "----"; done
# 让 Evolution 回调到 http://<你的主机>:9999/  （容器内访问宿主用 host.docker.internal）
```
或写个 20 行 Go 打印 `X-*` 头 + pretty JSON body（推荐，能看 header 名）。

---

## 3. 验证矩阵（逐条打勾；❌ 就地改对应 file:line）

> 每行：**我们发/收什么** → **怎么验** → **错了改哪**。全绿才进 E6。

### 3.1 实例生命周期

| # | 我们的假设 | 验证 curl | 改哪（不符时） |
|---|---|---|---|
| C1 | `POST /instance/create`，body `{"instanceName","integration":"WHATSAPP-BAILEYS","webhook":{"url","events":[CONNECTION_UPDATE,MESSAGES_UPDATE,QRCODE_UPDATED]}}` | 见下 create 命令 | `evolution_client.go:127-150` `CreateInstance` |
| C2 | 代理字段 `proxyHost/proxyPort/proxyProtocol/proxyUsername/proxyPassword`（平铺在 create body） | create 时带代理，看是否生效（下面查 IP） | `evolution_client.go:137-147` + `evolution_proxy.go` |
| C3 | webhook 配置随 create 传（url+events 数组），Evolution 据此回调 | 看第 4 步是否收到回调 | 若 Evolution 要单独 `POST /webhook/set/{inst}` → E6 CreateInstance 后补一次设置调用 |
| C4 | `GET /instance/connect/{name}`，响应含 `base64`（QR data URI） | 见下 connect 命令 | `evolution_client.go:154-163` `ConnectInstance`（字段可能是 `code`/`qrcode.base64`） |
| C5 | `GET /instance/connectionState/{name}`，响应 `instance.state` | `curl $EVO/instance/connectionState/verify1 -H "apikey:$KEY"` | `evolution_client.go:108-118` `FetchState` |
| C6 | `DELETE /instance/logout/{name}`、`DELETE /instance/delete/{name}` | 流程末尾调 | `evolution_client.go:166-176` |

**create（对拍 C1/C2/C3）**：
```bash
curl -s -X POST $EVO/instance/create -H "apikey: $KEY" -H "Content-Type: application/json" -d '{
  "instanceName":"verify1",
  "integration":"WHATSAPP-BAILEYS",
  "webhook":{"url":"http://host.docker.internal:9999/","events":["CONNECTION_UPDATE","MESSAGES_UPDATE","QRCODE_UPDATED"]}
}' | tee /tmp/create.json
```
> ❌ 常见偏差：v2 的 webhook 可能要嵌套成 `{"webhook":{"enabled":true,"url":...,"events":[...]}}` 或独立端点；`integration` 大小写；代理可能要嵌套 `{"proxy":{"host",...}}` 而非平铺。**记下真实接受的形状**。

**connect（对拍 C4，拿 QR）**：
```bash
curl -s $EVO/instance/connect/verify1 -H "apikey: $KEY" | tee /tmp/connect.json
# 把响应里的 base64 QR 存成 png 扫码，或直接看 Evolution 日志/manager UI 扫码
```

### 3.2 扫码登录 → 抓 connection.update（**信封对拍，最高价值**）

扫码后，捕获器（第 2 步）应收到 `connection.update` 回调。**把真实 body 和我们的假设逐字段对**：

| 我们读的字段（`webhook_evolution.go` / `_types.go`） | 真机实际字段？ | 改哪 |
|---|---|---|
| 顶层 `event`（我们 `normalizeEvent` 兼容 `CONNECTION_UPDATE`≡`connection.update`） | ? | `webhook_evolution_types.go:normalizeEvent` |
| 顶层 `instance`（= 我们的 instanceName，用来反查 jid） | ? | `webhook_evolution.go` 分派 |
| `data.state`（我们期望 `"open"`/`"close"`/`"connecting"`） | ? | `webhook_evolution.go:117-128`（close→conn_churn 健康信号 + SetInstanceState） |
| `data.remoteJid`（我们当成本账号 jid 回填 `account_instances.jid`） | ？**高危** | `webhook_evolution.go:resolveJID`/`BindInstanceJID` |

> ❌ **高危点 V1**：登录后本账号自己的 JID，Evolution 可能不放在 `data.remoteJid`，而在 `data.wuid` / `instance.owner` / 顶层别处。我们靠它回填 `account_instances.jid`，而 jid 又是回执匹配的 `assigned_jid`——**错了则回执全断**。务必找到真实字段并改 `BindInstanceJID` 的取值来源。

### 3.3 发一条 → 抓 sendText 响应 + messages.update（回执对拍）

```bash
curl -s -X POST $EVO/message/sendText/verify1 -H "apikey: $KEY" -H "Content-Type: application/json" -d '{
  "number":"<你的另一个测试号,含国码>",
  "text":"evo verify ping"
}' | tee /tmp/send.json
```

| # | 我们的假设 | 验证 | 改哪 |
|---|---|---|---|
| S1 | `POST /message/sendText/{name}`，body `{"number","text"}` | 上面命令能发出 | `evolution_client.go:213-220` `SendText` |
| S2 | 响应含 `key.id`（= 真实 WA 消息 id），我们落成 `campaign_recipients.message_id` | 看 `/tmp/send.json` 的 `key.id` | `evolution_client.go:207-222`（可能是 `key.id` 嵌在别处/叫 `messageId`） |
| S3 | webhook `messages.update` 的 `data.key.id` 与 S2 的 id 一致（回执对称锚点） | 对方已读后看回调 body | `webhook_evolution.go:129-143` |
| S4 | ack 语义：`data.ack` 数值 `2=已发跳过 / 3=delivered / 4=read`；或 `data.status` 字符串 `DELIVERY_ACK/READ` | 让对方收/读，看回调 `ack`/`status` 实际值 | `webhook_evolution_types.go:translateAck` |
| S5 | `data.key.fromMe==true` 才记回执 | 看出站回执 fromMe 字段名/值 | `webhook_evolution.go:131` |

> ❌ **高危点 V2**：`messages.update` 的 `data` 在很多 Evolution 版本是**数组**（批量），或结构是 `data:{keyId, status}`（无嵌套 `key`）。我们假设单对象 `data.key.id`。若是数组 → `translateAck`/handler 要改成遍历。**这是回执能不能对上的命门**。

### 3.4 presence（拟人化，非阻塞但要对）

| # | 假设 | 改哪 |
|---|---|---|
| P1 | `POST /instance/setPresence/{name}` body `{"presence":"available"/"unavailable"}` | `evolution_client.go:178-188` |
| P2 | `POST /chat/sendPresence/{name}` body `{"number","presence":"composing"/"paused"}` | `evolution_client.go:190-199` |

### 3.5 错误状态码分类（反封命门）

| # | 假设 | 怎么验 | 改哪 |
|---|---|---|---|
| E1 | 限流/过载 → HTTP `429`/`503`（`IsThrottle`）→ E6 触发 governor 降速 | 高频狂发触发限流，看真实状态码 | `evolution_client.go:IsThrottle` |
| E2 | 给**已登出/未连接**实例发消息 → HTTP `400/401/403/404/422`（`IsPermanent`）→ 不重试 | logout 后再 sendText，看返回码 | `evolution_client.go:IsPermanent` |

> ❌ **高危点 V3（E4 终审已预警）**：`403` 我们当 permanent（不重试）。若 Evolution 用 403 表示「临时鉴权抖动」，会把可恢复错误误判为永久 → 漏发。**务必确认登出/未连接的真实状态码**，据实调整 `IsPermanent`/`IsThrottle` 的集合。`409`(已存在)/`5xx` 我们落瞬态可重试是安全默认。

### 3.6 Webhook 鉴权（**全场最高危 V0，先验这个**）

我们的 webhook `verify()`（`webhook_evolution.go`）假设：**header `X-Evolution-Signature` = `hex(HMAC-SHA256(secret, rawBody))`**，非空 secret 时不匹配就 401。

> ❌ **高危点 V0**：**Evolution v2 原生并不用这种 HMAC 签名**——它通常只是裸 POST，或带一个静态 `apikey`/自定义 header，或啥都不带。**如果真机不发 `X-Evolution-Signature`，那么我们一旦设了 `WADIST_EVOLUTION_WEBHOOK_SECRET`（生产必设），就会把每一个真实回调都拒成 401——回执/状态全断。**

**验**：第 2 步捕获器打印所有请求头，看 Evolution 回调到底带什么鉴权头。
**改**（据实二选一，`webhook_evolution.go:verify`）：
- 若 Evolution 支持配置一个静态 token header → 改 `verify` 比对该 header（常量时间）。
- 若 Evolution 无鉴权 → 用**网络层**兜底（webhook 端点只在内网/防火墙对 Evolution 节点开放），`verify` 退化为可选，并在 spec/部署拓扑记档「webhook 信任边界 = 网络隔离」。

---

## 4. 端到端串一遍（全绿门槛）

```
create(带 webhook+代理) → connect 拿 QR → 扫码
  → 捕获 connection.update：state=open + 本账号 jid 字段【记下真实字段名】
sendText → 响应 key.id【记下】
  → 对方 delivered/read
  → 捕获 messages.update：data 形状 + ack/status 值 + fromMe【记下】
setPresence/sendTyping → 不报错
logout 后 sendText → 记下永久错误状态码
高频发 → 记下限流状态码
delete 清理
```

产出一张「假设 → 真实值」对照表，把每个 ❌ 就地改进对应 file:line，加/改单测断言（httptest mock 用**真实 payload**重写），跑 `make gate` 绿。

---

## 5. 验证完成后

- 把本 runbook 的「真实值」结论回填到 spec `docs/superpowers/specs/2026-07-10-evolution-api-migration-design.md` §9 未决点，删掉对应 `TODO(evo-verify)`。
- 更新记忆 [[klws-evolution-pivot]]：把「全局阻塞项：0 真机验证」改成「已验证，字段以 runbook 结论为准」。
- **然后**才写 E6 cutover 计划（live 组合 + 分片接线 + 删 whatsmeow + 全量重扫码），且 E6 让 `WADIST_SENDER`/`WADIST_CONN` **默认留 whatsmeow**——合并 E6 代码≠上线，给真机灰度留口子。

---

## 附：优先级（按危害排序，先验高危）

1. **V0 webhook 鉴权**（3.6）——错了整条回执/状态链 401 全断。
2. **V1 登录后本账号 jid 字段**（3.2）——错了回执 `assigned_jid` 对不上，全断。
3. **V2 messages.update data 形状**（3.3）——数组 vs 单对象，回执命门。
4. **V3 登出/限流状态码**（3.5）——错了误判永久/漏发或不降速。
5. C1–C6 / S1–S2 / P1–P2 路径与字段——错了对应调用直接 4xx。
