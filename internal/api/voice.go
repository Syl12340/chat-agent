package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

// Speech is opt-in and uses server-side gateway credentials. The agent and
// scheduler never request it; one explicit user action synthesizes one clip.
type SpeechConfig struct {
	Model string
	Voice string
}

func (s *Server) speechEnabled() bool {
	return !s.config.Agent.Demo && s.config.Speech.Model != "" && s.config.Agent.APIKey != ""
}

func (s *Server) speech(w http.ResponseWriter, r *http.Request) {
	if !s.speechEnabled() {
		fail(w, 503, "服务器尚未开启语音朗读")
		return
	}
	var input struct {
		Input string `json:"input"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF || !utf8.ValidString(input.Input) {
		fail(w, 400, "朗读内容无效")
		return
	}
	input.Input = strings.TrimSpace(input.Input)
	if input.Input == "" || utf8.RuneCountInString(input.Input) > 1200 {
		fail(w, 400, "单次朗读需要 1–1200 个字符，请选择较短的回答")
		return
	}
	select {
	case s.speechSlots <- struct{}{}:
		defer func() { <-s.speechSlots }()
	default:
		fail(w, 429, "已有朗读正在合成，请稍后再试")
		return
	}
	s.mu.Lock()
	allowed := !s.closing && time.Since(s.speechAttempt) >= 10*time.Second
	if allowed {
		s.speechAttempt = time.Now()
	}
	s.mu.Unlock()
	if !allowed {
		fail(w, 429, "请间隔至少 10 秒再生成朗读")
		return
	}
	voice := s.config.Speech.Voice
	if voice == "" {
		voice = "female_sweet"
	}
	body, _ := json.Marshal(map[string]any{"model": s.config.Speech.Model, "voice": voice, "input": input.Input, "response_format": "mp3", "speed": 1.0})
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	stop := context.AfterFunc(s.workCtx, cancel)
	defer stop()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(s.config.Agent.BaseURL, "/")+"/audio/speech", bytes.NewReader(body))
	if err != nil {
		fail(w, 503, "语音服务地址无效")
		return
	}
	req.Header.Set("Authorization", "Bearer "+s.config.Agent.APIKey)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(req)
	if err != nil {
		fail(w, 502, "语音合成未完成，没有自动重试")
		return
	}
	defer response.Body.Close()
	mediaType, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if response.StatusCode != 200 || mediaType != "audio/mpeg" {
		fail(w, 502, "语音服务暂不可用，没有自动重试")
		return
	}
	const maxAudio = 2 << 20
	audio, err := io.ReadAll(io.LimitReader(response.Body, maxAudio+1))
	if err != nil || len(audio) == 0 || len(audio) > maxAudio {
		fail(w, 502, "语音文件不完整或超过大小限制，没有自动重试")
		return
	}
	w.Header().Set("Content-Type", "audio/mpeg")
	w.Header().Set("Content-Disposition", `inline; filename="reply.mp3"`)
	w.WriteHeader(200)
	_, _ = w.Write(audio)
}
