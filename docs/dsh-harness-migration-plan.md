# DSH Harness 迁移规划

更新：2026-11-05。阶段 1–2 已随 0.16.0 部署，三项陪伴机制已交付至 0.17。阶段 3A 的存储与删除契约已形成设计基线，见[完整契约](harness-storage-contract.md)；3B/3C 运行时、迁移和恢复尚未实施，现有持久格式未改变。

## 当前结论

| 工作 | 实现状态 | 检查与交付状态 |
| --- | --- | --- |
| 阶段 1：提示词与工具统一 | 已实现 | 已包含在服务端构建和相关回归中，随 0.16.0 部署 |
| 阶段 2：执行流水线与逐步上下文 | 已实现 | 原六项相关回归通过；本批补做机制与只读部署检查后随 0.16.0 发布，未做真实模型验收 |
| 三个已报告的回归 | 已修复 | 测试夹具改为读取新上下文协议，原业务结果断言保留 |
| 会话/最新一轮删除 | 工作区已有实现 | 已随 0.16.0 发布；补做删除集成与保留摘要后重开检查 |
| 阶段 3A：事件格式与删除契约 | 设计已交付 | [harness-storage/1](harness-storage-contract.md) 明确权威日志、标识、回执、清理和迁移；未写入生产 v2 数据 |
| 阶段 3B/3C：持久结算与恢复迁移 | 未实施 | 以后续代码和故障流程结果验收，不能以 3A 文档代替恢复实现 |

当前服务端 **0.17.0**（`20261104-topics-02`），Android **0.17.0-debug / versionCode 14**（`android-20261104-02`），见[发布说明](release-0.17.md)。Windows 产物为 `bin/chat-agent-0.17.0.exe`。原 0.16/0.17 分阶段版本号建议未分别发布，阶段 3 的实际版本待确定。

实现细节见 [阶段 1–2 实施说明](harness-phases-1-2.md)。总体产品方向见 [功能规划](feature-plan.md)。

## 迁移目标与边界

将 DSH 的提示词组织、工具注册与执行契约、逐步上下文装配及恢复设计，用 Go 独立实现。继续服务现有智能陪伴：普通交流直接回应，需要查询或保存时由 LLM 自主选工具，不增加显式聊天模式、关键词意图门控或每轮分类模型。

遵守 AGENTS.md：允许复用、改写 DSH 的提示词与设计，保留来源和 MIT 许可；不嵌入其 TypeScript 运行时、Cordis 或插件实现。保留 Go 标准库、现有 Android/网页、设备白名单、加密连接、角色隔离、审核制记忆和写入授权边界。

迁移的是选定机制的行为契约，不承诺 DSH 插件 API 兼容或完整功能覆盖。

## 已固定的上游依据

参考版本为 `8bd073392eef6f54e921a093b95076248eb08cbb`（2026-11-03），不随 master 自动更新。本次规划更新未重新追踪上游。

| 固定版本材料 | 本项目采用的内容 |
| --- | --- |
| [架构](https://github.com/deepseek-ai/deepseek-harness/blob/8bd073392eef6f54e921a093b95076248eb08cbb/docs/architecture.md) | 组件职责、turn/step、持久事件与实时事件的区分 |
| [System Prompt](https://github.com/deepseek-ai/deepseek-harness/blob/8bd073392eef6f54e921a093b95076248eb08cbb/packages/core/system-prompt/README.md) | 有序分区、作用域、工具与提示装配统一 |
| [Prompt 源码](https://github.com/deepseek-ai/deepseek-harness/blob/8bd073392eef6f54e921a093b95076248eb08cbb/packages/core/system-prompt/src/index.ts) | 同名注册规则、显式覆盖与确定性顺序 |
| [Tools](https://github.com/deepseek-ai/deepseek-harness/blob/8bd073392eef6f54e921a093b95076248eb08cbb/packages/core/tools/README.md) | 每步准入、守卫、规范结果和取消 |
| [工具契约源码](https://github.com/deepseek-ai/deepseek-harness/blob/8bd073392eef6f54e921a093b95076248eb08cbb/packages/core/tools/src/contracts.ts) | 模型 schema 与宿主执行元数据分离 |
| [Agent Loop](https://github.com/deepseek-ai/deepseek-harness/blob/8bd073392eef6f54e921a093b95076248eb08cbb/packages/core/agent-loop/README.md) | 输入队列、逐步驱动、请求快照与生命周期 |
| [动态上下文源码](https://github.com/deepseek-ai/deepseek-harness/blob/8bd073392eef6f54e921a093b95076248eb08cbb/packages/core/agent-loop/src/runtime-context.ts) | 带来源的上下文快照、变化和清空 |

复用清单及许可见 [THIRD_PARTY_NOTICES.md](../THIRD_PARTY_NOTICES.md) 和 [MIT 全文](../licenses/DeepSeek-Harness-MIT.txt)。新增借鉴内容继续标注到具体文件，不自动拉入上游新依赖。

## 阶段 1：提示词与工具统一——已实现

- `internal/probe/prompt.go`：命名分区；按 order、name 排序；同名 Add 使 Render 报错，显式 Override 才能替换。
- `internal/probe/registry.go`：统一 Tool 定义、Usage、Order、schema、宿主元数据及执行器；每步得到一份私有 ToolSet。
- schema、每工具使用指引与执行查找使用相同工具集合；传给模型的字段仅为名称、描述和参数。
- 现有业务工具的 Usage 回到各自定义，时钟使用同一注册流程；原 `internal/api/tool_guidance.go` 已移除。
- 运行时身份、角色、陪伴规则和工具规则构成稳定指令；时间、修订号、参考和预算进入动态上下文。

边界：当前是编译期工具组合与显式分区覆盖，不是 Cordis 的动态插件树或完整多层配置系统。角色文档在新一轮加载，不在工具执行中途更换人格。

## 阶段 2：执行流水线与逐步上下文——已实现

```text
现有 API / 消息队列
  -> 真实本轮输入与角色作用域
  -> 每步装配：稳定提示 + 动态参考 + ToolSet
  -> ModelProvider：保留现有 Chat Completions SSE
  -> ToolExecutor：参数校验 -> 守卫 -> 协作式执行 -> 结果规范化
  -> 下一步或最终回复
```

- `internal/probe/executor.go`：严格参数检查、守卫、错误码、协作式超时和有界结果；普通工具失败回注模型，不自动重试。
- `internal/api/step_context.go`：刷新偏好、活动、关怀设置、时间和历史边界；保存数据未变化时复用本轮召回。
- `RuntimeSnapshot` / `RuntimeContextPrefix` 明确传输协议，业务参考位于 `saved_reference.saved_context`。内部 Source 标记不进 v1 日志；动态参考不能成为真实用户输入、写入依据或聊天气泡。
- `ModelProvider` / `ModelRequest` 分离模型传输；摘要 utility model 和主聊天路由保持原逻辑，无自动模型切换。
- memory 的可取消检索把停止信号传至 semantic rerank；保留原接口兼容。
- 默认串行；预算保持 4 次模型响应、4 次工具执行、30 秒，最终步骤不声明工具。

边界：参数校验仅支持现有 schema 子集，不是完整 JSON Schema；业务处理器继续负责授权语义、版本、角色及并发约束。工具成功提交后不会因停止而撤销。进程崩溃后的持久恢复尚未补齐。

## 已完成的回归与实际证据

| 测试 | 已检查的行为 |
| --- | --- |
| `TestRoleMemoryLifecycle` | 候选审核、跨会话已审核记忆、角色隔离、纠正与删除 |
| `TestActivityChatControl` | 实际活动写入与去重、版本/授权边界、暂停/恢复；下一步获得最新 revision，动态参考不进入聊天历史 |
| `TestStreamingToolLoop` | 完整工具调用、结果关联、未知工具拒绝、预算耗尽后的无工具收尾 |
| `TestRecallContextLifecycle` | 有预算的相关参考、纠正/遗忘、只读活动召回 |
| `TestKnowledgeManagementLifecycle` | 知识修改、合并、遗忘及上下文边界 |
| `TestRepeatedToolBatchFinalizes` | 重复工具批次不重放，进入最终回复 |

三个失败的根因是模拟模型/断言仍读取旧的参考格式。记忆夹具漏看新快照，活动夹具得到 revision=0 后被正常拒绝，probe 夹具仍在 system 消息查找动态预算提示。活动报错中的 `active=false` 是会话是否仍在生成，与活动的 active/paused 状态不是同一字段。

以上六项使用临时目录和本地模拟模型，已通过；服务端重新构建通过。没有据此宣称完整矩阵、真实模型聊天质量、删除功能或线上运行已经验收。

## 已完成：阶段 1–2 联合发布

阶段 1–2 已与现有删除功能、概率关怀及人设候选审核合并发布为 0.16.0。已正常停机备份，保留上一版目录，完成服务健康、新入口和 Android 更新下载哈希的只读检查；未重复收费模型验收。新关怀状态文件写入 v2，回退必须同时处理该文件；会话 JSONL 未迁移。

该发布不意味着阶段 3 已完成。3A 已基于当前删除、合并轮及业务存储确定契约；下一步为 3B → 3C，不能提前自动转换线上数据。

## 阶段 3：3A 契约已交付，3B/3C 待实施

实施顺序如下，各项以前一项确定的数据契约为基础，不要求拆成三个发布版本。

| 顺序 | 交付内容 | 完成标准 |
| --- | --- | --- |
| 3A：事件格式与删除契约（设计已交付） | [harness-storage/1](harness-storage-contract.md)：turn/attempt/step/call/operation 标识、严格事件 envelope、单会话权威日志、请求快照和业务回执 | 完成文档基线：具体路径、字节与保留预算、合并轮硬删、派生副本清理、显式 v1/v2 切换及回退；当前未实施该存储 |
| 3B：持久结算与业务回执 | 请求快照、attempt、工具开始/结果、停止原因；业务 operation ID 与结果一起提交 | 已提交的写入可查询回执；会话提交失败不把业务成功改成失败；结果不确定标为 unknown，不自动重跑 |
| 3C：恢复、迁移与发布 | v1 到 v2 转换、重启投影、删除后的恢复约束、迁移失败和回退 | 不重放付费请求或不确定工具，不把未完成回答当完整历史，不因恢复或迁移复活已删除内容 |

3A 的关键决定：v2 事件日志统一接收队列与运行事实，完整聊天和 turns 是投影；业务成功只认同一业务提交中的回执。模型 call ID 不作为跨重启业务身份。无回执的已派发工具保持 unknown，不自动重试。删除须清除派生请求快照与所有在线旧代，先登记非正文删除范围、完成物理清理后才能报告硬删成功。仅当前已收到完整响应的 assistant/tool batch 或最终回答进入历史，部分流只属于 attempt。详见契约中的 schema、预算和迁移步骤。

### 数据与恢复约束

- 会话运行记录描述模型请求与工具调用；业务存储及其原子回执决定写入是否成功；turns 保留队列和 UI 进度职责，不从显示文本推断业务成功。
- 可重建请求需要保存实际提示、参考、schema、模型路由及消息范围；只有 hash 不足以重建。不记录密钥、Bearer token 或私钥；有界快照按聊天敏感数据管理。
- 展示过但未完整完成的回答保存为 attempt，不自动进入后续模型历史；实时增量不逐 token 同步落盘。
- 不承诺跨业务文件的全局事务或所有工具 exactly-once。可查询的已提交回执用于避免重做；无法判断的结果保留 unknown，先查询或核对。
- 现有读取器严格接受 v1。v2 迁移必须显式转换、校验、原子发布，不能让旧二进制误写新格式；回退要说明新数据如何保留，不能只替换程序。

### 与现有删除功能的衔接

现有 `DELETE /api/sessions/{id}` 删除会话，`DELETE /api/sessions/{id}/turns/{request}?purge=1` 只允许硬删最新轮。阶段 3 必须保持这些语义：

- 最新轮删除要覆盖相关 request/attempt/运行快照，并处理摘要、历史边界和 turns 索引；既有角色长期记忆、活动不因删聊天而自动删除。
- 整会话删除覆盖该会话的所有在线日志代际、请求快照和运行记录。不能只删当前文件而留下可被读取器重新选中的旧代际。
- 迁移保留源文件用于回退时，删除必须同步处置这些在线源副本。不能用“追加删除标记”冒充用户要求的硬删除。
- 防止备份/迁移恢复时重新引入已删除在线记录；若保留最小删除登记以防复活，只留必要的非正文标识，并明确其保留范围。
- 部署备份、已导出文件和角色存储仍是独立数据范围；聊天删除不宣传为所有副本物理擦除。新增运行快照会增加副本，必须在存储设计阶段纳入清理。

`internal/probe` 到 `internal/harness` 的包名整理列为恢复逻辑稳定后的维护项，不与存储迁移强制绑定，也不长期保留两套运行时。

## 阶段 3 的最小检查

优先扩展现有流程，用临时目录和故障注入覆盖：

1. 业务写入已成功、会话结果提交失败后重启：读到原回执，不再写一次；没有回执时显示 unknown。
2. 流被取消或进程中断：部分文本保留为 attempt，不进入完整历史，收费模型请求不自动重放。
3. v1/v2 迁移及最新轮/整会话删除后重开、恢复：无索引错位、旧摘要或旧代际复活；失败不破坏原有可读数据。

不新增完整矩阵、独立审查任务或默认收费验收。只有实现变更、新失败或未解决问题需要时才扩大检查。

## 配套陪伴机制规划

用户提供的 speak-up、soul-md、bili-whale 调研已另列为[陪伴机制借鉴计划](companion-mechanism-plan.md)：前两项已随 0.16.0 发布；用户随后要求继续第三项，内容作息/漏斗已随 0.17 部署，见[机制说明](topics-0.17.md)与[发布记录](release-0.17.md)。这些是基于现有 Harness 的业务功能，不并入阶段 3 的存储恢复范围，也未安装 DSH 插件。

## 暂缓范围

Cordis、动态插件/市场/HMR、PTC/run_code、Shell、计算机控制、自动子代理/Agent Teams、通用目标无限续跑继续暂缓。MCP 通用接入、第三方 IM、关闭应用后的可靠触达、实时语音是独立产品方向，不捆绑迁移。provider 专有缓存优化只在有实测依据后适配；当前不承诺 ECNU 缓存或费用收益。

后续迁移顺序：以已交付 3A 契约为基线，按 3B → 3C 推进；继续依靠实际陪伴使用反馈调整表达，不把架构迁移本身等同于“像真人”。
