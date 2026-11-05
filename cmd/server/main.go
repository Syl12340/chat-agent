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
	"strconv"
	"strings"
	"syscall"
	"time"

	"pocket-agent/internal/api"
	"pocket-agent/internal/probe"
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

func run() error {
	c := probe.DefaultConfig()
	listen := flag.String("listen", "127.0.0.1:8080", "HTTP listen address")
	wireListen := flag.String("wire-listen", os.Getenv("POCKET_WIRE_LISTEN"), "optional encrypted TCP listen address")
	wireKey := flag.String("wire-key", os.Getenv("POCKET_WIRE_KEY"), "P-256 server signing private key PEM file")
	data := flag.String("data", "", "session directory (default data/sessions, or data/demo-sessions in demo mode)")
	rolesDir := flag.String("roles", "", "role workspace directory (default <data>/roles)")
	timeZone := flag.String("timezone", "Asia/Shanghai", "user timezone for reminder interpretation")
	webEnabled := flag.Bool("enable-web", false, "enable explicitly configured HTTPS web tools")
	flag.IntVar(&c.ContextBytes, "context-bytes", c.ContextBytes, "effective history bytes before automatic compaction; 0 disables")
	flag.IntVar(&c.KeepRecentTurns, "keep-turns", c.KeepRecentTurns, "recent user turns preserved when compacting")
	flag.BoolVar(&c.Demo, "demo", false, "explicit local demo without a model API")
	flag.DurationVar(&c.Timeout, "timeout", c.Timeout, "maximum generation duration")
	flag.IntVar(&c.MaxTurns, "max-turns", c.MaxTurns, "maximum model/tool loop turns")
	flag.IntVar(&c.MaxToolCalls, "max-tools", c.MaxToolCalls, "maximum executed tools per run")
	flag.IntVar(&c.MaxOutputTokens, "max-output", c.MaxOutputTokens, "output tokens per generation")
	flag.StringVar(&c.TokenParameter, "token-parameter", c.TokenParameter, "max_tokens or max_completion_tokens")
	flag.Parse()
	if *listen == "" && *wireListen == "" {
		return errors.New("at least one HTTP or encrypted TCP listener is required")
	}
	if (*wireListen == "") != (*wireKey == "") {
		return errors.New("wire-listen and wire-key must be configured together")
	}
	if *data == "" {
		*data = "data/sessions"
		if c.Demo {
			*data = "data/demo-sessions"
		}
	}
	c.BaseURL, c.APIKey, c.Model = os.Getenv("POCKET_BASE_URL"), os.Getenv("POCKET_API_KEY"), os.Getenv("POCKET_MODEL")
	c.UtilityModel = utilityModel(c.Model)
	chatModels, err := api.ParseChatModels(os.Getenv("POCKET_CHAT_MODELS"))
	if err != nil {
		return err
	}
	thinkingMax := 4096
	if value := os.Getenv("POCKET_THINKING_MAX_OUTPUT"); value != "" {
		thinkingMax, err = strconv.Atoi(value)
		if err != nil {
			return errors.New("invalid POCKET_THINKING_MAX_OUTPUT")
		}
	}
	app, err := api.New(api.Config{Agent: c, ChatModels: chatModels, ThinkingMaxOutput: thinkingMax, Speech: api.SpeechConfig{Model: os.Getenv("POCKET_TTS_MODEL"), Voice: os.Getenv("POCKET_TTS_VOICE")}, DataDir: *data, RolesDir: *rolesDir, TimeZone: *timeZone, Web: webtools.Config{Enabled: *webEnabled, AllowedHosts: strings.Split(os.Getenv("POCKET_WEB_HOSTS"), ","), SearchURL: searchEngine(*webEnabled)}, AccessToken: os.Getenv("POCKET_ACCESS_TOKEN"), AndroidReleaseDir: os.Getenv("POCKET_ANDROID_RELEASE_DIR"), Semantic: semantic.Config{BaseURL: os.Getenv("POCKET_BASE_URL"), APIKey: os.Getenv("POCKET_API_KEY"), Model: os.Getenv("POCKET_RERANK_MODEL")}})
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
