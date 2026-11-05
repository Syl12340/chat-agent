# 0.19.0 发布记录

2026-11-05。图片上传、模型/思考强度选择、聊天界面优化已完成开发，生产部署结果随后补记。Android 构建为 **0.19.0-debug / versionCode 16**，延续原包名、签名和设备身份。完整行为与接口见[功能说明](chat-0.19.md)。

本批关键检查均通过：新增 TestImageLifecycle、TestMultimodalThinkingLifecycle、TestMultimodalQueueLifecycle、TestImageChatLifecycle；受影响回归 TestDurableTurnQueue、TestProposalReviewAndConversationDeletion、TestDropTailPreservesSummary、TestCompactionPreservesHistory。没有新增完整矩阵、收费模型验收或重复无关测试。

浏览器在独立 demo 数据下检查了 ECNU Plus / 超高选择、仅图片发送、回复显示、图片懒加载及复制入口。390×844 手机布局无横向溢出，发送按钮保持在视口，深色为纯黑；浅色桌面也已截图检查。该检查没有真实识图或模型调用。Android 离线构建通过，真机选择器和软键盘尚未验收。

此次新增的 transcript/turns v2 是多模态和参数扩展，不是 Harness 3B/3C。旧数据仍可读取，0.18 不能读新 v2；有新写入后，回退必须保留现场并明确转换方案，不能仅换旧二进制或覆盖还原旧备份。
