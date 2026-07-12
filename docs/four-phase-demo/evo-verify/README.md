# Evolution v2 真机验证沙盒 + 手机侧清单

配合 `docs/evolution-real-machine-verification-runbook.md`。文档/源码已把 4 处命门（V0 鉴权、V1 wuid、V2 回执扁平、C2 代理端点）钉死并改码；**这个沙盒是最后一步真机对拍**——只有你的手机能做。

## 一次起环境

```bash
cd docs/four-phase-demo/evo-verify
docker compose up -d --build
export EVO=http://localhost:8080 KEY=verify-key-123
curl -s $EVO/ -H "apikey: $KEY" | head          # 冒烟：Evolution 活着
docker compose logs -f webhook-capture &          # 盯回调（另开窗口也行）
```

> 容器里回调宿主用 `http://host.docker.internal:9999/`（Linux 若不解析，用宿主 IP 或给 evolution 服务加 `extra_hosts: ["host.docker.internal:host-gateway"]`）。

## 你（人）要做的最少动作

1. **建实例（带 webhook + 鉴权头）**
   ```bash
   curl -s -X POST $EVO/instance/create -H "apikey: $KEY" -H "Content-Type: application/json" -d '{
     "instanceName":"verify1",
     "integration":"WHATSAPP-BAILEYS",
     "qrcode":false,
     "webhook":{"url":"http://host.docker.internal:9999/",
                "events":["CONNECTION_UPDATE","MESSAGES_UPDATE","QRCODE_UPDATED"],
                "headers":{"authorization":"wh-secret-abc"}}
   }'
   ```
   ✅ 对拍点：这正是我们 `EvoClient.CreateInstance` 现在发的形状（含 `webhook.headers.authorization`）。

2. **（可选，但生产必做）绑代理**
   ```bash
   curl -s -X POST $EVO/proxy/set/verify1 -H "apikey: $KEY" -H "Content-Type: application/json" -d '{
     "enabled":true,"host":"1.2.3.4","port":"1080","protocol":"socks5","username":"u","password":"p"
   }'
   ```
   ✅ 对拍点：`EvoClient.SetProxy` 的形状。Evolution 会真连代理测活，坏代理回 400。

3. **拿 QR 扫码登录**（**用备用号，别用生产号**）
   ```bash
   curl -s $EVO/instance/connect/verify1 -H "apikey: $KEY"   # 取 base64 QR 存 png 扫，或看 manager UI
   ```
   扫完盯 `webhook-capture` 的 `connection.update`：
   - [ ] **V1**：本账号 jid 是否在 `data.wuid`？（我们现在读 wuid。若真机放别处→改 `ownJID()`）
   - [ ] `data.state` 是否 `open`？

4. **发一条给你的另一个测试号**
   ```bash
   curl -s -X POST $EVO/message/sendText/verify1 -H "apikey: $KEY" -H "Content-Type: application/json" -d '{
     "number":"<另一个测试号,含国码>","text":"evo verify ping"
   }'
   ```
   - [ ] **S2**：响应里 `key.id` 存在？（=我们落库的 message_id）
   让对方**已读**后盯 `messages.update`：
   - [ ] **V2**：`data` 是扁平 `keyId/fromMe/status` 吗？（我们现在双形状容错，扁平/嵌套都吃）
   - [ ] `data.status` 已读时是 `READ`、送达是 `DELIVERY_ACK`？

5. **验鉴权头（V0）**
   - [ ] `webhook-capture` 打印的请求头里，是否有 `Authorization: wh-secret-abc`？（我们的 `verify()` 就比对这个。若头名带 `Bearer ` 前缀或大小写不同→改 `webhook_evolution.go`）

6. **验错误码（V3）**
   ```bash
   curl -s -X DELETE $EVO/instance/logout/verify1 -H "apikey: $KEY"   # 登出
   curl -s -o /dev/null -w "%{http_code}\n" -X POST $EVO/message/sendText/verify1 \
     -H "apikey: $KEY" -H "Content-Type: application/json" -d '{"number":"1555","text":"x"}'
   ```
   - [ ] 给登出实例发 → 记下状态码。若 403 表示"临时"而非永久登出→调 `cluster.IsPermanent`。

7. **清理**
   ```bash
   curl -s -X DELETE $EVO/instance/delete/verify1 -H "apikey: $KEY"
   docker compose down -v
   ```

## 全绿之后

把上面每个 [ ] 的真实值填进 runbook 的结论表；若某项与代码不符，按 runbook「改哪」列就地改 + 补单测 + `make gate`。全绿后才推 origin、才 `make deploy`（合并≠上线）。生产首发让 `WADIST_EVOLUTION_WEBHOOK_SECRET` 两侧一致（worker 建实例配的 header = console 校验的 token）。
