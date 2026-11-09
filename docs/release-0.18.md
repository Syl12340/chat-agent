# 0.18.0 交付与发布记录

2026-11-05。本批实现用户选择的“陪伴体验 + Harness 3A + 自建推送渠道”，已部署服务端 **0.18.0** 并发布 Android **0.18.0-debug / versionCode 15** 更新通道。保留原包名、签名和设备身份。

- [陪伴表达](companion-0.18.md)：减少复述、机械追问和模板收尾；关怀避免重复主题，共同话题可以零分享。没有调用真实模型证明质量提升，不覆盖自定义角色文件。
- [Harness 3A](harness-storage-contract.md)：已交付 `harness-storage/1` 契约，包括权威事件、请求快照、业务回执、unknown、删除范围及显式迁移回退。3B/3C 未实施，现有会话格式未迁移。
- [自建接收](push-channel.md)：Android 用户显式开启常驻通知，复用加密入口、设备鉴权；后台接收关怀/话题/到期提醒，默认关闭。后台问候要求接收租约在线且角色已启用自动生成。

## 最小检查

七项相关 Go 流程：TestPushInboxLifecycle、TestBackgroundCareLease、TestCareSkipAvoidsModelAndRerank、TestTopicDiscoveryFunnel、TestCareProbabilityLifecycle、TestRoleMemoryLifecycle、TestActivityChatControl。前两项覆盖订阅/鉴权、无副作用读取、持久化与租约失效、暂停、轮询中撤销、背景概率零调用及明确跟进一次生成。

两项原有回归最初被 Windows 沙箱阻止回环网络连接，提升执行权限后仅重跑这两项并通过；新流程使用本地模拟模型和临时数据，没有真实搜索/模型收费。通知权限、后台服务和去重代码通过 Android 离线构建；JS 语法与 Windows/Linux 构建结果随本批记录。没有执行真机安装、待机、Doze、电量或麦克风验收。

## 数据与回退

新增 delivery/state.json 只保存设备订阅，不新增推送正文副本。现有会话 v1、turns v1 和 life 兼容格式保持不变。升级不自动开启接收、关怀或话题；手机需更新并手动开启。停止前备份与独立版本目录仍用于发布回退；不回写旧备份覆盖上线后的用户数据。

## 实际发布

| 项目 | 记录 |
| --- | --- |
| 服务目录 | `/opt/chat-agent/releases/20261105-companion-01` |
| 上一版本 | `/opt/chat-agent/releases/20261104-topics-02` |
| 升级前一致性备份 | `/opt/chat-agent/backups/pre-companion-20261105-01.tar.gz`，root:0600，正常停服后备份应用数据和配置 |
| 源码提交 | `028fb3acdfe96f8296dacbed424ef1f5460115ff` |
| Linux SHA-256 | `54d74bcaf9df767aa8182fc8a6cfc9d3de230cfb1c52cd77483dd6aaa3363ba0` |
| Android 更新目录 | `/opt/chat-agent/android/releases/android-20261105-01` |
| Android 版本 | `0.18.0-debug` / versionCode `15` |
| APK 字节 | 1032533 |
| APK SHA-256 | `985fdaa1c5eaaf3e2a4f7fae69f856f599e081076eefc96f0fa5aee20f37f5d4` |
| 原签名证书 SHA-256 | `4905b966d06c53d22da462b9ef3e36ff27b54994829950a610f92a6bab68b24a` |

线上健康、版本、新入口及缓存、后台收件箱 schema、未认证拒绝、管理员不能开启设备订阅、更新清单与完整 APK 下载哈希均通过只读检查。上线时设备接收订阅数为 0，没有替用户开启。没有改动模型路由、服务器端口、密钥、人设文件或无关服务；没有触发付费验收、创建生产对话或向手机安装。

旧版本目录及数据备份保留，启动失败的发布脚本可自动切回旧目录；本次成功未触发回退。手机需通过“更新”安装，之后在“主动关怀 → 这台手机的通知”开启接收，接受常驻通知并允许系统通知权限。真机待机和自然对话效果留待实际使用反馈。
