# 0.25.0 OneBot owner 私聊 Channel P0–P2

2026-11-08。状态：**服务端已部署，OneBot 默认关闭**。线上版本为 0.25.0 / `20261108-channel-02`；Android 没有原生改动，更新通道仍为 0.21.2-debug / versionCode 20。

## 交付

- 新增传输无关 `internal/channel`：Envelope、Principal、ConversationKey、稳定 request ID、Inbox/Outbox 状态和纯文本 Renderer。
- OneBot v11 入站对原始请求体执行 HMAC-SHA1 常量时间验签，并同时核对 `X-Self-ID`、body self ID、owner QQ ID、好友私聊与纯文本消息段。
- Web 与 Channel 共用现有 `SubmitTurn`。Channel 使用 DeferStart，先持久化 turn 与 Inbox queued，再启动原 worker；没有第二套 Agent Loop。
- Channel Store 使用独立锁、严格 JSON、原子替换和容量上限。accepted 重启转 needs_attention；sending 重启转 unknown，不自动重放。
- completion observer 只在 recorder、模型运行和 turns 结算全部成功后原子写入 Outbox；dispatcher 在独立 goroutine 中最多自动尝试一次。
- 出站只允许 loopback HTTP 的 `send_private_msg`，使用独立 Bearer Token，目标来自已验证 session mapping；拒绝重定向。
- guest、群聊、自身消息和非文本段不进入模型。reasoning、工具协议与 chat-ui JSON 不发送到 QQ。
- 新增可选双向 SSH 脚本 `deploy/Open-OneBot-Tunnel.ps1`。功能未配置时不打开 Channel Store、不注册精确 webhook、不连接外部服务。

详细协议、崩溃窗口和许可边界见 [Channel Integration Architecture](channel-integration-architecture.md)。

## 最小验证

- 精确请求体 HMAC、篡改拒绝、owner 私聊规范化和确定性 request ID。
- Store 冲突检测、accepted→needs_attention、sending→unknown 和 unknown 不重发。
- 共享 SubmitTurn 去重及 completed-only observer。
- 假模型 + 假 OneBot HTTP 闭环：owner 只调用一次模型和一次 Action；重复事件不重复调用；错误签名 401；guest 不进模型；sent 重启不重放。
- 关闭态 `/hooks/onebot/v1/events` 明确返回 404，不落到网页 fallback。
- `go test ./internal/channel/... ./internal/api ./internal/turns ./internal/runlog ./cmd/server -count=1`、`go vet ./...`、`go build ./...` 和 Linux amd64 静态构建通过。

测试没有调用真实模型、真实 QQ 或生产 Channel 数据。

## 生产发布

| 项目 | 值 |
| --- | --- |
| 服务目录 | `/opt/pocket-agent/releases/20261108-channel-02` |
| Linux SHA-256 | `0b2e18c025c50f6f4e10a34af3c7e31a2f7016281462da17358db34489f6d366` |
| 停机备份 | `/opt/pocket-agent/backups/pre-channel-20261108-01.tar.gz` |
| 备份 SHA-256 | `48ced08513e85a256b6a1b387acebef4185eaa3a9d3079cda57a57191b312986` |

最终零模型检查：服务 active，`/healthz` 正常，已认证 status 为 0.25.0 / ecnu-max，scheduler 与 web search/fetch 正常；未认证 API 为 401。当前 2 个会话、30 条消息、15 轮记录可读，active/queued/paused 均为 0。

生产环境没有 `ONEBOT_ENABLED=true`，status 显示 `onebot_enabled=false`，关闭态 webhook 为 404。因此本次发布没有连接 SnowLuma、调用 QQ Action 或新增 Channel 数据。

## 回退

0.25 没有修改 runtime v2、turns v3 或 Android 格式。OneBot 未启用时可直接切回 0.24 二进制；若以后启用并产生 `<data>/channels/onebot` 数据，旧二进制会忽略该独立目录，但回退前仍应停机保留完整数据与环境配置备份。
