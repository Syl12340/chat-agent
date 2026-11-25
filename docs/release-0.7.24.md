# 0.7.24 工具确认续跑与记忆来源

2026-11-25 发布。普通对话中的敏感工具暂停等待确认，批准后仍在原轮次执行同一调用，发送方无需再发消息。待确认窗口最长 90 秒并受原轮超时限制。服务重启会使待确认调用失效。工作区工具在确认后重新检查会话权限。

`workspace_read` 支持从模型入口续页，并可校验完整文件 hash。写入串行处理独占创建和复核，已修改的原件需要重新读取。聊天、主动生成与手动摘要应用统一的模型选择结果。记忆候选引用本轮用户消息并保留审核，界面可查看原话。Linux shell 获得自己的 `TMPDIR`，Windows 默认没有 Documents 时使用独立工作区。

| 项目 | 证据 |
| --- | --- |
| 主服务和 `personal` | `20261125-tool-continuation-01` / 0.7.24，两个服务 active，`NRestarts=0`，健康接口返回 ok |
| Linux SHA-256 | `796493d682086ef26d5180db1073fd28637c834e7541966dcf593039db5985eb` |
| Android | `android-20261125-02`，`0.7.24-debug` / versionCode 62，包名 `dev.chatagent.ai.debug` |
| APK SHA-256 | `f5e004401ea850ffcb831341f6210f5d9ee6b5a90c684a6de15385469db0c5a3` |
| APK 证书 SHA-256 | `4905b966d06c53d22da462b9ef3e36ff27b54994829950a610f92a6bab68b24a` |
| 备份 | `/opt/chat-agent/backups/pre-tool-continuation-20261125-01.tar.gz` |
| 备份 SHA-256 | `0c40f46e6cf5157939078a7d0bc12723e5d1beb05bda827771d130b187170c52` |

Windows 和 Linux build/vet、受影响包测试、Web 语法、Android 构建和线上 APK 下载校验通过。回环 API 测试使用假模型验证了批准前不写入、参数摘要不匹配时拒绝、批准后一次执行，以及记忆仍为待审核且保留来源。生产 Linux 使用非 root 沙盒进程确认指定临时目录可写，目录外样例文件不可读。真机操作、独立的长任务工作区和任务重启后查回还需后续验收。

## 回退

在服务器通过以下命令切回 0.7.23。停服务后再切换各自链接，保留当前账户和数据。备份恢复会覆盖备份之后的记录。

```sh
systemctl stop chat-agent chat-agent@personal
ln -sfn /opt/chat-agent/releases/20261125-private-instances-01 /opt/chat-agent/current.rollback
mv -Tf /opt/chat-agent/current.rollback /opt/chat-agent/current
ln -sfn /opt/chat-agent/instances/releases/20261125-private-instances-01 /opt/chat-agent/instances/personal/current.rollback
mv -Tf /opt/chat-agent/instances/personal/current.rollback /opt/chat-agent/instances/personal/current
systemctl start chat-agent chat-agent@personal
ln -sfn /opt/chat-agent/android/releases/android-20261125-01 /opt/chat-agent/android/current.rollback
mv -Tf /opt/chat-agent/android/current.rollback /opt/chat-agent/android/current
curl -fsS http://127.0.0.1:9991/healthz
curl -fsS http://127.0.0.1:18761/healthz
```

已安装 versionCode 62 的 Android 设备需要用同一签名和更高 versionCode 重打旧版才能原地降回功能。
