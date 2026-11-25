# 0.7.23 私有实例与信任设备

2026-11-25 发布服务端和 Android。主服务沿用原设备权限，私有实例使用单用户账户和等效信任设备。第一台设备通过一次性链接认领账户，后续设备核对公钥指纹后加入。同一账户的信任设备共享会话、记忆、模型、工作区和管理设备的能力。撤销作用于对应设备。

Linux 现在可以运行多套私有实例。每套使用独立服务账号、回环入口、凭据、数据与工作区，单独配置 CPU、内存和进程上限。实例内工作区直接在私聊可用，文件写入和 shell 继续使用操作批准。主机维护工具及无沙盒固定命令从私有实例中移除。账号认领方法见[部署说明](../deploy/README.md#私有-ca-实例)。

| 项目 | 已发布版本 |
| --- | --- |
| 主服务 | `20261125-private-instances-01`，0.7.23 |
| 私有实例 | `chat-agent@personal.service`，HTTP `127.0.0.1:18761`，签名传输 `127.0.0.1:18762`，待认领 |
| Linux SHA-256 | `11732b5d6def5b41bb795eaf75c3d50846bb1ec168db6ef0544dbb517f6bb27a` |
| Android | `android-20261125-01`，0.7.23-debug / versionCode 61，包名 `dev.chatagent.ai.debug` |
| APK SHA-256 | `4fd69e7e80652faf37bd412bcf235f17a4def55d41eed6cbd3db14a1fdc11d18` |
| APK 证书 SHA-256 | `4905b966d06c53d22da462b9ef3e36ff27b54994829950a610f92a6bab68b24a` |
| 备份 | `/opt/chat-agent/backups/pre-private-instances-20261125-01.tar.gz` |
| 备份 SHA-256 | `6f471f86166243d1684f1431a5fb2576568064c6c0c17d2e1f6236de085ffc07` |

Windows 和 Linux 的 build/vet、受影响 Go 包、Web 语法检查、Android 构建和在线 APK 鉴权下载校验通过。两个 Linux 验证实例测试了一次性认领、签名登录、第二台设备可共享资源和管理设备、撤销后失效、跨实例凭据与文件拒绝，以及 A 重启时 B 仍可用。验证实例随后停用。主服务与 `personal` 均为 active，`NRestarts=0`，健康接口返回 ok。`personal` 初始为 Demo，模型可由用户在认领后配置。Android 真机和独立的每任务配额仍待专项验收。

## 回退

仅停用私有实例可执行 `systemctl disable --now chat-agent@personal`，该命令保留认领、配置和数据。主服务回退到已知的上一版 release，APK 链接切回上一版发布目录。执行者须确认应用数据兼容，保留现有数据优先于恢复旧备份。

```sh
systemctl stop chat-agent
ln -sfn /opt/chat-agent/releases/20261123-care-feedback-paging-01 /opt/chat-agent/current.rollback
mv -Tf /opt/chat-agent/current.rollback /opt/chat-agent/current
systemctl start chat-agent
ln -sfn /opt/chat-agent/android/releases/android-20261123-01 /opt/chat-agent/android/current.rollback
mv -Tf /opt/chat-agent/android/current.rollback /opt/chat-agent/android/current
curl -fsS http://127.0.0.1:9991/healthz
```

已安装 versionCode 61 的手机遵循系统降级限制。需要在手机端回退时，用上一版源代码重新构建同签名且 versionCode 更高的包。
