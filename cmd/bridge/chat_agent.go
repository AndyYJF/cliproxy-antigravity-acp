package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

func isAgentChat(raw []byte) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return false
	}
	for _, key := range []string{"tools", "tool_choice"} {
		if v := fields[key]; len(v) > 0 && string(v) != "null" {
			return true
		}
	}
	var messages []struct {
		Role      string
		ToolCalls json.RawMessage `json:"tool_calls"`
	}
	_ = json.Unmarshal(fields["messages"], &messages)
	for _, m := range messages {
		if m.Role == "tool" || len(m.ToolCalls) > 0 && string(m.ToolCalls) != "null" && string(m.ToolCalls) != "[]" {
			return true
		}
	}
	return false
}

func planChatAgent(raw []byte) (responsePlan, error) {
	bad := func(s string) (responsePlan, error) { return responsePlan{}, &httpError{400, s} }
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return bad("Invalid Chat Completions JSON")
	}
	for _, k := range []string{"functions", "function_call", "modalities", "audio"} {
		if v := fields[k]; len(v) > 0 && string(v) != "null" {
			return bad(k + " is not supported by the ACP chat adapter")
		}
	}
	var messages []struct {
		Role         string          `json:"role"`
		Content      any             `json:"content"`
		ToolCallID   string          `json:"tool_call_id"`
		FunctionCall json.RawMessage `json:"function_call"`
		ToolCalls    []struct {
			ID       string `json:"id"`
			Type     string `json:"type"`
			Function struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			} `json:"function"`
		} `json:"tool_calls"`
	}
	if json.Unmarshal(fields["messages"], &messages) != nil || len(messages) == 0 {
		return bad("messages must not be empty")
	}
	// Translate only content block names; image validation stays in the shared planner.
	content := func(value any) any {
		if parts, ok := value.([]any); ok {
			for _, part := range parts {
				if p, ok := part.(map[string]any); ok {
					if p["type"] == "text" {
						p["type"] = "input_text"
					}
					if p["type"] == "image_url" {
						p["type"] = "input_image"
					}
				}
			}
		}
		return value
	}
	history := []object{}
	seen := map[string]bool{}
	pending := map[string]bool{}
	for _, m := range messages {
		if len(m.FunctionCall) > 0 && string(m.FunctionCall) != "null" {
			return bad("Legacy function_call messages are not supported; use tool_calls")
		}
		if m.Role == "tool" {
			if len(m.ToolCalls) > 0 || !pending[m.ToolCallID] {
				return bad("Tool result must match an unresolved tool_call_id")
			}
			delete(pending, m.ToolCallID)
			history = append(history, object{"type": "function_call_output", "call_id": m.ToolCallID, "output": content(m.Content)})
			continue
		}
		if len(pending) > 0 {
			return bad("Supply results for all tool calls before continuing the conversation")
		}
		if m.Role != "system" && m.Role != "developer" && m.Role != "user" && m.Role != "assistant" {
			return bad("Unsupported message role")
		}
		if len(m.ToolCalls) > 0 && m.Role != "assistant" {
			return bad("Only assistant messages may contain tool_calls")
		}
		if m.Content != nil {
			history = append(history, object{"type": "message", "role": m.Role, "content": content(m.Content)})
		} else if len(m.ToolCalls) == 0 {
			return bad("Message content is required")
		}
		for _, call := range m.ToolCalls {
			var args map[string]any
			if call.ID == "" || seen[call.ID] || call.Type != "function" || call.Function.Name == "" || json.Unmarshal([]byte(call.Function.Arguments), &args) != nil || args == nil {
				return bad("Invalid or duplicate function tool call")
			}
			seen[call.ID] = true
			pending[call.ID] = true
			history = append(history, object{"type": "function_call", "call_id": call.ID, "name": call.Function.Name, "arguments": call.Function.Arguments})
		}
	}
	if len(pending) > 0 {
		return bad("Supply results for all tool calls")
	}
	if role := messages[len(messages)-1].Role; role != "user" && role != "tool" {
		return bad("The last message must be a user message or tool result")
	}
	var tools []struct {
		Type     string          `json:"type"`
		Function json.RawMessage `json:"function"`
	}
	if v := fields["tools"]; len(v) > 0 && json.Unmarshal(v, &tools) != nil {
		return bad("Invalid tools array")
	}
	flattened := []object{}
	for _, tool := range tools {
		var fn object
		if tool.Type != "function" || json.Unmarshal(tool.Function, &fn) != nil || fn == nil {
			return bad("Only function tools are supported")
		}
		fn["type"] = "function"
		flattened = append(flattened, fn)
	}
	fields["input"], _ = json.Marshal(history)
	fields["tools"], _ = json.Marshal(flattened)
	delete(fields, "messages")
	converted, _ := json.Marshal(fields)
	return planResponse(converted)
}

func chatAgent(w http.ResponseWriter, r *http.Request, raw []byte) {
	plan, err := planChatAgent(raw)
	if err != nil {
		sendError(w, err)
		return
	}
	writer := &chatAgentWriter{w: w, stream: plan.Request.Stream, model: plan.Request.Model, id: identifier("chatcmpl-acp-"), created: time.Now().Unix()}
	runAgentPlan(w, r, plan, writer)
}

type chatAgentWriter struct {
	w         http.ResponseWriter
	stream    bool
	model, id string
	created   int64
	text      strings.Builder
	call      object
}

func (w *chatAgentWriter) emit(delta object, finish any) error {
	b, _ := json.Marshal(object{"id": w.id, "object": "chat.completion.chunk", "created": w.created, "model": w.model, "choices": []object{{"index": 0, "delta": delta, "finish_reason": finish}}})
	if _, err := fmt.Fprintf(w.w, "data: %s\n\n", b); err != nil {
		return err
	}
	return http.NewResponseController(w.w).Flush()
}
func (w *chatAgentWriter) start() error {
	if !w.stream {
		return nil
	}
	w.w.Header().Set("Content-Type", "text/event-stream")
	w.w.Header().Set("Cache-Control", "no-cache")
	w.w.Header().Set("X-Accel-Buffering", "no")
	return w.emit(object{"role": "assistant", "content": ""}, nil)
}
func (w *chatAgentWriter) delta(s string) error {
	if w.text.Len()+len(s) > 8<<20 {
		return fmt.Errorf("ACP response exceeded size limit")
	}
	w.text.WriteString(s)
	if w.stream {
		return w.emit(object{"content": s}, nil)
	}
	return nil
}
func (w *chatAgentWriter) finishText() error { return nil }
func (w *chatAgentWriter) tool(tool clientTool, call clientInvocation) error {
	callID := call.CallID
	if callID == "" {
		callID = identifier("call_")
	}
	w.call = object{"id": callID, "type": "function", "function": object{"name": tool.OriginalName, "arguments": string(call.Arguments)}}
	if w.stream {
		delta := object{"index": 0}
		for k, v := range w.call {
			delta[k] = v
		}
		return w.emit(object{"tool_calls": []object{delta}}, nil)
	}
	return nil
}
func (w *chatAgentWriter) fail(err error) {
	if !w.stream {
		sendError(w.w, err)
		return
	}
	b, _ := json.Marshal(object{"error": object{"message": err.Error(), "type": "antigravity_acp_error"}})
	_, _ = fmt.Fprintf(w.w, "data: %s\n\n", b)
	_ = http.NewResponseController(w.w).Flush()
}
func (w *chatAgentWriter) complete() error {
	finish := "stop"
	if w.call != nil {
		finish = "tool_calls"
	}
	if w.stream {
		if err := w.emit(object{}, finish); err != nil {
			return err
		}
		if _, err := fmt.Fprint(w.w, "data: [DONE]\n\n"); err != nil {
			return err
		}
		return http.NewResponseController(w.w).Flush()
	}
	message := object{"role": "assistant", "content": w.text.String()}
	if w.call != nil {
		message["tool_calls"] = []object{w.call}
		if w.text.Len() == 0 {
			message["content"] = nil
		}
	}
	return sendJSON(w.w, 200, object{"id": w.id, "object": "chat.completion", "created": w.created, "model": w.model, "choices": []object{{"index": 0, "message": message, "finish_reason": finish}}})
}

func (w *chatAgentWriter) history() []object {
	items := []object{}
	if w.text.Len() > 0 {
		items = append(items, object{"type": "message", "role": "assistant", "content": w.text.String()})
	}
	if w.call != nil {
		fn := w.call["function"].(object)
		items = append(items, object{"type": "function_call", "call_id": w.call["id"], "name": fn["name"], "arguments": fn["arguments"]})
	}
	return canonicalHistory(items)
}
