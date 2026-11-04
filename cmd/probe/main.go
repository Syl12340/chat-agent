// probe is a single-turn compatibility CLI, not an HTTP server.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"

	"pocket-agent/internal/probe"
)

func main() {
	if err := run(); err != nil {
		message := err.Error()
		if secret := os.Getenv("POCKET_API_KEY"); secret != "" {
			message = strings.ReplaceAll(message, secret, "[REDACTED]")
		}
		fmt.Fprintln(os.Stderr, "probe:", message)
		os.Exit(1)
	}
}

func run() (runErr error) {
	c := probe.DefaultConfig()
	flag.DurationVar(&c.Timeout, "timeout", c.Timeout, "maximum run duration")
	flag.IntVar(&c.MaxTurns, "max-turns", c.MaxTurns, "maximum model/tool loop turns")
	flag.IntVar(&c.MaxToolCalls, "max-tools", c.MaxToolCalls, "maximum executed tools per run")
	flag.IntVar(&c.MaxOutputTokens, "max-output", c.MaxOutputTokens, "output tokens per generation")
	flag.StringVar(&c.TokenParameter, "token-parameter", c.TokenParameter, "max_tokens or max_completion_tokens")
	key := flag.String("session", "p0", "session key (reuse to resume)")
	data := flag.String("data", "data/sessions", "private session directory")
	ephemeral := flag.Bool("ephemeral", false, "in-memory session, no persistence")
	flag.Parse()
	c.BaseURL, c.APIKey, c.Model = os.Getenv("POCKET_BASE_URL"), os.Getenv("POCKET_API_KEY"), os.Getenv("POCKET_MODEL")
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	sess := probe.NewSession()
	// Validate configuration before touching the data directory.
	agent, err := probe.New(c, sess)
	if err != nil {
		return err
	}
	if !*ephemeral {
		sess, err = probe.OpenSession(*data, probe.AgentID, *key)
		if err != nil {
			return err
		}
		agent, err = probe.New(c, sess)
		if err != nil {
			sess.Close()
			return err
		}
	}
	defer func() { runErr = errors.Join(runErr, sess.Close()) }()
	prompt := strings.Join(flag.Args(), " ")
	if prompt == "" {
		prompt = "What is the current UTC time? Call get_current_time and then answer using its result."
	}
	err = probe.Run(ctx, agent, c.Timeout, prompt, func(ev probe.Event) {
		switch ev.Type {
		case probe.EventTextDelta:
			fmt.Print(ev.Text)
		case probe.EventToolCallStart:
			if ev.ToolCall != nil {
				fmt.Fprintf(os.Stderr, "[tool: %s]\n", ev.ToolCall.Function.Name)
			}
		case probe.EventToolResult:
			if ev.Result != nil && ev.Result.Error != "" {
				fmt.Fprintln(os.Stderr, "[tool failed]")
			}
		}
	})
	fmt.Println()
	return err
}
