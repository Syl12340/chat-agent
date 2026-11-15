# 持久受众与双端任务交付

日期：2026-11-15。承接[第一组共用改造](foundation-slice-2026-11-15.md)，尚未发布。

本组继续收口仿生陪伴、DSH Harness、任务 UI 与 QQ/IM 共用的两条契约。

## 已实现

- 群会话第一次建立或关联时，写入独立的 `session-audiences/<session>.json` 隐私下限。移动 mapping、停用渠道或服务器重启都不会让旧群 transcript 恢复私人记忆、陪伴或工具。
- 启动时为现有群 mapping 回填受众记录；损坏或不可读的记录按群聊失败关闭。群 mapping 仍不能接到带私人历史的会话，主动陪伴继续跳过群会话。
- 本机任务协议增加 session、role、request 归属。完整任务与摘要一起写入 operations v3；回执摘要覆盖这些归属，不能挪到另一轮对话。
- operation、完整 task、近期 receipt 与安全产物元数据统一持久化。receipt 缓存淘汰后，保留中的 operation 仍能授权下载对应文件。
- 回执中的文件数量、名称、类型、大小和 SHA-256 经过服务端约束；工作台直接显示持久下载入口。
- PWA cache 更新为 v38。

## 保留边界

- 群入口仍未连接真实账号验收；账号撤销、群成员身份和真实平台媒体降级继续保持关闭或灰度。
- 本机端重启后不会恢复进程或自动重放任务；晚到回执只能结算已经领取且摘要一致的 operation。
- 本机执行仍是 workspace guard 与固定动作，不是任意 shell 的 OS 沙箱。
- 当前任务卡使用已有工作台与工具 Presentation；完整进度日志、等待授权和跨任务成果面板仍按总规划继续。

## 最小验证

- 群隐私下限在重启和 mapping 移动后保持，损坏记录失败关闭。
- 完整本机任务身份、晚到回执、产物元数据和 receipt 淘汰后的下载授权可恢复。
- OneBot 私聊闭环、受影响 API/localagent 测试和 JavaScript 语法检查通过。
- Windows 与 `linux/amd64` 的 build/vet 通过。
