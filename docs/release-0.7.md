# 0.7.0 合并发布

2026-11-15。状态：服务端与 Android 更新通道已发布。本次把原定两批合并为一次切换，包含主动陪伴质量、双端 Agent/课件纵切、网页 UI 收敛，以及 QQ 官方/OneBot 渠道统一身份与会话联动。

## 2026-11-15 源码复核修正

本节是对已发布 0.7.0 的复核：该版本 QQ `TurnCompleted` 没找到入站来源时仍可能创建投递；会话合并没有来源/受众字段；群判定依赖当前 mapping，重关联、历史及主动目标仍有缺口。后续工作区已修正前两项，并收紧群重关联与主动目标，尚未重新发布，见[实施记录](foundation-slice-2026-11-15.md)。

发布时 QQ/OneBot 均关闭，未连接真实账号；没有证据表明发生过外发事故。已有构建、测试和发布凭据保持有效，但不能据此开放真实渠道或认定群隐私已验收。

## 已有渠道实现与目标边界

- 新增传输无关 Adapter 契约和 QQ Open Platform Gateway 适配器；OneBot 继续作为兼容入口，两者复用 CA 的会话、turn queue、Harness 和结算。
- 私聊采用十分钟、一次性绑定码，把平台 OpenID 绑定到 owner Principal；绑定后应用内与私聊共享审核后的长期记忆。
- 目标是只有 IM 入站触发的轮次回原 IM；当前 QQ 来源判断尚未正确保证这一点。主动陪伴已有私聊 delivery，末端跳过群投递，但目标选择仍需群/私人范围约束。
- 群聊默认关闭且默认只处理 @；已有工具与部分上下文过滤，历史、重关联与主动触发尚未形成完整群隔离。
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

本次评估未重新验证恢复步骤。上述备份是已有归档证据，不自动代表跨文件一致性快照或已通过恢复演练。

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
