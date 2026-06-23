# wadist — WhatsApp 多账号并发分发系统

多租户 SaaS,基于 whatsmeow,支撑 500+ 账号并发、动态 4G 代理隔离、事务级扣费与失败审核退款、故障自愈接管。

## 状态

按 `docs/superpowers/plans/2026-06-23-wa-distribution-roadmap.md` 的 11 个里程碑推进。

- **M0** 项目骨架 — go module、`internal/config`、`internal/log`(zap→waLog)、docker-compose、CI、Makefile、迁移/门禁脚本。
- **M1** 存储/会话持久化 — `internal/store`(Manager、DeviceStore、独立 advisory-lock 池)。✅
- M2–M11 — 待执行(代理、计费、对账、防封号、分发编排、可观测、停机、接管、容量、灰度、安全合规)。

## 本地开发

```bash
make up                 # 起 postgres:16 + redis:7
make test-race          # 全套测试(集成测试经 testcontainers 起临时 PG)
make gate               # 完整本地门禁:vet + staticcheck-less? -> tidy/vet/race/labels/vuln
make down
```

要求:Go 1.22+、Docker(集成测试用 testcontainers)。本地沙箱若 Ryuk reaper 起不来,测试命令已默认带 `TESTCONTAINERS_RYUK_DISABLED=true`。

## 配置(环境变量)

| 变量 | 必填 | 默认 |
|---|---|---|
| `WADIST_POSTGRES_DSN` | 是 | — |
| `WADIST_REDIS_ADDR` | 否 | `localhost:6379` |
| `WADIST_NODE_ID` | 否 | 主机名 |
| `WADIST_MAX_OPEN_CONNS` | 否 | `50` |
| `WADIST_MAX_LOCK_CONNS` | 否 | `300` |
