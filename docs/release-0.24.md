# 0.24.0 Harness 3C runtime 迁移与恢复

2026-11-07。状态：**服务端已部署**。线上版本为 0.24.0 / `20261107-runtime-v2-01`；Android 没有原生改动，更新通道仍为 0.21.2-debug / versionCode 20。

## 已实现

- 新增 `internal/runlog.Registry`。`format-index.json` 先登记会话，严格 manifest 再选择唯一 `g-<generation>`；读取器不扫描“最新目录”，不回退旧 transcript/turns。已登记但 manifest 缺失、删除未结算或切换未完成时进入维护态。
- 当前代以 `history.json` 和 `turns.json` 保存规范聊天/队列状态，`events.jsonl` 保存 attempt/model/tool 运行与结算事实；snapshot/payload 继续使用同步、SHA-256 引用和 fail-closed writer。队列 worker 与兼容流式入口都创建 recorder。
- `-migrate-runtime` 只在服务停止后显式执行。它验证 transcript v1/v2 和 turns v1/v2/v3，保留角色绑定、reset、summary、图片引用、模型/思考参数、完整 reasoning、部分进度、互动 envelope 与富文本原文；queued 保持暂停，running 变为 interrupted，不调用模型或工具。迁移成功后移除在线旧源。
- 维护命令会把新 runtime 树属主对齐到数据根属主。服务启动发现未迁移旧会话或未完成维护时拒绝运行，不偷偷转换。
- 最新合并轮硬删先写非正文删除登记，再构建无目标消息和整批 request IDs 的新代；不复制旧事件、payload 或模型快照，并删除全部在线旧代。成功响应返回实际删除的 request IDs。整会话删除同样先登记，再清 runtime、旧源和图片；删除 ID 不复用。
- `deletions.json` 与 format index 均上限 100000、无自动过期。中断清理由同一停机维护命令续作，不自动恢复被删正文。
- `-export-runtime <新目录>` 可在停机状态生成独立 transcript-v2/turns-v3 会话与图片兼容副本。runtime events、attempt 与 unknown 结算仍保留在源档案，旧格式不能表示这些事实。

实现借鉴 DeepSeek Harness（MIT）的 manifest 选代、运行事实与实时展示分离、unknown 不重放及删除重写思路；Go 侧独立实现，不嵌入 DSH/Cordis 运行时或插件。

## 最小验证

- `TestMigrationRestartProjection`：显式迁移后关闭/重开 registry；聊天文本、展示 reasoning 与 turns 状态可恢复，running 只转 interrupted；兼容导出可重新产生 transcript/turns 文件。
- `TestHardDeleteRewritesGeneration`：最新轮整批 request 和消息被删除，旧代及派生 snapshot 不存在；剩余历史/turns 可重开；整会话删除后 manifest 不可读、同 ID 不可复用。
- `TestMigrationSwitchFailureIsExplicit`：在 format index 登记后注入切换失败，旧源仍保留且在线读取明确为 maintenance；再次运行维护命令完成发布和旧源清理。
- 相关 `internal/runlog`、`internal/turns`、`internal/probe`、`internal/api` 回归通过；`go vet ./...`、`go build ./...` 与 Linux amd64 构建通过。没有调用真实模型。

## 生产切换

| 项目 | 结果 |
| --- | --- |
| 服务目录 | `/opt/pocket-agent/releases/20261107-runtime-v2-01` |
| 上一版本 | `/opt/pocket-agent/releases/20261106-continuity-ui-01` |
| Linux amd64 SHA-256 | `d04d0359a0c29d6d1b5ef03b2409ca46c734b18ef03e3d54190045969f2971a0`，线上一致 |
| 一致性备份 | `/opt/pocket-agent/backups/pre-runtime-v2-20261107-01.tar.gz`，SHA-256 `8ceba7a373519c8e1257dd4daae980e985460b04b142e8f03e4a7d29d9818830`，`root:root` / 0600 |
| 迁移结果 | `migrated=1 resumed=0 skipped=0`；1 个 active generation 1，旧在线 transcript/turns 已移除 |
| Android | 0.21.2-debug / versionCode 20，清单 SHA-256 `3929c00d6630660c68f2c5e07ada242b9497ebe1fe95482dbe617548145dd295` |

切换前线上为 1 个会话、20 条消息、12 条轮次，running/queued 均为 0。正常停服且所有 store/session lock 释放后创建备份，再执行显式迁移。迁移后同样为 20 条消息、12 条轮次，队列未暂停；没有新增模型请求、消息、工具执行或业务写入。

首次启动暴露了一个发布权限问题：迁移由 root 执行，新 `runtime/` 初始为 `root:root` / 0700，受限的 `pocket-agent` systemd 用户无法创建 registry lock。服务未读取或修改会话即退出。停掉重启循环后只将已验证的 runtime 树改为 `pocket-agent:pocket-agent`，清除 systemd rate-limit 状态并重启成功；随后将这一行为固化为维护命令的“对齐数据根属主”，重新构建并原子替换最终二进制。最终 runtime 目录为 0700、索引为 0600，属主均正确。

最终零模型检查确认：`/healthz` 正常，未认证状态接口为 401，`/api/status` 为 0.24.0 / ecnu-max，scheduler、web search/fetch 正常；会话 20 条消息和 12 条轮次可读，active/running/queued/unavailable 均为 0；9991 回环 HTTP 与 9992 加密 TCP 均监听。本机 SSH 隧道恢复为 `http://127.0.0.1:60991`，健康和网页入口为 200。

## 回退边界

- 切换后不能只把软链指回 0.22：旧二进制不知道 runtime v2。立即回退须停止服务，保留当前 runtime 现场，恢复整份 `pre-runtime-v2-20261107-01.tar.gz`，再切回旧发布目录。
- 整份备份回退会丢失备份后产生的新会话和业务修改。已有 0.24 新写入时，优先保留 runtime 现场并使用停机兼容导出处理可表示的会话；events、attempt、unknown 和新增业务状态不能靠旧格式无损表达。
- 删除登记必须与 format index 一起保留；缺少当前登记的旧备份不能自动合并上线。服务端硬删也不能追溯删除已下载通知、用户导出或部署备份。

本版没有修改模型密钥、设备白名单、防火墙、角色长期记忆、Android 包或公网端口，也没有把会话正文带离服务器。目录替换只同步文件本身，仍不宣传为断电级目录持久性或所有工具 exactly-once。
