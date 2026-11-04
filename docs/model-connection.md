# 真实模型接通记录

2026-10-27，服务器 `47.117.40.74` 已切换为 **live 模式**。

| 配置 | 当前值 |
| --- | --- |
| 接口协议 | OpenAI-compatible Chat Completions / SSE |
| Base URL | `https://chat.ecnu.edu.cn/open/api/v1` |
| 聊天模型 | `ecnu-max` |
| 客户端入口 | `pocket://47.117.40.74:9992` |
| 模型密钥 | 仅服务器 `/etc/pocket-agent/pocket-agent.env`，root:0600 |

通过认证后的 `/models` 查询确认可用聊天模型包括 ecnu-max 和 ecnu-plus。最初验证 ecnu-plus 后，已按用户要求切换为 ecnu-max，并验证时间工具循环及[本地搜索](local-search.md)。没有将模型密钥写入仓库、APK、网页或日志，也没有替换网页登录访问口令。

## 最小验收

只执行一次真实业务流程：通过本项目 HTTP API 创建新会话，要求模型调用 `get_current_time`，接收真实服务端时间工具结果，再生成最终回复。

- 当前状态接口 `demo=false`，模型为 `ecnu-max`；两种模型均已完成时间工具验证。
- 收到 1 次 `get_current_time` 工具开始事件和 1 次成功工具结果。
- 收到非空模型回复及 `done: {ok:true}`，SSE 正常结束。
- 服务 active，自动重启计数为 0，提醒调度健康。
- 没有自动重试、自动切换模型或运行完整测试矩阵。该流程消耗了少量真实模型请求额度，未查询余额或计费明细。

原演示目录 `/var/lib/pocket-agent/demo-sessions` 保留；真实目录为 `/var/lib/pocket-agent/sessions`。切换后客户端刷新或重新连接，开始新会话即可，无需重装 APK。若旧页面仍选中了演示会话，应返回会话列表或新建会话。

## 维护

服务器环境配置已保留原访问口令和 PocketWire 私钥路径。切换前配置备份在 `/etc/pocket-agent/pocket-agent.env.before-live`，权限 0600；验证结果摘要在 `/etc/pocket-agent/provider-verification.json`，不包含密钥。密钥修改后需重启 `pocket-agent`，客户端不需要知道模型密钥。

当前使用用户指定的 ecnu-max，不自动回退或更换模型。角色服从、长期记忆质量、摘要效果、配额和长期可用性不属于本次最小接通验证的结论。
