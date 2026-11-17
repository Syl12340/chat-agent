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
		Feedback string `json:"feedback"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if !decodeInput(w, r, &input) {
		return
	}
	var item companion.CareItem
	var err error
	if input.Feedback != "" && input.Status == "" {
		item, err = s.life.SetCareFeedback(role, r.PathValue("id"), input.Revision, input.Feedback)
	} else if input.Status != "" && input.Feedback == "" {
		item, err = s.life.UpdateCare(role, r.PathValue("id"), input.Revision, input.Status)
	} else {
		err = companion.ErrInvalid
	}
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
			recent = append(recent, map[string]string{"content": string(content), "status": item.Status, "feedback": item.Feedback})
			if len(recent) == 3 {
				break
			}
		}
	}
	// A check-in that was never answered is not a reason to send another one.
	// This is decided here rather than in the prompt so it cannot drift: on
	// 2026-11-12 one quiet evening produced three "you should sleep" messages in
	// forty minutes, none of them answered. Only the automatic path is held back;
	// a person who asks for one now has already decided they want it.
	if automatic && s.unansweredCheckIn(role, history, now) {
		item, silenceErr := s.life.SilenceCare(role, id, claimed.Revision)
		if silenceErr != nil {
			return companion.CareItem{}, silenceErr
		}
		return item, nil
	}
	trigger := map[string]any{"origin": "care", "trigger_id": claimed.ID, "preferences": pref.Preferences, "event": episode, "approved_memories": facts, "recent_check_ins": recent, "scheduled_title": claimed.Title, "current_time": now.In(s.location).Format(time.RFC3339)}
	trigger["motivation"] = careMotivation(history, claimed, now)
	preferred := ""
	if episode != nil {
		preferred = episode.SourceSessionID
	}
	var settled companion.CareItem
	settledOK := false
	_, _, persisted, generateErr := s.runProactive(ctx, role, preferred, "care", claimed.ID, trigger, careFollowupPrompt, 160, timeout, func(sessionID string, response probe.Message, messageIndex int) (probe.Message, bool, error) {
		if strings.TrimSpace(response.Content) == "[沉默]" {
			item, finishErr := s.life.SilenceCare(role, id, claimed.Revision)
			if finishErr != nil {
				return probe.Message{}, false, finishErr
			}
			settled, settledOK = item, true
			return probe.Message{}, false, nil
		}
		item, finishErr := s.life.PrepareCareDelivery(role, id, claimed.Revision, response.Content, sessionID, messageIndex)
		if finishErr != nil {
			return probe.Message{}, false, finishErr
		}
		settled, settledOK = item, true
		if item.Status != "delivering" {
			return probe.Message{}, false, nil
		}
		return probe.Message{Role: "assistant", Content: item.Content, Origin: "proactive"}, true, nil
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
question. A short statement may stand alone and no reply is required. If there
is explicit recent feedback, treat "helpful" as a weak cue and "less" as a
strong instruction to avoid a similar topic, opening, question or tone. Do not
infer any broader personality trait or silently change the user's settings. If there
is no natural, truthful reason to speak now, return exactly [沉默]. Otherwise
return only the check-in text, without JSON, headings or a chat-ui block.
The motivation object is reference data about this opportunity, not permission
to speak by itself. motivation.source_kind says whether the user explicitly
allowed an event follow-up or enabled an optional daily greeting. The recent
feedback counts are bounded, explicit reactions, not personality evidence.
motivation.check_ins_for_this_episode counts what was already
sent about this same event: when it is one or more, do not repeat the same
concern, opening, question or advice unless something concrete has changed, and
prefer [沉默] when only the wording would be new. motivation.hours_since_due is
how long this opportunity waited: a large number means the moment has probably
passed, so speak only if the message still fits the current time.`

// checkInsForEpisode counts the check-ins already delivered about one event.
// The three 2026-11-12 messages had different episode IDs, so this is not a
// de-duplication rule; it is material the model can weigh when deciding whether
// there is still something new to say.
func checkInsForEpisode(history []companion.CareItem, episodeID string) int {
	if episodeID == "" {
		return 0
	}
	count := 0
	for _, item := range history {
		if item.EpisodeID != episodeID || item.Content == "" {
			continue
		}
		if item.Status == "ready" || item.Status == "seen" || item.Status == "delivering" {
			count++
		}
	}
	return count
}

// careMotivation describes why this opportunity exists now, in numbers the model
// can act on instead of an adjective it has to guess at.
func careMotivation(history []companion.CareItem, claimed companion.CareItem, now time.Time) map[string]any {
	helpful, less := 0, 0
	cutoff := now.Add(-30 * 24 * time.Hour)
	for _, item := range history {
		if item.CreatedAt.Before(cutoff) {
			continue
		}
		switch item.Feedback {
		case "helpful":
			helpful++
		case "less":
			less++
		}
	}
	motivation := map[string]any{
		"check_ins_for_this_episode": checkInsForEpisode(history, claimed.EpisodeID),
		"hours_since_due":            int(now.Sub(claimed.DueAt).Hours()),
		"recent_helpful_feedback":    helpful,
		"recent_less_feedback":       less,
		"source_kind":                "optional_daily_greeting",
	}
	if claimed.EpisodeID != "" {
		motivation["has_event"] = true
		motivation["source_kind"] = "approved_event_followup"
	}
	return motivation
}

// careUnansweredWindow bounds how long a check-in still counts as "just sent".
// Past it, quiet is more likely a different part of the day than someone
// ignoring the last message, so speaking again is fair.
const careUnansweredWindow = 12 * time.Hour

// newestCheckIn returns the freshest delivered check-in that has a known place
// in a conversation, which is what makes "did anyone answer it" answerable.
func newestCheckIn(history []companion.CareItem, now time.Time) (companion.CareItem, bool) {
	var last companion.CareItem
	var lastAt time.Time
	found := false
	for _, item := range history {
		if item.Content == "" || item.SessionID == "" || item.MessageIndex == nil {
			continue
		}
		if item.Status != "ready" && item.Status != "seen" && item.Status != "delivering" {
			continue
		}
		at := item.CreatedAt
		if item.AttemptedAt != nil {
			at = *item.AttemptedAt
		}
		if now.Sub(at) > careUnansweredWindow {
			continue
		}
		if !found || at.After(lastAt) {
			last, lastAt, found = item, at, true
		}
	}
	return last, found
}

// unansweredCheckIn reports whether the newest fresh check-in was ever answered.
// It only looks at the conversation the check-in landed in, so a user who moved
// to another conversation still counts as having replied.
func (s *Server) unansweredCheckIn(role string, history []companion.CareItem, now time.Time) bool {
	last, ok := newestCheckIn(history, now)
	if !ok {
		return false
	}
	id, session, err := s.proactiveSession(role, last.SessionID)
	if err != nil || session == nil || id != last.SessionID {
		return false
	}
	messages := session.Messages()
	if *last.MessageIndex < 0 || *last.MessageIndex >= len(messages) {
		return false
	}
	return !answeredAfter(messages, *last.MessageIndex)
}

// answeredAfter reports whether the user said anything after that message.
// Anything they say counts, because a check-in that nudged the conversation
// forward did its job even when the reply was about something else.
func answeredAfter(messages []probe.Message, index int) bool {
	for _, message := range messages[index+1:] {
		if message.Role == "user" {
			return true
		}
	}
	return false
}

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
