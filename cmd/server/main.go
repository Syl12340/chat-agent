package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"pocket-agent/internal/api"
	"pocket-agent/internal/decision"
	"pocket-agent/internal/imagegen"
	"pocket-agent/internal/pluginapi"
	"pocket-agent/internal/probe"
	"pocket-agent/internal/runlog"
	"pocket-agent/internal/securewire"
	"pocket-agent/internal/semantic"
	"pocket-agent/internal/webtools"
)

func main() {
	if err := run(); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}

// splitList parses a comma-separated allow-list, dropping blanks so an unset
// variable disables the capability instead of inventing entries.
func splitList(raw string) []string {
	var out []string
	for _, item := range strings.Split(raw, ",") {
		if trimmed := strings.TrimSpace(item); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// decisionModel names the structured-decision model used by the decide tool.
// It is separate from the chat model and refuses to inherit it: asking a chat
// model to answer a decision request is a misconfiguration, not a fallback.
func decisionModel() string {
	if value := strings.TrimSpace(os.Getenv("POCKET_DECISION_MODEL")); value != "" {
		return value
	}
	return "ecnu-decision"
}

// utilityModel picks the model that summarizes history. Summaries are validated
// and rejected on failure, so a cheaper model is safe there and never faces the
// user; POCKET_UTILITY_MODEL overrides the default.
func utilityModel(primary string) string {
	if value := strings.TrimSpace(os.Getenv("POCKET_UTILITY_MODEL")); value != "" {
		return value
	}
	// ecnu-max costs three times ecnu-plus per token; keep the flagship for the
	// conversation and summarize on the small model.
	if primary == "ecnu-max" {
		return "ecnu-plus"
	}
	return primary
}

// searchEngine returns the public results page the agent reads directly.
// POCKET_SEARCH_URL overrides it; -enable-web falls back to a default engine so
// a single flag turns on both search and page reading.
func searchEngine(enabled bool) string {
	if value := strings.TrimSpace(os.Getenv("POCKET_SEARCH_URL")); value != "" {
		return value
	}
	if enabled {
		return "https://cn.bing.com/search"
	}
	return ""
}

func oneBotConfigFromEnv() (api.OneBotConfig, error) {
	enabledValue := strings.ToLower(strings.TrimSpace(os.Getenv("ONEBOT_ENABLED")))
	enabled := enabledValue == "1" || enabledValue == "true" || enabledValue == "yes"
	if enabledValue != "" && !enabled && enabledValue != "0" && enabledValue != "false" && enabledValue != "no" {
		return api.OneBotConfig{}, errors.New("invalid ONEBOT_ENABLED")
	}
	config := api.OneBotConfig{
		Enabled:       enabled,
		AccountID:     strings.TrimSpace(os.Getenv("ONEBOT_ACCOUNT_ID")),
		OwnerID:       strings.TrimSpace(os.Getenv("ONEBOT_OWNER_QQ_ID")),
		IngressSecret: os.Getenv("ONEBOT_INGRESS_SECRET"),
		ActionBaseURL: strings.TrimSpace(os.Getenv("ONEBOT_ACTION_BASE_URL")),
		ActionToken:   os.Getenv("ONEBOT_ACTION_TOKEN"),
		RoleID:        strings.TrimSpace(os.Getenv("ONEBOT_ROLE_ID")),
	}
	if value := strings.TrimSpace(os.Getenv("ONEBOT_TIMEOUT")); value != "" {
		timeout, err := time.ParseDuration(value)
		if err != nil || timeout <= 0 {
			return api.OneBotConfig{}, errors.New("invalid ONEBOT_TIMEOUT")
		}
		config.Timeout = timeout
	}
	if value := strings.TrimSpace(os.Getenv("ONEBOT_MAX_BODY_BYTES")); value != "" {
		limit, err := strconv.ParseInt(value, 10, 64)
		if err != nil || limit <= 0 {
			return api.OneBotConfig{}, errors.New("invalid ONEBOT_MAX_BODY_BYTES")
		}
		config.MaxBodyBytes = limit
	}
	return config, nil
}

func run() error {
	c := probe.DefaultConfig()
	listen := flag.String("listen", "127.0.0.1:8080", "HTTP listen address")
	wireListen := flag.String("wire-listen", os.Getenv("POCKET_WIRE_LISTEN"), "optional encrypted TCP listen address")
	wireKey := flag.String("wire-key", os.Getenv("POCKET_WIRE_KEY"), "P-256 server signing private key PEM file")
	data := flag.String("data", "", "session directory (default data/sessions, or data/demo-sessions in demo mode)")
	rolesDir := flag.String("roles", "", "role workspace directory (default <data>/roles)")
	timeZone := flag.String("timezone", "Asia/Shanghai", "user timezone for reminder interpretation")
	webEnabled := flag.Bool("enable-web", false, "enable explicitly configured HTTPS web tools")
	migrateRuntime := flag.Bool("migrate-runtime", false, "explicitly migrate stopped transcript/turn data to runtime v2 and exit")
	exportRuntime := flag.String("export-runtime", "", "export runtime conversations to a new transcript-v2/turns-v3 directory and exit")
	flag.IntVar(&c.ContextBytes, "context-bytes", c.ContextBytes, "effective history bytes before automatic compaction; 0 disables")
	flag.IntVar(&c.KeepRecentTurns, "keep-turns", c.KeepRecentTurns, "recent user turns preserved when compacting")
	flag.BoolVar(&c.Demo, "demo", false, "explicit local demo without a model API")
	flag.DurationVar(&c.Timeout, "timeout", c.Timeout, "maximum generation duration")
	flag.IntVar(&c.MaxTurns, "max-turns", c.MaxTurns, "maximum model/tool loop turns")
	flag.IntVar(&c.MaxToolCalls, "max-tools", c.MaxToolCalls, "maximum executed tools per run")
	flag.IntVar(&c.MaxOutputTokens, "max-output", c.MaxOutputTokens, "output tokens per generation; 0 omits the limit (provider default)")
	flag.StringVar(&c.TokenParameter, "token-parameter", c.TokenParameter, "max_tokens or max_completion_tokens")
	flag.Parse()
	if *data == "" {
		*data = "data/sessions"
		if c.Demo {
			*data = "data/demo-sessions"
		}
	}
	if *migrateRuntime && *exportRuntime != "" {
		return errors.New("migrate-runtime and export-runtime are mutually exclusive")
	}
	if *migrateRuntime || *exportRuntime != "" {
		registry, err := runlog.OpenRegistry(filepath.Join(*data, "runtime"))
		if err != nil {
			return err
		}
		if *migrateRuntime {
			report, migrateErr := registry.MigrateLegacy(*data)
			ownershipErr := registry.AdoptParentOwnership()
			closeErr := registry.Close()
			if err = errors.Join(migrateErr, ownershipErr, closeErr); err != nil {
				return err
			}
			fmt.Printf("chat-agent: runtime migration complete (migrated=%d resumed=%d skipped=%d)\n", report.Migrated, report.Resumed, report.Skipped)
			return nil
		}
		exportErr := registry.ExportLegacy(*data, *exportRuntime)
		closeErr := registry.Close()
		if err = errors.Join(exportErr, closeErr); err != nil {
			return err
		}
		fmt.Printf("chat-agent: runtime compatibility export complete (%s)\n", *exportRuntime)
		return nil
	}
	if *listen == "" && *wireListen == "" {
		return errors.New("at least one HTTP or encrypted TCP listener is required")
	}
	if (*wireListen == "") != (*wireKey == "") {
		return errors.New("wire-listen and wire-key must be configured together")
	}
	c.BaseURL, c.APIKey, c.Model = os.Getenv("POCKET_BASE_URL"), os.Getenv("POCKET_API_KEY"), os.Getenv("POCKET_MODEL")
	c.UtilityModel = utilityModel(c.Model)
	chatModels, err := api.ParseChatModels(os.Getenv("POCKET_CHAT_MODELS"))
	if err != nil {
		return err
	}
	thinkingMax := 0
	if value := os.Getenv("POCKET_THINKING_MAX_OUTPUT"); value != "" {
		thinkingMax, err = strconv.Atoi(value)
		if err != nil {
			return errors.New("invalid POCKET_THINKING_MAX_OUTPUT")
		}
	}
	onebotConfig, err := oneBotConfigFromEnv()
	if err != nil {
		return err
	}
	// Installed plugins are compiled in, but loading and permissions stay a host
	// decision: POCKET_PLUGINS_DISABLED switches a plugin off entirely, and
	// POCKET_PLUGIN_GRANTS grants the declared permissions one by one.
	pluginPolicy, err := pluginapi.NewPolicy(
		splitList(os.Getenv("POCKET_PLUGINS_DISABLED")),
		splitList(os.Getenv("POCKET_PLUGIN_GRANTS")),
	)
	if err != nil {
		return err
	}
	app, err := api.New(api.Config{Agent: c, ChatModels: chatModels, ThinkingMaxOutput: thinkingMax, Speech: api.SpeechConfig{Model: os.Getenv("POCKET_TTS_MODEL"), Voice: os.Getenv("POCKET_TTS_VOICE")}, DataDir: *data, RolesDir: *rolesDir, TimeZone: *timeZone, Web: webtools.Config{Enabled: *webEnabled, AllowedHosts: strings.Split(os.Getenv("POCKET_WEB_HOSTS"), ","), SearchURL: searchEngine(*webEnabled)}, AccessToken: os.Getenv("POCKET_ACCESS_TOKEN"), AndroidReleaseDir: os.Getenv("POCKET_ANDROID_RELEASE_DIR"), Semantic: semantic.Config{BaseURL: os.Getenv("POCKET_BASE_URL"), APIKey: os.Getenv("POCKET_API_KEY"), Model: os.Getenv("POCKET_RERANK_MODEL")}, Decision: decision.Config{BaseURL: os.Getenv("POCKET_BASE_URL"), APIKey: os.Getenv("POCKET_API_KEY"), Model: decisionModel()}, ImageRemote: imagegen.RemoteConfig{BaseURL: os.Getenv("POCKET_IMAGE_REMOTE"), Token: os.Getenv("POCKET_IMAGE_TOKEN")}, Images: imagegen.Config{Python: os.Getenv("POCKET_IMAGE_PYTHON"), Script: os.Getenv("POCKET_IMAGE_BRIDGE"), Profiles: splitList(os.Getenv("POCKET_IMAGE_PROFILES")), DefaultProfile: os.Getenv("POCKET_IMAGE_PROFILE")}, OneBot: onebotConfig, Plugins: pluginPolicy, HostTools: api.HostToolsConfig{Sessions: splitList(os.Getenv("POCKET_HOST_TOOL_SESSIONS")), WorkspaceRoot: os.Getenv("POCKET_WORKSPACE_ROOT"), WorkspaceActions: os.Getenv("POCKET_WORKSPACE_ACTIONS")}})
	if err != nil {
		return err
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = app.Close(ctx)
	}()
	type endpoint struct {
		server   *http.Server
		listener net.Listener
		scheme   string
	}
	var endpoints []endpoint
	defer func() {
		for _, endpoint := range endpoints {
			_ = endpoint.server.Close()
			_ = endpoint.listener.Close()
		}
	}()
	for _, binding := range []struct{ address, scheme string }{{*listen, "http"}, {*wireListen, "pocket"}} {
		if binding.address == "" {
			continue
		}
		var listener net.Listener
		var err error
		if binding.scheme == "pocket" {
			listener, err = securewire.Listen(binding.address, *wireKey)
		} else {
			listener, err = net.Listen("tcp", binding.address)
		}
		if err != nil {
			return err
		}
		srv := &http.Server{Handler: app.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
		endpoints = append(endpoints, endpoint{srv, listener, binding.scheme})
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	done := make(chan error, len(endpoints))
	mode := "configured model"
	if c.Demo {
		mode = "local demo (no model API)"
	}
	for _, endpoint := range endpoints {
		go func() {
			done <- endpoint.server.Serve(endpoint.listener)
		}()
		host := endpoint.listener.Addr().String()
		if strings.HasPrefix(host, "[::]") {
			host = "localhost" + strings.TrimPrefix(host, "[::]")
		}
		fmt.Printf("chat-agent: %s://%s (%s)\n", endpoint.scheme, host, mode)
	}
	select {
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		appErr := app.Close(shutdown)
		var serverErrors []error
		for _, endpoint := range endpoints {
			serverErrors = append(serverErrors, endpoint.server.Shutdown(shutdown))
		}
		return errors.Join(append(serverErrors, appErr)...)
	}
}
