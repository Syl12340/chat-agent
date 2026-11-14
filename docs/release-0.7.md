# 0.7.0 合并发布

2026-11-15。状态：服务端与 Android 更新通道已发布。本次把原定两批合并为一次切换，包含主动陪伴质量、双端 Agent/课件纵切、网页 UI 收敛，以及 QQ 官方/OneBot 渠道统一身份与会话联动。

## 渠道边界

- 新增传输无关 Adapter 契约和 QQ Open Platform Gateway 适配器；OneBot 继续作为兼容入口，两者复用 CA 的会话、turn queue、Harness 和结算。
- 私聊采用十分钟、一次性绑定码，把平台 OpenID 绑定到 owner Principal；绑定后应用内与私聊共享审核后的长期记忆。
- 只有 IM 入站触发的轮次才回原 IM，应用内消息不会因为 session 关联而外发。主动陪伴对已绑定私聊建立单一持久 delivery，群聊不主动投递。
- 群聊默认关闭且默认只处理 @；启用后按群隔离，不加载私人记忆、活动、任务、设备/服务器状态和写工具。
- QQ 图片附件经公共 HTTPS 边界和媒体仓库校验后交给支持视觉的模型。Telegram、飞书、企微本批未实现。
- 设置页只增加折叠的渠道绑定/会话关联区，不增加侧栏入口。QQ 官方与 OneBot 生产开关均保持关闭，本次验收未连接真实 QQ、未调用模型。

## 验证

- `go build ./...`、`go vet ./...` 通过。
- `GOOS=linux GOARCH=amd64 go build ./...` 与同目标 `go vet ./...` 通过。
- `go test ./internal/channel/... ./internal/api ./cmd/server ./web -count=1` 通过。
- Android `assembleDebug` 通过；APK 签名验证通过。
- 线上零模型验收：服务 active，`NRestarts=0`；版本 0.7.0；PWA `chat-shell-v36`；4 个会话、169 条消息、36 轮可读；active/running/queued/paused/unavailable 均为 0；scheduler、网页搜索和网页读取正常；OneBot/QQ Official 均关闭。

## 发布凭据

| 项目 | 值 |
|---|---|
| 服务 release | `/opt/chat-agent/releases/20261115-im-unified-01` |
| Linux SHA-256 | `7dd9903400d89dea720272e9d756e2f6f60fa28f103e2aa3a86a4e8f466a4a37` |
| Android release | `/opt/chat-agent/android/releases/android-20261115-01` |
| Android 版本 | `0.7.0-debug` / versionCode `38` |
| APK SHA-256 | `056d25102e08080938e0511f5480c812e930c42708fc8be9881d18899fa131eb` |
| 包名 | `dev.chatagent.ai.debug` |
| 签名证书 SHA-256 | `4905b966d06c53d22da462b9ef3e36ff27b54994829950a610f92a6bab68b24a` |
| 升级前备份 | `/opt/chat-agent/backups/pre-im-unified-20261115-01.tar.gz` |
| 备份 SHA-256 | `12fe02e0e789b531927e41363b1189325d0b7e1e567271fd3a74922ba62bb5e2` |

## 回退

先停服，再把服务链接切回 `/opt/chat-agent/releases/20261114-bounded-action-01`，清理已退出进程遗留的 `*.lock` 后启动。Android 更新通道可切回 `/opt/chat-agent/android/releases/android-20261114-48`。回退不删除 0.7 数据；如需恢复升级前快照，必须先另存当前数据，再从上述备份中恢复 `/var/lib/chat-agent` 与 `/etc/chat-agent`。

```sh
systemctl stop chat-agent
ln -sfn /opt/chat-agent/releases/20261114-bounded-action-01 /opt/chat-agent/current.next
mv -Tf /opt/chat-agent/current.next /opt/chat-agent/current
find /var/lib/chat-agent -name '*.lock' -delete
systemctl restart chat-agent

ln -sfn /opt/chat-agent/android/releases/android-20261114-48 /opt/chat-agent/android/current.next
mv -Tf /opt/chat-agent/android/current.next /opt/chat-agent/android/current
```
