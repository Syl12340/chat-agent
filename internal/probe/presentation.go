package probe

import "chat-agent/internal/presentation"

// ToolView adapts the host-owned, tagged render intent from DeepSeek Harness,
// packages/core/tools/src/presentation.ts (MIT, fcc0ad46).
type ToolView struct {
	Card         string             `json:"card"`
	Kind         string             `json:"kind"`
	Title        string             `json:"title,omitempty"`
	Summary      string             `json:"summary,omitempty"`
	Endpoint     string             `json:"endpoint"`
	Presentation *presentation.View `json:"presentation,omitempty"`
}

func presentTool(set *ToolSet, call ToolCall) ToolView {
	view := ToolView{Card: "generic", Kind: "other", Endpoint: "server"}
	if tool, ok := set.byName[call.Function.Name]; ok {
		if tool.definition.Kind != "" {
			view.Kind = tool.definition.Kind
		}
		if view.Kind == "search" {
			view.Card = "search"
		}
		if tool.definition.Present != nil {
			view.Title, view.Summary = tool.definition.Present([]byte(call.Function.Arguments))
		}
	}
	if view.Title != "" || view.Summary != "" {
		view.Presentation = presentation.ToolCardView(view.Title, view.Summary)
		if presentation.Validate(view.Presentation) != nil {
			view.Presentation = nil
		}
	}
	return view
}
