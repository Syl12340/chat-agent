# RFC：消息稳定标识（Message Identity）

> 排期更新：用户确立七项推进主线后，本 RFC 纳入[总路线 M3](product-direction.md)。下方 0.30 为原技术排期，正式版本随实施确定；已修订的身份和迁移契约继续有效。

## 2026-11-09 修订决议（优先于下方初稿）

状态：待实现设计；阶段 B，暂定 **0.30**。完整实施决策见[本体方案第 4 节](core-plan-2026-11.md#4-b稳定消息身份)。下方 0.28 排期和 v1 可选字段迁移方案撤回，保留初稿用于解释设计背景。

- 当前在线基线已经是 manifest 选中的 runtime v2，history 支持 transcript v1/v2，turns 支持 v3；不可再规划一次同名 v1→v2 迁移。
- 普通摘要仅改变有效上下文 floor，后台追加不会让旧索引漂移；身份用于解决删除后位置复用与跨代引用，不以错误的索引漂移作为依据。
- 采用宿主随机 128-bit ID，随首次提交持久化，日志顺序负责排序；撤回时间编码及“会话 key+全局序号”加载时派生方案，避免删除复用产生身份碰撞。
- 旧数据走显式停机迁移与新代校验/manifest 切换；临时产物保存已分配 ID，续作复用。逐文件类型冻结新版本，不悄悄向 version 1 增加未知字段。
- 来源有 ID 而校验失败时一律拒绝，不降级到索引。旧 life/topics 没有 hash，无法证明绑定时只保留历史展示/会话导航，不自动映射到当前位置。
- 稳定 ID 不取消 floor、角色、会话和完成状态检查；删除登记和新代重写延用 Harness 3C，不能另建旧代回读通道。

## 初稿（涉及上述决策的条目已被修订）

状态：草案，待评审。目标版本：0.28（见文末版本说明）。
范围：只做设计，不改现有生产数据格式，不引入前端框架，不重构 Harness。

## 1. 要解决的问题

当前交互来源校验依赖两个易变值：

```go
index := response.Source.MessageIndex          // internal/api/interactive.go:38
digest := sha256.Sum256([]byte(m.Content))     // :46
if response.Source.SHA256 != hex.EncodeToString(digest[:]) { reject }
```

`probe.Message` 目前没有任何持久标识：

```go
type Message struct {
	Source           string        // host-only，不进入模型视野
	Role             string
	Content          string
	ToolCalls        []ToolCall
	ToolCallID       string
	Images           []media.Image
	ReasoningContent string
}
```

这套契约对「一次性答题」是够的，对「跨轮更新的界面」不够：

| 场景 | 现契约的失效方式 |
| --- | --- |
| 会话压缩（compact）后历史被替换 | `message_index` 整体漂移，旧引用的指向变了 |
| 两条内容相同的消息（「好」「好」） | `sha256` 相同，无法区分 |
| 前端刷新 / 换设备 | 交互状态只在前端 Map，重建不了 |
| 角色或会话切换 | 本地 Map 清空，已选答案丢失 |
| 后台 followup 追加消息 | 索引再次增长，旧 surface 找不到原消息 |

结论：需要一个**宿主生成、模型不可见、可持久**的消息标识。

## 2. 设计约束

1. 模型不能看见、更不能伪造它。
2. 不给模型请求增加任何字段（避免影响缓存命中与提示词稳定性）。
3. 现有 v1 JSONL 可读，旧文件不需要停机迁移。
4. 旧交互契约（`message_index + sha256`）继续可用，不回归。
5. ID 一旦分配，删除后不再复用。

## 3. ID 格式

```
msg_<26 字符 Crockford base32，时间有序>
```

- 时间有序（前 10 位是毫秒时间戳），便于排序与分页。
- 26 字符固定长度，避免变长解析分支。
- 只在宿主内部使用，不参与任何模型提示词。

生成位置：`probe.Session.append` 落盘前，对每条 `Role == user|assistant|tool` 的消息赋值。

## 4. 存储格式

### 4.1 会话日志（v1 兼容扩展）

v1 记录结构保持 `{"version":1,"messages":[...]}` 不变，只在消息对象上增加一个可选字段：

```json
{"version":1,"messages":[{"id":"msg_01J...","role":"user","content":"今天好累"}]}
```

- 读取器**必须**接受缺失 `id`（旧文件）。
- 写入器**始终**输出 `id`。
- 旧消息在首次被新版本加载时，按「会话 key + 全局序号」确定性派生一个临时 ID，
  在下一次该会话发生重写（compact / 删除轮次）时落盘固化。
- 派生规则必须幂等：同一文件多次加载得到同一 ID，否则跨设备比对会失效。

### 4.2 不进入模型请求

`Message` 增加字段：

```go
ID string `json:"id,omitempty"`      // 落盘用
```

但在构造模型请求时**显式剔除**（不依赖 `omitempty` 的巧合）。建议在序列化前
统一走一个 `modelMessage(m Message)` 投影函数，确保白名单输出。

### 4.3 轮次关联

`turns.Turn` 已有 `StartIndex/EndIndex`。v2 增加：

```json
{"start_message_id":"msg_..","end_message_id":"msg_.."}
```

不替换索引字段（索引仍用于既有渲染），只是让引用不再依赖位置。

## 5. 生命周期语义

| 事件 | 语义 |
| --- | --- |
| 分配 | 消息首次落盘时生成，之后永不变更 |
| 压缩（compact） | 被折叠进摘要的消息**保留 ID 记录**在摘要元数据里，标记为 `folded`；未折叠消息 ID 不变 |
| 删除单轮（purge） | 被删消息 ID 写入会话级 tombstone 列表，不再复用 |
| 会话删除 | 整个日志移除，ID 空间随会话消失 |
| 迁移 | 派生 ID 与真实 ID 不得混用，迁移完成的会话打 `ids_fixed: true` |

**关键不变量**：任何 UI 引用必须能回答两个问题——「这条消息还在吗」「它还是原来的那条吗」。
ID + tombstone 足以回答；索引 + 哈希不能。

## 6. 交互契约演进

### v1（保持不变）

```
message_index + sha256(content) + role + session + 上下文边界
```

### v2（新增）

```
message_id + source_hash + turn_id + surface_id + revision
```

- `source_hash` 仍是内容哈希，但**只用于检测编辑**，不再承担身份职责。
- 两者并存：v1 交互继续按 v1 校验；新交互一律 v2。

## 7. 对 0.26 主动陪伴数据的影响

`life` v4 与 `topics` v2 目前把触发记录绑定到消息位置。迁移策略：

1. **不改现有字段**，增加可选 `message_id`。
2. 过渡期双写：位置 + ID 同时记录。
3. 读取时优先用 ID 定位；ID 缺失或已 tombstone 时回退到位置 + 哈希，并标记 `stale`。
4. `stale` 记录不用于「更新已有界面」，只用于展示历史。

不承诺旧记录能自动修好——需要一次显式的 `migrate-ids` 命令，产出报告后由用户确认。

## 8. 不可逆风险

- 一旦 tombstone 落盘，该 ID 永久不可复用；若实现错误会误杀仍在使用的引用。
- 派生 ID 规则若在不同版本间变化，会造成「同一条消息两个 ID」，跨设备合并会重复。
  → 派生规则必须写进代码注释并配测试，视为格式契约。

## 9. 版本说明

用户检查的 GitHub 状态是 `0.26.0 / versionCode 21`。当前工作区已经发布了
`0.27.0-debug / versionCode 22`，内容是 UI 重构（DSH 外观 + 新图标），与原本规划的
「0.27 = Message Identity」冲突。

建议：**Message Identity 落到 0.28**，0.27.x 只做稳定性收口，避免一次发布里同时改视觉与数据契约。
