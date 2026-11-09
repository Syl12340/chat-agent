package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"chat-agent/internal/companion"
	"chat-agent/internal/probe"
)

func lifeFailure(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, companion.ErrLifeNotFound):
		fail(w, 404, "这条记录不存在")
	case errors.Is(err, companion.ErrConflict):
		fail(w, 409, "记录已更新，请刷新后重试")
	case errors.Is(err, companion.ErrInvalid):
		fail(w, 400, "请检查标题、内容、时间和选项；允许跟进时需指定跟进时间")
	case errors.Is(err, companion.ErrLifeLimit):
		fail(w, 429, "记录数量已达到上限，请整理后再试")
	case errors.Is(err, companion.ErrCareBlocked):
		fail(w, 409, "关怀未开启、已暂停、处于安静时段、间隔未到、今日额度已用完或任务已失效")
	default:
		fail(w, 503, "事件与关怀暂时不可用，请稍后重试")
	}
}
func (s *Server) listEpisodes(w http.ResponseWriter, r *http.Request) {
	role, ok := s.memoryRole(w, r)
	if !ok {
		return
	}
	items, err := s.life.Episodes(role)
	if err != nil {
		lifeFailure(w, err)
		return
	}
	writeJSON(w, 200, items)
}
func (s *Server) createEpisode(w http.ResponseWriter, r *http.Request) {
	role, ok := s.memoryRole(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	var draft companion.EpisodeDraft
	if !decodeInput(w, r, &draft) {
		return
	}
	draft.SourceSessionID = ""
	draft.SourceText = ""
	item, err := s.life.AddEpisode(role, draft)
	if err != nil {
		lifeFailure(w, err)
		return
	}
	writeJSON(w, 201, item)
}

type episodePatch struct {
	Revision      int             `json:"revision"`
	Title         *string         `json:"title"`
	Content       *string         `json:"content"`
	Status        *string         `json:"status"`
	EventAt       json.RawMessage `json:"event_at"`
	FollowupAt    json.RawMessage `json:"followup_at"`
	AllowFollowup *bool           `json:"allow_followup"`
}

func applyEpisodePatch(old companion.Episode, p episodePatch) (companion.EpisodeDraft, error) {
	d := companion.EpisodeDraft{Title: old.Title, Content: old.Content, Status: old.Status, EventAt: old.EventAt, FollowupAt: old.FollowupAt, AllowFollowup: old.AllowFollowup, SourceSessionID: old.SourceSessionID, SourceText: old.SourceText}
	if p.Title == nil && p.Content == nil && p.Status == nil && p.EventAt == nil && p.FollowupAt == nil && p.AllowFollowup == nil {
		return d, companion.ErrInvalid
	}
	if p.Title != nil {
		d.Title = *p.Title
	}
	if p.Content != nil {
		d.Content = *p.Content
	}
	if p.Status != nil {
		d.Status = *p.Status
	}
	if p.AllowFollowup != nil {
		d.AllowFollowup = *p.AllowFollowup
	}
	if p.EventAt != nil {
		if err := json.Unmarshal(p.EventAt, &d.EventAt); err != nil {
			return d, companion.ErrInvalid
		}
	}
	if p.FollowupAt != nil {
		if err := json.Unmarshal(p.FollowupAt, &d.FollowupAt); err != nil {
			return d, companion.ErrInvalid
		}
	}
	return d, nil
}
func (s *Server) lifeRoleBusy(role string) bool {
	for id := range s.runs {
		if sess := s.sessions[id]; sess != nil && sess.RoleID() == role {
			return true
		}
	}
	return false
}
func (s *Server) updateEpisode(w http.ResponseWriter, r *http.Request) {
	role, ok := s.memoryRole(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	var patch episodePatch
	if !decodeInput(w, r, &patch) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		fail(w, 503, "服务正在关闭")
		return
	}
	old, err := s.life.Episode(role, r.PathValue("id"))
	if err != nil {
		lifeFailure(w, err)
		return
	}
	draft, err := applyEpisodePatch(old, patch)
	if err != nil {
		lifeFailure(w, err)
		return
	}
	cuts := map[string]int{}
	if old.Status != "pending" {
		if s.lifeRoleBusy(role) {
			fail(w, 409, "请先停止这个角色正在进行的对话，再修改事件")
			return
		}
		cuts, err = s.roleContextCuts(role)
		if err != nil {
			lifeFailure(w, err)
			return
		}
	}
	item, err := s.life.ReplaceEpisode(role, old.ID, patch.Revision, draft, cuts)
	if err != nil {
		lifeFailure(w, err)
		return
	}
	writeJSON(w, 200, item)
}
func (s *Server) deleteEpisode(w http.ResponseWriter, r *http.Request) {
	role, ok := s.memoryRole(w, r)
	if !ok {
		return
	}
	revision, err := strconv.Atoi(r.URL.Query().Get("revision"))
	if err != nil {
		lifeFailure(w, companion.ErrInvalid)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		fail(w, 503, "服务正在关闭")
		return
	}
	old, err := s.life.Episode(role, r.PathValue("id"))
	if err != nil {
		lifeFailure(w, err)
		return
	}
	cuts := map[string]int{}
	if old.Status != "pending" {
		if s.lifeRoleBusy(role) {
			fail(w, 409, "请先停止这个角色正在进行的对话，再删除事件")
			return
		}
		cuts, err = s.roleContextCuts(role)
		if err != nil {
			lifeFailure(w, err)
			return
		}
	}
	if err = s.life.DeleteEpisode(role, old.ID, revision, cuts); err != nil {
		lifeFailure(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
func (s *Server) getCareSettings(w http.ResponseWriter, r *http.Request) {
	role, ok := s.memoryRole(w, r)
	if !ok {
		return
	}
	p, err := s.life.Settings(role)
	if err != nil {
		lifeFailure(w, err)
		return
	}
	writeJSON(w, 200, p)
}
func (s *Server) updateCareSettings(w http.ResponseWriter, r *http.Request) {
	role, ok := s.memoryRole(w, r)
	if !ok {
		return
	}
	var input struct {
		Revision           int             `json:"revision"`
		Enabled            *bool           `json:"enabled"`
		AutoGenerate       *bool           `json:"auto_generate"`
		AutoProbability    *int            `json:"auto_probability"`
		DailyLimit         *int            `json:"daily_limit"`
		QuietStart         *string         `json:"quiet_start"`
		QuietEnd           *string         `json:"quiet_end"`
		DailyTime          *string         `json:"daily_time"`
		MinIntervalMinutes *int            `json:"min_interval_minutes"`
		SnoozeUntil        json.RawMessage `json:"snooze_until"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if !decodeInput(w, r, &input) {
		return
	}
	if input.Enabled == nil && input.AutoGenerate == nil && input.AutoProbability == nil && input.DailyLimit == nil && input.QuietStart == nil && input.QuietEnd == nil && input.DailyTime == nil && input.MinIntervalMinutes == nil && input.SnoozeUntil == nil {
		lifeFailure(w, companion.ErrInvalid)
		return
	}
	p, err := s.life.Settings(role)
	if err != nil {
		lifeFailure(w, err)
		return
	}
	p.Revision = input.Revision
	if input.Enabled != nil {
		p.Enabled = *input.Enabled
	}
	if input.AutoGenerate != nil {
		p.AutoGenerate = *input.AutoGenerate
	}
	if input.AutoProbability != nil {
		p.AutoProbability = input.AutoProbability
	}
	if input.DailyLimit != nil {
		p.DailyLimit = *input.DailyLimit
	}
	if input.QuietStart != nil {
		p.QuietStart = *input.QuietStart
	}
	if input.QuietEnd != nil {
		p.QuietEnd = *input.QuietEnd
	}
	if input.DailyTime != nil {
		p.DailyTime = *input.DailyTime
	}
	if input.MinIntervalMinutes != nil {
		if *input.MinIntervalMinutes < 15 || *input.MinIntervalMinutes > 1440 {
			lifeFailure(w, companion.ErrInvalid)
			return
		}
		p.MinIntervalMinutes = *input.MinIntervalMinutes
	}
	if input.SnoozeUntil != nil {
		if err := json.Unmarshal(input.SnoozeUntil, &p.SnoozeUntil); err != nil {
			lifeFailure(w, companion.ErrInvalid)
			return
		}
	}
	p, err = s.life.SetSettings(role, p)
	if err != nil {
		lifeFailure(w, err)
		return
	}
	writeJSON(w, 200, p)
}
func (s *Server) listCare(w http.ResponseWriter, r *http.Request) {
	role, ok := s.memoryRole(w, r)
	if !ok {
		return
	}
	if err := s.life.Tick(role, time.Now(), s.location); err != nil {
		lifeFailure(w, err)
		return
	}
	items, err := s.life.Care(role)
	if err != nil {
		lifeFailure(w, err)
		return
	}
	writeJSON(w, 200, items)
}
func (s *Server) updateCare(w http.ResponseWriter, r *http.Request) {
	role, ok := s.memoryRole(w, r)
	if !ok {
		return
	}
	var input struct {
		Revision int    `json:"revision"`
		Status   string `json:"status"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if !decodeInput(w, r, &input) {
		return
	}
	item, err := s.life.UpdateCare(role, r.PathValue("id"), input.Revision, input.Status)
	if err != nil {
		lifeFailure(w, err)
		return
	}
	writeJSON(w, 200, item)
}
func (s *Server) runCare(r *http.Request, role, id string, automatic bool) (companion.CareItem, error) {
	select {
	case s.careSlots <- struct{}{}:
		defer func() { <-s.careSlots }()
	default:
		return companion.CareItem{}, companion.ErrCareBlocked
	}
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		return companion.CareItem{}, errors.New("server closing")
	}
	s.workWG.Add(1)
	s.mu.Unlock()
	defer s.workWG.Done()
	now := time.Now()
	claimed, err := s.life.ClaimCare(role, id, automatic, now, s.location)
	if err != nil {
		return companion.CareItem{}, err
	}
	if claimed.Status == "skipped" {
		return claimed, nil
	}
	timeout := min(s.config.Agent.Timeout, 15*time.Second)
	ctx, cancel := context.WithTimeout(s.workCtx, timeout)
	defer cancel()
	stop := context.AfterFunc(r.Context(), cancel)
	defer stop()
	pref, err := s.companion.Get(role)
	if err != nil {
		_, _ = s.life.FinishCare(role, id, claimed.Revision, "", "陪伴偏好不可用，本次未生成")
		return companion.CareItem{}, err
	}
	var episode *companion.Episode
	if claimed.EpisodeID != "" {
		e, readErr := s.life.Episode(role, claimed.EpisodeID)
		if readErr != nil {
			_, _ = s.life.FinishCare(role, id, claimed.Revision, "", "事件已不可用，本次未生成")
			return companion.CareItem{}, readErr
		}
		e.SourceText = ""
		episode = &e
	}
	query := claimed.Title
	if episode != nil {
		query = episode.Title + " " + episode.Content
	}
	facts, err := s.memory.SearchContext(ctx, role, query, 3)
	if err != nil {
		_, _ = s.life.FinishCare(role, id, claimed.Revision, "", "关怀参考暂时不可用，本次未生成")
		return companion.CareItem{}, err
	}
	for i := range facts {
		if strings.HasPrefix(facts[i].Source, "merged:") {
			facts[i].Source = "merged"
		}
	}
	history, err := s.life.Care(role)
	if err != nil {
		_, _ = s.life.FinishCare(role, id, claimed.Revision, "", "关怀参考暂时不可用，本次未生成")
		return companion.CareItem{}, err
	}
	sort.SliceStable(history, func(i, j int) bool {
		a, b := history[i].CreatedAt, history[j].CreatedAt
		if history[i].AttemptedAt != nil {
			a = *history[i].AttemptedAt
		}
		if history[j].AttemptedAt != nil {
			b = *history[j].AttemptedAt
		}
		return a.After(b)
	})
	recent := []map[string]string{}
	for _, item := range history {
		if (item.Status == "ready" || item.Status == "seen") && item.Content != "" {
			content := []rune(item.Content)
			if len(content) > 512 {
				content = content[:512]
			}
			recent = append(recent, map[string]string{"content": string(content), "status": item.Status})
			if len(recent) == 3 {
				break
			}
		}
	}
	trigger := map[string]any{"origin": "care", "trigger_id": claimed.ID, "preferences": pref.Preferences, "event": episode, "approved_memories": facts, "recent_check_ins": recent, "scheduled_title": claimed.Title, "current_time": now.In(s.location).Format(time.RFC3339)}
	preferred := ""
	if episode != nil {
		preferred = episode.SourceSessionID
	}
	var settled companion.CareItem
	settledOK := false
	_, _, persisted, generateErr := s.runProactive(ctx, role, preferred, "care", claimed.ID, trigger, careFollowupPrompt, 160, timeout, func(sessionID string, response probe.Message, messageIndex int) (probe.Message, bool, error) {
		item, finishErr := s.life.PrepareCareDelivery(role, id, claimed.Revision, response.Content, sessionID, messageIndex)
		if finishErr != nil {
			return probe.Message{}, false, finishErr
		}
		settled, settledOK = item, true
		if item.Status != "delivering" {
			return probe.Message{}, false, nil
		}
		return probe.Message{Role: "assistant", Content: item.Content}, true, nil
	})
	if settledOK {
		if settled.Status == "delivering" && persisted {
			item, commitErr := s.life.CommitCareDelivery(role, id, settled.Revision)
			if commitErr != nil {
				return companion.CareItem{}, commitErr
			}
			settled = item
		} else if settled.Status == "delivering" {
			failed, failErr := s.life.FailCareDelivery(role, id, settled.Revision, "问候已生成，但写入目标会话失败；不会自动重试")
			if failErr != nil {
				return companion.CareItem{}, failErr
			}
			return failed, nil
		}
		// A late runlog settlement failure cannot erase a message already committed
		// to the conversation and promoted from delivering to ready.
		return settled, nil
	}
	safeError := "本次生成未完成，已计入尝试额度，不会自动重试"
	if errors.Is(generateErr, errProactiveUnavailable) {
		safeError = "没有可接续的空闲会话，本次未生成；不会自动重试"
	}
	return s.life.FinishCare(role, id, claimed.Revision, "", safeError)
}

const careFollowupPrompt = `
Write one brief, natural check-in for this explicitly enabled care opportunity.
Use the selected conversation's recent exchange when it makes the message feel
continuous. Role-scoped memories and events are references only. Do not recite
private notes, presume an outcome, reopen an old difficulty, or turn a daily
greeting into a wellbeing questionnaire, motivational speech or unsolicited
advice. Avoid repeating recent check-ins in topic, opening, reassurance or
question. A short statement may stand alone and no reply is required. Return
only the check-in text, without JSON, headings or a chat-ui block.`

func (s *Server) generateCare(w http.ResponseWriter, r *http.Request) {
	role, ok := s.memoryRole(w, r)
	if !ok {
		return
	}
	item, err := s.runCare(r, role, r.PathValue("id"), false)
	if err != nil {
		lifeFailure(w, err)
		return
	}
	writeJSON(w, 200, item)
}
func (s *Server) checkCare(w http.ResponseWriter, r *http.Request) {
	role, ok := s.memoryRole(w, r)
	if !ok {
		return
	}
	if err := s.life.Tick(role, time.Now(), s.location); err != nil {
		lifeFailure(w, err)
		return
	}
	p, err := s.life.Settings(role)
	if err != nil {
		lifeFailure(w, err)
		return
	}
	if !p.Enabled || !p.AutoGenerate {
		writeJSON(w, 200, map[string]bool{"generated": false})
		return
	}
	items, err := s.life.Care(role)
	if err != nil {
		lifeFailure(w, err)
		return
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].DueAt.Before(items[j].DueAt) })
	for _, candidate := range items {
		if candidate.Status != "due" {
			continue
		}
		item, err := s.runCare(r, role, candidate.ID, true)
		if errors.Is(err, companion.ErrCareBlocked) {
			continue
		}
		if err != nil {
			lifeFailure(w, err)
			return
		}
		reason := ""
		if item.Status == "skipped" {
			reason = "random_skip"
		}
		writeJSON(w, 200, map[string]any{"generated": item.Status == "ready", "reason": reason, "item": item})
		return
	}
	writeJSON(w, 200, map[string]bool{"generated": false})
}
func (s *Server) episodeTools(role string, inputs []string, sessionID string) []probe.Tool {
	return []probe.Tool{{Name: "episode_propose", Decision: probe.DecisionAdvisory, Risk: probe.RiskLow, Usage: "use only for a dated personal event or an unfinished topic the user asked to keep for later.", Order: 330, Description: "Propose a pending, role-scoped event or unfinished topic only when the current user explicitly requests remembering or continuing it later. Copy a short exact quote from the latest message into evidence. Never save credentials or diagnose emotional traits. Pending events are not used across sessions until the user approves them in the Events panel. Followup requires an explicit request to check in later and an RFC3339 time with timezone; otherwise omit it and set allow_followup false.", Parameters: json.RawMessage(`{"type":"object","properties":{"title":{"type":"string","maxLength":256},"content":{"type":"string","maxLength":4096},"evidence":{"type":"string","maxLength":1024},"event_at":{"type":["string","null"]},"followup_at":{"type":["string","null"]},"allow_followup":{"type":"boolean"}},"required":["title","content","evidence"],"additionalProperties":false}`), Execute: func(ctx context.Context, data json.RawMessage) (probe.ToolResult, error) {
		if err := ctx.Err(); err != nil {
			return probe.ToolResult{}, err
		}
		var args struct {
			Title         string     `json:"title"`
			Content       string     `json:"content"`
			Evidence      string     `json:"evidence"`
			EventAt       *time.Time `json:"event_at"`
			FollowupAt    *time.Time `json:"followup_at"`
			AllowFollowup bool       `json:"allow_followup"`
		}
		if err := toolInput(data, &args); err != nil {
			return probe.ToolResult{}, err
		}
		if !turnHasEvidence(inputs, args.Evidence) {
			return probe.ToolResult{}, errors.New("事件候选缺少当前用户消息中的依据")
		}
		item, err := s.life.AddEpisode(role, companion.EpisodeDraft{Title: args.Title, Content: args.Content, Status: "pending", EventAt: args.EventAt, FollowupAt: args.FollowupAt, AllowFollowup: args.AllowFollowup, SourceSessionID: sessionID, SourceText: args.Evidence})
		if err != nil {
			return probe.ToolResult{}, errors.New("事件候选保存失败，请在事件面板检查内容与时间")
		}
		out, _ := json.Marshal(item)
		return probe.ToolResult{Output: string(out)}, nil
	}}}
}
