# DSH Harness 迁移规划

更新：2026-11-07。阶段 1–2 已随 0.16.0 部署，三项陪伴机制已交付至 0.17。阶段 3A 契约和容量已冻结；0.23/3B 的 `RunRecorder`、实际请求快照、attempt/step/tool 事件与 task create 原子回执已在 0.24 接入生产 API。3C 已实现停机显式迁移、manifest 选代、重启恢复、硬删除/防复活和兼容导出，并通过三条最小故障流。整体方向见[项目现状与架构路线](architecture-roadmap-2026-11.md)。

## 当前结论

| 工作 | 实现状态 | 检查与交付状态 |
| --- | --- | --- |
| 阶段 1：提示词与工具统一 | 已实现 | 已包含在服务端构建和相关回归中，随 0.16.0 部署 |
| 阶段 2：执行流水线与逐步上下文 | 已实现 | 原六项相关回归通过；本批补做机制与只读部署检查后随 0.16.0 发布，未做真实模型验收 |
| 三个已报告的回归 | 已修复 | 测试夹具改为读取新上下文协议，原业务结果断言保留 |
| 会话/最新一轮删除 | 工作区已有实现 | 已随 0.16.0 发布；补做删除集成与保留摘要后重开检查 |
| 阶段 3A：事件格式与删除契约 | 设计及 0.23 容量已冻结 | [harness-storage/1](harness-storage-contract.md) 明确权威日志、标识、回执、清理和迁移；事件/快照/payload 上限已与当前正文和思考对齐 |
| 阶段 3B：持久结算 | 已实现并接线 | 原生 Go recorder、operation ID、task create 原子回执；隔离证据见 [0.23 实施记录](release-0.23.md)，API 接线见 [0.24 发布](release-0.24.md) |
| 阶段 3C：恢复迁移 | 已实现 | 旧格式显式转换、API 规范状态、删除登记/新代重写、停机修复和兼容导出通过最小故障注入 |
| 受限 host followup | 0.26 已实现 | 复用 Prompt/PrepareStep/Provider/RunRecorder，已有会话内一次响应且不暴露工具；可信触发不持久化为用户消息 |

Harness 3B/3C 的生产基线仍由 0.24 建立；最新服务端为 **0.26.0**（`20261108-followup-04`），Android 为 **0.26.0-debug / versionCode 21**（`android-20261108-01`），见[0.26 发布说明](release-0.26.md)。0.26 不另建 Agent Loop：主动触发只增加受限 followup 入口，runtime v2、turns v3 和普通聊天工具循环保持不变。

实现细节见 [阶段 1–2 实施说明](harness-phases-1-2.md)。总体产品方向见 [功能规划](feature-plan.md)。

## 迁移目标与边界

将 DSH 的提示词组织、工具注册与执行契约、逐步上下文装配及恢复设计，用 Go 独立实现。继续服务现有智能陪伴：普通交流直接回应，需要查询或保存时由 LLM 自主选工具，不增加显式聊天模式、关键词意图门控或每轮分类模型。

遵守 AGENTS.md。可以克隆 DSH，按适用许可直接复用、修改或移植源码，也可采用适合的包或独立组件。实施先找已有实现，记录来源、固定版本与分发义务。CA 的设备白名单、加密连接、角色隔离、审核制记忆和写入授权适用于所有接入能力。

迁移的是选定机制的行为契约，不承诺 DSH 插件 API 兼容或完整功能覆盖。

## 已固定的上游依据

参考版本为 `8bd073392eef6f54e921a093b95076248eb08cbb`（2026-11-03），不随 master 自动更新。本次重读该固定版本的架构和会话投影文档，未切换上游基线。

| 固定版本材料 | 本项目采用的内容 |
| --- | --- |
| [架构](https://github.com/deepseek-ai/deepseek-harness/blob/8bd073392eef6f54e921a093b95076248eb08cbb/docs/architecture.md) | 组件职责、turn/step、持久事件与实时事件的区分 |
| [System Prompt](https://github.com/deepseek-ai/deepseek-harness/blob/8bd073392eef6f54e921a093b95076248eb08cbb/packages/core/system-prompt/README.md) | 有序分区、作用域、工具与提示装配统一 |
| [Prompt 源码](https://github.com/deepseek-ai/deepseek-harness/blob/8bd073392eef6f54e921a093b95076248eb08cbb/packages/core/system-prompt/src/index.ts) | 同名注册规则、显式覆盖与确定性顺序 |
| [Tools](https://github.com/deepseek-ai/deepseek-harness/blob/8bd073392eef6f54e921a093b95076248eb08cbb/packages/core/tools/README.md) | 每步准入、守卫、规范结果和取消 |
| [工具契约源码](https://github.com/deepseek-ai/deepseek-harness/blob/8bd073392eef6f54e921a093b95076248eb08cbb/packages/core/tools/src/contracts.ts) | 模型 schema 与宿主执行元数据分离 |
| [Agent Loop](https://github.com/deepseek-ai/deepseek-harness/blob/8bd073392eef6f54e921a093b95076248eb08cbb/packages/core/agent-loop/README.md) | 输入队列、逐步驱动、请求快照与生命周期 |
| [动态上下文源码](https://github.com/deepseek-ai/deepseek-harness/blob/8bd073392eef6f54e921a093b95076248eb08cbb/packages/core/agent-loop/src/runtime-context.ts) | 带来源的上下文快照、变化和清空 |
| [会话投影](https://github.com/deepseek-ai/deepseek-harness/blob/8bd073392eef6f54e921a093b95076248eb08cbb/docs/subsystems/conversation.md) | 稳定节点身份、持久/实时事件装配和增量视图；作为后续网页工作台的设计依据 |

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
- 默认串行；保留 4 次模型响应、4 次工具执行，最终步骤不声明工具。普通聊天默认 30 秒，开启思考后采用配置值与 120 秒的较大值；0.21.2 默认省略聊天输出 token 上限，网络/文件与后台生成预算另行保留。

边界：参数校验仅支持现有 schema 子集，不是完整 JSON Schema；业务处理器继续负责授权语义、版本、角色及并发约束。工具成功提交后不会因停止而撤销；0.24 恢复使用持久回执/unknown 状态，仍不承诺跨业务文件的全局事务。

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

0.22 发布本身不意味着阶段 3 完成；0.23 随后完成了 3B 隔离实现，0.24 完成 3C 并坚持只由停机维护命令转换线上数据，服务启动不会自动迁移。

0.19 多模态扩展增加的 transcript/turns v2 与 0.21.2 turns v3 已由 0.24 显式验证并原样迁入 runtime 规范状态，图片仍是会话作用域的独立对象；模型参数、工具协议 reasoning、最终回答思考与未完成进度不按 v1 静默降级。见[多模态格式](chat-0.19.md)及[0.21.2](release-0.21.2.md)。

## 阶段 3：3A/3B/3C 已交付

实施顺序如下，各项以前一项确定的数据契约为基础。3B 先在显式隔离目录完成结算能力，3C 再以停机命令迁移并切换；服务启动发现未迁移旧会话、维护态或未完成删除时直接拒绝，不偷偷修复或回退。

| 顺序 | 交付内容 | 完成标准 |
| --- | --- | --- |
| 3A：事件格式与删除契约（已交付） | [harness-storage/1](harness-storage-contract.md)：稳定标识、事件 envelope、权威日志、请求快照和业务回执；容量与大正文采用有界文件引用 | 已与当前请求投影、4 MiB 回答和 1 MiB 展示思考对齐；删除/迁移规则保留给 3C 执行 |
| 3B：持久结算与业务回执（隔离完成） | `internal/runlog` 写请求 snapshot、模型/工具 payload、attempt/step/tool 事件和停止原因；业务 operation ID 在处理器前持久化 | task create 回执与任务同次替换；结算失败保留回执，none/无回执为 unknown；不会自动重跑模型或工具 |
| 3C：恢复、迁移与发布（已交付） | transcript v1/v2、turns v1/v2/v3 到 manifest runtime 代的转换、重启投影、删除登记/新代重写、显式修复及 transcript-v2/turns-v3 兼容导出 | 三条最小流程通过；不重放付费请求或不确定工具，不把未完成回答当完整历史，不因恢复或迁移复活已删除内容 |

0.24 的最终边界：manifest 选中的单一 runtime 代是权威；其中 history/turns 是规范状态，events 保存运行与结算事实。业务成功只认同一业务提交中的回执。模型 call ID 不作为跨重启业务身份；无回执的已派发工具保持 unknown，不自动重试。删除先登记非正文范围，再发布清除目标正文、全部旧代和模型快照的新代；完整清理后才返回成功。仅完整 assistant/tool batch 或最终回答进入历史，部分流只属于 attempt。

### 数据与恢复约束

- 会话运行记录描述模型请求与工具调用；业务存储及其原子回执决定写入是否成功；turns 保留队列和 UI 进度职责，不从显示文本推断业务成功。
- 可重建请求需要保存实际提示、参考、schema、模型路由及消息范围；只有 hash 不足以重建。不记录密钥、Bearer token 或私钥；有界快照按聊天敏感数据管理。
- 展示过但未完整完成的回答保存为 attempt，不自动进入后续模型历史；实时增量不逐 token 同步落盘。
- 不承诺跨业务文件的全局事务或所有工具 exactly-once。可查询的已提交回执用于避免重做；无法判断的结果保留 unknown，先查询或核对。
- 在线读取器只接受 format-index + manifest 选中的 runtime 代；停机迁移器才接受 transcript v1/v2 与 turns v1/v2/v3。回退使用 `-export-runtime <新目录>` 生成独立 transcript-v2/turns-v3 会话副本，runtime events/unknown 仍保留在源档案，不能只替换程序或强降 v1。
- 最终回答的展示 reasoning 与工具协议 reasoning 分开投影，思考正文不成为授权、记忆或摘要事实。旧 64 KiB 展示、4 MiB attempt 草案不兼容现有合法输出，3B 不得直接照搬；请求快照还须覆盖 thinking/effort 和“省略输出上限”的实际语义。

### 0.20 互动来源的迁移要求

组件定义仍是原始 assistant Content，答案仍是普通 user 消息内的保留 envelope，不新增独立 UI 日志。3B 请求快照及 3C 转换必须保留这些原始内容、模型/思考参数与只读答题工具范围。

- 未完成的 attempt 可以提供只读预览，不能因重启投影而升级为 completed 的可提交组件。
- 原始 SHA-256、消息绝对索引及来源回合必须保持可校验；若新投影不能保留原映射，则明确将旧交互标为过期，不能重写用户已提交的 envelope 来绑定新题。
- 摘要/遗忘/活动 floor、源轮硬删除及整会话删除继续使旧组件失效；恢复与迁移不得重新赋予动作资格。
- 答案的 request ID 去重不代表其全部工具跨业务文件 exactly-once。只读反馈工具范围由宿主显式保留，不从界面文案或历史 JSON 推断权限。

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
3. transcript v1/v2、turns v1/v2/v3 迁移及最新轮/整会话删除后重开、恢复：无索引错位、思考丢失、旧摘要或旧代际复活；失败不破坏原有可读数据。

不新增完整矩阵、独立审查任务或默认收费验收。只有实现变更、新失败或未解决问题需要时才扩大检查。

## 配套陪伴机制规划

用户提供的 speak-up、soul-md、bili-whale 调研已另列为[陪伴机制借鉴计划](companion-mechanism-plan.md)：前两项已随 0.16.0 发布；用户随后要求继续第三项，内容作息/漏斗已随 0.17 部署，见[机制说明](topics-0.17.md)与[发布记录](release-0.17.md)。这些是基于现有 Harness 的业务功能，不并入阶段 3 的存储恢复范围，也未安装 DSH 插件。

0.26 已将关怀/话题接入宿主控制的来源、已有目标会话和预算：复用同一 Prompt/PrepareStep/Provider/RunRecorder，只允许一次响应且不声明工具；概率、安静、未读和写入授权约束不变。GenUI 的稳定 message/surface 身份可继续消费同一套投影，不能另建第二套权威聊天日志。

## 暂缓范围

Cordis、动态插件/市场/HMR、PTC/run_code、Shell、计算机控制、自动子代理/Agent Teams、通用目标无限续跑继续暂缓。MCP 通用接入、第三方 IM、关闭应用后的可靠触达、实时语音是独立产品方向，不捆绑迁移。provider 专有缓存优化只在有实测依据后适配；当前不承诺 ECNU 缓存或费用收益。

Harness 1–3C 与受限 followup 已交付；后续继续依靠实际陪伴使用反馈调整表达，并在同一稳定投影上推进 GenUI v2，不把架构迁移本身等同于“像真人”。
