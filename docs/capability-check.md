# 能力实测清单

> 2026-11-15 阅读说明：本页保留各次实际使用记录，不能把旧版本一次成功推及当前所有环境和路径。最新源码状态、QQ/群边界及本机执行缺口见[项目评估](project-assessment-2026-11-15.md)，后续能力设计见[产品方向](product-direction.md)。本次仅更新文档，没有重做以下实测。

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

## 对话形态的实测（0.6.9 上的历史数据）

46 条助手回复的统计：

| 分组 | 条数 | 平均字数 | 带列表 | 带代码块 |
| --- | --- | --- | --- | --- |
| 纯聊天（无工具） | 24 | 305 | 6 | 1 |
| 调过工具之后 | 22 | 1394 | 5 | 7 |

纯聊天的 305 字说明"仿生语气"这件事没有系统性问题——提示词里"不要读起来像报告"
那一段是有效的。异常值集中在工具输出，已经在 0.6.10 修掉。

## 界面复查（0.6.8 上的一次真实端到端取图）

桌面 1440×900、手机 390×844 各取一遍：会话列表按今天/昨天/更早分组、工具卡片
（`整理资料 · 已完成`）、设置里的四个分区、移动端抽屉和欢迎页都正常，没有发现明显坏掉
的地方。

**一个已知的小竞态，没有修**：登录流程收尾时会调用 `selectSession`，而它会
`drawer(false)`。如果用户在首屏刚出来的一两秒内点开菜单，抽屉会被这个收尾动作关掉，
看起来像"点了没反应"。窗口很短，改法要在 `selectSession` 上区分"用户从抽屉里选的"和
"连接流程选的"，属于那种改了收益不确定的改动，先记在这里。

查这个的时候差点误判：一开始 Playwright 点 `#menu` 没反应，我以为是触摸事件被
`preventDefault` 吃掉了。加了 `DOMTokenList.toggle` 的调用栈探针才看到真相——
`drawer(true)` 执行了，紧接着 `selectSession` 又把它关掉。**先取证再动手，别先写修复。**

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

## 双端 Agent：中文 Windows 的文本读不出来的问题（0.7.5）

本地 agent 第一次真跑时，`list` 成功、`read` 报 `workspace file is not bounded UTF-8 text`。
当时以为是我把测试文件写错了编码，查下去发现两件事：

1. 那个文件确实不是 UTF-8：首字节 `D5 E2 CA C7` 是 GBK 的「这是」。我用 PowerShell
   `Set-Content` 写的，中文 Windows 上它默认就是 GBK。
2. 但这暴露的**不是测试问题，是产品缺口**：中文 Windows 的编辑器（记事本、VS Code 默认、
   Office 导出）至今大量写 GB18030。本地 agent 只吃 UTF-8，等于在这台机器上读不了大部分
   中文文本文件——而这正是它存在的理由。

修法：UTF-8 校验失败后再尝试 GB18030 解码，成功就返回文本；仍然不是合法 GB18030 的（二进制）
继续拒绝。解码在 UTF-8 通过之后才发生，所以已经是 UTF-8 的文件一个字都不会被改动。

为了这个引入了 `golang.org/x/text`（此前项目只有 websocket 一个三方依赖），理由和许可证
记录在 `THIRD_PARTY_NOTICES.md`，BSD-3 全文在 `licenses/golang-x-text-BSD.txt`。

### 端到端实测（0.7.5，本机 127.0.0.1:9992 → 服务器）

| 步骤 | 结果 |
| --- | --- |
| list | 返回 5 个真实文件 |
| read `笔记.txt`（GBK） | 正确解码为「这是本地 agent 演示目录。里面只有几段文字，没有隐私内容。」 |
| `local_action` hostname | 正确走到审批链，模型解释风险并请用户确认，没有擅自执行 |

临时会话验证完已删除。

## 工具调用折叠（0.7.6）

一次回合里调两个工具，原来是两张各 56px 的独立卡片纵向堆叠，把对话往下推了 120px 以上
才说正事。连续 loop 让"多工具回合"变成常态之后，这个代价只会越来越大。

现在从第二个调用开始，整组折成一行 `▸ 2 个工具调用`；点开是原来的卡片，带左侧引导线。

Playwright 实测：折叠态 `groupH=28`、展开态 `groupH=152`（两张卡片），`open:false` 是初始
状态。回合内的步骤行仍然实时显示「第 N 步 · 正在执行 X · 已用 Ns」，所以折叠不会藏住
「现在在干什么」。

样式单独放在 `web/tool-group.css`，和上一轮的 `sidebar-compact.css` 一样，不去改
`tool-card.css` 里已经成型的规则。

## 输入区与侧栏（0.7.4）

### 发送和停止不再抢同一个位置

原来的 `.composer-bottom` 里并排放着两个按钮：圆形 ↑（发送）和一个带文字的「■ 停止生成」。
生成中两个同时出现，视觉上互相争抢，也没有主次。

现在停止按钮改成与发送同尺寸的圆形方块（36×36，深色为主），发送按钮在生成中降为次要样式
（`is-queued`：34×34、浅色）并留在原位——**因为生成中继续输入排队发送是这个产品的既有能力**，
不能为了对齐 DSH 的视觉而砍掉它。主次分明之后，两个按钮不再冲突。

Playwright 实测：`stop{36×36}`、`send{34×34, queued:true}`、`note:false`、
`brandSub:none`、`filter 32px`、`newChat 42px`。

### 侧栏从九块压到六块

去掉营销文案「一点想法，随时放进口袋」、品牌副标题「你的随身助手」，压低新会话按钮
（56→42）、筛选框（→32）、会话行（→32）、列表标题间距。

覆盖样式放在独立的 `web/sidebar-compact.css` 里，而不是改散在 `style.css` / `chat-ui.css` /
`dsh-look.css` 三张表里的原规则——层叠已经很复杂，动原规则的风险高于收益。

### 又一次编码事故（必须记录）

这轮两次用 PowerShell 的 `Set-Content` 改写含中文的前端文件，**都写坏了 UTF-8**：
`index.html` 里的 `</small>` 变成 `口袋�?small>`，`server.go` 上一轮也这样炸过。

两次都靠 `git checkout` 恢复，然后改用 edit 工具重做。结论固定下来了：
**不要用 shell 重定向改写含多字节字符的源文件**，改用能识别编码的编辑工具。

## 长任务可观察 + 反馈调频（0.7.3）

### 回合内的步骤状态

持续 loop 之后，一轮对话可以跑很多步，而状态行还只写「正在回复…」——那句话在单步时代
是信息，在多步时代是噪音。

现在进行中的回合显示：

```
第 2 步 · 等待模型回应 · 已用 1s
```

- 步数取自本轮已产生的工具卡片数
- 正在执行时显示工具名，否则显示「等待模型回应」
- 耗时由一个 1 秒的 ticker 刷新，**起点记在会话上而不是卡片上**——回合每次轮询都会重建
  卡片，起点跟着卡片走就会永远显示 0s。第一版正是这样错的，Playwright 抓到 `已用 0s`
  才改过来

Playwright 实测（截图 `.cache/ui-review/task-trace.png`）：两张工具卡片「读取时间」「查看待办」
均为「已完成 · 服务端」，状态行 `第 2 步 · 等待模型回应 · 已用 1s`。

### 反馈不再只被收集

`helpful` / `less` 之前只是存下来给模型看，从不影响调度——收集了却不使用。

现在 `ClaimCare` 用 `careInterval` 代替固定的 `min_interval_minutes`，按最近 30 天里有反馈
的问候调整：

| 情况 | 间隔 |
| --- | --- |
| 没有反馈，或全是 helpful | 保持配置值 |
| 少数是 less | ×2 |
| 一半及以上是 less | ×4 |
| 上限 | 24 小时封顶，30 天外的旧反馈不参与 |

封顶和窗口都是刻意的：一个糟糕的星期应该让问候变少，而不是永久关掉；一年前的评价也不该
继续拖累今天。

### 持久 GenUI：核实结论是「已经具备」

按要求做了核实而不是改动。这条链路已经不缺：

- 卡片绑定 `source.message_index + sha256`，来源变化即失效
- `reconcileOne` 遍历历史 turn，用 `parseUIResponse` 找到匹配回答后 `applyAnswer` 恢复
  选项/填空，且明确只做只读恢复、不重新提交
- `answerTurns` 还会从被队列裁剪的 `uiSourceMessages` 里找回原始回答，所以老卡片的答案
  不会因为队列轮转而丢失

**未做端到端验证**：线上三个会话里 `chat_ui` 卡片数量为 0（`cards: 0 answers: 0`），没有
可验证的真实卡片。没有为了凑证据去伪造一个卡片。

## 主动关怀：没人回应的问候不再追加（0.7.1）

线上数据里有一条很典型的证据。2026-11-12 晚上 23:22、23:27、23:59 三个时间点，同一个
「补 chat-agent 文档」上下文生成了三条问候：

- 「这么晚还在写文档，辛苦啦。豆豆睡了吗？」
- 「已经过零点啦，该睡了。文档明天再写也来得及。」
- 「那份 chat-agent 的文档，可以定一句开头……」

三条状态都是 `seen`，**用户一条都没有回**。事件本身是三次不同 episode（id 各不相同），
所以按事件去重拦不住；问题在「上一条没回应就接着发第二条」。

现在 `runCare` 在生成前做一次确定性判断（`unansweredCheckIn`）：取最近一条已投递
（`ready`/`seen`/`delivering`）且仍在 12 小时内的问候，看它落地的那个会话里，它之后有没有
任何用户消息。没有就直接 `SilenceCare`，状态记 `silent`，不调用模型、不花钱、不打扰。

选择放在服务端而不是提示词里，是因为提示词会漂移；这条规则不允许漂移。12 小时这个窗口
是刻意的：超过半天还没说话，更可能是「另一个时段」而不是「无视刚才那条」。

**未线上实测**：这条路径需要真实触发一次关怀才有线上证据，现有 care 机会都已终结
（`seen`/`canceled`），没有可复用的 due 项。逻辑由 `newestCheckIn` / `answeredAfter` 的
单元测试覆盖，全量回归通过。

另外，这条规则**只拦自动路径**。0.7.2 修掉了一个我自己引入的缺陷：最初它对手动生成也生效，
于是用户主动点「现在生成一条」会被「上一条没回应」拦住。手动是已经做出的决定，不该被
自动抑制规则覆盖。

### 动机：让模型知道「为什么是现在」（0.7.2）

0.7.1 只解决了「没人回就别再发」，没解决「凭什么现在发」。提示词要求模型「没有真实理由
就沉默」，但喂进去的材料里只有偏好和最近三次问候内容——**理由本身是缺失的**。

现在 trigger 里多一个 `motivation`：

| 字段 | 含义 |
| --- | --- |
| `check_ins_for_this_episode` | 这个事件已经投递过几条问候 |
| `hours_since_due` | 这个机会从到期到现在积压了多久 |
| `has_event` | 是否绑定了具体事件（日常问候没有） |

提示词据此给出判定规则：同一事件已经问过 ≥1 次时，不要重复同样的关心、开场或建议，
除非确实有新情况，否则宁可选 `[沉默]`；`hours_since_due` 很大说明时机已过，只在消息仍
符合当前时间时才开口。

**为什么是这两个数而不是「用户多久没说话」**：`probe.Message` 没有时间戳，会话里也拿不到
可靠的最后用户发言时间。与其塞一个猜出来的值，不如只给能确定的。

**仍然未线上验证**：线上 `POST /api/care/check` 返回 `generated:false`，6 条历史机会全部
终结，造不出 due 项。没有为了凑证据去捏造事件或往真实会话里塞主动消息。

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

### 维护动作的输出以前会糊满一整条回复（0.6.10 修）

线上 46 条助手回复里，最长两条各约 **12,700 字**——都是干跑 `prune-releases` 之后把
helper 的 JSON 原样念回来的。其余 44 条中位数在 300 字上下（纯聊天平均 305 字，完全正常），
所以这不是"语气像报告"的问题，是**工具有一次把 12 KiB 塞进了对话**。

`server_action` 以前把 helper 的 stdout 整个放进 `detail`，而 `cappedBuffer` 的上限是 32 KiB。
现在 `boundReport` 保留开头 4 KiB 并写明截断，切点落在合法 UTF-8 边界上：

    102 个版本，保留 10 个（含当前 20261114-bounded-action-01），可清理 98 个，约 1014.5 MiB。…
    …（输出已截断：只保留前 4096 字节，原文共 12186 字节）

0.6.10 上重跑同一条链路（授权 → 拒 → 发票 → 重试 → 真跑）：工具输出 **4099 字节**，
回复 **471 字**，模型自己说明"输出已截断"并照摘要讲。同一条路从 12,759 字降到 471 字。

工具描述里也加了一句：报告被截断时按现有内容总结，**不要为了补全而重跑动作**——`--apply`
类动作重跑是有代价的。

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
