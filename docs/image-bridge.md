# 图像生成桥

## 作业接口

CA 的异步图像工具使用 imagebridge 1.1 的 `POST /jobs`、`GET /jobs/{id}/result` 和 `POST /jobs/{id}/cancel`。发布本轮 CA 时同步更新本机桥，可用 `.\scripts\tunnel.ps1 -Build` 重建后启动。旧的 `/generate` 供已有客户端使用。

CA 在派发前为每轮请求保存一个作业 ID，返回后从会话内查询。渲染器受理后保存独立的 renderer ID，取消按此 ID 转发。界面将取消回执与最终渲染状态分开。服务中断或提交回执丢失时保留待核对状态，原请求不会自动重放。完成记录包含 profile、seed、原始尺寸、原始产物哈希和会话媒体引用。

把本机的 GPU 接到服务器上的 chat-agent，服务器不需要 Python、不需要模型权重，也不开任何入站端口。

## 1. 拓扑

```
[服务器] chat-agent
   │  POCKET_IMAGE_REMOTE=http://127.0.0.1:18190
   ▼
127.0.0.1:18190            ← SSH 反向隧道（-R）
   ▼
[本机] imagebridge  127.0.0.1:18190
   ▼
imgctl.py ──▶ ComfyUI 127.0.0.1:8188
```

三段都是 loopback。服务器侧只有 `127.0.0.1` 能访问 18190，本机侧 bridge 默认也只绑 `127.0.0.1`。

## 2. 为什么不是直接转发 8188

ComfyUI 的 8188 是 **prompt graph API**：调用方要自己拼节点图、选 checkpoint、连采样器。
把这套协议暴露给服务器，等于把工作流知识搬进 chat-agent，且换来一个巨大的攻击面。

`imgctl` 已经把「一个 profile + 一句话」封装好了。桥只转发这一个请求形状：

```json
POST /generate
{"prompt":"...","profile":"wai_v17","width":512,"height":512,"steps":8}
→ {"job_id":"...","profile":"wai_v17","seed":123,"images":[{"mime":"image/png","base64":"..."}]}
```

工作流文件、模型路径、显存策略全部留在有 GPU 的机器上。

## 3. health 是给模型看的，不是用来屏蔽的

`GET /health`：

```json
{
  "ok": true,
  "comfy": true,
  "profiles": ["wai_v17", "nanosaur2"],
  "blocked": [
    {"name":"z_anime_d8","reason":"需要 8.0GB 空闲内存，当前 6.8GB","min_free_ram_gb":8},
    {"name":"llada_t2i","reason":"需要 18.0GB 空闲内存，当前 6.8GB","min_free_ram_gb":18}
  ],
  "default": "wai_v17",
  "memory": {"available_gb": 6.84, "total_gb": 31.22},
  "gpu": {"name":"NVIDIA GeForce RTX 4060 Laptop GPU","vram_used":5934,"vram_total":8188},
  "queue": {"running": 0, "pending": 0}
}
```

**关键决定：能力不可用时，工具不会被隐藏。**

宿主只负责如实报告，由模型决定怎么办——换个轻的 profile、等一会儿、还是直接告诉用户现在画不了。
如果宿主替它屏蔽，模型就没有解释的依据，用户只会看到「这个功能不见了」。

`image_generate` 和 `image_health` 始终注册；`blocked` 里的原因带具体数字，模型可以直接引用。

### 为什么 blocked 很重要

`profiles[].ready` 只表示**模型文件在磁盘上**。渲染器还会在提交前检查宿主内存，
不够就拒绝——这是实际最常见的失败。

早期版本只报 `ready`，结果 health 说 9 个都能用，实际上 7 个会失败。
现在按 `min_free_ram_gb` 与当前可用内存比较，分成「能跑」和「被挡」两组。

## 4. 启动

本机（Windows）：

```powershell
.\scripts\tunnel.ps1              # 起桥 + 建隧道，前台守护
.\scripts\tunnel.ps1 -Check       # 只检查连通性
.\scripts\tunnel.ps1 -Build       # 先重编 imagebridge
```

脚本会：
1. 起 `imagebridge`（绑 `127.0.0.1:18190`）
2. 建 `ssh -R 127.0.0.1:18190:127.0.0.1:18190`
3. 本机与服务器两侧各探测一次
4. 任一进程退出就重连

服务器（`/etc/pocket-agent/pocket-agent.env`）：

```
POCKET_IMAGE_REMOTE=http://127.0.0.1:18190
POCKET_IMAGE_PROFILE=wai_v17
```

改完 `systemctl restart pocket-agent`。

## 5. 后端选择

| 环境变量 | 效果 |
| --- | --- |
| `POCKET_IMAGE_REMOTE` | 走隧道到本机 bridge（GPU 在别处） |
| `POCKET_IMAGE_PYTHON` + `POCKET_IMAGE_BRIDGE` | 走本地 CLI（chat-agent 和 GPU 同机） |
| 都不设 | 没有图像能力，工具不注册 |

远程优先：配了隧道说明运维是刻意把 GPU 挪走的。

## 6. 故障排查

| 现象 | 检查 |
| --- | --- |
| `image_generation: false` | 服务器上 `curl 127.0.0.1:18190/health` |
| 隧道不通 | `ssh -R` 是否还活着；脚本会重连 |
| health 通但生成 502 | 多半是 RAM 不足，看 `blocked` 的原因 |
| 生成很慢 | 首次加载模型要几十秒；`queue.pending` 能看出排队 |

服务器侧快速自查：

```sh
curl -s http://127.0.0.1:18190/health | head -c 300
TOKEN=$(sed -n 's/^POCKET_ACCESS_TOKEN=//p' /etc/pocket-agent/pocket-agent.env)
curl -s -H "Authorization: Bearer $TOKEN" http://127.0.0.1:9991/api/status
```

## 7. 已知限制

- **尺寸可能被工作流覆盖**：请求 512×512 时，bridge 实际按 workflow 输出（实测 `wai_v17` 返回 640×640 JPEG）。
  工具描述里没有承诺精确尺寸。
- **一次只跑一个**：并发请求会排队，`queue.pending` 可见。
- **隧道依赖本机在线**：本机关机/休眠后能力消失，模型会从 health 看到原因。
- **无认证**：两端都只绑 loopback，靠 SSH 保证链路。要跨不可信主机时给 `imagebridge -token` 设置共享密钥。
