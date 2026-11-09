package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"chat-agent/internal/modelconfig"
)

type modelConfigInput struct {
	ID       string   `json:"id"`
	Label    string   `json:"label"`
	BaseURL  string   `json:"base_url"`
	APIKey   *string  `json:"api_key"`
	Model    string   `json:"model"`
	Vision   bool     `json:"vision"`
	Thinking []string `json:"thinking"`
}

func (s *Server) listModelConfigs(w http.ResponseWriter, r *http.Request) {
	if !requireDeviceAdmin(w, r) {
		return
	}
	items := make([]modelconfig.PublicRoute, 0)
	for _, route := range s.modelConfigs.List() {
		items = append(items, modelconfig.Public(route))
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": items})
}

func (s *Server) createModelConfig(w http.ResponseWriter, r *http.Request) {
	if !requireDeviceAdmin(w, r) {
		return
	}
	input, ok := decodeModelConfig(w, r)
	if !ok {
		return
	}
	if input.APIKey == nil {
		fail(w, http.StatusBadRequest, "新增模型配置必须提供 API Key")
		return
	}
	if s.reservedModelID(input.ID) {
		fail(w, http.StatusConflict, "模型标识已被环境配置占用")
		return
	}
	if _, exists := s.modelConfigs.Get(input.ID); exists {
		fail(w, http.StatusConflict, "模型配置已存在")
		return
	}
	route, err := s.modelConfigs.Put(input.route(""))
	if err != nil {
		modelConfigFailure(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, modelconfig.Public(route))
}

func (s *Server) updateModelConfig(w http.ResponseWriter, r *http.Request) {
	if !requireDeviceAdmin(w, r) {
		return
	}
	input, ok := decodeModelConfig(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if input.ID != "" && input.ID != id {
		fail(w, http.StatusBadRequest, "模型标识不能修改")
		return
	}
	current, exists := s.modelConfigs.Get(id)
	if !exists {
		fail(w, http.StatusNotFound, "模型配置不存在")
		return
	}
	key := current.APIKey
	if input.APIKey != nil {
		key = *input.APIKey
	}
	input.ID = id
	route, err := s.modelConfigs.Put(input.route(key))
	if err != nil {
		modelConfigFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, modelconfig.Public(route))
}

func (s *Server) deleteModelConfig(w http.ResponseWriter, r *http.Request) {
	if !requireDeviceAdmin(w, r) {
		return
	}
	id := r.PathValue("id")
	if s.modelConfigInUse(id) {
		fail(w, http.StatusConflict, "模型仍被正在运行或排队的请求使用，请等待完成或取消后再删除")
		return
	}
	if err := s.modelConfigs.Delete(id); err != nil {
		modelConfigFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func decodeModelConfig(w http.ResponseWriter, r *http.Request) (modelConfigInput, bool) {
	var input modelConfigInput
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	d.DisallowUnknownFields()
	var extra any
	if d.Decode(&input) != nil || d.Decode(&extra) != io.EOF {
		fail(w, http.StatusBadRequest, "模型配置格式无效")
		return input, false
	}
	if input.APIKey != nil {
		key := strings.TrimSpace(*input.APIKey)
		input.APIKey = &key
	}
	return input, true
}

func (input modelConfigInput) route(key string) modelconfig.Route {
	if input.APIKey != nil {
		key = *input.APIKey
	}
	return modelconfig.Route{ID: input.ID, Label: input.Label, BaseURL: input.BaseURL, APIKey: key, Model: input.Model, Vision: input.Vision, Thinking: input.Thinking}
}

func modelConfigFailure(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, modelconfig.ErrInvalid):
		fail(w, http.StatusBadRequest, "模型配置无效，请检查标识、HTTPS 端点、模型名、API Key 和能力")
	case errors.Is(err, modelconfig.ErrNotFound):
		fail(w, http.StatusNotFound, "模型配置不存在")
	default:
		fail(w, http.StatusServiceUnavailable, "模型配置暂时无法保存")
	}
}

func (s *Server) reservedModelID(id string) bool {
	for _, model := range s.staticChatModels() {
		if model.ID == id {
			return true
		}
	}
	return false
}

func (s *Server) modelConfigInUse(id string) bool {
	s.mu.Lock()
	if len(s.runs) > 0 {
		s.mu.Unlock()
		return true
	}
	s.mu.Unlock()
	entries, err := s.runtime.Entries()
	if err != nil {
		return true
	}
	for _, entry := range entries {
		items, _, err := s.turns.List(entry.ID)
		if err != nil {
			return true
		}
		for _, turn := range items {
			if turn.Model == id && (turn.Status == "queued" || turn.Status == "running") {
				return true
			}
		}
	}
	return false
}
