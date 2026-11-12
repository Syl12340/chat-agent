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

## 已知缺陷：会话被暂停后，界面没有办法恢复

一个会话一旦有 turn 没有以 `completed` 结束，就会进入 `paused`。之后**每次提问都立刻
失败、正文为空**，而服务本身完全正常。

上一版把原因写成"待审批的调用卡死会话"，那是错的。审批只是众多失败之一，
`store.go` 的规则是：

```
if status != "completed" { next.Paused = true }
```

超时、取消、审批拒绝、模型报错——**任何一种失败都会暂停会话**。

这是刻意的设计，`internal/turns` 的测试明确断言"failure did not pause queue"会失败。
用意是失败后不自动重跑。

**真正的问题是没有出口。** 恢复端点是 `POST /api/sessions/{id}/turns/resume`，
它能用——手动调用返回 200，`paused` 变回 `false`，紧接着提交的 turn 能正常完成。
但前端从来没有调用过它：`web/app.js` 和 `web/tasks.js` 里搜不到 `resume`。
用户看到的是"发什么都失败"，没有任何提示，也没有按钮。

**判定方法**：一个会话持续失败时，先开一个新会话对照。新会话正常，就说明是 paused，
不是服务故障——我为此连续误判了好几轮。

**待修**：在界面上把 `paused` 显示出来，并接上那个已经存在的恢复端点。

## 尚未实测## 尚未实测

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
