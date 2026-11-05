package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// This is an explicit server allowlist. The browser cannot choose an endpoint,
// supply provider credentials, or turn an embedding/image generator into chat.
type ChatModel struct {
	ID       string   `json:"id"`
	Label    string   `json:"label"`
	Vision   bool     `json:"vision"`
	Thinking []string `json:"thinking"`
}

func (s *Server) chatModels() []ChatModel {
	if len(s.config.ChatModels) > 0 {
		return s.config.ChatModels
	}
	u, _ := url.Parse(s.config.Agent.BaseURL)
	if u != nil && u.Scheme == "https" && strings.EqualFold(u.Host, "chat.ecnu.edu.cn") && strings.TrimRight(u.Path, "/") == "/open/api/v1" {
		models := []ChatModel{{"ecnu-max", "ECNU Max", true, []string{"off", "medium", "high", "ultra"}}, {"ecnu-plus", "ECNU Plus", true, []string{"off", "medium", "high", "ultra"}}}
		if id := s.config.Agent.Model; id != "" && id != "ecnu-max" && id != "ecnu-plus" {
			models = append(models, ChatModel{id, id, false, []string{"off"}})
		}
		return models
	}
	id := s.config.Agent.Model
	if id == "" {
		id = "demo"
	}
	return []ChatModel{{id, id, false, []string{"off"}}}
}

func (s *Server) chatOptions(w http.ResponseWriter, r *http.Request) {
	defaultModel := s.config.Agent.Model
	if defaultModel == "" {
		defaultModel = "demo"
	}
	writeJSON(w, 200, map[string]any{"default_model": defaultModel, "default_thinking": "off", "models": s.chatModels()})
}

type chatSelection struct {
	Model, Thinking, Effort, ProviderThinking string
	Vision                                    bool
}

func (s *Server) selectChat(model, thinking string) (chatSelection, error) {
	if model == "" {
		model = s.config.Agent.Model
		if model == "" {
			model = "demo"
		}
	}
	if thinking == "" {
		thinking = "off"
	}
	for _, candidate := range s.chatModels() {
		if candidate.ID != model {
			continue
		}
		allowed := false
		for _, level := range candidate.Thinking {
			allowed = allowed || level == thinking
		}
		if !allowed {
			return chatSelection{}, errors.New("所选模型不支持这个思考档位，请重新选择")
		}
		choice := chatSelection{Model: model, Thinking: thinking, Vision: candidate.Vision}
		if (model == "ecnu-max" || model == "ecnu-plus") && len(candidate.Thinking) > 1 {
			choice.ProviderThinking = "disabled"
			if thinking != "off" {
				choice.ProviderThinking = "enabled"
				if model == "ecnu-max" {
					choice.Effort = map[string]string{"medium": "low", "high": "high", "ultra": "max"}[thinking]
				} else {
					choice.Effort = map[string]string{"medium": "low", "high": "medium", "ultra": "xhigh"}[thinking]
				}
			}
		} else if thinking != "off" {
			return chatSelection{}, errors.New("此模型尚未配置思考参数协议")
		}
		return choice, nil
	}
	return chatSelection{}, errors.New("模型未在服务器的对话允许列表中")
}

func (s *Server) chatTimeout(choice chatSelection) time.Duration {
	if choice.Thinking != "off" {
		return max(s.config.Agent.Timeout, 120*time.Second)
	}
	return s.config.Agent.Timeout
}

// Public config is deliberately narrow; it never exposes Config/APIKey.
func validChatModels(models []ChatModel, defaultModel string) bool {
	if len(models) == 0 {
		return true
	}
	if len(models) > 16 {
		return false
	}
	seen := map[string]bool{}
	found := false
	for _, model := range models {
		if strings.TrimSpace(model.ID) == "" || len(model.ID) > 128 || seen[model.ID] || len(model.Label) > 128 || len(model.Thinking) == 0 || len(model.Thinking) > 4 {
			return false
		}
		seen[model.ID] = true
		found = found || model.ID == defaultModel
		off := false
		levels := map[string]bool{}
		for _, level := range model.Thinking {
			if levels[level] || (level != "off" && level != "medium" && level != "high" && level != "ultra") {
				return false
			}
			if level != "off" && model.ID != "ecnu-max" && model.ID != "ecnu-plus" {
				return false
			}
			levels[level] = true
			off = off || level == "off"
		}
		if !off {
			return false
		}
	}
	return found
}

// Reserved for an operator's explicit allowlist; no dynamic provider/model
// discovery on every page load and no silent fallback on generation failure.
func ParseChatModels(value string) ([]ChatModel, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}
	var models []ChatModel
	d := json.NewDecoder(strings.NewReader(value))
	d.DisallowUnknownFields()
	var extra any
	if len(value) > 16<<10 || d.Decode(&models) != nil || d.Decode(&extra) != io.EOF {
		return nil, errors.New("invalid POCKET_CHAT_MODELS JSON")
	}
	return models, nil
}
