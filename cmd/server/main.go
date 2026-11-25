package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
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

	"chat-agent/internal/api"
	"chat-agent/internal/decision"
	"chat-agent/internal/imagegen"
	"chat-agent/internal/pluginapi"
	"chat-agent/internal/probe"
	"chat-agent/internal/runlog"
	"chat-agent/internal/sandbox"
	"chat-agent/internal/securewire"
	"chat-agent/internal/semantic"
	"chat-agent/internal/storage"
	"chat-agent/internal/webtools"
	workspaceplugin "chat-agent/plugins/builtin/workspace"
)

func main() {
	// The confinement has to be applied by the process being confined, so the
	// sandbox runs as this same binary re-executed with a reserved first
	// argument. It is checked before flags are parsed and before any state is
	// opened: a mistaken invocation then fails as a sandbox problem rather than
	// starting a second server.
	if len(os.Args) > 1 && os.Args[1] == sandbox.Marker {
		policy, argv, err := sandbox.ParseArgs(os.Args[2:])
		if err == nil {
			err = sandbox.Run(policy, argv)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(70)
		}
		return
	}
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
	if value := strings.TrimSpace(os.Getenv("CHAT_DECISION_MODEL")); value != "" {
		return value
	}
	return "ecnu-decision"
}

// utilityModel picks the model that summarizes history. Summaries are validated
// and rejected on failure, so a cheaper model is safe there and never faces the
// user; CHAT_UTILITY_MODEL overrides the default.
func utilityModel(primary string) string {
	if value := strings.TrimSpace(os.Getenv("CHAT_UTILITY_MODEL")); value != "" {
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
// CHAT_SEARCH_URL overrides it; -enable-web falls back to a default engine so
// a single flag turns on both search and page reading.
func searchEngine(enabled bool) string {
	if value := strings.TrimSpace(os.Getenv("CHAT_SEARCH_URL")); value != "" {
		return value
	}
	if enabled {
		// Keep this a bare results-page URL: startup validates that it carries no
		// query parameters. The engine flags that actually change what is returned
		// belong on the request, where they can depend on the query.
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

func envBool(name string) (bool, error) {
	value := strings.ToLower(strings.TrimSpace(os.Getenv(name)))
	switch value {
	case "", "0", "false", "no":
		return false, nil
	case "1", "true", "yes":
		return true, nil
	default:
		return false, fmt.Errorf("invalid %s", name)
	}
}

func qqOfficialConfigFromEnv() (api.QQOfficialConfig, error) {
	enabled, err := envBool("QQ_OFFICIAL_ENABLED")
	if err != nil {
		return api.QQOfficialConfig{}, err
	}
	allowGroups, err := envBool("QQ_OFFICIAL_ALLOW_GROUPS")
	if err != nil {
		return api.QQOfficialConfig{}, err
	}
	allowGroupAll, err := envBool("QQ_OFFICIAL_ALLOW_GROUP_ALL")
	if err != nil {
		return api.QQOfficialConfig{}, err
	}
	if allowGroupAll && !allowGroups {
		return api.QQOfficialConfig{}, errors.New("QQ_OFFICIAL_ALLOW_GROUP_ALL requires QQ_OFFICIAL_ALLOW_GROUPS")
	}
	return api.QQOfficialConfig{Enabled: enabled, AppID: strings.TrimSpace(os.Getenv("QQ_OFFICIAL_APP_ID")), Secret: os.Getenv("QQ_OFFICIAL_SECRET"), RoleID: strings.TrimSpace(os.Getenv("QQ_OFFICIAL_ROLE_ID")), AllowGroups: allowGroups, AllowGroupAll: allowGroupAll}, nil
}

func astrBotConfigFromEnv() (api.AstrBotConfig, error) {
	enabled, err := envBool("ASTRBOT_BRIDGE_ENABLED")
	if err != nil {
		return api.AstrBotConfig{}, err
	}
	allowGroups, err := envBool("ASTRBOT_BRIDGE_ALLOW_GROUPS")
	if err != nil {
		return api.AstrBotConfig{}, err
	}
	config := api.AstrBotConfig{Enabled: enabled, Secret: os.Getenv("ASTRBOT_BRIDGE_SECRET"), RoleID: strings.TrimSpace(os.Getenv("ASTRBOT_BRIDGE_ROLE_ID")), AllowGroups: allowGroups}
	if value := strings.TrimSpace(os.Getenv("ASTRBOT_BRIDGE_MAX_BODY_BYTES")); value != "" {
		limit, parseErr := strconv.ParseInt(value, 10, 64)
		if parseErr != nil || limit <= 0 {
			return api.AstrBotConfig{}, errors.New("invalid ASTRBOT_BRIDGE_MAX_BODY_BYTES")
		}
		config.MaxBodyBytes = limit
	}
	if value := strings.TrimSpace(os.Getenv("ASTRBOT_BRIDGE_CLAIM_TIMEOUT")); value != "" {
		timeout, parseErr := time.ParseDuration(value)
		if parseErr != nil || timeout <= 0 {
			return api.AstrBotConfig{}, errors.New("invalid ASTRBOT_BRIDGE_CLAIM_TIMEOUT")
		}
		config.ClaimTimeout = timeout
	}
	return config, nil
}

func run() error {
	c := probe.DefaultConfig()
	desktopDefault := strings.Contains(strings.ToLower(filepath.Base(os.Args[0])), "desktop")
	listenDefault := "127.0.0.1:8080"
	if desktopDefault {
		listenDefault = "127.0.0.1:8765"
	}
	desktop := flag.Bool("desktop", desktopDefault, "run as a loopback desktop agent and open the local web UI")
	openDesktopBrowser := flag.Bool("open-browser", true, "open the desktop web UI after startup")
	listen := flag.String("listen", listenDefault, "HTTP listen address")
	wireListen := flag.String("wire-listen", os.Getenv("CHAT_WIRE_LISTEN"), "optional encrypted TCP listen address")
	wireKey := flag.String("wire-key", os.Getenv("CHAT_WIRE_KEY"), "P-256 server signing private key PEM file")
	data := flag.String("data", "", "session directory (default data/sessions, or data/demo-sessions in demo mode)")
	instance := flag.String("instance", "", "private cloud instance name")
	workspaceRoot := flag.String("workspace", "", "desktop workspace exposed to the local agent loop")
	rolesDir := flag.String("roles", "", "role workspace directory (default <data>/roles)")
	timeZone := flag.String("timezone", "Asia/Shanghai", "user timezone for reminder interpretation")
	webEnabled := flag.Bool("enable-web", false, "enable explicitly configured HTTPS web tools")
	migrateRuntime := flag.Bool("migrate-runtime", false, "explicitly migrate stopped transcript/turn data to runtime v2 and exit")
	exportRuntime := flag.String("export-runtime", "", "export runtime conversations to a new transcript-v2/turns-v3 directory and exit")
	flag.IntVar(&c.ContextBytes, "context-bytes", c.ContextBytes, "effective history bytes before automatic compaction; 0 disables")
	flag.IntVar(&c.KeepRecentTurns, "keep-turns", c.KeepRecentTurns, "recent user turns preserved when compacting")
	flag.BoolVar(&c.Demo, "demo", false, "explicit local demo without a model API")
	flag.DurationVar(&c.Timeout, "timeout", c.Timeout, "maximum generation duration")
	flag.IntVar(&c.MaxTurns, "max-turns", c.MaxTurns, "maximum model/tool loop turns; 0 continues until final response, cancel or timeout")
	flag.IntVar(&c.MaxToolCalls, "max-tools", c.MaxToolCalls, "maximum executed tools per run; 0 uses the continuous loop")
	flag.IntVar(&c.MaxOutputTokens, "max-output", c.MaxOutputTokens, "output tokens per generation; 0 omits the limit (provider default)")
	flag.StringVar(&c.TokenParameter, "token-parameter", c.TokenParameter, "max_tokens or max_completion_tokens")
	flag.Parse()
	if *instance != "" && (!storage.ValidID(*instance) || *desktop || !filepath.IsAbs(*data) || !filepath.IsAbs(os.Getenv("CHAT_WORKSPACE_ROOT")) || !loopbackAddress(*listen) || (*wireListen != "" && !loopbackAddress(*wireListen))) {
		return errors.New("private instances need explicit absolute storage and workspace paths, and loopback listeners")
	}
	if *desktop {
		if *wireListen != "" || *wireKey != "" {
			return errors.New("desktop mode only serves loopback HTTP")
		}
		if !loopbackAddress(*listen) {
			return errors.New("desktop mode must listen on loopback")
		}
		if *workspaceRoot == "" {
			*workspaceRoot = defaultDesktopWorkspace()
		}
		root, err := filepath.Abs(*workspaceRoot)
		if err != nil {
			return err
		}
		*workspaceRoot = root
	}
	if *data == "" {
		if *desktop {
			var err error
			*data, err = defaultDesktopData()
			if err != nil {
				return err
			}
		} else {
			*data = "data/sessions"
			if c.Demo {
				*data = "data/demo-sessions"
			}
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
	c.BaseURL, c.APIKey, c.Model = os.Getenv("CHAT_BASE_URL"), os.Getenv("CHAT_API_KEY"), os.Getenv("CHAT_MODEL")
	if *desktop && c.BaseURL == "" && c.APIKey == "" && c.Model == "" {
		// The local UI starts immediately. A user can add a real provider in its
		// existing model settings; selecting that route switches the turn out of
		// demo without restarting the desktop core.
		c.Demo = true
	}
	c.UtilityModel = utilityModel(c.Model)
	chatModels, err := api.ParseChatModels(os.Getenv("CHAT_CHAT_MODELS"))
	if err != nil {
		return err
	}
	thinkingMax := 0
	if value := os.Getenv("CHAT_THINKING_MAX_OUTPUT"); value != "" {
		thinkingMax, err = strconv.Atoi(value)
		if err != nil {
			return errors.New("invalid CHAT_THINKING_MAX_OUTPUT")
		}
	}
	onebotConfig, err := oneBotConfigFromEnv()
	if err != nil {
		return err
	}
	qqOfficialConfig, err := qqOfficialConfigFromEnv()
	if err != nil {
		return err
	}
	astrBotConfig, err := astrBotConfigFromEnv()
	if err != nil {
		return err
	}
	// Installed plugins are compiled in, but loading and permissions stay a host
	// decision: CHAT_PLUGINS_DISABLED switches a plugin off entirely, and
	// CHAT_PLUGIN_GRANTS grants the declared permissions one by one.
	grants := splitList(os.Getenv("CHAT_PLUGIN_GRANTS"))
	if *desktop || *instance != "" {
		grants = append(grants, workspaceplugin.ID+":"+workspaceplugin.Permission)
	}
	pluginPolicy, err := pluginapi.NewPolicy(
		splitList(os.Getenv("CHAT_PLUGINS_DISABLED")),
		grants,
	)
	if err != nil {
		return err
	}
	accessToken := os.Getenv("CHAT_ACCESS_TOKEN")
	if *desktop && accessToken == "" {
		accessToken, err = desktopAccessToken(*data)
		if err != nil {
			return err
		}
	}
	hostWorkspace := os.Getenv("CHAT_WORKSPACE_ROOT")
	workspaceActions := os.Getenv("CHAT_WORKSPACE_ACTIONS")
	if *desktop {
		hostWorkspace = *workspaceRoot
	}
	if *instance != "" {
		workspaceActions = ""
	}
	claimHash := os.Getenv("CHAT_INSTANCE_CLAIM_HASH")
	var claimExpires time.Time
	if *instance != "" && claimHash != "" {
		decoded, hashErr := hex.DecodeString(claimHash)
		seconds, timeErr := strconv.ParseInt(os.Getenv("CHAT_INSTANCE_CLAIM_EXPIRES"), 10, 64)
		if hashErr != nil || len(decoded) != 32 || timeErr != nil || seconds <= 0 {
			return errors.New("invalid private instance claim configuration")
		}
		claimHash = hex.EncodeToString(decoded)
		claimExpires = time.Unix(seconds, 0)
	}
	app, err := api.New(api.Config{Agent: c, ChatModels: chatModels, ThinkingMaxOutput: thinkingMax, Speech: api.SpeechConfig{Model: os.Getenv("CHAT_TTS_MODEL"), Voice: os.Getenv("CHAT_TTS_VOICE")}, DataDir: *data, RolesDir: *rolesDir, TimeZone: *timeZone, Web: webtools.Config{Enabled: *webEnabled, AllowedHosts: strings.Split(os.Getenv("CHAT_WEB_HOSTS"), ","), SearchURL: searchEngine(*webEnabled)}, AccessToken: accessToken, AndroidReleaseDir: os.Getenv("CHAT_ANDROID_RELEASE_DIR"), Semantic: semantic.Config{BaseURL: os.Getenv("CHAT_BASE_URL"), APIKey: os.Getenv("CHAT_API_KEY"), Model: os.Getenv("CHAT_RERANK_MODEL")}, Decision: decision.Config{BaseURL: os.Getenv("CHAT_BASE_URL"), APIKey: os.Getenv("CHAT_API_KEY"), Model: decisionModel()}, ImageRemote: imagegen.RemoteConfig{BaseURL: os.Getenv("CHAT_IMAGE_REMOTE"), Token: os.Getenv("CHAT_IMAGE_TOKEN")}, Images: imagegen.Config{Python: os.Getenv("CHAT_IMAGE_PYTHON"), Script: os.Getenv("CHAT_IMAGE_BRIDGE"), Profiles: splitList(os.Getenv("CHAT_IMAGE_PROFILES")), DefaultProfile: os.Getenv("CHAT_IMAGE_PROFILE")}, OneBot: onebotConfig, QQOfficial: qqOfficialConfig, AstrBot: astrBotConfig, Plugins: pluginPolicy, HostTools: api.HostToolsConfig{Sessions: splitList(os.Getenv("CHAT_HOST_TOOL_SESSIONS")), WorkspaceRoot: hostWorkspace, WorkspaceActions: workspaceActions, AutoGrant: *desktop, ServiceUnits: splitList(os.Getenv("CHAT_SERVICE_UNITS")), ServiceActions: os.Getenv("CHAT_SERVICE_ACTIONS")}, LocalAgent: api.LocalAgentConfig{Devices: api.ParseLocalAgentDevices(os.Getenv("CHAT_LOCAL_AGENT_DEVICES")), DefaultDevice: os.Getenv("CHAT_LOCAL_AGENT_DEVICE")}, DesktopLocal: *desktop, InstanceName: *instance, ClaimHash: claimHash, ClaimExpires: claimExpires})
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
	for _, binding := range []struct{ address, scheme string }{{*listen, "http"}, {*wireListen, "chat"}} {
		if binding.address == "" {
			continue
		}
		var listener net.Listener
		var err error
		if binding.scheme == "chat" {
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
	if *desktop && *openDesktopBrowser {
		launch := "http://" + endpoints[0].listener.Addr().String() + "/#access_token=" + accessToken
		if err := openBrowser(launch); err != nil {
			log.Printf("open local UI: %v", err)
		}
		fmt.Printf("chat-agent: local workspace %s\n", *workspaceRoot)
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

func loopbackAddress(address string) bool {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func defaultDesktopData() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "chat-agent", "desktop"), nil
}

func defaultDesktopWorkspace() string {
	home, err := os.UserHomeDir()
	if err != nil {
		path, _ := filepath.Abs(".")
		return path
	}
	documents := filepath.Join(home, "Documents")
	if info, err := os.Stat(documents); err == nil && info.IsDir() {
		return documents
	}
	return home
}

func desktopAccessToken(dataDir string) (string, error) {
	path := filepath.Join(dataDir, "desktop-access.token")
	if raw, err := os.ReadFile(path); err == nil {
		token := strings.TrimSpace(string(raw))
		if len(token) >= 32 && len(token) <= 256 {
			return token, nil
		}
		return "", errors.New("desktop access token is damaged")
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	token := hex.EncodeToString(random[:])
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return "", err
	}
	if err := storage.Replace(path, []byte(token+"\n")); err != nil {
		return "", err
	}
	return token, nil
}
