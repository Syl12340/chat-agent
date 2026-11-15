# 源码级 UI 复刻：素材与方法

写于 2026-11-16。目标是把 chat-agent 的界面做成和 DSH 一样，**基于 DSH 自己的源码**，
不是照着截图猜。

## DSH 源码在哪

不在仓库里，在 Electron 的打包文件里：

```
D:\dsh\resources\app.asar        209 MB
D:\dsh\resources\app.asar.unpacked
```

`app.asar` 是 Chromium Pickle 包，格式：

```
[u32 = 4][u32 = pickle 大小][u32 = JSON 长度][JSON 头][内容...]
```

头部后面有 padding，所以**不要按字段宽度硬算偏移**，直接在前 64 字节里找 `{"files"`，
再用 `JSONDecoder.raw_decode` 拿到 JSON 的真实长度。内容基点 = `8 + 第二字段`。

两个脚本（`.cache/`，未纳入版本管理）：

```powershell
python .cache/asar-list.py    D:\dsh\resources\app.asar '.css'        # 列出匹配项
python .cache/asar-extract.py D:\dsh\resources\app.asar 'theme/src' .cache\dsh-ui
```

坑：asar 头部里的 `offset` 是**字符串**（避免 JSON 精度丢失），必须 `int()` 后再用。

## 素材清单

`app.asar` 里 21,717 个条目，其中：

| 类别 | 数量 | 位置 |
| --- | --- | --- |
| CSS Modules 源文件 | 236 | `@deepseek-ai/dsh-client-ui-*/src/**/*.module.css` |
| 设计令牌 | **237 个** | `dsh-client-ui-theme/src/styles/design-platform.css` (29 KB) |
| 聊天界面 | 141 | `dsh-client-ui-chat/src/**` |
| 输入区 | — | `dsh-client-ui-conversation/src/client/input/**` |
| 主题/字体 | — | `dsh-client-ui-theme/src/styles/*` |

令牌命名是 `--dsw-*`，分三层：

- `--dsw-alias-*` —— 语义别名（`bg-base`、`bg-layer-1`、`border-l1`、`state-business-primary`…），
  共 14 个可由主题覆盖
- `--dsw-radius-*`、`--dsw-space-*` 等尺度令牌
- 其余为调色板与字形

样式表除令牌外还有：`base.css`、`corner-shape.css`、`focus.css`、`scrollbar.css`、`shiki.css`。

## 复刻计划（下一步）

技术栈不同（DSH 是 React + CSS Modules，chat-agent 是原生 JS + 全局 CSS），所以**不是逐行
照抄组件**，而是：

1. **先换底**：把 `design-platform.css` 的 237 个令牌映射进 `web/tokens.css`，保留 chat-agent
   现有的变量名作为别名指向 `--dsw-*`。这样所有现有样式立刻继承 DSH 的配色、圆角、间距。
2. **再换骨架**：对照 `dsh-client-ui-layout` 与 `dsh-client-ui-sidebar` 的 CSS，重写
   `web/style.css` 里的布局段（三栏比例、栏宽、边框、滚动条）。
3. **最后换零件**：按 `dsh-client-ui-chat` 的消息节点、`conversation/input` 的 composer、
   `dsh-client-ui-tool-*` 的工具卡，逐个替换现有的卡片样式。
4. 每一步都截图与 DSH 界面对比，而不是只看代码。

## 现阶段状态

- 0.7.7 已完成「状态可见性」：设置里能看到 `图像生成 · ComfyUI 没有运行`，删除按钮有危险色
- 提取链路已打通并验证（能正确读出 DSH 的 CSS 源码）
- **尚未开始替换**：237 个令牌还没有映射进 chat-agent
