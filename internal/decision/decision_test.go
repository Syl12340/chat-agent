package decision

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func validRequest() Request {
	return Request{
		State: "用户说今天心情不错，想去公园",
		Questions: map[string]Question{
			"mood": {Type: TypeChoice, Instructions: "情绪倾向", Criteria: map[string]string{
				"positive": "积极", "neutral": "中性", "negative": "消极",
			}},
		},
	}
}

func TestValidateRejectsUnsupportedShapes(t *testing.T) {
	cases := map[string]Request{
		"nil state": {State: nil, Questions: map[string]Question{"q": {Type: TypeNoul, Instructions: "x"}}},
		"empty state": {State: "   ", Questions: map[string]Question{"q": {Type: TypeNoul, Instructions: "x"}}},
		"no questions": {State: "x", Questions: nil},
		"bad type": {State: "x", Questions: map[string]Question{"q": {Type: "bool", Instructions: "x"}}},
		"choice empty": {State: "x", Questions: map[string]Question{"q": {Type: TypeChoice, Instructions: "x", Criteria: map[string]string{}}}},
		"score one band": {State: "x", Questions: map[string]Question{"q": {Type: TypeScore, Instructions: "x", Criteria: []string{"only"}}}},
		"noul custom keys": {State: "x", Questions: map[string]Question{"q": {Type: TypeNoul, Instructions: "x", Criteria: map[string]string{"符合": "a", "不符合": "b"}}}},
		"bad image": {State: "x", Images: []string{"data:image/gif;base64,AAA"}, Questions: map[string]Question{"q": {Type: TypeNoul, Instructions: "x"}}},
		"missing instructions": {State: "x", Questions: map[string]Question{"q": {Type: TypeNoul}}},
	}
	for name, req := range cases {
		if err := req.Validate(); err == nil {
			t.Fatalf("%s: expected rejection", name)
		}
	}
	if err := validRequest().Validate(); err != nil {
		t.Fatalf("valid request rejected: %v", err)
	}
}

func TestDecideParsesAllThreeShapes(t *testing.T) {
	var got wireRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/systemone" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer key" {
			t.Errorf("missing bearer token")
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{
			"id":"decision-1","model":"vllm-sr/d3-flash",
			"answers":{
				"mood":{"type":"choice","choice":"positive","probabilities":{"positive":0.9,"neutral":0.08,"negative":0.02},"confidence":0.9},
				"ok":{"type":"noul","noul":0.97,"probabilities":{"yes":0.97,"no":0.03},"confidence":0.97},
				"urgent":{"type":"score","score":2.12,"legend":{"0":"不紧急","1":"一般","2":"紧急","3":"非常紧急"},"probabilities":{"0":0.01,"1":0.09,"2":0.65,"3":0.24},"confidence":0.65}
			},
			"usage":{"input_tokens":345,"output_tokens":0},
			"diagnostics":{"mood":{"label_mass":1.0,"argmax_is_label":true}},
			"gateway":{"requested_model":"ecnu-decision","upstream_latency_ms":82.9,"warnings":["root instructions removed"]}
		}`))
	}))
	defer server.Close()

	client := New(Config{BaseURL: server.URL, APIKey: "key", Model: "ecnu-decision"})
	if client == nil {
		t.Fatal("client should be enabled")
	}
	result, err := client.Decide(context.Background(), Request{
		State: map[string]any{"channel": "web"},
		Questions: map[string]Question{
			"mood":   {Type: TypeChoice, Instructions: "情绪", Criteria: map[string]string{"positive": "积极", "neutral": "中性", "negative": "消极"}},
			"ok":     {Type: TypeNoul, Instructions: "是否成立"},
			"urgent": {Type: TypeScore, Instructions: "紧急度", Criteria: []string{"不紧急", "一般", "紧急", "非常紧急"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Model != "ecnu-decision" {
		t.Fatalf("model not forwarded: %s", got.Model)
	}
	if result.Model != "ecnu-decision" {
		t.Fatalf("should report requested model, got %s", result.Model)
	}
	if result.Answers["mood"].Choice != "positive" || result.Answers["mood"].Confidence != 0.9 {
		t.Fatalf("choice answer wrong: %+v", result.Answers["mood"])
	}
	if result.Answers["ok"].Noul != 0.97 {
		t.Fatalf("noul answer wrong: %+v", result.Answers["ok"])
	}
	score := result.Answers["urgent"]
	if score.Score != 2.12 || score.Legend["3"] != "非常紧急" || len(score.Bands) != 4 {
		t.Fatalf("score answer wrong: %+v", score)
	}
	if result.InputTok != 345 || len(result.Warnings) != 1 {
		t.Fatalf("usage/warnings not captured: %+v", result)
	}
}

func TestDecideReadsBothErrorShapes(t *testing.T) {
	cases := map[string]string{
		"detail array": `{"detail":[{"type":"value_error","loc":["body","questions"],"msg":"score criteria must contain 2 to 10 ordered items"}]}`,
		"error object": `{"error":{"message":"the prompt has 20659 tokens, more than max_model_len=16384","type":"BadRequestError"}}`,
	}
	for name, body := range cases {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(body))
		}))
		client := New(Config{BaseURL: server.URL, APIKey: "key", Model: "ecnu-decision"})
		_, err := client.Decide(context.Background(), validRequest())
		server.Close()
		if err == nil {
			t.Fatalf("%s: expected error", name)
		}
		if !strings.Contains(err.Error(), "400") {
			t.Fatalf("%s: error should carry status: %v", name, err)
		}
		if strings.Contains(err.Error(), "无法识别的错误响应") {
			t.Fatalf("%s: should parse the message: %v", name, err)
		}
	}
}

func TestBreakerStopsRetryingAfterFailures(t *testing.T) {
	var hits int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()

	client := New(Config{BaseURL: server.URL, APIKey: "key", Model: "ecnu-decision"})
	for i := 0; i < 4; i++ {
		if _, err := client.Decide(context.Background(), validRequest()); err == nil {
			t.Fatal("expected failure")
		}
	}
	if hits != breakerThreshold {
		t.Fatalf("breaker should stop after %d calls, got %d", breakerThreshold, hits)
	}
}

func TestDisabledClientAndValidation(t *testing.T) {
	if New(Config{}) != nil {
		t.Fatal("incomplete config must not produce a client")
	}
	var client *Client
	if _, err := client.Decide(context.Background(), validRequest()); err == nil {
		t.Fatal("nil client should error")
	}
}
