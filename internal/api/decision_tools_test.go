package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"chat-agent/internal/decision"
	"chat-agent/internal/probe"
)

type questionArg = struct {
	ID           string `json:"id"`
	Type         string `json:"type"`
	Instructions string `json:"instructions"`
	Labels       []struct {
		ID          string `json:"id"`
		Description string `json:"description"`
	} `json:"labels"`
	Bands []string `json:"bands"`
}

func TestDecisionToolSchemaRegisters(t *testing.T) {
	server := &Server{decision: decision.New(decision.Config{BaseURL: "https://example.com/v1", APIKey: "k", Model: "ecnu-decision"})}
	tools := server.decisionTool()
	if len(tools) != 1 || tools[0].Name != "decide" {
		t.Fatalf("expected one decide tool, got %+v", tools)
	}
	// The registry rejects unknown schema vocabulary, so this also proves the
	// parameter schema stays inside what the model can actually be offered.
	if _, err := probe.NewToolRegistry(tools); err != nil {
		t.Fatalf("schema rejected by registry: %v", err)
	}
	if disabled := (&Server{}).decisionTool(); disabled != nil {
		t.Fatal("an unconfigured server must not offer the tool")
	}
}

func TestBuildDecisionRequestMapsCriteria(t *testing.T) {
	choice := questionArg{ID: "mood", Type: "choice", Instructions: "情绪倾向"}
	choice.Labels = append(choice.Labels,
		struct {
			ID          string `json:"id"`
			Description string `json:"description"`
		}{"positive", "积极"},
		struct {
			ID          string `json:"id"`
			Description string `json:"description"`
		}{"negative", "消极"},
	)
	score := questionArg{ID: "urgent", Type: "score", Instructions: "紧急度", Bands: []string{"不紧急", "一般", "紧急"}}
	noul := questionArg{ID: "valid", Type: "noul", Instructions: "是否属于后勤范围"}

	req, err := buildDecisionRequest(json.RawMessage(`"用户宿舍空调坏了"`), []questionArg{choice, score, noul})
	if err != nil {
		t.Fatal(err)
	}
	if req.State != "用户宿舍空调坏了" {
		t.Fatalf("state not mapped: %#v", req.State)
	}
	if table, ok := req.Questions["mood"].Criteria.(map[string]string); !ok || table["positive"] != "积极" {
		t.Fatalf("choice criteria wrong: %#v", req.Questions["mood"].Criteria)
	}
	if bands, ok := req.Questions["urgent"].Criteria.([]string); !ok || len(bands) != 3 {
		t.Fatalf("score criteria wrong: %#v", req.Questions["urgent"].Criteria)
	}
	if req.Questions["valid"].Criteria != nil {
		t.Fatalf("noul should carry no criteria: %#v", req.Questions["valid"].Criteria)
	}
}

func TestBuildDecisionRequestAcceptsJSONStateAndRejectsBadInput(t *testing.T) {
	ok := questionArg{ID: "q", Type: "noul", Instructions: "是否成立"}
	req, err := buildDecisionRequest(json.RawMessage(`{"channel":"web","title":"挂失"}`), []questionArg{ok})
	if err != nil {
		t.Fatal(err)
	}
	if _, isObject := req.State.(map[string]any); !isObject {
		t.Fatalf("structured state should stay an object: %#v", req.State)
	}

	bad := []struct {
		name  string
		state string
		q     []questionArg
	}{
		{"empty state", `"   "`, []questionArg{ok}},
		{"unknown type", `"x"`, []questionArg{{ID: "q", Type: "bool", Instructions: "x"}}},
		{"choice without labels", `"x"`, []questionArg{{ID: "q", Type: "choice", Instructions: "x"}}},
		{"score with one band", `"x"`, []questionArg{{ID: "q", Type: "score", Instructions: "x", Bands: []string{"only"}}}},
	}
	for _, item := range bad {
		if _, err := buildDecisionRequest(json.RawMessage(item.state), item.q); err == nil {
			t.Fatalf("%s: expected rejection", item.name)
		}
	}
	if _, err := buildDecisionRequest(json.RawMessage(`"x"`), []questionArg{ok, ok}); err == nil {
		t.Fatal("duplicate question ids must be rejected")
	}
}

func TestDecisionToolExecuteReturnsCompactAnswers(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"d1","answers":{
			"mood":{"type":"choice","choice":"positive","probabilities":{"positive":0.91,"negative":0.09},"confidence":0.91},
			"urgent":{"type":"score","score":1.4,"legend":{"0":"不紧急","1":"一般","2":"紧急"},"probabilities":{"0":0.0,"1":0.6,"2":0.4},"confidence":0.6}
		},"usage":{"input_tokens":12},"gateway":{"requested_model":"ecnu-decision"}}`))
	}))
	defer server.Close()

	s := &Server{decision: decision.New(decision.Config{BaseURL: server.URL, APIKey: "k", Model: "ecnu-decision"})}
	tool := s.decisionTool()[0]
	args := `{"state":"今天心情不错","questions":[
		{"id":"mood","type":"choice","instructions":"情绪","labels":[{"id":"positive","description":"积极"},{"id":"negative","description":"消极"}]},
		{"id":"urgent","type":"score","instructions":"紧急度","bands":["不紧急","一般","紧急"]}]}`
	result, err := tool.Execute(context.Background(), json.RawMessage(args))
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Answers []decisionAnswer `json:"answers"`
		Note    string           `json:"note"`
	}
	if err := json.Unmarshal([]byte(result.Output), &out); err != nil {
		t.Fatalf("output is not JSON: %v", err)
	}
	if len(out.Answers) != 2 {
		t.Fatalf("expected two answers, got %+v", out.Answers)
	}
	// Sorted by id so the model sees a stable order.
	if out.Answers[0].ID != "mood" || out.Answers[1].ID != "urgent" {
		t.Fatalf("answers not ordered: %+v", out.Answers)
	}
	if out.Answers[0].Label != "positive" || out.Answers[0].Confidence != 0.91 {
		t.Fatalf("choice answer wrong: %+v", out.Answers[0])
	}
	if out.Answers[1].Score == nil || *out.Answers[1].Score != 1.4 || out.Answers[1].Scale["2"] != "紧急" {
		t.Fatalf("score answer wrong: %+v", out.Answers[1])
	}
	if !strings.Contains(out.Note, "不是用户授权") {
		t.Fatalf("note must restate the authorization boundary: %q", out.Note)
	}
}
