# 插件化架构

## 2026-11-09 目标与实现边界更新

“万物皆插件化”已成为用户确认的主线，统一按[推进目标](product-direction.md) M0–M4 落地。业务能力、Provider、提示分区、上下文、触发与呈现逐步纳入插件；身份、授权、会话和结算保留统一核心。当前仅有受信 Go builtin 路径；工作区代码存在不代表全部已发布验证。

现实现 `toolCapabilities()` 使用 `AllowAll()`，`newPluginHost` 会从 manifest 填文件系统允许集合。下文“权限是声明，不是授权”是目标契约，尚须补宿主 grant 与本轮/执行端权限交集；在此之前不开放通用执行插件。进程内插件不是安全沙箱。

首批迁移搜索/图像/决策，继而执行端、只读运维和资料能力，再迁记忆/活动/陪伴等有状态业务。Linux 沙箱、本地 worker 和运维代理共享任务/回执契约，但权限域分开；现有 L2/L3 不支持状态不能仅靠修改 manifest 放行。

状态：第一阶段实现中。本文描述已落地的部分与后续路线，不描述尚未存在的代码。

## 1. 目标

把 chat-agent 从「一个把所有能力写进 `internal/api` 的聊天应用」推进为
「一个小内核 + 一组可替换能力」。

一句话原则：

> **插件决定自己能做什么，Core 决定它什么时候能做、能对谁做、做完怎么算数。**

## 2. 四层

```
应用层   Web / Android / OneBot
─────────────────────────────────────
核心层   Identity · Conversation · Harness · Scheduler · Permission · Plugin Manager
─────────────────────────────────────
能力层   Search / Memory / Companion / Activities / Generative UI / Media …
─────────────────────────────────────
基础设施 Filesystem · Model Provider · Network · Storage
```

Core 只协调，不实现业务。能力以插件交付。

## 3. 三级插件体系

| 级别 | 形态 | 状态 |
| --- | --- | --- |
| L1 Built-in | 进程内 Go 包，静态注册 | **已实现** |
| L2 Service | 独立进程，socket RPC | 未实现，manifest 会拒绝 |
| L3 Sandboxed | WASM / 容器 | 未实现，manifest 会拒绝 |

`spec.runtime` 写 `service` 或 `sandbox` 时，加载会直接报
「尚未实现，当前只支持 builtin」——**宁可明确拒绝，也不要假装支持**。

不用 Go 的 `plugin` 标准库：动态加载、编译版本兼容和卸载能力都不够。

## 4. Manifest

格式是 **JSON**，不是 YAML：本项目守 Go 标准库，没有 YAML 解码器。
结构与 `plugin.yaml` 完全一致，要换格式写个转换器即可。

```json
{
  "apiVersion": "chat-agent.dev/v1",
  "kind": "Plugin",
  "metadata": {
    "id": "builtin.search",
    "name": "Web Search",
    "version": "0.1.0"
  },
  "spec": {
    "runtime": "builtin",
    "capabilities": ["search.web", "search.fetch"],
    "permissions": { "network": ["public-https"] },
    "tools": ["web_search", "web_fetch"],
    "provides": ["search.client"]
  }
}
```

### 校验规则（`Manifest.Validate`）

- `apiVersion` 必须精确匹配，未来版本不会静默通过
- `metadata.id` 必须是点分小写（`builtin.search`）
- `metadata.version` 必须是 `x.y.z`
- 能力名必须点分（`search.web`），不允许裸词
- `spec.tools` 里的工具名必须 `[a-z][a-z0-9_]*`
- `permissions.filesystem` 只能是逻辑命名空间（`memory://role`），
  绝对路径和 `..` 一律拒绝
- 插件不能依赖自己提供的服务

### 权限是声明，不是授权

Manifest 里写 `network: ["public-https"]` **不代表**真的开了网络。
宿主通过 `Host.Allowed(permission)` 重新判断，插件不能把 manifest 当许可。

## 5. 扩展点与生命周期

```go
type Plugin interface {
    Manifest() Manifest
    Register(*Registry) error   // 纯声明：不 I/O、不起协程
    Start(context.Context, Host) error
    Stop(context.Context) error
}
```

`Register` 阶段能声明的只有 `Tool` 和 `Service`。

**关键约束**：`Registry.Tool` 会核对 manifest——

- 注册了没声明的工具 → 报错
- 工具引用了没声明的能力 → 报错
- 参数不是合法 JSON → 报错

这样 manifest 不会高估能力，模型不会被塞一个幻影工具。

### 依赖不满足就不加载

这是从 Cordis（DeepSeek Harness，MIT）借来的语义：

```
插件 A 依赖 search.client
没有任何插件提供 search.client
→ A 进入 Catalog.Unavailable，理由写明缺哪个服务
→ 其余插件照常加载，进程照常启动
```

**缺依赖不是启动失败**。一个能力缺失不该让整个服务起不来。

## 6. 安装 ≠ 暴露

三个层次必须分开：

```
安装的插件目录  →  Catalog.Tools()
                    ↓ Admit(Enablement)
本轮允许的能力  →  Admission.Tools
                    ↓
模型能看见的工具
```

已经落地的部分：`Catalog.Admit(enable)` 按 capability 过滤，
`Catalog.Tools()` 始终保留全集——过滤一次准入不会从目录里删东西。

执行工具仅在有已授权工作区和可用执行端时暴露；由自然语言任务和宿主授权判断，不要求用户切换“聊天/工作”模式。工作区执行与服务器管理是独立权限。

## 7. 与 Harness 的关系（不可妥协）

插件注册的工具**必须**走现有执行链：

```
Catalog.Admit → probe.Tool → ToolRegistry → ToolSet → ToolGuard
              → RunRecorder → ToolResult
```

唯一的接缝是 `internal/api/plugin_tools.go` 里的 `pluginTools()`：
它把 `pluginapi.Tool` 转成 `probe.Tool`，之后一切照旧。

插件**不能**：

- 自己创建聊天历史
- 自己实现一套 LLM 工具循环
- 自己维护一套权限和用户确认
- 直接写运行记录

## 8. 已迁移的插件

### `builtin.search`

`plugins/builtin/search`，包装既有的 `webtools.Client`：

- `web_search` → capability `search.web`
- `web_fetch` → capability `search.fetch`

manifest 按客户端**实际能力**生成：没配搜索端点就没有 `web_search`，
工具名和 capability 一起消失。未配置时 `New()` 返回 nil，插件根本不存在。

`internal/api` 里原来的 `webTools()` 和 `internal/api/tasks.go` 里的
150 行硬编码已删除。Core 不再知道这两个工具存在。

## 9. 踩过的坑

**typed nil 装进接口不等于 nil**。`search.New()` 在未配置时返回零值，
`Loader.Add(nil-plugin)` 的 `if plugin == nil` 拦不住它，`Load` 时
`plugin.Manifest()` 直接 panic。

`Loader.Add` 现在用反射挡：指针/映射/切片/函数/通道类型且 `IsNil()` 的一律忽略。
构造函数也应返回接口类型（`pluginapi.Plugin`），让 nil 就是真 nil。

## 10. Android 壳的边界（能力协商）

APK 是 WebView 壳，原生层必须保持**极薄**，判断标准只有一条：
**需要系统权限的才进原生**。

| 职责 | 归属 | 原因 |
| --- | --- | --- |
| UI、交互、业务逻辑 | 网页 | 改完部署即生效，无需重装 |
| 设备密钥签名 | 原生 | 需要 Android Keystore |
| 系统通知 | 原生 | 需要 Notification 权限 |
| 文件选择 | 原生 | 需要 SAF |
| 服务器地址 / 错误兜底 | 原生 | 网页还没加载出来时也要能用 |
| 装包 | 原生 | 需要 PackageInstaller |

原生顶栏已删除，`刷新 / 更新 / 服务器` 移到网页侧栏，通过 `shell_*` 通道回原生。

### 能力协商而不是版本匹配

新增 `web/native-capabilities.js`。原生注入显式清单：

```js
window.PocketAndroidCapabilities() // { version: 25, features: [...] }
```

旧壳没有这个函数时，按注入对象反推（`version: 0`）。

页面据此**降级**而不是报错：

| 能力缺失 | 行为 |
| --- | --- |
| `voice` | 隐藏语音按钮，不提示升级 |
| `files.picker` | 提示「这台客户端还不能上传图片」 |
| `care` | 说明「还不能发系统通知」 |
| `preferences` | 回退到 localStorage |
| `shell` | 侧栏不显示连接区 |

写死的「请更新 Android 客户端」这类文案不再是缺省行为。

## 11. 路线

| 阶段 | 内容 | 状态 |
| --- | --- | --- |
| P0 | Manifest、Plugin API、静态 Registry、Capability Catalog、首个插件 | **已完成** |
| P1 | Clock / Model Provider / OneBot / Push / Media 插件化 | 未开始 |
| P2 | Memory / Activities / Companion / Topics / Generative UI | 未开始 |
| P3 | Workspace Executor / Operator / DSH 集成 | 未开始 |
| P4 | 安装卸载、版本兼容、信任策略 | 未开始 |

**不要先迁 Memory 和 Companion**：它们与持久化格式、角色隔离、遗忘机制、
主动陪伴状态绑得很紧，应该先验证 API 足够表达这些复杂业务。

## 12. 明确不做的

- 嵌入 DSH 运行时或 Cordis
- 运行时加载 `.so`
- 通用事件总线（先定义少量明确事件即可）
- 为插件化引入 SQLite / 消息队列

---

借鉴说明：插件生命周期与依赖语义参考 DeepSeek Harness 的 Cordis 框架（MIT），
Go 侧独立实现，未复用其代码。
