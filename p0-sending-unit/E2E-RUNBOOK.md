# P0-1 真机端到端验证

前置：1 个真实协议号（已注册 WhatsApp）、1 个可用住宅代理、1 个已知在网的接收测试号。

1. 起依赖：`docker compose up -d`，`npx prisma migrate deploy`
2. 起控制服务：`npm run build && npm start`（监听 3000，Evolution 通过 host.docker.internal 回调）
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
3. 校准点：确认 Evolution v2 的 `/chat/whatsappNumbers/{instance}` 返回字段（number/exists/jid）与客户端映射一致；若字段名不同，改 `evolution-client.ts` 的 checkNumbers 映射即可。
4. 只把 ON_WHATSAPP 的号喂给发送队列（P0-4 批量投放接入点）——这一步把筛号的保号价值真正兑现。
