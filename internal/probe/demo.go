package probe

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"pocket-agent/internal/interactive"
)

// demoStream is an explicitly selected, deterministic local demo. No API calls.
func (a *Agent) demoStream(ctx context.Context, tools *ToolSet, emit func(Event)) (Message, error) {
	messages := a.session.Messages()
	last := messages[len(messages)-1]
	userText := ""
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" {
			userText = messages[i].Content
			break
		}
	}
	text := "这是 **chat-agent 本地演示**，没有调用真实模型。\n\n你可以用它体验流式聊天、停止生成，以及刷新后恢复会话。\n\n- 输入“现在几点”，体验时间工具\n- 配置服务端模型后，即可开始真实对话\n\n> 你刚刚说：" + strings.ReplaceAll(last.Content, "\n", " ")
	if last.Role == "tool" {
		name := "get_current_time"
		for i := len(messages) - 2; i >= 0; i-- {
			if messages[i].Role == "assistant" {
				for _, call := range messages[i].ToolCalls {
					if call.ID == last.ToolCallID {
						name = call.Function.Name
					}
				}
				break
			}
		}
		if strings.HasPrefix(last.Content, "Error: ") {
			text = "工具没有完成这次操作：\n\n" + last.Content + "\n\n你可以检查输入后重试。"
		} else {
			switch name {
			case "todo_add", "todo_complete", "reminder_add", "reminder_cancel":
				var item struct {
					Title  string     `json:"title"`
					Status string     `json:"status"`
					DueAt  *time.Time `json:"due_at"`
				}
				_ = json.Unmarshal([]byte(last.Content), &item)
				switch name {
				case "todo_add":
					text = "已保存待办：**" + item.Title + "**。可以在侧栏“待办提醒”中查看。"
				case "todo_complete":
					text = "待办已完成：**" + item.Title + "**。"
				case "reminder_cancel":
					text = "提醒已取消：**" + item.Title + "**。"
				case "reminder_add":
					text = "提醒已保存：**" + item.Title + "**。\n\n"
					if item.DueAt != nil {
						text += "时间：`" + item.DueAt.Format(time.RFC3339) + "`。\n\n"
					}
					text += "到期后会进入本角色的提醒收件箱。本版本不提供关闭应用后的系统推送。"
				}
			case "todo_list", "reminder_list":
				var body struct {
					Items []struct {
						ID       string `json:"id"`
						Title    string `json:"title"`
						Status   string `json:"status"`
						Revision int    `json:"revision"`
					} `json:"items"`
				}
				_ = json.Unmarshal([]byte(last.Content), &body)
				if name == "todo_list" && strings.Contains(userText, "完成第一条待办") {
					for _, item := range body.Items {
						if item.Status == "open" {
							input, _ := json.Marshal(map[string]any{"id": item.ID, "revision": item.Revision})
							return demoCall("todo_complete", string(input)), nil
						}
					}
				}
				text = "当前角色的任务记录：\n"
				if len(body.Items) == 0 {
					text = "当前角色还没有这类记录。"
				}
				for _, item := range body.Items {
					text += "\n- " + item.Title + "（" + item.Status + "）"
				}
			case "memory_write":
				var entry struct {
					Status  string `json:"status"`
					Content string `json:"content"`
				}
				_ = json.Unmarshal([]byte(last.Content), &entry)
				if entry.Status == "approved" {
					text = "这条事实已经在当前角色的已审核记忆中。"
				} else {
					text = "已提出一条**待审核记忆**：\n\n> " + entry.Content + "\n\n请在左侧“记忆管理”中确认；审核前不会用于新会话。"
				}
			case "memory_search":
				var entries []struct {
					Content string `json:"content"`
				}
				_ = json.Unmarshal([]byte(last.Content), &entries)
				text = "当前角色没有匹配的已审核记忆。待审核的候选不会用于对话。"
				if len(entries) > 0 {
					text = "当前角色的已审核记忆：\n"
					for _, entry := range entries {
						text += "\n- " + entry.Content
					}
				}
			case "skill_load":
				text = "已读取当前角色的 Skill：\n\n" + last.Content
			default:
				text = fmt.Sprintf("时间工具返回的 UTC 时间是：`%s`。\n\n这是本地演示中真实执行的服务端时间工具。", last.Content)
			}
		}
	} else if response, _, _ := interactive.ParseResponse(last.Content); response != nil {
		text = "已收到你的答案。这是本地演示反馈，没有调用真实模型，也没有修改记忆、人设或任务。\n\n"
		if response.ComponentType == "quiz.single_choice" {
			text += "你选择了选项 **" + response.Answer.OptionID + "**。示例题里，2 大于 1，因此 B 更合适。"
		} else {
			text += "你完成了填空。真实对话中，模型会结合题目给出解释与反馈。"
		}
	} else if strings.TrimSpace(last.Content) == "互动演示" {
		text = "这是四种互动组件的本地演示。答题需要点击提交；曲线滑块仅在本机计算。\n\n" +
			"```chat-ui\n" + `{"schema_version":1,"id":"demo-choice","type":"quiz.single_choice","props":{"title":"试一试","question":"哪个数更大？","options":[{"id":"A","label":"1"},{"id":"B","label":"2"}]}}` + "\n```\n\n" +
			"```chat-ui\n" + `{"schema_version":1,"id":"demo-fill","type":"quiz.fill_blank","props":{"question":"用一句话描述你看到的波形。","fields":[{"id":"description","label":"你的描述","placeholder":"例如：上下周期变化"}]}}` + "\n```\n\n" +
			"```chat-ui\n" + `{"schema_version":1,"id":"demo-wave","type":"chart.line","props":{"title":"正弦波","x_label":"时间 (s)","y_label":"位移","source":{"kind":"sine","amplitude":1,"frequency":1,"duration":2,"controls":["amplitude","frequency","phase"]}}}` + "\n```\n\n" +
			"```chat-ui\n" + `{"schema_version":1,"id":"demo-vectors","type":"diagram.vector","props":{"title":"二维矢量","vectors":[{"id":"a","label":"A","from":{"x":0,"y":0},"to":{"x":2,"y":1}},{"id":"b","label":"B","from":{"x":0,"y":0},"to":{"x":-1,"y":2}}]}}` + "\n```\n\n你可以展开组件，在工作台中继续操作。"
	} else if _, ok := tools.byName["todo_add"]; ok && strings.HasPrefix(strings.TrimSpace(last.Content), "待办：") {
		input, _ := json.Marshal(map[string]string{"title": strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(last.Content), "待办："))})
		return demoCall("todo_add", string(input)), nil
	} else if _, ok := tools.byName["todo_list"]; ok && (strings.Contains(last.Content, "查看待办") || strings.Contains(last.Content, "完成第一条待办")) {
		return demoCall("todo_list", "{}"), nil
	} else if _, ok := tools.byName["reminder_add"]; ok && strings.Contains(last.Content, "提醒") && (strings.Contains(last.Content, "一分钟后") || strings.Contains(last.Content, "1分钟后")) {
		input, _ := json.Marshal(map[string]string{"title": "喝水并活动一下", "due_at": time.Now().Add(time.Minute).UTC().Format(time.RFC3339)})
		return demoCall("reminder_add", string(input)), nil
	} else if _, ok := tools.byName["reminder_list"]; ok && strings.Contains(last.Content, "查看提醒") {
		return demoCall("reminder_list", "{}"), nil
	} else if _, ok := tools.byName["memory_write"]; ok && strings.HasPrefix(strings.TrimSpace(last.Content), "记住") {
		content := strings.TrimSpace(strings.TrimLeft(strings.TrimPrefix(strings.TrimSpace(last.Content), "记住"), "：: "))
		input, _ := json.Marshal(map[string]string{"content": content})
		return demoCall("memory_write", string(input)), nil
	} else if _, ok := tools.byName["memory_search"]; ok && (strings.Contains(last.Content, "记忆") || strings.Contains(last.Content, "记得") || strings.Contains(last.Content, "喜欢")) {
		return demoCall("memory_search", `{"query":""}`), nil
	} else if _, ok := tools.byName["get_current_time"]; ok && (strings.Contains(last.Content, "时间") || strings.Contains(last.Content, "几点") || strings.Contains(strings.ToLower(last.Content), "time")) {
		return demoCall("get_current_time", "{}"), nil
	}
	runes := []rune(text)
	for i := 0; i < len(runes); i += 6 {
		timer := time.NewTimer(15 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return Message{}, context.Cause(ctx)
		case <-timer.C:
		}
		emit(Event{Type: EventTextDelta, Text: string(runes[i:min(i+6, len(runes))])})
	}
	return Message{Role: "assistant", Content: text}, nil
}

func demoCall(name, input string) Message {
	return Message{Role: "assistant", ToolCalls: []ToolCall{{ID: fmt.Sprintf("demo-%d", time.Now().UnixNano()), Type: "function", Function: FunctionCall{Name: name, Arguments: input}}}}
}
