# 决策模型与图像生成

本轮新增两个能力，都遵循同一条原则：**给模型的是能力，不是权限**。

## 一、`decide`：向决策模型求助

### 是什么

平台提供 `POST /open/api/v1/systemone`（模型 `ecnu-decision`），底层是 vLLM 部署的
D3（d3-flash）。它不做自然语言生成，直接返回**每个选项的概率分布**：

- `choice`：分类，返回 `choice` + `probabilities` + `confidence`
- `noul`：是否成立，返回 `noul`（成立概率）
- `score`：有序评分，返回 0 基**期望档位** `score` + `legend`

不做提示词工程、不需要解析输出——标签口径由 `criteria` 直接声明。
当前**限时免费**，不扣 credits。单请求最多 64 题，单题上下文 16384 token，超限直接报错不截断。

### 怎么接的

`internal/decision`（客户端）+ `internal/api/decision_tools.go`（工具）。

工具名 `decide`，一次最多 8 题（远低于接口上限，避免把一轮对话变成批处理任务）。
参数是数组结构，因为工具 schema 只支持固定属性，不支持 `additionalProperties` 作为 schema：

```json
{"state": "…材料…", "questions": [
  {"id": "mood", "type": "choice", "instructions": "情绪倾向",
   "labels": [{"id": "positive", "description": "积极"}, {"id": "negative", "description": "消极"}]},
  {"id": "urgent", "type": "score", "instructions": "紧急度", "bands": ["不紧急", "一般", "紧急"]}
]}
```

返回按 id 排序的紧凑结果，并附带一句固定的边界说明：
**「概率判断仅供参考，不是用户授权；置信度低时不要强行采用。」**

### 什么时候该用

工具描述里写死了边界：

- **该用**：判断有固定答案集合，而且用户没让你自己拿主意——路由、是否成立、紧急程度、归类
- **不该用**：开放问题、创作类选择、用户想听你自己的看法

这不是通用推理捷径。它买的是**校准过的概率**，用于窄的一类问题。

### 失败处理

- 连续 2 次失败开 60 秒熔断，避免每轮都等超时
- 4xx 视为服务健康（请求被拒），5xx 才计入熔断
- 错误体兼容网关的 `detail` 数组和后端的 `error.message` 两种形状

### 配置

| 变量 | 默认 | 说明 |
| --- | --- | --- |
| `CHAT_BASE_URL` | — | 复用现有平台地址 |
| `CHAT_API_KEY` | — | 复用现有令牌 |
| `CHAT_DECISION_MODEL` | `ecnu-decision` | 不继承对话模型；用对话模型回答决策请求是配置错误，不是回退 |

未配齐时 `New()` 返回 nil，**工具根本不会出现**在工具目录里。

## 二、`image_generate`：本地画图

### 走的是哪条路

本机的 `E:\IAST\image-generation\agent-bridge` 是一个**受控桥**，不是裸 ComfyUI：

- 它持有 `profiles.json` 白名单，自己把画布 workflow 转成 ComfyUI API prompt
- 不加载模型、不下载文件、不执行 agent 给的任意 workflow JSON
- 拒绝并发作业，按 profile 校验尺寸与空闲内存
- 产物复制到 `outputs/agent/<job-id>`

所以 agent 侧**只调 CLI**，从不直连 8188，也从不提供 workflow。
「用哪个模型体系」是配置决策，不是模型能选的东西。

### 可用档案（本机实测）

| profile | 说明 | 内存门槛 |
| --- | --- | --- |
| `wai_v17` | WAI Illustrious SDXL v17 | 6 GB |
| `animer_miaomiao` / `z_anime_d8` / `krea2_draft` / `z_image_base_q5` | 二次元向 | 8 GB |
| `nanosaur2` | 低门槛 | 4 GB |
| `qwen21` | — | 12 GB |
| `llada_t2i` / `llada_edit` | 大模型 | 18 GB |

白名单由 `CHAT_IMAGE_PROFILES` 决定，工具 schema 的 `enum` 直接反映它，
所以模型看到的就是真实可用的档位。

### 产物怎么回到对话里

生成的图片**存进会话媒体库**（`media.Store.Upload`），工具返回：

```json
{"image_id": "…", "url": "/api/sessions/<sid>/images/<id>", "profile": "wai_v17", "seed": 123,
 "note": "图片已附加到本会话，用户可以直接看到；不要粘贴这个 URL。"}
```

模型拿到的是**引用**，不是它能伪造的路径。图片路由需要鉴权，
所以前端必须走 `request()` 取 blob，不能直接用 `<img src>`。

### 实测

```
profile=wai_v17 512x512 steps=8  →  11 秒，320 KB PNG
```

ComfyUI 0.37.0 / PyTorch 2.13.0+cu132 / RTX 4060 Laptop 8 GB，`--lowvram` 模式。

### 配置

| 变量 | 说明 |
| --- | --- |
| `CHAT_IMAGE_PYTHON` | 跑桥的解释器 |
| `CHAT_IMAGE_BRIDGE` | `imgctl.py` 路径 |
| `CHAT_IMAGE_PROFILES` | 逗号分隔白名单 |
| `CHAT_IMAGE_PROFILE` | 默认档案 |

三项缺一即禁用。**服务器上没有 ComfyUI，所以线上环境不配这些变量，工具自动不出现**——
这是刻意的：宁可少一个工具，也不要让模型看到一个必然失败的能力。

## 三、顺带修的两个问题

1. **`agent-bridge` 的 `health` 命令崩溃**：`nvidia-smi` 返回 `53.36` 这样的浮点温度，
   代码却做 `int(values[5])`，直接 `ValueError`。已改成先 `float` 再取整，同时容错
   其他非整数字段（利用率也会出现小数）。
2. **ComfyUI 进程已退出**：`comfyui.err.log` 显示它之前正常跑完过作业，但进程不在了，
   端口 8188 无监听。已重新拉起并确认 `/system_stats` 正常。
