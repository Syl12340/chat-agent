# Harness 3A：持久运行与删除契约

更新：2026-11-07，设计基线 `harness-storage/1`。0.24 已实现 3C：停机显式迁移 transcript v1/v2 与 turns v1/v2/v3、manifest 选代、API 接线、删除登记、新代重写及兼容导出；0.23 的事件 writer、snapshot/payload 和 task create 回执同时接入聊天运行。生产切换记录见 [0.24 发布](release-0.24.md)，总体依据见[项目现状与架构路线](architecture-roadmap-2026-11.md)。

设计借鉴 DeepSeek Harness（MIT，固定参考 `8bd073392eef6f54e921a093b95076248eb08cbb`）对 turn/step、请求快照和持久事件的区分，Go 侧独立实现；许可见 [THIRD_PARTY_NOTICES](../THIRD_PARTY_NOTICES.md)。本契约不引入 Cordis、数据库依赖或新的模型调用。

## 1. 现状与本次明确的决定

当前 `probe.RunMessages` 先保存真实用户输入；工具执行完毕后才保存完整 assistant/tool batch。业务写入成功后会话保存仍可能失败。`turns.Progress` 约一秒保存进度、重启将 running 改为 interrupted，但台账文本不是业务成功凭据。`Session` 严格读取 transcript v1/v2；0.21.2 在 v2 中保存完整回答的 reasoning，turns v3 另保存思考进度；末尾缺换行仍拒绝继续追加。`storage.Replace` 同目录替换且同步文件，尚不承诺目录断电持久性。

0.24 将权威边界收敛为**manifest 明确选中的单个 runtime 代**。该代中的 `history.json` 与 `turns.json` 是聊天/队列的规范持久状态，`events.jsonl` 是 attempt/model/tool 的追加式运行与结算事实；业务结果仍以业务存储中的回执为准。这里修订了 3A 的“所有聊天都必须仅由事件重放”草案：当前 Go 会话与队列本来就是各自原子提交，强行双写全量事件会制造另一个不真实的跨文件事务。读取器只认同一 manifest 代，旧 transcript/turns 在切换后移除，因此没有两套在线权威。以后若改为完整 event sourcing，必须另做格式版本，不得把当前规范状态伪称为可从现有事件完整重建。

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

以下路径已由 0.24 接入 `<data>/runtime`。旧 `<data>/pocket/<session>.jsonl` 与 `<data>/turns/<session>.json` 只允许停机迁移器读取；在线服务发现未登记旧会话会拒绝启动，不能因版本数字相同而当成本节事件格式。

```text
<data>/runtime/<session>/manifest.json
<data>/runtime/<session>/g-<generation>/events.jsonl
<data>/runtime/<session>/g-<generation>/snapshots/<snapshot-id>.json
<data>/runtime/<session>/g-<generation>/history.json       # 规范聊天状态
<data>/runtime/<session>/g-<generation>/turns.json         # 规范队列/展示状态
<data>/runtime/format-index.json                         # 非正文 v2 会话登记
<data>/runtime/deletions.json                            # 非正文删除登记
```

manifest 的严格字段为 `version:2, contract:"harness-storage/1", session_id, role_id, generation, imported_from`；generation 只接受本地生成的路径组件，不能接受任意相对路径。读取器仅从显式 manifest 选择当前代，**不扫描“最新文件”、不回退旧代、损坏也不尝试旧 transcript/turns**。未发布的候选代永不自动选用。首次发布 runtime v2 前，在会话目录外写入非正文的已迁移 session ID 登记；已登记但缺失/损坏 manifest 的会话只进入维护状态，不能因 manifest 丢失而重新选择尚待清理的旧格式源文件。

| 数据 | 权威来源与职责 |
| --- | --- |
| 接收输入、合并队列 | manifest 选中代的 `turns.json`；request ID、摘要、批次、参数、状态和完整/部分展示进度都在该规范状态中 |
| 完整历史、摘要、上下文 floor | manifest 选中代的 `history.json`；完整消息批次才提交，仍不从 turns 进度拼历史 |
| attempt / model / tool 执行生命周期 | 同代 v2 events 与 snapshot/payload；结算失败 fail-closed，不自动重放付费请求或 unknown 工具 |
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

示例 ID 为便于阅读的占位值；实际摘要是 64 位十六进制，时间及 ID 必须通过读取器校验。0.23/0.24 已落地 `attempt.*`、`model.*`、`tool.*`；下表其余 kind 保留为未来完整 event-sourcing 版本的 schema，不得由 0.24 读取器伪造。当前 request/turn/history/summary/reset 的权威状态由同一 manifest 代内的规范文件承担：

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

用户输入只提交一次。完整收到的 assistant/tool batch 可以进入后续历史，即使之后最终回答失败；它记录真实发生的调用，但所有 tool 内容仍是不可信参考。部分 assistant 文本仅用于 attempt 展示，不进入后续模型上下文。0.24 以 `history.json` 的同步成功作为完整历史提交点，再结束 attempt；UI 不以 SSE done 或 turns 的 completed 单独判断历史已保存。未来若启用 `history.committed`，其顺序必须保持相同。

合并消息的一次回答覆盖整个 turn。最新轮硬删覆盖该 turn 的全部 request IDs 与对应输入/回答，不能只移除其中一个 request 的队列行却留下同批回答。0.24 兼容 API 返回实际删除的 request IDs；中间 turn 继续返回 409。排队但未执行的单条消息仍可使用既有软撤回。

## 5. 请求快照、来源与预算

snapshot 必须保存最终准备的 model/system/messages/tools、Thinking/ReasoningEffort、MaxOutputTokens/TokenParameter，以及执行时的历史边界、角色文件版本摘要、宿主参考来源及本步预算。准备边界须与 Provider 的实际消息筛选和字段编码一致；直接序列化尚含展示专用字段的 `ModelRequest` 不足以称为实际请求。输出上限为 0 时记录“参数省略、服务商默认”，不虚构默认数值。只保存 hash 无法重建请求，不能标为完整快照。保存路由别名/不带凭据的目的地主机及适配版本，不保存 APIKey、Authorization/Cookie、设备私钥、challenge 响应或带密钥查询参数；不直接序列化整个 Config/HTTP 请求对象。

动态 `RuntimeSnapshot`、摘要、记忆和工具结果即使作为 user role 传输也保存独立来源标签。它们永远不能用来通过 `turnHasEvidence`；写入依据只来自本 turn 的真实、已接收用户输入。快照按聊天敏感数据保护，用户在原文输入的私人内容不会因命名为“快照”变成匿名数据；管理导出需明确选择，默认不暴露给模型、推送或诊断日志。

0.23 的 3B 隔离实现已冻结以下编码后上限。它们是运行台账的存储边界，不是聊天 token 上限；输出参数为 0 仍表示不向服务商发送 token 参数。容量在外部操作派发前检查，超限直接停止本 attempt，不截断后谎称快照完整。

| 范围 | 0.23 冻结值 | 处理方式 |
| --- | --- | --- |
| 合并用户文字 | 64 KiB | 沿用现有准入；最多 8 条、3 张图片，图片二进制不复制进快照 |
| 单请求 snapshot | 128 MiB 编码后 JSON | 保存完整 model/system/messages/tools 与思考、输出参数；超限时模型请求不派发 |
| 单大 payload | 64 MiB 编码后 JSON | 保存完整/部分模型结果或工具参数/结果；足以覆盖 4 MiB 回答、1 MiB 展示思考及 JSON 最坏转义余量 |
| 单事件行 | 256 KiB（含换行） | 只放身份、状态、小型元数据和 payload 引用；正文不内联 |
| 单会话隔离代 | 1 GiB | 包括 manifest、事件、snapshot 与 payload；满额后 writer 失败关闭，旧数据仍可只读检查 |
| task store v2 | 16 MiB、每角色最多 8192 / 全 store 最多 32768 回执 | 回执和任务状态同一次原子替换；任一边界满额即拒绝新的受保护写入，不按日期复用 operation ID |
| 接收队列 | 8 条 queued、2048 条 request、最近 32 条详细台账 | 保留现有产品语义；详细台账数量限制不等于可以丢弃请求身份与去重记录 |

`internal/runlog` 采用同代内有界文件：先同步并原子替换 snapshot/payload，再向 `events.jsonl` 追加带 ID、相对路径、字节数和 SHA-256 的引用并 Sync。写入短写或 Sync 失败立即封闭 writer；未被事件引用的孤儿文件不构成运行事实。0.24 已在队列 worker 与兼容流式入口创建 recorder，并由 registry 把它固定到 manifest 当前代。磁盘仍可能外部耗尽，业务写入成功但结算事件失败按原子回执或 unknown 处理，不能声称跨文件事务。

### 图片依赖（0.19 后补充）

图片独立保存在会话绑定的 images store，DataURL 只在请求内存中。0.24 请求快照使用不可变图片引用、MIME、尺寸和 SHA-256 关联原始规范化二进制，不复制 base64；重建必须同时拥有快照及哈希匹配的完整图片，只有 ID/hash 不算可重建。快照 JSON 的最终预算不含共享图片字节，图片另受媒体配额约束。依赖图片被删除后不再承诺快照可重建。

删除和迁移须覆盖这些图片依赖。整会话清理所有本会话原图及上传草稿；删最新合并轮只清仅由该轮使用的原图，保留其他有效消息仍引用的图片。清理请求快照不得顺便删除仍被聊天使用的图片。旧 transcript v2 的图片引用/完整 reasoning 与 turns v2/v3 的参数和思考进度须显式导入，不能按旧 v1 规则静默丢掉。

### 思考展示与历史投影（0.21.2 修订）

服务商返回的 reasoning 可在鉴权后实时/历史展示；不再沿用“禁止展示 raw reasoning”的旧文案。最终回答的展示 reasoning 与模型工具协议所需 reasoning 分开投影，前者不因已存储而自动回传模型。部分思考随 attempt 保留并明确未完成，不补造成完整回答，也不作为记忆、摘要事实或授权依据。

迁移时保持文字、截断标志与完成状态，不把 turns v3 的进度冒充 transcript 完整消息。删除、投影失效和容量计算均覆盖正文与思考副本；服务商未返回的内部内容不可推测补齐。

## 6. 业务回执与不确定结果

写工具按 `receipt_capability` 显式声明 `atomic` 或 `none`，不能因为工具可写就假定原子去重。atomic 的业务 store 在同一锁、同一次持久替换中同时写业务修改和回执：`role_id, operation_id, arguments_sha256, operation_kind, committed_at, entity_ids, resulting_revisions, outcome_digest`。回执不保存原聊天、证据原句、完整参数或模型提示；必要的当前业务内容仍属于各自 store。再次查询同 operation 且摘要一致返回原结果引用；实体已被后续修改/删除时说明原提交事实，不重新创建或宣称当前值相同。

0.23 只把 `todo_add` / `reminder_add` 升级为 task store v2 原子回执；活动既有请求记录、角色 proposal 的部分恢复逻辑和任务的普通 HTTP 幂等 key 仍不能直接升级为全工具回执。角色文件与候选文件是多文件写入，未实现明确提交协议前标记 none。memory merge、删除、revision 更新以及任务 update/delete 也须单独接回执，不能用 create 的覆盖范围代替。

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
3. 整会话：撤销可读 manifest，关闭 writer，物理移除该 session 的全部当前/旧/候选代与所有旧格式 transcript/turns 文件，以及含其正文的迁移临时文件。只读缓存同时清理；同名会话 ID 不复用。
4. 清理所有在线旧代后才返回 200；清理中断/失败返回 503 与删除任务 ID，保留登记使读取不可见，后续只完成清理，不恢复内容。不能仅追加 tombstone 就报告“硬删完成”。

最新轮删除也要检查运行记录中是否存在其正文的派生复制；不能只按 envelope 的 request ID 筛选。0.24 采用隐私优先边界：删除重写后的新代不复制旧事件、payload 或任何模型快照，因此旧 attempt 诊断不可再重建，也不会带回被删内容。没有删除依赖的普通保留策略另行实现；完整聊天、request 去重身份及获准业务内容不随诊断 TTL 删除。计划内清理通过新代完成，不在活动 journal 中打洞。

角色长期记忆、活动、事件、任务、偏好和人设候选保持独立范围；它们可能含用户主动保存的来源文字，删会话不声称全部擦除。新增运行副本不得躲在业务回执里。网页/手机已收到的通知、用户导出和部署备份不能由服务端会话删除追溯撤销。

`deletions.json` 只保留必要 ID，不存正文/hash 内容，不自动过期；最多 100000 项，满额时先拒绝新数据创建并要求维护归档，不能静默抛弃登记。format-index 的已迁移/已删除会话 ID 同样最多 100000 项且不自动过期。恢复旧备份必须同时携带**当前删除登记与格式登记**，先在隔离副本应用登记、清理全部对应代/快照/旧格式源文件，再校验发布；若当前登记丢失或无法确认完整，禁止自动合并/上线旧会话，要求明确的离线恢复处置。防复活不等于物理销毁历史备份。

## 8. 现有 transcript/ledger → runtime v2 的迁移及回退

3C 的显式维护命令在停止服务、持有独占存储锁后执行；不在启动时偷偷转换。下文旧格式明确包含 transcript v1/v2 和 turns v1/v2/v3，不能只按最初的 v1 数据写转换器。

1. 先留应用数据与配置一致性备份、记录当前删除登记；检查剩余空间与现有全部格式的可读性。源文件损坏/末行撕裂不自动丢弃，输出非正文错误定位并中止该会话。
2. 将旧格式角色绑定、reset、summary、消息原始记录和 turns 纳入候选代；保留图片引用、参数、完整/部分思考及 UI 原文。迁移不为旧工具结果伪造 snapshot、operation ID、事件或 committed 回执；只有原 turns 已可靠记录的范围继续作为可删除 request 边界。
3. 验证消息顺序、assistant/tool 配对、summary 覆盖、角色绑定、请求摘要及删除登记。原 queued 保留但暂停，running 转 interrupted，不发网络或执行工具。
4. 同步候选文件及目录，写入已迁移登记并原子发布 manifest；全部旧格式在线源移入仅迁移器可见的候选清理范围并删除，读取器无自动回退路径。登记前失败保留原可读数据；登记后、manifest 发布前失败保持维护态，由显式命令完成发布或核验后撤销登记，不能自动回退。发布后失败按 manifest 继续完成清理。目录同步在不支持的平台须明示较弱持久性并拒绝声称断电无损。
5. 切换后不再允许旧二进制运行该数据根。回退只允许：停止服务，保留 runtime v2 现场，显式导出可表示的完整历史/队列到独立旧格式目录，应用当前删除登记并校验，再使用明确兼容的旧程序。包含图片/思考进度时目标程序须支持对应 transcript v2 / turns v3；不支持则拒绝有损回退，不能强写 v1。unknown、attempt 和快照无法表示的部分保留隔离档案并明确不会被旧程序显示；不得覆盖或丢弃新业务状态以制造兼容。

迁移/清理过程的 manifest、临时目录和未完成步骤是维护事实，不可含聊天正文。备份回退会丢失哪些新提交、已发生的业务写入如何保留，都在实际发布记录中说明；生产迁移只在用户明确要求的本次发布中停机执行。

## 9. 实施结果与最小验收

3B 完成第 5 节容量修订、事件/snapshot/payload 写入和 task create 原子回执；3C 已接入现有 API、旧数据转换与重启恢复。保留现有对外接口及角色/授权边界，包重命名另行处理。只保留三个关键故障流程：

1. 业务提交后运行结果落盘失败，重启查询原回执；none/无回执显示 unknown，计数证实没有重执行。
2. 模型流取消/进程中断，partial 不进历史，已提交工具结果可见；没有新的付费请求。
3. 旧数据迁移与最新合并轮/整会话硬删，覆盖派生快照、旧代、summary、台账和旧备份恢复；注入切换失败仍有明确可读权威，不复活删除正文。

前两条 3B 故障流、正常 snapshot/hash 流与第三条 3C 迁移/删除/切换故障流均已通过最小测试。测试不等于断电恢复演练，也不扩大 `atomic` 回执的工具范围。
