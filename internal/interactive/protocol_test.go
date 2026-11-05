package interactive

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestProtocolBoundaries(t *testing.T) {
	// Documentation examples are the shared contract, not a second fixture set.
	data, err := os.ReadFile("../../docs/schemas/chat-ui-v1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Examples []json.RawMessage `json:"examples"`
	}
	if err = json.Unmarshal(data, &schema); err != nil {
		t.Fatal(err)
	}
	for _, example := range schema.Examples {
		if _, err = ParseComponent(string(example)); err != nil {
			t.Fatalf("schema example: %v", err)
		}
	}
	valid := string(schema.Examples[0])
	fence := func(raw string) string { return "```chat-ui\n" + raw + "\n```" }
	for _, raw := range []string{
		strings.Replace(valid, `"schema_version": 1`, `"schema_version": 2`, 1),
		strings.Replace(valid, `"props": {`, `"props": {"onclick":"alert(1)",`, 1),
		strings.Replace(valid, `"props": {`, `"props": {"__proto__":{},`, 1),
		strings.Repeat(" ", MaxBlockBytes) + valid,
		`{"schema_version":1,"id":"x","type":"chart.line","props":{"source":{"kind":"sine","amplitude":1e309,"frequency":1,"duration":1}}}`,
	} {
		if _, err = ParseComponent(raw); err == nil {
			t.Fatalf("invalid declaration accepted: %.100s", raw)
		}
	}
	if got := Extract("````markdown\n" + fence(valid) + "\n````"); len(got) != 0 {
		t.Fatal("nested example became interactive")
	}
	if got := Extract(fence(valid) + "\n" + fence(valid)); len(got) != 2 || got[0].Err == nil || got[1].Err == nil {
		t.Fatal("duplicate IDs not both rejected")
	}
	if got := Extract("```chat-ui\n" + valid); len(got) != 1 || got[0].Err == nil {
		t.Fatal("unclosed fence accepted")
	}
	for _, raw := range []string{`{"chat_ui_response":`, `{"\u0063hat_ui_response":{}}`, `{"chat_ui_response":{},"chat_ui_response":{}}`} {
		if _, reserved, err := ParseResponse(raw); !reserved || err == nil {
			t.Fatal("reserved malformed envelope became prose")
		}
	}
	if _, reserved, err := ParseResponse("Please explain chat_ui_response"); reserved || err != nil {
		t.Fatal("ordinary prose reserved")
	}
}
