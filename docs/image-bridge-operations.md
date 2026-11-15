# 本机图像生成链路：运维手册

写于 2026-11-16。这条链路此前一直是「不可用」，而且**给出的原因是错的**——真正的原因
和该查的地方对不上，所以每次都要重新摸一遍。这里把它固定下来。

## 架构

```
[服务器] chat-agent ──► 127.0.0.1:18190 ──(SSH 反向隧道)──► [本机] imagebridge ──► imgctl ──► ComfyUI 8188
```

服务器上没有 Python、没有模型权重，也不需要对外开放任何端口。

相关文件：

| 文件 | 作用 |
| --- | --- |
| `bin/imagebridge.exe` | 本机的桥，监听 127.0.0.1:18190 |
| `scripts/image-bridge.py` | 桥的启动器与守护（`--check` / `--stop`） |
| `scripts/tunnel.ps1` | **正确入口**：起桥 + 建反向隧道，前台守护 |
| `scripts/serve-local.py` | 只转发 9991；加 `--with-image-bridge` 才转发 18190，且要求桥已在跑 |
| `E:\IAST\image-generation\start.bat` | 启动 ComfyUI 本体（人的决定，见下） |

## 三种症状要能分得清

服务端 `/api/status` 的 `image_reason` 是唯一可靠的判据：

| image_reason | 含义 | 该做什么 |
| --- | --- | --- |
| `无法连接到图像生成桥，检查隧道是否在线` | 服务器连不上 18190 | 桥没起，或隧道没带 `--with-image-bridge` |
| `ComfyUI 没有运行` | 桥在，ComfyUI 的 8188 没开 | 启动 ComfyUI |
| 空字符串且 `image_generation: true` | 整条链路可用 | 无 |

**这两种「不可用」不是一回事。** 桥的设计意图就是在 ComfyUI 关闭时仍然监听——这样服务端
才能说出「ComfyUI 没有运行」，而不是把一切归咎于隧道。2026-11-16 之前线上一直显示的是
第一种，实际却是第二种，于是一直在查隧道。

## 恢复步骤

```powershell
# 一键：起桥 + 建隧道（正确入口）
.\scripts\tunnel.ps1

# 只看状态，不启动
python scripts\image-bridge.py --check
```

输出应当形如：

```
  bridge   127.0.0.1:18190  up
  comfyui  127.0.0.1:8188   down
  comfy_online=False bridge=0.1.0
```

然后启动 ComfyUI 本体：

```
E:\IAST\image-generation\start.bat
```

**这一步是人的决定**，不自动化：ComfyUI 会占显存（本机 RTX 4060 Laptop，8 GiB）、加载模型
耗时，什么时候开应该由你决定。桥的注释里也是这么写的。

## 一个容易误判的地方

服务端的健康探测有 **20 秒 TTL**（`imagegen.healthTTL`），探测在后台进行、最多每 20 秒一次。

所以：**刚修好链路后立刻查 `/api/status`，看到的还是上一次的旧结论。** 等 20 秒再查。

2026-11-16 我自己就撞了一次：桥刚起来、隧道刚转发，查 `/api/status` 仍然是
`无法连接到图像生成桥`，差点当成 bug 去改代码。20 秒后重查，变成了
`ComfyUI 没有运行`——正确的那个。

## 当前状态（2026-11-16 02:00 左右）

- 桥：已起，`bridge=0.1.0`，`comfy_online=False`
- 隧道：已带 `--with-image-bridge` 转发 18190
- 服务端：`image_generation:false`，`image_reason:ComfyUI 没有运行`
- ComfyUI 本体：**未启动**，所以还没有真正跑过一张图

桥的 `/health` 是完整的：9 个 profile（`wai_v17`、`anima_miaomiao`、`z_anime_d8`、
`llada_t2i`、`llada_edit` 等）、GPU 温度与显存、内存压力、队列长度都在。换句话说，除了
ComfyUI 那一端，链路已经全部就绪。
