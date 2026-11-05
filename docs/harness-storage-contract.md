# Harness 3A：持久运行与删除契约

2026-11-05，契约版本 `harness-storage/1`。这是阶段 3A 的设计交付；0.19 的现有 transcript/turns 兼容 v1 并按需写多模态 v2；它们与本文规划的 runtime 事件 v2 布局不同。尚未实施本文的事件写入器、迁移器或恢复器。3B/3C 实现前以本文为基线，发生变更须同时更新契约和迁移说明。

设计借鉴 DeepSeek Harness（MIT，固定参考 `8bd073392eef6f54e921a093b95076248eb08cbb`）对 turn/step、请求快照和持久事件的区分，Go 侧独立实现；许可见 [THIRD_PARTY_NOTICES](../THIRD_PARTY_NOTICES.md)。本契约不引入 Cordis、数据库依赖或新的模型调用。

## 1. 现状与本次明确的决定

当前 `probe.RunMessages` 先保存真实用户输入；工具执行完毕后才保存完整 assistant/tool batch。业务写入成功后会话保存仍可能失败。`turns.Progress` 约一秒保存进度、重启将 running 改为 interrupted，但台账文本不是业务成功凭据。`Session` 严格读取 transcript v1/v2；0.19 的 v2 只增加图片引用及工具思考协议，末尾缺换行仍拒绝继续追加。`storage.Replace` 同目录替换且同步文件，尚不承诺目录断电持久性。

因此 v2 采用**每会话一份权威事件日志**；完整聊天历史与 turns 是其投影，业务结果仍以业务存储中的回执为准。请求正文放在同一代目录中的有界快照文件，不另建可独立复活会话的全局聊天日志。不能在 v1 JSONL 里直接加入事件字段，也不能让 v1/v2 同时成为写入权威。

## 2. 标识与作用域

| 字段 | 生成者与含义 |
| --- | --- |
| `session_id` | 宿主创建、不可重用的会话 ID；现有 API 路径仍使用它，角色绑定不可改 |
| `request_id` | 客户端提交 ID；同会话同 ID、同内容摘要返回原接收结果，不同内容返回 409 |
| `turn_id` | 宿主生成的逻辑运行 ID；合并的 1–4 个 request 同属一 turn，不能用序号或消息下标作身份 |
| `attempt_id` | 每次明确开始执行生成新的 128 位随机 ID；手动再次尝试是新 attempt，保留旧 attempt 的状态 |
| `step_id` | attempt 下的宿主递增步骤 ID；一次主模型请求对应一步，不因工具个数增加模型步骤 |
| `call_id` | 宿主根据 attempt/step/调用位置生成；模型提供的 ID 另存 `provider_call_id`，只用于协议关联 |
| `operation_id` | 写工具的稳定业务操作 ID，按版本化的 role/session/attempt/step/call 规范元组 SHA-256 计算；宿主注入，模型不可指定 |
| `event_seq` / `event_id` | 当前物理代内从 1 递增 / 宿主随机 ID；事件不能重复或跳号，不靠时间戳排序；重写代重新连续编号，保留存活 event ID 和逻辑关联 |
| `generation` | 同一 session 的物理存储代号；重写/迁移生成新代，不改变逻辑消息 ID |

所有 ID 先通过现有路径组件检查；角色、会话、操作名和参数摘要都参与回执核对。operation ID 相同但规范参数摘要不同必须冲突，不能返回旧结果。模型生成的相同 call ID、工具名或相同输出文本不能证明是同一次业务操作。

## 3. 权威存储及边界

以下路径是待实现 v2 布局，`<data>` 为现有 DataDir；旧 `<data>/pocket/<session>.jsonl` 与 `<data>/turns/<session>.json` 在未迁移状态由现有 transcript/ledger v1/v2 使用，不能因版本数字相同而当成本节事件格式。

```text
<data>/runtime/<session>/manifest.json
<data>/runtime/<session>/g-<generation>/events.jsonl
<data>/runtime/<session>/g-<generation>/snapshots/<snapshot-id>.json
<data>/runtime/<session>/g-<generation>/history.json       # 可重建投影
<data>/runtime/<session>/g-<generation>/turns.json         # 可重建投影
<data>/runtime/format-index.json                         # 非正文 v2 会话登记
<data>/runtime/deletions.json                            # 非正文删除登记
```

manifest 的严格字段为 `version:2, contract:"harness-storage/1", session_id, role_id, generation, imported_from`；generation 只接受本地生成的路径组件，不能接受任意相对路径。读取器仅从显式 manifest 选择当前代，**不扫描“最新文件”、不回退旧代、损坏也不尝试旧 v1**。未发布的候选代永不自动选用。首次发布 v2 前，在会话目录外写入非正文的已迁移 session ID 登记；已登记但缺失/损坏 manifest 的会话只进入维护状态，不能因 manifest 丢失而重新选择尚待清理的 v1。

| 数据 | 权威来源与职责 |
| --- | --- |
| 接收输入、合并队列、执行生命周期 | v2 events；`request.accepted` 必须包含原始输入和摘要，不能只保留 UI 台账 |
| 完整历史、摘要、上下文 floor | 已提交的历史事件；history.json 只是缓存，不从进度文本拼历史 |
| 模型请求 | 已绑定的不可变 snapshot；只说明“请求是什么”，不说明服务商执行成功 |
| 业务写入 | memory/tasks/activities/life/companion/roles 各自状态及其同事务回执；运行日志不能反过来覆盖业务结果 |
| turns / 网页实时事件 / 推送 | 队列与展示投影；通知已发送不等于用户已读，更不等于工具提交成功 |
| 长期记忆、人设及活动 | 保留既有角色作用域和审核/授权；会话删除不会自动删除独立角色数据 |

同一会话仍为一个写入者；事件追加前编码并检查长度，写入完整换行记录并 Sync 后才能宣布持久成功。出现短写/Sync 失败立即封闭该 writer，不能继续追加；修复由 3C 显式处理。快照文件先写入、同步、原子替换，再追加引用它的事件；没有引用的孤儿快照可清理，不影响运行历史。

## 4. 事件格式与状态

事件 envelope 必含 `version:2, event_id, event_seq, session_id, role_id, kind, at, payload`。`at` 为 UTC RFC3339Nano；按事件种类严格检查 request/turn/attempt/step/call/operation 关联字段和 payload，拒绝未知版本、未知类型和越界引用。模型原文始终是数据，不能充当 envelope 或控制字段。

```json
{"version":2,"event_id":"e_01","event_seq":8,"session_id":"s_01","role_id":"companion","kind":"tool.started","at":"2026-11-05T01:00:00Z","turn_id":"t_01","attempt_id":"a_01","step_id":"step_1","call_id":"c_01","operation_id":"sha256-hex","payload":{"name":"activity_control","arguments":{"action":"pause","activity_id":"activity_01","revision":2},"arguments_sha256":"sha256-hex","provider_call_id":"provider_01","receipt_capability":"atomic"}}
```

示例 ID 为便于阅读的占位值；实际摘要是 64 位十六进制，时间及 ID 必须通过读取器校验。schema 按 kind 定义：

| kind | 必需 payload / 语义 |
| --- | --- |
| `request.accepted` | request ID、原文、规范内容摘要、排队顺序；落盘后才返回接收成功 |
| `request.withdrawn` | 尚未开始的 request ID；软撤回保留原接收记录，不能称为硬删除 |
| `turn.started` | request IDs、有序用户消息 ID、开始时 history 边界；固定合并批次 |
| `attempt.started` | 显式触发来源、原 attempt（如有）、宿主预算；不能把后台任务冒充用户消息 |
| `model.requested` | step、snapshot ID 与 SHA-256、路由别名；先落盘再发网络请求，表示“可能已发送”，不保证服务商收到 |
| `model.finished` | complete/error/canceled/unknown、完成消息或有界 partial、finish reason；只有完整协议响应才可调度工具 |
| `tool.started` | 宿主 call ID、provider call ID、名称、规范参数及摘要、operation ID、回执能力；通过准入后、进入处理器前写入 |
| `tool.finished` | 规范结果、错误码、结算状态、回执引用及结果摘要、耗时；错误文本不能覆盖已提交成功回执 |
| `history.committed` | 完整用户输入、完整 assistant/tool batch 或最终回答的稳定 message IDs；每项指明 turn/attempt/step，不把部分流写成最终回答 |
| `summary.committed` | 摘要文本、覆盖的稳定 message IDs 及范围版本；不再只依赖可能漂移的整数下标 |
| `context.reset` | 重置点及新上下文 floor；是保留历史的软重置，不等于删除 |
| `attempt.finished` | completed/canceled/failed/interrupted/unknown、停止原因及已知工具结算；completed 不代表所有工具成功 |
| `queue.paused` / `queue.resumed` | 明确暂停/恢复原因；重启不隐式恢复付费执行 |

每个 attempt 最多一个终态；已持久化的终态不可改写成另一个终态，通过新代修复时保留事实差异说明。主流程取消不会回滚已提交工具，进程消失后的未终结 attempt 投影为 interrupted；`tool.started` 无可信结算时为 unknown，而不是失败或成功。

### 完整历史与 attempt

用户输入只提交一次。完整收到的 assistant/tool batch 可以进入后续历史，即使之后最终回答失败；它记录真实发生的调用，但所有 tool 内容仍是不可信参考。部分 assistant 文本仅用于 attempt 展示，不进入后续模型上下文。完整最终回答必须先提交 `history.committed`，再结束 attempt；UI 不以 SSE done 或 turns 的 completed 单独判断历史已保存。

合并消息的一次回答覆盖整个 turn。最新轮硬删必须覆盖该 turn 的全部 request IDs 与对应输入/回答，不能只移除其中一个 request 的队列行却留下同批回答。3C 需向兼容 API 返回实际删除的 request IDs；中间 turn 继续返回 409。排队但未执行的单条消息仍可使用既有软撤回。

## 5. 请求快照、来源与预算

snapshot 必须保存 `ModelRequest` 的实际 model/system/messages/tools/MaxOutputTokens/TokenParameter，以及执行时的历史边界、角色文件版本摘要、宿主参考来源及本步预算。只保存 hash 无法重建请求，不能标为完整快照。保存路由别名/不带凭据的目的地主机，不保存 APIKey、Authorization/Cookie、设备私钥、challenge 响应或带密钥查询参数；不直接序列化整个 Config/HTTP 请求对象。

动态 `RuntimeSnapshot`、摘要、记忆和工具结果即使作为 user role 传输也保存独立来源标签。它们永远不能用来通过 `turnHasEvidence`；写入依据只来自本 turn 的真实、已接收用户输入。快照按聊天敏感数据保护，用户在原文输入的私人内容不会因命名为“快照”变成匿名数据；管理导出需明确选择，默认不暴露给模型、推送或诊断日志。

待实现的首版硬预算：

| 范围 | 上限与满额行为 |
| --- | --- |
| 原始合并输入 / attempt 展示文本 | 各 64 KiB，沿用当前边界；展示允许标注截断，真实输入不可静默截断 |
| 单快照 | 512 KiB 编码后 JSON；超过时在发模型请求前拒绝，不省略 schema/消息后声称可重建 |
| 单事件行 | 1 MiB（含换行）；工具结果沿用现有 32 KiB 文本、256 KiB 结构化值与显式 truncated 标记 |
| 单 attempt | 4 MiB 持久预算，预留 64 KiB 结算空间；耗尽时停止新增模型/工具调用，仍能记录停止与回执引用 |
| 单会话 / 全 runtime | 32 MiB / 256 MiB 当前代与快照；候选代写入前另检查所需临时空间，满额拒绝新执行，已有内容继续可读和可删 |
| 接收队列 | 保持现有最多 8 条 queued、2048 条 request 及最近 32 条详细台账的产品边界 |

预算必须在派发前预留，磁盘仍可能外部耗尽；写入成功但落结算失败仍按回执/unknown 规则处理，不能声称全局事务。主模型之外的摘要/关怀/话题请求采用相同 snapshot/attempt 原则时需各有宿主来源；本次不把已有 topic budget 回执冒充全 Harness 回执。

### 图片依赖（0.19 后补充）

当前图片已独立保存在会话绑定的 images store，DataURL 只在请求内存中。未来请求快照用不可变图片引用、MIME、尺寸和 SHA-256 关联原始规范化二进制，不复制 base64；重建必须同时拥有快照及哈希匹配的完整图片，只有 ID/hash 不算可重建。快照 512 KiB 预算不含共享图片字节，图片另受媒体配额约束。依赖图片被删除后明确 snapshot_unavailable，不伪造完整快照。

删除和迁移须覆盖这些图片依赖。整会话清理所有本会话原图及上传草稿；删最新合并轮只清仅由该轮使用的原图，保留其他有效消息仍引用的图片。清理请求快照不得顺便删除仍被聊天使用的图片。旧 transcript v2 的图片引用/工具 reasoning 与台账选择参数须显式导入，不能按旧 v1 规则静默丢掉。raw reasoning 继续只作协议状态，不进入用户展示或授权来源。

## 6. 业务回执与不确定结果

写工具按 `receipt_capability` 显式声明 `atomic` 或 `none`，不能因为工具可写就假定原子去重。atomic 的业务 store 在同一锁、同一次持久替换中同时写业务修改和回执：`role_id, operation_id, arguments_sha256, operation_kind, committed_at, entity_ids, resulting_revisions, outcome_digest`。回执不保存原聊天、证据原句、完整参数或模型提示；必要的当前业务内容仍属于各自 store。再次查询同 operation 且摘要一致返回原结果引用；实体已被后续修改/删除时说明原提交事实，不重新创建或宣称当前值相同。

活动/任务已有创建幂等 key、角色 proposal 的部分恢复逻辑不能直接升级为全工具回执；3B 必须逐 store 修改持久格式并公布支持清单。角色文件与候选文件是多文件写入，未实现明确提交协议前标记 none。memory merge、删除、revision 更新也须单独接回执，不能只覆盖 create。

| 重启时证据 | 恢复结果 |
| --- | --- |
| 已有原子 committed 回执，运行日志没有结果 | 显示业务已提交，补建只读结果投影，不调用原工具/模型；会话保存失败单独说明 |
| 持久事件确认准入拒绝或未派发 | not_started / denied，不称业务失败回滚 |
| 有 tool.started，找不到回执或工具为 none | unknown；没有回执不证明没有执行，禁止自动重跑 |
| 模型 requested 后无完整结果 | interrupted / unknown，不自动重放付费请求，不执行半截 tool call |

回执首版每角色最多 8192 个，**不自动按日期清除后再次接受旧 operation ID**；满额暂停新的受保护写入，保留查询/删除已有数据能力，后续按明确维护方案归档或压缩。会话删除只清理运行副本，不撤销已保存角色业务；保留最小回执可避免旧请求重新执行。回执摘要/ID 是持久元数据，界面与导出说明须明确其保留边界。不承诺跨业务文件 exactly-once，也不以重试掩盖 unknown。

## 7. 删除、保留与恢复防复活

生成、迁移和删除共享同一 session 门闩；生成中仍返回 409。删除分为可恢复的清理流程，避免跨文件失败后把旧内容重新当权威：

1. 写入非正文删除登记并同步，包含随机 delete ID、session ID、删除范围（整会话，或 turn/request/message/attempt IDs）、登记序号与时间。运行/导出/迁移读取器都先应用登记；登记失败时不宣布删除成功。
2. 最新 turn：重写到新候选代，物理去掉其请求、attempt、工具参数/结果、请求快照和台账；**清除所有存活模型快照**，因为后续快照可能复制待删消息。使包含被删内容的摘要和历史 floor 失效，后续重算不调用模型进行删除。校验新代后切 manifest。
3. 整会话：撤销可读 manifest，关闭 writer，物理移除该 session 的全部当前/旧/候选代与兼容 v1/turns 文件，以及含其正文的迁移临时文件。只读缓存同时清理；同名会话 ID 不复用。
4. 清理所有在线旧代后才返回 200；清理中断/失败返回 503 与删除任务 ID，保留登记使读取不可见，后续只完成清理，不恢复内容。不能仅追加 tombstone 就报告“硬删完成”。

最新轮删除也要检查保留事件中是否存在其正文的派生复制；不能只按 envelope 的 turn ID 筛选。清除快照会使旧 attempt 标为 `snapshot_unavailable: deleted_dependency`，不再承诺请求可重建。没有删除依赖的普通保留清理可在终态满 7 天后删除快照、部分流与详细运行参数/结果，只保留有界状态/引用；完整聊天、request 去重身份及获准业务内容不随诊断 TTL 删除。计划内清理通过新代完成，不在活动 journal 中打洞。

角色长期记忆、活动、事件、任务、偏好和人设候选保持独立范围；它们可能含用户主动保存的来源文字，删会话不声称全部擦除。新增运行副本不得躲在业务回执里。网页/手机已收到的通知、用户导出和部署备份不能由服务端会话删除追溯撤销。

`deletions.json` 只保留必要 ID，不存正文/hash 内容，不自动过期；最多 100000 项，满额时先拒绝新数据创建并要求维护归档，不能静默抛弃登记。format-index 的已迁移/已删除会话 ID 同样最多 100000 项且不自动过期。恢复旧备份必须同时携带**当前删除登记与格式登记**，先在隔离副本应用登记、清理全部对应代/快照/旧 v1，再校验发布；若当前登记丢失或无法确认完整，禁止自动合并/上线旧会话，要求明确的离线恢复处置。防复活不等于物理销毁历史备份。

## 8. 现有 transcript/ledger → runtime 事件 v2 的迁移及回退

3C 的显式维护命令在停止服务、持有独占存储锁后执行；不在启动时偷偷转换。

1. 先留应用数据与配置一致性备份、记录当前删除登记；检查剩余空间与 v1 全量可读性。源文件损坏/末行撕裂不自动丢弃，输出非正文错误定位并中止该会话。
2. 将 v1 角色绑定、reset、summary、消息和 turns 纳入候选代，分配稳定 message IDs。旧工具结果标 `legacy_unknown`，不伪造 snapshot、operation ID 或 committed 回执。只有可靠匹配的完整台账范围才绑定 turn；无法匹配的旧历史保留为 legacy batch，不能伪造可删除 request 边界。
3. 验证消息顺序、assistant/tool 配对、summary 覆盖、角色绑定、请求摘要及删除登记。原 queued 保留但暂停，running 转 interrupted，不发网络或执行工具。
4. 同步候选文件及目录，写入已迁移登记并原子发布 manifest；旧 v1 在线源移入仅迁移器可见的候选清理范围并删除，读取器无自动回退路径。登记前失败保留原可读 v1；登记后、manifest 发布前失败保持维护态，由显式命令完成发布或核验后撤销登记，不能自动回退。发布后失败按 manifest 继续完成清理。目录同步在不支持的平台须明示较弱持久性并拒绝声称断电无损。
5. 切换后不再允许旧二进制运行该数据根。回退只允许：停止服务，保留 v2 现场，显式导出可表示的完整历史/队列到独立 v1 根，应用当前删除登记并校验，再使用旧程序。unknown、attempt 和快照无法塞回 v1，保留隔离档案并明确不会被旧程序显示；不得覆盖或丢弃新业务状态以制造兼容。

迁移/清理过程的 manifest、临时目录和未完成步骤是维护事实，不可含聊天正文。备份回退会丢失哪些新提交、已发生的业务写入如何保留，都必须在实际发布记录中说明；本契约不授权立即执行生产迁移。

## 9. 后续实施与最小验收

3B 先加入事件/snapshot 写入和逐业务原子回执，使用临时数据验证；3C 再接入现有 API、旧数据转换与重启恢复。保留现有对外接口及角色/授权边界，包重命名另行处理。只保留三个关键故障流程：

1. 业务提交后运行结果落盘失败，重启查询原回执；none/无回执显示 unknown，计数证实没有重执行。
2. 模型流取消/进程中断，partial 不进历史，已提交工具结果可见；没有新的付费请求。
3. 旧数据迁移与最新合并轮/整会话硬删，覆盖派生快照、旧代、summary、台账和旧备份恢复；注入切换失败仍有明确可读权威，不复活删除正文。

这些是 3B/3C 的完成标准，本次未运行这些测试，也未据此宣称恢复功能已完成。
