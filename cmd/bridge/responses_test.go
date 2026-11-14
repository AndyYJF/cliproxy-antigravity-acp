package main

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestResponseValidation(t *testing.T) {
	base := object{"model": "antigravity-acp/gemini-3.8-flash", "input": "hello", "store": false}
	for key, value := range map[string]any{"previous_response_id": "old", "tools": []object{{"type": "web_search"}}, "store": true, "input": []object{{"type": "message", "role": "user", "content": []object{{"type": "input_image", "image_url": "hidden"}}}}} {
		t.Run(key, func(t *testing.T) {
			body := object{}
			for k, v := range base {
				body[k] = v
			}
			body[key] = value
			raw, _ := json.Marshal(body)
			if _, err := planResponse(raw); err == nil {
				t.Fatal("accepted unsupported request")
			}
		})
	}
	raw := []byte(`{"model":"antigravity-acp/gemini-3.8-flash","input":[{"type":"function_call","call_id":"call1","name":"lookup","arguments":"{}"},{"type":"function_call_output","call_id":"call1","output":"UNIQUE_REAL_RESULT"}],"tools":[{"type":"namespace","name":"client","tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}]},{"type":"custom","name":"apply_patch","format":{"type":"text"}}]}`)
	plan, err := planResponse(raw)
	if err != nil || !strings.Contains(plan.Prompt, "UNIQUE_REAL_RESULT") || len(plan.Tools) != 2 || plan.Tools[0].Namespace != "client" || !plan.Tools[1].Custom {
		t.Fatalf("history or tool schema lost: %v", err)
	}
}
func TestResponseNativeToolHandoff(t *testing.T) {
	fixture(t)
	for _, stream := range []bool{false, true} {
		body, _ := json.Marshal(object{"model": "antigravity-acp/gemini-3.8-flash", "input": "__CLIENT_TOOL__", "stream": stream, "tools": []object{{"type": "function", "name": "lookup", "parameters": object{"type": "object"}}}})
		w := httptest.NewRecorder()
		responses(w, httptest.NewRequest("POST", "/v1/responses", strings.NewReader(string(body))))
		output := w.Body.String()
		if w.Code != 200 || !strings.Contains(output, `"name":"lookup"`) || !strings.Contains(output, `\"value\":\"fixture\"`) || !strings.Contains(output, "call_") || strings.Contains(output, "PRIVATE_CLI_SUMMARY") {
			t.Fatalf("invalid tool handoff %d %s", w.Code, output)
		}
		if stream && (!strings.Contains(output, "response.function_call_arguments.delta") || !strings.Contains(output, "response.completed")) {
			t.Fatal(output)
		}
	}
}
func TestResponseFailureNotCompleted(t *testing.T) {
	fixture(t)
	for _, prompt := range []string{"__SERVER_TOOL__", "__FAIL__"} {
		body, _ := json.Marshal(object{"model": "antigravity-acp/gemini-3.8-flash", "input": prompt, "stream": true})
		w := httptest.NewRecorder()
		responses(w, httptest.NewRequest("POST", "/v1/responses", strings.NewReader(string(body))))
		output := w.Body.String()
		if !strings.Contains(output, "response.failed") || strings.Contains(output, "response.completed") {
			t.Fatal(output)
		}
	}
}
func TestResponseTextStream(t *testing.T) {
	fixture(t)
	w := httptest.NewRecorder()
	responses(w, httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"antigravity-acp/gemini-3.8-flash","input":"hello","stream":true}`)))
	output := w.Body.String()
	if !strings.Contains(output, "response.output_text.delta") || !strings.Contains(output, "LOCAL_FIXTURE_OK") || !strings.Contains(output, "response.completed") || strings.Contains(output, "PRIVATE_THOUGHT") {
		t.Fatal(output)
	}
}
