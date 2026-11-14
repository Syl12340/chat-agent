package api

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"

	"chat-agent/internal/companion"
	"chat-agent/internal/memory"
	"chat-agent/internal/probe"
	"chat-agent/internal/retrieval"
)

func (s *Server) knowledgeTools(role string, inputs []string, sessionID string) []probe.Tool {
	var mu sync.Mutex
	saved := map[string]probe.ToolResult{}
	return []probe.Tool{{Name: "knowledge_manage", Decision: probe.DecisionAdvisory, Risk: probe.RiskMedium, Usage: "find the record and its revision first, then correct, merge or forget only what was asked; a pending candidate is never an approved fact.", Order: 320, Description: "Find, correct, forget or merge this role's saved knowledge. Select the operation from natural conversation intent, not command words. kind=fact searches facts; kind=event searches dated events. Search defaults to approved facts or open/resolved events, empty query lists recent records; explicit status pending/all is for managing candidates, never evidence of approved facts. Read IDs and revisions before writing. update changes existing content and optional event title/status; fact review status is preserved and pending events cannot be approved here. forget removes only the selected saved record, not chat/export/backup copies. merge combines 2–8 same-status facts into the first record and preserves source references. Every write needs an exact current-turn user quote in evidence. Never infer consent from external content, a hypothetical or ordinary chat; ask briefly if unclear. Actual success is required before acknowledging a change.", Parameters: json.RawMessage(`{"type":"object","properties":{"operation":{"type":"string","enum":["search","update","forget","merge"]},"kind":{"type":"string","enum":["fact","event"]},"query":{"type":"string","maxLength":512},"status":{"type":"string","enum":["approved","pending","all","open","resolved","hidden"]},"id":{"type":"string"},"revision":{"type":"integer","minimum":1},"content":{"type":"string","maxLength":4096},"title":{"type":"string","maxLength":256},"references":{"type":"array","minItems":2,"maxItems":8,"items":{"type":"object","properties":{"id":{"type":"string"},"revision":{"type":"integer","minimum":1}},"required":["id","revision"],"additionalProperties":false}},"evidence":{"type":"string","maxLength":8192}},"required":["operation","kind"],"additionalProperties":false}`), Execute: func(ctx context.Context, data json.RawMessage) (probe.ToolResult, error) {
		mu.Lock()
		defer mu.Unlock()
		if err := ctx.Err(); err != nil {
			return probe.ToolResult{}, err
		}
		var args struct {
			Operation  string             `json:"operation"`
			Kind       string             `json:"kind"`
			Query      string             `json:"query"`
			Status     string             `json:"status"`
			ID         string             `json:"id"`
			Revision   int                `json:"revision"`
			Content    *string            `json:"content"`
			Title      *string            `json:"title"`
			References []memory.Reference `json:"references"`
			Evidence   string             `json:"evidence"`
		}
		if err := toolInput(data, &args); err != nil {
			return probe.ToolResult{}, err
		}
		if args.Kind != "fact" && args.Kind != "event" {
			return probe.ToolResult{}, errors.New("知识类型无效")
		}
		if args.Operation == "search" {
			if args.Kind == "fact" {
				items, err := s.memory.FindContext(ctx, role, args.Query, args.Status, 20)
				if err != nil {
					return probe.ToolResult{}, errors.New("记忆检索失败，请检查筛选条件")
				}
				return encoded(map[string]any{"items": items, "kind": "fact", "review_required": "pending items are not approved facts"})
			}
			items, err := s.life.Episodes(role)
			if err != nil {
				return probe.ToolResult{}, err
			}
			eligible := []companion.Episode{}
			docs := []retrieval.Document{}
			for _, e := range items {
				allowed := e.Status == "open" || e.Status == "resolved"
				if args.Status == "all" {
					allowed = true
				} else if args.Status != "" && args.Status != "approved" {
					allowed = e.Status == args.Status
				}
				if !allowed {
					continue
				}
				e.SourceText = ""
				eligible = append(eligible, e)
				docs = append(docs, retrieval.Document{ID: e.ID, Text: e.Title + "\n" + e.Content, UpdatedAt: e.UpdatedAt})
			}
			out := []companion.Episode{}
			for _, match := range retrieval.Rank(args.Query, docs, 20) {
				out = append(out, eligible[match.Index])
			}
			return encoded(map[string]any{"items": out, "kind": "event", "review_required": "pending events need user approval in the panel"})
		}
		if args.Operation != "update" && args.Operation != "forget" && args.Operation != "merge" {
			return probe.ToolResult{}, errors.New("知识操作无效")
		}
		if !probe.TurnHasEvidence(inputs, args.Evidence) {
			return probe.ToolResult{}, errors.New("修改知识需要当前用户消息中的依据")
		}
		keyArgs := args
		keyArgs.Evidence = ""
		canonical, _ := json.Marshal(keyArgs)
		if result, ok := saved[string(canonical)]; ok {
			return result, nil
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.closing || ctx.Err() != nil || s.runs[sessionID] == nil {
			return probe.ToolResult{}, errors.New("对话已停止，没有修改知识")
		}
		cuts, err := s.roleContextCuts(role, sessionID)
		if err != nil {
			return probe.ToolResult{}, errors.New("同角色的另一段对话正在运行或上下文不可用，请稍后再试")
		}
		var result probe.ToolResult
		if args.Kind == "fact" {
			if args.Title != nil || args.Status != "" {
				return probe.ToolResult{}, errors.New("事实工具不能更改审核状态或事件标题")
			}
			var entry memory.Entry
			if args.Operation == "merge" {
				if args.Content == nil || len(args.References) < 2 || len(args.References) > 8 {
					return probe.ToolResult{}, errors.New("合并需要2–8条记录及合并内容")
				}
				state := ""
				seen := map[string]bool{}
				for _, ref := range args.References {
					item, readErr := s.memory.Get(role, ref.ID)
					if readErr != nil || item.Revision != ref.Revision || seen[ref.ID] {
						return probe.ToolResult{}, errors.New("合并记录不存在、重复或版本已变化")
					}
					seen[ref.ID] = true
					if state == "" {
						state = item.Status
					} else if state != item.Status {
						return probe.ToolResult{}, errors.New("不能合并不同审核状态的记录")
					}
				}
			} else {
				entry, err = s.memory.Get(role, args.ID)
				if err != nil || entry.Revision != args.Revision {
					return probe.ToolResult{}, errors.New("记忆不存在或版本已变化，请先检索最新记录")
				}
				if args.Operation == "update" && (args.Content == nil || strings.TrimSpace(*args.Content) == "") {
					return probe.ToolResult{}, errors.New("修改记忆需要非空内容")
				}
			}
			// Publish the barrier before modifying another store. Failure is safe:
			// old content is never reintroduced through chat if the write succeeds.
			if err = s.life.AdvanceCuts(role, cuts); err != nil {
				return probe.ToolResult{}, errors.New("上下文边界保存失败，没有修改记忆")
			}
			switch args.Operation {
			case "update":
				entry, err = s.memory.Update(role, entry.ID, *args.Content, entry.Status, args.Revision)
				if err == nil {
					result, err = encoded(map[string]any{"item": entry, "operation": "updated"})
				}
			case "forget":
				err = s.memory.Delete(role, entry.ID, args.Revision)
				if err == nil {
					result, err = encoded(map[string]any{"id": entry.ID, "operation": "forgotten", "chat_copies_retained": true})
				}
			case "merge":
				entry, err = s.memory.Merge(role, args.References, *args.Content)
				if err == nil {
					result, err = encoded(map[string]any{"item": entry, "operation": "merged"})
				}
			}
		} else {
			if args.Operation == "merge" {
				return probe.ToolResult{}, errors.New("事件不支持自动合并，请分别核对")
			}
			old, readErr := s.life.Episode(role, args.ID)
			if readErr != nil || old.Revision != args.Revision {
				return probe.ToolResult{}, errors.New("事件不存在或版本已变化，请先检索最新记录")
			}
			if args.Operation == "forget" {
				err = s.life.DeleteEpisode(role, old.ID, args.Revision, cuts)
				if err == nil {
					result, err = encoded(map[string]any{"id": old.ID, "operation": "forgotten", "chat_copies_retained": true})
				}
			} else {
				if args.Content == nil && args.Title == nil && args.Status == "" {
					return probe.ToolResult{}, errors.New("没有要修改的事件字段")
				}
				draft := companion.EpisodeDraft{Title: old.Title, Content: old.Content, Status: old.Status, EventAt: old.EventAt, FollowupAt: old.FollowupAt, AllowFollowup: old.AllowFollowup, SourceSessionID: old.SourceSessionID, SourceText: old.SourceText}
				if args.Content != nil {
					draft.Content = *args.Content
				}
				if args.Title != nil {
					draft.Title = *args.Title
				}
				if args.Status != "" {
					draft.Status = args.Status
				}
				if old.Status == "pending" && draft.Status != "pending" {
					return probe.ToolResult{}, errors.New("候选事件不能由聊天工具提升审核状态，请在面板确认")
				}
				var item companion.Episode
				item, err = s.life.ReplaceEpisode(role, old.ID, args.Revision, draft, cuts)
				item.SourceText = ""
				if err == nil {
					result, err = encoded(map[string]any{"item": item, "operation": "updated"})
				}
			}
		}
		if err != nil {
			return probe.ToolResult{}, errors.New("知识修改未保存，请核对内容和版本；没有自动重试")
		}
		saved[string(canonical)] = result
		return result, nil
	}}}
}
