# 0.7.16 仿生聊天与 AstrBot 网关发布

2026-11-16。状态：服务端与 Android 更新通道已发布。本次作为一个纵切完成规划更新、实现、最小验证、备份、服务器切换和 APK 发布，没有连接真实 QQ/AstrBot 账号，也没有调用真实模型。

## 交付内容

- 连发聚合从固定等待改为 **650 ms debounce + 1.8 s 最大等待**：新消息会延后起跑，但持续输入不会让一轮无限等待。
- 渠道回答采用 Aurabot 式语义气泡：普通短文本最多拆三段；代码块、表格、列表等结构内容保持原子，不为“像人”而破坏可读性。
- 增加 AstrBot transport-only 网关。插件先停止被接管事件的 AstrBot LLM pipeline，只把规范化消息送入 CA；CA 仍唯一管理模型、会话、审核制记忆、工具、Harness 与结算。
- AstrBot 入站采用 HMAC-SHA256、稳定事件 ID 和持久去重；私聊通过 CA 一次性绑定码归属 owner；群聊默认关闭并使用独立作用域。
- 出站使用持久 Outbox、租约领取和显式 ack。结果不确定时标记 unknown，不自动重发；ack 签名绑定 delivery ID。
- 首版适配器默认关闭且仅允许 `aiocqhttp` 文本私聊。真实账号凭据不进入仓库，媒体和群聊没有在本版宣称可用。
- 记录 Aurabot/AstrBot 上游 commit、源位置、采用方式与许可证；AstrBot 插件按 AGPL-3.0-or-later 分发。

## 规划更新

接下来仍按四条纵切推进：仿生聊天与主动陪伴、QQ 等 IM 联动、DSH Harness/双端 Agent、UI/GenUI 与 EduWork 学业能力。每个完成纵切默认同批更新规划、实现、最小测试、Git 推送、服务器部署和 APK 发布；一个纵切只打包一次。缺少外部凭据的 adapter 以默认关闭状态上线，不把部署拆成下一轮。

## 验证

- `go build -buildvcs=false ./...`、`go vet ./...` 通过。
- `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -buildvcs=false ./...` 与对应 `go vet ./...` 通过。
- 相关测试通过：AstrBot 私聊绑定/去重/多气泡/关闭群聊/签名 ack，连发 debounce 最大等待，以及 `internal/channel/...`。
- Windows 沙箱不允许测试服务器绑定回环端口；相关两个 API 测试在沙箱外仅监听 `127.0.0.1` 重跑通过。系统临时目录跨卷导致的原子 rename 假失败通过把 TEMP/TMP 固定到工作区同卷排除。
- AstrBot Python 插件可编译，配置 Schema 可解析。
- Android `assembleDebug` 通过；APK 元数据为 `0.7.16-debug` / versionCode `54`，v2 签名有效且签名证书延续。
- 线上零模型只读验收：服务 active，`NRestarts=0`；版本 0.7.16；PWA `chat-shell-v52`；连续 Harness 为 true；4 个会话、111 条消息、34 轮可读；active/running/queued/paused/unavailable 均为 0；scheduler、网页搜索和网页读取正常；AstrBot、OneBot、QQ Official 均关闭，禁用的 AstrBot/OneBot webhook 返回 404。

## 发布凭据

| 项目 | 值 |
|---|---|
| 服务 release | `/opt/chat-agent/releases/20261116-bionic-astrbot-01` |
| Linux SHA-256 | `44efbf6e3da04fca561ba16412f355d13500ff925391ac81a6215fec73e5c557` |
| Android release | `/opt/chat-agent/android/releases/android-20261116-15` |
| Android 版本 | `0.7.16-debug` / versionCode `54` |
| APK SHA-256 | `cf99a06b451b7664996efc5c6382f740b4b0a319d9007b87a37ec5410f683a2d` |
| 包名 | `dev.chatagent.ai.debug` |
| 签名证书 SHA-256 | `4905b966d06c53d22da462b9ef3e36ff27b54994829950a610f92a6bab68b24a` |
| 升级前备份 | `/opt/chat-agent/backups/pre-bionic-astrbot-20261116-01.tar.gz` |
| 备份 SHA-256 | `6d5bfb8f336abef43fedb6ac045b2f532cef6ace5b7ad8cdebf3fff1d9811739` |

## 回退

本次没有迁移既有数据格式；AstrBot 生产保持关闭，因此没有新增渠道运行数据。二进制可切回上一发布，Android 更新指针可切回上一目录。已安装 code 54 的手机不会自动降级；需要覆盖降级时仍受 Android 签名与 versionCode 规则约束。

```sh
systemctl stop chat-agent
ln -sfn /opt/chat-agent/releases/20261116-rail-collapse-02 /opt/chat-agent/current.next
mv -Tf /opt/chat-agent/current.next /opt/chat-agent/current
find /var/lib/chat-agent -name '*.lock' -delete
systemctl restart chat-agent

ln -sfn /opt/chat-agent/android/releases/android-20261116-14 /opt/chat-agent/android/current.next
mv -Tf /opt/chat-agent/android/current.next /opt/chat-agent/android/current
```

如需连数据与配置一起恢复，先停服并另存当前状态，再解包升级前备份；不要在服务运行时覆盖 `/var/lib/chat-agent`。恢复后清理 `*.lock` 再启动。
