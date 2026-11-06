# 0.22.0 陪伴接续与模型消息投影

2026-11-06。状态：**代码实现完成，尚未部署**。服务端源码版本为 0.22.0；本次没有原生 Android 改动，已发布的 0.21.2-debug / versionCode 20 包装仍兼容。部署与线上检查完成前，生产环境仍是 0.21.2。

## 行为变化

- 上下文体积估算和 Provider 请求共用同一模型消息投影。最终回答的展示用 `reasoning_content` 继续保存在 transcript/turns 并可在界面查看，但不再计入下一轮模型上下文或因此提前触发摘要；带工具调用的 reasoning 仍按上游协议保留并回传。
- 当前消息有具体检索词时只用当前消息召回，不让旧话题覆盖换题。对“那后来呢”一类没有具体检索词的短接续，最多回看最近三条可见用户消息，取最近的具体主题召回已审核记忆、事件和活动；历史 floor 之前的内容、assistant 文本和 `chat_ui_response` 不参与。
- 共同话题决策增加最近三次已生成/已读分享，要求同时避免重复主题、开场、观察和问题。既有来源 URL 去重、未读限制、三小时间隔、每日额度、公开网页正文读取及零输出选择不变。

这些变化不增加分类模型、模型调用、工具次数或持久格式。没有新增显式聊天模式、自动记忆写入或后台写权限。计划/实际、当前纠正、话题切换和少问问题仍由同一轮 LLM 结合现有历史与有来源参考处理。

## 最小验证

- `TestModelProjectionSeparatesDisplayAndToolReasoning`：长展示思考不触发压缩、不进入 Provider；工具协议 reasoning 仍保留；展示思考仍写回会话。
- `TestContinuityRecallQuery`：指代式接续使用最近可见用户主题；具体换题不被覆盖；历史 floor 与互动答案包不泄漏到检索。
- 相关 `internal/probe`、`internal/retrieval`、`internal/memory`、`internal/api` 包测试通过；`go build -buildvcs=false -o bin/chat-agent-0.22.0.exe ./cmd/server` 与 `go vet ./...` 通过。

本次未调用真实模型、未发送现有会话历史、未迁移数据、未修改生产配置或发布 Android 包。自然表达质量仍需部署后的日常使用反馈；本地模拟回归只证明上下文选择和协议边界。
