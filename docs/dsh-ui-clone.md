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

## 第一步：令牌层（已完成，0.7.8）

`web/dsw-tokens.css` 由 `.cache/gen-dsw-tokens.py` 从 `design-platform.css` 生成，**228 个
`--dsw-*` 声明原样复制**，只改选择器形状（DSH 用 `body` / `body[data-ds-dark-theme]`，
我们用 `:root` + `prefers-color-scheme`）。不要手改这个文件，改生成脚本。

然后 `web/tokens.css` 把现有变量名指向它们，约 2000 行已有 CSS 一行没动就继承了 DSH 的配色。

### 三个发现

**1. 调色板本来就是同一套。** 对比之后发现 chat-agent 的 `#0f1115`、`#61666b`、`#4176e6`、
`#151517` 精确等于 DSH 的 `neutral-bluish-1000`、`neutral-bluish-700`、`deepseek-500`、
`neutral-bluish-950`。所以「复刻」的差距不在颜色值，而在**层级策略**：DSH 亮色下
`bg-layer-1/2/3` 全是纯白，靠 `border-l1..l4` 的 rgba 细线分层；我们原来用 `#f8f8f8` /
`#f3f3f3` / `#ebebeb` 的灰阶。

**2. 亮色全白不会糊。** 这是事前最担心的（侧栏与内容只靠一条线区分）。实测通过：
浏览器验证过 `surface = surfaceLow = rgb(255,255,255)`，界面依然分得清。

**3. `--surface-high` 不能映射到 `bg-layer-3`。** 第一次按字面对映，结果亮色下
`surface-high` 变成纯白——而它在 37 处 CSS 里被用作 hover 与选中底色，等于让所有交互反馈
失效。改用 DSH 的 `interactive-bg-hover` / `interactive-bg-active`（半透明
`rgba(38,49,72,.06)` / `rgba(255,255,255,.08)`）：既能自适应任意底色，又和 DSH 的
hover 完全一致。`--surface-container` 同理改指 `card-fill`（亮 `#fafafa`、暗 `#2c2c2e`），
否则用户消息气泡和卡片会失去可见背景。

### 验证方式

部署前用 Playwright **拦截 CSS 响应**预览（CSP 禁止 `addStyleTag` 内联样式，所以走
`page.route`），确认亮/暗两套都能用再发布。线上复查：`dsw-tokens.css` 与 `tokens.css`
均 200，`--dsw-alias-bg-base` 在亮色解析为 `rgb(255,255,255)`、暗色 `rgb(21,21,23)`。

## 第二步：栏框（已完成，0.7.9）

DSH 的列宽不是拍脑袋的，写在 `dsh-client-ui-layout/src/client/columns.ts` 里，是一份契约：

| 常量 | 值 | 含义 |
| --- | --- | --- |
| `SIDEBAR_DEFAULT` | 280 | 未拖动前的宽度 |
| `SIDEBAR_MIN` / `MAX` | 264 / 420 | 拖动夹取范围 |
| `SIDEBAR_COLLAPSED` | 56 | 收起后的图标栏 |
| `CENTER_MIN` | 400 | 右栏打开时保护的中心宽度 |
| `SIDEBAR_AUTO_COLLAPSE` | 1024 | 自动收成图标栏 |
| `SIDEBAR_DRAWER_BREAKPOINT` | 768 | 改为覆盖式抽屉 |

侧栏原本已经是 280px，正好等于 DSH 默认值。真正的差别在**分隔方式**：

```css
/* DSH AppFrame.module.css */
.sidebarCol {
  background: var(--dsw-specific-sidebar-fill);   /* 亮 rgb(249,250,251) / 暗 rgb(27,27,28) */
  border-right: 0.5px solid var(--dsw-alias-border-l3);
}
```

我们原来是 `background: var(--surface-low)`（上一步之后等于纯白）+ `1px` 实线。改成
`sidebar-fill` + **0.5px** 之后，侧栏与内容之间才有了 DSH 那种"面与面的接缝"，而不是
一条画上去的线。

### 一个必须注意的层叠陷阱

`sidebar-compact.css` 晚于所有断点规则加载，而 **media query 不增加特异性**。所以
`@media(max-width:760px){.sidebar{width:min(300px,86vw)}}` 会被后面那条无条件的
`.sidebar{width:280px}` 盖掉——移动端会变成固定 280px。

解法是把分支在同一个文件里重述一遍（264px 是 DSH 的 `SIDEBAR_MIN`）。这类问题只有把
加载顺序和断点放在一起看才能发现。

## 复刻计划（剩余步骤）

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
