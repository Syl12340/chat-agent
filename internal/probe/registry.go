package probe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Registry/step admission follows DeepSeek Harness (MIT), independently
// implemented in Go. See THIRD_PARTY_NOTICES.md for the pinned upstream source.
// Only ToolSchema crosses the model boundary. Everything else is host-owned.
type ToolSchema struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type Tool struct {
	Name        string                                           `json:"name"`
	Description string                                           `json:"description"`
	Parameters  json.RawMessage                                  `json:"parameters"`
	Usage       string                                           `json:"-"`
	Prompt      string                                           `json:"-"`
	Kind        string                                           `json:"-"`
	Present     func(json.RawMessage) (title, summary string)    `json:"-"`
	Order       int                                              `json:"-"`
	Timeout     time.Duration                                    `json:"-"`
	Available   func(context.Context, StepContext) (bool, error) `json:"-"`
	Validate    func(json.RawMessage) error                      `json:"-"`
	Guard       ToolGuard                                        `json:"-"`
	Receipt     ReceiptCapability                                `json:"-"`
	// ApprovalRequired forces a host-issued, single-use ticket even when an
	// independent decision service considers the call safe.
	ApprovalRequired bool `json:"-"`
	// Decision is the host's requirement, not a suggestion: the executor enforces
	// it even when the model never asks. Empty means DecisionNever.
	Decision DecisionRequirement `json:"-"`
	// Risk describes the cost of a wrong or failed call. Metadata only.
	Risk    RiskLevel                                                  `json:"-"`
	Execute func(context.Context, json.RawMessage) (ToolResult, error) `json:"-"`
}

type ToolExecution struct {
	Step      StepContext
	CallID    string
	Name      string
	Arguments json.RawMessage
}

type ToolGuard func(context.Context, ToolExecution) error

type registeredTool struct {
	definition Tool
	schema     *parameterSchema
}

type ToolRegistry struct{ tools []registeredTool }

// ToolSet is a step-owned admission snapshot shared by the assembler and
// executor. Its maps and definitions are private; callers only get copies.
type ToolSet struct {
	step    StepContext
	ordered []registeredTool
	byName  map[string]registeredTool
}

// MaxTools bounds the catalog offered to one step. The limit exists because
// selection quality falls as the list grows, not because of a storage cost, so
// raising it needs a reason beyond "one more capability arrived".
//
// It has now been raised three times, and the third raise is the instructive one.
// The second raise assumed host tools were the whole story: they are admitted only to
// conversations the operator named, so a larger ceiling applied to a narrow case. Then
// local_workspace and a maintenance action were added, and every conversation in the
// granted session went over the limit — including plain "how is the server doing".
//
// The refusal is logged as "turn agent unavailable: too many tools", but the turn's
// category says generation_failed, which reads like an upstream fault. It is not; the
// request never left the host.
//
// The real answer remains per-turn scoping. Until then this is generous, and the
// refusal names every tool it saw.
const MaxTools = 48

// ErrTooManyTools marks a registry that refused to build. Callers wrap it so the
// failure is not mistaken for a model-side problem: the request never leaves the
// host, and reporting it as a generation failure sends the reader to the wrong place.
var ErrTooManyTools = errors.New("too many tools")

func NewToolRegistry(definitions []Tool) (*ToolRegistry, error) {
	if len(definitions) > MaxTools {
		names := make([]string, 0, len(definitions))
		for _, tool := range definitions {
			names = append(names, tool.Name)
		}
		return nil, fmt.Errorf("%w: %d registered, limit %d: %s", ErrTooManyTools, len(definitions), MaxTools, strings.Join(names, ", "))
	}
	r := &ToolRegistry{}
	names := map[string]bool{}
	for _, tool := range definitions {
		if tool.Receipt == "" {
			tool.Receipt = ReceiptNone
		}
		if tool.Decision != "" && !tool.Decision.Valid() {
			return nil, fmt.Errorf("tool %s has an unknown decision requirement %q", tool.Name, tool.Decision)
		}
		if !tool.Risk.Valid() {
			return nil, fmt.Errorf("tool %s has an unknown risk level %q", tool.Name, tool.Risk)
		}
		if tool.Name == "" || tool.Execute == nil || tool.Timeout < 0 || names[tool.Name] || tool.Receipt != ReceiptNone && tool.Receipt != ReceiptAtomic {
			return nil, errors.New("invalid or duplicate tool definition")
		}
		schema, err := parseParameterSchema(tool.Parameters)
		if err != nil {
			return nil, fmt.Errorf("tool %s: %w", tool.Name, err)
		}
		tool.Parameters = append(json.RawMessage(nil), tool.Parameters...)
		r.tools = append(r.tools, registeredTool{tool, schema})
		names[tool.Name] = true
	}
	sort.Slice(r.tools, func(i, j int) bool {
		a, b := r.tools[i].definition, r.tools[j].definition
		if a.Order == b.Order {
			return a.Name < b.Name
		}
		return a.Order < b.Order
	})
	return r, nil
}

func (r *ToolRegistry) Resolve(ctx context.Context, step StepContext) (*ToolSet, error) {
	set := &ToolSet{step: step, byName: map[string]registeredTool{}}
	if !step.ToolsAllowed {
		return set, ctx.Err()
	}
	for _, tool := range r.tools {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if tool.definition.Available != nil {
			ok, err := tool.definition.Available(ctx, step)
			if err != nil {
				return nil, fmt.Errorf("tool availability failed: %s", tool.definition.Name)
			}
			if !ok {
				continue
			}
		}
		set.ordered = append(set.ordered, tool)
		set.byName[tool.definition.Name] = tool
	}
	return set, nil
}

func (s *ToolSet) Schemas() []ToolSchema {
	out := make([]ToolSchema, 0, len(s.ordered))
	for _, t := range s.ordered {
		d := t.definition
		out = append(out, ToolSchema{Name: d.Name, Description: d.Description, Parameters: append(json.RawMessage(nil), d.Parameters...)})
	}
	return out
}

func (s *ToolSet) guidance() string {
	if len(s.ordered) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n## How to use these tools\nCall a tool only when its result changes the reply; ordinary conversation needs none. Never claim a write succeeded before its result says so, and never repeat a completed call with the same arguments.\n")
	for _, t := range s.ordered {
		d := t.definition
		if d.Usage != "" {
			fmt.Fprintf(&b, "- %s — %s\n", d.Name, d.Usage)
		}
	}
	return b.String()
}

func (s *ToolSet) names() []string {
	names := make([]string, 0, len(s.ordered))
	for _, t := range s.ordered {
		names = append(names, t.definition.Name)
	}
	return names
}
