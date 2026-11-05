// Package interactive implements the finite chat-ui v1 contract. Definitions
// are untrusted assistant data; validating them never authorizes a write.
package interactive

import (
	"encoding/json"
	"errors"
	"io"
	"math"
	"regexp"
	"strings"
	"unicode/utf8"
)

const MaxBlockBytes = 8 << 10

var safeID = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
var hashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var reservedPattern = regexp.MustCompile(`"chat_ui_response"\s*:`)

type Component struct {
	ID    string
	Type  string
	Props map[string]any
}
type Declaration struct {
	Ordinal   int
	Raw       string
	Component *Component
	Err       error
}
type Source struct {
	MessageIndex int    `json:"message_index"`
	SHA256       string `json:"sha256"`
}
type AnswerField struct {
	ID    string `json:"id"`
	Value string `json:"value"`
}
type Response struct {
	SchemaVersion int    `json:"schema_version"`
	ComponentID   string `json:"component_id"`
	ComponentType string `json:"component_type"`
	Source        Source `json:"source"`
	Answer        struct {
		OptionID string        `json:"option_id,omitempty"`
		Fields   []AnswerField `json:"fields,omitempty"`
	} `json:"answer"`
}

// decode rejects duplicate/prototype keys before maps can overwrite them.
func decode(raw string) (any, error) {
	d := json.NewDecoder(strings.NewReader(raw))
	d.UseNumber()
	var read func(int) (any, error)
	read = func(depth int) (any, error) {
		if depth > 16 {
			return nil, errors.New("JSON nesting too deep")
		}
		t, err := d.Token()
		if err != nil {
			return nil, err
		}
		switch t {
		case json.Delim('{'):
			m := map[string]any{}
			for d.More() {
				k, e := d.Token()
				if e != nil {
					return nil, e
				}
				key, ok := k.(string)
				if !ok {
					return nil, errors.New("invalid key")
				}
				if key == "__proto__" || key == "prototype" || key == "constructor" {
					return nil, errors.New("reserved key")
				}
				if _, ok = m[key]; ok {
					return nil, errors.New("duplicate key")
				}
				value, e := read(depth + 1)
				if e != nil {
					return nil, e
				}
				m[key] = value
			}
			_, err = d.Token()
			return m, err
		case json.Delim('['):
			a := []any{}
			for d.More() {
				v, e := read(depth + 1)
				if e != nil {
					return nil, e
				}
				a = append(a, v)
			}
			_, err = d.Token()
			return a, err
		default:
			if _, ok := t.(json.Delim); ok {
				return nil, errors.New("invalid delimiter")
			}
			return t, nil
		}
	}
	v, err := read(0)
	if err != nil {
		return nil, err
	}
	if _, err = d.Token(); err != io.EOF {
		return nil, errors.New("trailing JSON")
	}
	return v, nil
}
func object(v any, required string, optional string) (map[string]any, bool) {
	m, ok := v.(map[string]any)
	if !ok {
		return nil, false
	}
	allowed := map[string]bool{}
	for _, k := range strings.Fields(required) {
		allowed[k] = true
		if _, ok := m[k]; !ok {
			return nil, false
		}
	}
	for _, k := range strings.Fields(optional) {
		allowed[k] = true
	}
	for k := range m {
		if !allowed[k] {
			return nil, false
		}
	}
	return m, true
}
func text(v any, min, max int) bool {
	s, ok := v.(string)
	n := utf8.RuneCountInString(s)
	return ok && n >= min && n <= max
}
func id(v any, max int) bool {
	s, ok := v.(string)
	return ok && len(s) <= max && safeID.MatchString(s)
}
func number(v any, min, max float64) bool {
	n, ok := v.(json.Number)
	if !ok {
		return false
	}
	f, e := n.Float64()
	return e == nil && !math.IsInf(f, 0) && !math.IsNaN(f) && f >= min && f <= max
}
func optionalText(m map[string]any, key string, max int) bool {
	v, ok := m[key]
	return !ok || text(v, 1, max)
}
func array(v any, min, max int) ([]any, bool) {
	a, ok := v.([]any)
	return a, ok && len(a) >= min && len(a) <= max
}
func point(v any) bool {
	m, ok := object(v, "x y", "")
	return ok && number(m["x"], -1e6, 1e6) && number(m["y"], -1e6, 1e6)
}

func ParseComponent(raw string) (*Component, error) {
	bad := func() (*Component, error) { return nil, errors.New("invalid chat-ui v1 definition") }
	if len(raw) > MaxBlockBytes || !utf8.ValidString(raw) {
		return bad()
	}
	v, e := decode(raw)
	if e != nil {
		return nil, e
	}
	m, ok := object(v, "schema_version id type props", "")
	if !ok || !number(m["schema_version"], 1, 1) || !id(m["id"], 64) {
		return bad()
	}
	kind, ok := m["type"].(string)
	if !ok {
		return bad()
	}
	var p map[string]any
	switch kind {
	case "quiz.single_choice", "quiz.fill_blank":
		listKey := "options"
		minCount, maxCount := 2, 8
		if kind == "quiz.fill_blank" {
			listKey = "fields"
			minCount, maxCount = 1, 4
		}
		p, ok = object(m["props"], "question "+listKey, "title submit_label")
		if !ok || !text(p["question"], 1, 1024) || !optionalText(p, "title", 80) {
			return bad()
		}
		if label, exists := p["submit_label"]; exists && label != "提交答案" {
			return bad()
		}
		a, ok := array(p[listKey], minCount, maxCount)
		if !ok {
			return bad()
		}
		seen := map[string]bool{}
		for _, v := range a {
			optional := ""
			limit := 256
			if listKey == "fields" {
				optional = "placeholder"
				limit = 120
			}
			item, ok := object(v, "id label", optional)
			if !ok || !id(item["id"], 24) || !text(item["label"], 1, limit) {
				return bad()
			}
			key := item["id"].(string)
			if seen[key] {
				return bad()
			}
			seen[key] = true
			if ph, exists := item["placeholder"]; exists && !text(ph, 0, 128) {
				return bad()
			}
		}
	case "chart.line":
		p, ok = object(m["props"], "source", "title x_label y_label")
		if !ok || !optionalText(p, "title", 80) || !optionalText(p, "x_label", 80) || !optionalText(p, "y_label", 80) {
			return bad()
		}
		src, yes := mSource(p["source"])
		if !yes {
			return bad()
		}
		switch src["kind"] {
		case "samples":
			if _, ok := object(src, "kind series", ""); !ok {
				return bad()
			}
			series, ok := array(src["series"], 1, 3)
			if !ok {
				return bad()
			}
			total := 0
			for _, v := range series {
				item, ok := object(v, "points", "name")
				if !ok || !optionalText(item, "name", 80) {
					return bad()
				}
				points, ok := array(item["points"], 1, 256)
				if !ok {
					return bad()
				}
				total += len(points)
				if total > 256 {
					return bad()
				}
				for _, pt := range points {
					if !point(pt) {
						return bad()
					}
				}
			}
		case "sine":
			if _, ok := object(src, "kind amplitude frequency duration", "phase offset controls"); !ok || !number(src["amplitude"], 0, 10) || !number(src["frequency"], .1, 5) || !number(src["duration"], .1, 5) {
				return bad()
			}
			if v, exists := src["phase"]; exists && !number(v, -math.Pi, math.Pi) {
				return bad()
			}
			if v, exists := src["offset"]; exists && !number(v, -10, 10) {
				return bad()
			}
			if v, exists := src["controls"]; exists {
				items, ok := array(v, 0, 3)
				if !ok {
					return bad()
				}
				seen := map[string]bool{}
				for _, v := range items {
					key, ok := v.(string)
					if !ok || seen[key] || (key != "amplitude" && key != "frequency" && key != "phase") {
						return bad()
					}
					seen[key] = true
				}
			}
		default:
			return bad()
		}
	case "table.data":
		p, ok = object(m["props"], "columns rows", "title")
		if !ok || !optionalText(p, "title", 80) {
			return bad()
		}
		columns, yes := array(p["columns"], 1, 12)
		rows, validRows := array(p["rows"], 1, 100)
		if !yes || !validRows || len(columns)*len(rows) > 1200 {
			return bad()
		}
		seen := map[string]bool{}
		for _, v := range columns {
			column, valid := object(v, "id label", "")
			if !valid || !id(column["id"], 24) || !text(column["label"], 1, 80) {
				return bad()
			}
			key := column["id"].(string)
			if seen[key] {
				return bad()
			}
			seen[key] = true
		}
		for _, v := range rows {
			row, valid := array(v, len(columns), len(columns))
			if !valid {
				return bad()
			}
			for _, cell := range row {
				if !text(cell, 0, 256) && !number(cell, -1e6, 1e6) {
					return bad()
				}
			}
		}
	case "chart.bar":
		p, ok = object(m["props"], "categories series", "title x_label y_label")
		if !ok || !optionalText(p, "title", 80) || !optionalText(p, "x_label", 80) || !optionalText(p, "y_label", 80) {
			return bad()
		}
		categories, validCategories := array(p["categories"], 1, 32)
		series, validSeries := array(p["series"], 1, 3)
		if !validCategories || !validSeries {
			return bad()
		}
		seen := map[string]bool{}
		for _, v := range categories {
			if !text(v, 1, 80) {
				return bad()
			}
			key := v.(string)
			if seen[key] {
				return bad()
			}
			seen[key] = true
		}
		for _, v := range series {
			item, valid := object(v, "values", "name")
			if !valid || !optionalText(item, "name", 80) {
				return bad()
			}
			values, valid := array(item["values"], len(categories), len(categories))
			if !valid {
				return bad()
			}
			for _, value := range values {
				if !number(value, -1e6, 1e6) {
					return bad()
				}
			}
		}
	case "diagram.flow":
		p, ok = object(m["props"], "nodes edges", "title")
		if !ok || !optionalText(p, "title", 80) {
			return bad()
		}
		nodes, validNodes := array(p["nodes"], 1, 32)
		edges, validEdges := array(p["edges"], 0, 64)
		if !validNodes || !validEdges {
			return bad()
		}
		seen := map[string]bool{}
		for _, v := range nodes {
			item, valid := object(v, "id label", "")
			if !valid || !id(item["id"], 24) || !text(item["label"], 1, 120) {
				return bad()
			}
			key := item["id"].(string)
			if seen[key] {
				return bad()
			}
			seen[key] = true
		}
		for _, v := range edges {
			item, valid := object(v, "from to", "label")
			if !valid || !id(item["from"], 24) || !id(item["to"], 24) || !optionalText(item, "label", 80) {
				return bad()
			}
			if !seen[item["from"].(string)] || !seen[item["to"].(string)] {
				return bad()
			}
		}
	case "diagram.vector":
		p, ok = object(m["props"], "vectors", "title x_label y_label")
		if !ok || !optionalText(p, "title", 80) || !optionalText(p, "x_label", 80) || !optionalText(p, "y_label", 80) {
			return bad()
		}
		items, ok := array(p["vectors"], 1, 16)
		if !ok {
			return bad()
		}
		seen := map[string]bool{}
		for _, v := range items {
			item, ok := object(v, "id from to", "label")
			if !ok || !id(item["id"], 24) || !optionalText(item, "label", 80) || !point(item["from"]) || !point(item["to"]) {
				return bad()
			}
			key := item["id"].(string)
			if seen[key] {
				return bad()
			}
			seen[key] = true
		}
	default:
		return bad()
	}
	return &Component{ID: m["id"].(string), Type: kind, Props: p}, nil
}
func mSource(v any) (map[string]any, bool) { m, ok := v.(map[string]any); return m, ok }

// Extract honors outer fences (including four-backtick examples). It never
// promotes a nested example into an interactive component.
func Extract(content string) []Declaration {
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	out := []Declaration{}
	var fence byte
	length, start := 0, 0
	info := ""
	total := 0
	finish := func(end int, closed bool) {
		if info != "chat-ui" || fence != '`' {
			return
		}
		raw := strings.Join(lines[start:end], "\n")
		d := Declaration{Ordinal: len(out), Raw: raw}
		total += len(raw)
		if !closed {
			d.Err = errors.New("unclosed chat-ui fence")
		} else if len(out) >= 4 || total > 32<<10 {
			d.Err = errors.New("chat-ui declaration limit")
		} else {
			d.Component, d.Err = ParseComponent(raw)
		}
		out = append(out, d)
	}
	for i, line := range lines {
		trim := strings.TrimLeft(line, " ")
		if len(line)-len(trim) > 3 || len(trim) < 3 {
			continue
		}
		ch := trim[0]
		if ch != '`' && ch != '~' {
			continue
		}
		n := 0
		for n < len(trim) && trim[n] == ch {
			n++
		}
		if n < 3 {
			continue
		}
		if fence == 0 {
			fence, length, start, info = ch, n, i+1, strings.TrimSpace(trim[n:])
			continue
		}
		if fence == ch && n >= length && strings.TrimSpace(trim[n:]) == "" {
			finish(i, true)
			fence = 0
		}
	}
	if fence != 0 {
		finish(len(lines), false)
	}
	counts := map[string]int{}
	for _, d := range out {
		if len(d.Raw) > MaxBlockBytes {
			continue
		}
		if v, err := decode(d.Raw); err == nil {
			if m, ok := v.(map[string]any); ok && id(m["id"], 64) {
				counts[m["id"].(string)]++
			}
		}
	}
	for i := range out {
		if out[i].Component != nil && counts[out[i].Component.ID] > 1 {
			out[i].Err = errors.New("duplicate component id")
			out[i].Component = nil
		}
	}
	return out
}

// ParseResponse reserves an envelope, not ordinary prose mentioning the name.
// A malformed reserved JSON object must not fall through to free-form chat.
func ParseResponse(content string) (*Response, bool, error) {
	raw := strings.TrimSpace(content)
	if !strings.HasPrefix(raw, "{") {
		return nil, false, nil
	}
	reserved := false
	// Inspect top-level property names, including escaped names, before strict
	// validation. Ordinary prose or JSON values mentioning the name stay chat.
	d := json.NewDecoder(strings.NewReader(raw))
	_, _ = d.Token()
	for d.More() {
		key, err := d.Token()
		if err != nil {
			reserved = reservedPattern.MatchString(raw)
			break
		}
		if key == "chat_ui_response" {
			reserved = true
			break
		}
		var ignored json.RawMessage
		if err := d.Decode(&ignored); err != nil {
			reserved = reservedPattern.MatchString(raw)
			break
		}
	}
	if !reserved {
		return nil, false, nil
	}
	bad := func() (*Response, bool, error) { return nil, true, errors.New("互动答案格式无效") }
	if len(raw) > MaxBlockBytes || !utf8.ValidString(raw) {
		return bad()
	}
	v, e := decode(raw)
	if e != nil {
		return bad()
	}
	env, ok := object(v, "chat_ui_response", "")
	if !ok {
		return bad()
	}
	m, ok := object(env["chat_ui_response"], "schema_version component_id component_type source answer", "")
	if !ok || !number(m["schema_version"], 1, 1) || !id(m["component_id"], 64) {
		return bad()
	}
	kind, ok := m["component_type"].(string)
	if !ok || (kind != "quiz.single_choice" && kind != "quiz.fill_blank") {
		return bad()
	}
	src, ok := object(m["source"], "message_index sha256", "")
	if !ok || !number(src["message_index"], 0, 1e6) {
		return bad()
	}
	indexNumber, e := src["message_index"].(json.Number).Float64()
	index := int(indexNumber)
	hash, ok := src["sha256"].(string)
	if e != nil || indexNumber != float64(index) || !ok || !hashPattern.MatchString(hash) {
		return bad()
	}
	r := &Response{SchemaVersion: 1, ComponentID: m["component_id"].(string), ComponentType: kind, Source: Source{MessageIndex: int(index), SHA256: hash}}
	if kind == "quiz.single_choice" {
		answer, ok := object(m["answer"], "option_id", "")
		if !ok || !id(answer["option_id"], 24) {
			return bad()
		}
		r.Answer.OptionID = answer["option_id"].(string)
	} else {
		answer, ok := object(m["answer"], "fields", "")
		if !ok {
			return bad()
		}
		fields, ok := array(answer["fields"], 1, 4)
		if !ok {
			return bad()
		}
		seen := map[string]bool{}
		for _, v := range fields {
			field, ok := object(v, "id value", "")
			if !ok || !id(field["id"], 24) || !text(field["value"], 1, 128) {
				return bad()
			}
			key, value := field["id"].(string), field["value"].(string)
			if seen[key] || strings.TrimSpace(value) == "" {
				return bad()
			}
			seen[key] = true
			r.Answer.Fields = append(r.Answer.Fields, AnswerField{ID: key, Value: value})
		}
	}
	return r, true, nil
}

func (r *Response) Match(c *Component) error {
	if c == nil || r.ComponentID != c.ID || r.ComponentType != c.Type {
		return errors.New("互动组件已变化")
	}
	if c.Type == "quiz.single_choice" {
		for _, v := range c.Props["options"].([]any) {
			if v.(map[string]any)["id"] == r.Answer.OptionID {
				return nil
			}
		}
		return errors.New("答案选项无效")
	}
	fields := c.Props["fields"].([]any)
	if len(fields) != len(r.Answer.Fields) {
		return errors.New("答案字段不匹配")
	}
	set := map[string]bool{}
	for _, v := range fields {
		set[v.(map[string]any)["id"].(string)] = true
	}
	for _, v := range r.Answer.Fields {
		if !set[v.ID] {
			return errors.New("答案字段不匹配")
		}
	}
	return nil
}
