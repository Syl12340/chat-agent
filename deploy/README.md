# 单机部署（SSH 隧道）

当前发布（2026-11-15）为服务端 **0.7.0**（`20261115-im-unified-01`）与 Android **0.7.0-debug / versionCode 38**（`android-20261115-01`）。合批包含主动陪伴/双端 Agent/课件纵切和 QQ 官方/OneBot 的统一身份、会话联动与群聊隔离；两个 QQ 入口生产均保持关闭。两端 SHA-256、备份、零模型验收及回退命令见[0.7 发布记录](../docs/release-0.7.md)。

历史发布（2026-11-09，版本线重置后）：服务端 **0.4.0** 为 `20261109-unified-01`；Android 为 **0.4.0-debug / versionCode 7**（`android-20261109-19`）。设备 challenge 自动连接、统一助手、服务器模型配置、受限工作区 shell、文件回传和仿生分段已上线；一致性备份为 `pre-unified-20261109-01.tar.gz`。见[发布记录](../docs/release-0.4.md)。

历史发布（2026-11-08）：服务端 **0.25.0** 为 `20261108-channel-02`；Android 为 **0.21.2-debug / versionCode 20**（`android-20261106-02`）。OneBot v11 owner 私聊 P0–P2 已部署但默认关闭；一致性备份 `pre-channel-20261108-01.tar.gz`、版本/鉴权/历史/关闭态 webhook 和监听均通过零模型检查。见[发布记录](../docs/release-0.25.md)。

历史发布（2026-11-07）：服务端 **0.24.0** 为 `20261107-runtime-v2-01`；Android 仍为 **0.21.2-debug / versionCode 20**（`android-20261106-02`）。Harness 3B/3C 的运行结算、显式迁移、恢复与删除防复活已上线；停机备份 `pre-runtime-v2-20261107-01.tar.gz`、迁移后历史、鉴权、监听和 Android 更新清单均通过零模型检查。见[发布记录](../docs/release-0.24.md)。

历史发布（2026-11-06）：服务端 **0.22.0** 为 `20261106-continuity-ui-01`；Android 仍为 **0.21.2-debug / versionCode 20**（`android-20261106-02`）。陪伴接续、模型消息投影与网页 UI 已上线；一致性备份 `pre-continuity-ui-20261106-01.tar.gz`、线上版本/鉴权/旧会话/静态资源/监听和 Android 清单均通过零模型检查。见[发布记录](../docs/release-0.22.md)。

历史发布（2026-11-06）：服务端 **0.21.2** 为 `20261106-cot-01`，Android **0.21.2-debug / versionCode 20** 为 `android-20261106-02`。思考正文显示、默认省略输出 token 上限已上线；正常停机备份 `pre-cot-20261106-01.tar.gz`、健康/原会话可读、完整 APK 哈希通过，零模型检查调用。思考进度按需写 turns v3，出现新数据后不可直接切回旧二进制。见[发布与回退边界](../docs/release-0.21.2.md)。

历史发布（2026-11-06）：服务端 **0.21.1** 为 `20261106-reply-01`，Android **0.21.1-debug / versionCode 19** 为 `android-20261106-01`。备份 `pre-reply-20261106-01.tar.gz` 保留；新恢复入口、明确错误类别、线上健康/资源/旧会话及完整 APK 哈希通过。线上独立临时会话已完成超高思考回复并删除，原用户问题没有重发。见[排障记录](../docs/release-0.21.1.md)。

历史发布（2026-11-05）：服务端 **0.21.0** 为 `20261105-rich-01`，Android **0.21.0-debug / versionCode 18** 为 `android-20261105-04`。参数记忆、Markdown/MathML/JSON 树、七类结构和 UI 优化已上线；一致性备份 `pre-rich-20261105-01.tar.gz` 保留，线上只读与完整 APK 哈希通过。见[发布记录](../docs/release-0.21.md)。

历史发布（2026-11-05）：服务端 **0.20.0** 为 `20261105-ui-01`，Android **0.20.0-debug / versionCode 17** 为 `android-20261105-03`。四种互动组件与电脑网页工作台已上线；一致性备份 `pre-ui-20261105-01.tar.gz` 保留，线上只读与完整 APK 哈希通过。详见[发布记录](../docs/release-0.20.md)及[电脑端使用](../docs/desktop-web-plan.md)。下文旧版本与首次口令说明为历史记录；设备连接以当前白名单机制为准。

历史发布（2026-11-05）：服务端 **0.19.0** 为 `20261105-chat-01`，Android **0.19.0-debug / versionCode 16** 为 `android-20261105-02`。一致性备份 `pre-chat-20261105-01.tar.gz` 和旧版本保留，线上只读检查及 APK 下载哈希通过。注意新 transcript/turns v2 不能直接切回 0.18，见[发布与回退边界](../docs/release-0.19.md)。以下为历史记录与通用部署操作。

历史发布（2026-11-05）：服务端 **0.18.0** 已切至 `20261105-companion-01`，Android 更新通道为 **0.18.0-debug / versionCode 15**（`android-20261105-01`）。一致性备份 `pre-companion-20261105-01.tar.gz` 已保留，线上只读检查及 APK 下载哈希通过。后台接收默认关闭，无配置/端口/角色文件变更，未安装手机。见[发布记录](../docs/release-0.18.md)。以下为历史发布及通用操作说明。

历史发布（2026-11-04）：服务端 **0.17.0** / `20261104-topics-02`；Android **versionCode 14** / `android-20261104-02`。备份 `/opt/chat-agent/backups/pre-topics-20261104-02.tar.gz`。话题默认关闭，线上只读与 APK 下载哈希检查通过；未调用真实模型。详见[0.17 发布记录](../docs/release-0.17.md)。以下旧版本段落为历史记录。

历史发布（2026-11-04）：服务端 **0.16.0** / `20261104-companion-01`；Android **0.16.0-debug / versionCode 13** / `android-20261104-01`。一致性备份 `/opt/chat-agent/backups/pre-companion-20261104-01.tar.gz`。已完成线上只读检查和 APK 下载哈希检查，未调用真实模型。关怀 state.json 兼容读取 v1、后续写 v2，回退不可仅换旧二进制。详见[本次发布](../docs/release-0.16.md)。下文旧版本检查与发布段落为历史记录。

2026-10-27 已部署至 `root@47.117.40.74`：Ubuntu 24.04 / Linux amd64。

当前为 **live 真实模型模式**，使用用户指定的 `ecnu-max`，[接通记录](../docs/model-connection.md)。已接入[本地搜索 JSON/MCP 服务](../docs/local-search.md)，仅监听 `127.0.0.1:9993`。保留 `127.0.0.1:9991` 的 SSH 隧道和 `9992` 公网加密 TCP；Android 使用 `chat://47.117.40.74:9992`。[协议说明](../docs/encrypted-transport.md)。9990/frps、9999/Node 及已有 Apache 未修改，也未修改防火墙。

## 现在访问

这台电脑本轮已在后台启动隧道，打开 <http://127.0.0.1:60991>。访问口令保存在本地忽略文件 `.cache/chat-agent-access.txt` 的 `ACCESS_TOKEN` 行。将其输入网页登录框；不需要输入 SSH 密钥或模型 API Key。默认端口 19991 本轮被 Windows 拒绝绑定，因此只调整了本机转发端口，服务器仍监听 9991。

电脑重启或隧道断开后，在项目根目录执行并保持终端打开：

```powershell
.\deploy\Open-Tunnel.ps1 -LocalPort 60991
```

同一端口已有隧道时可直接使用。关闭隧道不停止服务器上的服务。当前后台隧道的 PID 记录在 `.cache/chat-agent-tunnel.pid`，日志位于 `.cache/chat-agent-tunnel.err`；这些文件不加入 Git。

这条隧道仅绑定电脑回环地址，手机不能直接填写电脑公网地址来访问。当前 Android 新版可直接使用上面的 chat 地址，不依赖此隧道。USB 调试旧版本时，可针对明确的测试设备执行 `adb -s <设备ID> reverse tcp:19991 tcp:60991`。

## 可选 OneBot / QQ 私聊通道

服务端包含默认关闭的 OneBot v11 owner 私聊通道。未配置 `ONEBOT_ENABLED=true` 时，不注册 webhook、不打开 Channel Store，也不连接 SnowLuma。协议、权限和防重复边界见[Channel 架构](../docs/channel-integration-architecture.md)。

SnowLuma 在本机、chat-agent 在服务器时，运行独立双向隧道并保持终端打开：

```powershell
.\deploy\Open-OneBot-Tunnel.ps1
```

它将本机 `127.0.0.1:18080` 转发到服务端 9991，同时把服务端 `127.0.0.1:13000` 反向转发到本机 SnowLuma 3000。SnowLuma HTTP Client URL 填写 `http://127.0.0.1:18080/hooks/onebot/v1/events`；HTTP Server 只监听本机回环。Ingress HMAC secret 与 Action Bearer token 必须分别生成并只写入服务端 root:0600 环境文件和 SnowLuma 对应配置，不提交 Git。

启用前在 `/etc/chat-agent/chat-agent.env` 配置：

```text
ONEBOT_ENABLED=true
ONEBOT_ACCOUNT_ID=<机器人 QQ 数字 ID>
ONEBOT_OWNER_QQ_ID=<主人 QQ 数字 ID>
ONEBOT_INGRESS_SECRET=<至少 32 字符的独立随机值>
ONEBOT_ACTION_BASE_URL=http://127.0.0.1:13000/
ONEBOT_ACTION_TOKEN=<至少 32 字符的另一独立随机值>
ONEBOT_ROLE_ID=companion
ONEBOT_TIMEOUT=5s
ONEBOT_MAX_BODY_BYTES=262144
```

第一版只处理 owner 好友私聊纯文本。guest、群聊、自身消息和非文本段不会进入模型；出站只调用 `send_private_msg`，目标来自已验证映射。超时或断线后的发送状态是 unknown，不自动重发，也不会重新调用模型。

## 服务布局

| 位置 | 用途 |
| --- | --- |
| `/opt/chat-agent/releases/<版本>/` | 只读应用版本与启动脚本 |
| `/opt/chat-agent/current` | 当前版本软链接 |
| `/etc/chat-agent/chat-agent.env` | root:0600，模式、访问口令和模型配置 |
| `/var/lib/chat-agent/demo-sessions` | 演示模式持久化目录 |
| `/var/lib/chat-agent/sessions` | 真实模型模式持久化目录，切换时创建 |
| `/etc/systemd/system/chat-agent.service` | 开机自启、异常退出后重启 |
| systemd journal | 运行日志 |

服务使用 `chat-agent` 专用无登录账户；数据目录权限 0700，配置不会给浏览器返回。systemd 限制写入范围、权限提升和进程资源，内存上限 256 MiB。没有在服务器安装 Go、npm 或构建依赖。

## 查看与维护

```sh
systemctl status chat-agent --no-pager
journalctl -u chat-agent -n 50 --no-pager
curl --fail http://127.0.0.1:9991/healthz
systemctl restart chat-agent
```

从 0.22 及更早格式升级到 0.24 runtime 时，必须先停止服务并确认无进程持锁，再在原数据目录运行一次显式迁移：

```sh
systemctl stop chat-agent
/opt/chat-agent/current/chat-agent -migrate-runtime -data /var/lib/chat-agent/sessions
systemctl start chat-agent
```

维护命令会把新 runtime 树的属主对齐到数据目录属主，即使用 root 执行也不会留下服务账户无法读取的文件。迁移发布后不能只切换旧二进制回退；应停机恢复迁移前的完整数据与配置备份。需要旧格式副本时使用 `-export-runtime <全新目录>`，不要覆盖生产目录。

## 接通真实模型

在服务器上用编辑器修改 `/etc/chat-agent/chat-agent.env`，保留现有访问口令并设置：

```text
CHAT_MODE=live
CHAT_BASE_URL=https://your-api.example/v1
CHAT_API_KEY=<在服务器私下填写真实密钥>
CHAT_MODEL=<真实模型 ID>
```

随后执行 `systemctl restart chat-agent`。演示与真实会话目录分开，不自动迁移演示数据。恢复演示时改回 `CHAT_MODE=demo`。当前已开启本地 web_search；网页抓取仍需独立配置域名白名单，见[搜索配置](../docs/local-search.md)。

## 构建与安装

本机 PowerShell 构建静态 Linux 包：

```powershell
$env:GOOS='linux'; $env:GOARCH='amd64'; $env:CGO_ENABLED='0'
go build -trimpath -buildvcs=false -ldflags '-s -w' -o bin/chat-agent-linux-amd64 ./cmd/server
```

将二进制命名为 `chat-agent`，与 `install.sh`、`start.sh`、`chat-agent.service`、`chat-agent.env.example` 一起上传到独立暂存目录。由 root 执行 `sh install.sh <新版本ID> <二进制SHA256>`。安装保留已有配置和数据，只更新应用版本及本项目的 unit；升级前应备份数据，并检查新版本格式兼容性。安装失败时需检查当前版本链接、服务状态和日志，不自动反复重试。

## 验证与剩余范围

当前 Agent 版本 `20261109-unified-01`（0.4.0），Linux SHA-256：`c0ab716b25651ac906582a41214eee9b7282ff7a62417d7c556922738c5e64ca`。本版不迁移已有 runtime/turns/life/topics 格式；新增模型配置写入独立的 `model-configs/state.json`。切换前备份为 `/opt/chat-agent/backups/pre-unified-20261109-01.tar.gz`（SHA-256 `887fff14613e6e10022d2e3f0c347c99af50eefa804c8646434577a99944c891`）。

本次零模型部署检查覆盖健康、鉴权、版本、调度与搜索开关、历史和 turns 投影、OneBot 关闭态、Android 清单/下载、9991/9992/9993 监听。最终有 2 个会话、38 条消息和 19 轮记录可读，active/running/queued/paused/unavailable 均为 0；`onebot_enabled=false`，未鉴权 API 为 401，关闭态 webhook 为 404。没有向真实模型或 QQ 发送请求。详见[0.26 发布记录](../docs/release-0.26.md)。

尚未配置公网浏览器 HTTPS、自动备份、日志保留策略和生产速率限制；真实模型已完成一次最小工具循环验收，长期运行尚未验收。文件型存储需要一致性备份：安排短暂维护，正常停止服务后备份 `/var/lib/chat-agent`，再启动服务；配置文件中的密钥应独立加密备份。不要直接将正在写入的数据当作已验证的可恢复备份。

## 可选的工作区与主机巡检

在主机本地配置 `CHAT_HOST_TOOL_SESSIONS` 为获准会话 ID，多个用逗号分隔。再按需要将 `builtin.workspace:filesystem:workspace://server` 或 `builtin.server:filesystem:diagnostics://host` 加入现有 `CHAT_PLUGIN_GRANTS`。停用任一插件可将对应 ID 加入 `CHAT_PLUGINS_DISABLED`。

工作区指向 `CHAT_WORKSPACE_ROOT` 的独立目录。执行动作可选，由 `CHAT_WORKSPACE_ACTIONS` 的 JSON 对象定义。键是模型可选的动作名，值是宿主配置的完整 argv 数组。只把受信动作放入白名单。执行器在 Linux 上以 CA 服务账号运行，使用进程组取消和固定超时。它继承服务单元的访问权限，适用于受信任务。

巡检读取负载、内存、磁盘、监听端口、固定服务状态、发布链接和备份文件名。日志工具只返回 CA 与搜索服务日志的时间、优先级与 unit。采集账号权限不足时会报告相应数据不可读。

新工具视图随 turns v4 保存。回退旧服务时按发布记录恢复升级前备份，或停机通过兼容导出取得不含新卡片元数据的 turns v3。

## Android 包发布

当前服务版本 `20261109-unified-01`，Android 包为 `0.4.0-debug`（versionCode 7，发布目录 `android-20261109-19`），包名为 `dev.chatagent.ai.debug`。APK SHA-256 为 `335498e562cb9b85fd0a4b446bce0e53433afa4234ca1abdd02510ec7c1e04e9`。发布使用 `deploy/publish-android.ps1`，详情见[自动登录与 Android 更新说明](../docs/android-updates.md)。服务端 `CHAT_ANDROID_RELEASE_DIR=/opt/chat-agent/android/current` 已配置；后续发布只切换安装包目录，无需重启服务。

语音朗读在同一服务环境文件中显式设置 `CHAT_TTS_MODEL=ecnu-tts` 和 `CHAT_TTS_VOICE=xiayu`，复用服务端网关密钥；聊天模型仍为 `ecnu-max`。取消配置并重启即可关闭，不增加端口、后台推送服务或持久化音频。

0.15 新增 `<data>/devices/state.json` 白名单，备份应包含该目录。首次设备注册等待管理员批准；维护口令保留用于首次授权和恢复，正常 Android 不自动读取旧维护口令。按本轮指示仅构建发布，未跑显式验收脚本。
