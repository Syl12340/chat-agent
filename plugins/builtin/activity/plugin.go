// Package activity exposes shared activities as tools.
//
// These were the last two business tools in the API package. They needed three
// host capabilities that now exist as seams: probe.ToolInputs for the evidence
// quote, pluginapi.WriteGate to serialize a revisioned write against other
// conversations of the role, and WriteScope.Cuts so binding or resuming an
// activity also advances the context barrier that the panels publish.
package activity

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"chat-agent/internal/companion"
	"chat-agent/internal/pluginapi"
	"chat-agent/internal/probe"
	"chat-agent/internal/retrieval"
)

const PluginID = "builtin.activity"

// contextBudget mirrors the host's recall budget: a listing may fill the same
// slice of the request the panel-written reference would have used.
const contextBudget = 16 << 10

// Store is the narrow slice of the activity store these tools use. Every write is
// revisioned and carries the context barrier.
type Store interface {
	List(role string) ([]companion.Activity, error)
	Session(role, sessionID string) (*companion.Activity, error)
	OpenForSession(role, sessionID, id string, revision int, draft companion.ActivityDraft, cuts map[string]int) (companion.Activity, bool, error)
	Patch(role, id string, revision int, p companion.ActivityPatch, cuts map[string]int) (companion.Activity, error)
}

// New returns nil when a dependency is missing, matching the other builtins.
func New(store Store, gate pluginapi.WriteGate) pluginapi.Plugin {
	if store == nil || gate == nil {
		return nil
	}
	return &Plugin{
		store: store,
		gate:  gate,
		manifest: pluginapi.Manifest{
			APIVersion: pluginapi.APIVersion,
			Kind:       pluginapi.Kind,
			Metadata: pluginapi.Metadata{
				ID:          PluginID,
				Name:        "共同活动",
				Version:     "0.1.0",
				Description: "按角色列出、开始、接续并记录共同活动的进展",
			},
			Spec: pluginapi.Spec{
				Runtime:      pluginapi.RuntimeBuiltin,
				Capabilities: []string{"activity.shared"},
				Tools:        []string{"shared_activity_open", "shared_activity_update"},
			},
		},
	}
}

// Plugin answers "what are we doing together, and how is it going".
type Plugin struct {
	store    Store
	gate     pluginapi.WriteGate
	manifest pluginapi.Manifest
}

func (p *Plugin) Manifest() pluginapi.Manifest                { return p.manifest }
func (p *Plugin) Start(context.Context, pluginapi.Host) error { return nil }
func (p *Plugin) Stop(context.Context) error                  { return nil }

func (p *Plugin) Register(registry *pluginapi.Registry) error {
	if err := registry.Tool(p.openTool()); err != nil {
		return err
	}
	return registry.Tool(p.updateTool())
}

// stop means "the write closure refused on its own terms"; the reason travels in
// the captured error, not through the gate.
var stop = errors.New("activity write stopped")

func (p *Plugin) openTool() pluginapi.Tool {
	return pluginapi.Tool{
		Name:  "shared_activity_open",
		Order: 600,
		Usage: "list, begin or resume a shared activity; merely reading one never resumes or rebinds it.",
		Description: "List, begin or continue the current role's shared activities, chosen from conversation " +
			"intent without command syntax or a settings visit. action=list reads up to eight IDs/revisions/titles; " +
			"optional query ranks matching saved goals/progress and returns full matching notes within a bounded " +
			"result. It is read-only: recalling a paused/completed activity never resumes or binds it. action=create " +
			"requires title, kind and goal plus a real current-turn user quote as evidence; create only when an " +
			"ongoing shared activity is wanted, not for ordinary small talk. action=open requires activity_id, " +
			"revision and evidence and resumes/binds that existing activity. Switching affects this conversation, " +
			"not another role or the old activity's status. Returned activity_state overrides the initial snapshot " +
			"and can be used by shared_activity_update in this turn. Kind is only an organizing label, never a mode " +
			"the user must select.",
		Parameters: json.RawMessage(`{"type":"object","properties":{"action":{"type":"string","enum":["list","create","open"]},"query":{"type":"string","maxLength":512},"activity_id":{"type":"string"},"revision":{"type":"integer","minimum":1},"kind":{"type":"string","enum":["bedtime","reflection","interest","small_step"]},"title":{"type":"string","maxLength":256},"goal":{"type":"string","maxLength":2048},"evidence":{"type":"string","maxLength":8192}},"required":["action"],"additionalProperties":false}`),
		Handler:    p.open,
	}
}

func (p *Plugin) updateTool() pluginapi.Tool {
	return pluginapi.Tool{
		Name:  "shared_activity_update",
		Order: 610,
		Usage: "update only the activity bound to this conversation, quoting the current turn as evidence.",
		Description: "Save progress and/or change status only for the activity already bound to this conversation, " +
			"on the latest user's explicit request. Evidence is a short exact quote from a current-turn user message. " +
			"Infer the intended action from conversation context, without command syntax. Use the latest " +
			"activity_state or tool-result revision. Call shared_activity_open first if no activity is bound. Save " +
			"only progress actually stated or explicitly referenced by the user, not inferred feelings or " +
			"personality. Never save credentials. An empty progress removes the saved note; use only when that " +
			"lasting effect is intended. Status active resumes, paused pauses, completed ends the activity. Ordinary " +
			"fatigue, a good night or a topic change does not authorize persistent updates. A successful result " +
			"overrides the initial snapshot; acknowledge only actual success. This is not an event, reminder or " +
			"general memory tool.",
		Parameters: json.RawMessage(`{"type":"object","properties":{"revision":{"type":"integer","minimum":1},"evidence":{"type":"string","maxLength":8192},"progress":{"type":"string","maxLength":4096},"status":{"type":"string","enum":["active","paused","completed"]}},"required":["revision","evidence"],"additionalProperties":false}`),
		Handler:    p.update,
	}
}

func (p *Plugin) open(ctx context.Context, data json.RawMessage) (pluginapi.Result, error) {
	var args struct {
		Action   string `json:"action"`
		ID       string `json:"activity_id"`
		Revision int    `json:"revision"`
		Kind     string `json:"kind"`
		Title    string `json:"title"`
		Goal     string `json:"goal"`
		Evidence string `json:"evidence"`
		Query    string `json:"query"`
	}
	if err := json.Unmarshal(data, &args); err != nil {
		return pluginapi.Result{}, err
	}
	role := probe.ToolRole(ctx)
	session := probe.ToolSession(ctx)
	if role == "" || session == "" {
		return pluginapi.Result{}, errors.New("工具上下文缺少角色或会话")
	}
	if args.Action == "list" {
		return p.list(role, args.Query)
	}
	if args.Query != "" {
		return pluginapi.Result{}, errors.New("query 仅用于读取活动，不用于修改")
	}
	inputs := probe.ToolInputs(ctx)
	if !probe.TurnHasEvidence(inputs, args.Evidence) {
		return pluginapi.Result{}, errors.New("开始或接续活动需要当前用户消息中的依据")
	}
	if args.Action == "create" {
		if args.ID != "" || args.Revision != 0 || args.Title == "" || args.Kind == "" {
			return pluginapi.Result{}, errors.New("新活动参数不完整或存在冲突")
		}
	} else if args.Action == "open" {
		if args.ID == "" || args.Revision < 1 || args.Title != "" || args.Goal != "" || args.Kind != "" {
			return pluginapi.Result{}, errors.New("接续活动需要有效标识和当前版本")
		}
	} else {
		return pluginapi.Result{}, errors.New("未知活动操作")
	}
	keyArgs := args
	keyArgs.Evidence = ""
	canonical, err := json.Marshal(keyArgs)
	if err != nil {
		return pluginapi.Result{}, err
	}
	// The host folded a per-turn random nonce into the idempotency key so two
	// identical calls in one turn created one activity and the same call in a
	// later turn created another. The turn's user text is now that identity: it is
	// fixed for the whole turn and changes when the user says something new.
	draft := companion.ActivityDraft{Kind: args.Kind, Title: args.Title, Goal: args.Goal, IdempotencyKey: idempotencyKey(session, inputs, canonical)}
	var out pluginapi.Result
	var refusal error
	err = p.gate.WithRoleWrite(role, session, func(scope pluginapi.WriteScope) error {
		item, created, err := p.store.OpenForSession(role, session, args.ID, args.Revision, draft, scope.Cuts)
		if err != nil {
			refusal = errors.New("活动未开始：请检查参数、版本或记录上限；不要盲目重复创建")
			return stop
		}
		out, refusal = encode(map[string]any{"activity": item, "activity_state": map[string]any{"revision": item.Revision, "status": item.Status}, "created": created})
		if refusal != nil {
			return stop
		}
		return nil
	})
	if refusal != nil {
		return pluginapi.Result{}, refusal
	}
	switch {
	case errors.Is(err, pluginapi.ErrWriteSessionEnded):
		return pluginapi.Result{}, errors.New("对话已停止，没有开始活动")
	case errors.Is(err, pluginapi.ErrWriteRoleBusy), errors.Is(err, pluginapi.ErrWriteContextUnavailable):
		return pluginapi.Result{}, errors.New("同角色其他对话正在运行或上下文不可用，请稍后再试")
	case err != nil:
		return pluginapi.Result{}, errors.New("活动未开始：请检查参数、版本或记录上限；不要盲目重复创建")
	}
	return out, nil
}

func (p *Plugin) list(role, query string) (pluginapi.Result, error) {
	if len([]rune(query)) > 512 {
		return pluginapi.Result{}, errors.New("活动查询过长")
	}
	items, err := p.store.List(role)
	if err != nil {
		return pluginapi.Result{}, errors.New("活动列表暂时不可用")
	}
	out := []map[string]any{}
	docs := []retrieval.Document{}
	for _, item := range items {
		docs = append(docs, retrieval.Document{ID: item.ID, Text: item.Title + "\n" + item.Goal + "\n" + item.Progress, UpdatedAt: item.UpdatedAt})
	}
	for _, match := range retrieval.Rank(query, docs, 8) {
		item := items[match.Index]
		entry := map[string]any{"id": item.ID, "revision": item.Revision, "title": item.Title, "status": item.Status, "updated_at": item.UpdatedAt}
		if strings.TrimSpace(query) != "" {
			entry["goal"] = item.Goal
			entry["progress"] = item.Progress
		}
		candidate := append(append([]map[string]any{}, out...), entry)
		// Keep the whole list under the same budget the panel-written reference
		// uses, so a long history cannot crowd out the rest of the request.
		if len(referenceJSON(candidate)) <= contextBudget {
			out = candidate
		}
	}
	return encode(out)
}

func (p *Plugin) update(ctx context.Context, data json.RawMessage) (pluginapi.Result, error) {
	var args struct {
		Revision int     `json:"revision"`
		Evidence string  `json:"evidence"`
		Progress *string `json:"progress"`
		Status   *string `json:"status"`
	}
	if err := json.Unmarshal(data, &args); err != nil {
		return pluginapi.Result{}, err
	}
	role := probe.ToolRole(ctx)
	session := probe.ToolSession(ctx)
	if role == "" || session == "" {
		return pluginapi.Result{}, errors.New("工具上下文缺少角色或会话")
	}
	if !probe.TurnHasEvidence(probe.ToolInputs(ctx), args.Evidence) || args.Revision < 1 || (args.Progress == nil && args.Status == nil) {
		return pluginapi.Result{}, errors.New("需要当前用户消息的依据、有效版本和待修改字段")
	}
	var out pluginapi.Result
	var refusal error
	err := p.gate.WithRoleWrite(role, session, func(scope pluginapi.WriteScope) error {
		current, err := p.store.Session(role, session)
		if err != nil || current == nil || current.Revision != args.Revision {
			refusal = errors.New("活动已更新或删除，请查看最新记录后再试")
			return stop
		}
		item, err := p.store.Patch(role, current.ID, args.Revision, companion.ActivityPatch{Progress: args.Progress, Status: args.Status}, scope.Cuts)
		if err != nil {
			refusal = errors.New("活动修改没有保存，请在共同活动面板查看后重试")
			return stop
		}
		saved := map[string]any{"revision": item.Revision, "status": item.Status, "progress_saved": args.Progress != nil}
		if item.Status == "active" {
			saved["goal"], saved["progress"] = item.Goal, item.Progress
		}
		out, refusal = encode(saved)
		if refusal != nil {
			return stop
		}
		return nil
	})
	if refusal != nil {
		return pluginapi.Result{}, refusal
	}
	switch {
	case errors.Is(err, pluginapi.ErrWriteSessionEnded):
		return pluginapi.Result{}, errors.New("对话已停止，活动没有修改")
	case errors.Is(err, pluginapi.ErrWriteRoleBusy), errors.Is(err, pluginapi.ErrWriteContextUnavailable):
		return pluginapi.Result{}, errors.New("这个角色的另一段对话正在运行，请停止后再修改活动")
	case err != nil:
		return pluginapi.Result{}, errors.New("活动修改没有保存，请在共同活动面板查看后重试")
	}
	return out, nil
}

// idempotencyKey identifies "this request in this turn": the session, the user
// text that started the turn, and the arguments with the quote removed.
func idempotencyKey(session string, inputs []string, canonical []byte) string {
	digest := sha256.New()
	digest.Write([]byte(session))
	for _, input := range inputs {
		digest.Write([]byte{0})
		digest.Write([]byte(input))
	}
	seed := digest.Sum(nil)
	key := sha256.Sum256(append(seed, canonical...))
	return hex.EncodeToString(key[:])
}

func referenceJSON(value any) []byte {
	var data bytes.Buffer
	encoder := json.NewEncoder(&data)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(value)
	return bytes.TrimSpace(data.Bytes())
}

func encode(value any) (pluginapi.Result, error) {
	out, err := json.Marshal(value)
	if err != nil {
		return pluginapi.Result{}, err
	}
	return pluginapi.Result{Output: string(out)}, nil
}
