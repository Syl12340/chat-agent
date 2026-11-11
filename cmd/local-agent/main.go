package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"chat-agent/internal/localagent"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	workspace := flag.String("workspace", "", "absolute directory exposed to the local agent")
	state := flag.String("state", defaultState(), "absolute durable receipt file")
	actionsPath := flag.String("actions", "", "JSON file mapping action names to fixed argv arrays")
	server := flag.String("server", "", "HTTPS chat-agent origin")
	device := flag.String("device", "", "registered local agent device ID")
	tokenFile := flag.String("token-file", "", "file containing the device bearer token")
	stdio := flag.Bool("stdio", false, "read ca.local-agent.v1 tasks as JSON lines from stdin")
	flag.Parse()
	if *workspace == "" {
		return errors.New("-workspace is required")
	}
	guard, err := localagent.OpenGuard(*workspace)
	if err != nil {
		return err
	}
	defer guard.Close()
	store, err := localagent.OpenReceiptStore(*state)
	if err != nil {
		return err
	}
	actions, err := loadActions(*actionsPath)
	if err != nil {
		return err
	}
	executor := &localagent.Executor{Guard: guard, Store: store, Actions: actions, Timeout: 2 * time.Minute}
	if *stdio {
		return runStdio(executor)
	}
	if *server == "" || *device == "" || *tokenFile == "" {
		return errors.New("-server, -device and -token-file are required outside -stdio mode")
	}
	token, err := os.ReadFile(*tokenFile)
	if err != nil {
		return fmt.Errorf("read device token: %w", err)
	}
	carrier := &localagent.Carrier{BaseURL: *server, Device: *device, Token: strings.TrimSpace(string(token))}
	if err := carrier.Validate(); err != nil {
		return err
	}
	ctx := context.Background()
	for {
		task, err := carrier.Claim(ctx)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			time.Sleep(3 * time.Second)
			continue
		}
		if task == nil {
			time.Sleep(time.Second)
			continue
		}
		receipt := executor.Execute(ctx, *task)
		for carrier.Complete(ctx, receipt) != nil {
			time.Sleep(3 * time.Second)
		}
	}
}

func runStdio(executor *localagent.Executor) error {
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	encoder := json.NewEncoder(os.Stdout)
	for scanner.Scan() {
		var task localagent.Task
		if json.Unmarshal(scanner.Bytes(), &task) != nil {
			return errors.New("stdin contains an invalid local agent task")
		}
		if err := encoder.Encode(executor.Execute(context.Background(), task)); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func loadActions(path string) (map[string][]string, error) {
	if path == "" {
		return map[string][]string{}, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	actions := map[string][]string{}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	if decoder.Decode(&actions) != nil || len(actions) > 32 {
		return nil, errors.New("invalid local action allow-list")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return nil, errors.New("invalid local action allow-list")
	}
	for name, argv := range actions {
		if name == "" || len(name) > 64 || len(argv) == 0 || len(argv) > 32 || !filepath.IsAbs(argv[0]) {
			return nil, errors.New("each local action needs a name and fixed absolute executable")
		}
	}
	return actions, nil
}

func defaultState() string {
	base, err := os.UserConfigDir()
	if err != nil {
		path, _ := filepath.Abs("local-agent-state.json")
		return path
	}
	return filepath.Join(base, "chat-agent", "local-agent-state.json")
}
