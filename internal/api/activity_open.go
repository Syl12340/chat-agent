package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync"

	"chat-agent/internal/companion"
	"chat-agent/internal/probe"
	"chat-agent/internal/retrieval"
)

func (s *Server) activityOpenTool(role string, inputs []string, sessionID string) []probe.Tool {
	var nonce [16]byte
	_, nonceErr := rand.Read(nonce[:])
	var mu sync.Mutex
	saved := map[string]probe.ToolResult{}
	return []probe.Tool{{Name: "shared_activity_open", Usage: "list, begin or resume a shared activity; merely reading one never resumes or rebinds it.", Order: 600, Description: "List, begin or continue the current role's shared activities, chosen from conversation intent without command syntax or a settings visit. action=list reads up to eight IDs/revisions/titles; optional query ranks matching saved goals/progress and returns full matching notes within a bounded result. It is read-only: recalling a paused/completed activity never resumes or binds it. action=create requires title, kind and goal plus a real current-turn user quote as evidence; create only when an ongoing shared activity is wanted, not for ordinary small talk. action=open requires activity_id, revision and evidence and resumes/binds that existing activity. Switching affects this conversation, not another role or the old activity's status. Returned activity_state overrides the initial snapshot and can be used by shared_activity_update in this turn. Kind is only an organizing label, never a mode the user must select.", Parameters: json.RawMessage(`{"type":"object","properties":{"action":{"type":"string","enum":["list","create","open"]},"query":{"type":"string","maxLength":512},"activity_id":{"type":"string"},"revision":{"type":"integer","minimum":1},"kind":{"type":"string","enum":["bedtime","reflection","interest","small_step"]},"title":{"type":"string","maxLength":256},"goal":{"type":"string","maxLength":2048},"evidence":{"type":"string","maxLength":8192}},"required":["action"],"additionalProperties":false}`), Execute: func(ctx context.Context, data json.RawMessage) (probe.ToolResult, error) {
		mu.Lock()
		defer mu.Unlock()
		if err := ctx.Err(); err != nil {
			return probe.ToolResult{}, err
		}
		var args struct {
			Action   string `json:"action"`
			ID       string `json:"activity_id"`
			Revision int    `json:"revision"`
			Kind     string `json:"kind"`
			Title    string `json:"title"`
			Goal     string `json:"goal"`
			Evidence string `json:"evidence"`
			Query    string `json:"query"`
		}
		if err := toolInput(data, &args); err != nil {
			return probe.ToolResult{}, err
		}
		if args.Action == "list" {
			if len([]rune(args.Query)) > 512 {
				return probe.ToolResult{}, errors.New("活动查询过长")
			}
			items, err := s.activities.List(role)
			if err != nil {
				return probe.ToolResult{}, errors.New("活动列表暂时不可用")
			}
			out := []map[string]any{}
			docs := []retrieval.Document{}
			for _, a := range items {
				docs = append(docs, retrieval.Document{ID: a.ID, Text: a.Title + "\n" + a.Goal + "\n" + a.Progress, UpdatedAt: a.UpdatedAt})
			}
			for _, match := range retrieval.Rank(args.Query, docs, 8) {
				a := items[match.Index]
				item := map[string]any{"id": a.ID, "revision": a.Revision, "title": a.Title, "status": a.Status, "updated_at": a.UpdatedAt}
				if strings.TrimSpace(args.Query) != "" {
					item["goal"] = a.Goal
					item["progress"] = a.Progress
				}
				candidate := append(append([]map[string]any{}, out...), item)
				if len(referenceJSON(candidate)) <= recallContextBytes {
					out = candidate
				}
			}
			return encoded(out)
		}
		if args.Query != "" {
			return probe.ToolResult{}, errors.New("query 仅用于读取活动，不用于修改")
		}
		if !probe.TurnHasEvidence(inputs, args.Evidence) {
			return probe.ToolResult{}, errors.New("开始或接续活动需要当前用户消息中的依据")
		}
		if args.Action == "create" {
			if nonceErr != nil || args.ID != "" || args.Revision != 0 || args.Title == "" || args.Kind == "" {
				return probe.ToolResult{}, errors.New("新活动参数不完整或存在冲突")
			}
		} else if args.Action == "open" {
			if args.ID == "" || args.Revision < 1 || args.Title != "" || args.Goal != "" || args.Kind != "" {
				return probe.ToolResult{}, errors.New("接续活动需要有效标识和当前版本")
			}
		} else {
			return probe.ToolResult{}, errors.New("未知活动操作")
		}
		keyArgs := args
		keyArgs.Evidence = ""
		canonical, _ := json.Marshal(keyArgs)
		if result, ok := saved[string(canonical)]; ok {
			return result, nil
		}
		digest := sha256.Sum256(append(nonce[:], canonical...))
		draft := companion.ActivityDraft{Kind: args.Kind, Title: args.Title, Goal: args.Goal, IdempotencyKey: hex.EncodeToString(digest[:])}
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.closing || ctx.Err() != nil || s.runs[sessionID] == nil {
			return probe.ToolResult{}, errors.New("对话已停止，没有开始活动")
		}
		cuts, err := s.roleContextCuts(role, sessionID)
		if err != nil {
			return probe.ToolResult{}, errors.New("同角色其他对话正在运行或上下文不可用，请稍后再试")
		}
		item, created, err := s.activities.OpenForSession(role, sessionID, args.ID, args.Revision, draft, cuts)
		if err != nil {
			return probe.ToolResult{}, errors.New("活动未开始：请检查参数、版本或记录上限；不要盲目重复创建")
		}
		result, err := encoded(map[string]any{"activity": item, "activity_state": map[string]any{"revision": item.Revision, "status": item.Status}, "created": created})
		if err == nil {
			saved[string(canonical)] = result
		}
		return result, err
	}}}
}
