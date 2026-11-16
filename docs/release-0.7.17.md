# 0.7.17 四条主线纵切发布

2026-11-16。状态：服务端与 Android 更新通道已发布。本次把仿生聊天、QQ 等 IM 联动、DSH Harness/双端 Agent、UI/GenUI/EduWork 四条主线合成一次实现、验证、备份与部署；没有连接真实 QQ/AstrBot 账号，没有调用真实模型，也没有增加侧栏常驻入口。

## 交付内容

### 1. 仿生聊天与陪伴

- 陪伴偏好新增 `default` / `single` / `bubbles` 回复节奏；由系统提示告诉模型可用表达方式，不增加显式对话模式切换。
- 网页与 AstrBot 共享该偏好：`single` 保持一个气泡，其余继续使用结构安全的语义分段；旧偏好文件按 `default` 兼容读取。

### 2. QQ 等 IM 联动

- 渠道页显示 QQ Official、OneBot v11、AstrBot 的启用状态和 Inbox/Outbox 状态计数，不展示消息正文。
- 管理员可解绑外部身份；解绑同时把该账号尚未结算的私聊投递标为失败。
- AstrBot 领取投递前再次核对身份绑定，即使解绑后的清理遇到异常，也不会继续向已撤销账号发送私聊。
- 三个生产网关继续默认关闭；本版只完成安全和可观测性收口，不宣称真实账号闭环已经验收。

### 3. DSH Harness / 双端 Agent

- 新增 `local_job_start`：模型可把宿主预先配置的固定动作作为异步任务启动，立即获得 `operation_id`，无需让聊天轮一直等待。
- 新增 `local_job_status`：同一会话与角色可查询状态、回执和有界输出；跨会话查询被拒绝。
- 复用已有持久 operation/task/receipt 协议和晚到回执结算；重启时未完成任务明确进入 interrupted，不伪装成功。
- 本机端仍只允许宿主配置的动作，没有 OS 级强制沙箱时不开放任意 shell；服务器工作区自由 shell 继续使用现有 fail-closed Linux 约束。

### 4. UI / GenUI / EduWork

- `courseware_read` 返回与文档版本、哈希、字节区间和行区间绑定的 `source_ref`。
- 课件工作台可直接查看引用原文并前后翻页；分析、讲解、测验草稿要求模型引用同一 SourceRef。
- SourceRef 由服务端按实际命中位置生成，查询命中后不会把调用方请求的旧偏移冒充真实来源。
- 本版先建立可追溯引用底座；PDF/PPTX 的真正页级定位、可更新讲解/测验 surface 和错题反馈仍是后续纵切。

## 最小验证

- `go build -buildvcs=false ./...`、`go vet ./...` 通过。
- `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -buildvcs=false ./...` 与对应 `go vet ./...` 通过。
- 受影响测试通过：渠道身份撤销与待投递止损、回复节奏旧数据兼容、异步本机任务状态/会话隔离、课件 SourceRef，以及完整 `internal/api` 包。
- 受影响网页脚本均通过 `node --check`；`git diff --check` 通过。
- Android `assembleDebug` 通过；APK 元数据为 `0.7.17-debug` / versionCode `55`，签名有效且证书延续。
- 线上零模型只读验收：服务 active，`NRestarts=0`；版本 0.7.17；PWA `chat-shell-v53`；连续 Harness 为 true；4 个会话、111 条消息、34 轮可读；active/running/queued/paused/unavailable 均为 0；scheduler、网页搜索和网页读取正常；AstrBot、OneBot、QQ Official 均关闭，禁用的 AstrBot/OneBot webhook 返回 404；最近十分钟无 warning 级服务日志。

## 发布凭据

| 项目 | 值 |
|---|---|
| 服务 release | `/opt/chat-agent/releases/20261116-four-slices-01` |
| Linux SHA-256 | `c6f8bcd70a7b9dd853a3274793d8a627c5b24c2d4411c65a7f54d796fa9ec68c` |
| Android release | `/opt/chat-agent/android/releases/android-20261116-16` |
| Android 版本 | `0.7.17-debug` / versionCode `55` |
| APK SHA-256 | `759ecff42303bda45acb404ca57207fd57c1b7592a6024df7a2c386135f73262` |
| 包名 | `dev.chatagent.ai.debug` |
| 签名证书 SHA-256 | `4905b966d06c53d22da462b9ef3e36ff27b54994829950a610f92a6bab68b24a` |
| 升级前备份 | `/opt/chat-agent/backups/pre-four-slices-20261116-01.tar.gz` |
| 备份 SHA-256 | `c7f28fa7b604a5fbd9272755c95bea315a01e1c27d8f731f6299b7a42a4cfce1` |

## 回退边界与命令

渠道、任务和课件存储没有不可逆迁移。新增的 `reply_rhythm` 会写入严格解码的陪伴偏好 JSON：在 0.7.17 尚未保存陪伴偏好前可只切回旧二进制；一旦保存过新偏好，回退到 0.7.16 必须恢复本次升级前备份，不能只切软链接。Android 已安装 code 55 的设备不会自动降级。

仅回退应用（确认 0.7.17 上线后没有写过陪伴偏好时）：

```sh
systemctl stop chat-agent
ln -sfn /opt/chat-agent/releases/20261116-bionic-astrbot-01 /opt/chat-agent/current.next
mv -Tf /opt/chat-agent/current.next /opt/chat-agent/current
find /var/lib/chat-agent -name '*.lock' -delete
systemctl restart chat-agent

ln -sfn /opt/chat-agent/android/releases/android-20261116-15 /opt/chat-agent/android/current.next
mv -Tf /opt/chat-agent/android/current.next /opt/chat-agent/android/current
```

如已写入新偏好或需要连数据/配置一起恢复：先停服并另存当前状态，再解包 `/opt/chat-agent/backups/pre-four-slices-20261116-01.tar.gz` 到 `/`，清理 `*.lock` 后启动。恢复备份会丢失升级后的全部数据变更，执行前必须另存现状。
