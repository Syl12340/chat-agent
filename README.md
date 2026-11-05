# chat-agent

当前发布为 **0.18.0**；Android 更新包为 **0.18.0-debug（versionCode 15）**。2026-11-05 已部署到现有服务器及更新通道，保留原包名、签名和设备身份。[本次发布](docs/release-0.18.md)。源码仓库：[Syl12340/chat-agent](https://github.com/Syl12340/chat-agent)。

本批调整陪伴表达、交付 Harness 3A 存储契约，并增加默认关闭的 Android 加密后台接收。后台接收关怀、共同话题和到期提醒，须显式开启常驻通知；不保证系统强停或省电时实时送达。七项关键 Go 流程、构建及线上只读检查通过，未调用真实搜索/模型或安装真机。3B/3C 恢复迁移仍未实施。[推送渠道](docs/push-channel.md)、[Harness 规划](docs/dsh-harness-migration-plan.md)。

独立实现的轻量个人 AI 助手，当前具备 P0–P3 核心能力：运行时、PWA 聊天、角色/记忆、待办和提醒收件箱。Go 标准库 + 原生 HTML/CSS/JavaScript，**零第三方模块、无前端构建步骤**，网页资源嵌入单个可执行文件。

**0.7.0 已交付**：睡前闲聊、一天复盘、共同兴趣和小目标陪伴。共同活动支持手动保存进展、跨会话继续、暂停与结束，只影响明确关联的会话。[使用说明](docs/activities-0.7.md)。原有[连续聊天、事件与关怀](docs/companion-0.6.md)、[陪伴偏好](docs/companion.md)继续保留。

优先在现有 Android/网页改善连续消息、自然分段回复与话题接续；QQ、微信、B站等第三方平台接入保持低优先级。当前已上线能力、实际缺口和后续建议排期统一见[当前规划](docs/feature-plan.md)。

支持手机页面、单用户鉴权、会话列表、流式回答、停止生成、历史恢复、工具过程展示、基础 Markdown、角色切换、记忆审核与上下文压缩。为保持零依赖，记忆使用版本化 JSON 与基础关键词检索，未引入原规格中的 SQLite/FTS5。

## 快速体验

需要 Go 1.26。Windows / PowerShell 7：

```powershell
go build -buildvcs=false -o bin/chat-agent-0.18.0.exe ./cmd/server
$env:POCKET_ACCESS_TOKEN = [Convert]::ToHexString([Security.Cryptography.RandomNumberGenerator]::GetBytes(32))
$env:POCKET_ACCESS_TOKEN  # 在自己的终端查看，随后输入网页登录框
.\bin\chat-agent-0.18.0.exe -demo
```

在浏览器打开 [本机页面](http://127.0.0.1:8080)，首次通过管理员维护入口批准设备指纹，之后由设备签名自动连接。点击“看看此刻的时间”即可体验完整工具循环。

也可输入“记住：我偏好简洁、具体的回答”，打开侧栏“记忆管理”审核候选，再创建新会话询问记忆。演示只是确定性的界面验证，不代表真实模型的理解和摘要质量。

P3 演示命令：“待办：整理项目笔记”、“查看待办”、“完成第一条待办”、“一分钟后提醒我喝水”、“查看提醒”。在侧栏“待办提醒”可直接操作并查看到期收件箱。

`-demo` 明确使用本地固定回复，不调用真实模型；页面会显示“本地演示”。演示默认使用 `data/demo-sessions`，与真实模式的 `data/sessions` 分开。Ctrl+C 正常关闭会取消请求并释放会话锁。

Linux 构建：`go build -buildvcs=false -o bin/chat-agent ./cmd/server`。运行前设置相同环境变量。`-buildvcs=false` 用于避免当前环境父级 Git 状态异常影响构建。

## 连接真实模型

当前云端部署已接通 `https://chat.ecnu.edu.cn/open/api/v1` 的 `ecnu-max`，完成真实工具循环与流式回复验收，并已启用[服务器本地搜索](docs/local-search.md)。[模型验证记录](docs/model-connection.md)。模型密钥仅保留在服务器，Android 客户端无需重装。

保持访问令牌，配置服务端模型环境变量，去掉 `-demo`：

```powershell
$env:POCKET_BASE_URL='https://your-api.example/v1'
$env:POCKET_MODEL='your-model-id'
# 在自己的终端安全设置 POCKET_API_KEY
.\bin\chat-agent-0.18.0.exe
```

[.env.example](.env.example) 只是模板，程序不会自动加载 `.env`。

| 环境变量 | 用途 |
| --- | --- |
| `POCKET_ACCESS_TOKEN` | HTTP 服务访问口令，至少 32 字符；浏览器使用此口令 |
| `POCKET_BASE_URL` | 自定义 API 地址，包含需要的 `/v1`，不包含 `/chat/completions` |
| `POCKET_API_KEY` | 模型服务密钥，仅保留在服务器 |
| `POCKET_MODEL` | 原样发给模型服务的模型 ID |
| `POCKET_TTS_MODEL` / `POCKET_TTS_VOICE` | 可选点选朗读模型/音色；留空不开启，复用服务端网关和密钥 |
| `POCKET_WEB_HOSTS` | 可选抓取域名白名单，需显式 `-enable-web` |
| `POCKET_SEARCH_URL` / `POCKET_SEARCH_KEY` | 可选自定义搜索端点与 Bearer 密钥，仅留在服务端 |

默认输出字段为 `max_tokens`。需要其他字段时显式设置 `-token-parameter max_completion_tokens`。不发送 `stream_options`，不自动重试收费请求或切换协议。

可选参数：`-listen 127.0.0.1:8080`、`-data <目录>`、`-timeout 30s`、`-max-turns 4`、`-max-tools 4`、`-max-output 1024`。默认最多 4 个会话同时生成，每个会话同一时刻只有一个请求。

Android 当前可以填写 `pocket://47.117.40.74:9992`，通过固定服务器公钥的加密 TCP 直接连接云端；协议与密钥说明见 [加密传输](docs/encrypted-transport.md)。服务器同时保留回环 HTTP 与 SSH 隧道。普通浏览器若需公网 PWA，可另配 HTTPS 反向代理，关闭响应缓冲并保留 Host 和 Authorization 头；当前尚未提供公网浏览器 HTTPS 入口。[连接和维护步骤](deploy/README.md)。

## 页面与数据行为

- 桌面显示会话侧栏，手机通过左上角按钮打开会话抽屉。桌面 Enter 发送、Shift+Enter 换行；手机回车换行，通过发送按钮提交。
- 支持标题、列表、引用、代码块、行内代码、强调和 HTTP(S) 链接的基础 Markdown；不渲染模型提供的 HTML。
- 生成时可继续发送；短时间连发会聚合处理，普通短回复按语义分段展示。服务端接收后独立执行，刷新、断网或退出登录不会取消已接收消息；点击“停止”会取消生成并暂停后续队列，等待消息可撤回或手动继续。
- 未完成回复保存在独立队列记录中，刷新后仍明确标注中断，不作为完成回复送入后续模型上下文。重启后不自动重放不确定的模型调用。
- 刷新或重新打开旧会话会从服务器读取历史。重置通过追加版本化记录清空有效上下文，原始日志仍保留；它不是物理删除操作。
- 设备首次登记经管理员批准后，Android 使用 Keystore 私钥、浏览器使用 WebCrypto 私钥响应服务器 challenge；短期会话自动续期，退出后保留设备身份并暂停自动登录。模型 API Key 不发给网页。[自动登录与应用内更新](docs/android-updates.md)。
- PWA 只缓存应用页面与静态资源，不缓存带认证的请求、API 响应、设备认证或聊天历史。离线可打开界面，发送消息仍需连接服务器。
- `GET /healthz` 是不含配置的公开健康检查。所有 `/api/` 控制接口要求 Bearer 访问令牌；没有独立工具执行接口。

## 角色、Rules、Skills 与记忆

首次启动会在 `<data>/roles` 初始化“小伴”和“计划伙伴”两个角色，已有文档不会被覆盖。下拉选择角色会创建一个新会话；打开旧会话会恢复它原来绑定的角色，不改变角色归属或串用记忆。

“角色设置”可分别编辑人格 `IDENTITY.md`、规则 `RULES.md`、风格 `PROFILE.md`，也能查看当前角色的 Skills。文档更新用于之后的新请求。可用 `-roles <目录>` 指定工作区；添加 Skill 时在角色的 `skills/<名称>/SKILL.md` 放入 Markdown，之后按需读取，不执行代码或授予工具权限。

“记忆管理”提供候选审核、手动添加、编辑、删除和 Markdown 导出。助手的 `memory_write` 只能提出待审核候选；只有已审核事实能被新会话自动检索或由 `memory_search` 返回。修改和删除携带 revision，旧页面不会静默覆盖新纠正。初始记忆为空，不预写个人事实。

删除会从当前记忆文件移除条目；已有聊天、摘要、导出文件和备份不会一起删除。重置会话同样不会清除角色长期记忆。完整隐私清理需要同时处理这些数据副本。

```text
<data>/
├── pocket/<session>.jsonl    # 聊天日志、固定角色与摘要记录
├── memory/<role>.json        # 按角色隔离的候选和已审核事实
├── activities/state.json     # 共同活动、保存进展与会话绑定
├── life/state.json           # 事件、关怀设置与收件箱
├── turns/<session>.json      # 消息队列与中断状态
└── roles/<role>/
    ├── IDENTITY.md
    ├── RULES.md
    ├── PROFILE.md
    └── skills/<name>/SKILL.md
```

每个角色最多 500 条记忆、每条最多 4096 字节。检索使用中文双字片段和英文关键词，不是向量或语义检索。SQLite/FTS5 作为后续替换项；当前没有冒充已完成该数据库要求。

## 待办与一次性提醒

“待办提醒”按当前角色展示待办和提醒收件箱。可添加、完成、重新打开或删除待办；提醒可以取消、确认收到或删除。聊天工具同样绑定当前角色，不能指定其他角色 ID。

提醒默认按 `Asia/Shanghai` 解释聊天中的时间，可用 `-timezone <IANA 时区>` 调整；二进制嵌入时区数据。模型提交的时间必须带 RFC3339 时区。手动日期输入按设备时区转换为 UTC，列表按配置的用户时区显示，面板会标明二者。

服务端每秒检查一次，将到期提醒持久化为“已到期”。重启后补记停机期间过期的提醒，后续扫描不会反复触发。页面打开时每 15 秒刷新到期计数，也可手动刷新；切回页面会立即检查。

到期状态持久保留在收件箱，提醒本身不调用模型。Android 0.18 可在关怀面板单独开启[加密后台接收](docs/push-channel.md)，显示常驻通知，按角色安静/暂停设置通知。默认关闭，不等同于系统级可靠推送。关怀也需用户另行开启，接收在线时才补后台自动生成，继续遵守概率和额度。

创建请求使用持久化的 `idempotency_key`。相同角色内的同名有效待办、同标题且同时间的有效提醒也会合并；最多保留 16 个去重请求别名，已使用标识不能改绑其他任务。修改/删除校验 revision，旧版本不能重新打开或覆盖新状态。每个角色最多 500 条、总计最多 2000 条记录，标题最多 256 字节。

任务位于 `<data>/tasks/state.json`，使用同步后替换的版本化 JSON 和独占实例锁。写入失败时不发布到期状态，调度器稍后重试；API 状态和收件箱可查看调度是否暂时写入失败。已有角色 Markdown 不自动覆盖；若旧文档还写着“没有待办工具”，可在角色设置按当前工具更新。缺失的 task-management Skill 会被初始化。

## 可选网页工具

网页工具默认不注册。只有启动时显式开启并提供配置才可使用：

主服务在显式开启 `-enable-web` 后，直接请求搜索引擎公开 HTML 页面并解析候选，默认 `https://cn.bing.com/search`。需要正文时直接下载目标 HTTPS 网页并提取文本。旧 `cmd/searchd` JSON/MCP 包装器代码仍保留供历史独立部署使用，主服务不再使用其 JSON/RSS 返回值。

```powershell
# 可选指定公开 HTML 搜索引擎结果页；不能配置成 JSON 包装接口。
$env:POCKET_SEARCH_URL='https://cn.bing.com/search'
# 可选缩小网页抓取域名范围；留空允许经过校验的公开 HTTPS 站点。
$env:POCKET_WEB_HOSTS='example.org,docs.example.org'
.\bin\chat-agent-0.18.0.exe -enable-web
```

抓取仅接受 HTTPS/443 和有效 DNS 域名，拒绝私有/保留地址、账号、IP 字面量和内网跳转，连接固定到已检查 IP，保持 TLS 校验，不使用环境代理。不会执行网页脚本；读取最多 256 KiB、正文返回最多 12 KiB，有明确超时和解析错误。结果始终是不可信参考，不授予写入权限。

“共同话题”需要另行明确开启，并配置可以发送给搜索引擎的公开兴趣词。每次计划最多一次搜索、五条候选和两篇正文，模型可选择不分享；无后台手机推送承诺。[0.17 共同话题说明](docs/topics-0.17.md)。

## 上下文整理

在“连接信息”中可点击“整理较早的对话”并查看摘要。默认保留最近 4 个用户回合，较早部分用同一模型生成摘要；聊天页面和原始日志仍保留全部消息。

当有效聊天历史超过默认 64 KiB 时，会尝试自动整理。`-context-bytes 0` 关闭自动整理，`-context-bytes <字节数>` 调整触发阈值，`-keep-turns <回合数>` 调整保留范围。阈值是历史字节量估计，不是模型 token 上限；模型限制仍需按实际 API 配置。

摘要请求禁用工具，不自动重试，和当前运行共享超时；生成摘要会增加一次真实模型请求。摘要失败、被截断或取消时不会替换有效上下文。过短或无法安全切分的会话保持原样；输入超过 256 KiB 的摘要任务会拒绝执行并保留历史。演示模式只生成带明确标记的摘录。

## 会话存储

JSONL 保存于 `<data>/pocket/<id>.jsonl`。每批消息执行文件 Sync；工具调用与对应结果作为一批写入。Windows 的正常重启恢复已测试，不再依赖外部运行时的目录同步或平台锁。

每个打开的会话有独占 `.lock` 文件，记忆和任务目录各有 `.store.lock`，正常关闭后清理。异常退出可能残留锁，必须确认无写入进程后手动处理，程序不抢锁。旧 Harness 格式和截断记录会明确报错并保留原文件。P1 会话可读取并默认归属“小伴”；新增角色/摘要记录不保证可被旧二进制回滚读取。当前支持单实例，损坏文件没有自动修复，也未进行断电恢复验收。

## 最小验证

```powershell
go test ./internal/memory -run '^TestReferenceRetrievalLifecycle$' -count=1
go test ./internal/api -run '^(TestRecallContextLifecycle|TestKnowledgeManagementLifecycle)$' -count=1
go list -m all
```

仅保留 22 项 Go 核心测试；本批两条新增召回流程、五条直接相关回归和离线认证竞态检查通过，没有完整矩阵。服务器四轮 ecnu-max 对话使用独立虚构数据；模拟器验证识别退出、播放和断网恢复不重放消息。真机验收待完成。

可选浏览器检查使用已经安装的 Playwright，不作为应用依赖，也不会自动安装：

```powershell
# 如包不在默认路径，可设置 PLAYWRIGHT_MODULE 指向已安装的包
# 先启动本地 -demo 服务，并设置同一 POCKET_ACCESS_TOKEN
node scripts/browser-smoke.cjs
```

检查只允许连接显式演示模式。用同一条流程覆盖桌面、390px 手机视口、聊天、记忆审核、角色隔离、上下文恢复、待办工具与面板、到期提醒、确认/取消、离线页面和私有接口不进入缓存，并将截图保存到 `.cache/`。

## 代码结构与后续范围

Android Kotlin 包装已加入 [`android/`](android/README.md)：原生服务器设置、公钥配对的加密 TCP、本地 WebView 代理、返回键/键盘适配、连接错误页面和 Markdown 系统保存。继续使用现有 Go 服务；支持 pocket 加密连接或 HTTPS，调试版额外支持局域网 HTTP。构建和安装步骤见 Android 说明。

| 目录 | 职责 |
| --- | --- |
| `cmd/server` | HTTP 服务入口与正常关闭 |
| `cmd/searchd` | 回环监听的搜索 JSON/MCP 服务入口 |
| `cmd/probe` | P0 单轮真实 API 验证 CLI |
| `internal/api` | 鉴权、会话路由、SSE、取消和重置 |
| `internal/probe` | 独立 Agent loop、Provider、工具白名单和 JSONL |
| `internal/roles` | 角色工作区、默认模板与 Markdown Skills |
| `internal/companion` | 交流偏好、事件与关怀、修订校验与陪伴上下文策略 |
| `internal/turns` | 消息去重、持久队列、进度与中断恢复 |
| `internal/memory` | 按角色隔离的事实、审核状态和关键词检索 |
| `internal/storage` | 安全名称和同步后替换文件的通用辅助 |
| `internal/tasks` | 待办、一次性提醒、持久化状态与去重 |
| `internal/webtools` | 可选 HTTPS 抓取/搜索、DNS 地址与响应边界 |
| `internal/securewire` | 固定服务器身份、临时 ECDH 与有界 AES-GCM TCP 流 |
| `internal/localsearch` | 固定 RSS 上游、缓存、限流与本地 MCP 包装 |
| `web` | 嵌入式 PWA，无 CDN 和前端依赖 |
| `android` | Kotlin Android WebView 包装与 Gradle wrapper |
| `deploy` | Linux systemd 部署、演示/真实模式切换与 SSH 隧道 |
| `scripts` | 可选浏览器检查、可复现图标生成 |

[P3 验证报告](docs/p3.md) 记录功能交付；[服务器部署](deploy/README.md)、[真实模型验证](docs/model-connection.md) 和 [本地搜索](docs/local-search.md) 记录当前 ecnu-max、加密连接及搜索/MCP 验收。[P2](docs/p2.md)、[P1](docs/p1.md) 和 [P0](docs/compatibility.md) 保留历史记录。P4 仍需完善整体限流、公网浏览器 HTTPS 和备份。公网网页抓取、记忆/摘要质量和长期运行仍待验收。

子代理规则见 [AGENTS.md](AGENTS.md)：仅用户指定或高并行开发使用，不用于独立审查。本次三个实现方向并行开发，集成与部署由主代理完成。
