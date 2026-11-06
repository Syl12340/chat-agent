# 0.23 Harness 3B 隔离持久结算

2026-11-07。状态：**源码完成，未部署，未接入生产权威数据**。生产仍为服务端 0.22.0，Android 仍为 0.21.2-debug / versionCode 20。

## 已实现

- 新增原生 Go `internal/runlog`。每个会话隔离目录使用 manifest、`g-1/events.jsonl`、`snapshots/` 和 `payloads/`；事件采用连续 sequence 和随机稳定 ID，完整行写入并 Sync，短写或 Sync 失败后 writer fail-closed。
- 模型网络请求前保存最终 `ModelRequest`；system、模型可见 messages、工具 schema、thinking/effort 和输出参数语义均在同一准备边界冻结。图片只保存既有元数据引用，不复制内存 DataURL。输出上限为 0 时原样记录为“参数省略/服务商默认”。
- 保存 attempt、step、模型请求/结果、工具开始/结果和停止原因。大正文使用带 SHA-256、字节数及相对路径的独立文件引用；取消后的 partial 只属于 attempt，不提交为完整 assistant 历史。
- 工具通过准入后、处理器调用前生成并持久化 operation ID。`ToolOperationID` 只在 recorder 启用时注入，既有 `ToolCallID` 保持兼容。
- tasks state 升级为可读 v1/v2；v2 每角色最多保留 8192 个、全 store 最多 32768 个回执。`todo_add`、`reminder_add` 声明 `atomic`，任务改变与最小回执在同一锁和同一次 `state.json` 原子替换中提交；回执含角色、operation、参数摘要、实体/revision 和结果摘要，不保存提示词或授权原文。

## 冻结容量

- 事件行 256 KiB；单请求 snapshot 128 MiB；单模型/工具 payload 64 MiB；单会话隔离代 1 GiB。
- tasks v2 snapshot 16 MiB；回执满额后拒绝新的受保护写入，不按日期清理后复用旧 operation ID。
- 容量是编码后存储边界，不是 token 限制。超限发生在外部派发前时停止本 attempt，不截断后冒充完整快照。

## 回执支持范围

| 工具 | 0.23 能力 |
| --- | --- |
| `todo_add`、`reminder_add` | `atomic`（仅在 3B recorder 提供 operation ID 时启用） |
| 其他读取工具 | `none` / 不需要业务结算 |
| 其他写工具，包括记忆、活动、角色候选、任务更新/删除 | `none`；出现 `tool.started` 而没有可信业务回执时为 unknown，禁止自动重跑 |

现有任务幂等 key、活动请求记录或角色候选恢复逻辑没有被升级成通用回执。

## 最小故障验证

1. 模拟 task 已提交、`tool.finished` 台账落盘失败：重开 task store 后仍可按 operation 查询回执；用同 operation 和参数再次调用只返回原记录，任务总数仍为 1；运行事件停在 `tool.started`，没有第二次模型请求。
2. 模型先流出 partial 再取消：台账保存 canceled partial，transcript 只含用户输入，没有不完整 assistant 消息，Provider 调用次数为 1。
3. 正常生命周期验证 snapshot/payload 引用、连续事件、SHA-256 和图片 DataURL 不落盘。

## 明确保留给 0.24 / 3C

- 不在现有 API worker 创建 recorder，不改变线上 transcript/turns 的权威性。
- 不迁移旧 transcript v1/v2 或 turns v1/v2/v3，不做重启投影、runtime 会话读取、保留清理、整会话/最新轮删除联动或回退导出。
- 不自动恢复、重发付费模型请求或重做 unknown 工具。
- 不部署服务器，不修改生产数据、配置、密钥、Android 包或更新清单，也没有调用真实模型。
