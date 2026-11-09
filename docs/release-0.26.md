# 0.26.0 主动陪伴统一会话

2026-11-08。状态：**服务端与 Android 更新通道已发布**。服务端为 0.26.0 / `20261108-followup-04`；Android 为 0.26.0-debug / versionCode 21 / `android-20261108-01`。沿用原包名、debug 签名、设备身份、加密 9992 入口和 `ecnu-max`；OneBot 仍默认关闭。

## 交付

- 自动关怀和共同话题不再生成一份与聊天分离的文本。宿主优先选择事件来源会话，否则选择最近的同角色、有人类消息的空闲会话；没有可接续会话时不创建空会话，也不调用模型。
- 新增一次响应、零工具的 host followup。它复用角色提示、近期会话和 Harness `RunRecorder`，但可信触发只进入临时 runtime snapshot，不伪装成用户消息、授权或写入证据。
- followup 最多读取最近 12 条、从真实用户边界开始的消息；后台轮不开放时钟、记忆、任务、活动、搜索或其他工具，也不继承聊天输入框的思考档位。
- 模型输出先进入不可见的 `delivering` 中间态，再 fsync 到目标会话，最后才成为 ready 的关怀或话题记录。会话写入失败会结算为 failed；进程在中间态退出则重启后变为 interrupted、清空绑定且不重试，因此不会留下“推送已送达、会话无消息”的假状态。
- 既有概率闸门、安静时段、共享额度、未读克制、话题正文漏斗和可选零输出保持不变。明确事件跟进不被随机沉默吞掉；共同话题仍只读公开网页且最多一次模型决策。
- Web 收件箱新增“回到这段对话”。自建推送携带会话 ID；Android 为每条通知使用独立 `PendingIntent`，点击后由原生壳通知网页打开并刷新对应会话。

## 数据与回退边界

`companion/life/state.json` 写入版本升至 v4，`topics/state.json` 写入版本升至 v2；旧 v1–v3 / v1 仍可读取，下一次相关写入才升级。`delivering` 允许在提交窗口暂存会话和索引，但不进入收件箱/推送；正常完成后转 ready，重启恢复则清空并转 interrupted。长期绑定只存在于 ready/seen。

一旦生产产生 v4 life 或 v2 topics 数据，0.25 及更早程序的严格 JSON 读取器不能安全接管。回退必须正常停服并恢复本次切换前的完整数据备份，不能只改 `current` 符号链接。Android 更新通道不通过降低 versionCode 回退。

## 最小验证

- 全仓 `go test ./... -count=1`、`go vet ./...`、`go build ./...` 通过；Linux amd64 与 Windows 构建通过。
- 关怀流程验证近期聊天上下文进入实际模型请求、assistant 消息与收件箱索引一致、推送保留目标会话；角色隔离和未审核内容过滤仍保留。
- 话题流程验证一次搜索、最多两次正文读取、一次模型决策、零输出不发布，以及 ready 消息与会话索引一致；life/topics 各有一条 prepared delivery 重启不假送达测试。
- `node --check` 覆盖变更的网页模块；Android `:app:assembleDebug` 通过，APK 元数据、签名和下载哈希均通过发布脚本校验。

测试使用临时 Provider，没有调用真实模型、真实搜索或生产聊天。没有新增第三方 Go/前端/Android 依赖。

## 生产发布

| 项目 | 值 |
| --- | --- |
| 服务目录 | `/opt/chat-agent/releases/20261108-followup-04` |
| Linux SHA-256 | `030760d41981aec036faf302259c5e32c28f4c37e50747eeeaa3d1e9397ba5ea` |
| 最终切换前备份 | `/opt/chat-agent/backups/pre-followup-20261108-04.tar.gz`；SHA-256 `144524cefca381655778397d2eabff4bb93035e95ee73c556396c27ed46c2b5e` |
| 0.25 回退备份 | `/opt/chat-agent/backups/pre-followup-20261108-01.tar.gz`；SHA-256 `a2b56c492df0d57fa92613384ac2d7fa0394665cc0228686ad138a78d87444a8` |
| Android 目录 | `/opt/chat-agent/android/releases/android-20261108-01` |
| APK | 948172 字节；SHA-256 `6b0632b33498a2cf55603dbcdc3deb189256775e886699a86bc7be0e53ef664a` |
| 签名证书 SHA-256 | `4905b966d06c53d22da462b9ef3e36ff27b54994829950a610f92a6bab68b24a` |

最终零模型检查确认：服务和本地搜索 active，`/api/status` 为 0.26.0 / ecnu-max，scheduler、web search/fetch 正常，PWA 为 `chat-shell-v29`，Android 清单为 versionCode 21；2 个会话、38 条消息、19 轮记录均可读，active/running/queued/paused/unavailable 均为 0。9991 回环 HTTP、9992 加密入口和 9993 本地搜索继续监听；未鉴权 API 为 401，OneBot 关闭态 webhook 为 404，近十分钟服务无 warning 日志。

本次没有发送生产聊天、触发关怀/话题、调用真实模型或修改已有消息。未做 Android 真机安装、通知点击和待机验收；自然表达质量仍需日常使用反馈。
