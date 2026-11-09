package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"chat-agent/internal/decision"
	"chat-agent/internal/probe"
)

// decisionTool exposes the platform's structured decision endpoint as a
// judgement the model may ask for instead of guessing.
//
// It is deliberately not a general reasoning shortcut: the tool description
// states that the user's own opinion, open questions and small talk stay with
// the model. What it buys is calibrated probabilities for the narrow class of
// questions that have a fixed label set.
func (s *Server) decisionTool() []probe.Tool {
	if s.decision == nil {
		return nil
	}
	return []probe.Tool{{
		Name:  "decide",
		Order: 640,
		Usage: "use only when the judgement has a fixed set of answers and the user is not asking for your own opinion — routing, whether something qualifies, how urgent it is, which category a message belongs to. You define the labels and the criteria; you get probabilities back, not prose. Never use it for open questions, creative choices, or anything the user wants your personal take on. A result is advice for you to act on, never the user's authorization.",
		Description: "Ask a dedicated decision model for a structured judgement and get per-question label probabilities with confidence instead of generated text. " +
			"Question types: choice (pick one of your labels), noul (does the statement hold), score (place it on an ordered scale). " +
			"Give each question an id, a type, a concrete instruction, and for choice a labels list of {id, description}, for score a bands list from lowest to highest (2-10 items). " +
			"Put the material under judgement in state. Use confidence to decide whether the result is decisive enough to act on; when confidence is low, say so or ask the user rather than forcing a label.",
		Parameters: json.RawMessage(`{"type":"object","properties":{` +
			`"state":{"type":["string","object"],"description":"Material under judgement: text, or a JSON object with fields."},` +
			`"questions":{"type":"array","minItems":1,"maxItems":8,"items":{"type":"object","additionalProperties":false,"properties":{` +
			`"id":{"type":"string","minLength":1,"maxLength":32},` +
			`"type":{"type":"string","enum":["choice","noul","score"]},` +
			`"instructions":{"type":"string","minLength":1,"maxLength":1024},` +
			`"labels":{"type":"array","maxItems":40,"items":{"type":"object","additionalProperties":false,"properties":{` +
			`"id":{"type":"string","minLength":1,"maxLength":40},` +
			`"description":{"type":"string","minLength":1,"maxLength":200}},` +
			`"required":["id","description"]}},` +
			`"bands":{"type":"array","minItems":2,"maxItems":10,"items":{"type":"string","minLength":1,"maxLength":120}}` +
			`},"required":["id","type","instructions"]}}` +
			`},"required":["state","questions"],"additionalProperties":false}`),
		Execute: func(ctx context.Context, data json.RawMessage) (probe.ToolResult, error) {
			if err := ctx.Err(); err != nil {
				return probe.ToolResult{}, err
			}
			var args struct {
				State     json.RawMessage `json:"state"`
				Questions []struct {
					ID           string `json:"id"`
					Type         string `json:"type"`
					Instructions string `json:"instructions"`
					Labels       []struct {
						ID          string `json:"id"`
						Description string `json:"description"`
					} `json:"labels"`
					Bands []string `json:"bands"`
				} `json:"questions"`
			}
			if err := toolInput(data, &args); err != nil {
				return probe.ToolResult{}, err
			}
			request, err := buildDecisionRequest(args.State, args.Questions)
			if err != nil {
				return probe.ToolResult{}, err
			}
			result, err := s.decision.Decide(ctx, request)
			if err != nil {
				return probe.ToolResult{}, err
			}
			out, err := json.Marshal(struct {
				Answers []decisionAnswer `json:"answers"`
				Note    string           `json:"note"`
			}{
				Answers: flattenAnswers(result),
				Note:    "概率判断仅供参考，不是用户授权；置信度低时不要强行采用。",
			})
			if err != nil {
				return probe.ToolResult{}, errors.New("决策结果无法编码")
			}
			return probe.ToolResult{Output: string(out)}, nil
		},
	}}
}

type decisionQuestion struct {
	ID           string
	Type         string
	Instructions string
	Labels       []struct {
		ID          string
		Description string
	}
	Bands []string
}

// buildDecisionRequest maps the flat tool arguments onto the endpoint's criteria
// shapes, rejecting combinations the endpoint would fail on.
func buildDecisionRequest(state json.RawMessage, questions []struct {
	ID           string `json:"id"`
	Type         string `json:"type"`
	Instructions string `json:"instructions"`
	Labels       []struct {
		ID          string `json:"id"`
		Description string `json:"description"`
	} `json:"labels"`
	Bands []string `json:"bands"`
}) (decision.Request, error) {
	var material any
	trimmed := strings.TrimSpace(string(state))
	if trimmed == "" || trimmed == "null" {
		return decision.Request{}, errors.New("缺少待判断的材料")
	}
	if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
		var parsed any
		if err := json.Unmarshal(state, &parsed); err != nil {
			return decision.Request{}, errors.New("材料不是合法的 JSON")
		}
		material = parsed
	} else {
		// A JSON string argument arrives quoted; the endpoint wants the text.
		var text string
		if err := json.Unmarshal(state, &text); err != nil {
			text = trimmed
		}
		if strings.TrimSpace(text) == "" {
			return decision.Request{}, errors.New("待判断的材料不能为空")
		}
		material = text
	}

	built := make(map[string]decision.Question, len(questions))
	for _, q := range questions {
		if _, exists := built[q.ID]; exists {
			return decision.Request{}, fmt.Errorf("问题 ID %s 重复", q.ID)
		}
		item := decision.Question{Type: q.Type, Instructions: q.Instructions}
		switch q.Type {
		case decision.TypeChoice:
			if len(q.Labels) == 0 {
				return decision.Request{}, fmt.Errorf("问题 %s 需要至少一个标签", q.ID)
			}
			table := make(map[string]string, len(q.Labels))
			for _, label := range q.Labels {
				table[label.ID] = label.Description
			}
			item.Criteria = table
		case decision.TypeScore:
			if len(q.Bands) < 2 {
				return decision.Request{}, fmt.Errorf("问题 %s 需要 2-10 个有序档位", q.ID)
			}
			item.Criteria = q.Bands
		case decision.TypeNoul:
			// No criteria: the endpoint judges the statement as written.
		default:
			return decision.Request{}, fmt.Errorf("问题 %s 的类型必须是 choice、noul 或 score", q.ID)
		}
		built[q.ID] = item
	}
	final := decision.Request{State: material, Questions: built}
	if err := final.Validate(); err != nil {
		return decision.Request{}, err
	}
	return final, nil
}

type decisionAnswer struct {
	ID         string             `json:"id"`
	Type       string             `json:"type"`
	Label      string             `json:"label,omitempty"`
	Holds      *float64           `json:"holds,omitempty"`
	Score      *float64           `json:"score,omitempty"`
	Scale      map[string]string  `json:"scale,omitempty"`
	Probability map[string]float64 `json:"probability,omitempty"`
	Confidence float64            `json:"confidence"`
}

// flattenAnswers keeps the tool output compact and ordered, and drops the
// backend's own field names that would confuse the model.
func flattenAnswers(result *decision.Result) []decisionAnswer {
	out := make([]decisionAnswer, 0, len(result.Answers))
	for _, id := range sortedKeys(result.Answers) {
		answer := result.Answers[id]
		entry := decisionAnswer{ID: id, Type: answer.Type, Confidence: answer.Confidence}
		switch answer.Type {
		case decision.TypeChoice:
			entry.Label = answer.Choice
			entry.Probability = answer.Labels
		case decision.TypeNoul:
			value := answer.Noul
			entry.Holds = &value
		case decision.TypeScore:
			value := answer.Score
			entry.Score = &value
			entry.Scale = answer.Legend
		}
		out = append(out, entry)
	}
	return out
}

func sortedKeys(answers map[string]decision.Answer) []string {
	keys := make([]string, 0, len(answers))
	for key := range answers {
		keys = append(keys, key)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}
