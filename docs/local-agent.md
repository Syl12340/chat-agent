# 本机执行端（local-agent）

Windows 侧的工作区执行端。服务器的模型可以把任务交给它，它只在一个固定目录里
读写，命令只能从这个目录外的一份**固定 argv 清单**里选。

它不是操作系统沙箱：`os.Root` 挡住路径逃逸，动作清单挡住任意命令，但进程本身
没有 Landlock 之类的隔离。汇报里 `guard.os_sandbox` 永远是 `false`，就是说这个。

## 两种运行方式

### 1. 标准输入（不需要服务器）

任务以 JSON Lines 从 stdin 读，回执写到 stdout。用来验证清单和执行逻辑：

```bash
local-agent -workspace C:\work -actions actions.json -state C:\state.json -stdio
```

每行一个任务，回执一行一条。

### 2. 连服务器

```bash
local-agent -workspace C:\work -actions actions.json -state C:\state.json \
  -server https://chat.example.com -device <device-id> -token-file token.txt
```

轮询 `/api/local-agent/v1/tasks/claim`，执行，再 `POST .../complete`。
**服务器当前还没有这两个路由**，这条路径暂时跑不通。

## 动作清单

`-actions` 指向一份 JSON：动作名 → 固定 argv。第一个元素必须是**绝对路径**，
不经过 PATH 解析，也不经过 shell。

```json
{
  "go_version": ["C:\\Program Files\\Go\\bin\\go.exe", "version"],
  "list_dir": ["C:\\Windows\\system32\\cmd.exe", "/c", "dir", "/b"]
}
```

模型只能选名字，拼不出命令行。清单外的名字直接拒绝：
`action is not in the local allow-list`。

## 任务格式

```json
{"protocol":"ca.local-agent.v1","operation_id":"op_read_1","kind":"read","path":"hello.txt"}
```

- `kind` 是 `list` / `read` / `search` / `exec` 之一。写错会被告知全部四个取值。
- `operation_id` 必须以 `op_` 开头。
- `exec` 用 `action` 字段指清单里的名字，**不接受参数**。
- 操作 ID 是幂等的：同样的 ID 加同样的请求体再来一次，直接返回上次的回执；
  同样的 ID 换个请求体会被拒（`operation identity was already used for a different task`）。

## 回执里的 guard

```json
{"guard":{"kind":"workspace-guard","os_sandbox":false,"privilege_escalation":false,
          "shell":false,"workspace":"C:\\work"}}
```

`shell: false` 是实情：这里从不调 shell，`cmd /c` 也是清单里写死的一条 argv。
