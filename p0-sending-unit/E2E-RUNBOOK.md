# P0-1 真机端到端验证

前置：1 个真实协议号（已注册 WhatsApp）、1 个可用住宅代理、1 个已知在网的接收测试号。

1. 起依赖：`docker compose up -d`，`npx prisma migrate deploy`
2. 起控制服务：`npm run build && npm start`（监听 3000，Evolution 通过 host.docker.internal 回调）
   - 注意：webhook 现在带密钥令牌。`.env` 里设 `WEBHOOK_SECRET`，Evolution 回调地址会自动带上 `/hook/<secret>`；直接 POST `/hook`（无令牌）会被 401 拒绝。
3. 导号：调 `service.importAccount('<协议号>')`，记录返回 id
4. 上线：`service.bringOnline(id, { host, port, protocol:'http', username, password })`
   - 拿到 `pairingCode`，在该协议号的 WhatsApp「已连接的设备 → 连接设备 → 用号码连接」输入配对码
   - 观察控制台 webhook：应收到 `connection.update state=open`，DB 中账号变 `ONLINE`
5. 发送：`service.sendMessage(id, '<接收测试号>', 'teste de conexão')`
   - 接收号应收到消息；DB 中 Message 由 `PENDING`→`SENT`→`DELIVERED`（→`READ` 若已读）
6. 封号验证（可选）：在手机端主动登出该设备，应收到 `connection.update state=close 401`，DB 账号变 `DEAD`、proxy 清空、deadReason=LOGGED_OUT
7. 记录：本轮该号从上线到被封共发出多少条 → 这就是真实 N 值的第一个数据点，回填给 P0-3 养号试验。

## 筛号（P0-2）真机验证

前置：一个已上线的"检测号"（专用于筛号，与主发送号隔离），其 Evolution 实例名记为 <checker>。

1. 导入原始号码（可来自号码库）：
   `node dist/index.js screen-import 1187654321 +55 11 91234-5678 5511222222222`
   - 输出 `{imported, invalid, duplicates}`；无效号入库标 INVALID，重复按 e164 去重。
2. 跑在网检测：
   `node dist/index.js screen-run <checker> 200`
   - 输出 `{checked, onWhatsApp, notOnWhatsApp}`；在网号状态变 ON_WHATSAPP 并记录 jid。
3. 校准点（字段名）：确认 Evolution v2 的 `/chat/whatsappNumbers/{instance}` 返回字段（number/exists/jid）与客户端映射一致；若字段名不同，改 `evolution-client.ts` 的 checkNumbers 映射即可。
4. **校准点（最关键 · 号码回显）**：`ScreeningService` 按 `result.number === target.e164` 匹配结果。务必确认响应里的 `number` **原样回显你发送的 e164**。两个静默失败要盯死：
   - 若响应缺 `number` 字段 → 客户端把它兜底成 `''`，则**所有号都匹配不上、全部滞留 PENDING、`checked:0` 却不报错**。
   - **巴西专属坑**：WhatsApp 常把 9 位手机 e164 解析成**不带第九位**的 jid（如 `551187654321@...`）。若 `number` 回显的是 jid 规范形而非你发的 `5511987654321`，匹配就会 miss → 真实在网号被误判、卡在 PENDING（丢客户，非号损）。
   - 处置：跑一小批先核对回显；若不一致，P0-4 里给匹配加"按 jid 数字前缀兜底"的 fallback。
5. **批量上限**：`screen-run` 一次性把整批发给检测端点。若 v2 对单请求号码数有上限，大批会失败/截断——真机先小批（如 50）验证端点容量，P0-4 再做分片。
6. 只把 ON_WHATSAPP 的号喂给发送队列（P0-4 批量投放接入点）——这一步把筛号的保号价值真正兑现。
