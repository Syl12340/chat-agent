# 持续 Harness Loop

日期：2026-11-15。已发布：服务端 `0.7.0` / release `20261115-continuous-loop-01`。

## 原因

普通对话原先继承 `probe.DefaultConfig` 的 4 个模型 step、4 次工具调用和 30 秒总时限。一次“模型选择工具 → 执行 → 模型读取结果”至少使用两个 step，因此复杂任务很快进入无工具的强制收尾边界。稳定系统提示还要求 `Be concise`，进一步造成普通回答偏短。

DSH 固定源码 `fcc0ad462172522aaf5c93a7b2e01f6b9f0b0159` 的 `packages/core/agent-loop/src/agent.ts` 在一个 turn 内持续执行 step，直到模型返回无工具的最终回答、被取消或失败；它没有 CA 这组默认四步截断。CA 现在采用相同的主要终止语义，并保留自己的 Go 安全边界。

## 当前行为

- 默认 `max_turns=0`、`max_tools=0`：不按模型步或工具数提前收尾。
- 模型返回最终回答、用户停止、五分钟总时限、工具自身超时、审批拒绝、运行错误或重复同批调用都会结束或阻止继续。
- `-max-turns`、`-max-tools` 仍可由部署方设为非零硬上限；`CHAT_MAX_TURNS`、`CHAT_MAX_TOOLS` 和 `CHAT_TURN_TIMEOUT` 接入部署脚本。
- 普通模型请求继续不发送人为输出 token 上限；Provider 自身客观上限仍存在。
- Runtime snapshot 明确标记 continuous loop/tool 状态，不再向模型伪装成只剩几步。
- 稳定提示改为按用户请求决定深度和长度，不再全局要求短答。

主动关怀仍是独立的一步、无工具、短输出流程；持续 loop 只用于用户发起的普通 Harness turn，不会放大后台主动生成成本。

## 最小验证

- 新夹具连续执行 6 次不同工具调用，并在第 7 个模型 step 返回最终回答，超过旧四步边界后仍完整结算。
- probe 完整测试通过。
- API 的会话、队列、活动工具循环、reasoning 持久化和完成观察器回归通过。

## 上线实测（2026-11-15）

线上临时会话里发了一个需要连续 6 件事的请求（查时间 → 加待办 → 列待办 → 再加 → 再列 →
说星期几）。实际发生 5 次工具调用，分散在 5 个以上模型 step 里，零错误，最后完整给出
汇总回答（672 字，带表格）。旧的 `MaxToolCalls=4` 会在第 5 次调用返回 `TOOL_LIMIT`，
现在没有。

`/api/status` 现在直接暴露这三个值，部署后回读为
`{"continuous_loop":true,"max_turns":0,"max_tools":0,"timeout_seconds":300}`。

注意：服务端 `version` 仍是 `0.7.0`，而 2026-11-15 早些时候发布的 `20261115-im-unified-01`
也是 `0.7.0` 但不含这些字段。**区分标志是 `continuous_loop`，不是版本号。**
