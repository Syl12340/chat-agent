# 0.7.18 四条主线第二纵切发布

2026-11-17。状态：服务端与 Android 更新通道已发布。本次把主动陪伴质量、双端 Agent 工作台、课件 GenUI 和 AstrBot 上线前诊断合为一次实现、验证、备份与部署；没有调用真实模型，没有连接真实 QQ/AstrBot 账号，也没有增加侧栏常驻入口。

## 交付内容

### 1. 仿生聊天与主动陪伴

- 主动生成上下文明确区分“用户允许跟进的事件”和“可选日常问候”，同时提供等待时长、同事件已发送次数及近 30 天 `helpful` / `less` 显式反馈计数。
- 系统提示将这些数据限定为本次触达参考，不允许从一次反馈或未回复推断人格、情绪或关系变化；没有自然理由时继续允许 `[沉默]`。
- 关怀界面显示来源类型；“少一点这种”的说明明确只降低近期同类频率，不自动修改长期画像。

### 2. DSH Harness / 双端 Agent

- 执行端领取任务时公布能力和宿主配置的固定动作名；模型可用 `local_endpoint_status` 先查在线状态与可用动作，公告不包含命令参数或 token。
- 已在执行中的任务取消后进入 `cancel_requested`，只有执行端回传 `canceled`、`completed` 或 `failed` 才形成终态；取消请求不再被伪装成已回滚。
- operation、终态和产物默认保留七天；服务重启把未结束任务明确结算为 `interrupted`，不会自动重放。
- Windows 执行端同步发布新二进制。本机仍是工作区路径守卫加固定 argv，不是 OS 强制沙箱，因此没有开放任意 shell。

### 3. UI / GenUI / EduWork

- PPTX 提取按真实 `slideN.xml` 顺序保留幻灯片边界，`SourceRef` 增加 locator 与起止幻灯片；页号由服务端提取结果产生，不由模型猜测。
- 课件原文片段可导出为包含文档 ID、修订、SHA-256、完整 SourceRef 和原文的 Markdown 附件。
- 新增 `study_surface_save`：模型可把有精确 SourceRef 的阶段总结、进度和后续问题保存为会话内持久面板，并以 `surface_id + expected_revision` 更新，避免重复创建和覆盖冲突。
- 课件插件停用时学习面板工具也不注入。PDF/扫描件定位以及独立答题、评分、错题回执仍是后续能力，本版不把展示面板冒充完整学习档案。

### 4. QQ / AstrBot 联动

- AstrBot 状态页显示最近已验签入站、插件领取轮询、发送结果回执、实际平台与 owner 绑定检查。
- 缺任一真实链路证据时明确列出问题；“启用开关”不等于 Ready，Ready 也不替代平台侧消息送达验收。
- 生产网关继续默认关闭。真实 `AstrBot + aiocqhttp` owner 私聊的绑定、连发、回复、重启/unknown 和解绑仍是外部验收缺口。

## 最小验证

- `go build -buildvcs=false ./...`、`go vet ./...` 通过。
- `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -buildvcs=false ./...` 与对应 `go vet ./...` 通过。
- 受影响包 `internal/localagent`、`internal/courseware`、`plugins/builtin/courseware` 和完整 `internal/api` 测试通过。
- 受影响网页脚本均通过 `node --check`；`git diff --check` 通过。
- Android `assembleDebug` 通过；APK 元数据为 `0.7.18-debug` / versionCode `56`，签名有效且证书延续。
- 线上零模型只读验收：服务 active，`NRestarts=0`；版本 0.7.18；PWA `chat-shell-v54`；连续 Harness 为 true；4 个会话、103 条消息、35 个 turn 可读；active/running/queued/paused/unavailable 均为 0；scheduler、网页搜索和网页读取正常；三个外部 IM 网关均关闭；最近十分钟无 warning 级服务日志。

## 发布凭据

| 项目 | 值 |
|---|---|
| 服务 release | `/opt/chat-agent/releases/20261117-four-slices-02` |
| Linux SHA-256 | `972df1fb7348573d8f81e3788f30ac72f50d99b6eba62b9933c3b2e7b0b72efe` |
| Windows local-agent SHA-256 | `c2376c3bc4d84bc963515f716c6be5345cc0dc8d2dca99fe143085e02fd39167` |
| Android release | `/opt/chat-agent/android/releases/android-20261117-01` |
| Android 版本 | `0.7.18-debug` / versionCode `56` |
| APK SHA-256 | `dfd1e61c167f8f44cbf93e777fa6d0e573125b87905688ef01c713b1ce41aeb6` |
| APK 大小 | `1,229,031` bytes |
| 包名 | `dev.chatagent.ai.debug` |
| 签名证书 SHA-256 | `4905b966d06c53d22da462b9ef3e36ff27b54994829950a610f92a6bab68b24a` |
| 升级前备份 | `/opt/chat-agent/backups/pre-four-slices-20261117-01.tar.gz` |
| 备份 SHA-256 | `a4a554011b95573fe7d937feecd203285b799cf2d6993e8cd8520aef524ff259` |

## 回退边界与命令

本版没有不可逆存储迁移。新增 operation 保留字段可被 0.7.17 忽略；课件 locator、PPTX 文本标记和学习面板沿用兼容 JSON/持久 surface 格式；主动反馈和 AstrBot 诊断没有新增持久 schema。因此可只切回上一服务二进制。Android 已安装 code 56 的设备不会自动降级。

```sh
systemctl stop chat-agent
ln -sfn /opt/chat-agent/releases/20261116-four-slices-01 /opt/chat-agent/current.next
mv -Tf /opt/chat-agent/current.next /opt/chat-agent/current
find /var/lib/chat-agent -name '*.lock' -delete
systemctl restart chat-agent

ln -sfn /opt/chat-agent/android/releases/android-20261116-16 /opt/chat-agent/android/current.next
mv -Tf /opt/chat-agent/android/current.next /opt/chat-agent/android/current
```

若需要同时丢弃 0.7.18 上线后的全部数据变更，可停服并另存当前状态，再把 `/opt/chat-agent/backups/pre-four-slices-20261117-01.tar.gz` 解包到 `/`，清理 `*.lock` 后启动。恢复备份会丢失升级后的聊天和其他数据，只应在明确需要时使用。
