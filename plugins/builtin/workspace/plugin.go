// Package workspace serves files and fixed actions in one host-approved directory.
package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"chat-agent/internal/pluginapi"
	"chat-agent/internal/probe"
)

const ID = "builtin.workspace"
const Permission = "filesystem:workspace://server"
const limit = 12 << 10

type Plugin struct {
	path    string
	root    *os.Root
	actions map[string][]string
}

func New(path, dataDir, actionsJSON string) (*Plugin, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("workspace root must be absolute")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, err
	}
	data, err := filepath.Abs(dataDir)
	if err != nil {
		return nil, err
	}
	if real, err := filepath.EvalSymlinks(data); err == nil {
		data = real
	}
	if filepath.Dir(resolved) == resolved {
		return nil, errors.New("workspace root must be a dedicated directory")
	}
	for _, protected := range []string{data, "/etc", "/proc", "/sys", "/dev", "/run", "/root", "/opt/chat-agent"} {
		if under(resolved, protected) || under(protected, resolved) {
			return nil, errors.New("workspace root overlaps protected host data")
		}
	}
	actions := map[string][]string{}
	if actionsJSON != "" {
		if len(actionsJSON) > 16384 || json.Unmarshal([]byte(actionsJSON), &actions) != nil || len(actions) > 16 {
			return nil, errors.New("invalid workspace actions JSON")
		}
		for name, argv := range actions {
			if name == "" || len(name) > 48 || len(argv) == 0 || len(argv) > 24 || !filepath.IsAbs(argv[0]) {
				return nil, errors.New("workspace action requires a name and a fixed absolute executable")
			}
		}
	}
	return &Plugin{path: resolved, actions: actions}, nil
}

func under(base, path string) bool {
	relative, err := filepath.Rel(base, path)
	return err == nil && filepath.IsLocal(relative)
}

func (p *Plugin) Manifest() pluginapi.Manifest {
	tools := []string{"workspace_list", "workspace_read", "workspace_search"}
	if len(p.actions) > 0 && canExecute() {
		tools = append(tools, "workspace_exec")
	}
	if sandboxReady() {
		tools = append(tools, "workspace_shell")
	}
	return pluginapi.Manifest{
		APIVersion: pluginapi.APIVersion, Kind: pluginapi.Kind,
		Metadata: pluginapi.Metadata{ID: ID, Name: "服务器工作区", Version: "0.1.0"},
		Spec: pluginapi.Spec{Runtime: pluginapi.RuntimeBuiltin, Capabilities: []string{"workspace.server"},
			Permissions: pluginapi.Permissions{Filesystem: []string{"workspace://server"}}, Tools: tools},
	}
}

func (p *Plugin) Start(_ context.Context, host pluginapi.Host) error {
	if !host.Allowed(Permission) {
		return errors.New("workspace permission denied")
	}
	root, err := os.OpenRoot(p.path)
	p.root = root
	return err
}
func (p *Plugin) Stop(context.Context) error {
	if p.root == nil {
		return nil
	}
	return p.root.Close()
}

func (p *Plugin) Register(registry *pluginapi.Registry) error {
	path := `{"type":"object","properties":{"workspace":{"type":"string","enum":["server"]},"path":{"type":"string","maxLength":256}},"required":["workspace","path"],"additionalProperties":false}`
	search := `{"type":"object","properties":{"workspace":{"type":"string","enum":["server"]},"query":{"type":"string","minLength":1,"maxLength":128}},"required":["workspace","query"],"additionalProperties":false}`
	tools := []pluginapi.Tool{
		{Name: "workspace_list", Description: "List the approved server workspace. Use path . for its root.", Parameters: json.RawMessage(path), Handler: p.list},
		{Name: "workspace_read", Description: "Read a small UTF-8 text file in the approved server workspace.", Parameters: json.RawMessage(path), Handler: p.read},
		{Name: "workspace_search", Description: "Find literal text in small files in the approved server workspace.", Parameters: json.RawMessage(search), Handler: p.search},
	}
	if len(p.actions) > 0 && canExecute() {
		names := make([]string, 0, len(p.actions))
		for name := range p.actions {
			names = append(names, name)
		}
		slices.Sort(names)
		enum, _ := json.Marshal(names)
		schema := `{"type":"object","properties":{"workspace":{"type":"string","enum":["server"]},"action":{"type":"string","enum":` + string(enum) + `},"require_sandbox":{"type":"boolean"}},"required":["workspace","action"],"additionalProperties":false}`
		tools = append(tools, pluginapi.Tool{Name: "workspace_exec", Description: "Run a fixed operator-configured action in the trusted workspace. Process restrictions are not a security sandbox.", Usage: "use only when the user explicitly requests the configured action.", Parameters: json.RawMessage(schema), Handler: p.execute})
	}
	if sandboxReady() {
		shell := `{"type":"object","properties":{"workspace":{"type":"string","enum":["server"]},"command":{"type":"string","minLength":1,"maxLength":4096},"cwd":{"type":"string","maxLength":256}},"required":["workspace","command"],"additionalProperties":false}`
		tools = append(tools, pluginapi.Tool{
			Name:        "workspace_shell",
			Description: "Run a shell command on the server inside a confined workspace. Only the workspace is writable; system directories are readable and cannot be changed; there is no outbound TCP. Report failures as failures.",
			Usage:       "use for inspecting or changing the workspace, or reading host state. It is not a general root shell and cannot reach the open internet.",
			Parameters:  json.RawMessage(shell),
			Handler:     p.shell,
		})
	}
	for i, tool := range tools {
		tool.Capability, tool.Order, tool.Timeout = "workspace.server", 710+i, 12*time.Second
		if err := registry.Tool(tool); err != nil {
			return err
		}
	}
	return nil
}

func allowedPath(path string) bool {
	if path == "." {
		return true
	}
	if !fs.ValidPath(path) || len(path) > 256 || strings.ContainsAny(path, "\\:\x00") {
		return false
	}
	for _, part := range strings.Split(path, "/") {
		if strings.HasPrefix(part, ".") {
			return false
		}
	}
	return true
}

func pathArg(raw json.RawMessage) (string, error) {
	var args struct{ Workspace, Path string }
	if json.Unmarshal(raw, &args) != nil || args.Workspace != "server" || !allowedPath(args.Path) {
		return "", errors.New("invalid workspace or relative path")
	}
	return args.Path, nil
}

func (p *Plugin) list(ctx context.Context, raw json.RawMessage) (pluginapi.Result, error) {
	path, err := pathArg(raw)
	if err != nil {
		return pluginapi.Result{}, err
	}
	root, err := p.root.OpenRoot(path)
	if err != nil {
		return pluginapi.Result{}, errors.New("workspace directory unavailable")
	}
	defer root.Close()
	f, err := root.Open(".")
	if err != nil {
		return pluginapi.Result{}, errors.New("workspace directory unavailable")
	}
	defer f.Close()
	items, err := f.ReadDir(100)
	if err != nil && !errors.Is(err, io.EOF) {
		return pluginapi.Result{}, errors.New("workspace directory unavailable")
	}
	names := []string{}
	for _, entry := range items {
		if !allowedPath(entry.Name()) || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		name := entry.Name()
		if entry.IsDir() {
			name += "/"
		}
		names = append(names, name)
	}
	if err := ctx.Err(); err != nil {
		return pluginapi.Result{}, err
	}
	return encoded(map[string]any{"entries": names, "limit": 100})
}

func (p *Plugin) text(path string) (string, error) {
	info, err := p.root.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > limit {
		return "", errors.New("workspace file is missing or exceeds the text limit")
	}
	f, err := openText(p.root, path)
	if err != nil {
		return "", errors.New("workspace file unavailable")
	}
	defer f.Close()
	info, err = f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return "", errors.New("workspace file is not regular")
	}
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || len(b) > limit || !utf8.Valid(b) || strings.ContainsRune(string(b), 0) {
		return "", errors.New("workspace file is not bounded UTF-8 text")
	}
	return string(b), nil
}

func (p *Plugin) read(ctx context.Context, raw json.RawMessage) (pluginapi.Result, error) {
	path, err := pathArg(raw)
	if err != nil {
		return pluginapi.Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return pluginapi.Result{}, err
	}
	content, err := p.text(path)
	return pluginapi.Result{Output: content}, err
}

func (p *Plugin) search(ctx context.Context, raw json.RawMessage) (pluginapi.Result, error) {
	var args struct{ Workspace, Query string }
	if json.Unmarshal(raw, &args) != nil || args.Workspace != "server" || strings.TrimSpace(args.Query) == "" || len(args.Query) > 512 {
		return pluginapi.Result{}, errors.New("invalid search query")
	}
	paths := []string{}
	visited := 0
	err := fs.WalkDir(p.root.FS(), ".", func(path string, entry fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		visited++
		if visited > 200 || len(paths) >= 30 {
			return fs.SkipAll
		}
		if err != nil {
			return nil
		}
		if !allowedPath(path) {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if entry.Type().IsRegular() {
			content, err := p.text(path)
			if err == nil && strings.Contains(content, args.Query) {
				paths = append(paths, path)
			}
		}
		return nil
	})
	if err != nil {
		return pluginapi.Result{}, err
	}
	return encoded(map[string]any{"matches": paths, "scan_limit": 200})
}

func (p *Plugin) execute(ctx context.Context, raw json.RawMessage) (pluginapi.Result, error) {
	var args struct {
		Workspace, Action string
		RequireSandbox    bool `json:"require_sandbox"`
	}
	if json.Unmarshal(raw, &args) != nil || args.Workspace != "server" {
		return pluginapi.Result{}, errors.New("invalid workspace")
	}
	if args.RequireSandbox {
		return pluginapi.Result{}, errors.New("SANDBOX_UNAVAILABLE: this host only offers the fixed process runner")
	}
	argv := p.actions[args.Action]
	operation := probe.ToolOperationID(ctx)
	if len(argv) == 0 || operation == "" {
		return pluginapi.Result{}, errors.New("workspace action requires a configured action and a recorded operation")
	}
	output, status := runAction(ctx, p.path, argv)
	return encoded(map[string]any{"operation_id": operation, "status": status, "output": output})
}

func encoded(value any) (pluginapi.Result, error) {
	data, err := json.Marshal(value)
	return pluginapi.Result{Output: string(data)}, err
}

// shell runs one command line inside the confined workspace.
//
// The point of the capability is a terminal on the server, so the command is
// free-form and reaches /bin/sh. What constrains it is the filesystem policy:
// read-only system directories, one writable workspace, no TCP. When that
// policy cannot be enforced the command does not run and the caller is told
// SANDBOX_UNAVAILABLE, because running unconfined would be a different promise.
func (p *Plugin) shell(ctx context.Context, raw json.RawMessage) (pluginapi.Result, error) {
	var args struct {
		Workspace, Command string
		Cwd                string `json:"cwd"`
	}
	if json.Unmarshal(raw, &args) != nil || args.Workspace != "server" {
		return pluginapi.Result{}, errors.New("invalid workspace")
	}
	command := strings.TrimSpace(args.Command)
	if command == "" || len(command) > 4096 {
		return pluginapi.Result{}, errors.New("command must be between 1 and 4096 bytes")
	}
	dir := p.path
	if cwd := strings.TrimSpace(args.Cwd); cwd != "" && cwd != "." {
		if !allowedPath(cwd) {
			return pluginapi.Result{}, errors.New("cwd must be a relative path inside the workspace")
		}
		if _, err := p.root.Stat(cwd); err != nil {
			return pluginapi.Result{}, errors.New("cwd is not a directory in the workspace")
		}
		dir = filepath.Join(p.path, filepath.Clean(cwd))
	}
	output, status, err := runShell(ctx, p.path, dir, command)
	if err != nil {
		return pluginapi.Result{}, err
	}
	return encoded(map[string]any{
		"status": status, "output": output, "cwd": strings.TrimPrefix(dir, p.path),
		"writable": "workspace only", "network": "no outbound TCP", "sandbox": sandboxStatus(),
	})
}
