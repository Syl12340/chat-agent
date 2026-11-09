# RFC：v1 / v2 兼容方案

> 排期更新：以[总路线 M0–M4](product-direction.md)为准，下方 0.30–0.32 仅为原技术标签；兼容、授权与迁移边界继续有效。

## 2026-11-09 修订决议（优先于下方初稿）

当前排期以[本体方案](core-plan-2026-11.md)为准：0.29.x 收口、0.30 身份、0.31 surface、0.32 按需动作。线上 APK 已核对为 0.29.2-debug/27；下方 0.27/22 不再是当前状态。

迁移对象是当前 manifest 选中的 runtime 代；新格式必须分别声明 history/turns/events/业务文件版本。下文 `chat-agent migrate --to 2 --dry-run` 只是旧草案的伪命令，**不是已有可执行命令**。新入口沿现有停机迁移设计扩展，完成实现后再提供真实命令。

撤回普通加载时自动补写 ID、旁路 `.v1.bak` 自动接管等方案。预检失败不切 manifest；正式迁移写新代并校验，切换失败通过既有维护机制续作或恢复备份。旧 ID 已失效不能回退 index+hash；v1 仍按旧来源协议独立校验。

备份必须包含 `/var/lib/chat-agent` 和 `/etc/chat-agent`；回退按当次实际备份结构操作，不能拿示例覆盖生产。七种旧组件复用已有回归，只有对应解析/渲染改动才增加相关用例，不要求每次手测完整组件矩阵。

## 初稿（涉及上述决策的条目已被修订）

状态：草案。与 `rfc-message-identity.md`、`rfc-generative-ui-v2.md` 配套。
目标：引入 v2 时，**现有七种组件与已发布交互零回归**。

## 1. 不可破坏的清单

以下行为在今天已经工作，v2 落地过程中必须保持：

| # | 现有行为 | 位置 |
| --- | --- | --- |
| 1 | ```` ```chat-ui ```` 代码块解析出 7 种组件 | `interactive.Extract` |
| 2 | 声明必须有 `schema_version:1 / id / type / props` | `protocol.go` |
| 3 | 4 块/消息、8 KiB/块、32 KiB/总量 | 同上 |
| 4 | `message_index + sha256` 来源校验 | `interactive.go` |
| 5 | `uiContextFloor`（life / activity / summary 边界） | `interactive.go` |
| 6 | 来源必须是 completed 批次的最后一条 assistant 消息 | 同上 |
| 7 | 提交后只读：不授权记忆/角色/活动/任务写入 | `chat_ui_response` 策略 |
| 8 | 本地滑块零模型请求 | `web/ui/charts.js` |

## 2. 协议层：共存而非替换

```
一条 assistant 消息
├── ```chat-ui  … v1 声明（一次性、只读/答题）
└── 运行日志中的 surface 事件（v2，可更新）
```

- 解析器先跑 v1 提取，行为完全不变。
- v2 事件**不写在消息正文里**，而是写入会话日志的独立记录类型。
  这样旧客户端看不到 v2 事件，不会渲染出未知卡片，也不会报错。
- 旧客户端读到 `schema_version:2` 的组件时：**忽略并在正文里已有说明文本**，
  这是 v1 策略里「Add a brief standalone explanation outside the block」的原因，
  保留该要求即可优雅降级。

## 3. 存储层：读取宽容，写入严格

| 方向 | 规则 |
| --- | --- |
| 读旧文件 | 缺 `id`、缺 `surface` 字段一律接受 |
| 读新文件 | 旧二进制**会拒绝**未知记录类型 → 必须显式迁移，不允许混跑 |
| 写 | 一律输出新格式；不提供「降级写回」开关 |

**明确不做**：让旧二进制读新文件。`session.go` 的 `DisallowUnknownFields` 是刻意设计，
绕过它等于放弃格式校验。正确做法是迁移 + 备份 + 一次性切换，回退靠备份而不是靠旧二进制。

### 3.1 迁移步骤

1. 停止服务，完整备份 `var/lib/chat-agent`。
2. 运行 `chat-agent migrate --to 2 --dry-run`，输出：会话数、将补 ID 的消息数、
   将被标记 tombstone 的引用数、无法归类的记录数。
3. 人工核对 dry-run 报告；异常即中止。
4. 正式迁移：写 `*.v2.jsonl` 旁路文件，校验通过后原子替换，保留 `.v1.bak`。
5. 启动新版本，确认会话列表、历史、交互都能打开。

### 3.2 回退

只能整体回退：停服务 → 恢复备份 → 启动旧版本。
**不允许**「只把二进制换成旧的、数据留新的」。

## 4. 来源校验：两套并行

```go
// v1：完全保留，逻辑不变
if response.Source.MessageIndex 用 index + sha256 + floor 校验

// v2：新增路径
if response.Source.MessageID != "" 用 id + turn_id + revision 校验
```

判定顺序：有 `message_id` 走 v2，否则走 v1。不做「v2 失败后回退 v1」——
降级校验会掩盖真实的过期状态。

## 5. 前端

`web/ui/` 现有模块（`render.js` / `protocol.js` / `charts.js` / `quiz.js` /
`structures.js` / `vectors.js` / `state.js`）保持职责不变，新增：

- `surface.js`：接收宿主下发的 surface 投影，按 `surface_id` 找到已渲染容器，
  原地替换内容；找不到就插入到对应消息之后。
- 状态仍以前端 Map 做缓存，但**可重建**：刷新时由服务端历史重建，不依赖内存。
- 不做框架迁移，继续原生 JS。

### 5.1 渲染降级

| 情况 | 行为 |
| --- | --- |
| 未知 `type` | 不渲染，保留正文 |
| 内容超限 | 拒绝整个 surface，显示「界面过大无法显示」 |
| `revision` 落后 | 只读展示，按钮禁用 |
| `lifecycle: closed` | 只读展示 |

## 6. 回归清单（每次改动 v2 都要跑）

- [ ] 7 种 v1 组件都能解析、渲染、答题
- [ ] 4/8KiB/32KiB 上限仍然生效
- [ ] 非 ASCII ID、重复 ID 仍被拒绝
- [ ] 过期来源（压缩后、activity reset 后）仍被拒绝
- [ ] 来源不是最后一条 assistant 消息时仍被拒绝
- [ ] 本地滑块不产生模型请求
- [ ] `chat_ui_response` 不能写入记忆/角色/活动
- [ ] 旧 `.jsonl`（无 ID）能正常打开
- [ ] 会话删除、单轮 purge 后，界面不残留、不误判

## 7. 版本号与发布纪律

用户检查的 GitHub 状态为 `0.26.0 / versionCode 21`。
当前工作区已发布 `0.27.0-debug / versionCode 22`（UI 重构 + 新图标）。

因此建议：

| 版本 | 内容 | 数据格式 |
| --- | --- | --- |
| 0.27.x | UI 外观收口、稳定性 | 不变 |
| **0.28** | Message Identity（日志 v2 迁移） | **变**，需备份 |
| 0.29 | Generative UI v2（surface） | 变，叠加在 v2 之上 |
| 0.30 | UI Actions（业务交互） | 不变（新增 operation 记录） |

一次发布只改一类风险：**不要在同一版里既改视觉又改数据格式**。
