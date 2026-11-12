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
| 服务器维护动作 `server_action` | 未配置。需要一个 basename 以 `chat-agent-` 开头、接受 `--operation-id` 的 helper |
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

写入路径（update / snooze / resume）尚未实测：它们要求当前轮用户消息里的原话作为依据，
我没有拿用户的真实消息去试。

## 尚未实测## 尚未实测## 尚未实测

以下有代码、有工具，但没有任何一次真实调用记录：

- `knowledge_manage`（资料整理）
- `episode_propose`（事件候选）
- `skill_load`（技能加载）
- `shared_activity_open` / `shared_activity_update`（共同活动）
- `care_preferences`（关怀设置）
- `image_generate` / `image_status` / `image_cancel`（图像生成全链路）
- `decide`（结构化决策打分）
- 主动消息（proactive）——需要等一个触发窗口
- 陪伴侧的连续聊天分段、话题预算

跑过之前不要把上面这些当成可用能力写进文档或演示。
