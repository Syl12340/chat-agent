# 主动消息

用户会在会话里收到助手主动发来的消息。这一页说明它什么时候发、为什么大多数时候不发，
以及怎么手动验证一次。

## 链路

```
episode（事件）→ Tick → care 条目（due）→ checkCare → 模型生成 → 会话消息
```

**上游是事件，不是计时器。** `Tick` 只遍历满足这些条件的 episode：

- `status = open`
- `allow_followup = true`
- `followup_at` 已到

把它转成一条 care 条目；`checkCare` 再把到期的条目交给模型写成一句话。没有未完结的
事件，就没有主动消息——把配置全开也不会发。

这是设计选择：主动消息是「对某件没做完的事的跟进」，不是随机的问候。

## 配置

`GET/PATCH /api/care/settings`，按角色存。

| 字段 | 作用 | 约束 |
| --- | --- | --- |
| `enabled` | 总开关 | — |
| `auto_generate` | 自动生成开关 | 关闭时 `check` 直接返回 `generated:false` |
| `auto_probability` | 触发概率 | 0–100 |
| `daily_limit` | 每日上限 | 1–5 |
| `quiet_start` / `quiet_end` | 静默时段 `HH:MM` | 两者不能相同 |
| `min_interval_minutes` | 两次之间最短间隔 | **15–1440** |
| `daily_time` | 每日固定时刻 | 空表示不设 |
| `snooze_until` | 暂停到某时刻 | — |

## 手动跑一次

自动路径要等静默时段外的概率抽中，不方便验证。手动路径走同一段代码：

```bash
# 1. 建一个已到跟进时间的事件
curl -X POST .../api/episodes -d '{
  "title": "补文档",
  "content": "要在五天内补完 chat-agent 的文档。",
  "status": "open",
  "allow_followup": true,
  "followup_at": "2026-11-12T15:22:03Z"
}'

# 2. 打开闸门（概率 100、静默窗口挪开、间隔取最小值）
curl -X PATCH .../api/care/settings -d '{
  "revision": N, "enabled": true, "auto_generate": true,
  "auto_probability": 100, "daily_limit": 5,
  "quiet_start": "00:00", "quiet_end": "00:01",
  "min_interval_minutes": 15
}'

# 3. 一次调用同时完成 Tick 和生成
curl -X POST .../api/care/check -d '{}'
```

`check` 返回 `{"generated": true, "item": {...}}`，消息同时写进会话。

**记得把配置改回去。** 验证时用的闸门开着，等于允许系统随时打扰用户。

## 实测记录

- 事件创建返回 201，`status` 默认就是 `open`，不需要审核。
- `check` 在 3.4 秒内完成「建条目 + 生成消息」两步，条目状态 `ready`。
- 会话消息数从 43 变成 44，内容是针对那条事件的具体建议，不是泛泛的关心。
- 页面上出现灰色横幅「有 1 条关怀待查看 · 打开收件箱 →」，和提醒的橙色横幅分开。

## 字段陷阱

- 事件正文的字段是 **`content`**，不是 `summary`。传错会得到 400，报错只说「内容构成不符」。
- `AllowFollowup` 为真时 `followup_at` 必填，否则校验失败。
- `min_interval_minutes` 传 0 会被拒（下限 15），`quiet_start == quiet_end` 也会被拒。

## 尚未确定

- 自动路径（非手动 `check`）在没有人工干预时是否按预期抽概率并限流——需要挂着观察一段时间，
  目前只有手动路径的证据。
- care 条目变成 `ready` 之后，用户「已读」的交互是否会把状态推到 `seen`，以及是否影响下次生成。
