# 0.4.0 统一助手、模型配置与受限工作区

2026-11-09。状态：**服务端与 Android 更新通道已发布**。

- 源码功能提交：`500322859e46412d0c266989b2169714c2e130f3`
- 服务端：`0.4.0` / `20261109-unified-01`
- Android：`0.4.0-debug` / versionCode `7` / `android-20261109-19`
- Android 包名：`dev.chatagent.ai.debug`

## 交付范围

- 已登记且批准的设备自动发起 challenge、签名并连接；等待批准时定期重试，显式退出后暂停自动连接。服务器持久化设备公钥指纹、状态和会话，不保存客户端私钥。
- 新会话统一使用 `companion`，网页删除角色选择；旧 `planner` 会话仅为历史兼容保留读取。
- 设备管理员可在连接设置中新增、修改和删除模型路由。API Key 只写服务器的 `model-configs/state.json`，列表和响应只返回 `configured`，每轮执行固定自己的路由快照。
- `workspace_shell` 支持 `read-only` / `workspace-write`，使用 Landlock、seccomp socket denial、资源限制和每次独立临时目录；隔离不可用时拒绝执行。高风险 shell 仍经过宿主决策/授权。
- `workspace_share` 返回绑定会话、路径和 SHA-256 的下载回执；点击下载时重新读取并校验，文件变化会拒绝，单文件上限 32 MiB。
- DSH 固定在 `fcc0ad462172522aaf5c93a7b2e01f6b9f0b0159` 参考工具契约、沙箱失败闭合和 API Key 边界。Aurabot 固定在 `0a0ccb1cc401dee00ce0faac2cffbf57d0efecda` 适配可选 `|||` 气泡分段；AstrBot `6b10af75e118714335aff3aab047bf7689fa1388` 仅作架构参考。来源与许可见 `THIRD_PARTY_NOTICES.md`。
- 修复暗色侧栏底部“访问已保护”区域的浅色背景。

## 构建与验证

- `go build ./...`
- `go vet ./...`
- `go test ./...`
- `GOOS=linux GOARCH=amd64 go build ./...`
- `GOOS=linux GOARCH=amd64 go vet ./...`
- Android `:app:testDebugUnitTest :app:assembleDebug`（离线缓存）
- 前端入口脚本通过 `node --check`

线上零模型验收确认服务 `active`、`NRestarts=0`、health 正常、`/api/status` 为 0.4.0，模型管理响应不含密钥，工作区插件 v0.2.0 已加载。目标机以 `chat-agent` 账号直接验证：允许的 shell 可启动，读取 `/etc/passwd` 被 Landlock 拒绝，Python 创建 socket 被 seccomp 拒绝。没有调用 ECNU 模型，也没有向工作区写入测试文件。

## 产物、备份与回退

- Linux amd64 SHA-256：`c0ab716b25651ac906582a41214eee9b7282ff7a62417d7c556922738c5e64ca`
- APK SHA-256：`335498e562cb9b85fd0a4b446bce0e53433afa4234ca1abdd02510ec7c1e04e9`
- 部署前一致性备份：`/opt/chat-agent/backups/pre-unified-20261109-01.tar.gz`
- 备份 SHA-256：`887fff14613e6e10022d2e3f0c347c99af50eefa804c8646434577a99944c891`

本版没有迁移已有 runtime/turns/life/topics 格式。仅回退程序与 Android 更新通道：

```sh
ln -s /opt/chat-agent/releases/20261109-toolcard-01 /opt/chat-agent/current.next
mv -Tf /opt/chat-agent/current.next /opt/chat-agent/current
systemctl restart chat-agent
ln -s /opt/chat-agent/android/releases/android-20261109-18 /opt/chat-agent/android/current.next
mv -Tf /opt/chat-agent/android/current.next /opt/chat-agent/android/current
```

如果需要连同发布后业务数据一起恢复，先停止服务并单独保留当前数据，再使用上面的完整备份恢复；不要在服务写入期间覆盖数据目录。
