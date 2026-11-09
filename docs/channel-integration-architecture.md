# Channel Integration Architecture

更新：2026-11-08

状态：P0–P2 已随服务端 0.25 部署，默认关闭；生产尚未配置 OneBot 凭据

基线：chat-agent 0.24.0，commit `7083aaa`

## 1. 总体决策

chat-agent 增加独立的 Channel / Integration Layer，使 QQ、Web、Android、DSH 和 AstrBot 复用同一套身份、会话、记忆与 Agent Runtime。

原则：

1. 一个 Agent Runtime，多种入口。
2. 不重写 Harness，不改变 runtime v2、turns v3。
3. Channel 不通过 HTTP 调用本服务的公开 API，而使用受限 Go 内部接口。
4. 第一版只支持一个明确配置的主人 QQ 账号私聊。
5. 入站、模型执行、出站分别结算；不确定的副作用不自动重试。
6. Go 标准库优先，不引入第二套 Agent Loop 或动态插件运行时。
7. OneBot v11 是协议边界；SnowLuma 只是可替换的外部实现。

第一条交付链路：

```text
QQ
 │
SnowLuma
 │ OneBot v11
Channel Ingress
 │
Existing Session / Turns Queue
 │
Existing Go Harness
 │
Completed Turn
 │
Channel Outbox
 │
SnowLuma
 │
QQ
```

P0–P2 只实现这条链路。AstrBot、DSH Bridge、Aurabot 式模块化先保持稳定扩展边界。

## 2. 当前基线与边界

0.24 已有：

- Go HTTP 服务、原生 JavaScript Web/PWA、Kotlin Android 包装。
- runtime registry、format index 和 manifest 选代。
- RunRecorder、attempt、step、tool 运行事实与请求快照。
- turns store 持久队列、request ID 去重、进度和恢复。
- 约 450 ms 的消息聚合窗口。
- 模型、工具、角色、审核制记忆、活动、任务和业务回执。
- chat-ui v1 的七类 Generative UI 组件。

必须保留：

- 新 webhook 不复用设备 Bearer 身份；它使用独立 HMAC、机器人账号检查和 sender ACL。
- Channel 来源、远端 message ID、映射和投递状态进入独立 Channel Store。
- 普通 Channel 消息必须进入现有 turns queue，不直接调用 Provider，不建立第二个 worker。
- 第一版只允许 owner，不能把 guest 或群聊写入同一角色的私人记忆。
- 不扫描旧 transcript，不绕过 runtime manifest 自行选择 generation。

## 3. OneBot v11 入站契约

### 3.1 双向认证

| 方向 | 认证 |
| --- | --- |
| SnowLuma → chat-agent | 原始请求体 HMAC-SHA1，`X-Signature: sha1=<hex>` |
| chat-agent → SnowLuma | `Authorization: Bearer <token>` |

必须使用两套独立凭据：

- `ONEBOT_INGRESS_SECRET`
- `ONEBOT_ACTION_TOKEN`

Webhook 在 JSON 解码前对原始字节验签，使用常量时间比较。`X-Self-ID` 只做账号一致性检查，不能代替 HMAC。

### 3.2 MVP 接受范围

只接受：

- `post_type=message`
- `message_type=private`
- `sub_type=friend`
- body `self_id` 与 `X-Self-ID` 都等于配置的机器人账号
- `user_id` 等于明确配置的 owner QQ ID
- 非机器人自身消息
- 有 `message_id`
- 字符串纯文本，或仅包含 `text` 的 message segment 数组

guest、群聊、临时会话、自身消息应在进入模型前被忽略。图片、语音、视频、文件、卡片和 CQ 段第一版明确拒绝，不猜测也不自动补全。

### 3.3 Channel Envelope

`internal/channel` 定义传输无关结构：

```go
type Envelope struct {
    Version       int
    EventID       string
    Platform      string
    Adapter       string
    AccountID     string
    Conversation  Conversation
    Sender        Sender
    Message       Message
    OccurredAt    time.Time
    ReceivedAt    time.Time
    PayloadSHA256 string
}
```

约束：

- 远端 ID 一律转换为字符串；DisplayName 仅用于展示。
- 不持久化完整原始 OneBot payload。
- 进入 turns 后，正文继续由现有 runtime history 保存。
- Inbox 可在 turn 持久化后清除正文，只保留 digest、身份和状态。
- 相同 EventID、不同 PayloadSHA256 是冲突，不是重复。

### 3.4 确定性 request ID

```text
lowercase_hex(SHA256("onebot:v11:" + account_id + ":" + message_id))
```

使用完整 64 位十六进制值。Webhook 在 Inbox 状态更新前退出时，再次提交仍由 `turns.EnqueueWithOptions` 识别 duplicate，不产生第二条用户消息。

## 4. Session 与 Identity

映射键：

```text
platform / account_id / conversation_kind / conversation_id / role_id
```

MVP 固定：

- platform：`onebot-v11`
- conversation kind：`private`
- conversation ID：owner QQ ID
- role ID：`companion`，或服务端显式配置值

映射创建使用 provisioning 状态：

1. 查询映射。
2. 不存在时生成随机 session ID。
3. 先持久化 mapping=provisioning。
4. 检查 runtime registry 是否已有该会话。
5. 不存在则创建，存在则核对 role ID。
6. 只打开 manifest 选中的 history。
7. mapping 更新为 active。

身份关联不能根据昵称自动完成。未来多用户需先引入 Principal / Identity 层，并重新设计 memory 的身份隔离。

## 5. Inbox / Outbox

建议目录：

```text
<data>/channels/onebot/
  state.json
  inbox/
    <event-key>.json
  outbox/
    <delivery-id>.json
  .store.lock
```

遵循现有存储规则：目录 0700、文件 0600、严格 JSON、容量上限、临时文件原子替换、密钥不落盘。

Inbox 状态：

```text
accepted -> queued -> completed
        \            \-> failed
         \-> failed / needs_attention
```

- `accepted`：事件已持久化。
- `queued`：确定性 request ID 已进入 turns。
- `completed`：模型成功且 Outbox 已创建。
- `failed`：明确且不可恢复的错误。
- `needs_attention`：状态不确定，必须显式处理。

Outbox 状态：

```text
pending -> sending -> sent
                   \-> unknown
                   \-> failed
```

发送前先持久化 `sending`。只有 OneBot 明确返回成功才写 `sent`；明确拒绝写 `failed`；超时、断线、响应解析失败或重启遗留 `sending` 都写 `unknown`。`unknown` 永不自动重发。

不宣传 exactly-once。设计选择是牺牲自动补发，避免角色重复发言。

## 6. 与现有 Harness 的接缝

### 6.1 共享提交接口

P0 已将 Web 队列入口抽取为：

```go
type TurnSubmission struct {
    SessionID string
    RequestID string
    Content   string
    Mode      string
    Model     string
    Thinking  string
    Images    []media.Image
}

func (s *Server) SubmitTurn(
    ctx context.Context,
    input TurnSubmission,
) (turns.Turn, bool, error)
```

Web 仍负责设备认证和图片引用解析；Channel 负责 HMAC、Principal 和 ACL。共享入口负责 session 检查、互动响应校验、选项规范化、队列去重和 worker 启动。Channel 使用 `DeferStart`：先让 turn 与 Inbox queued 都持久化，再启动同一个 worker，避免极快回复越过入站结算。

### 6.2 唯一 worker

Channel 继续使用现有：

- 450 ms aggregation。
- 原有并发与队列上限。
- 原有工具预算、超时和停止规则。
- runtime v2 recorder 与 turns v3 结算。

不创建新的 LLM worker。Channel-bound session 仍可在 Web UI 中查看。

### 6.3 完成观察器

P0 已增加可选 `TurnCompletionObserver`。只有以下条件全部成立才调用：

1. `probe.RunMessages` 正常结束。
2. `Recorder.Close` 成功。
3. `turns.FinishWithReasoning` 成功。
4. 最终状态为 completed。

观察器收到 session ID、root request ID、合并的 request IDs、最终可见文本和完成时间。它只能持久化 Outbox 意图，不能在 model worker 中执行网络发送。失败、取消、interrupted 和 progress-save 失败不产生回复意图。

## 7. 出站 OneBot Action

MVP 只允许：

```http
POST /send_private_msg
Authorization: Bearer <ONEBOT_ACTION_TOKEN>
Content-Type: application/json
```

```json
{
  "user_id": 123456789,
  "message": [
    {"type": "text", "data": {"text": "最终回复文本"}}
  ]
}
```

目标 QQ ID 必须来自已验证 mapping，不接受模型或事件正文提供任意发送目标。MVP 不开放群管理、撤回、文件、Cookie、任意 Action 或 SnowLuma MCP write mode。

P0 的 Action client 只接受 loopback HTTP 地址和独立 Bearer Token，为 P2 dispatcher 提供窄接口。

### Renderer

QQ 只发送最终可见文本：

- 不发送 reasoning、工具协议或 partial stream。
- 不原样发送 chat-ui JSON。
- Markdown 做有限降级。
- 交互组件降级为文本摘要或提示用户打开 Web UI。
- Renderer 必须是确定性纯函数。

## 8. 重启和恢复

- 不自动恢复 running/interrupted 收费模型请求。
- 不自动解除 turns queue 的 paused 状态。
- accepted 但未 enqueue 的旧事件转 needs_attention。
- queued 遵循 turns 自身恢复语义。
- `/continue` 只能由已验证 owner 显式触发。
- 遗留 sending 转 unknown，不自动发送。
- 发送失败不重新调用模型。

## 9. SSH 部署拓扑

本地 Windows 运行 SnowLuma、远端 Linux 运行 chat-agent 时，可使用独立双向隧道：

```text
ssh -N \
  -L 18080:127.0.0.1:9991 \
  -R 127.0.0.1:13000:127.0.0.1:3000 \
  user@chat-agent-server
```

| 地址 | 作用 |
| --- | --- |
| 本机 `127.0.0.1:18080` | 转发到远端 chat-agent 9991 webhook |
| 远端 `127.0.0.1:13000` | 转发到本机 SnowLuma 3000 Action API |

SnowLuma HTTP Client 指向 `http://127.0.0.1:18080/hooks/onebot/v1/events`；chat-agent Action base URL 为 `http://127.0.0.1:13000/`。两端默认只绑定 loopback，不把 webhook 或 Action API 暴露到公网。

## 10. 后续集成边界

### Aurabot

借鉴有类型事件订阅、工具/上下文/规则扩展、定时触发、生命周期清理、主人/访客/系统权限和跨平台记忆来源。Go 核心不加载动态 JS，不移植依赖 DSH 宿主的 host。后续能力模块采用静态、类型安全的 `Setup/Close` 接口。

### DSH Bridge

DSH 定位为受限客户端，不嵌入 chat-agent runtime。首批只读状态、会话检索、获准记忆、活动待办和角色公开配置；写入需独立 integration credential、scope 和显式授权，记忆只能提交候选。

### AstrBot

借鉴 EventBus、Pipeline、Platform Adapter 和停止/排空语义。未来作为外部多平台网关；转发给 chat-agent 后必须阻止 AstrBot 自己的 LLM Pipeline 再回复。不复制 AGPL 核心代码。

### SnowLuma

只作为 OneBot v11 外部实现。chat-agent 不复制或打包 SnowLuma 源码、二进制和 native addon，也不把其 MCP write mode 暴露成无限制模型工具。

## 11. P0–P2 实施状态

### P0：契约和内部接缝——已完成

- `internal/channel`：Envelope、Principal、ConversationKey、Inbox/Outbox 状态契约。
- `internal/channel/onebot`：原始请求体 HMAC、owner 私聊纯文本规范化、确定性 request ID、loopback Action client。
- Web `enqueueTurn` 与未来 Channel 共用 `SubmitTurn`。
- worker 成功结算后调用可选 completion observer。
- 未改变 runtime v2、turns v3、Provider 或工具循环。

### P1：主人 QQ 私聊入站——已实现、默认关闭

- 独立 webhook 路由与配置。
- Channel Store、session mapping、Inbox 持久化。
- owner ACL、稳定 session 和确定性去重。
- 显式 `/continue`。

### P2：最终回复与可靠投递——已实现、默认关闭

- completion observer 持久化 Outbox。
- 独立 dispatcher 与一次自动发送尝试。
- Renderer、unknown/failed/sent 结算。
- 双向 SSH 隧道脚本、状态和脱敏日志。

## 12. 最小验收

只保留关键行为测试：

| 行为 | 预期 |
| --- | --- |
| valid HMAC | 接受精确原始请求体 |
| body tampering / invalid HMAC | 401，不写 Inbox |
| wrong self ID / guest / group / self | 不进入模型 |
| owner friend private text | 产生规范 Envelope |
| same account + message ID | 相同 request ID |
| same event ID + different digest | conflict |
| crash after Inbox before enqueue | needs_attention，不自动调用模型 |
| crash after enqueue before Inbox update | turns 去重 |
| completed turn | 只持久化 Outbox 意图 |
| failed/canceled/interrupted | 不生成回复意图 |
| outbox sending at restart | unknown，不重发 |
| OneBot `retcode=0` | sent |
| timeout / invalid response | unknown |
| non-zero retcode | failed |

测试使用临时目录与 fake OneBot server，不调用真实 QQ、真实模型或生产数据。

## 13. 配置草案

```text
ONEBOT_ENABLED=false
ONEBOT_ACCOUNT_ID=
ONEBOT_OWNER_QQ_ID=
ONEBOT_INGRESS_SECRET=
ONEBOT_ACTION_BASE_URL=http://127.0.0.1:13000/
ONEBOT_ACTION_TOKEN=
ONEBOT_ROLE_ID=companion
ONEBOT_TIMEOUT=5s
ONEBOT_MAX_BODY_BYTES=262144
```

- 默认关闭，不做热更新。
- ingress secret 与 action token 必须分离。
- Action URL 默认只允许 loopback。
- 不接受事件正文提供 URL。
- 日志不记录 token、完整正文或原始 payload。

## 14. 风险和不承诺

1. HTTP POST 源端不保证失败重投，隧道断开期间事件可能丢失。
2. OneBot 发送缺少可依赖的跨重启幂等键，unknown 无法安全自动补发。
3. 当前 memory 主要按 role 隔离，不能直接开放给陌生人或群聊。
4. turns 的暂停和恢复语义优先于 Channel 自动化便利。
5. 不保证 Channel Store、runtime、turns 与 OneBot 的全局事务。
6. HMAC 证明请求来自持有密钥的一端，不单独证明 QQ 用户本人身份。
7. 群聊、访客、主动搭话和任意 QQ Action 需要单独权限模型。

## 15. 上游参考与许可

研究快照：2026-11-08。实施前应重新核对上游版本和许可。

- DeepSeek Harness 的适用许可允许直接复用源码与组件。Channel Bridge 的部署方式按具体能力和权限域确定。 [项目复用规则](../AGENTS.md)
- Aurabot：MIT；可借鉴模块 API、权限与记忆来源，复制或改写时登记许可声明。
- [AstrBot](https://github.com/AstrBotDevs/AstrBot)：AGPL-3.0-or-later；优先独立进程和协议，不复制核心代码。
- [SnowLuma HTTP POST adapter](https://github.com/SnowLuma/SnowLuma/blob/main/packages/onebot/src/network/http-post-adapter.ts)：确认 `X-Signature` HMAC-SHA1 和 `X-Self-ID`。
- [SnowLuma HTTP Server adapter](https://github.com/SnowLuma/SnowLuma/blob/main/packages/onebot/src/network/http-server-adapter.ts)：确认 Action HTTP/Bearer 行为。
- [SnowLuma LICENSE](https://github.com/SnowLuma/SnowLuma/blob/main/LICENSE)：Source-Available Non-Commercial License 1.0，不是 OSI 开源许可证，native addon 另有专有边界。

最终方向：先完成 P0–P2 的 owner QQ 私聊闭环。稳定后再引入跨平台 Principal、Aurabot 式模块化、DSH Bridge 和 AstrBot 多平台网关。
