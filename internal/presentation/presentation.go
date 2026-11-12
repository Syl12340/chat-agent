// Package presentation defines the finite, host-owned UI projection shared by
// tool cards and future persistent surfaces. It is deliberately not an HTML,
// script or arbitrary JSON extension point.
package presentation

import (
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"
)

const Protocol = "ca.presentation"
const Version = 1

type Surface string

const (
	ToolCard Surface = "tool_card"
	Panel    Surface = "panel"
	Strip    Surface = "strip"
)

type Budget struct {
	Bytes, Nodes, Depth, Children int
}

var budgets = map[Surface]Budget{
	ToolCard: {Bytes: 8 << 10, Nodes: 40, Depth: 6, Children: 16},
	Panel:    {Bytes: 32 << 10, Nodes: 120, Depth: 8, Children: 32},
	Strip:    {Bytes: 2 << 10, Nodes: 8, Depth: 3, Children: 4},
}

type View struct {
	Protocol    string  `json:"protocol"`
	Version     int     `json:"version"`
	Surface     Surface `json:"surface"`
	ID          string  `json:"id,omitempty"`
	Revision    int     `json:"revision,omitempty"`
	OperationID string  `json:"operation_id,omitempty"`
	MinApp      string  `json:"min_app,omitempty"`
	// Title names the stored view in the client's surface list. Without it every
	// persistent surface is labelled by its type alone, so several of them look
	// identical.
	Title    string `json:"title,omitempty"`
	Root     Node   `json:"root"`
	Fallback string `json:"fallback,omitempty"`
}

type Node struct {
	Type     string     `json:"type"`
	ID       string     `json:"id,omitempty"`
	Text     string     `json:"text,omitempty"`
	Label    string     `json:"label,omitempty"`
	Value    string     `json:"value,omitempty"`
	Unit     string     `json:"unit,omitempty"`
	Tone     string     `json:"tone,omitempty"`
	Progress *float64   `json:"progress,omitempty"`
	Headers  []string   `json:"headers,omitempty"`
	Rows     [][]string `json:"rows,omitempty"`
	Children []Node     `json:"children,omitempty"`
	Actions  []Action   `json:"actions,omitempty"`
}

type Action struct {
	ID               string `json:"id"`
	Label            string `json:"label"`
	ExpectedRevision *int   `json:"expected_revision,omitempty"`
	Effect           Effect `json:"effect"`
}

type Effect struct {
	Type        string          `json:"type"`
	Text        string          `json:"text,omitempty"`
	URL         string          `json:"url,omitempty"`
	View        *View           `json:"view,omitempty"`
	Patch       json.RawMessage `json:"patch,omitempty"`
	OperationID string          `json:"operation_id,omitempty"`
}

var safeID = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,80}$`)
var semver = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:[-+][A-Za-z0-9.-]+)?$`)

func ToolCardView(title, summary string) *View {
	if strings.TrimSpace(title) == "" {
		title = "工具"
	}
	children := []Node{{Type: "text", Text: clip(title, 160)}}
	if strings.TrimSpace(summary) != "" {
		children = append(children, Node{Type: "text", Text: clip(summary, 500), Tone: "muted"})
	}
	return &View{Protocol: Protocol, Version: Version, Surface: ToolCard,
		Root: Node{Type: "stack", Children: children}, Fallback: clip(strings.TrimSpace(title+" "+summary), 600)}
}

func clip(value string, n int) string {
	value = strings.TrimSpace(value)
	r := []rune(value)
	if len(r) <= n {
		return value
	}
	return string(r[:n]) + "…"
}

func Validate(view *View) error {
	if view == nil || view.Protocol != Protocol || view.Version != Version {
		return errors.New("unsupported presentation protocol")
	}
	b, ok := budgets[view.Surface]
	if !ok {
		return errors.New("unsupported presentation surface")
	}
	if view.ID != "" && !safeID.MatchString(view.ID) {
		return errors.New("invalid presentation id")
	}
	if view.Revision < 0 || view.OperationID != "" && !safeID.MatchString(view.OperationID) {
		return errors.New("invalid presentation revision or operation")
	}
	if view.MinApp != "" && !semver.MatchString(view.MinApp) {
		return errors.New("invalid minimum app version")
	}
	if utf8.RuneCountInString(view.Fallback) > 1000 {
		return errors.New("presentation fallback is too long")
	}
	if utf8.RuneCountInString(view.Title) > 60 {
		return errors.New("presentation title is too long")
	}
	raw, err := json.Marshal(view)
	if err != nil || len(raw) > b.Bytes {
		return errors.New("presentation exceeds byte budget")
	}
	nodes := 0
	if err = validateNode(&view.Root, 1, b, &nodes); err != nil {
		return err
	}
	return nil
}

func validateNode(node *Node, depth int, budget Budget, count *int) error {
	(*count)++
	if *count > budget.Nodes || depth > budget.Depth || len(node.Children) > budget.Children {
		return errors.New("presentation exceeds structural budget")
	}
	if node.ID != "" && !safeID.MatchString(node.ID) {
		return errors.New("invalid presentation node id")
	}
	if utf8.RuneCountInString(node.Text) > 1000 || utf8.RuneCountInString(node.Label) > 120 || utf8.RuneCountInString(node.Value) > 240 || utf8.RuneCountInString(node.Unit) > 40 {
		return errors.New("presentation text exceeds limit")
	}
	if node.Tone != "" && node.Tone != "muted" && node.Tone != "info" && node.Tone != "success" && node.Tone != "warning" && node.Tone != "danger" {
		return errors.New("invalid presentation tone")
	}
	switch node.Type {
	case "stack", "row":
		if len(node.Children) == 0 {
			return errors.New("empty presentation container")
		}
	case "text", "callout", "empty_state":
		if strings.TrimSpace(node.Text) == "" {
			return errors.New("empty presentation text")
		}
	case "metric":
		if strings.TrimSpace(node.Label) == "" || strings.TrimSpace(node.Value) == "" {
			return errors.New("invalid metric")
		}
	case "progress":
		if node.Progress == nil || *node.Progress < 0 || *node.Progress > 1 {
			return errors.New("invalid progress")
		}
	case "table":
		if len(node.Headers) == 0 || len(node.Headers) > 8 || len(node.Rows) > 20 {
			return errors.New("invalid presentation table")
		}
		for _, h := range node.Headers {
			if strings.TrimSpace(h) == "" || utf8.RuneCountInString(h) > 80 {
				return errors.New("invalid table header")
			}
		}
		for _, row := range node.Rows {
			if len(row) != len(node.Headers) {
				return errors.New("invalid table row")
			}
			for _, cell := range row {
				if utf8.RuneCountInString(cell) > 240 {
					return errors.New("table cell is too long")
				}
			}
		}
	case "actions":
		if len(node.Actions) == 0 || len(node.Actions) > 4 {
			return errors.New("invalid presentation actions")
		}
		seen := map[string]bool{}
		for i := range node.Actions {
			a := &node.Actions[i]
			if !safeID.MatchString(a.ID) || seen[a.ID] || strings.TrimSpace(a.Label) == "" || utf8.RuneCountInString(a.Label) > 60 || a.ExpectedRevision != nil && *a.ExpectedRevision < 0 {
				return errors.New("invalid presentation action")
			}
			seen[a.ID] = true
			if err := ValidateEffect(&a.Effect, node.Type); err != nil {
				return err
			}
		}
	default:
		return errors.New("unsupported presentation node")
	}
	if node.Type != "stack" && node.Type != "row" && len(node.Children) != 0 {
		return errors.New("children are not allowed on this node")
	}
	for i := range node.Children {
		if err := validateNode(&node.Children[i], depth+1, budget, count); err != nil {
			return err
		}
	}
	return nil
}

func ValidateEffect(effect *Effect, _ string) error {
	if effect == nil || effect.OperationID != "" && !safeID.MatchString(effect.OperationID) {
		return errors.New("invalid presentation effect")
	}
	switch effect.Type {
	case "insert_draft", "copy", "toast":
		if strings.TrimSpace(effect.Text) == "" || utf8.RuneCountInString(effect.Text) > 4000 {
			return errors.New("invalid text effect")
		}
	case "open_https":
		u, err := url.Parse(effect.URL)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
			return errors.New("invalid HTTPS effect")
		}
	case "refresh":
	case "replace_view":
		if effect.View == nil || Validate(effect.View) != nil {
			return errors.New("invalid replacement view")
		}
	case "patch_view":
		if len(effect.Patch) == 0 || len(effect.Patch) > 8<<10 || !json.Valid(effect.Patch) {
			return errors.New("invalid view patch")
		}
		var patch struct {
			Revision int  `json:"revision"`
			Root     Node `json:"root"`
		}
		var keys map[string]json.RawMessage
		if json.Unmarshal(effect.Patch, &keys) != nil || len(keys) != 2 || keys["revision"] == nil || keys["root"] == nil || json.Unmarshal(effect.Patch, &patch) != nil || patch.Revision < 1 {
			return errors.New("invalid view patch")
		}
		count := 0
		if validateNode(&patch.Root, 1, budgets[Panel], &count) != nil {
			return errors.New("invalid view patch")
		}
	default:
		return errors.New("unsupported presentation effect")
	}
	return nil
}
