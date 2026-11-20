# 0.7.19 电脑本地 Agent 与工程方向更新

2026-11-21。状态：服务端与 Android 更新通道已发布；Windows 桌面程序已在本机工作区构建并完成回环烟测。本版把“双端 Agent”从“电脑绑定成云端执行器”校正为“电脑和云端各自运行完整 CA Core”，并基于当前源码重新制定工程方向。

## 交付内容

### 电脑本地运行时

- `chat-agent-desktop-0.7.19-windows-amd64.exe` 依据文件名自动进入桌面模式，也可显式使用 `-desktop`。
- 只允许 loopback HTTP，默认 `127.0.0.1:8765`；拒绝公网监听和 securewire 组合。
- 首次启动生成本机访问令牌并保存到用户配置目录，不打印令牌；浏览器通过 URL fragment 接收，页面取走后立即清除地址栏。
- 没有预设模型时界面仍可打开；在模型设置中添加 API 后，后续 turn 动态退出 demo，不要求重启 Core。模型凭据、会话和角色数据留在本机。
- 默认工作区是用户 `Documents`，不存在时使用用户目录。桌面会话无需设备绑定或会话授权即可使用本机工具。
- 原 `local-agent` 远程执行协议继续保留为云端额外调用电脑的可选能力，已从桌面主路径和本地模式入口中退出。

### 本机工作区写入

- 新增 `workspace_write`，限定 UTF-8 与 256 KiB。
- 修改已有文件必须提供刚读取内容的 SHA-256；新文件必须显式使用 `missing`，避免悄悄覆盖。
- 写入采用同目录临时文件、同步并原子替换；写入仍是高风险、逐次批准工具。
- 哈希校验前先拒绝目录和超过 256 KiB 的旧文件，避免写工具为了比对而把任意大文件读入内存。
- Windows 尚无已经验证的强制 shell 沙箱，因此本版不注册任意 shell。固定动作与路径 guard 不冒充 OS 隔离。

### 规划与工程方向

- 更新[当前规划](feature-plan.md)和[产品方向](product-direction.md)，明确电脑本地与云端是同一内核的两种完整运行配置。
- 新增[工程方向评估](engineering-direction-2026-11-20.md)：优先正式桌面更新、稳定核心契约、API 领域化、强制沙箱、显式跨端接续、学习闭环和真实 QQ 私聊；不再以插件、工具、设置项或侧栏入口数量衡量进展。
- 0.7.19 不暗中同步本地与云端历史、附件或凭据。显式跨端接续仍是后续能力。

## 最小验证

- `go test ./cmd/server ./internal/api ./plugins/builtin/workspace -count=1` 通过。
- Windows `go build -buildvcs=false ./...`、`go vet ./...` 通过。
- `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -buildvcs=false ./...` 与对应 `go vet ./...` 通过。
- `web/app.js`、`web/host-tools.js`、`web/local-agent-ui.js` 通过 `node --check`，`git diff --check` 通过。
- 桌面成品在同一 Windows 进程环境完成真实回环烟测：0.7.19、`desktop_local=true`、demo 可启动、持续 loop、工作区自动授权且包含 `workspace_write`。
- Android `assembleDebug` 通过；APK 为 `0.7.19-debug` / versionCode 57，包名与签名延续。
- 线上只读验收：服务 active、`NRestarts=0`、版本 0.7.19、PWA `chat-shell-v55`、持续 loop、调度正常、三个 IM 网关保持关闭，最近十分钟 warning 数为 0。线上更新接口实际下载 APK 的 SHA-256 与清单一致。

## 发布凭据

| 项目 | 值 |
| --- | --- |
| 服务 release | `/opt/chat-agent/releases/20261121-desktop-local-02` |
| Linux SHA-256 | `6cd86a2f6426501645a900cab20d0dee0f1c6e9d3ef8c532786efac3a383e67a` |
| Windows 桌面文件 | `bin/chat-agent-desktop-0.7.19-windows-amd64.exe` |
| Windows 桌面 SHA-256 | `788bea4a7d306fe97dbf9f73168df83e5312d56ff01b9bad046dcad95d5bd3e6` |
| Android release | `/opt/chat-agent/android/releases/android-20261120-01` |
| Android 版本 | `0.7.19-debug` / versionCode `57` |
| APK SHA-256 | `18b108b61157184516440a896732bfeda37e28146a6950a431473164b243ce69` |
| APK 大小 | `1,229,035` bytes |
| 包名 | `dev.chatagent.ai.debug` |
| 签名证书 SHA-256 | `4905b966d06c53d22da462b9ef3e36ff27b54994829950a610f92a6bab68b24a` |
| 最终切换前备份 | `/opt/chat-agent/backups/pre-desktop-local-20261121-02.tar.gz` |
| 备份 SHA-256 | `b94fb58a7c981a20c796f41c2f0f3ffa793378e4c37cac00512f7ecc20698a26` |

桌面成品当前位于共享工作区，没有伪装成已经存在的公网桌面更新渠道。正式桌面清单、下载、原子替换和失败回退已列为下一工程闭环。最终 Linux 边界收紧没有 Android 代码变化，因此 Android 沿用同批已发布的 0.7.19 / versionCode 57，没有制造第二个无意义 APK 批次。

## 回退

本版没有生产持久 schema 迁移；桌面数据使用独立本机目录，工作区写工具不会在部署时主动写文件。因此服务端可只切回 0.7.18：

```sh
systemctl stop chat-agent
ln -sfn /opt/chat-agent/releases/20261117-four-slices-02 /opt/chat-agent/current.next
mv -Tf /opt/chat-agent/current.next /opt/chat-agent/current
find /var/lib/chat-agent -name '*.lock' -delete
systemctl restart chat-agent

ln -sfn /opt/chat-agent/android/releases/android-20261117-01 /opt/chat-agent/android/current.next
mv -Tf /opt/chat-agent/android/current.next /opt/chat-agent/android/current
```

Android 已安装 versionCode 57 的设备不能通过更新通道自动降级。若必须恢复升级前全部服务数据，先停服并另存当前状态，再把备份解包到 `/`，清理 `*.lock` 后启动；这会丢失 0.7.19 上线后的新聊天和其他数据，不能作为普通二进制回退使用。
