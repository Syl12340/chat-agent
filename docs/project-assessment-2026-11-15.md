# chat-agent 项目评估

日期：2026-11-15。评估基线：`75747a834f1c519964f750925f5c36e274dd090f`。评估完成后，工作区已继续实现来源/受众、持久群作用域、本机任务/回执和文件交付的两组共用改造，见[第一组](foundation-slice-2026-11-15.md)与[第二组](audience-operation-slice-2026-11-15.md)记录；尚未重新部署、连接真实 IM 或调用业务模型。

详细方向见[产品方向与能力设计](product-direction.md)。本文中的“已有”指可在源码定位的能力；“已有发布记录”不等于本次重新实测，也不等于所有分支已在真实环境闭环。

## 1. 结论

CA 已经是具有聊天、记忆、工具执行和多端入口的个人 Agent 原型，不能再按“绝大多数方向尚无第一块砖”规划。它也还不是可无条件开放所有能力的成熟应用：部分能力只完成窄流程，来源、权限、任务和产物在不同模块之间还没有形成一致契约。

下一步的主要价值在于把现有能力连起来：自然陪伴与跨渠道记忆、真实课件到学习成果、远程任务到可恢复的文件交付。无需迁移 Go，也不宜继续用新增入口数量衡量进展。

当前最需要纠正的判断：

- 消息身份、持久 surface、Linux shell、本机执行端和 ComfyUI 作业接口**已经存在**，不应再列为从零开发。
- 批注后端不等于批注前端；课件提取加提示词不等于完整学业系统；操作索引不等于持久任务回执。
- QQ 官方/OneBot 代码已发布，但真实账号未启用；当前来源路由与群隔离存在具体缺口，不能写成“只差填写凭据”。
- UI 已完成一轮精简和主题调整；后续应完善上下文工作台与交互，不应从最早的简陋界面重新设计。

## 2. 能力现状

| 领域 | 已有实现 | 距离完整体验的缺口 | 主要依据 |
| --- | --- | --- | --- |
| 核心 Harness | 逐步上下文、工具目录/执行器、审批、run 记录、队列与 runtime 选代 | 不同业务回执覆盖不一致；跨入口受众未纳入统一 turn 契约 | [probe](../internal/probe/)、[step_context](../internal/api/step_context.go)、[turns](../internal/turns/store.go) |
| 自然聊天 | 连发合并、生成中继续输入、排队、停止、语义气泡、统一助手 | 插话/合并不能跨身份和受众；自然表达质量仍需真实使用反馈 | [turn worker](../internal/api/turns.go)、[客户端](../web/app.js) |
| 记忆与陪伴 | 候选审核、明确偏好、纠正/遗忘、活动、主动关怀与概率/预算/静默、逐条反馈 | 主要仍是角色作用域；反馈使用有限近期记录；主动目标选择缺少公共/私人约束 | [knowledge](../internal/api/knowledge.go)、[life](../internal/api/life.go)、[proactive](../internal/api/proactive.go) |
| Linux 工作区 | 读/搜/文件操作、真实 shell、非 root、Landlock/网络限制、超时/输出约束 | 不等于完整容器；任务级资源和授权出口、长任务观察仍需扩展 | [workspace](../plugins/builtin/workspace/)、[sandbox](../internal/sandbox/) |
| Windows 本机 Agent | 配置设备、目录读取/搜索、带旧值摘要的写入、固定动作、取消、完整任务/回执与产物元数据持久化、文件回传 | 固定动作不等于任意命令编排或 OS 沙箱；没有进程恢复和长日志流 | [local API](../internal/api/local_agent.go)、[本机契约](../internal/localagent/contracts.go) |
| 服务器小精灵 | 状态、日志、服务、发布、固定维护动作与审批/回执 | 宿主级资源目录、通用变更提案/diff/批准/复查/恢复尚未闭环 | [server plugin](../plugins/builtin/server/) |
| 插件 | Manifest/Catalog、授权、生命周期、多个业务 Tool、Service、工具附属 Prompt | 尚无统一独立 Provider/ContextSource/Trigger/Renderer 注册体系 | [pluginapi](../internal/pluginapi/plugin.go)、[builtin](../plugins/builtin/) |
| 课件与文献 | 文本/HTML/DOCX/PPTX 提取，list/read，分析/讲解/测试草稿入口；文献搜索/网页正文 | 不支持 PDF；无原件保存/页级结构和引用；学习进度、完整书目/全文任务待接通 | [courseware](../internal/courseware/store.go)、[课件插件](../plugins/builtin/courseware/)、[文献插件](../plugins/builtin/literature/) |
| GenUI 与学习交互 | Markdown/公式/结构渲染、chat-ui 组件、Presentation、持久 surface CRUD/修订、消息身份、批注 API | surface 缺少统一来源/业务绑定；批注无用户可用的选段前端；未找到侧聊产品链路 | [surfaces](../internal/api/surfaces.go)、[annotations](../internal/api/annotations.go)、[web/ui](../web/ui/) |
| ComfyUI | 图像桥、profile、作业 ID/查询/取消、参数/hash、图片导入 | 服务重启后旧运行记录会成为 unknown；已有 renderer ID 尚未自动重新观察/导入 | [image jobs](../internal/api/image_jobs.go)、[bridge jobs](../cmd/imagebridge/jobs.go) |
| 客户端 | Web/PWA、Kotlin 壳、设备 challenge、服务器模型配置、工具卡、右侧工作台、精简侧栏 | CSS 仍有多层覆盖；任务/资料/成果需统一；最新真机、待机触达证据有限 | [web](../web/)、[model configs](../internal/api/model_configs.go)、[已有验证记录](capability-check.md) |
| QQ/IM | 官方 Gateway、OneBot、身份绑定、session mapping、Inbox/Outbox、图片入站、来源/受众路由、持久群作用域、同一 Harness | 真实平台未验；账号撤销、群成员身份和媒体降级仍待闭环 | [channel](../internal/channel/)、[qqofficial](../internal/api/qqofficial.go)、[identity](../internal/api/channel_identity.go) |

没有用百分比评价这些能力。一个 API、一个工具、一个可运行用例和一个可长期使用的产品流程，代表不同完成程度。

## 3. 影响后续方向的具体发现

以下为源码静态发现，用于确定设计前置条件；本次没有运行复现，也没有认定发生过真实数据外发。

### 3.1 QQ 出站来源判断：工作区已修正，待发布

[qqofficial.go](../internal/api/qqofficial.go) 的 `TurnCompleted` 先从 `MappingForSession` 得到 `ok=true`，随后寻找入站来源时复用同一个变量。没有找到 Inbox 时，`ok` 仍为 true，后面的检查不会退出，仍可为应用发起的轮次建立 QQ 投递意图。

这直接否定了上一轮发布描述中“应用消息不会因会话关联外发”的无条件表述。后续工作区已独立检查 Inbox，并把来源/受众写入 turns v5 和完成回调；应用轮次不会建立 QQ delivery。0.7.0 线上版本仍不包含该修正。

### 3.2 群聊隔离：已封住两条路径，仍不是完整数据边界

[channel_identity.go](../internal/api/channel_identity.go) 的群判定依赖当前启用的 integration 与 mapping；关联会话接口检查同角色与运行状态，但没有拒绝把群渠道关联到带私人历史的会话。停用渠道或移动 mapping 也会改变原会话的群判定。

[knowledge.go](../internal/api/knowledge.go) 已对群关闭部分私人上下文和工具，这是有效措施，但不会清除会话已有历史。另一个独立路径是 [proactive.go](../internal/api/proactive.go)：选目标时只依据角色、优先会话和历史；[life.go](../internal/api/life.go) 组装的 trigger 可包含获批记忆与私人事件。末端拒绝向群投递，不能阻止私人内容先写入群会话。

工作区现已跳过群会话作为主动目标，拒绝把群 mapping 关联到已有私人消息的会话，并以独立 sidecar 固定群 transcript 的隐私下限；停用或移动 mapping 后旧会话仍按群聊隔离。turn 来源同时阻止跨受众合并。真实群入口在账号撤销、成员身份和平台验收完成前仍应保持关闭。

### 3.3 连发合并没有来源维度：工作区已修正

[turns.Store.Begin](../internal/turns/store.go) 根据模型、思考档位、模式、体积与回调合并；当前 [worker 回调](../internal/api/turns.go) 只排除互动响应。Turn 本身没有发送者、入口、受众或授权集合。

后续 turns v5 已持久化外部来源、发送者和受众；聚合要求这些字段完全一致，并把根轮次来源带到出站观察者。应用来源保持兼容旧 JSON 与旧请求摘要。

### 3.4 本机执行端回执归属：工作区已修正

[localAgentComplete](../internal/api/local_agent.go) 验证 Bearer 与请求中的 device ID，也验证回执 operation ID 与 URL 一致；但没有把已认证设备与该 operation 的目标设备比较，也没有按原任务核对 request hash/协议/终态。队列 `complete` 还会在没有 inflight 项时保存回执。

后续工作区已校验目标设备、协议、operation、原任务 SHA-256 和终态；其他已配对设备和摘要不匹配的回执会被拒绝。该路径已有最小回归，尚未发布。

### 3.5 本机操作和近期回执已统一持久化

[localQueue](../internal/api/local_agent.go) 现在把 operation、绑定 session/role/request 的完整 task、近期 32 条 receipt 和安全产物元数据写入 operations v3。重启仍把未完成项标为 interrupted，避免自动重放；已领取任务可以由原设备用相同摘要补交晚到终态。receipt 缓存淘汰后，保留中的 operation 仍可授权对应产物下载。进程恢复、长日志流和明确保留/清理策略仍待统一 Operation/Artifact 契约。

### 3.6 学业与 GenUI 的完成口径需要收紧

[courseware.Store.Add](../internal/courseware/store.go) 保存文本和元数据，没有保留上传原件；Office 提取把 XML 文本压平，PDF 明确不支持。页面上的分析/讲解/测试把请求放进草稿，实际能力依赖模型读取文本；没有页码、题目来源和学习档案。

[persistentSurface](../internal/api/surfaces.go) 具备 ID/revision/view，但没有统一消息/资料来源；这与 chat-ui 的来源校验是不同层次。批注 API 已校验消息 ID 与内容 hash，但 `web/` 没有对应选段批注产品接线，也未发现侧聊入口与派生会话链路。

这些地基有价值，后续应围绕真实资料和业务动作完成闭环，不应重新开发另一个 surface CRUD，也不能把现状称为完整 GPT GenUI 或 EduWork 迁移。

## 4. 发布、测试与使用证据

最近发布记录为服务端 **0.7.0**（`20261115-im-unified-01`）、Android **0.7.0-debug / versionCode 38**（`android-20261115-01`）。校验和、备份和回退信息见[0.7 发布记录](release-0.7.md)；这里引用历史证据，不声称本次重新检查线上。

已有记录表明 Windows/Linux 构建与 vet、所列相关包测试通过，发布后服务 active、NRestarts=0，已有会话可读。它们证明构建和覆盖到的行为，不证明真实 QQ、群隐私、跨设备回执或自然表达已完整验收。上述静态缺口说明测试覆盖仍有具体空白。

发布当时 QQ 官方与 OneBot 均关闭，没有连接真实账号或调用模型验收。Android 包已发布也不等于最新真机、Doze 或强停后触达已验证。ComfyUI 历史健康检查不能代替当前模型/profile 可运行状态。

升级前备份存在，但现有记录不足以证明其为跨文件一致性快照或已完成恢复演练。涉及新业务存储时应明确停止写入/一致性方式与恢复范围，不把 tar 成功写成恢复可靠。

## 5. 原规划的修正

| 旧表述 | 当前修正 |
| --- | --- |
| 多条主线还没有第一块砖 | 基础大多已有，主要缺共享契约与端到端体验 |
| 消息身份之后才做 surface/批注 | 身份、surface 与批注后端已存在，补来源/交互和侧聊 |
| ComfyUI 尚需加 job ID | 作业化已存在，补回执丢失、重启观察和产物持续可用 |
| QQ 仅纯文本 owner 私聊 | 新代码包含官方 Gateway、图片与可选群路径；真实入口仍关闭且边界未闭合 |
| 插件化只迁移一个搜索工具 | 多个业务工具已入插件；缺少其他独立扩展点验证 |
| Go 零第三方模块 | 已引入 `github.com/coder/websocket v1.8.15`；仍为 Go 标准库优先 |
| 参考项目都不能装，所以只能借鉴思想 | 可以依法直接复用适合的源码/组件，或采用独立服务；宿主依赖和许可逐组件评估 |
| N1→N2→N3 等固定批次 | 改为八方向的目标、具体能力、依赖与完成标准，不安排工程排期 |

## 6. 规划上的决定

- 保持 Go 核心和现有客户端，不以换栈代替能力收口。
- 自然陪伴、学业和任务交付是主要用户价值；插件化与协议统一服务于这三者。
- IM 来源/受众和执行回执归属是相应能力开放的前置条件，不与“以后再优化体验”混在一起。
- 继续减少手工管理入口；提供自然语言操作、上下文卡片、统一工作台和必要的可查管理路径。
- 以 DSH 源码为首要复用依据，结合 dsh-Go 远程协议、Aurabot 节奏与主动性、AstrBot 事件/平台分层、EduWork 学习成果组件；逐项保留来源和许可。
- 不为规划新增测试或部署。后续实现只验证所改关键边界，复用已有检查，避免重复矩阵和重复付费模型调用。
