# CA 工具、执行和角色记忆研究

基于 `8aadad2` / 0.7.23。保留同一套 Go Harness。电脑本地运行完整 loop，云端由各用户的私有实例运行。实例内信任设备共享账户资源，执行授权和记忆可见范围继续由宿主校验。

**首个实施目标是从对话发起一个能完成、能取消、能查回结果的任务。** 先修正常工具入口、批准后续跑和模型路由，再把同一条运行链用于服务器临时沙盒和本机文件任务。角色记忆可并行补来源、纠正与检索评估。

## 工具调用从准入到结算

| 当前证据 | 优化方向 | 最小验收 |
| --- | --- | --- |
| Harness 已按 step 组装工具，串行校验 schema、guard 和批准，并记录 operation。`workspace_read` 的 handler 支持续页，工具 schema 没有 `offset` | 先修工具注册入口。后续页携带同一文件版本，版本变化返回冲突。以 `ToolResult.Value` 和现有 Receipt 保存业务事实，由宿主从中生成有界的模型文本与工具卡 | 通过真实 ToolExecutor 路径读第二页。跨 UTF-8 边界的页有效，下一页 cursor 与 hash 在模型可见结果中保留 |
| Web 批准成功后提示用户再发消息，模型需要重新产生相同参数；现有票据绑定角色、会话、工具与参数摘要 | 复用现有批准面，把待确认记录绑定到具体 operation、请求主体、受众与授权版本。保存该调用的规范化参数与后续位置。确认后由宿主重查权限并继续同一调用，客户端重连或重复点击查询原 operation | 一次点击继续原调用。改参数或撤销授权会拒绝，重复点击只有一次副作用。断线后显示待确认、已完成或未知 |
| 当前重复保护比较相邻的完整工具批次，相同就复用上次结果并进入收尾 | 写入、发送和提交用操作者、资源、操作 ID 及业务回执判定幂等。突变结果不明时查询原操作。等待远端任务交给显式状态查询或观察流，避免把正常轮询压成最终回答 | 模型以不同批次重提同一执行请求仍只提交一次。状态查询可观察从 running 到终态。无进展时以明确原因收尾 |
| `Output`、`Data`、卡片及 runlog 对相同结果各取不同投影，模型输出按字符边界截断 | 先给文件读取、写入和任务映射清楚的值、模型摘要、卡片字段。大结果保存为受保护产物，仅投影摘要、状态和可读取定位。模型输出只附任务需要的最小数据 | 长结果不丢 hash、状态或后续读取位置，工具卡与 runlog 显示同一终态，私有产物需要同账户授权 |

先保留写入串行。只有多个经过证明无副作用、互无依赖的查询才考虑有界并行。记录每轮工具调用数、执行和模型耗时、被拒原因及重复提交，观测指标使用工具名和类别，正文与参数按现有授权读取。 [执行器](../internal/probe/executor.go) · [loop](../internal/probe/agent.go) · [批准入口](../internal/api/approval.go) · [Web 确认入口](../web/app.js) · [工作区插件](../plugins/builtin/workspace/plugin.go)

## 服务端沙盒与电脑本地 loop

当前有三种位置。云端实例通过 Linux `workspace_shell` 执行，使用 Landlock、seccomp 和有界输出。私有实例的 CPU、内存和进程数已按服务分开，任务仍共享所属实例的限额。电脑本地直接运行 CA loop，Windows 工作区可读写，尚无按产品要求验过的任意 shell 后端。旧远程执行端通过绑定电脑提供文件和固定动作。三种位置沿用现有入口，任务卡统一标明实际运行位置、可用范围和取消状态。 [Linux 执行器](../plugins/builtin/workspace/exec_linux.go) · [非 Linux 行为](../plugins/builtin/workspace/exec_other.go) · [远程执行端](../internal/localagent/executor.go) · [实例发布记录](release-0.7.23.md)

1. **先统一任务可用性。** 运行状态从实际装配得到，区分工作区未授权、仅可读写文件、沙盒不可用与远程设备离线。聊天 UI 和系统工具提示用同一事实。先修 Linux 临时目录的配置传递，调用方设置的 `TMPDIR` 目前没有进入最终 shell 的环境。就绪验收验证实际策略安装，避免用 ABI 查询替代运行能力。
2. **服务器任务使用独立工作目录。** 用户选定的输入按清单放入临时任务目录，操作按已有审批规则限定到该目录。进程组取消和按任务资源控制负责进程、内存、时限及输出，启动时只传预期的文件描述符。采用非 root 的现有进程级方案，以系统实际支持的隔离手段验证策略。超限或取消后记录真实终态，产物按 `Operation/Receipt/Artifact` 回当前会话。持久工作区写入仍用版本校验和明确授权。 [沙盒实现](../internal/sandbox/sandbox_linux.go) · [Landlock 文件边界](https://docs.kernel.org/7.1/userspace-api/landlock.html#filesystem-flags) · [systemd 资源控制](https://www.freedesktop.org/software/systemd/man/latest/systemd.resource-control.html)
3. **把长任务从对话时限解开。** 现有普通 loop 已支持持续调用，默认一轮仍有总时限。短工具同步结算，长任务分为提交、观察和取消，重启后按同一 operation 对账。用户中途补充时，同来源、同受众的输入可在下一安全 step 接纳并显示回执。已有副作用和确认中的参数仍由原 operation 负责，其余输入进入下一轮队列。
4. **本地先做文件任务闭环。** 补独立默认工作区、重复启动和异常退出恢复，再做读改、可查询提交和下载。本机凭据与产物默认留在本机。Windows shell 等后端在文件、进程树、网络和资源边界有实证后注册。轻量个人实例继续共享 Linux 内核，跨信任域执行需要单独选型验证隔离。

验收选两个场景。手机发送 CSV 到临时沙盒，拿回报告，取消后确认任务与子进程已结束。电脑打开本地 CA，读取有版本文件、修改并分享，客户端断线后用同一任务 ID 确认内容和状态。桌面直接启动、后台摘要和主动任务共用之前规划的模型路由快照，避免从 demo 动态配模型后出现不同路径的配置漂移。 [现有工作项](project-assessment-2026-11-23.md)

## 角色、长期记忆和会话上下文

CA 已有 `IDENTITY.md`、`RULES.md`、`PROFILE.md`、按需加载的技能、经用户审核的角色提案和长期记忆候选。召回已按当前角色、审核状态和上下文预算投影事实、事件及活动。这里优先补来源与状态变化，自动召回还是提供参考，角色设定和历史摘要分别保留用途。 [角色装配](../internal/roles/roles.go) · [角色提案插件](../plugins/builtin/roleprofile/plugin.go) · [召回装配](../internal/api/recall_context.go)

| 信息类型 | 建议的更新规则 |
| --- | --- |
| 角色规则、身份、资料 | 规则和身份由显式编辑控制。模型可引用用户明确表达并提议更新 PROFILE，审批卡显示差异与来源，按版本批准，下一轮生效。已有角色提案机制可复用 |
| 用户事实和偏好 | 记录来源消息、创建与更新时间、审核状态和修订。当前 `memory_write` 的“用户明确要求记住”主要依赖提示词，handler 只有会话来源。让候选提交绑定当前用户输入的稳定引用。摘要、工具输出、外部文档只能作为内容参考 |
| 事件、共同活动与短期状态 | 有期限、状态和可见范围的业务记录保留独立身份。设备共享以账户为边界，召回按角色和受众限制。私有会话的记忆是否用于 QQ 私聊由用户授权，群聊使用自己的范围 |
| 会话摘要 | 用于压缩当前会话。更正和遗忘沿用 HistoryFloor、版本刷新与旧摘要重建。旧历史、导出和备份有独立删除与保留语义 |

当前记忆检索按审核状态筛选，将候选交给可选 rerank，由客户端按请求大小截取。先在本地按相关度缩小候选范围，再按显式配置对少量候选使用语义重排。按 step 的缓存签名目前会序列化多份角色状态，可以用集合修订号。把当前问题、来源范围、集合修订以及活动和历史下界纳入缓存，接纳运行中补充时同时刷新查询。对时间变化优先采用有效、已审核的新版本，置信不足就留空或问一句。先测少量中文多会话样例，记录正确引用、过期事实、跨受众误召回、重排次数和耗时，再决定是否需要向量索引。 [记忆候选写入](../plugins/builtin/memory/plugin.go) · [检索](../internal/memory/store.go) · [重排客户端](../internal/semantic/rerank.go) · [step 缓存](../internal/api/step_context.go) · [LongMemEval 论文](https://proceedings.iclr.cc/paper_files/paper/2025/file/d813d324dbf0598bbdc9c8e79740ed01-Paper-Conference.pdf)

## DSH 和 Aurabot 可复用实现

| 固定源码 | 用到 CA 的位置 |
| --- | --- |
| DSH `fcc0ad46` 的 `core/tools/src/contracts.ts`、`presentation.ts` | 规范工具值、模型内容投影和纯 UI 投影。先移植文件和任务两个消费场景，保留现有通用工具卡降级。 [工具契约](https://github.com/deepseek-ai/deepseek-harness/blob/fcc0ad462172522aaf5c93a7b2e01f6b9f0b0159/packages/core/tools/src/contracts.ts) |
| DSH `interaction/user-approval`、`core/agent-loop/src/agent.ts` | 单次批准结果、原调用续接、`next-step` 和 `next-turn` 的输入时机。在 CA 持久台账里记录接纳和结算。 [批准接口](https://github.com/deepseek-ai/deepseek-harness/tree/fcc0ad462172522aaf5c93a7b2e01f6b9f0b0159/packages/interaction/user-approval) · [Agent loop](https://github.com/deepseek-ai/deepseek-harness/blob/fcc0ad462172522aaf5c93a7b2e01f6b9f0b0159/packages/core/agent-loop/src/agent.ts) |
| DSH `sandbox/sandbox` | 复用明确模式和不可用语义。按 CA 的部署保留独立的网络、进程和任务配额。Windows 后端按实际可强制执行的边界单独验收。 [沙盒契约](https://github.com/deepseek-ai/deepseek-harness/tree/fcc0ad462172522aaf5c93a7b2e01f6b9f0b0159/packages/sandbox/sandbox) |
| Aurabot `0a0ccb1c` 的 `src/chat/hub.js` | 借鉴私聊合并、插话与后续排队，把同一规则接入 CA 入口。 [收发实现](https://github.com/aureth-labs/dsh-aurabot-qq/blob/0a0ccb1cc401dee00ce0faac2cffbf57d0efecda/src/chat/hub.js) |

实装时按文件复用或移植，核对许可证、依赖和上游 commit，并登记源码归属。 [第三方记录](../THIRD_PARTY_NOTICES.md)

## 实施顺序

1. **正常入口先通。** 分页 schema、统一模型路由、工具可用性和批准后续跑。用本地假 provider 和一次真实入口验收，收集调用与耗时基线。
2. **打通真实任务。** 桌面启动恢复，服务器临时目录、任务资源与取消、文件版本和产物回执。验证一个正常任务、一次取消及一次断线查询。
3. **改善连续使用。** 下一 step 接纳用户补充，角色提案差异与记忆来源可追溯，纠正后跨会话清除旧事实影响。保留候选审核并先做小样本记忆回放。

研究以本地源码和固定上游为依据。工具时延、记忆质量与沙盒隔离结果以实施后的代表场景测量为准。
