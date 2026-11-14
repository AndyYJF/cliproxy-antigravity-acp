package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var errClientTool = errors.New("client tool handoff")

type responseRequest struct {
	Model        string            `json:"model"`
	Input        json.RawMessage   `json:"input"`
	Instructions string            `json:"instructions"`
	Stream       bool              `json:"stream"`
	Tools        []json.RawMessage `json:"tools"`
}
type responsePlan struct {
	History []object
	Policy  string
	Images  []inputImage
	Request responseRequest
	Prompt  string
	Tools   []clientTool
}

func identifier(prefix string) string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return prefix + hex.EncodeToString(b)
}
func planResponse(raw []byte) (responsePlan, error) {
	var plan responsePlan
	var fields map[string]json.RawMessage
	bad := func(message string) (responsePlan, error) { return plan, &httpError{400, message} }
	if json.Unmarshal(raw, &plan.Request) != nil || json.Unmarshal(raw, &fields) != nil {
		return bad("Invalid Responses JSON")
	}
	req := &plan.Request
	if !strings.HasPrefix(req.Model, "antigravity-acp/gemini") {
		return bad("Select an antigravity-acp/ Gemini model")
	}
	for _, key := range []string{"previous_response_id", "conversation", "background"} {
		if v := fields[key]; len(v) > 0 && string(v) != "null" && string(v) != "false" {
			return bad(key + " is not supported; replay the full input with store=false")
		}
	}
	if v := fields["store"]; len(v) > 0 && string(v) != "false" && string(v) != "null" {
		return bad("Only store=false is supported")
	}
	if v := fields["tool_choice"]; len(v) > 0 && string(v) != "\"auto\"" && string(v) != "\"none\"" && string(v) != "null" {
		return bad("Only auto or none tool_choice is supported")
	}
	var inputText string
	var input []object
	if json.Unmarshal(req.Input, &inputText) == nil {
		input = []object{{"type": "message", "role": "user", "content": inputText}}
	} else if json.Unmarshal(req.Input, &input) != nil || len(input) == 0 {
		return bad("input must contain text messages or tool history")
	}
	plan.History = canonicalHistory(input)
	history := []object{}
	for _, item := range input {
		kind, _ := item["type"].(string)
		switch kind {
		case "reasoning":
			continue
		case "", "message":
			role, _ := item["role"].(string)
			if role != "system" && role != "developer" && role != "user" && role != "assistant" {
				return bad("Unsupported input message role")
			}
			content, err := normalizeContent(item["content"], &plan.Images, false)
			if err != nil {
				return bad(err.Error())
			}
			item["content"] = content
		case "function_call", "custom_tool_call":
			if item["call_id"] == nil || item["name"] == nil {
				return bad("Tool history requires call_id and name")
			}
		case "function_call_output", "custom_tool_call_output":
			if item["call_id"] == nil {
				return bad("Tool output requires call_id")
			}
			content, err := normalizeContent(item["output"], &plan.Images, false)
			if err != nil {
				return bad(err.Error())
			}
			item["output"] = content
		default:
			return bad("Unsupported Responses input type: " + kind)
		}
		history = append(history, item)
	}
	seen := map[string]bool{}
	var add func([]json.RawMessage, string) error
	add = func(rawTools []json.RawMessage, namespace string) error {
		for _, rawTool := range rawTools {
			var tool struct {
				Type        string            `json:"type"`
				Name        string            `json:"name"`
				Description string            `json:"description"`
				Parameters  json.RawMessage   `json:"parameters"`
				Tools       []json.RawMessage `json:"tools"`
				Format      json.RawMessage   `json:"format"`
			}
			if json.Unmarshal(rawTool, &tool) != nil {
				return errors.New("Invalid tool definition")
			}
			if tool.Type == "namespace" {
				if namespace != "" || tool.Name == "" {
					return errors.New("Invalid tool namespace")
				}
				if err := add(tool.Tools, tool.Name); err != nil {
					return err
				}
				continue
			}
			if tool.Type != "function" && tool.Type != "custom" {
				return fmt.Errorf("Unsupported tool type %s; disable hosted tools such as web_search in Codex", tool.Type)
			}
			if tool.Name == "" || len(tool.Name) > 128 {
				return errors.New("Invalid tool name")
			}
			key := namespace + "." + tool.Name
			if seen[key] {
				return errors.New("Duplicate tool name")
			}
			seen[key] = true
			schema := tool.Parameters
			if tool.Type == "custom" {
				schema = json.RawMessage(`{"type":"object","properties":{"input":{"type":"string"}},"required":["input"],"additionalProperties":false}`)
				tool.Description += "\nMCP transport: put the complete raw tool text in the input string field. Inside that string, do not add a JSON wrapper or Markdown fences."
				var format struct {
					Type       string `json:"type"`
					Syntax     string `json:"syntax"`
					Definition string `json:"definition"`
				}
				_ = json.Unmarshal(tool.Format, &format)
				if format.Type == "grammar" {
					tool.Description += "\nInput grammar (" + format.Syntax + "):\n" + format.Definition
					if tool.Name == "apply_patch" && strings.Contains(format.Definition, "*** Begin Patch") {
						guide := "Codex apply_patch format. The input value is raw patch text. Start with *** Begin Patch and end with *** End Patch. For each changed file use *** Update File: path, then a bare @@ delimiter. Hunk coordinates/line numbers are not supported. Prefix every context line with a space, removed line with -, added line with +, including blank lines. Use *** Add File: path with + lines for new files. A multi-file patch uses multiple file sections inside one Begin/End pair. Example:\n*** Begin Patch\n*** Update File: example.py\n@@\n-old_value = 1\n+new_value = 2\n*** End Patch\n"
						tool.Description = guide + "\n" + tool.Description
						schema, _ = json.Marshal(object{"type": "object", "properties": object{"input": object{"type": "string", "description": guide}}, "required": []string{"input"}, "additionalProperties": false})
					}
				} else if len(tool.Format) > 0 {
					tool.Description += "\nTool input format: " + string(tool.Format)
				}
			} else if len(schema) == 0 || string(schema) == "null" {
				schema = json.RawMessage(`{"type":"object","properties":{}}`)
			}
			var schemaObject map[string]any
			if json.Unmarshal(schema, &schemaObject) != nil || schemaObject["type"] != "object" {
				return errors.New("Tool parameters must be an object schema")
			}
			name := fmt.Sprintf("client_tool_%d", len(plan.Tools))
			plan.Tools = append(plan.Tools, clientTool{Name: name, OriginalName: tool.Name, Namespace: namespace, Custom: tool.Type == "custom", Description: "Client-side tool " + strings.TrimPrefix(key, ".") + ". " + tool.Description, InputSchema: schema})
		}
		return nil
	}
	if err := add(req.Tools, ""); err != nil {
		return bad(err.Error())
	}
	if string(fields["tool_choice"]) == "\"none\"" {
		plan.Tools = nil
	}
	transcript, _ := json.Marshal(history)
	plan.Prompt = "You are the model for an external coding agent client. The conversation below is the full client history, including real tool results. Continue from its final item. Follow its system/developer instructions when consistent with your own instructions. All file and command operations belong to the CLIENT machine, not this server. Use ONLY the cpa_client_tools MCP tools provided for this session; do not use built-in file, shell, search, browser or other tools. Never inspect this server or its settings. When a tool is needed, call its matching MCP tool with the actual arguments; do not print a tool call as text. Prior function_call_output/custom_tool_call_output items are real results; do not repeat successful operations. Answer normally when the task is complete. Do not mention the transport wrapper.\nClient instructions:\n" + req.Instructions + "\nClient history (JSON):\n" + string(transcript)
	return plan, nil
}

type responseWriter struct {
	w         http.ResponseWriter
	stream    bool
	model, id string
	created   int64
	sequence  int
	output    []object
	text      strings.Builder
	messageID string
	started   bool
}

func (s *responseWriter) event(kind string, value object) error {
	if !s.stream {
		return nil
	}
	value["type"] = kind
	value["sequence_number"] = s.sequence
	s.sequence++
	raw, _ := json.Marshal(value)
	if _, err := fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", kind, raw); err != nil {
		return err
	}
	return http.NewResponseController(s.w).Flush()
}
func (s *responseWriter) snapshot(status string) object {
	return object{"id": s.id, "object": "response", "created_at": s.created, "status": status, "model": s.model, "output": s.output, "error": nil, "incomplete_details": nil, "usage": nil, "store": false}
}
func (s *responseWriter) start() error {
	s.started = true
	if s.stream {
		s.w.Header().Set("Content-Type", "text/event-stream")
		s.w.Header().Set("Cache-Control", "no-cache")
		s.w.Header().Set("X-Accel-Buffering", "no")
		if err := s.event("response.created", object{"response": s.snapshot("in_progress")}); err != nil {
			return err
		}
		return s.event("response.in_progress", object{"response": s.snapshot("in_progress")})
	}
	return nil
}
func (s *responseWriter) delta(text string) error {
	if text == "" {
		return nil
	}
	if s.text.Len()+len(text) > 8<<20 {
		return errors.New("ACP response exceeded size limit")
	}
	if s.messageID == "" {
		s.messageID = identifier("msg_")
		item := object{"id": s.messageID, "type": "message", "status": "in_progress", "role": "assistant", "content": []object{}}
		s.output = append(s.output, item)
		if err := s.event("response.output_item.added", object{"output_index": 0, "item": item}); err != nil {
			return err
		}
		if err := s.event("response.content_part.added", object{"item_id": s.messageID, "output_index": 0, "content_index": 0, "part": object{"type": "output_text", "text": "", "annotations": []any{}}}); err != nil {
			return err
		}
	}
	s.text.WriteString(text)
	return s.event("response.output_text.delta", object{"item_id": s.messageID, "output_index": 0, "content_index": 0, "delta": text})
}
func (s *responseWriter) finishText() error {
	if s.messageID == "" {
		return nil
	}
	text := s.text.String()
	part := object{"type": "output_text", "text": text, "annotations": []any{}}
	item := object{"id": s.messageID, "type": "message", "status": "completed", "role": "assistant", "content": []object{part}}
	s.output[0] = item
	if err := s.event("response.output_text.done", object{"item_id": s.messageID, "output_index": 0, "content_index": 0, "text": text}); err != nil {
		return err
	}
	if err := s.event("response.content_part.done", object{"item_id": s.messageID, "output_index": 0, "content_index": 0, "part": part}); err != nil {
		return err
	}
	return s.event("response.output_item.done", object{"output_index": 0, "item": item})
}
func (s *responseWriter) tool(tool clientTool, call clientInvocation) error {
	index := len(s.output)
	id := identifier("fc_")
	callID := call.CallID
	if callID == "" {
		callID = identifier("call_")
	}
	kind, key, event := "function_call", "arguments", "response.function_call_arguments"
	value := string(call.Arguments)
	if tool.Custom {
		var input struct {
			Input string `json:"input"`
		}
		if json.Unmarshal(call.Arguments, &input) != nil {
			return errors.New("Invalid custom tool input")
		}
		kind, key, event = "custom_tool_call", "input", "response.custom_tool_call_input"
		value = input.Input
	}
	item := object{"id": id, "type": kind, "call_id": callID, "name": tool.OriginalName, key: "", "status": "in_progress"}
	if tool.Namespace != "" {
		item["namespace"] = tool.Namespace
	}
	if err := s.event("response.output_item.added", object{"output_index": index, "item": item}); err != nil {
		return err
	}
	if err := s.event(event+".delta", object{"item_id": id, "output_index": index, "delta": value}); err != nil {
		return err
	}
	item[key] = value
	item["status"] = "completed"
	if err := s.event(event+".done", object{"item_id": id, "output_index": index, key: value}); err != nil {
		return err
	}
	s.output = append(s.output, item)
	return s.event("response.output_item.done", object{"output_index": index, "item": item})
}
func (s *responseWriter) fail(err error) {
	if !s.started || !s.stream {
		sendError(s.w, err)
		return
	}
	failure := s.snapshot("failed")
	failure["error"] = object{"code": "antigravity_acp_error", "message": err.Error()}
	_ = s.event("response.failed", object{"response": failure})
}
func (s *responseWriter) complete() error {
	response := s.snapshot("completed")
	if !s.stream {
		return sendJSON(s.w, 200, response)
	}
	return s.event("response.completed", object{"response": response})
}

// MCP aliases are generated by the adapter, never taken from client-provided names.
func matchClientTool(title string, tools []clientTool) *clientTool {
	for i := range tools {
		alias := tools[i].Name
		for _, candidate := range []string{alias, "cpa_client_tools_" + alias, "mcp__cpa_client_tools__" + alias, "mcp_cpa_client_tools_" + alias} {
			if title == candidate || title == "Run "+candidate {
				return &tools[i]
			}
		}
	}
	return nil
}
func responses(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, requestLimit))
	if err != nil {
		sendError(w, &httpError{413, "Request is too large"})
		return
	}
	traceRequestedModel(r.Context(), raw)
	plan, err := planResponse(raw)
	if err != nil {
		sendError(w, err)
		return
	}
	writer := &responseWriter{w: w, stream: plan.Request.Stream, model: plan.Request.Model, id: identifier("resp_"), created: time.Now().Unix(), output: []object{}}
	runAgentPlan(w, r, plan, writer)
}

// Both API formats share the ACP session and client-side tool handoff.
type agentWriter interface {
	start() error
	delta(string) error
	finishText() error
	tool(clientTool, clientInvocation) error
	fail(error)
	complete() error
	history() []object
}

func runAgentPlan(w http.ResponseWriter, r *http.Request, plan responsePlan, writer agentWriter) {
	if sessionReuse(r) {
		runReusablePlan(w, r, plan, writer)
		return
	}
	writer = &tracedAgentWriter{agentWriter: writer, ctx: r.Context()}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	a, err := startACP(ctx)
	if err != nil {
		sendError(w, err)
		return
	}
	defer a.close()
	if err = a.checkImages(plan.Images); err != nil {
		sendError(w, err)
		return
	}
	blocks, err := imagePrompt(ctx, plan.Prompt, plan.Images)
	if err != nil {
		sendError(w, err)
		return
	}
	servers := []any{}
	if len(plan.Tools) > 0 {
		toolsPath := filepath.Join(a.cwd, "client-tools.json")
		data, _ := json.Marshal(plan.Tools)
		if err = os.WriteFile(toolsPath, data, 0600); err != nil {
			sendError(w, err)
			return
		}
		executable, e := os.Executable()
		if e != nil {
			sendError(w, e)
			return
		}
		servers = append(servers, object{"name": "cpa_client_tools", "command": executable, "args": []string{"--client-mcp", toolsPath}, "env": []any{}})
	}
	session, err := a.newSessionWithTools(ctx, servers)
	if err != nil {
		sendError(w, err)
		return
	}
	if err = a.selectModel(ctx, session, strings.TrimPrefix(plan.Request.Model, "antigravity-acp/")); err != nil {
		sendError(w, err)
		return
	}
	if err = writer.start(); err != nil {
		return
	}
	var invocation *clientInvocation
	var selected *clientTool
	capture := func(raw json.RawMessage) error {
		var call struct {
			Title    string          `json:"title"`
			RawInput json.RawMessage `json:"rawInput"`
		}
		if json.Unmarshal(raw, &call) != nil {
			return errors.New("Invalid ACP tool update")
		}
		tool := matchClientTool(call.Title, plan.Tools)
		if tool == nil {
			return fmt.Errorf("ACP attempted an unsupported server tool: %.120s", call.Title)
		}
		if len(call.RawInput) == 0 || string(call.RawInput) == "null" {
			return errors.New("ACP client tool has no arguments")
		}
		var args map[string]any
		if json.Unmarshal(call.RawInput, &args) != nil || args == nil {
			return errors.New("Invalid MCP tool arguments")
		}
		// agy adds its own display summary to MCP input; it is not a client argument.
		var schema struct {
			Properties map[string]json.RawMessage `json:"properties"`
		}
		_ = json.Unmarshal(tool.InputSchema, &schema)
		if _, declared := schema.Properties["toolSummary"]; !declared {
			delete(args, "toolSummary")
		}
		arguments, marshalErr := json.Marshal(args)
		if marshalErr != nil {
			return marshalErr
		}
		selected = tool
		invocation = &clientInvocation{Name: tool.Name, Arguments: arguments}
		return errClientTool
	}
	a.permission = func(raw json.RawMessage) error {
		var p struct {
			ToolCall json.RawMessage `json:"toolCall"`
		}
		if json.Unmarshal(raw, &p) != nil {
			return errors.New("Invalid ACP permission request")
		}
		return capture(p.ToolCall)
	}
	updates := make(chan json.RawMessage, 16)
	done := make(chan struct {
		raw json.RawMessage
		err error
	}, 1)
	go func() {
		result, e := a.call(ctx, "session/prompt", object{"sessionId": a.session, "prompt": blocks}, func(raw json.RawMessage) error {
			var p struct {
				SessionID string          `json:"sessionId"`
				Update    json.RawMessage `json:"update"`
			}
			if json.Unmarshal(raw, &p) != nil || p.SessionID != a.session {
				return nil
			}
			var u struct {
				Kind  string `json:"sessionUpdate"`
				Title string `json:"title"`
			}
			if json.Unmarshal(p.Update, &u) != nil {
				return errors.New("Invalid ACP update")
			}
			if u.Kind == "tool_call" {
				return capture(p.Update)
			}
			select {
			case updates <- p.Update:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
		done <- struct {
			raw json.RawMessage
			err error
		}{result, e}
	}()
	handleUpdate := func(raw json.RawMessage) error {
		var u struct {
			Kind    string                      `json:"sessionUpdate"`
			Content struct{ Type, Text string } `json:"content"`
		}
		if json.Unmarshal(raw, &u) != nil {
			return errors.New("Invalid ACP update")
		}
		if u.Kind != "agent_message_chunk" {
			return nil
		}
		if u.Content.Type != "text" {
			return errors.New("Only text output is supported")
		}
		return writer.delta(u.Content.Text)
	}
	for {
		select {
		case <-r.Context().Done():
			cancel()
			<-done
			return
		case update := <-updates:
			if err = handleUpdate(update); err != nil {
				cancel()
				<-done
				writer.fail(err)
				return
			}
		case result := <-done:
			for len(updates) > 0 {
				if err = handleUpdate(<-updates); err != nil {
					writer.fail(err)
					return
				}
			}
			if result.err != nil && !errors.Is(result.err, errClientTool) {
				writer.fail(result.err)
				return
			}
			if err = writer.finishText(); err != nil {
				return
			}
			if invocation != nil && selected != nil {
				if err = writer.tool(*selected, *invocation); err != nil {
					writer.fail(err)
					return
				}
			} else {
				var stop struct {
					Reason string `json:"stopReason"`
				}
				if json.Unmarshal(result.raw, &stop) != nil || stop.Reason != "end_turn" {
					writer.fail(errors.New("ACP response did not finish normally"))
					return
				}
			}
			_ = writer.complete()
			return
		}
	}
}

func (s *responseWriter) history() []object { return canonicalHistory(s.output) }
