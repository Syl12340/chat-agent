# dsh-Go、AstrBot 与 Aurabot 源码调研及 CA 改造路线

更新：2026-11-10。本轮固定上游源码并阅读关键实现，用于修正
[推进目标与架构路线](product-direction.md)。本文是架构与实施依据，不表示相关功能已经交付，
也不授予 Agent 新的主机权限。

## 1. 结论

CA 不迁移到 Flutter、Dart、Python 或 Node，也不嵌入 DSH/Cordis、AstrBot 或 Aurabot
运行时。继续使用 **Go Core + 原生 Web/PWA + Kotlin Android 壳**，但把现有零散能力收敛为
三条协议：

1. **Remote Protocol**：业务客户端位于统一 Carrier 之上，本地 HTTP、现有加密连接和未来本机
   执行端使用相同的会话/任务 API；断线恢复、能力协商和结果不确定性成为协议事实。
2. **Presentation Protocol**：工具卡片、声明式界面和后续持久 surface 共用版本化节点、预算、
   能力降级和受控 effect，不允许插件注入任意 HTML/JS。
3. **Conversation Runtime**：每轮运行由有身份的输入、可回放事件、串行执行、插话/排队和终态
   组成；聊天节奏和主动性建立在可观察状态机上，而不是固定延迟或额外分类模型。

三套上游分工不同：

| 上游 | 对 CA 最有价值的机制 | 本轮结论 |
| --- | --- | --- |
| dsh-Go | Carrier/Mux、远程信任与重连、插件声明式视图、移动端运行 UX | 主架构参考；`plugin-kit` 的 MIT 部分可作为后续直接复用候选 |
| AstrBot | 有类型运行事件、可移植输入输出、session/turn 串行与 steer、短时文件票据 | AGPL 架构参考，不复制源码；不把多 IM 接入提前为主线 |
| Aurabot | 连发合并、运行中插话、下一轮排队、气泡上限、主动搭话状态机、单次投递 | MIT；已有气泡约定可继续演进，其他机制按 CA 边界用 Go 改造 |

## 2. 固定来源与许可边界

源码检出位于被 Git 忽略的 `.cache/`，不进入发行包。

| 仓库 | 固定提交 | 许可 | 使用边界 |
| --- | --- | --- | --- |
| [ccch1mneyyy/dsh-Go](https://github.com/ccch1mneyyy/dsh-Go) | `a9877420ab9aee121134faf01b6a92a39ce0b05b`（v1.0.15） | 根仓库 AGPL-3.0；`sdk/plugin-kit` 独立 MIT | AGPL 文件只作架构研究；复制 `plugin-kit` 文件前逐文件登记并保留 MIT 声明 |
| [AstrBotDevs/AstrBot](https://github.com/AstrBotDevs/AstrBot) | `68acac91b1ab92c2ac4cfd811c0898913a1391c8` | AGPL-3.0 | 只借鉴行为和分层，不复制源码或素材 |
| [aureth-labs/dsh-aurabot-qq](https://github.com/aureth-labs/dsh-aurabot-qq) | `5eb5b9463f5f0ce14964a2c7d5dcba44ad67fabe` | MIT | 可复制、修改或移植；实际采用时记录源文件、目标文件与改动 |

不把星数等易变信息写入工程事实。dsh-Go 的仓库名也不表示它由 Go 实现：客户端主体是
Flutter/Dart，Android 系统接入含 Kotlin。CA 借鉴协议与交互，不迁移技术栈。

## 3. dsh-Go 源码结论

### 3.1 `packages/dsh_client`：先解耦业务 API 与物理连接

关键实现位于 `lib/src/carrier.dart`、`connection.dart`、`mux.dart`、`rpc.dart`：

- `DshCarrier` 只负责单次调用、打开链路、允许的 endpoint 和 peer capabilities；上层 API
  不关心它是本地 HTTP 还是远程加密隧道。
- `DshMuxLink` 在一条物理连接上承载多个逻辑流，区分 open/item/end/error/cancel。
- Mux 拥有连接 generation、ready 握手、重连退避、流重开和 resync，而不是让每个页面分别
  猜测 WebSocket 是否恢复。
- 主机业务错误与 carrier/transport 错误分开；传输中断意味着写操作结果可能 unknown，不能
  自动生成新请求重试。
- endpoint allowlist 和 peer capability 属于连接边界，不依赖界面隐藏按钮实现权限。

映射到 CA：现有 Android `ChatWire`/`WireGateway` 与 Web API 能工作，但尚未形成统一的
Carrier seam。第一步不更换现有 P-256 securewire，而是在其上定义统一请求、订阅、generation、
capability 和错误语义，再提供 HTTP 与 securewire 两个 adapter。

### 3.2 `packages/dsh_remote`：借鉴信任与恢复，不急着引入 WebRTC

关键实现位于 `account/`、`carrier/tunnel_carrier.dart`、`channel/`、`crypto/noise_ik.dart`、
`pairing/` 和 `transport/`：

- relay 或 LAN/P2P/TURN 最终都返回普通 `DshClient`，路径切换不污染业务 API。
- Noise IK 绑定双方设备身份；Host 有 pinned/unpinned/keyChanged/unknown 明确信任状态。
- access token 与旋转 refresh token 分开；刷新 single-flight，避免旧 refresh token 并发复用
  导致整个 token family 被撤销。
- 配对是有期限的状态机，并固定 server origin、Host 身份、手机身份和人工校验码。
- 每次重连产生新 generation，重新同步 watch 快照；能力不兼容时终止，不能把两种历史表示
  混进同一客户端缓存。
- 远程 endpoint 采用固定 allowlist；传输丢失与 Host 拒绝是两类错误。

CA 已有设备公钥 challenge、服务器白名单和加密连接，不从零重做鉴权。当前缺口是 Host key
变化的显式 UI、连接 generation/resync、endpoint/capability 协商、长期设备会话轮换和多执行端
抽象。Noise/WebRTC 只在以后出现真实直连需求时评估，不进入近期前置。

### 3.3 `sdk/plugin-kit`：Presentation v1 的直接候选

该目录是独立 MIT 许可，包含机器可读 schema、构造器、validator、capability helper、effect
检查器和 test host。最值得采用的不是 Node SDK 本身，而是以下稳定契约：

- 协议版本、view 版本和 `minApp` 分开；新增能力采用 additive compatibility。
- toolCard、panel、strip 分别限制字节、节点、深度和子节点，不能共用一个无限 JSON 入口。
- 未知组件由 capability 检查后降级，而不是交给客户端异常终止整条消息。
- 插件不能直接改宿主；用户点击 action 后，handler 只返回宿主 allowlist 内的 effects。
- deferred effect 有更窄的权限和并发/时限；延迟回调不能突然导航、运行命令或扩大授权。
- 审批、凭据、插件安装、Host 切换等安全面不开放给普通插件视图。

CA 首版只选 `stack/row/text/metric/progress/table/actions/callout/emptyState`，再映射现有
chart/flow。工具卡片与持久 surface 可共享节点词汇，但预算、动作和持久规则必须分别定义。
历史中的旧卡片需要永久通用 fallback。

## 4. AstrBot 源码结论

### 4.1 可移植的运行事实

`astrbot/core/agent/events.py` 把 turn、provider request、文本/思考增量、工具开始/进度/完成、
usage 和终态建模为不可变、带 discriminator 的事件。`runtime.py` 进一步规定：

- 一个 session 只有一条串行执行链，状态是 idle/queued/running/closed；
- `AgentTurn.events()` 是可回放 journal，多个观察者不会互相抢事件；
- 等待者取消不等于取消执行，owner cancel 与 graceful stop 也分开；
- `follow_up` 新建排队 turn，`steer` 只在同一来源、同一发送者、同一权限和同一插件集合下
  注入当前 turn；
- `InputReceipt` 在持久消费边界后确认，未确认的输入可以安全重试。

这比为每种 UI 单独定义临时 SSE 字段更适合 CA。CA 不需要复制全量事件总线，但应在现有 run
event 上补 `event_id/session_id/turn_id/sequence`、确定的终态和 generation，以便 Android/Web
断线后重建同一事实。

### 4.2 输入、输出和文件

`agent/input.py` 与 `agent/output.py` 将模型消息、IM 协议和展示输出分开：文本、图片、文件、
音频、视频、reasoning 和扩展内容是有类型组件；媒体引用区分 path/url/inline。`file_token_service.py`
用短时、默认单次的随机票据发布本地文件，而不是把服务器路径直接交给前端。

CA 的执行端文件回传应沿用这个方向，但必须额外绑定 device/session/operation、规范 MIME、大小、
hash、过期时间和下载审计。票据只授予读取已发布产物，不授予枚举父目录。

### 4.3 不搬的部分

AstrBot 的 Python Agent、Pipeline、平台 adapter 和插件系统受 AGPL 约束，且第三方 IM 仍是 CA
低优先级。近期只把 typed facts、turn ownership、媒体契约和 stop/drain 语义写成 CA 自有 Go
协议；未来需要多 IM 时优先把 AstrBot 作为独立网关，通过 Channel 协议接入，避免双 LLM 回复。

## 5. Aurabot 源码结论

### 5.1 仿生聊天不是单纯延迟

`src/chat/hub.js` 的核心是每 chat 独立状态：

- 连发先进入 debounce + max-wait 窗口，文本、图片、发送者和语音偏好一起合并；
- 私聊运行中有能力时使用 steer；群聊或不适合插话时进入下一轮队列；
- 当前轮结束统一 `release`，只唤醒一批已聚合的新消息；
- 排队消息的发送者和直接请求者不能给上一轮借权；
- 输入中提示、媒体适配、输出清洗和最终投递是不同边界。

CA 近期应采用“短窗口合并 + 明确 steer 或 next-turn”而不是一律把生成中的新消息堆到下一轮。
steer 必须满足同会话、同设备 principal、同角色权限和当前 turn 可接受边界；否则排队。

`src/chat/reply.js` 的 `|||` 是模型可选气泡边界，同时有最多五个文字气泡、长文/代码/列表保持
整体、输出清洗和一次引用约束。CA 已采用可选 `|||` 且上限更低，继续保留；不把按字数模拟
打字的 `bubbleDelay` 作为默认体验，移动端只需要真实 streaming/typing 状态。

### 5.2 主动搭话是一套可结算状态机

`src/features/companion.js` 和 `src/purpose/` 分离了醒着时段、idle、每日上限、随机间隔、追问概率、
素材、候选目的、沉默、投递结局和下一次计划。关键点是：

- 落在安静时段的计划推到醒来后并加抖动；停机恢复只补一次，不追赶所有错过任务；
- 模型调用开始即占预算，不能因为最后选择沉默就假装没有成本；
- sent/silent/failed/delivery-failed 分开结算；投递结果不确定时不换渠道重发；
- 主动消息允许 `[沉默]`，追问只在有限时间窗、有限概率下发生；
- 主人和白名单对象分开计数，跨聊天隐私不进入主动提示。

CA 已有概率闸门、每日额度、安静时段和共同话题。下一步不移植 Aurabot 的九维驱动或 QQ
平台状态，而是补齐 attempt 计费、沉默/失败结算、停机不追赶和单次投递语义。

### 5.3 安全机制可借鉴但不可代替 CA 授权

`src/security/approval.js` 将审批按 owner 私聊串行排队、超时失败关闭，并允许网页与 QQ 谁先答
谁结算；Abort 或卸载时只结算一次。这能指导多端审批同步，但自然语言“确认”不能替代 CA
现有绑定参数摘要的一次性票据。Aurabot 的 ownerQQ、DSH session 和危险命令 guard 也不能直接
扩大 CA 服务器管理范围。

## 6. CA 的目标协议

### 6.1 Remote Protocol v1

```text
CA Client API
  ├─ HttpCarrier       本机/SSH 隧道网页
  ├─ SecureWireCarrier Android 现有加密连接
  └─ AgentCarrier      后续 Windows/Linux 执行端
        │
        └─ Call + Subscribe + Cancel + Resync
```

每条连接公布 `protocol_version`、`generation`、`peer_id`、`capabilities`、`allowed_endpoints` 和
`limits`。订阅事件携带稳定 sequence；重连先获取 authoritative snapshot，再从 cursor 继续。
写操作始终使用原 operation ID 查询结局，transport unknown 不自动重放。

### 6.2 Presentation Protocol v1

服务端保存规范化 view，客户端只渲染支持的子集：

```text
View { protocol, version, surface, min_app, root, fallback }
Action { id, label, schema, expected_revision }
Effect { type, payload, operation_id? }
```

首批 effect 只允许：insert draft、copy、HTTPS link（确认）、toast、refresh、replace/patch view。
运行 shell、审批、修改凭据、安装插件或切换执行端不属于普通 effect。

### 6.3 Conversation Runtime v1

一轮有 `queued → running → completed|failed|cancelled` 状态；输入收据、事件 journal、最终结果和
usage 都由 Core 持有。客户端可以：

- 新建 follow-up；
- 在严格同 principal/role/session 且运行时声明安全边界时 steer；
- 请求 graceful stop，失败后再 cancel；
- 重连后按 generation + sequence 恢复，而不是把“页面还开着”当作执行仍存在。

## 7. 实施顺序

这三条线不能一次性大改。按可独立部署的增量推进：

| 阶段 | 交付 | 最小验证 |
| --- | --- | --- |
| R1 Carrier seam | Go/JS/Kotlin 共用连接状态与错误表；HTTP、securewire adapter；不改密码学 | 同一会话经两种 carrier 行为一致；断线不把 unknown 写操作当失败重发 |
| R2 generation/resync | 连接 generation、事件 cursor、authoritative snapshot、审批/工具状态恢复 | 生成中断网再连只出现一份终态；待审批可恢复且只结算一次 |
| R3 Presentation v1 | 共享节点子集、预算、validator、capability downgrade；迁三个高频工具卡 | 旧历史有 fallback；未知组件不阻断消息；超预算拒绝 |
| R4 Effects v1 | 用户手势触发的受控 effects、revision、operation ID | 双击只执行一次；后台不能导航/执行命令；旧 revision 拒绝 |
| R5 Conversation steering | 短窗口连发合并；同权限私聊 steer；其他消息下一轮聚合 | 不漏答、不借权、不重复 turn；取消后 session 仍可用 |
| R6 主动性结算 | attempt/silent/failed/unknown、停机单次恢复、单渠道投递 | 掷空零模型；调用即计预算；投递不明不跨渠道重发 |
| R7 文件产物 | 有类型媒体/文件输出、短时下载票据、hash/MIME/大小/来源 | 不能读取任意路径；过期/重复使用按策略拒绝；历史保留元数据 |

R1–R4 是 dsh-Go 研究直接形成的近期架构工作；R5–R6 将 AstrBot/Aurabot 的运行与仿生机制
收敛到 CA 本体；R7 与双端 Agent、服务器小精灵、ComfyUI 共用。WebRTC、多 Host、动态插件市场、
第三方 IM 和完整 form 组件族仍按需评估，不是首批前置。

## 8. 最小测试与复用纪律

每阶段只保留一条正常路径和一条关键失败路径：carrier unknown、resync 去重、view 超限、effect
重复、steer 权限不一致、主动投递不确定、文件越界。复用现有测试，不增加全平台矩阵。

开始复制上游文件前必须：

1. 固定实际采用的 commit 和源路径；
2. 逐文件确认许可，AGPL 与 MIT 目录不能按仓库印象混用；
3. 在 `THIRD_PARTY_NOTICES.md` 记录目标文件和修改；
4. 保留对应许可证全文；
5. 让 CA Core 继续统一身份、角色、工具准入、写入授权和结算。

