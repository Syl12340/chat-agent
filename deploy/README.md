# 单机部署（SSH 隧道）

当前发布（2026-11-05）：服务端 **0.19.0** 为 `20261105-chat-01`，Android **0.19.0-debug / versionCode 16** 为 `android-20261105-02`。一致性备份 `pre-chat-20261105-01.tar.gz` 和旧版本保留，线上只读检查及 APK 下载哈希通过。注意新 transcript/turns v2 不能直接切回 0.18，见[发布与回退边界](../docs/release-0.19.md)。以下为历史记录与通用部署操作。

当前发布（2026-11-05）：服务端 **0.18.0** 已切至 `20261105-companion-01`，Android 更新通道为 **0.18.0-debug / versionCode 15**（`android-20261105-01`）。一致性备份 `pre-companion-20261105-01.tar.gz` 已保留，线上只读检查及 APK 下载哈希通过。后台接收默认关闭，无配置/端口/角色文件变更，未安装手机。见[发布记录](../docs/release-0.18.md)。以下为历史发布及通用操作说明。

当前发布（2026-11-04）：服务端 **0.17.0** / `20261104-topics-02`；Android **versionCode 14** / `android-20261104-02`。备份 `/opt/pocket-agent/backups/pre-topics-20261104-02.tar.gz`。话题默认关闭，线上只读与 APK 下载哈希检查通过；未调用真实模型。详见[0.17 发布记录](../docs/release-0.17.md)。以下旧版本段落为历史记录。

当前发布（2026-11-04）：服务端 **0.16.0** / `20261104-companion-01`；Android **0.16.0-debug / versionCode 13** / `android-20261104-01`。一致性备份 `/opt/pocket-agent/backups/pre-companion-20261104-01.tar.gz`。已完成线上只读检查和 APK 下载哈希检查，未调用真实模型。关怀 state.json 兼容读取 v1、后续写 v2，回退不可仅换旧二进制。详见[本次发布](../docs/release-0.16.md)。下文旧版本检查与发布段落为历史记录。

2026-10-27 已部署至 `root@47.117.40.74`：Ubuntu 24.04 / Linux amd64。

当前为 **live 真实模型模式**，使用用户指定的 `ecnu-max`，[接通记录](../docs/model-connection.md)。已接入[本地搜索 JSON/MCP 服务](../docs/local-search.md)，仅监听 `127.0.0.1:9993`。保留 `127.0.0.1:9991` 的 SSH 隧道和 `9992` 公网加密 TCP；Android 使用 `pocket://47.117.40.74:9992`。[协议说明](../docs/encrypted-transport.md)。9990/frps、9999/Node 及已有 Apache 未修改，也未修改防火墙。

## 现在访问

这台电脑已启动后台隧道，打开 <http://127.0.0.1:19991>。访问口令保存在本地忽略文件 `.cache/pocket-agent-access.txt` 的 `ACCESS_TOKEN` 行。将其输入网页登录框；不需要输入 SSH 密钥或模型 API Key。

电脑重启或隧道断开后，在项目根目录执行并保持终端打开：

```powershell
.\deploy\Open-Tunnel.ps1
```

同一端口已有隧道时可直接使用。关闭隧道不停止服务器上的服务。当前后台隧道的 PID 记录在 `.cache/pocket-agent-tunnel.pid`，日志位于 `.cache/pocket-agent-tunnel.err`；这些文件不加入 Git。

这条隧道仅绑定电脑回环地址，手机不能直接填写电脑公网地址来访问。当前 Android 新版可直接使用上面的 pocket 地址，不依赖此隧道。USB 调试旧版本时，可针对明确的测试设备执行 `adb -s <设备ID> reverse tcp:19991 tcp:19991`。

## 服务布局

| 位置 | 用途 |
| --- | --- |
| `/opt/pocket-agent/releases/<版本>/` | 只读应用版本与启动脚本 |
| `/opt/pocket-agent/current` | 当前版本软链接 |
| `/etc/pocket-agent/pocket-agent.env` | root:0600，模式、访问口令和模型配置 |
| `/var/lib/pocket-agent/demo-sessions` | 演示模式持久化目录 |
| `/var/lib/pocket-agent/sessions` | 真实模型模式持久化目录，切换时创建 |
| `/etc/systemd/system/pocket-agent.service` | 开机自启、异常退出后重启 |
| systemd journal | 运行日志 |

服务使用 `pocket-agent` 专用无登录账户；数据目录权限 0700，配置不会给浏览器返回。systemd 限制写入范围、权限提升和进程资源，内存上限 256 MiB。没有在服务器安装 Go、npm 或构建依赖。

## 查看与维护

```sh
systemctl status pocket-agent --no-pager
journalctl -u pocket-agent -n 50 --no-pager
curl --fail http://127.0.0.1:9991/healthz
systemctl restart pocket-agent
```

## 接通真实模型

在服务器上用编辑器修改 `/etc/pocket-agent/pocket-agent.env`，保留现有访问口令并设置：

```text
POCKET_MODE=live
POCKET_BASE_URL=https://your-api.example/v1
POCKET_API_KEY=<在服务器私下填写真实密钥>
POCKET_MODEL=<真实模型 ID>
```

随后执行 `systemctl restart pocket-agent`。演示与真实会话目录分开，不自动迁移演示数据。恢复演示时改回 `POCKET_MODE=demo`。当前已开启本地 web_search；网页抓取仍需独立配置域名白名单，见[搜索配置](../docs/local-search.md)。

## 构建与安装

本机 PowerShell 构建静态 Linux 包：

```powershell
$env:GOOS='linux'; $env:GOARCH='amd64'; $env:CGO_ENABLED='0'
go build -trimpath -buildvcs=false -ldflags '-s -w' -o bin/chat-agent-linux-amd64 ./cmd/server
```

将二进制命名为 `pocket-agent`，与 `install.sh`、`start.sh`、`pocket-agent.service`、`pocket-agent.env.example` 一起上传到独立暂存目录。由 root 执行 `sh install.sh <新版本ID> <二进制SHA256>`。安装保留已有配置和数据，只更新应用版本及本项目的 unit；升级前应备份数据，并检查新版本格式兼容性。安装失败时需检查当前版本链接、服务状态和日志，不自动反复重试。

## 验证与剩余范围

本次只执行一条分两阶段的部署检查：健康检查、未认证 API 返回 401、Android 导出静态资源、真实 Linux 进程内的演示 SSE 时间工具，以及 systemd 重启后聊天历史恢复。另从本机隧道验证了鉴权后的状态接口和提醒调度状态。

当前 Agent 版本 `20261103-device-01`（0.15.0），Linux SHA-256：`c14c74d9b3233cc0460087c48d101fae5ee8eb3d103781150ddc1c75d2f7d2c0`；搜索服务仍为 `20261027-search-01`，程序 SHA-256：`08e3db205ab94cc828bd51da9d1f9b2fe5d2197bb2eea78c0fb76a071d2832f8`。旧版本保留以供回滚。升级前已停机备份生产会话和环境配置至 `/opt/pocket-agent/backups/pre-device-20261103-01.tar.gz`（root-only）。[本次变更说明](../docs/release-0.15.md)。

尚未配置公网浏览器 HTTPS、自动备份、日志保留策略和生产速率限制；真实模型已完成一次最小工具循环验收，长期运行尚未验收。文件型存储需要一致性备份：安排短暂维护，正常停止服务后备份 `/var/lib/pocket-agent`，再启动服务；配置文件中的密钥应独立加密备份。不要直接将正在写入的数据当作已验证的可恢复备份。

## Android 包发布

当前服务版本 `20261103-device-01`，Android 包 `0.15.0-debug`（versionCode 12，发布目录 `android-20261103-07`），支持自动登录与应用内更新。发布使用 `deploy/publish-android.ps1`，详情见[自动登录与更新发布说明](../docs/android-updates.md)。服务端 `POCKET_ANDROID_RELEASE_DIR=/opt/pocket-agent/android/current` 已配置；后续发布只切换安装包目录，无需重启服务。

语音朗读在同一服务环境文件中显式设置 `POCKET_TTS_MODEL=ecnu-tts` 和 `POCKET_TTS_VOICE=xiayu`，复用服务端网关密钥；聊天模型仍为 `ecnu-max`。取消配置并重启即可关闭，不增加端口、后台推送服务或持久化音频。

0.15 新增 `<data>/devices/state.json` 白名单，备份应包含该目录。首次设备注册等待管理员批准；维护口令保留用于首次授权和恢复，正常 Android 不自动读取旧维护口令。按本轮指示仅构建发布，未跑显式验收脚本。
