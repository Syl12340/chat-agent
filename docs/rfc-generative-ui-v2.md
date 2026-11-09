# RFC：Generative UI v2 —— 持久界面（Surface）

> 排期更新：本 RFC 纳入[总路线 M3 成果工作台](product-direction.md)。下方 0.31 为原技术排期；客户端设计系统和主要页面在 M0/M1 先行，不等待 surface。

## 2026-11-09 修订决议（优先于下方初稿）

状态：待实现设计；阶段 C，暂定 **0.31**，依赖消息身份阶段。以[本体方案第 5 节](core-plan-2026-11.md#5-c持久界面设计)为当前存储与权限契约。

- 已有 runtime v2，撤回下文“version 保持 1、新增 surface kind”与“v1→v2 前置”的写法。history codec 必须明确升版，相关 manifest/迁移/导出/删除一起支持。
- surface 事件与最终 assistant 消息放在同一可校验 history 提交中，成功同步后才可见；events 是诊断记录，不作为另一份 surface 真相。半写记录按损坏记录停止恢复，不显示半个 surface。
- 以持久图表作为首个闭环；共同活动时间线随后接业务状态投影，避免同时创建第二份进度权威。
- 宿主生成 surface_id/revision/source；模型可引用当前会话内的 surface 并提交 expected_revision，但不能自行推进 revision。来源使用现有 request/batch 标识，区分 creator 与 last_writer。
- 创建来源被删则 surface 失效；更新来源被删则冻结，不能回放旧状态导致 revision 回退或按钮复活。摘要/遗忘/活动重置 floor 仍有约束。
- 首版只有本地操作和只读对话提交；0.26 后台 followup 不产生 surface；业务按钮属于阶段 D。

## 初稿（涉及上述决策的条目已被修订）

状态：草案，待评审。目标版本：0.29。
前置：`rfc-message-identity.md`（v2 依赖稳定消息标识）。
范围：只做设计；不改现有 v1 契约，不引入前端框架，不重构 Harness。

## 1. v1 已经有什么

`internal/api/interactive.go` 定义了 chat-ui v1 的完整契约，实际能力不弱：

| 组件 | 用途 |
| --- | --- |
| `quiz.single_choice` | 单选 |
| `quiz.fill_blank` | 填空 |
| `chart.line` | 折线（含本地滑块的 sine 模式） |
| `chart.bar` | 柱状 |
| `diagram.vector` | 矢量图 |
| `diagram.flow` | 流程/关系图 |
| `table.data` | 数据表 |

限制已经很严：4 块/消息、8 KiB/块、32 KiB/总量、ASCII ID、无 HTML/SVG/脚本/URL/自定义动作。

**来源校验同样严格**（`validateUIResponse`）：

1. `message_index` 不得早于 life/activity/summary 构成的 `uiContextFloor`
2. 来源必须是 `assistant` 且无 `tool_calls`
3. `sha256(content)` 必须匹配
4. 该消息必须是某个 `completed` 批次的**最后一条**
5. 组件 ID 必须在消息里真实存在

这套校验解决的是「这条回答还能不能被信任」，**不是**「这个界面还能不能被更新」。
v2 要补的正是后者。

## 2. 核心抽象：Surface

```text
Surface
├── surface_id   host 生成，会话内唯一，不可复用
├── revision     单调递增整数，从 1 开始
├── source       绑定的消息 / 轮次 / 角色 / 会话
├── lifecycle    active | closed
└── content      受限组件树（复用 v1 的七种类型 + 有限布局容器）
```

### 2.1 字段职责

| 字段 | 谁生成 | 作用 | 滥用时的后果 |
| --- | --- | --- | --- |
| `surface_id` | 宿主 | 同一会话内持续引用同一个界面 | 模型若能指定，就能覆盖别人的界面 |
| `revision` | 宿主 | 拒绝过期操作 | 客户端能自增就能绕过顺序检查 |
| `source` | 宿主 | 绑定到真实消息与轮次 | 模型能写就能伪造「用户授权」 |
| `lifecycle` | 宿主 | 关闭后不再接受更新 | 已结束的卡片还能改 |

**四个字段全部由宿主赋值，模型只能提出内容。** 这是 v2 与 v1 权限模型一致的地方：
模型生成一个界面不等于用户授权，界面上显示「完成」也不等于业务写入成功。

### 2.2 与 v1 的关系

- v1 的 ```` ```chat-ui ```` 代码块**继续存在**，含义不变：一次性、只读/答题、绑定单条消息。
- v2 不是「更多组件」，而是**同一批组件的可更新投影**。
- 一条消息里可以同时有 v1 声明和 v2 的 `surface.create`，互不冲突。

## 3. 存储：复用运行时历史，不新建数据库

用户路线里明确说「不要为了 v2 另造一套数据库和事件总线」。设计如下：

### 3.1 事件写入现有会话日志

在 v1 JSONL 记录之外，允许一种新记录（`version` 保持 1，新增 kind）：

```json
{"version":1,"surface":{"id":"surf_01J..","revision":1,"action":"create",
 "source":{"message_id":"msg_01J..","turn_id":"turn_01J..","role_id":"companion"},
 "content":{...}}}
```

- 旧二进制读到未知 kind 会拒绝——这是**格式演进**，需要显式版本迁移，不能悄悄上线
  （与 `harness-storage-contract.md` 的既有约束一致）。
- 因此 v2 的前提是「会话日志 v1 → v2 迁移」，属于 Message Identity 阶段的一部分。

### 3.2 重建

- 启动或打开会话时，按日志顺序回放 `create/update/close`，得到当前 surface 投影。
- 投影只是内存结构，**不是第二份真相**；真相永远是日志。
- 没有性能问题前不引入独立状态存储。

### 3.3 更新语义

- 更新提交**完整受限内容**，不支持 JSON Patch。理由：Patch 需要模型理解路径语义，
  且在并发更新下容易产生中间态；完整替换天然幂等。
- `revision` 必须严格等于 `current + 1`，否则拒绝并返回当前 revision。

## 4. 三类交互必须严格分层

这是 v2 最关键的权限设计，和用户路线里的判断一致：

| 类型 | 例子 | 执行路径 | 模型调用 | 授权 |
| --- | --- | --- | --- | --- |
| **本地交互** | 展开、缩放、滑块、选项草稿 | 纯浏览器 | 0 | 无 |
| **对话交互** | 提交答案、要求继续解释 | 绑定来源，走现有 SubmitTurn | 1 轮 | 来源校验 |
| **业务交互** | 保存进度、创建任务、修改活动 | 宿主校验 + 业务回执 | 0~1 | 显式授权 |

### 4.1 硬性规则

1. 模型生成按钮 ≠ 用户授权。
2. UI 显示成功 ≠ 业务写入成功；业务交互必须有回执，且回执来自业务存储而非模型。
3. 旧 `revision` 的按钮不能提交。
4. 重复点击不能产生第二个业务对象（需要 operation id）。
5. 结果不确定时不允许自动重试，只能查询核对。

### 4.2 首版范围

**v2 首版只做「本地交互 + 只读展示 + 对话交互」。** 业务交互按需逐个接入。

理由是现有业务回执覆盖不均：`todo_add` / `reminder_add` 已经有原子回执，
但活动、记忆、角色的部分写操作还没有同等级别的幂等保证。在补齐前开放通用业务按钮，
等于把「模型生成的界面」当成「用户意愿」，风险不可接受。

## 5. 第一批界面（不新增组件类型）

### A. 共同活动时间线（首选 MVP）

- 数据源：`internal/companion` 的活动状态，**不另建第二份进度记录**。
- surface 生命周期：创建活动 → `create`；记录进展 → `update`（revision+1）；
  暂停/恢复 → `update`；结束 → `close`。
- 验证点：跨天回来仍能恢复、结束后按钮消失、旧 revision 拒绝。

### B. 持久图表

- 复用 `chart.line` / `chart.bar`，改为 `update` 换数据而不是新发一张图。
- 适用：学习进度、趋势、计划完成度。

### C. 决策与对比卡片

- 用 `table.data` + `diagram.flow` 组合，验证「有限布局容器」。
- 布局容器只允许：纵向堆叠、两列网格、可折叠分组。**不做自由定位。**

### 明确排除

- 波形/频谱：当前语音链路只到「Android 系统识别返回文本」，没有音频采集、保存与 DSP。
  不能让模型编造波形数据冒充真实音频分析。
- 任意 HTML/SVG/脚本：延续 v1 禁令。

## 6. 协议草案

```json
{
  "schema_version": 2,
  "surface_id": "surf_01J8Z...",
  "revision": 3,
  "source": {"message_id": "msg_01J8Y...", "turn_id": "turn_01J8X..."},
  "lifecycle": "active",
  "content": {
    "layout": {"type": "stack", "gap": "normal"},
    "children": [
      {"id": "progress", "type": "chart.line", "props": {"title": "背单词", "series": []}},
      {"id": "note", "type": "table.data", "props": {}}
    ]
  }
}
```

约束延续 v1：类型白名单、ID 唯一、尺寸上限、无 URL/脚本/自定义动作。
新增限制：`children` 最多 8 个；布局嵌套最多 2 层；单 surface 内容 ≤24 KiB。

## 7. 待决问题

1. **surface 与消息的关系**：一个 surface 能否被多条消息引用（例如后续消息补充说明）？
   倾向：可以引用，但 `source` 只记创建它的那条，更新记 `last_writer`。
2. **未完成轮次里的 surface**：流式途中不落盘，只有批次提交后才生效——沿用 v1 的
   「最终 assistant 消息」语义。
3. **跨会话**：surface 不跨会话。角色级「看板」是另一个产品问题，暂不设计。
4. **删除会话时** surface 随日志消失，无需单独清理。
