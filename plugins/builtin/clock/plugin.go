// Package clock exposes the host clock and its step-scoped prompt guidance.
package clock

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"chat-agent/internal/pluginapi"
)

const Capability = "clock.utc"

type Plugin struct{}

func (Plugin) Manifest() pluginapi.Manifest {
	return pluginapi.Manifest{
		APIVersion: pluginapi.APIVersion, Kind: pluginapi.Kind,
		Metadata: pluginapi.Metadata{ID: "builtin.clock", Name: "服务器时间", Version: "0.1.0"},
		Spec:     pluginapi.Spec{Runtime: pluginapi.RuntimeBuiltin, Capabilities: []string{Capability}, Tools: []string{"get_current_time"}},
	}
}

func (Plugin) Register(registry *pluginapi.Registry) error { return registry.Tool(Tool()) }
func (Plugin) Start(context.Context, pluginapi.Host) error { return nil }
func (Plugin) Stop(context.Context) error                  { return nil }

func Tool() pluginapi.Tool {
	return pluginapi.Tool{
		Name: "get_current_time", Capability: Capability, Order: 100,
		Description: "Get current UTC server time in RFC3339 format.",
		Usage:       "call it whenever the answer depends on the real current time instead of guessing.",
		Prompt:      "## Time reference\n\nThe clock returns UTC. Convert to the user's timezone only when the timezone is known; state the zone when it matters.",
		Parameters:  json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
		Handler: func(ctx context.Context, input json.RawMessage) (pluginapi.Result, error) {
			if err := ctx.Err(); err != nil {
				return pluginapi.Result{}, err
			}
			var args map[string]json.RawMessage
			if err := json.Unmarshal(input, &args); err != nil || args == nil || len(args) != 0 {
				return pluginapi.Result{}, errors.New("clock arguments must be an empty object")
			}
			return pluginapi.Result{Output: time.Now().UTC().Format(time.RFC3339)}, nil
		},
	}
}
