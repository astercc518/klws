# Evolution 整栈上线检查单（生产）

**前提**：`main` 已含 E6 cutover（whatsmeow 已删，Evolution 是唯一数据面）+ 模块二/九 + P1–P4 换皮 + **P2-T8 扫码修复**。本机即生产 docker-compose `klws` 宿主。**这是不可逆的 go-live，需操作者在场 + 手持所有账号的手机 + 可用代理。**

> 图例：🤖=我(自动)已备好 ｜ 👤=只能你做 ｜ ⛔=不可逆/高危

---

## ⭐ 2026-07-15 真机验证结果摘要（读这个先）

用备用号 + 移动代理 + Evolution 沙盒对拍,已坐实/修复:

| 项 | 结果 | 已落地 |
|---|---|---|
| **Evolution 版本** | 🔴 v2.1.1 的 Baileys 被 WhatsApp 淘汰(握手 405,即使走代理);**必须 v2.3.x** | ✅ 全部 compose 升级到 `evoapicloud/evolution-api:v2.3.7`(镜像名也迁移 atendai→evoapicloud) |
| **Evolution DB** | v2 用 Prisma 强制 PostgreSQL(v1 式 `DATABASE_ENABLED=false` 会崩) | ✅ `docker-compose.evolution.yml` 补 `evolution-postgres` 服务 |
| **代理** | 🔴 数据中心 IP 直连被 WhatsApp 405 拒;**每个实例必须配住宅/移动代理** | 代码 fail-closed(无代理不 connect);👤 上线必须备代理 |
| **QR 传递** | QR 在 `qrcode.updated` webhook 事件 `data.qrcode.base64`(带 data: 前缀),非 connect 同步响应 | ✅ FIX-1:webhook 缓存 QR,`/qr` 端点读缓存 |
| **V0 webhook 鉴权** | ✅ Evolution 回发 `Authorization: <配的值>`(无 Bearer 前缀),verify() 比对正确 | ✅ webhook secret **可安全启用**(不再"先留空") |
| **V3 错误码(部分)** | 不存在实例→404、错误 apikey→401、logout 幂等→200 | ✅ IsPermanent 含 401/404 坐实 |
| **前端占位** | "节点与发信设备"页 whatsmeow 占位误导 | ✅ FIX-2:改成跳「实例」页真向导 |

**仍需扫码登录后验(👤 上线灰度时补)**：V1 本号 jid 在 `data.wuid`?V2 回执扁平 `keyId/fromMe/status`?V3 登出/限流完整码?**代理键迁移**(扫码回填 jid 后 worker `StickyBindProxy` 时序——当前 fail-closed 可见非腐蚀,账号进发送轮转前需实现迁移,见 runbook)。

---

## 0. 前置（配置已备好）
- 🤖 `docker-compose.evolution.yml`（Evolution v2.3.7 + 独立 Redis + **独立 PostgreSQL** overlay，webhook 内网隔离）。
- 🤖 `.env.example` 含 Evolution 段（`EVO_PG_PASSWORD`、webhook secret 可启用）。
- 👤 在**线上 `.env`** 填：
  - `WADIST_EVOLUTION_APIKEY=<强随机>`（= evolution 服务 AUTHENTICATION_API_KEY）
  - `EVO_PG_PASSWORD=<强随机>`（evolution-postgres 密码）
  - `WADIST_EVOLUTION_WEBHOOK_SECRET=<强随机>`（V0 已确认可启用；worker 建实例配的 header = console 校验值 = 此值，三处一致）
  - `WADIST_SENDER=evolution`、`WADIST_CONN=evolution`

## 1. 真机验证（⛔ 硬前置——部分已做，剩余项灰度时补）
- ✅ V0/V3部分/QR机制/版本/代理 已对拍(见上摘要)。
- 👤 剩余 V1/V2/代理键迁移在第 5 步灰度扫码时补验(runbook 有步骤)。**别跳灰度**。

## 2. 起 Evolution 服务（可逆）
```bash
docker compose -f docker-compose.prod.yml -f docker-compose.adopt.yml \
  -f docker-compose.evolution.yml up -d evolution-postgres evolution-redis evolution
docker compose ... logs -f evolution        # 确认 "Welcome to the Evolution API ... v2.3.7"
curl -s http://<内网>:8080/ -H "apikey: $WADIST_EVOLUTION_APIKEY" | head   # 冒烟(容器内)
```

## 3. 部署 backend+frontend（⛔ 重建线上容器）
- 🤖/👤 `make deploy`（`scripts/deploy.sh`，带 postgres 行数护栏）。上线 P1–P4 换皮后台 + FIX-1(QR webhook 缓存)+ FIX-2(实例向导入口)+ webhook Authorization 鉴权。

## 4. 重建 wadist 为 Evolution 发送面（⛔ 真发消息+计费冻结）
```bash
docker compose -f docker-compose.prod.yml -f docker-compose.adopt.yml \
  -f docker-compose.evolution.yml -f docker-compose.worker.yml up -d --build wadist
```
- ⚠️ worker.yml 的 `wadist_badger` 卷是 whatsmeow 残留，Evolution 不用，可忽略。

## 5. 扫码接入账号（👤⛔ 只能你做，需手机 + 代理）
**用管理后台的扫码向导（FIX-1/FIX-2 修好的）**：
1. admin 后台 → 资源组 → **「实例」页 `/admin/instances`** → 点「接入新账号」
   - (「节点与发信设备」页的入口现在也跳到这里)
2. 向导选 **租户 + 国家码** → 创建（自动从代理池分配代理 + 分片；**无可用代理会拒绝创建**——先确保 proxy_pool 有该国存活代理）
3. 向导轮询 → **显示真 QR**（来自 Evolution `qrcode.updated` webhook，经后端缓存）
4. **用该账号手机扫码登录**（WhatsApp → 已关联的设备 → 关联设备）
5. 灰度 1–2 个号：确认 `account_instances.jid` 绑对（来自 `data.wuid`，验 V1）、发一条看回执落库（验 V2）
- ⚠️ **代理池必须先有存活代理**，否则创建实例 fail-closed 拒绝（数据中心直连会被 WhatsApp 405）。

## 6. 端到端验证
- 👤 灰度账号发送 → 送达/已读回执落库（`campaign_recipients.state`/`message_id`）→ 计费 Hold/Settle 正常 → 无异常封号信号。绿了再扩量。
- 👤 **代理键迁移**：确认灰度账号进发送轮转后代理粘性正常（若 worker `StickyBindProxy` 报 `ErrAccountMissing` 或代理漂移，按 runbook 实现 instance→jid 键迁移再扩量）。

## 7. Push 代码（已自动推送）
- 🤖 P1–P4 + Evolution 升级 + P2-T8 修复均已 push origin（用你提供的 token）。**建议撤销轮换该 token**（它在聊天里明文出现过）。

## 回滚现实
whatsmeow 已删，**没有 env 翻转回滚**。回滚 = 重建旧镜像 + 账号退回原会话，成本高。故第 1 步真机验证 + 第 5 步灰度是关键刹车。
