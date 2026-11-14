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
| 服务器维护动作 `server_action` | helper 已配置（`server_releases` 能返回版本），审批票能开到，但**批准后的实际执行从未观察到**，见下节 |
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

- `knowledge_manage`（资料整理）
- `episode_propose`（事件候选）
- `skill_load`（技能加载）
- `shared_activity_open` / `shared_activity_update`（共同活动）
- `image_generate` / `image_status` / `image_cancel`（图像生成全链路）
- `decide`（结构化决策打分）
- 主动消息（proactive）——需要等一个触发窗口
- 陪伴侧的连续聊天分段、话题预算

跑过之前不要把上面这些当成可用能力写进文档或演示。

## server_action 的审批链：开到票，没走到执行

`server_action` 声明了 `Decision:"required"`、`Risk:"critical"`、`ApprovalRequired:true`，
所以调用路径一定是「先拿到一张审批票，再由人批准，然后重试」。

已经确认的：

- 未批准时返回 `status: approval_required` 和一个 `digest`（`acd3255d…`）
- `POST /api/approvals` 返回 200 和票据 `apr_d81eb56c…`，绑定到会话和摘要
- `/api/sessions/*/turns/resume` 对**不存在的会话**返回 404——这是对的，不是接口坏了

没确认的：批准之后那一次重试真的把动作执行了。上一轮做这个验证时，重试撞上服务重启
导致的 `paused`，等去恢复时会话已经被删了（`/var/lib/chat-agent/sessions/runtime` 里
没有那个目录，`/api/sessions` 只剩 3 个真实会话）。

**教训**：拿一个测试会话做审批走查时，别让"重启"和"会话还在"互相干扰。重启一定会让
未完成的会话进入 `paused`，而清理会话时我连测试会话一起删掉了，证据链断在半路。

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
