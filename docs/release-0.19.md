# 0.19.0 发布记录

2026-11-05。图片上传、模型/思考强度选择、聊天界面优化已部署到生产服务器，Android 更新通道已发布。Android 构建为 **0.19.0-debug / versionCode 16**，延续原包名、签名和设备身份。完整行为与接口见[功能说明](chat-0.19.md)。

本批关键检查均通过：新增 TestImageLifecycle、TestMultimodalThinkingLifecycle、TestMultimodalQueueLifecycle、TestImageChatLifecycle；受影响回归 TestDurableTurnQueue、TestProposalReviewAndConversationDeletion、TestDropTailPreservesSummary、TestCompactionPreservesHistory。没有新增完整矩阵、收费模型验收或重复无关测试。

浏览器在独立 demo 数据下检查了 ECNU Plus / 超高选择、仅图片发送、回复显示、图片懒加载及复制入口。390×844 手机布局无横向溢出，发送按钮保持在视口，深色为纯黑；浅色桌面也已截图检查。该检查没有真实识图或模型调用。Android 离线构建通过，真机选择器和软键盘尚未验收。

此次新增的 transcript/turns v2 是多模态和参数扩展，不是 Harness 3B/3C。旧数据仍可读取，0.18 不能读新 v2；有新写入后，回退必须保留现场并明确转换方案，不能仅换旧二进制或覆盖还原旧备份。

## 实际发布

| 项目 | 记录 |
| --- | --- |
| 服务目录 | `/opt/chat-agent/releases/20261105-chat-01` |
| 上一版本 | `/opt/chat-agent/releases/20261105-companion-01` |
| 一致性备份 | `/opt/chat-agent/backups/pre-chat-20261105-01.tar.gz`，root:0600，正常停服后备份数据和配置 |
| 源码提交 | `32a7c84acc80ae8c060b51721b7c7169fe7250f3` |
| Linux SHA-256 | `70fd03236af29061ba0fdb8ac13c87cbd42a3beeb1893170bb4467fab03cf1fb` |
| Android 更新目录 | `/opt/chat-agent/android/releases/android-20261105-02` |
| APK 版本 / 字节 | `0.19.0-debug` / versionCode `16` / 944924 字节 |
| APK SHA-256 | `e72c387543c8e8752ecbed12b00f270b8da9a261c18865a1c4958965a8bb08df` |
| 原签名证书 SHA-256 | `4905b966d06c53d22da462b9ef3e36ff27b54994829950a610f92a6bab68b24a` |

线上只读检查覆盖健康、版本、模型允许列表、图像/思考选项、新 JS/CSS/SW、未认证访问拒绝、原有会话可读性、更新清单及完整 APK 下载哈希。没有上传生产图片、创建生产对话或触发付费生成。原模型默认、后台路由、角色文件、设备白名单、服务器端口和无关服务保持原配置。

发布命令曾遇到一次自动权限审核超时，确认旧服务正常且版本目录未创建后重试成功；没有发生停服发布失败或数据回退。旧目录与备份保留。脚本遇到异常时会先检查是否有新格式写入，存在时保留新读取器和失败现场，不用旧备份覆盖新数据。

手机尚未安装。通过应用内“更新”安装新包后，使用输入框“＋”选择图片，顶部切换模型，输入框下方选择思考强度；原登录、签名和后台接收偏好保留。高档思考可能增加等待时间和用量，实际识图/思考质量与真机选择器留待使用反馈。
