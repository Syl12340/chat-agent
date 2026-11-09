# 设计系统

更新：2026-11-09。客户端整体重做的地基。本文描述**已经生效的约束**，以及还没做完的部分。

## 1. 为什么要有这一层

`web/` 下的样式是四层叠加长出来的：`style.css`（初版）、`chat-ui.css`（0.21/0.22 两次改版）、`desktop-workspace.css`、`dsh-look.css`。同一套颜色在 `style.css` 和 `chat-ui.css` 各写了一遍，值还不一样（`--on-surface` 一处 `#0f1115`、一处 `#222`），最终颜色取决于加载顺序。改一个页面先要猜"现在生效的是哪一层"。

`tokens.css` 是这件事的终点：**颜色和尺度只在那里定义一次**。`style.css` 与 `chat-ui.css` 里的重复调色板已删除，保留的只是"把根元素接到 token 上"的那几行。

取值不是新设计的，是**当前实际渲染的那一份**（`chat-ui.css` 的明色值 + 两处一致的暗色值），所以这次迁移在屏幕上不改变任何颜色。

## 2. Token

| 组 | 变量 | 说明 |
| --- | --- | --- |
| 强调色 | `--primary` `--on-primary` `--primary-container` `--on-primary-container` | 明色下 `theme.js` 会按用户选的主题重算这一组 |
| 次级/第三 | `--secondary*` `--tertiary-container` `--on-tertiary-container` | |
| 状态 | `--error*` `--warning-container` `--on-warning-container` | |
| 表面 | `--surface` `--surface-low` `--surface-container` `--surface-high` `--surface-highest` | 从页面到最上层控件 |
| 文字 | `--on-surface` `--on-surface-variant` | |
| 描边 | `--outline` `--outline-variant` | |
| 其它 | `--sidebar` `--scrim` `--shadow` | |
| 字体 | `--font-ui` `--font-mono` `--text-2xs`…`--text-2xl` | 2xs=10px 到 2xl=26px |
| 间距 | `--space-1`…`--space-6` | 4/8/12/16/24/32 |
| 圆角 | `--radius-xs`…`--radius-panel` `--radius-pill` | 4/8/12/16/20/28，与 DSH 的尺度一致 |
| 控件高度 | `--control-sm` `--control-md` `--control-lg` | 32/40/48，48 是触控底线 |
| 焦点 | `--focus-width` `--focus-offset` | 焦点必须是可见的环，不能只改颜色 |
| 动效 | `--easing` | `cubic-bezier(.4,0,.2,1)`，与 DSH 曲线一致 |

暗色在 `@media (prefers-color-scheme: dark)` 里重新赋值。**新加 token 必须同时加进明暗两块**，否则暗色下会拿到明色值。

## 3. 两条硬规则

### 不写字面值

组件规则里不写十六进制颜色、不写 px 圆角、不重复字体栈。需要新值时先问：现有 token 里有没有；没有就加到 `tokens.css`，而不是就地写死。

### 整圆必须声明 `corner-shape: round`

`style.css` 给根元素设了 `corner-shape: superellipse(1.5)`——卡片、对话气泡的柔和感来自它。但同一个属性会把 `border-radius: 50%` 变成"圆角方块"，头像、发送键、圆点全都不圆了。

所以每个 50% 圆角的选择器都要配对声明 `corner-shape: round`。已覆盖：`.send-button` `.image-add` `.image-draft button` `.image-viewer>button` `.session-delete` `.accent-dot` `.note-dot` `.status-dot` 以及两个 `::before` 圆点。规则在 `tokens.css` 末尾，**新增圆形控件要加进那个选择器列表**。

支持 `corner-shape` 的浏览器（Chromium 139+）按新形状绘制；不支持的浏览器本来就画圆，忽略这条也正确。

## 4. 还没做

- **`:hover` 没有包在 `@media (hover: hover)` 里**。触屏上点击后会残留 hover 态。DSH 的规范要求所有 hover 效果包进这个查询，键盘焦点与选中态留在外面。这是一次涉及全站几百条规则的批量改造，等逐页重做时一起做。
- **`dsh-look.css` 仍是覆盖层**，没有并入组件样式。逐页替换页面时，被替换页面的旧规则要跟着删掉，而不是继续往上叠。
- **页面的 CSS 字节数没降**。这一轮只是把颜色收到一处；真正的减重要靠删掉被新页面取代的旧规则。

## 5. 参考

- 取值与形状规则参考 DeepSeek Harness `packages/client/ui-theme`（MIT），特别是 `styles/base.css` 的字体栈与圆角尺度、`docs/web-styling.md` 的组件规则。本项目是原生 CSS，没有引入其 CSS Modules、`clsx` 或组件库。
