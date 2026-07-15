# 养号引擎(P5)灰度上线 Runbook

范围:`internal/warmup/`(引擎/调度/闸门)+ `internal/api/warmup_api.go` /
`internal/api/warmup_scripts_api.go`(后台养号中心 API)+
`frontend/components/admin-warmup*.tsx`(后台 UI)。目标:新号先经历一段
"养号"期(互发对话、积累回复/在线时长),达到毕业阈值(MATURE)后才可承接业务
发送,同时业务发送闸门 `WADIST_WARMUP_GATE` 全程可开可关,回退零风险。

## ① 部署

1. 确认 `migrations/0024_warmup.sql` 已随 `make deploy` 的 postgres 护栏跑过
   (该迁移建 `warmup_profiles`/`warmup_policies`/`warmup_scripts` 三表,并
   REVOKE `app_tenant`/`app_customer` 对它们的权限——worker/admin 走
   BYPASSRLS 的 SystemPool)。
2. 部署后确认新增/修改的两个 env:
   - `WADIST_WARMUP_INTERVAL_MS`(默认 `300000` = 5 分钟,养号 tick 周期)。
   - `WADIST_WARMUP_GATE`(默认 `off`,业务发送闸门)。
   参见 `.env.example` 中 "养号引擎(P5)" 段。
3. `go build ./... && go vet ./...` 全绿再上线;`internal/warmup/` 与
   `internal/api/` 的养号相关测试(`go test ./internal/warmup/ -v`、
   `go test ./internal/api/ -run Warmup -v`)必须先本地跑通。

## ② 扫码接入 ≥2 个同国号(账号池)

- 养号引擎需要至少两个"同语种/同国"账号互相发消息才能演出真实对话(脚本库
  按 `lang` 挑脚本,`internal/warmup/scripts.go` 的 `LoadScripts`/`PickScript`)。
- 通过现有扫码配对流程接入账号后,`internal/api` 的 webhook
  (`connection.update` → 自动 `EnrollIfAbsent`)会把新号自动收进
  `warmup_profiles`,初始 `stage=WARMING`。
- 建议每个语种(pt/en/zh,见 `migrations/0024_warmup.sql` 内置脚本)先备至
  少 2 个测试号,确认脚本库覆盖到位后再批量接入。

## ③ 观察 `/admin/warmup` 计数增长与自动毕业

- 打开后台"养号中心"(`/admin/warmup`):总览卡(新号/养号中/已成熟/已暂停)
  应随 tick 周期(默认 5 分钟)持续变化。
- 单号行的"养号进度"列(消息数/回复数/在线时长)应逐步增长;达到车道策略
  (`/admin/warmup/policies`,FAST/STANDARD 两条车道各自的毕业阈值)后,
  `stage` 应自动从 `WARMING` 变为 `MATURE`,且 `matured_at` 落库。
- 若某语种一直没有账号在动,检查该语种脚本库是否为空/全部禁用(后台
  "脚本库" 入口,`/admin/warmup/scripts`)——养号引擎选不到脚本时会跳过该号
  这一轮 tick,不会报错但也不会有进度。
- 封号自动降级:若账号在养号期或已毕业后被封(`account_devices.ban_status`
  变化),对应 `warmup_profiles` 行应自动降级(见 Task 17),不需要人工干预;
  可在养号中心筛选 `paused=true` 或降级历史核实。

## ④ 确认养号互发不计费

- 养号互发消息**不经过**计费链路(`internal/billing`)。上线前用 SQL 抽查:
  ```sql
  -- 应为空:养号期间账号互发的消息不应在 wallet_ledger 里留任何条目
  SELECT * FROM wallet_ledger
   WHERE account_jid IN (SELECT account_jid FROM warmup_profiles WHERE stage='WARMING')
     AND created_at > now() - interval '1 hour';
  ```
  (具体列名以当前 schema 为准;核心断言是"养号发的消息 = 0 条计费记录"。)
- 同时确认养号引擎写消息走的是独立的 warmup sender 路径,不复用业务发送
  队列/额度扣减逻辑(红线:五大引擎包业务逻辑不改,养号是旁路)。

## ⑤ `WADIST_WARMUP_GATE=on` 灰度开闸

- 确认②③④都通过、且已有一批账号自然毕业到 `MATURE` 后,再把
  `WADIST_WARMUP_GATE` 从 `off` 改为 `on` 并重启/热加载 backend。
- 建议先在单租户/小流量环境开闸观察,再全量铺开。

## ⑥ 验证业务发送只走 MATURE 号

- 开闸后跑一次小批量业务发送(campaign 或单发),核对
  `internal/dispatch/selectaccount.go` 的选号结果:所有实际发送账号的
  `warmup_profiles.stage` 都应为 `MATURE`。
- 后台"养号中心"里筛选 `stage=WARMING` 的账号,确认它们在这批发送里完全
  没有被选中(业务配额剩余列此时应保持不变)。
- 若发现 `WARMING`/`NEW` 号被选中发业务消息,立即执行 ⑦ 回退。

## ⑦ 回退

```
WADIST_WARMUP_GATE=off
```

- 改回 `off` 并重启/热加载 backend 即可让业务发送选号逻辑退回闸门开启前的
  行为(选号不再要求 `MATURE`)。这是唯一需要动的开关——养号引擎本身
  (`WADIST_WARMUP_INTERVAL_MS`)可以继续跑,不影响回退的即时生效,回退零
  风险、零数据迁移。
- 如需连养号引擎一起停(极端情况),把 `WADIST_WARMUP_INTERVAL_MS` 设为
  `<=0` 即可关闭养号 tick 循环。
