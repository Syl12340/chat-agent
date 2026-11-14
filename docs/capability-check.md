# 能力实测清单

记录哪些能力**真的在线上跑通过**，以及怎么跑的。写这一页的理由很简单：这个项目里
反复出现「代码在、接线在、却没人跑过」的东西——搜索解析、本机执行端、图像诊断都
栽在这上面。下面只列有实测证据的。

验证方式：在运维会话里提自然语言请求，看工具调用、返回和界面表现；
需要界面证据的用浏览器检查 DOM。

## 已实测可用

| 能力 | 证据 |
| --- | --- |
| 待办 `todo_add` | 写入成功，返回完整记录（id、revision、幂等 key），`status=open` |
| 提醒 `reminder_add` | 约三分钟后的提醒，`due_at` 换算正确；到点后 scheduler 把状态从 `scheduled` 改成 `due` |
| 提醒收件箱 | 到期后页面横幅显示「有 1 条到期提醒 · 查看收件箱 →」，任务计数为 1 |
| 提醒确认 | 收件箱里 `due` 状态提供「已收到」（→ `acknowledged`）和「取消提醒」两个动作 |
| 记忆候选 `memory_write` | 写入后 `status=pending`，模型明确说明需在记忆管理里确认后才生效 |
| 偏好 `companion_preferences_update` | 「偏好简洁回复」被记成 `reply_length=short`，走的是偏好字段而不是记忆 |
| 联网搜索 `web_search` | 英文查询返回 landlock.io 与内核文档；中文查询 5/5 命中 |
| 文献检索 `literature_search` | 工具被正确选中并返回候选，带 `relevance` 统计 |
| 服务器体检 `server_status` | 负载、内存、磁盘、监听端口、服务状态 |
| 服务详情 `server_service` | 白名单内单元返回 active/重启次数/内存/启动时间；白名单外的名字在参数层就被拒 |
| 沙箱命令 `workspace_shell` | `uid=993`、`/etc` 只读、工作区可写、出站 TCP 被拒、`Landlock ABI 4` |
| 本机文件 `local_workspace` | 模型读到本机工作区里的文件内容，回执带 `guard` 与工作区路径 |
| 工具卡片 | 展开后显示参数与格式化结果；多段回复在宽屏上合并为连续正文 |

## 已知不可用或有条件

| 项 | 现状 |
| --- | --- |
| 图像生成 | 桥可运行，ComfyUI 未启动；状态如实报「ComfyUI 没有运行」 |
| 中文搜索的极端查询 | 正常查询可用；搜索质量告警（`relevance.warning`）尚未在真实请求中触发过一次 |
| 服务器维护动作 `server_action` | 已实测：拒绝 → 界面批准 → 原样重试 → 真的执行 `prune-releases`，见下节 |
| ECNU eduwork | 服务器解析不到相关域名，未接入 |

## 主动消息

链路和手动验证方法见 [proactive.md](proactive.md)。实测：建一条已到跟进时间的 open 事件后，
`POST /api/care/check` 在 3.4 秒内完成「建条目 + 生成消息」，会话消息数从 43 变成 44，
页面出现灰色横幅「有 1 条关怀待查看」。

**触发源是事件，不是计时器。** 没有 open 且到期的 episode，把配置全开也不会发。

自动路径（不等人工 `check`）尚未观察到，仍需挂机一段时间取证。

## 会话暂停（paused）：设计如此，界面有出口

一个会话只要有 turn 没以 `completed` 结束，就会进入 `paused`：

```
if status != "completed" { next.Paused = true }
```

超时、取消、审批拒绝、模型报错都算。用意是失败后不自动重跑，`internal/turns` 的测试
明确断言这一点。

**界面是能恢复的**，链路完整：

- `/api/sessions/{id}/messages` 返回 `queue_paused: true`
- `web/turns.js` 据此显示 `#turn-resume` 按钮，文案是「继续队列」或「恢复聊天」
- 点击调用 `POST /api/sessions/{id}/turns/resume`
- 重连本身**不会**自动恢复，必须由人点

我用 API 探针验证时看不到按钮，先后两次误判成"界面没有出口"，第二次的更正也错了——
原因都是搜索范围不全（只搜了 `app.js` 和 `tasks.js`，漏了 `turns.js`）。

**判定方法**：会话持续失败时先开一个新会话对照。新会话正常、老会话全失败，就是
`paused`，点按钮或调 `turns/resume` 即可，不是服务故障。

## decide：实测不可用

`decide` 工具依赖 `/systemone` 端点（SystemOne 协议，专用模型 `ecnu-decision`）。
在当前的 ECNU 平台上这个端点不存在：

- 默认 `ecnu-decision` + `/systemone` → `决策模型连接失败`
- 把模型改成 `ecnu-max` → `422 missing field messages`（chat 端点不接受这个请求体）

两个都不是参数问题：第一个是端点缺失，第二个是端点选错了。**`decide` 从未成功返回过。**

上一轮为了让审批流程能跑，把 `CHAT_DECISION_MODEL` 设成了 `ecnu-max`，依据是
`ecnu-decision` 在 `/chat/completions` 上返回 500。那个前提就是错的——决策客户端
根本不走 chat 端点。该行已删除，默认值恢复。

**影响**：任何需要独立评估的操作（`ApprovalRequired` 的工具，例如 `server_action`）
都会卡在审批。修这条路要么找到可用的 SystemOne 端点，要么把审批改成不依赖它的形式。

## 图像链路：桥和隧道通了，ComfyUI 没开

`/api/status` 的 `image_reason` 会区分三种状态，它们指向完全不同的位置：

| 文案 | 含义 | 该看哪里 |
|---|---|---|
| 无法连接到图像生成桥，检查隧道是否在线 | 服务器够不到本机 bridge | 反向隧道 |
| ComfyUI 没有运行 | bridge 通了，它后面没东西 | 本机 ComfyUI |
| （空，`image_generation: true`） | 全链路可用 | — |

**隧道断了的一个隐蔽原因**：在客户端 `Stop-Process -Force` 掉 `serve-local.py` 之后，
服务器端的 sshd 会继续占着反向端口。下次启动就是

    Error: remote port forwarding failed for listen port 18190

客户端看起来是"隧道起不来"，实际服务器上还有一个僵尸监听。清理方式：

    ss -tlnp | grep 18190          # 找到 sshd 的 pid
    kill <pid>                      # 释放端口后再起隧道

本轮就是这样恢复的，之后 `image_reason` 从"无法连接到桥"变成"ComfyUI 没有运行"——
链路是通的，缺的只是本机那个 ComfyUI 进程。

## care_preferences：实测可用

读取路径在 0.6.2 上验证通过：

    {"role_id":"companion","revision":18,"enabled":true,"auto_generate":false,
     "auto_probability":100,"daily_limit":5,"quiet_start":"22:00","quiet_end":"08:00",
     "min_interval_minutes":180,"snooze_until":null}

模型读得准，还自己接上了当前时间：

    关怀功能：已开启，但自动生成是关闭的（auto_probability 100% 目前不起作用）
    ...
    顺便提醒一下～现在是凌晨一点多，正处在安静时段里，你还没睡呀？

注意 `auto_generate:false` 时 `auto_probability:100` 不起作用——它只影响可选的每日自动
问候，不是关怀总开关。这一点模型解释对了。

写入路径（update / snooze / resume）要求当前轮用户消息里的原话作为依据，
没有拿用户的真实消息在线上试过。但它不是"没跑过"：`internal/api/care_tools_test.go`
用假模型驱动真实插件、真实写入闸门和真实存储，跑通 snooze → resume → 保留既有计划
这一整条，并断言没有依据、依据被改写、依据来自助手消息时写入都会失败。
`plugins/builtin/care/plugin_test.go` 另外覆盖了修订号过期、以及两种拒绝路径。

## 尚未实测

以下有代码、有工具，但没有任何一次真实调用记录：

- `episode_propose`（事件候选）
- `skill_load`（技能加载）
- `shared_activity_open` / `shared_activity_update`（共同活动）
- `image_generate` / `image_status` / `image_cancel`（图像生成全链路）
- `decide`（结构化决策打分）
- 主动消息（proactive）——需要等一个触发窗口
- 陪伴侧的连续聊天分段、话题预算

跑过之前不要把上面这些当成可用能力写进文档或演示。

`knowledge_manage` 只算跑通一半：0.6.7 上在真会话里让它 `search` 事实，工具真的被调到并
返回了已批准记录（读路径成立）。**写路径（update / forget / merge）仍然只有测试覆盖**，
和 `care_preferences` 一样缺一次真实的用户原话。

## server_action 的审批链：已走完，从拒绝到执行

0.6.5 上完整跑通了。会话 `d225d65e90875bb4d42a9827446f48db`，动作 `prune-releases`：

1. 第一次调用 → `DECISION_NEEDS_APPROVAL`，摘要
   `acd3255df6cd268427422127b2e7f4be916f5afbca3c5ad38848aecc1e9d2546`
   （摘要只取决于参数 `{"action":"prune-releases"}`，所以每轮都一样）。
2. 模型没有替用户假设同意，而是讲了风险、要求界面确认。
3. `POST /api/approvals` → 票据 `apr_f40cba27fa9aec0a54b6bbf601a6fb68`。
4. 原样重试 → **真的执行了**，返回

       102 个版本，保留 10 个（含当前 20261114-approval-record-01），
       可清理 92 个，约 946.4 MiB。这是预演，加 --apply 才会真的删除

票据一次性：再重试一次会重新要票，符合设计。

### 这条路上踩到的两个 bug

**一、一次审批拒绝能把整个会话锁死（0.6.4 修）。**

写入侧记录 `tool.decision` 事件，reference 的 kind 写的是 `"decision"`；校验侧
`validEvent` 的白名单里既没有这个 kind，也没有 `tool.decision` 这个事件类型。于是日志
从写入那一刻起就是「不可读」的：下一次 `runlog.Open` 遍历事件时必然报
`invalid event at sequence 4`，之后这个会话**每一条消息**都返回

    503 运行记录暂时不可用；没有调用模型

看起来像会话坏了，其实是自己写的东西自己读不回来。触发条件就是任意一次门禁拒绝，
也就是 `server_action` 的必经之路。修法是让校验侧接受这个形状，并加了一条回归测试
（把 `validEvent` 的修改撤回后，测试会以 `invalid event at sequence 4` 失败）。

顺带补了日志：这条 503 之前**一行日志都没有**，operator 分不清是日志坏了还是磁盘满了。

**二、已批准的调用还会再要一次批准（0.6.5 修）。**

票据被消费后，记录里仍然带着 `Approval` 字段，客户端据此又渲染一个确认按钮；再点只会
拿到 409（拒绝记录已经被消费掉了）。现在批准后只留 `ApprovalID` 作为审计线索。
`TestHostTicketCoversOneExactCall` 断言了这一点。

### 第三个坑：30 秒的回合预算不够

`-timeout` 是**整个回合**的预算，不是一次模型调用的。审批流一轮要调两次模型，第二次
还要读进 32 KiB 的工具输出——30 秒必然超时，报「模型响应超时」。

0.6.5 起 `start.sh` 支持 `CHAT_TURN_TIMEOUT`（传给 `-timeout`），线上设成 `120s`。
`/api/status` 的 `timeout_seconds` 可以看到当前值。

**仍然待办**：`server_action` 的输出会原样把 92 个版本的 JSON 全塞进上下文。摘要那行
已经够人看了，完整列表对模型判断没有增量，却吃掉大半预算。下一轮该给它加输出上限。

### 主机工具的开关方式（0.6.6 起可以在运行时开关）

主机工具（`server_*`、`workspace_*`）按会话授权，两条路并集：

- `CHAT_HOST_TOOL_SESSIONS=<会话 id,...>`：部署级默认，改它要重启
- `POST /api/host-tools/sessions {"session":"…","allow":true}`：运行时授权，落在
  `<数据目录>/host-tools.json`，最多 8 个会话，立即生效、立即能撤销

页面上在「设置 → 服务器运维」里，一个开关管当前会话，只有 owner/管理员看得到。
**它不是模型工具**：助手既不能给自己开权限，也不能扩大会话名单；开关本身也不等于执行
许可，`server_action` 仍然要逐次审批。

实测（0.6.6，会话 `d225d65e…`，全程没有重启）：

- 授权前：`/api/host-tools` 报 `configured: true`、10 个工具、`sessions: []`
- `POST allow:true` → 同一个会话发一句「用 server_status 看一下服务器」，模型真的调到
  了 `server_status`，拿到磁盘和监听端口
- `POST allow:false` → 再问一次，执行器直接回 `UNKNOWN_TOOL`

**为什么 `configured` 与会话名单解耦**：以前 `hostPlugins()` 在会话名单为空时直接不建
插件，导致「先授权、后使用」永远不可能——插件根本不存在。现在只要部署配置了
`CHAT_SERVICE_UNITS` 或 `CHAT_WORKSPACE_ROOT` 就建插件，谁能看见由 `hostPluginAllowed`
按会话决定。

**界面上踩的坑**：第一版开关点了没反应。原因是点击后 `render()` 先用**旧**的服务端状态
把复选框写了回去，紧接着 `JSON.stringify({allow: allow.checked})` 读到的已经是回填后的
`false`，于是每次都在请求「取消授权」。修法是把用户意图单独存在 `pendingAllow` 里，
发请求用它、渲染也用它。这个 bug 用 API 探针完全看不出来，只有真的点一下才会暴露。

## 插件契约：为什么有些工具搬不动（0.6.3 已解决两个）

宿主工具搬到 `plugins/builtin/*` 的前提是它只能依赖「注入的窄接口 + 工具上下文」。
卡了很久的两个缺口：

1. **当前轮用户原话**：`care_preferences`、`topic_preferences`、`episode_propose`、
   `knowledge_manage` 都要校验「用户确实这么说了」。插件的 `StepContext` 里没有
   `inputs`，只能看着 `evidence` 字段干瞪眼。
2. **跨会话同角色运行状态**：两个同角色会话同时读改写同一份带修订号的设置，后写的
   会静默覆盖前一个。

0.6.3 补的东西：

- `probe.ToolInputs(ctx)` / `probe.WithToolInputs` —— 只有本轮用户消息，助手输出、
  工具结果、更早的历史都不在里面。`probe.TurnHasEvidence` 跟它配套。
- `pluginapi.WriteGate` —— `WithRoleWrite(role, session, func() error)` 把整个
  读改写塞进宿主的锁里；查完再写中间不能有别的 run 插进来。拒绝时返回
  `ErrWriteSessionEnded` / `ErrWriteRoleBusy`，插件自己翻译成中文。
- `probe.ToolContext(...)` 把“哪次调用、哪个会话、哪个角色、哪个幂等键”收敛成唯一
  一份定义（执行器也用它），插件因此第一次可以写单元测试——之前 memory / tasks /
  skill 三个插件是零测试，因为 context key 全是包内私有。

同时把 `care_preferences`、`topic_preferences` 搬成了插件。现状：

    11 个插件 / 27 个工具

还留在宿主里的 4 个：`episode_propose`、`knowledge_manage`、`shared_activity_open`、
`shared_activity_update`。前两个现在只差搬（依赖都齐了），后两个还要跨会话的 run 状态。
另外 `image_generate` / `image_status` / `image_cancel` 和 `local_workspace` 单独计。
