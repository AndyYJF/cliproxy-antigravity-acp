package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Cached processes are bounded; a miss always replays the supplied full history.
const sessionIdleTTL = 5 * time.Minute

var liveSessions = sessionPool{entries: map[string]*liveSession{}}

type sessionPool struct {
	mu                     sync.Mutex
	entries                map[string]*liveSession
	generation             uint64
	retired                []retiredSession
	hits, misses, bypassed uint64
	processHits            uint64
}
type liveEvent struct {
	text   string
	call   *pendingCall
	result json.RawMessage
	err    error
	done   bool
}
type pendingCall struct {
	tool       clientTool
	invocation clientInvocation
	result     chan object
}
type liveSession struct {
	id, scope, home string
	parts           []string
	life            sync.Mutex
	a               *acp
	processReused   bool
	ctx             context.Context
	cancel          context.CancelFunc
	server          *http.Server
	events          chan liveEvent
	toolReady       chan string
	pending         *pendingCall
	history         []string
	busy            bool
	used            time.Time
	timer           *time.Timer
}

func sessionReuse(r *http.Request) bool {
	enabled := env("ACP_SESSION_REUSE", "1") != "0" && r.Header.Get("X-ACP-Caller-Scope") != ""
	if !enabled {
		if record := requestTrace(r.Context()); record != nil {
			record.SessionCache = "bypass"
			record.SessionReason = "missing_caller_scope"
			if env("ACP_SESSION_REUSE", "1") == "0" {
				record.SessionReason = "reuse_disabled"
			}
			publishRequestTrace(record)
		}
		liveSessions.mu.Lock()
		liveSessions.bypassed++
		liveSessions.mu.Unlock()
	}
	return enabled
}
func digest(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func historyHashes(history []object) []string {
	result := make([]string, len(history))
	for i, item := range history {
		result[i] = digest(item)
	}
	return result
}
func historyPrefix(prefix, whole []string) bool {
	if len(prefix) == 0 || len(prefix) >= len(whole) {
		return false
	}
	for i, v := range prefix {
		if whole[i] != v {
			return false
		}
	}
	return true
}

// Canonicalize only representation differences; never discard conversation text,
// images, tool arguments, names, IDs or outputs when deciding whether to reuse.
func canonicalHistory(input []object) []object {
	raw, _ := json.Marshal(input)
	var copied []object
	_ = json.Unmarshal(raw, &copied)
	out := []object{}
	for _, item := range copied {
		kind, _ := item["type"].(string)
		if kind == "reasoning" {
			continue
		}
		next := object{}
		switch kind {
		case "", "message":
			role, _ := item["role"].(string)
			content := canonicalContent(item["content"])
			if role == "assistant" && (content == nil || content == "") {
				continue
			}
			next = object{"type": "message", "role": role, "content": content}
		case "function_call", "custom_tool_call":
			next = object{"type": kind, "call_id": item["call_id"], "name": item["name"]}
			if ns, ok := item["namespace"].(string); ok && ns != "" {
				next["namespace"] = ns
			}
			if kind == "function_call" {
				value := item["arguments"]
				if text, ok := value.(string); ok {
					var args any
					if json.Unmarshal([]byte(text), &args) == nil {
						value = args
					}
				}
				next["arguments"] = value
			} else {
				next["input"] = item["input"]
			}
		case "function_call_output", "custom_tool_call_output":
			next = object{"type": kind, "call_id": item["call_id"], "output": canonicalContent(item["output"])}
		default:
			next = item
		}
		out = append(out, next)
	}
	return out
}
func canonicalContent(value any) any {
	parts, ok := value.([]any)
	if !ok {
		return value
	}
	out := []any{}
	var text strings.Builder
	onlyText := true
	for _, v := range parts {
		p, ok := v.(map[string]any)
		if !ok {
			return value
		}
		switch p["type"] {
		case "text", "input_text", "output_text":
			s, ok := p["text"].(string)
			if !ok {
				return value
			}
			text.WriteString(s)
			out = append(out, object{"type": "input_text", "text": s})
		case "image_url", "input_image":
			onlyText = false
			u := p["image_url"]
			if nested, ok := u.(map[string]any); ok {
				u = nested["url"]
			}
			out = append(out, object{"type": "input_image", "image_url": u})
		default:
			return value
		}
	}
	if onlyText {
		return text.String()
	}
	return out
}
func (p *sessionPool) checkout(scope, home string, history []object, diagnostics ...sessionDiagnostic) (*liveSession, bool, error) {
	hashes := historyHashes(history)
	d := sessionDiagnostic{}
	if len(diagnostics) > 0 {
		d = diagnostics[0]
	}
	p.mu.Lock()
	scope = fmt.Sprintf("%s/%d", scope, p.generation)
	for id, entry := range p.entries {
		if entry.busy {
			continue
		}
		dead := entry.ctx.Err() != nil
		if entry.a != nil {
			select {
			case <-entry.a.exited:
				dead = true
			default:
			}
		}
		if dead {
			p.rememberRetired(entry, "process_exited")
			delete(p.entries, id)
			if entry.timer != nil {
				entry.timer.Stop()
			}
			go entry.close()
		}
	}
	var chosen *liveSession
	for _, s := range p.entries {
		if s.busy {
			continue
		}
		if s.scope != scope || !historyPrefix(s.history, hashes) {
			continue
		}
		if s.pending != nil {
			tail := history[len(s.history):]
			if len(tail) != 1 || (tail[0]["type"] != "function_call_output" && tail[0]["type"] != "custom_tool_call_output") || tail[0]["call_id"] != s.historyCallID() {
				continue
			}
		} else {
			valid := true
			for _, item := range history[len(s.history):] {
				if item["type"] != "message" || item["role"] != "user" {
					valid = false
				}
			}
			if !valid {
				continue
			}
		}
		if chosen == nil || len(s.history) > len(chosen.history) {
			chosen = s
		}
	}
	if chosen != nil {
		p.hits++
		chosen.busy = true
		if chosen.timer != nil {
			chosen.timer.Stop()
		}
		p.mu.Unlock()
		return chosen, true, nil
	}
	p.misses++
	if d.record != nil {
		d.record.SessionCache = "miss"
		d.record.SessionReason, d.record.SessionDifferences = p.explainMiss(scope, hashes, d)
		publishRequestTrace(d.record)
	}
	// A completed conversation may donate its process, but never a pending tool RPC.
	// Keep the same caller/account/model/tool scope and require advertised close support.
	var donor *liveSession
	for _, candidate := range p.entries {
		if !candidate.busy && candidate.pending == nil && candidate.scope == scope && candidate.home == home && candidate.a != nil && candidate.a.closeCapable && (donor == nil || candidate.used.Before(donor.used)) {
			donor = candidate
		}
	}
	var process *acp
	if donor != nil {
		p.rememberRetired(donor, "process_reassigned")
		delete(p.entries, donor.id)
		if donor.timer != nil {
			donor.timer.Stop()
		}
		donor.life.Lock()
		process, donor.a = donor.a, nil
		donor.life.Unlock()
	}
	var evicted *liveSession
	if len(p.entries) >= admission.limit {
		for _, s := range p.entries {
			if !s.busy && (evicted == nil || s.used.Before(evicted.used)) {
				evicted = s
			}
		}
		if evicted == nil {
			p.mu.Unlock()
			if d.record != nil {
				d.record.SessionReason = "session_capacity"
				publishRequestTrace(d.record)
			}
			return nil, false, &httpError{429, "ACP session capacity reached"}
		}
		p.rememberRetired(evicted, "capacity_evicted")
		delete(p.entries, evicted.id)
		if evicted.timer != nil {
			evicted.timer.Stop()
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &liveSession{id: identifier("acps_"), scope: scope, home: home, a: process, parts: d.parts, ctx: ctx, cancel: cancel, events: make(chan liveEvent, 16), toolReady: make(chan string, 16), busy: true}
	p.entries[s.id] = s
	p.mu.Unlock()
	if donor != nil {
		donor.close()
	}
	if evicted != nil {
		evicted.close()
	}
	return s, false, nil
}

// callID belongs to the returned API call, not the upstream MCP alias.
func (s *liveSession) historyCallID() any { return s.pending.invocation.CallID }
func (s *liveSession) close() {
	s.cancel()
	s.life.Lock()
	server, a := s.server, s.a
	s.life.Unlock()
	if server != nil {
		_ = server.Close()
	}
	if a != nil {
		a.close()
	}
}
func (p *sessionPool) finish(s *liveSession, keep bool) {
	p.mu.Lock()
	if !keep || p.entries[s.id] != s {
		p.rememberRetired(s, "previous_request_ended")
		delete(p.entries, s.id)
		p.mu.Unlock()
		s.close()
		return
	}
	s.busy = false
	s.used = time.Now()
	used := s.used
	s.timer = time.AfterFunc(sessionIdleTTL, func() {
		p.mu.Lock()
		if p.entries[s.id] != s || s.busy || !s.used.Equal(used) {
			p.mu.Unlock()
			return
		}
		p.rememberRetired(s, "idle_expired")
		delete(p.entries, s.id)
		p.mu.Unlock()
		s.close()
	})
	p.mu.Unlock()
}
func (p *sessionPool) clearIdle() {
	p.mu.Lock()
	p.generation++
	var removed []*liveSession
	for id, s := range p.entries {
		if !s.busy {
			p.rememberRetired(s, "account_switched")
			delete(p.entries, id)
			if s.timer != nil {
				s.timer.Stop()
			}
			removed = append(removed, s)
		}
	}
	p.mu.Unlock()
	for _, s := range removed {
		s.close()
	}
}
func (p *sessionPool) closeAll() {
	p.mu.Lock()
	var removed []*liveSession
	for id, s := range p.entries {
		delete(p.entries, id)
		if s.timer != nil {
			s.timer.Stop()
		}
		removed = append(removed, s)
	}
	p.mu.Unlock()
	for _, s := range removed {
		s.close()
	}
}
func (s *liveSession) emit(e liveEvent) error {
	select {
	case s.events <- e:
		return nil
	case <-s.ctx.Done():
		return s.ctx.Err()
	}
}
func (s *liveSession) start(ctx context.Context, plan responsePlan) error {
	s.life.Lock()
	a := s.a
	s.life.Unlock()
	if a != nil {
		closeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		_, err := a.call(closeCtx, "session/close", object{"sessionId": a.session}, nil)
		cancel()
		if err != nil {
			a.close()
			a = nil
			if ctx.Err() != nil {
				return ctx.Err()
			}
		} else {
			if a.sessionCWD != "" && a.sessionCWD != a.cwd {
				_ = os.RemoveAll(a.sessionCWD)
			}
			a.session, a.sessionCWD = "", ""
			a.permission, a.permissionResult = nil, nil
			s.processReused = true
		}
	}
	var err error
	if a == nil {
		a, err = startACPHome(ctx, s.home)
		if err != nil {
			return err
		}
	}
	s.life.Lock()
	s.a = a
	s.life.Unlock()
	if s.ctx.Err() != nil {
		return s.ctx.Err()
	}
	if err = a.checkImages(plan.Images); err != nil {
		return err
	}
	cwd, err := os.MkdirTemp(a.cwd, "session-")
	if err != nil {
		return err
	}
	servers := []any{}
	if len(plan.Tools) > 0 {
		socket := filepath.Join(cwd, "tools.sock")
		listener, err := net.Listen("unix", socket)
		if err != nil {
			return err
		}
		if err = os.Chmod(socket, 0600); err != nil {
			listener.Close()
			return err
		}
		server := &http.Server{ReadHeaderTimeout: 10 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != "POST" || r.URL.Path != "/invoke" {
				http.NotFound(w, r)
				return
			}
			var call clientInvocation
			if json.NewDecoder(http.MaxBytesReader(w, r.Body, requestLimit)).Decode(&call) != nil {
				sendError(w, &httpError{400, "Invalid MCP invocation"})
				return
			}
			tool := matchClientTool(call.Name, plan.Tools)
			if tool == nil {
				sendError(w, errors.New("Unknown client MCP tool"))
				return
			}
			var args map[string]any
			if json.Unmarshal(call.Arguments, &args) != nil || args == nil {
				sendError(w, errors.New("Invalid MCP arguments"))
				return
			}
			var schema struct{ Properties map[string]json.RawMessage }
			_ = json.Unmarshal(tool.InputSchema, &schema)
			if _, declared := schema.Properties["toolSummary"]; !declared {
				delete(args, "toolSummary")
			}
			call.Arguments, _ = json.Marshal(args)
			call.CallID = identifier("call_")
			select {
			case alias := <-s.toolReady:
				if alias != tool.Name {
					s.cancel()
					sendError(w, errors.New("MCP event mismatch"))
					return
				}
			case <-s.ctx.Done():
				return
			case <-r.Context().Done():
				s.cancel()
				return
			}
			pending := &pendingCall{tool: *tool, invocation: call, result: make(chan object, 1)}
			if s.emit(liveEvent{call: pending}) != nil {
				return
			}
			select {
			case result := <-pending.result:
				sendJSON(w, 200, result)
			case <-s.ctx.Done():
				sendError(w, s.ctx.Err())
			case <-r.Context().Done():
				s.cancel()
			}
		})}
		s.life.Lock()
		s.server = server
		s.life.Unlock()
		if s.ctx.Err() != nil {
			listener.Close()
			return s.ctx.Err()
		}
		go func() { _ = server.Serve(listener) }()
		toolsPath := filepath.Join(cwd, "client-tools.json")
		data, _ := json.Marshal(plan.Tools)
		if err = os.WriteFile(toolsPath, data, 0600); err != nil {
			return err
		}
		executable, err := os.Executable()
		if err != nil {
			return err
		}
		servers = append(servers, object{"name": "cpa_client_tools", "command": executable, "args": []string{"--client-mcp", toolsPath, socket}, "env": []any{}})
		a.permissionResult = func(raw json.RawMessage) (any, error) {
			var p struct {
				ToolCall struct{ Title string }
				Options  []struct {
					OptionID string
					Kind     string
				}
			}
			if json.Unmarshal(raw, &p) != nil || matchClientTool(p.ToolCall.Title, plan.Tools) == nil {
				return nil, errors.New("Unsupported server tool permission")
			}
			for _, option := range p.Options {
				if option.Kind == "allow_once" {
					return object{"outcome": object{"outcome": "selected", "optionId": option.OptionID}}, nil
				}
			}
			return nil, errors.New("Client MCP permission has no allow_once option")
		}
	}
	a.web = newNativeWeb(plan.NativeWeb)
	session, err := a.newSessionIn(ctx, cwd, servers)
	if err != nil {
		return err
	}
	return a.selectModel(ctx, session, strings.TrimPrefix(plan.Request.Model, "antigravity-acp/"))
}
func (s *liveSession) prompt(blocks []object, tools []clientTool) {
	go func() {
		result, err := s.a.call(s.ctx, "session/prompt", object{"sessionId": s.a.session, "prompt": blocks}, func(raw json.RawMessage) error {
			var p struct {
				SessionID string
				Update    struct {
					SessionUpdate string
					Title         string
					Content       struct{ Type, Text string }
				}
			}
			if json.Unmarshal(raw, &p) != nil || p.SessionID != s.a.session {
				return nil
			}
			var webUpdate struct {
				Update json.RawMessage `json:"update"`
			}
			_ = json.Unmarshal(raw, &webUpdate)
			if handled, err := s.a.web.update(webUpdate.Update); handled {
				return err
			}
			switch p.Update.SessionUpdate {
			case "tool_call":
				tool := matchClientTool(p.Update.Title, tools)
				if tool == nil {
					return errors.New("ACP attempted an unsupported server tool")
				}
				select {
				case s.toolReady <- tool.Name:
				case <-s.ctx.Done():
					return s.ctx.Err()
				}
			case "agent_message_chunk":
				if p.Update.Content.Type != "text" {
					return errors.New("Only text output is supported")
				}
				return s.emit(liveEvent{text: p.Update.Content.Text})
			}
			return nil
		})
		_ = s.emit(liveEvent{done: true, result: result, err: err})
	}()
}
func runReusablePlan(w http.ResponseWriter, r *http.Request, plan responsePlan, writer agentWriter) {
	writer = &tracedAgentWriter{agentWriter: writer, ctx: r.Context()}
	home := env("HOME", "/home/acp")
	if accounts != nil {
		home = accounts.selectedHome()
	}
	// Tool names and namespaces are part of scope even though MCP wire JSON omits them.
	names := []string{}
	for _, tool := range plan.Tools {
		names = append(names, tool.Namespace+"."+tool.OriginalName)
	}
	scopeValues := []any{r.Header.Get("X-ACP-Caller-Scope"), r.URL.Path, home, plan.Request.Model, plan.Request.Instructions, plan.Tools, names, object{"client": plan.Policy, "native_web": plan.NativeWeb}}
	scope := digest(scopeValues)
	parts := make([]string, len(scopeValues))
	for i, value := range scopeValues {
		parts[i] = digest(value)
	}
	systemHistory := []object{}
	for _, item := range plan.History {
		if item["role"] == "system" || item["role"] == "developer" {
			systemHistory = append(systemHistory, item)
		}
	}
	parts[4] = digest([]any{plan.Request.Instructions, systemHistory})
	s, reused, err := liveSessions.checkout(scope, home, plan.History, sessionDiagnostic{parts, requestTrace(r.Context())})
	if err != nil {
		sendError(w, err)
		return
	}
	traceAccount(r.Context(), s.home)
	if record := requestTrace(r.Context()); record != nil {
		record.SessionID = s.id
		if reused {
			record.SessionCache = "hit"
			record.SessionReason = "history_prefix_match"
			traceModel(r.Context(), plan.Request.Model)
		}
		publishRequestTrace(record)
	}
	keep := false
	defer func() { liveSessions.finish(s, keep) }()
	// Only an active HTTP owner can cancel an in-flight turn; successful handoff
	// deliberately leaves the MCP request pending until the next HTTP request.
	stopCancel := context.AfterFunc(r.Context(), s.cancel)
	defer stopCancel()
	setupStart := time.Now()
	if !reused {
		if err = s.start(r.Context(), plan); err != nil {
			sendError(w, err)
			return
		}
	}
	defer observeNativeWeb(r.Context(), s.a.web)()
	if err = s.a.checkImages(plan.Images); err != nil {
		sendError(w, err)
		return
	}
	if reused && s.pending != nil {
		item := plan.History[len(s.history)]
		var images []inputImage
		value, err := normalizeContent(item["output"], &images, false)
		if err != nil {
			sendError(w, err)
			return
		}
		text, _ := value.(string)
		if text == "" {
			b, _ := json.Marshal(value)
			text = string(b)
		}
		blocks, err := imagePrompt(r.Context(), text, images)
		if err != nil {
			sendError(w, err)
			return
		}
		s.pending.result <- object{"content": blocks}
		s.pending = nil
	} else {
		prompt, images := plan.Prompt, plan.Images
		if reused {
			tail := plan.History[len(s.history):]
			images = nil
			prepared := []object{}
			for _, item := range tail {
				content, e := normalizeContent(item["content"], &images, false)
				if e != nil {
					sendError(w, e)
					return
				}
				prepared = append(prepared, object{"role": item["role"], "content": content})
			}
			raw, _ := json.Marshal(prepared)
			prompt = "Continue the existing client conversation. Only these new user messages follow; prior instructions and tool boundaries still apply:\n" + string(raw)
		}
		blocks, err := imagePrompt(r.Context(), prompt, images)
		if err != nil {
			sendError(w, err)
			return
		}
		s.prompt(blocks, plan.Tools)
	}
	state := "miss"
	if reused {
		state = "hit"
	}
	processState := "cold"
	if reused {
		processState = "session"
	} else if s.processReused {
		processState = "reused"
		liveSessions.mu.Lock()
		liveSessions.processHits++
		liveSessions.mu.Unlock()
	}
	setupMS := float64(time.Since(setupStart).Microseconds()) / 1000
	if record := requestTrace(r.Context()); record != nil {
		record.ProcessCache, record.SetupMS = processState, setupMS
		publishRequestTrace(record)
	}
	w.Header().Set("X-ACP-Process-Cache", processState)
	w.Header().Set("X-ACP-Session-Cache", state)
	w.Header().Set("X-ACP-Session-Id", s.id)
	w.Header().Set("Server-Timing", fmt.Sprintf("acp_setup;dur=%.2f", setupMS))
	if err = writer.start(); err != nil {
		return
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case e := <-s.events:
			if e.err != nil {
				writer.fail(e.err)
				return
			}
			if e.text != "" {
				if err = writer.delta(e.text); err != nil {
					writer.fail(err)
					return
				}
			}
			if e.call != nil || e.done {
				if err = writer.finishText(); err != nil {
					return
				}
				if e.call != nil {
					if err = writer.tool(e.call.tool, e.call.invocation); err != nil {
						writer.fail(err)
						return
					}
					s.pending = e.call
				} else {
					var stop struct{ StopReason string }
					if json.Unmarshal(e.result, &stop) != nil || stop.StopReason != "end_turn" {
						writer.fail(errors.New("ACP response did not finish normally"))
						return
					}
				}
				if err = writer.complete(); err != nil {
					return
				}
				s.history = append(historyHashes(plan.History), historyHashes(writer.history())...)
				// Stop the request cancellation callback before releasing the persistent owner.
				if !stopCancel() || r.Context().Err() != nil {
					return
				}
				keep = true
				return
			}
		}
	}
}

// A private Unix socket carries MCP tool results; no network endpoint is exposed.
func relayClientTool(socket string, call clientInvocation) (object, error) {
	raw, _ := json.Marshal(call)
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: sessionIdleTTL + time.Minute}
	resp, err := client.Post("http://localhost/invoke", "application/json", bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, errors.New("Client tool relay closed")
	}
	var result object
	err = json.NewDecoder(resp.Body).Decode(&result)
	return result, err
}

func (p *sessionPool) stats() object {
	p.mu.Lock()
	defer p.mu.Unlock()
	idle := 0
	for _, s := range p.entries {
		if !s.busy {
			idle++
		}
	}
	return object{"sessions": len(p.entries), "idle": idle, "hits": p.hits, "misses": p.misses, "bypassed": p.bypassed, "process_hits": p.processHits}
}
