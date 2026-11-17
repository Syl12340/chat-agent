# AstrBot IM 网关与仿生消息纵切

日期：2026-11-17。状态：0.7.18 已加入真实接入前检；生产开关关闭，尚未连接真实 IM 账号。

## 结论

AstrBot 作为外部多平台收发网关，chat-agent 是唯一 Agent。被插件接管的消息会先停止 AstrBot 自带 LLM pipeline，再通过签名协议进入 CA 的持久 Inbox、turn queue、Harness、记忆与结算；回复由 AstrBot 插件从 CA Outbox 领取后按原 `unified_msg_origin` 投递。不会建立第二套模型、人格或记忆。

Aurabot 不嵌入运行时。CA 采用其 MIT `src/chat/hub.js` 的短 debounce + 最大等待思想，以及 `src/chat/reply.js` 的“短闲聊可分气泡、结构内容保持整体”规则，用 Go/原生前端实现同一机制。已有 OneBot 入口继续兼容 Aurabot/SnowLuma 使用的 QQ 传输边界。

## 当前能力

- AstrBot 文本私聊入站；默认只允许 `aiocqhttp`，其他平台必须逐项加入插件 allowlist。
- 私聊首次使用 CA 生成的十分钟单次绑定码，绑定后无感认证并共享 owner 已审核记忆。
- HMAC-SHA256 入站与回执验签；稳定事件 ID、持久 Inbox 去重、来源/发送者/受众随 turn 保存。
- CA 生成完成后原子建立 Outbox；插件领取、发送并回执 `sent / failed / unknown`。
- 领取后一分钟无回执自动转 `unknown`，不自动重发，避免平台已经发送但响应丢失时出现重复消息。
- 回复可投影为最多三个短气泡；Markdown 列表、代码、公式、引用、链接及长文保持一条，且没有伪打字延迟。
- 连发窗口采用 650ms debounce、1800ms 最大等待。新消息可延长短窗口，但不能让会话无限等待。
- 主动消息可进入同一个 AstrBot Outbox；群聊主动消息仍禁止。
- 渠道状态页会分别显示最近一次已验签入站、插件领取轮询、发送回执和平台名，并检查
  是否已有 `aiocqhttp` owner 身份绑定。缺少其中任一项都会明确列出，不能把“开关已开”
  当成渠道可用。

## 安全边界

- `ASTRBOT_BRIDGE_ALLOW_GROUPS=false` 为默认值。启用群聊后，每个群使用不可逆的 `group-isolated` 作用域，不读取 owner 私人记忆、活动、设备或服务器状态。
- 当前桥只接纯文本。图片、文件、语音、引用和 AstrBot 按钮尚未加入协议，不能通过文本路径伪装成已验证附件。
- 插件默认只允许回环 HTTP；跨主机必须使用私有 TLS 隧道。Bridge secret 只保存在两端配置，不进入消息、日志或网页。
- AstrBot 的 `qq_official` 当前不支持 `Context.send_message()` 主动按 session 发送，因此默认 allowlist 不包含它；CA 自有 QQ Official 适配器继续负责官方机器人入口。
- 插件导入 AstrBot API，以 AGPL-3.0-or-later 分发；CA Go runtime 未复制 AstrBot 源码，仍按自身边界运行。

## 配置

CA 服务端：

```text
ASTRBOT_BRIDGE_ENABLED=true
ASTRBOT_BRIDGE_SECRET=<32+ 字符随机密钥>
ASTRBOT_BRIDGE_ROLE_ID=companion
ASTRBOT_BRIDGE_ALLOW_GROUPS=false
ASTRBOT_BRIDGE_CLAIM_TIMEOUT=1m
```

把 `integrations/astrbot-chat-agent` 安装为 AstrBot 插件，在插件面板填写同一密钥；同机使用 `http://127.0.0.1:9991`。默认平台 allowlist 为 `aiocqhttp`。AstrBot 对已接管消息不会再运行自己的模型。

协议入口：

```text
POST /hooks/astrbot/v1/events
GET  /hooks/astrbot/v1/deliveries/next
POST /hooks/astrbot/v1/deliveries/{id}/ack
```

## 已完成的本地协议验证

- 假 AstrBot 私聊未绑定时不调用模型；单次绑定码成功后才能进入 CA。
- 同一事件重复提交只结算一次模型调用和一次 delivery。
- 两段短闲聊回复被投影为两个消息；结构化列表不拆。
- 群聊默认返回空处理且不调用模型。
- delivery 领取与签名回执闭环通过。
- 一次完整的伪网关闭环会更新入站、轮询与回执诊断；没有 owner 绑定时状态仍不 Ready。

真实验收仍必须使用 AstrBot + `aiocqhttp` 登录账号完成 owner 私聊、连发、回复、重启、
unknown 和解绑流程。媒体、群聊与长期掉线恢复仍需单独实测；代码发布和诊断 Ready 都
不替代平台侧消息送达证据。
