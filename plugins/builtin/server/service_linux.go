//go:build linux

package server

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"chat-agent/internal/pluginapi"
)

// normalizeUnit accepts a plain unit name and returns it with the .service
// suffix. Anything containing a path separator, whitespace or a leading dash is
// refused: those are how a "unit name" turns into an argument to systemctl.
func normalizeUnit(raw string) (string, bool) {
	unit := strings.TrimSuffix(strings.TrimSpace(raw), ".service")
	if unit == "" || len(unit) > 64 {
		return "", false
	}
	for _, r := range unit {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '-' && r != '_' && r != '.' && r != '@' {
			return "", false
		}
	}
	return unit + ".service", true
}

// service reads one allow-listed unit. Every property here is readable by an
// unprivileged account, which is why this capability needs no escalation path.
func (p Plugin) service(ctx context.Context, raw json.RawMessage) (pluginapi.Result, error) {
	var args struct {
		Unit string
	}
	if json.Unmarshal(raw, &args) != nil {
		return pluginapi.Result{}, errors.New("invalid request")
	}
	name, ok := normalizeUnit(args.Unit)
	if !ok {
		return pluginapi.Result{}, errors.New("invalid unit name")
	}
	// The allow-list is checked against the normalized name, so "chat-agent" and
	// "chat-agent.service" are the same request and everything else is refused.
	allowed := false
	for _, unit := range p.units {
		if unit == name {
			allowed = true
			break
		}
	}
	if !allowed {
		return pluginapi.Result{}, errors.New("unit is not in the allow-list")
	}

	props := "ActiveState,SubState,NRestarts,MemoryCurrent,ExecMainPID,ExecMainStartTimestamp,Result,ExecMainStatus,UnitFileState"
	raw_out := output(ctx, "/usr/bin/systemctl", "show", name, "-p", props)
	if raw_out == unavailable {
		return pluginapi.Result{}, errors.New("systemctl could not be read for this unit")
	}

	values := map[string]string{}
	for _, line := range strings.Split(raw_out, "\n") {
		if key, value, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
			values[key] = value
		}
	}
	number := func(key string) any {
		// systemd reports unavailable counters as [not set] or a huge sentinel.
		text := values[key]
		if text == "" || text == "[not set]" {
			return nil
		}
		n, err := strconv.ParseInt(text, 10, 64)
		if err != nil || n < 0 {
			return nil
		}
		return n
	}
	report := map[string]any{
		"unit":     strings.TrimSuffix(name, ".service"),
		"active":   values["ActiveState"],
		"sub":      values["SubState"],
		"restarts": number("NRestarts"),
		"pid":      number("ExecMainPID"),
		"result":   values["Result"],
		"enabled":  values["UnitFileState"],
	}
	if memory := number("MemoryCurrent"); memory != nil {
		report["memory_bytes"] = memory
	}
	if code := number("ExecMainStatus"); code != nil {
		report["exit_status"] = code
	}
	if started := values["ExecMainStartTimestamp"]; started != "" && !strings.Contains(started, "n/a") {
		report["started"] = started
	}
	// Result carries the reason a unit stopped; naming it plainly saves a round
	// trip through server_logs for the common "it restarted, why" question.
	if values["Result"] != "" && values["Result"] != "success" {
		report["last_result"] = values["Result"]
	}
	return encoded(report)
}

var _ = time.Second
