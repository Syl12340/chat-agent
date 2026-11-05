# 0.18.0 交付与发布记录

2026-11-05。本批实现用户选择的“陪伴体验 + Harness 3A + 自建推送渠道”。当前为构建交付，生产部署结果在完成发布后补记；此前线上版本为 0.17.0 / Android versionCode 14。

- [陪伴表达](companion-0.18.md)：减少复述、机械追问和模板收尾；关怀避免重复主题，共同话题可以零分享。没有调用真实模型证明质量提升，不覆盖自定义角色文件。
- [Harness 3A](harness-storage-contract.md)：已交付 `harness-storage/1` 契约，包括权威事件、请求快照、业务回执、unknown、删除范围及显式迁移回退。3B/3C 未实施，现有会话格式未迁移。
- [自建接收](push-channel.md)：Android 用户显式开启常驻通知，复用加密入口、设备鉴权；后台接收关怀/话题/到期提醒，默认关闭。后台问候要求接收租约在线且角色已启用自动生成。

## 最小检查

七项相关 Go 流程：TestPushInboxLifecycle、TestBackgroundCareLease、TestCareSkipAvoidsModelAndRerank、TestTopicDiscoveryFunnel、TestCareProbabilityLifecycle、TestRoleMemoryLifecycle、TestActivityChatControl。前两项覆盖订阅/鉴权、无副作用读取、持久化与租约失效、暂停、轮询中撤销、背景概率零调用及明确跟进一次生成。

两项原有回归最初被 Windows 沙箱阻止回环网络连接，提升执行权限后仅重跑这两项并通过；新流程使用本地模拟模型和临时数据，没有真实搜索/模型收费。通知权限、后台服务和去重代码通过 Android 离线构建；JS 语法与 Windows/Linux 构建结果随本批记录。没有执行真机安装、待机、Doze、电量或麦克风验收。

## 数据与回退

新增 delivery/state.json 只保存设备订阅，不新增推送正文副本。现有会话 v1、turns v1 和 life 兼容格式保持不变。升级不自动开启接收、关怀或话题；手机需更新并手动开启。停止前备份与独立版本目录仍用于发布回退；不回写旧备份覆盖上线后的用户数据。
