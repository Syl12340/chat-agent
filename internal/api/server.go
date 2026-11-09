package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"chat-agent/internal/companion"
	"chat-agent/internal/decision"
	"chat-agent/internal/deviceauth"
	"chat-agent/internal/imagegen"
	"chat-agent/internal/interactive"
	"chat-agent/internal/media"
	"chat-agent/internal/memory"
	"chat-agent/internal/pluginapi"
	"chat-agent/internal/probe"
	"chat-agent/internal/roles"
	"chat-agent/internal/runlog"
	"chat-agent/internal/semantic"
	"chat-agent/internal/tasks"
	"chat-agent/internal/topics"
	"chat-agent/internal/turns"
	"chat-agent/internal/webtools"
	"chat-agent/plugins/builtin/clock"
	"chat-agent/plugins/builtin/search"
	"chat-agent/web"
)

type Config struct {
	Agent             probe.Config
	DataDir           string
	AccessToken       string
	RolesDir          string
	TimeZone          string
	Web               webtools.Config
	AndroidReleaseDir string
	Speech            SpeechConfig
	Semantic          semantic.Config
	Decision          decision.Config
	Images            imagegen.Config
	// ImageRemote, when set, replaces Images: the GPU lives on another machine
	// and is reached through a tunnel.
	ImageRemote            imagegen.RemoteConfig
	ChatModels             []ChatModel
	ThinkingMaxOutput      int
	TurnCompletionObserver TurnCompletionObserver
	OneBot                 OneBotConfig
	HostTools              HostToolsConfig
	// Plugins is the host's decision about installed plugins: which load, and
	// which declared permissions are really granted. The zero value enables
	// everything and grants nothing.
	Plugins pluginapi.Policy
}

type runState struct {
	cancel context.CancelFunc
	done   chan struct{}
	queued bool
}
type Server struct {
	config          Config
	tokenHash       [32]byte
	mu              sync.Mutex
	sessions        map[string]*probe.Session
	runs            map[string]*runState
	closing         bool
	roles           *roles.Manager
	memory          *memory.Store
	companion       *companion.Store
	life            *companion.LifeStore
	activities      *companion.ActivityStore
	devices         *deviceauth.Store
	turns           *turns.Store
	runtime         *runlog.Registry
	workCtx         context.Context
	workCancel      context.CancelFunc
	workWG          sync.WaitGroup
	careSlots       chan struct{}
	tasks           *tasks.Store
	web             *webtools.Client
	location        *time.Location
	schedulerCancel context.CancelFunc
	schedulerDone   chan struct{}
	schedulerOK     atomic.Bool
	speechSlots     chan struct{}
	speechAttempt   time.Time
	decision        *decision.Client
	imagegen        imagegen.Generator
	plugins         *pluginapi.Catalog
	// pluginPolicy is the host's grant for the loaded catalog. Capability
	// admission and host permissions both read it, so "declared" never silently
	// becomes "authorized".
	pluginPolicy pluginapi.Policy
	// approvals holds host-issued, single-use authorizations. It is the only
	// source of consent for a call that could not be assessed.
	approvals      *probe.ApprovalStore
	topics         *topics.Store
	topicSource    topicGateway
	topicSweepMu   sync.Mutex
	push           *pushStore
	pushSlots      chan struct{}
	images         *media.Store
	mediaMu        sync.Mutex
	activeImageJob imageWork // protected by mediaMu
	onebot         *oneBotIntegration
	turnObserver   TurnCompletionObserver
}

func New(c Config) (*Server, error) {
	hostPlugins, err := c.hostPlugins()
	if err != nil {
		return nil, err
	}
	if c.ThinkingMaxOutput < 0 {
		return nil, errors.New("thinking output budget cannot be negative (0 uses provider default)")
	}
	defaultModel := c.Agent.Model
	if defaultModel == "" {
		defaultModel = "demo"
	}
	if !validChatModels(c.ChatModels, defaultModel) {
		return nil, errors.New("invalid chat model allowlist")
	}
	if len(c.AccessToken) < 32 {
		return nil, errors.New("CHAT_ACCESS_TOKEN must contain at least 32 characters")
	}
	if _, err := probe.New(c.Agent, probe.NewSession()); err != nil {
		return nil, err
	}
	if c.DataDir == "" {
		return nil, errors.New("data directory is required")
	}
	if c.TimeZone == "" {
		c.TimeZone = "Asia/Shanghai"
	}
	location, err := time.LoadLocation(c.TimeZone)
	if err != nil {
		return nil, errors.New("invalid user time zone")
	}
	path, err := filepath.Abs(c.DataDir)
	if err != nil {
		return nil, err
	}
	c.DataDir = path
	if err := os.MkdirAll(filepath.Join(path, probe.AgentID), 0700); err != nil {
		return nil, err
	}
	if c.RolesDir == "" {
		c.RolesDir = filepath.Join(path, "roles")
	}
	roleManager, err := roles.Open(c.RolesDir)
	if err != nil {
		return nil, err
	}
	if _, err := roleManager.List(); err != nil {
		return nil, err
	}
	mem, err := memory.Open(filepath.Join(path, "memory"))
	if err != nil {
		return nil, err
	}
	if reranker := semantic.New(c.Semantic); reranker != nil {
		mem.SetReranker(reranker)
	}
	taskStore, err := tasks.Open(filepath.Join(path, "tasks"))
	if err != nil {
		mem.Close()
		return nil, err
	}
	webClient, err := webtools.New(c.Web)
	if err != nil {
		taskStore.Close()
		mem.Close()
		return nil, err
	}
	companionStore, err := companion.Open(filepath.Join(path, "companion"))
	if err != nil {
		webClient.Close()
		taskStore.Close()
		mem.Close()
		return nil, err
	}
	lifeStore, err := companion.OpenLife(filepath.Join(path, "life"))
	if err != nil {
		companionStore.Close()
		webClient.Close()
		taskStore.Close()
		mem.Close()
		return nil, err
	}
	runtimeStore, err := runlog.OpenRegistry(filepath.Join(path, "runtime"))
	if err != nil {
		lifeStore.Close()
		companionStore.Close()
		webClient.Close()
		taskStore.Close()
		mem.Close()
		return nil, err
	}
	if err = runtimeStore.RequireMigrated(path); err != nil {
		runtimeStore.Close()
		lifeStore.Close()
		companionStore.Close()
		webClient.Close()
		taskStore.Close()
		mem.Close()
		return nil, err
	}
	turnStore, err := turns.OpenResolved(filepath.Join(path, "turns"), runtimeStore.TurnsPath)
	if err != nil {
		runtimeStore.Close()
		lifeStore.Close()
		companionStore.Close()
		webClient.Close()
		taskStore.Close()
		mem.Close()
		return nil, err
	}
	activityStore, err := companion.OpenActivities(filepath.Join(path, "activities"))
	if err != nil {
		turnStore.Close()
		runtimeStore.Close()
		lifeStore.Close()
		companionStore.Close()
		webClient.Close()
		taskStore.Close()
		mem.Close()
		return nil, err
	}
	deviceStore, err := deviceauth.Open(filepath.Join(path, "devices"))
	if err != nil {
		webClient.Close()
		return nil, errors.Join(err, activityStore.Close(), turnStore.Close(), runtimeStore.Close(), lifeStore.Close(), companionStore.Close(), taskStore.Close(), mem.Close())
	}
	topicStore, err := topics.Open(filepath.Join(path, "topics"))
	if err != nil {
		webClient.Close()
		return nil, errors.Join(err, deviceStore.Close(), activityStore.Close(), turnStore.Close(), runtimeStore.Close(), lifeStore.Close(), companionStore.Close(), taskStore.Close(), mem.Close())
	}
	workCtx, workCancel := context.WithCancel(context.Background())
	push, err := openPush(filepath.Join(path, "delivery"))
	if err != nil {
		workCancel()
		webClient.Close()
		return nil, errors.Join(err, topicStore.Close(), deviceStore.Close(), activityStore.Close(), turnStore.Close(), runtimeStore.Close(), lifeStore.Close(), companionStore.Close(), taskStore.Close(), mem.Close())
	}
	images, err := media.Open(filepath.Join(path, "images"))
	if err != nil {
		workCancel()
		webClient.Close()
		return nil, errors.Join(err, push.close(), topicStore.Close(), deviceStore.Close(), activityStore.Close(), turnStore.Close(), runtimeStore.Close(), lifeStore.Close(), companionStore.Close(), taskStore.Close(), mem.Close())
	}
	s := &Server{decision: decision.New(c.Decision), imagegen: imageGenerator(c), approvals: probe.NewApprovalStore(), devices: deviceStore, activities: activityStore, life: lifeStore, turns: turnStore, runtime: runtimeStore, workCtx: workCtx, workCancel: workCancel, careSlots: make(chan struct{}, 1), config: c, tokenHash: sha256.Sum256([]byte(c.AccessToken)), sessions: map[string]*probe.Session{}, runs: map[string]*runState{}, roles: roleManager, memory: mem, companion: companionStore, tasks: taskStore, web: webClient, location: location}
	// Built-in plugins: each one is a Go value registered statically. A plugin
	// whose dependencies are missing lands in Catalog.Unavailable instead of
	// failing the process, so one missing capability never blacks out the rest.
	loader := pluginapi.NewLoader()
	loader.SetPolicy(c.Plugins)
	loader.Add(search.New(webClient))
	loader.Add(clock.Plugin{})
	for _, p := range hostPlugins {
		loader.Add(p)
	}
	pluginCatalog, err := loader.Load()
	if err != nil {
		return nil, err
	}
	if err := pluginCatalog.StartAll(workCtx, func(manifest pluginapi.Manifest) pluginapi.Host {
		return newPluginHost(manifest, c.DataDir, c.Plugins)
	}); err != nil {
		return nil, err
	}
	s.plugins = pluginCatalog
	s.pluginPolicy = c.Plugins

	s.speechSlots = make(chan struct{}, 1)
	s.topics = topicStore
	s.topicSource = webClient
	s.push = push
	s.images = images
	s.pushSlots = make(chan struct{}, 4)
	onebot, err := newOneBotIntegration(s, c.OneBot, filepath.Join(path, "channels", "onebot"))
	if err != nil {
		workCancel()
		webClient.Close()
		return nil, errors.Join(err, images.Close(), push.close(), topicStore.Close(), deviceStore.Close(), activityStore.Close(), turnStore.Close(), runtimeStore.Close(), lifeStore.Close(), companionStore.Close(), taskStore.Close(), mem.Close())
	}
	s.onebot = onebot
	s.turnObserver = combineCompletionObservers(c.TurnCompletionObserver, onebot)
	s.startScheduler()
	s.startTopicScheduler()
	s.startPushScheduler()
	return s, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]string{"status": "ok"}) })
	if s.onebot != nil {
		mux.HandleFunc("POST /hooks/onebot/v1/events", s.oneBotWebhook)
	}
	mux.HandleFunc("/hooks/", func(w http.ResponseWriter, r *http.Request) { fail(w, http.StatusNotFound, "接口不存在") })
	mux.HandleFunc("GET /api/status", s.status)
	mux.HandleFunc("POST /api/approvals", s.approveCall)
	mux.HandleFunc("GET /api/plugins", s.listPlugins)
	mux.HandleFunc("GET /api/chat/options", s.chatOptions)
	mux.HandleFunc("POST /api/sessions/{id}/images", s.uploadImage)
	mux.HandleFunc("GET /api/sessions/{id}/images/{image}", s.readImage)
	mux.HandleFunc("DELETE /api/sessions/{id}/images/{image}", s.deleteImage)
	mux.HandleFunc("GET /api/sessions/{id}/image-jobs/{job}", s.imageJobHTTP)
	mux.HandleFunc("POST /api/sessions/{id}/image-jobs/{job}/cancel", s.imageJobHTTP)
	mux.HandleFunc("GET /api/push/settings", s.getPushSettings)
	mux.HandleFunc("PATCH /api/push/settings", s.updatePushSettings)
	mux.HandleFunc("GET /api/push/inbox", s.pushInbox)
	mux.HandleFunc("POST /api/device/register", s.registerDevice)
	mux.HandleFunc("POST /api/device/challenge", s.challengeDevice)
	mux.HandleFunc("POST /api/device/verify", s.verifyDevice)
	mux.HandleFunc("POST /api/device/logout", s.logoutDevice)
	mux.HandleFunc("GET /api/devices", s.listDevices)
	mux.HandleFunc("PATCH /api/devices/{id}", s.updateDevice)
	mux.HandleFunc("POST /api/voice/speech", s.speech)
	mux.HandleFunc("GET /api/updates/android", s.androidUpdate)
	mux.HandleFunc("GET /api/updates/android/{sha}/app.apk", s.androidAPK)
	mux.HandleFunc("GET /api/sessions", s.list)
	mux.HandleFunc("POST /api/sessions", s.create)
	mux.HandleFunc("DELETE /api/sessions/{id}", s.deleteSession)
	mux.HandleFunc("GET /api/sessions/{id}/turns", s.listTurns)
	mux.HandleFunc("POST /api/sessions/{id}/turns", s.enqueueTurn)
	mux.HandleFunc("POST /api/sessions/{id}/turns/resume", s.resumeTurns)
	mux.HandleFunc("DELETE /api/sessions/{id}/turns/{request}", s.withdrawTurn)
	mux.HandleFunc("GET /api/episodes", s.listEpisodes)
	mux.HandleFunc("POST /api/episodes", s.createEpisode)
	mux.HandleFunc("PATCH /api/episodes/{id}", s.updateEpisode)
	mux.HandleFunc("DELETE /api/episodes/{id}", s.deleteEpisode)
	mux.HandleFunc("GET /api/care/settings", s.getCareSettings)
	mux.HandleFunc("GET /api/topics/settings", s.getTopicSettings)
	mux.HandleFunc("PATCH /api/topics/settings", s.updateTopicSettings)
	mux.HandleFunc("GET /api/topics", s.listTopics)
	mux.HandleFunc("PATCH /api/topics/{id}", s.markTopic)
	mux.HandleFunc("DELETE /api/topics/{id}", s.deleteTopic)
	mux.HandleFunc("PATCH /api/care/settings", s.updateCareSettings)
	mux.HandleFunc("GET /api/care", s.listCare)
	mux.HandleFunc("POST /api/care/check", s.checkCare)
	mux.HandleFunc("POST /api/care/{id}/generate", s.generateCare)
	mux.HandleFunc("PATCH /api/care/{id}", s.updateCare)
	mux.HandleFunc("GET /api/activities", s.listActivities)
	mux.HandleFunc("POST /api/activities", s.createActivity)
	mux.HandleFunc("PATCH /api/activities/{id}", s.updateActivity)
	mux.HandleFunc("DELETE /api/activities/{id}", s.deleteActivity)
	mux.HandleFunc("POST /api/activities/{id}/attach", s.attachActivity)
	mux.HandleFunc("GET /api/sessions/{id}/messages", s.history)
	mux.HandleFunc("POST /api/sessions/{id}/messages", s.message)
	mux.HandleFunc("POST /api/sessions/{id}/cancel", s.cancel)
	mux.HandleFunc("POST /api/sessions/{id}/reset", s.reset)
	mux.HandleFunc("POST /api/sessions/{id}/compact", s.compact)
	mux.HandleFunc("GET /api/roles", s.listRoles)
	mux.HandleFunc("GET /api/roles/{id}", s.getRole)
	mux.HandleFunc("PATCH /api/roles/{id}", s.updateRole)
	mux.HandleFunc("GET /api/roles/{id}/proposals", s.listRoleProposals)
	mux.HandleFunc("PATCH /api/roles/{id}/proposals/{proposal}", s.reviewRoleProposal)
	mux.HandleFunc("DELETE /api/roles/{id}/proposals/{proposal}", s.deleteRoleProposal)
	mux.HandleFunc("GET /api/roles/{id}/companion", s.getCompanion)
	mux.HandleFunc("PATCH /api/roles/{id}/companion", s.updateCompanion)
	mux.HandleFunc("GET /api/memory", s.listMemory)
	mux.HandleFunc("POST /api/memory", s.createMemory)
	mux.HandleFunc("GET /api/memory/export", s.exportMemory)
	mux.HandleFunc("PATCH /api/memory/{id}", s.updateMemory)
	mux.HandleFunc("DELETE /api/memory/{id}", s.deleteMemory)
	mux.HandleFunc("GET /api/todos", s.listTasks)
	mux.HandleFunc("POST /api/todos", s.createTask)
	mux.HandleFunc("PATCH /api/todos/{id}", s.updateTask)
	mux.HandleFunc("DELETE /api/todos/{id}", s.deleteTask)
	mux.HandleFunc("GET /api/reminders", s.listTasks)
	mux.HandleFunc("POST /api/reminders", s.createTask)
	mux.HandleFunc("PATCH /api/reminders/{id}", s.updateTask)
	mux.HandleFunc("DELETE /api/reminders/{id}", s.deleteTask)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) { fail(w, 404, "接口不存在") })
	mux.Handle("/", web.Handler())
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' blob:; connect-src 'self'; media-src 'self' blob:; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		if strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/api" {
			w.Header().Set("Cache-Control", "no-store")
			if origin := r.Header.Get("Origin"); origin != "" {
				u, err := url.Parse(origin)
				if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host != r.Host {
					deviceFail(w, 403, "cross_origin_denied", "不允许跨站访问")
					return
				}
			}
			if !publicDeviceRequest(r) {
				var ok bool
				r, ok = s.authenticateDeviceRequest(w, r)
				if !ok {
					return
				}
			}
		}
		mux.ServeHTTP(w, r)
	})
}

// version is the service version reported by /api/status.
const version = "0.30.0"

func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	model := s.config.Agent.Model
	if s.config.Agent.Demo {
		model = "chat-agent Demo"
	}
	identity := currentAuth(r)
	writeJSON(w, 200, map[string]any{"name": "chat-agent", "version": version, "auth_method": identity.Method, "device_id": identity.Device.ID, "device_admin": identity.Method == "owner" || identity.Device.Admin, "speech_enabled": s.speechEnabled(), "speech_max_chars": 1200, "model": model, "demo": s.config.Agent.Demo, "max_output": s.config.Agent.MaxOutputTokens, "thinking_max_output": s.config.ThinkingMaxOutput, "timeout_seconds": s.config.Agent.Timeout.Seconds(), "time_zone": s.location.String(), "web_fetch": s.web.FetchEnabled(), "web_search": s.web.SearchEnabled(), "image_generation": s.imageAvailable(), "image_reason": s.imageUnavailableReason(), "reminder_delivery": "inbox", "scheduler_ok": s.schedulerOK.Load(), "onebot_enabled": s.onebot != nil})
}

func validID(id string) bool {
	if len(id) == 0 || len(id) > 128 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

func (s *Server) session(id string) (*probe.Session, error) {
	if !validID(id) {
		return nil, os.ErrNotExist
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return nil, errors.New("server is shutting down")
	}
	if sess := s.sessions[id]; sess != nil {
		return sess, nil
	}
	historyPath, err := s.runtime.HistoryPath(id)
	if err != nil {
		return nil, err
	}
	sess, err := probe.OpenRuntimeSession(historyPath)
	if err != nil {
		return nil, err
	}
	s.sessions[id] = sess
	return sess, nil
}

func (s *Server) get(w http.ResponseWriter, r *http.Request) (*probe.Session, bool) {
	sess, err := s.session(r.PathValue("id"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			fail(w, 404, "会话不存在")
		} else {
			fail(w, 503, "会话不可用，请检查数据目录或锁文件")
		}
		return nil, false
	}
	return sess, true
}

type sessionInfo struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	UpdatedAt   time.Time `json:"updated_at"`
	Active      bool      `json:"active"`
	Unavailable bool      `json:"unavailable,omitempty"`
	RoleID      string    `json:"role_id"`
}

func title(messages []probe.Message) string {
	for _, m := range messages {
		if m.Role == "user" {
			if strings.TrimSpace(m.Content) == "" && len(m.Images) > 0 {
				return "图片对话"
			}
			r := []rune(strings.Join(strings.Fields(m.Content), " "))
			if len(r) > 32 {
				return string(r[:32]) + "…"
			}
			if len(r) > 0 {
				return string(r)
			}
		}
	}
	return "新会话"
}

func (s *Server) list(w http.ResponseWriter, r *http.Request) {
	entries, err := s.runtime.Entries()
	if err != nil {
		fail(w, 500, "无法读取会话列表")
		return
	}
	items := []sessionInfo{}
	for _, entry := range entries {
		item := sessionInfo{ID: entry.ID, Title: "无法读取的会话", UpdatedAt: entry.UpdatedAt, RoleID: entry.RoleID}
		sess, err := s.session(entry.ID)
		if err != nil {
			item.Unavailable = true
		} else {
			item.Title = title(sess.Messages())
			item.RoleID = sess.RoleID()
		}
		s.mu.Lock()
		item.Active = s.runs[entry.ID] != nil
		s.mu.Unlock()
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].UpdatedAt.After(items[j].UpdatedAt) })
	writeJSON(w, 200, items)
}

func (s *Server) create(w http.ResponseWriter, r *http.Request) {
	var input struct {
		RoleID string `json:"role_id"`
	}
	if r.Body != nil {
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		decoder.DisallowUnknownFields()
		err := decoder.Decode(&input)
		if err != nil && err != io.EOF {
			fail(w, 400, "角色参数无效")
			return
		}
		if err == nil {
			var extra any
			if decoder.Decode(&extra) != io.EOF {
				fail(w, 400, "角色参数无效")
				return
			}
		}
	}
	if input.RoleID == "" {
		input.RoleID = "companion"
	}
	if _, err := s.roles.Load(input.RoleID); err != nil {
		fail(w, 400, "角色不存在或配置无效")
		return
	}
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		fail(w, 500, "无法创建会话")
		return
	}
	id := hex.EncodeToString(b[:])
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		fail(w, 503, "服务正在关闭")
		return
	}
	if err := s.runtime.Create(id, input.RoleID); err != nil {
		fail(w, 500, "无法创建会话")
		return
	}
	historyPath, err := s.runtime.HistoryPath(id)
	if err != nil {
		fail(w, 500, "无法读取新会话")
		return
	}
	sess, err := probe.OpenRuntimeSession(historyPath)
	if err != nil {
		fail(w, 500, "无法打开新会话")
		return
	}
	s.sessions[id] = sess
	writeJSON(w, 201, sessionInfo{ID: id, Title: "新会话", UpdatedAt: time.Now().UTC(), RoleID: input.RoleID})
}

func (s *Server) history(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.get(w, r)
	if !ok {
		return
	}
	s.mu.Lock()
	active := s.runs[r.PathValue("id")] != nil
	s.mu.Unlock()
	items, paused, err := s.turns.List(r.PathValue("id"))
	if err != nil {
		turnFailure(w, err)
		return
	}
	activity, err := s.activities.Session(sess.RoleID(), r.PathValue("id"))
	if err != nil {
		fail(w, 503, "活动暂时不可用，请稍后重试")
		return
	}
	floor, err := s.uiContextFloor(sess, r.PathValue("id"))
	if err != nil {
		fail(w, 503, "互动上下文暂时不可用")
		return
	}
	writeJSON(w, 200, map[string]any{"id": r.PathValue("id"), "messages": visibleMessages(sess.Messages()), "active": active, "role_id": sess.RoleID(), "summary": sess.Summary(), "turns": items, "queue_paused": paused, "activity": activity, "ui_context_floor": floor})
}

func (s *Server) message(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Content string `json:"content"`
		Mode    string `json:"mode"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 128<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		fail(w, 400, "消息格式无效或请求过大")
		return
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF || strings.TrimSpace(input.Content) == "" || len(input.Content) > 64<<10 || !companion.ValidMode(input.Mode) {
		fail(w, 400, "娑堟伅椤讳负闈炵┖鏂囨湰锛屾渶澶?64 KiB")
		return
	}
	if _, reserved, _ := interactive.ParseResponse(input.Content); reserved {
		fail(w, 400, "互动答案请通过消息队列提交")
		return
	}
	sess, ok := s.get(w, r)
	if !ok {
		return
	}
	if _, ok := w.(http.Flusher); !ok {
		fail(w, 500, "服务不支持流式响应")
		return
	}
	id := r.PathValue("id")
	ctx, cancel := context.WithCancel(r.Context())
	state := &runState{cancel: cancel, done: make(chan struct{})}
	var requestBytes [16]byte
	if _, err := rand.Read(requestBytes[:]); err != nil {
		cancel()
		fail(w, 500, "无法创建运行记录")
		return
	}
	requestID := hex.EncodeToString(requestBytes[:])
	s.mu.Lock()
	if s.closing || s.runs[id] != nil || len(s.runs) >= 4 {
		closing, busy := s.closing, s.runs[id] != nil
		s.mu.Unlock()
		cancel()
		if closing {
			fail(w, 503, "服务正在关闭")
		} else if busy {
			fail(w, 409, "此会话正在生成，请先停止")
		} else {
			fail(w, 429, "同时生成的会话过多，请稍后重试")
		}
		return
	}
	recorder, err := runlog.Open(s.runtime.Root(), runlog.Scope{SessionID: id, RoleID: sess.RoleID(), RequestID: requestID})
	if err != nil {
		s.mu.Unlock()
		cancel()
		fail(w, 503, "运行记录暂时不可用；没有调用模型")
		return
	}
	agent, err := s.agentForTurn(sess, []string{input.Content}, id, func(c *probe.Config) {
		c.RequestID = requestID
		c.Recorder = recorder
	})
	if err != nil {
		// The client gets a fixed sentence, but an operator needs the reason: this
		// path covers role, memory, activity and tool assembly failures, and they
		// look identical from the outside.
		log.Printf("session agent unavailable: %v", err)
		recorder.Close()
		s.mu.Unlock()
		cancel()
		fail(w, 503, "模型或陪伴上下文暂时不可用")
		return
	}
	s.runs[id] = state
	s.mu.Unlock()
	var once sync.Once
	finish := func() {
		once.Do(func() { cancel(); s.mu.Lock(); delete(s.runs, id); close(state.done); s.mu.Unlock() })
	}
	defer finish()
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("X-Accel-Buffering", "no")
	controller := http.NewResponseController(w)
	send := func(event string, data any) {
		_ = controller.SetWriteDeadline(time.Now().Add(10 * time.Second))
		b, _ := json.Marshal(data)
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b); err != nil {
			cancel()
			return
		}
		if err := controller.Flush(); err != nil {
			cancel()
		}
	}
	send("ready", map[string]string{"id": id})
	var toolEvent turns.ToolEvent
	var toolStarted time.Time
	err = probe.Run(ctx, agent, s.config.Agent.Timeout, input.Content, func(ev probe.Event) {
		switch ev.Type {
		case probe.EventTextDelta:
			send("text_delta", map[string]string{"text": ev.Text})
		case probe.EventReasoningDelta:
			send("reasoning_delta", map[string]string{"text": ev.Text})
		case probe.EventToolCallStart:
			toolStarted = time.Now()
			toolEvent = startToolView(ev)
			send("tool_start", toolEvent)
		case probe.EventToolResult:
			finishToolView(&toolEvent, ev.Result, time.Since(toolStarted))
			payload := map[string]any{"id": toolEvent.ID, "name": toolEvent.Name, "output": toolEvent.Output, "error": toolEvent.Error, "code": ev.Result.Code, "view": toolEvent.View, "status": toolEvent.Status, "elapsed_ms": toolEvent.ElapsedMS, "approval": toolEvent.Approval}
			// The decision trace is only useful to the client when it can offer a
			// human confirmation, so it travels with the result that needs one.
			if ev.Result.Decision != nil {
				payload["decision"] = ev.Result.Decision
			}
			send("tool_result", payload)
		case probe.EventCompactionStart:
			send("compaction_start", map[string]bool{"active": true})
		case probe.EventCompactionDone:
			send("compaction_done", map[string]bool{"changed": ev.Text != "skipped"})
		}
	})
	err = errors.Join(err, recorder.Close())
	finish()
	if err == nil {
		send("done", map[string]bool{"ok": true})
		return
	}
	if errors.Is(err, context.Canceled) {
		send("canceled", map[string]string{"message": "已停止生成"})
		return
	}
	code, message := generationFailure(err)
	log.Printf("generation failed: category=%s", code)
	send("error", map[string]string{"message": message})
}

func (s *Server) cancel(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.get(w, r); !ok {
		return
	}
	if err := s.turns.Pause(r.PathValue("id"), true); err != nil {
		turnFailure(w, err)
		return
	}
	s.mu.Lock()
	state := s.runs[r.PathValue("id")]
	s.mu.Unlock()
	if state != nil {
		state.cancel()
		select {
		case <-state.done:
		case <-r.Context().Done():
			return
		}
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) reset(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.get(w, r)
	if !ok {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.runs[r.PathValue("id")] != nil {
		fail(w, 409, "请先停止生成再重置")
		return
	}
	if err := sess.Reset(); err != nil {
		fail(w, 500, "无法重置会话")
		return
	}
	if err := s.turns.Reset(r.PathValue("id")); err != nil {
		turnFailure(w, err)
		return
	}
	if err := s.life.ResetFloor(sess.RoleID(), r.PathValue("id")); err != nil {
		fail(w, 503, "会话已清空，但上下文边界暂时不可用")
		return
	}
	if err := s.activities.ResetFloor(sess.RoleID(), r.PathValue("id")); err != nil {
		fail(w, 503, "会话已清空，但活动上下文边界暂时不可用")
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// Close cancels runs, joins them, then releases session files and writer locks.
func (s *Server) Close(ctx context.Context) error {
	s.mu.Lock()
	s.closing = true
	s.workCancel()
	states := make([]*runState, 0, len(s.runs))
	for _, state := range s.runs {
		states = append(states, state)
	}
	sessions := make([]*probe.Session, 0, len(s.sessions))
	for _, sess := range s.sessions {
		sessions = append(sessions, sess)
	}
	s.mu.Unlock()
	for _, state := range states {
		state.cancel()
	}
	s.schedulerCancel()
	select {
	case <-s.schedulerDone:
	case <-ctx.Done():
		return context.Cause(ctx)
	}
	for _, state := range states {
		select {
		case <-state.done:
		case <-ctx.Done():
			return context.Cause(ctx)
		}
	}
	joined := make(chan struct{})
	go func() { s.workWG.Wait(); close(joined) }()
	select {
	case <-joined:
	case <-ctx.Done():
		return context.Cause(ctx)
	}
	var err error
	if s.plugins != nil {
		s.plugins.StopAll(ctx)
	}
	if s.onebot != nil {
		err = errors.Join(err, s.onebot.Close(ctx))
	}
	for _, sess := range sessions {
		err = errors.Join(err, sess.Close())
	}
	err = errors.Join(err, s.memory.Close())
	err = errors.Join(err, s.tasks.Close())
	err = errors.Join(err, s.companion.Close(), s.life.Close(), s.turns.Close(), s.runtime.Close(), s.activities.Close())
	err = errors.Join(err, s.devices.Close())
	err = errors.Join(err, s.topics.Close())
	err = errors.Join(err, s.push.close())
	err = errors.Join(err, s.images.Close())
	s.web.Close()
	return err
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}
func fail(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
