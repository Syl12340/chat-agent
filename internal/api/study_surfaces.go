package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"chat-agent/internal/presentation"
	"chat-agent/internal/probe"
)

func (s *Server) studySurfaceTools(scope localTaskScope) []probe.Tool {
	tool := probe.Tool{
		Name:        "study_surface_save",
		Kind:        "edit",
		Order:       787,
		Timeout:     4 * time.Second,
		Description: "Create or revise one persistent courseware study panel in the current conversation. The source must be an exact SourceRef returned by courseware_read. Returns the surface id and revision for later updates.",
		Usage:       "use after a source-grounded explanation or exercise when a durable study panel would help. Do not publish ordinary chat as a surface. To revise, pass the returned surface_id and expected_revision.",
		Parameters: json.RawMessage(`{"type":"object","properties":{` +
			`"surface_id":{"type":"string"},"expected_revision":{"type":"integer","minimum":0},` +
			`"document_id":{"type":"string"},"document_revision":{"type":"integer","minimum":1},"document_sha256":{"type":"string"},` +
			`"source_start":{"type":"integer","minimum":0},"source_end":{"type":"integer","minimum":1},` +
			`"title":{"type":"string","maxLength":60},"summary":{"type":"string","maxLength":1000},` +
			`"progress":{"type":"number","minimum":0,"maximum":1},` +
			`"next_questions":{"type":"array","maxItems":4,"items":{"type":"string","maxLength":240}}` +
			`},"required":["document_id","document_revision","document_sha256","source_start","source_end","title","summary"],"additionalProperties":false}`),
		Present: func(raw json.RawMessage) (string, string) {
			var input struct {
				Title string `json:"title"`
			}
			_ = json.Unmarshal(raw, &input)
			return "保存学习面板", input.Title
		},
		Execute: func(_ context.Context, raw json.RawMessage) (probe.ToolResult, error) {
			return s.saveStudySurface(raw, scope)
		},
	}
	return []probe.Tool{tool}
}

func (s *Server) saveStudySurface(raw json.RawMessage, scope localTaskScope) (probe.ToolResult, error) {
	var input struct {
		SurfaceID        string   `json:"surface_id"`
		ExpectedRevision int      `json:"expected_revision"`
		DocumentID       string   `json:"document_id"`
		DocumentRevision int      `json:"document_revision"`
		DocumentSHA256   string   `json:"document_sha256"`
		SourceStart      int      `json:"source_start"`
		SourceEnd        int      `json:"source_end"`
		Title            string   `json:"title"`
		Summary          string   `json:"summary"`
		Progress         *float64 `json:"progress"`
		NextQuestions    []string `json:"next_questions"`
	}
	if json.Unmarshal(raw, &input) != nil || input.SourceEnd <= input.SourceStart || input.SourceEnd-input.SourceStart > 12000 || len(input.NextQuestions) > 4 || !utf8.ValidString(input.Title) || !utf8.ValidString(input.Summary) {
		return probe.ToolResult{}, errors.New("invalid study surface request")
	}
	doc, _, ref, err := s.courseware.Read(scope.RoleID, input.DocumentID, "", input.SourceStart, input.SourceEnd-input.SourceStart)
	if err != nil || doc.Revision != input.DocumentRevision || doc.SHA256 != input.DocumentSHA256 || ref.Start != input.SourceStart || ref.End != input.SourceEnd {
		return probe.ToolResult{}, errors.New("study source is unavailable or changed")
	}
	if strings.TrimSpace(input.Title) == "" || strings.TrimSpace(input.Summary) == "" || utf8.RuneCountInString(input.Title) > 60 || utf8.RuneCountInString(input.Summary) > 1000 {
		return probe.ToolResult{}, errors.New("study surface text is invalid")
	}
	if input.Progress != nil && (*input.Progress < 0 || *input.Progress > 1) {
		return probe.ToolResult{}, errors.New("study progress is invalid")
	}
	id := strings.TrimSpace(input.SurfaceID)
	if id == "" {
		var random [10]byte
		if _, err = rand.Read(random[:]); err != nil {
			return probe.ToolResult{}, err
		}
		id = "study_" + hex.EncodeToString(random[:])
	}
	location := fmt.Sprintf("%s · 原文行 %d–%d", doc.Name, ref.StartLine, ref.EndLine)
	if ref.StartPage > 0 {
		location = fmt.Sprintf("%s · 幻灯片 %d", doc.Name, ref.StartPage)
		if ref.EndPage != ref.StartPage {
			location += fmt.Sprintf("–%d", ref.EndPage)
		}
	}
	children := []presentation.Node{{Type: "callout", Text: strings.TrimSpace(input.Summary), Tone: "info"}, {Type: "text", Text: location, Tone: "muted"}}
	if input.Progress != nil {
		value := *input.Progress
		children = append(children, presentation.Node{Type: "progress", Progress: &value, Label: "学习进度"})
	}
	actions := []presentation.Action{}
	for index, question := range input.NextQuestions {
		question = strings.TrimSpace(question)
		if question == "" || utf8.RuneCountInString(question) > 240 {
			continue
		}
		label := question
		if utf8.RuneCountInString(label) > 60 {
			label = string([]rune(label)[:59]) + "…"
		}
		draft := fmt.Sprintf("请继续根据课件 %s（文档 ID %s，修订 %d，SHA-256 %s，原文范围 %d–%d）回答：%s", doc.Name, doc.ID, doc.Revision, doc.SHA256, ref.Start, ref.End, question)
		actions = append(actions, presentation.Action{ID: fmt.Sprintf("question_%d", index+1), Label: label, Effect: presentation.Effect{Type: "insert_draft", Text: draft}})
	}
	if len(actions) > 0 {
		children = append(children, presentation.Node{Type: "actions", Actions: actions})
	}
	view := presentation.View{Protocol: presentation.Protocol, Version: presentation.Version, Surface: presentation.Panel, ID: id, Title: strings.TrimSpace(input.Title), Root: presentation.Node{Type: "stack", Children: children}, Fallback: strings.TrimSpace(input.Summary)}
	s.mu.Lock()
	item, err := s.surfaceStore().put(scope.SessionID, scope.RoleID, view, input.ExpectedRevision)
	s.mu.Unlock()
	if err != nil {
		return probe.ToolResult{}, err
	}
	return encoded(map[string]any{"surface_id": item.ID, "revision": item.Revision, "source_ref": ref, "instruction": "the panel is persistent; use this id and revision to update instead of creating duplicates"})
}
