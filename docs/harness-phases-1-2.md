# Harness 阶段 1–2 实施说明

发布更新：阶段 1–2 已于 2026-11-04 随 [0.16.0](release-0.16.md) 部署。下文“未部署”为此前单独实施阶段的记录；阶段 3 仍未实施。

更新：2026-11-04。阶段 1–2 已在本地实现，保留 Go 标准库，无新增运行依赖。三个已报告的回归已修复，六项直接相关的本地模拟回归通过；Windows 服务端重新构建通过，产物 `bin/chat-agent-harness.exe`。未部署、未更新 Android 包、未进行真实模型或线上验收。下一步范围以[当前迁移规划](dsh-harness-migration-plan.md)为准。

初次交付按当时要求仅构建、未运行测试；收到失败报告后补做上述最小回归。以下记录对应已经完成的修复。

报告中的 `TestRoleMemoryLifecycle`、`TestActivityChatControl` 仍读取旧的平面参考消息，未读取新协议 `saved_reference.saved_context`：前者漏看已经发给模型的记忆，后者把未读取到的活动 revision 当作 0，调用被正常拒绝。活动报错中的 `active=false` 是会话生成状态，不表示活动状态被改为暂停。`TestStreamingToolLoop` 仍要求最终收尾提示出现在系统消息中，而新架构将预算放在动态上下文。

本次用 `RuntimeSnapshot` 和 `RuntimeContextPrefix` 明确现有协议，更新测试夹具读取方式，保留原业务结果断言；活动流程另断言下一步看到最新修订号、动态快照不写入聊天。生产删除实现和业务执行语义未修改。通过的六项为：`TestRoleMemoryLifecycle`、`TestActivityChatControl`、`TestStreamingToolLoop`、`TestRecallContextLifecycle`、`TestKnowledgeManagementLifecycle`、`TestRepeatedToolBatchFinalizes`。不代表完整测试矩阵、删除功能验收或线上验收。

## 工具与提示词

`internal/probe/registry.go` 的 Tool 定义同时拥有名称、描述、参数 schema、Usage、Order、可用性条件、可选参数校验器、守卫、协作式超时与执行函数。现有全部业务工具的使用指引已迁回其定义，时钟也走同一注册流程；原独立 tool_guidance 表已移除。

每一步 Resolve 一份私有 ToolSet，模型 schema、工具指引和执行查找共享同一个准入集合。仅名称、描述、参数三项进入模型请求。工具仍默认串行，不按用户话语关键词决定可用性。支持可用性回调用于真实配置、能力和资源状态；现有角色工具主要在构造时按能力配置注册。

提示词按 order、name 确定性排序；同名 Add 导致 Render 报错，显式 Override 才能替换已存在分区。运行时身份、角色、陪伴规则、工具规则和指引保持独立职责；日期、保存状态、修订号和剩余预算进入动态参考。

## 执行流水线

`internal/probe/executor.go` 执行：

```text
本步准入 -> 参数 schema 校验 -> 可选业务校验器
         -> 全局守卫 -> 工具守卫 -> 协作式执行 -> 规范化结果
```

任何守卫拒绝后直接返回，不进入执行，也没有后续重新放行步骤。角色和会话作用域由可信 StepContext 提供，现有业务处理器内的版本、原文来源、审核状态、会话绑定和并发检查继续执行；原文匹配仍不等于证明语义授权。

参数校验仅支持当前工具用到的有限 JSON Schema 字段：基本类型、对象属性、required、additionalProperties、enum、数值上下界、字符串长度和数组长度/items。未知 schema 字段在注册时拒绝，不是完整 JSON Schema 实现。调用前拒绝重复键、过大参数和过深嵌套；具体时间、字节长度、跨字段条件仍由业务处理器校验。

ToolResult 保留旧 Output/Error，同时可携带 JSON Value、错误码、调用 ID、耗时和截断标记。JSON 文本输出转为结构化值，纯文本转为 JSON 字符串。模型只收到有界文本或明确错误，不收到执行器元数据。输出超过 32 KiB 显式标记截断，不把已经完成的操作改称失败；宿主结构化值超过 256 KiB 则不保留。

未知工具、参数错误、守卫拒绝和普通工具失败回注模型；取消和总预算仍会终止运行。执行器不自动重试，不另起无法回收的后台工具 goroutine。超时依赖处理器协作取消；成功提交的写入不因同时到达的停止信号变成“已撤销”。批次执行中停止后，会结算剩余未执行调用并尽力保存完整批次。

## 每步状态与模型适配

`internal/api/step_context.go` 按角色读取当前偏好、活动绑定及状态、关怀设置、时间和有效历史边界。相关参考的缓存键包含已审核事实、可见事件、活动及版本；数据变化或删除后重建参考。数据未变化时复用本轮召回，不因为预算或时间变化重复 rerank；跨轮仍正常召回。

动态快照放在保留的旧历史之后、本轮消息之前，保持实际用户消息或工具结果处于末尾，并避免拆开 assistant/tool 批次。快照带内部 `Source=runtime-context`；该字段不发送给模型，也不写入 v1 聊天格式，传输文本明确声明来源和参考性质。输入/日志入口拒绝把带来源标记的消息当成真实用户消息持久化。写入 evidence 始终来自 API 接收的真实本轮输入，不从参考内容抽取。

新增 ModelProvider/ModelRequest 接口，现有 Chat Completions SSE 保留完整结束后才能执行工具的规则。主聊天模型及摘要 utility model 的选择保持原逻辑。摘要和主动问候仍禁止工具，不自动切换模型。memory 的可取消检索接口把停止信号继续传给 semantic rerank；原无 context 接口保留兼容。

## 仍有的边界

- 默认预算保持 4 次模型响应、4 次工具调用和 30 秒；工具额度用尽或最终步骤不声明工具。
- 自定义角色文档仍在新一轮构造时加载，不在工具批次中途替换人格。
- v1 JSONL、turns 队列、现有 API、鉴权和客户端协议保留。运行时仍位于 internal/probe，包名迁移留待阶段 3。
- 动态参考是请求级投影，尚无 DSH 式完整持久事件与请求重建能力。
- 工具成功后、会话提交前的进程崩溃窗口仍存在。跨存储幂等结果、unknown 状态恢复和版本迁移属于阶段 3，不声称已解决。
- 未增加 Cordis、动态插件、PTC/run_code、Shell、自动子代理或后台主动触达。

来源固定到 DSH commit `8bd073392eef6f54e921a093b95076248eb08cbb`，见 [来源与许可](../THIRD_PARTY_NOTICES.md)。计划见 [迁移计划](dsh-harness-migration-plan.md)。
