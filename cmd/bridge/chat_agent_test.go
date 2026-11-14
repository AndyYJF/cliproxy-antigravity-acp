package main

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestChatAgentHandoffAndReplay(t *testing.T) {
	fixture(t)
	for _, stream := range []bool{false, true} {
		body, _ := json.Marshal(object{"model": "antigravity-acp/gemini-3.8-flash", "messages": []object{{"role": "user", "content": "__CLIENT_TOOL__"}}, "stream": stream, "tools": []object{{"type": "function", "function": object{"name": "lookup", "parameters": object{"type": "object"}}}}})
		w := httptest.NewRecorder()
		chat(w, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(string(body))))
		output := w.Body.String()
		if w.Code != 200 || !strings.Contains(output, `"name":"lookup"`) || !strings.Contains(output, `"finish_reason":"tool_calls"`) || strings.Contains(output, "PRIVATE_CLI_SUMMARY") {
			t.Fatalf("handoff: %d %s", w.Code, output)
		}
		var message object
		if stream {
			for _, line := range strings.Split(output, "\n") {
				if !strings.HasPrefix(line, "data: {") {
					continue
				}
				var chunk struct{ Choices []struct{ Delta object } }
				_ = json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &chunk)
				if len(chunk.Choices) > 0 && chunk.Choices[0].Delta["tool_calls"] != nil {
					message = chunk.Choices[0].Delta
					message["role"] = "assistant"
				}
			}
			if !strings.Contains(output, "[DONE]") {
				t.Fatal(output)
			}
		} else {
			var result struct{ Choices []struct{ Message object } }
			_ = json.Unmarshal(w.Body.Bytes(), &result)
			message = result.Choices[0].Message
		}
		if message == nil {
			t.Fatal("missing tool message")
		}
		calls := message["tool_calls"].([]any)
		call := calls[0].(map[string]any)
		replay := object{"model": "antigravity-acp/gemini-3.8-flash", "messages": []object{{"role": "user", "content": "lookup"}, message, {"role": "tool", "tool_call_id": call["id"], "content": "UNIQUE_REAL_RESULT"}}, "tools": []any{}}
		raw, _ := json.Marshal(replay)
		plan, err := planChatAgent(raw)
		if err != nil || !strings.Contains(plan.Prompt, "UNIQUE_REAL_RESULT") {
			t.Fatalf("replay: %v", err)
		}
		w = httptest.NewRecorder()
		chat(w, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(string(raw))))
		if w.Code != 200 || !strings.Contains(w.Body.String(), "LOCAL_FIXTURE_OK") {
			t.Fatal(w.Body.String())
		}
		replay["messages"] = []object{{"role": "tool", "tool_call_id": "unknown", "content": "orphan"}}
		raw, _ = json.Marshal(replay)
		if _, err = planChatAgent(raw); err == nil {
			t.Fatal("accepted orphan tool result")
		}
	}
}
func TestChatAgentFailureNotCompleted(t *testing.T) {
	fixture(t)
	for _, prompt := range []string{"__SERVER_TOOL__", "__FAIL__"} {
		raw, _ := json.Marshal(object{"model": "antigravity-acp/gemini-3.8-flash", "stream": true, "messages": []object{{"role": "user", "content": prompt}}, "tools": []any{}})
		w := httptest.NewRecorder()
		chat(w, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(string(raw))))
		if !strings.Contains(w.Body.String(), `"error"`) || strings.Contains(w.Body.String(), "[DONE]") {
			t.Fatal(w.Body.String())
		}
	}
}

func TestChatAgentImagesInToolHistory(t *testing.T) {
	fixture(t)
	img := fixtureImage(t)
	body := object{"model": "antigravity-acp/gemini-3.8-flash", "messages": []object{
		{"role": "user", "content": []object{{"type": "text", "text": "__CHECK_IMAGES__"}, {"type": "image_url", "image_url": object{"url": img}}}},
		{"role": "assistant", "content": nil, "tool_calls": []object{{"id": "call_image", "type": "function", "function": object{"name": "read", "arguments": "{}"}}}},
		{"role": "tool", "tool_call_id": "call_image", "content": []object{{"type": "text", "text": "image result"}, {"type": "image_url", "image_url": object{"url": img}}}},
	}, "tools": []any{}}
	raw, _ := json.Marshal(body)
	w := httptest.NewRecorder()
	chat(w, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(string(raw))))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "LOCAL_FIXTURE_OK") {
		t.Fatal(w.Code, w.Body.String())
	}
}
