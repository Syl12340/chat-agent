package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"pocket-agent/internal/companion"
)

type pushItem struct {
	ID        string    `json:"id"`
	RoleID    string    `json:"role_id"`
	Kind      string    `json:"kind"`
	Title     string    `json:"title"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
	SessionID string    `json:"session_id,omitempty"`
}
type pushInbox struct {
	Cursor    string     `json:"cursor"`
	Items     []pushItem `json:"items"`
	Truncated bool       `json:"truncated"`
}

func (s *Server) getPushSettings(w http.ResponseWriter, r *http.Request) {
	auth := currentAuth(r)
	if auth.Method != "device" {
		deviceFail(w, 403, "push_device_required", "请用已批准的设备身份开启后台接收")
		return
	}
	writeJSON(w, 200, map[string]bool{"enabled": s.push.get(auth.Device.ID)})
}
func (s *Server) updatePushSettings(w http.ResponseWriter, r *http.Request) {
	auth := currentAuth(r)
	if auth.Method != "device" {
		deviceFail(w, 403, "push_device_required", "请用已批准的设备身份开启后台接收")
		return
	}
	var input struct {
		Enabled *bool `json:"enabled"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1024)
	if !decodeInput(w, r, &input) {
		return
	}
	if input.Enabled == nil {
		fail(w, 400, "请明确开启或关闭后台接收")
		return
	}
	if err := s.push.set(auth.Device.ID, *input.Enabled); err != nil {
		fail(w, 503, "后台接收设置暂时无法保存")
		return
	}
	writeJSON(w, 200, map[string]bool{"enabled": *input.Enabled})
}

func (s *Server) receiverOnline(now time.Time) bool {
	devices, err := s.devices.List("", true)
	if err != nil {
		return false
	}
	approved := make(map[string]bool, len(devices))
	for _, d := range devices {
		approved[d.ID] = d.Status == "approved"
	}
	return s.push.live(approved, now)
}

// Bounded snapshots use the original inboxes as authority, not a second copy of
// private messages. The cursor is a content digest, never an acknowledgement.
// GET does no model/search work and never marks the source record as seen.
func (s *Server) readPushInbox(now time.Time) (pushInbox, error) {
	out := pushInbox{Items: []pushItem{}}
	roles, err := s.roles.List()
	if err != nil {
		return out, err
	}
	if len(roles) > 32 {
		roles = roles[:32]
		out.Truncated = true
	}
	for _, role := range roles {
		p, err := s.life.Settings(role.ID)
		if err != nil {
			return out, err
		}
		if !careDeliveryAllowed(p, now, s.location) {
			continue
		}
		care, err := s.life.Care(role.ID)
		if err != nil {
			return out, err
		}
		for _, c := range care {
			if c.Status != "ready" || c.Content == "" || c.AttemptedAt == nil {
				continue
			}
			out.Items = append(out.Items, pushItem{ID: c.ID, RoleID: role.ID, Kind: "care", Title: c.Title, Content: c.Content, CreatedAt: *c.AttemptedAt, SessionID: c.SessionID})
		}
		topics, err := s.topics.Records(role.ID)
		if err != nil {
			return out, err
		}
		for _, item := range topics {
			if item.Status != "ready" || item.Content == "" {
				continue
			}
			out.Items = append(out.Items, pushItem{ID: item.ID, RoleID: role.ID, Kind: "topic", Title: "分享给你", Content: item.Content, CreatedAt: item.UpdatedAt, SessionID: item.SessionID})
		}
		reminders, err := s.tasks.List(role.ID, "reminder")
		if err != nil {
			return out, err
		}
		for _, item := range reminders {
			if item.Status != "due" || item.FiredAt == nil {
				continue
			}
			out.Items = append(out.Items, pushItem{ID: item.ID, RoleID: role.ID, Kind: "reminder", Title: "到期提醒", Content: item.Title, CreatedAt: *item.FiredAt})
		}
	}
	sort.Slice(out.Items, func(i, j int) bool {
		a, b := out.Items[i], out.Items[j]
		if !a.CreatedAt.Equal(b.CreatedAt) {
			return a.CreatedAt.Before(b.CreatedAt)
		}
		return a.RoleID+":"+a.Kind+":"+a.ID < b.RoleID+":"+b.Kind+":"+b.ID
	})
	if len(out.Items) > 100 {
		out.Items = out.Items[len(out.Items)-100:]
		out.Truncated = true
	}
	for i := range out.Items {
		out.Items[i].Title = boundedPushText(strings.Join(strings.Fields(out.Items[i].Title), " "), 256)
		out.Items[i].Content = boundedPushText(out.Items[i].Content, 4096)
	}
	// JSON escaping can expand text: bound encoded bytes and keep the newest
	// contiguous suffix. Source inboxes still retain all records.
	encodedBytes, start := 128, len(out.Items)
	for i := len(out.Items) - 1; i >= 0; i-- {
		item, err := json.Marshal(out.Items[i])
		if err != nil {
			return out, err
		}
		if encodedBytes+len(item)+1 > 900<<10 {
			break
		}
		encodedBytes += len(item) + 1
		start = i
	}
	if start > 0 {
		out.Items = out.Items[start:]
		out.Truncated = true
	}
	data, err := json.Marshal(out)
	if err != nil {
		return out, err
	}
	hash := sha256.Sum256(data)
	out.Cursor = hex.EncodeToString(hash[:])
	return out, nil
}

func boundedPushText(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	for limit > 0 && !utf8.RuneStart(value[limit]) {
		limit--
	}
	return value[:limit]
}

func (s *Server) pushInbox(w http.ResponseWriter, r *http.Request) {
	cursor := r.URL.Query().Get("cursor")
	if cursor != "" {
		decoded, err := hex.DecodeString(cursor)
		if err != nil || len(decoded) != sha256.Size {
			fail(w, 400, "接收游标无效")
			return
		}
	}
	wait := 0
	if raw := r.URL.Query().Get("wait"); raw != "" {
		var err error
		wait, err = strconv.Atoi(raw)
		if err != nil || wait < 0 || wait > 20 {
			fail(w, 400, "接收等待时间应为0到20秒")
			return
		}
	}
	select {
	case s.pushSlots <- struct{}{}:
		defer func() { <-s.pushSlots }()
	default:
		fail(w, 429, "后台接收连接已达上限")
		return
	}
	timeout := time.NewTimer(time.Duration(wait) * time.Second)
	defer timeout.Stop()
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for {
		// Recheck approval/session during long polls, including before publishing
		// a response. Revoking a device does not wait for the next TCP request.
		authenticated, ok := s.authenticateDeviceRequest(w, r)
		if !ok {
			return
		}
		auth := currentAuth(authenticated)
		if auth.Method == "device" && !s.push.touch(auth.Device.ID, time.Now()) {
			deviceFail(w, 409, "push_disabled", "此设备的后台接收已关闭")
			return
		}
		value, err := s.readPushInbox(time.Now())
		if err != nil {
			fail(w, 503, "消息收件箱暂时不可用")
			return
		}
		if cursor == "" || value.Cursor != cursor || wait == 0 {
			writeJSON(w, 200, value)
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-s.workCtx.Done():
			fail(w, 503, "服务正在关闭")
			return
		case <-timeout.C:
			wait = 0
		case <-tick.C:
		}
	}
}

func careDeliveryAllowed(p companion.CareSettings, now time.Time, location *time.Location) bool {
	if p.SnoozeUntil != nil && now.Before(*p.SnoozeUntil) {
		return false
	}
	minute := func(clock string) int {
		parsed, err := time.Parse("15:04", clock)
		if err != nil {
			return -1
		}
		return parsed.Hour()*60 + parsed.Minute()
	}
	start, end := minute(p.QuietStart), minute(p.QuietEnd)
	if start < 0 || end < 0 {
		return false
	}
	local := now.In(location)
	current := local.Hour()*60 + local.Minute()
	if start == end {
		return true
	}
	if start < end {
		return current < start || current >= end
	}
	return current < start && current >= end
}

func (s *Server) startPushScheduler() {
	s.workWG.Add(1)
	go func() {
		defer s.workWG.Done()
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-s.workCtx.Done():
				return
			case <-ticker.C:
			}
			if err := s.sweepBackgroundCare(time.Now()); err != nil && s.workCtx.Err() == nil {
				log.Print("background care could not settle state; no automatic generation replay")
			}
		}
	}()
}

// An opt-in alone is insufficient: a recently online receiver must exist. Thus
// upgrading never silently turns foreground care into a paid background task.
func (s *Server) sweepBackgroundCare(now time.Time) error {
	if !s.receiverOnline(now) {
		return nil
	}
	roles, err := s.roles.List()
	if err != nil {
		return err
	}
	for _, role := range roles {
		if s.workCtx.Err() != nil {
			return s.workCtx.Err()
		}
		s.mu.Lock()
		busy := s.closing || len(s.runs) > 0
		s.mu.Unlock()
		if busy || !s.receiverOnline(time.Now()) {
			return nil
		}
		p, err := s.life.Settings(role.ID)
		if err != nil {
			return err
		}
		if !p.Enabled || !p.AutoGenerate || !careDeliveryAllowed(p, now, s.location) {
			continue
		}
		if err = s.life.Tick(role.ID, now, s.location); err != nil {
			return err
		}
		items, err := s.life.Care(role.ID)
		if err != nil {
			return err
		}
		unread := false
		for _, c := range items {
			unread = unread || c.Status == "ready" || c.Status == "generating"
		}
		topicItems, err := s.topics.Records(role.ID)
		if err != nil {
			return err
		}
		for _, c := range topicItems {
			unread = unread || c.Status == "ready" || c.Status == "running"
		}
		sort.SliceStable(items, func(i, j int) bool { return items[i].DueAt.Before(items[j].DueAt) })
		for _, c := range items {
			if c.Status != "due" || c.DueAt.After(now) {
				continue
			}
			// Optional daily greetings don't catch up after two hours or stack
			// over unread items; explicit event followups retain their semantics.
			if c.EpisodeID == "" && (unread || now.Sub(c.DueAt) > 2*time.Hour) {
				continue
			}
			r, _ := http.NewRequestWithContext(s.workCtx, http.MethodPost, "http://internal/api/care/check", nil)
			_, err = s.runCare(r, role.ID, c.ID, true)
			if err != nil && err != companion.ErrCareBlocked {
				return err
			}
			break
		}
	}
	return nil
}
