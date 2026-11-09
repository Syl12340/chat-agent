# 服务器本地搜索

2026-10-27 已部署并接入 `ecnu-max` 的 `web_search`。手机刷新原连接即可使用，不需要重装 APK，也不需要搜索 API Key。

```text
Android → ChatWire → Agent / ecnu-max
                         ↓ web_search
               127.0.0.1:9993/search
                         ↓ HTTPS
                 cn.bing.com 公开 RSS
```

本地运行的是包装服务，搜索索引来自 Bing，搜索词会发送给 Bing；不读取服务器文件，也不建立离线全网索引。实现只用 Go 标准库，没有安装 Node 包、容器或外部 Agent 项目。

## 接口

| 接口 | 用途 |
| --- | --- |
| `GET http://127.0.0.1:9993/healthz` | 本机健康检查，不返回配置 |
| `GET http://127.0.0.1:9993/search?q=...` | Agent 使用的 JSON 搜索 |
| `POST http://127.0.0.1:9993/mcp` | MCP JSON-RPC 入口 |

除健康检查外，都要求 `Authorization: Bearer <独立搜索口令>`。JSON 返回 `results`（标题、HTTPS URL、摘要）、`source=bing-rss`、`cached` 和 `fetched_at`。Agent 直接使用 JSON，MCP 复用相同搜索核心与缓存，没有额外引入通用 MCP 客户端。

MCP 提供 `initialize`、`notifications/initialized`、`ping`、`tools/list`、`tools/call`，只暴露 `web_search`，参数为 `{"query":"搜索词"}`。采用 stateless Streamable HTTP，返回 JSON；不分配 session、不提供独立 SSE 流，GET /mcp 返回 405。验证版本为 `2025-11-25`，兼容这里使用的 `2025-03-26` 消息，不宣称覆盖 MCP 全部功能。

初始化后，调用请求应带 `Content-Type: application/json`、`Accept: application/json, text/event-stream`、`MCP-Protocol-Version: 2025-11-25` 和上述鉴权头。示例请求体：

```json
{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"web_search","arguments":{"query":"Go net/http official documentation"}}}
```

## 配置与边界

- `/etc/chat-search/search.env`：root:0600，保存单独生成的 `CHAT_SEARCH_TOKEN`。
- `/etc/chat-agent/chat-agent.env`：增加 `CHAT_ENABLE_WEB=1`、`CHAT_LOCAL_SEARCH_URL=http://127.0.0.1:9993/search`、匹配的 `CHAT_LOCAL_SEARCH_KEY`。
- 两个口令字段对应同一个搜索专用随机值，与网页登录口令、ECNU Key、ChatWire 私钥独立；不写入 APK，不发给 Bing。
- 本地例外只允许明确的 `http://127.0.0.1:<port>/search`，使用独立 client、禁止代理和重定向，不放宽网页抓取的私网限制。不能同时配置外部 `CHAT_SEARCH_URL`。
- 默认内存缓存 5 分钟、最多 128 个查询；上游未缓存请求最多 20 次/分钟、同时最多 2 个，期限 6 秒，响应最多 512 KiB。
- 查询最多 1024 UTF-8 字节，结果最多 5 条；标题最多 512 字节，摘要最多 2048 字节。不执行网页脚本、不逐个抓取结果目标；返回链接未被逐一验真，不能作为工具授权。
- 失败不缓存、不自动重试或切换收费服务。HTTP 返回错误状态，MCP 返回 `isError=true`，错误页不会冒充零结果。不记录查询访问日志。
- 服务仅绑定回环，拒绝非本地 Host、外站 Origin 和错误口令。MCP 传输与 Origin 规则参考 [MCP 官方规范](https://modelcontextprotocol.io/specification/2025-11-25/basic/transports)。

## 维护

`chat-search` 是独立无登录账户，systemd 开机自启，内存上限 128 MiB。本次空闲 MemoryCurrent 约 6.2 MiB，不是负载峰值。

```sh
systemctl status chat-search chat-agent --no-pager
journalctl -u chat-search -n 30 --no-pager
curl --fail http://127.0.0.1:9993/healthz
```

缓存与频率通过 `chat-search` 启动参数 `-cache-ttl 5m`、`-upstream-per-minute 20` 调整，建议使用 `systemctl edit chat-search` 的 ExecStart override；修改后 daemon-reload 并重启。变更搜索口令需同步两个环境文件并分别重启。

Agent 与搜索版本均为 `20261027-search-01`。Agent 启动依赖位于 `/etc/systemd/system/chat-agent.service.d/20-local-search.conf`。切换前环境备份为 `/etc/chat-agent/chat-agent.env.before-local-search`（0600），旧应用版本和数据保留。临时关闭联网可设 `CHAT_ENABLE_WEB=0` 后重启 Agent；完整回滚需恢复匹配版本/配置并移除搜索依赖 drop-in。

## 最小验证

只运行受影响的 localsearch、webtools、api 三包，共 5 项检查：新增 1 项搜索/MCP 流程，扩展已有联网边界，没有重复完整矩阵。

服务器实测：未认证搜索 401；真实 RSS 查询返回 5 条结果；MCP 初始化、发现和调用通过，相同查询复用缓存；`ecnu-max` 调用 1 次 web_search 并完成带 HTTPS 来源链接的回复。该模型工具循环消耗少量真实模型额度。

两个服务 active、开机自启、自动重启计数均为 0；9993 仅监听 127.0.0.1，9990/frps、9999/Node、9992/ChatWire 保持原用途。

Bing RSS 是本次实际可用的公开网页接口，没有签约 API 的稳定性承诺，可能调整格式、限流、拒绝请求或返回相关性不足的结果。没有绕过验证码/封锁。此前 Exa/Tavily 匿名 MCP 查询受到额度限制；本地服务不依赖它们，也未注册账户、付款或提交解锁问卷。
