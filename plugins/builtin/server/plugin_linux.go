// Package server exposes read-only diagnostics for the configured Linux host.
package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"chat-agent/internal/pluginapi"
	"chat-agent/internal/probe"
)

const ID = "builtin.server"
const Permission = "filesystem:diagnostics://host"
const MaintenancePermission = "filesystem:maintenance://host"

// New builds the diagnostics plugin for an explicit set of units.
//
// The unit list is the allow-list: the tool cannot name a unit that is not in
// it, so the capability stays "look at these services" rather than "query
// systemd". An empty list means the service tool is not registered at all.
func New(units []string) pluginapi.Plugin {
	plugin, _ := NewWithActions(units, "")
	return plugin
}

// NewWithActions optionally registers fixed, operator-supplied maintenance
// commands. JSON shape: {"action-id":["/absolute/helper","fixed-arg"]}.
func NewWithActions(units []string, rawActions string) (pluginapi.Plugin, error) {
	clean := make([]string, 0, len(units))
	for _, unit := range units {
		if name, ok := normalizeUnit(unit); ok {
			clean = append(clean, name)
		}
	}
	slices.Sort(clean)
	actions, err := parseActions(rawActions)
	if err != nil {
		return nil, err
	}
	return Plugin{units: slices.Compact(clean), actions: actions}, nil
}

// Plugin carries the unit allow-list.
type Plugin struct {
	units   []string
	actions map[string][]string
}

func (p Plugin) Manifest() pluginapi.Manifest {
	tools := []string{"server_status", "server_releases", "server_logs"}
	if len(p.units) > 0 {
		tools = append(tools, "server_service")
	}
	capabilities := []string{"diagnostics.host"}
	permissions := []string{"diagnostics://host"}
	if len(p.actions) > 0 {
		tools = append(tools, "server_action")
		capabilities = append(capabilities, "maintenance.host")
		permissions = append(permissions, "maintenance://host")
	}
	return pluginapi.Manifest{
		APIVersion: pluginapi.APIVersion, Kind: pluginapi.Kind,
		Metadata: pluginapi.Metadata{ID: ID, Name: "服务器巡检与受限维护", Version: "0.3.0"},
		Spec: pluginapi.Spec{Runtime: pluginapi.RuntimeBuiltin, Capabilities: capabilities,
			Permissions: pluginapi.Permissions{Filesystem: permissions},
			Tools:       tools},
	}
}
func (p Plugin) Start(_ context.Context, host pluginapi.Host) error {
	if !host.Allowed(Permission) {
		return errors.New("server diagnostics permission denied")
	}
	if len(p.actions) > 0 && !host.Allowed(MaintenancePermission) {
		return errors.New("server maintenance permission denied")
	}
	return nil
}
func (Plugin) Stop(context.Context) error { return nil }

func (p Plugin) Register(registry *pluginapi.Registry) error {
	tools := []pluginapi.Tool{
		{Name: "server_status", Description: "Read Linux load, memory, disk usage, listening TCP/UDP ports and CA service status.", Handler: status},
		{Name: "server_releases", Description: "Read active CA and Android release names and list available backup filenames.", Handler: releases},
		{Name: "server_logs", Description: "Read recent CA and search service journal timestamps and priorities. Message bodies are omitted.", Handler: logs},
	}
	if len(p.units) > 0 {
		enum, _ := json.Marshal(p.units)
		schema := `{"type":"object","properties":{"unit":{"type":"string","enum":` + string(enum) + `}},"required":["unit"],"additionalProperties":false}`
		tools = append(tools, pluginapi.Tool{
			Name:        "server_service",
			Description: "Read one allow-listed systemd unit in detail: active state, restarts, memory, start time, enabled state and the last exit result. Read-only; it cannot start, stop or restart anything.",
			Usage:       "use when a service looks unhealthy or the user asks whether something restarted or failed.",
			Parameters:  json.RawMessage(schema),
			Handler:     p.service,
		})
	}
	if len(p.actions) > 0 {
		names := make([]string, 0, len(p.actions))
		for name := range p.actions {
			names = append(names, name)
		}
		slices.Sort(names)
		enum, _ := json.Marshal(names)
		tools = append(tools, pluginapi.Tool{
			Name:             "server_action",
			Description:      "Run one operator-configured server maintenance action. The command and arguments are fixed by server configuration; every call requires one-time human approval.",
			Usage:            "use only after explaining the exact configured action, expected impact and recovery path.",
			Parameters:       json.RawMessage(`{"type":"object","properties":{"action":{"type":"string","enum":` + string(enum) + `}},"required":["action"],"additionalProperties":false}`),
			Capability:       "maintenance.host",
			Decision:         "required",
			Risk:             "critical",
			ApprovalRequired: true,
			Receipt:          "atomic",
			Timeout:          2 * time.Minute,
			Handler:          p.action,
		})
	}
	for i, tool := range tools {
		if tool.Capability == "" {
			tool.Capability = "diagnostics.host"
		}
		tool.Order = 730 + i
		if tool.Timeout == 0 {
			tool.Timeout = 8 * time.Second
		}
		if tool.Parameters == nil {
			tool.Parameters = json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
		}
		if err := registry.Tool(tool); err != nil {
			return err
		}
	}
	return nil
}

func parseActions(raw string) (map[string][]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var actions map[string][]string
	decoder := json.NewDecoder(strings.NewReader(raw))
	if err := decoder.Decode(&actions); err != nil {
		return nil, errors.New("invalid server maintenance actions")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return nil, errors.New("invalid server maintenance actions")
	}
	for name, argv := range actions {
		// Maintenance is only delegated to an operator-installed narrow helper.
		// Generic shells/system utilities would turn this fixed-action map into an
		// accidental root command runner even though the model cannot edit it.
		if !actionName(name) || len(argv) == 0 || len(argv) > 16 || !filepath.IsAbs(argv[0]) || !strings.HasPrefix(filepath.Base(argv[0]), "chat-agent-") {
			return nil, errors.New("invalid server maintenance action")
		}
		for _, value := range argv {
			if value == "" || len(value) > 512 || strings.ContainsRune(value, 0) {
				return nil, errors.New("invalid server maintenance action argument")
			}
		}
	}
	return actions, nil
}

func actionName(value string) bool {
	if value == "" || len(value) > 63 {
		return false
	}
	for _, r := range value {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' && r != '_' {
			return false
		}
	}
	return true
}

func (p Plugin) action(ctx context.Context, raw json.RawMessage) (pluginapi.Result, error) {
	var args struct {
		Action string `json:"action"`
	}
	if json.Unmarshal(raw, &args) != nil {
		return pluginapi.Result{}, errors.New("invalid request")
	}
	argv, ok := p.actions[args.Action]
	if !ok {
		return pluginapi.Result{}, errors.New("action is not in the allow-list")
	}
	operationID := probe.ToolOperationID(ctx)
	if operationID == "" {
		return pluginapi.Result{}, errors.New("durable operation identity is unavailable")
	}
	call := append(append([]string(nil), argv[1:]...), "--operation-id", operationID)
	cmd := exec.CommandContext(ctx, argv[0], call...)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C", "CHAT_OPERATION_ID=" + operationID}
	cmd.WaitDelay = time.Second
	var stdout, stderr cappedBuffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = "no diagnostic output"
		}
		return pluginapi.Result{}, errors.New("configured maintenance helper failed: " + detail)
	}
	detail := strings.TrimSpace(stdout.String())
	value := map[string]any{"action": args.Action, "operation_id": operationID, "status": "completed"}
	if detail != "" {
		value["detail"] = detail
	}
	encodedValue, _ := json.Marshal(value)
	argumentsDigest := sha256.Sum256(raw)
	outcomeDigest := sha256.Sum256(encodedValue)
	return pluginapi.Result{Output: string(encodedValue), Data: encodedValue, Receipt: &pluginapi.Receipt{
		Store: "server-ops", OperationID: operationID,
		ArgumentsSHA256: hex.EncodeToString(argumentsDigest[:]), EntityIDs: []string{args.Action},
		ResultingRevisions: []int{1}, OutcomeDigest: hex.EncodeToString(outcomeDigest[:]),
	}}, nil
}

func status(ctx context.Context, _ json.RawMessage) (pluginapi.Result, error) {
	report := map[string]any{"load": readProc("/proc/loadavg")}
	memory := []string{}
	for _, line := range strings.Split(readProc("/proc/meminfo"), "\n") {
		if strings.HasPrefix(line, "MemTotal:") || strings.HasPrefix(line, "MemAvailable:") || strings.HasPrefix(line, "SwapFree:") {
			memory = append(memory, line)
		}
	}
	report["memory"] = memory
	disks := map[string]any{}
	for _, path := range []string{"/", "/var/lib/chat-agent"} {
		var stat syscall.Statfs_t
		if syscall.Statfs(path, &stat) == nil {
			disks[path] = map[string]uint64{"total_bytes": stat.Blocks * uint64(stat.Bsize), "available_bytes": stat.Bavail * uint64(stat.Bsize)}
		}
	}
	report["disk"] = disks
	report["services"] = output(ctx, "/usr/bin/systemctl", "show", "--no-pager", "--property=Id,ActiveState,SubState,NRestarts", "chat-agent.service", "chat-search.service")
	report["listeners"] = output(ctx, "/usr/bin/ss", "-H", "-lntu")
	return encoded(report)
}

func readProc(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	b, _ := io.ReadAll(io.LimitReader(f, 4096))
	return strings.TrimSpace(string(b))
}

func releaseName(path string) string {
	target, err := os.Readlink(path)
	if err != nil {
		return unavailable
	}
	return filepath.Base(target)
}

func releases(ctx context.Context, _ json.RawMessage) (pluginapi.Result, error) {
	report := map[string]any{"server": releaseName("/opt/chat-agent/current"), "android": releaseName("/opt/chat-agent/android/current")}
	dir, err := os.Open("/opt/chat-agent/backups")
	if err != nil {
		// Say which failure this is. "unavailable" alone reads as "there are no
		// backups", which is a different and alarming claim than "this account
		// may not list them".
		report["backup_inventory"] = unavailable
		if errors.Is(err, fs.ErrPermission) {
			report["backup_reason"] = "the service account cannot list the backup directory"
		} else {
			report["backup_reason"] = "the backup directory could not be opened"
		}
		return encoded(report)
	}
	defer dir.Close()
	entries, err := dir.ReadDir(64)
	if err != nil && !errors.Is(err, io.EOF) {
		report["backup_inventory"] = unavailable
		report["backup_reason"] = "the backup directory could not be read"
		return encoded(report)
	}
	names := []string{}
	for _, entry := range entries {
		if entry.Type().IsRegular() && strings.HasSuffix(entry.Name(), ".tar.gz") {
			names = append(names, entry.Name())
		}
	}
	if err := ctx.Err(); err != nil {
		return pluginapi.Result{}, err
	}
	// A full listing is dozens of names. Handing all of them to the model both
	// crowds out the answer and slows the turn until it times out, so report the
	// count and the newest few, which is what a restore decision actually needs.
	const shown = 8
	total := len(names)
	slices.Sort(names)
	if total > shown {
		names = names[total-shown:]
	}
	report["backup_inventory"] = names
	report["backup_total"] = total
	if total > shown {
		report["backup_note"] = "only the newest entries are listed; names sort by time"
	}
	return encoded(report)
}

func logs(ctx context.Context, _ json.RawMessage) (pluginapi.Result, error) {
	raw := output(ctx, "/usr/bin/journalctl", "--system", "--no-pager", "-n", "15", "-o", "json", "--output-fields=__REALTIME_TIMESTAMP,PRIORITY,_SYSTEMD_UNIT", "-u", "chat-agent.service", "-u", "chat-search.service")
	// A collector that could not run says so itself; check that before parsing, so
	// a failed read cannot be reported as "no entries".
	if raw == unavailable {
		return pluginapi.Result{}, errors.New("service journal unavailable to the configured collector")
	}
	type row struct {
		Time     string `json:"__REALTIME_TIMESTAMP"`
		Priority string `json:"PRIORITY"`
		Unit     string `json:"_SYSTEMD_UNIT"`
	}
	rows := []row{}
	malformed, otherUnits := 0, 0
	for _, line := range strings.Split(raw, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var r row
		if json.Unmarshal([]byte(line), &r) != nil || r.Time == "" {
			// The read is capped, so the final line can be cut mid-object. Say how
			// many were dropped instead of letting a short list look complete.
			malformed++
			continue
		}
		if r.Unit != "chat-agent.service" && r.Unit != "chat-search.service" {
			// journalctl interleaves entries from related scopes even with -u.
			// Filtering those out is the filter working, not data going missing,
			// so counting them as unreadable would misreport every read.
			otherUnits++
			continue
		}
		rows = append(rows, r)
	}
	report := map[string]any{"entries": rows, "message_bodies": "omitted", "scope": "journals visible to service account"}
	if malformed > 0 {
		report["unreadable_lines"] = malformed
	}
	if otherUnits > 0 {
		report["filtered_other_units"] = otherUnits
	}
	return encoded(report)
}

// unavailable is what output() returns when the command could not run or failed.
const unavailable = "unavailable"

type cappedBuffer struct{ bytes.Buffer }

func (b *cappedBuffer) Write(p []byte) (int, error) {
	_, _ = b.Buffer.Write(p[:min(len(p), 32<<10-b.Len())])
	return len(p), nil
}
func output(ctx context.Context, path string, args ...string) string {
	probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(probeCtx, path, args...)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C", "SYSTEMD_COLORS=0", "SYSTEMD_PAGER=cat"}
	cmd.WaitDelay = time.Second
	var b cappedBuffer
	cmd.Stdout = &b
	if cmd.Run() != nil {
		return unavailable
	}
	return strings.ToValidUTF8(strings.TrimSpace(b.String()), "�")
}
func encoded(data any) (pluginapi.Result, error) {
	raw, err := json.Marshal(data)
	return pluginapi.Result{Output: string(raw)}, err
}
