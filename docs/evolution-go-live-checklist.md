# Evolution 整栈上线检查单（生产）

**前提**：`main` 已含 E6 cutover（whatsmeow 已删，Evolution 是唯一数据面）+ 模块二/九。本机即生产 docker-compose `klws` 宿主。**这是不可逆的 go-live，需操作者在场 + 手持所有账号的手机。**

> 图例：🤖=我(自动)已备好 ｜ 👤=只能你做 ｜ ⛔=不可逆/高危

## 0. 前置（已备好）
- 🤖 `docker-compose.evolution.yml`（Evolution+独立 Redis overlay，webhook 内网隔离）。
- 🤖 `.env.example` 追加 Evolution 段（含"webhook secret 先留空"的安全默认）。
- 👤 在**线上 `.env`** 填：`WADIST_EVOLUTION_APIKEY=<强随机>`（与 evolution 服务一致）；确认 `WADIST_EVOLUTION_WEBHOOK_SECRET=` **留空**；`WADIST_SENDER/CONN=evolution`。

## 1. 真机验证（⛔ 上线硬前置，别跳）
- 👤 跑 `docs/evolution-real-machine-verification-runbook.md` + `docs/four-phase-demo/evo-verify/` 沙盒，用**备用号**对拍 V0–V3：webhook 是否发 `Authorization` 头、本账号 jid 是否在 `data.wuid`、`messages.update` 是否扁平 `keyId/status`、登出/限流真实状态码。
- 全绿后：若确认了 Authorization 头形状，再决定是否给 `WADIST_EVOLUTION_WEBHOOK_SECRET` 设值（否则**保持留空**）。

## 2. 起 Evolution 服务（可逆）
```bash
docker compose -f docker-compose.prod.yml -f docker-compose.adopt.yml \
  -f docker-compose.evolution.yml up -d evolution-redis evolution
docker compose ... logs -f evolution   # 确认起来
curl -s http://<内网>/ -H "apikey: $WADIST_EVOLUTION_APIKEY" | head   # 冒烟(容器内)
```

## 3. 部署 backend+frontend（⛔ 重建线上容器）
- 🤖/👤 `make deploy`（= `scripts/deploy.sh` backend+frontend，带 postgres 数据丢失护栏，会前后校验 pg 行数不变）。上线模块二/九的 API/后台/前端 + 新的 webhook 接收器（Authorization 鉴权）。

## 4. 重建 wadist 为 Evolution 发送面（⛔ 真发消息+计费冻结）
```bash
docker compose -f docker-compose.prod.yml -f docker-compose.adopt.yml \
  -f docker-compose.evolution.yml -f docker-compose.worker.yml up -d --build wadist
```
- ⚠️ worker.yml 的 `wadist_badger` 卷/注释是 whatsmeow 残留，Evolution 不用；可忽略（后续清理）。

## 5. 全量重扫码（👤⛔ 只能你做，需手机）
- 每个账号：wadist/orchestrator 建 Evolution 实例 → 取 QR → **用该账号的手机扫码登录**。账号不重扫，Evolution 侧无会话，发不出去。
- 建议先扫 1–2 个号灰度：发一条、看回执（messages.update 到 backend webhook）、确认 `account_instances.jid` 绑对（来自 `data.wuid`）。

## 6. 端到端验证
- 👤 灰度账号发送 → 送达/已读回执落库（`campaign_recipients.state`/`message_id`）→ 计费 Hold/Settle 正常 → 无异常封号信号。绿了再扩量重扫其余账号。

## 7. Push 代码（👤 需凭证）
- 👤 `git push origin main`（本机无 GitHub token）。给 PAT / `gh auth login` / 你本地推。CI `release-gate.yml` 会在 push 时跑门禁。

## 回滚现实
whatsmeow 已删，**没有 env 翻转回滚**。回滚 = 重建旧镜像（切换前的 wadist 构建）+ 账号退回原会话，成本高。故第 1 步真机验证 + 第 5 步灰度是关键刹车。
