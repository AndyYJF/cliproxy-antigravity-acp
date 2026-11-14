package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

type object = map[string]any
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return e.Message }

type rpcMessage struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}
type acp struct {
	closeCapable     bool
	sessionCWD       string
	imageCapable     bool
	cmd              *exec.Cmd
	in               io.WriteCloser
	messages         chan rpcMessage
	exited           chan struct{}
	done             chan struct{}
	writeMu          sync.Mutex
	once             sync.Once
	next             int
	session          string
	cwd              string
	waitErr          error
	permission       func(json.RawMessage) error
	permissionResult func(json.RawMessage) (any, error)
}

func startACP(ctx context.Context) (*acp, error) {
	home := env("HOME", "/home/acp")
	if accounts != nil {
		home = accounts.selectedHome()
	}
	return startACPHome(ctx, home)
}

func startACPHome(ctx context.Context, home string) (*acp, error) {
	traceAccount(ctx, home)
	cwd, err := os.MkdirTemp(env("WORK_ROOT", "/work"), "chat-")
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(env("AGY_BINARY", "/usr/local/bin/agy"), "acp", "--include-default-customizations=false")
	cmd.Dir = cwd
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Env = accountEnvironment(home)
	cmd.Stderr = io.Discard
	in, err := cmd.StdinPipe()
	if err != nil {
		os.RemoveAll(cwd)
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		in.Close()
		os.RemoveAll(cwd)
		return nil, err
	}
	a := &acp{cmd: cmd, in: in, messages: make(chan rpcMessage, 16), exited: make(chan struct{}), done: make(chan struct{}), cwd: cwd}
	if err = cmd.Start(); err != nil {
		in.Close()
		out.Close()
		os.RemoveAll(cwd)
		return nil, fmt.Errorf("start official agy: %w", err)
	}
	go func() {
		defer close(a.messages)
		defer func() { a.waitErr = cmd.Wait(); close(a.exited) }()
		scan := bufio.NewScanner(out)
		scan.Buffer(make([]byte, 65536), 8<<20)
		for scan.Scan() {
			var msg rpcMessage
			if json.Unmarshal(scan.Bytes(), &msg) != nil {
				continue
			}
			select {
			case a.messages <- msg:
			case <-a.done:
				return
			}
		}
		if scan.Err() != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
	}()

	initialized, err := a.call(ctx, "initialize", object{"protocolVersion": 1, "clientCapabilities": object{}, "clientInfo": object{"name": "cliproxy-antigravity-acp", "version": "0.11.1"}}, nil)
	if err != nil {
		a.close()
		return nil, err
	}
	var capabilities struct {
		AgentCapabilities struct {
			SessionCapabilities struct {
				Close json.RawMessage `json:"close"`
			} `json:"sessionCapabilities"`
			PromptCapabilities struct {
				Image bool `json:"image"`
			} `json:"promptCapabilities"`
		} `json:"agentCapabilities"`
	}
	_ = json.Unmarshal(initialized, &capabilities)
	a.imageCapable = capabilities.AgentCapabilities.PromptCapabilities.Image
	a.closeCapable = len(capabilities.AgentCapabilities.SessionCapabilities.Close) > 0 && string(capabilities.AgentCapabilities.SessionCapabilities.Close) != "null"
	return a, nil
}
func (a *acp) write(v any) error {
	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	return json.NewEncoder(a.in).Encode(v)
}
func (a *acp) close() {
	a.once.Do(func() {
		close(a.done)
		_ = a.in.Close()
		select {
		case <-a.exited:
		default:
			_ = syscall.Kill(-a.cmd.Process.Pid, syscall.SIGKILL)
			<-a.exited
		}
		_ = os.RemoveAll(a.cwd)
	})
}
func (a *acp) cancel() {
	if a.session != "" {
		_ = a.write(object{"jsonrpc": "2.0", "method": "session/cancel", "params": object{"sessionId": a.session}})
	}
	select {
	case <-a.exited:
	case <-time.After(500 * time.Millisecond):
	}
	a.close()
}
func (a *acp) call(ctx context.Context, method string, params any, update func(json.RawMessage) error) (json.RawMessage, error) {
	a.next++
	id := a.next
	if err := a.write(object{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
		return nil, err
	}
	for {
		select {
		case <-ctx.Done():
			a.cancel()
			return nil, ctx.Err()
		case msg, ok := <-a.messages:
			if !ok {
				return nil, errors.New("Official agy exited before completing the request")
			}
			if msg.Method != "" {
				if len(msg.ID) > 0 && string(msg.ID) != "null" {
					if msg.Method == "session/request_permission" {
						var permissionSession struct {
							SessionID string `json:"sessionId"`
						}
						_ = json.Unmarshal(msg.Params, &permissionSession)
						if permissionSession.SessionID != "" && permissionSession.SessionID != a.session {
							_ = a.write(object{"jsonrpc": "2.0", "id": msg.ID, "result": object{"outcome": object{"outcome": "cancelled"}}})
							continue
						}
						if a.permissionResult != nil {
							result, err := a.permissionResult(msg.Params)
							if err != nil {
								return nil, err
							}
							if err = a.write(object{"jsonrpc": "2.0", "id": msg.ID, "result": result}); err != nil {
								return nil, err
							}
							continue
						}
						if a.permission != nil {
							if err := a.permission(msg.Params); err != nil {
								return nil, err
							}
						}
						_ = a.write(object{"jsonrpc": "2.0", "id": msg.ID, "result": object{"outcome": object{"outcome": "cancelled"}}})
						return nil, errors.New("The agent requested a tool permission; chat-only mode cannot approve tools")
					}
					_ = a.write(object{"jsonrpc": "2.0", "id": msg.ID, "error": object{"code": -32601, "message": "This chat client does not provide tools or interactive forms"}})
					return nil, errors.New("The agent requested an unsupported client operation")
				}
				if msg.Method == "session/update" && update != nil {
					if err := update(msg.Params); err != nil {
						return nil, err
					}
				}
				continue
			}
			var got int
			_ = json.Unmarshal(msg.ID, &got)
			if got != id {
				continue
			}
			if msg.Error != nil {
				return nil, msg.Error
			}
			return msg.Result, nil
		}
	}
}

type optionValue struct {
	Value   string        `json:"value"`
	Name    string        `json:"name"`
	Options []optionValue `json:"options"`
}
type configOption struct {
	ID       string        `json:"id"`
	Category string        `json:"category"`
	Options  []optionValue `json:"options"`
}
type sessionInfo struct {
	SessionID string `json:"sessionId"`
	Meta      struct {
		Antigravity struct {
			Tier accountTier `json:"tier"`
		} `json:"antigravity"`
	} `json:"_meta"`
	ConfigOptions []configOption `json:"configOptions"`
	Models        struct {
		AvailableModels []struct {
			ModelID string `json:"modelId"`
			Name    string `json:"name"`
		} `json:"availableModels"`
	} `json:"models"`
}

func (a *acp) newSession(ctx context.Context) (sessionInfo, error) {
	return a.newSessionWithTools(ctx, []any{})
}
func (a *acp) newSessionWithTools(ctx context.Context, servers []any) (sessionInfo, error) {
	return a.newSessionIn(ctx, a.cwd, servers)
}
func (a *acp) newSessionIn(ctx context.Context, cwd string, servers []any) (sessionInfo, error) {
	var s sessionInfo
	raw, err := a.call(ctx, "session/new", object{"cwd": cwd, "mcpServers": servers}, nil)
	if err != nil {
		return s, err
	}
	err = json.Unmarshal(raw, &s)
	a.session = s.SessionID
	a.sessionCWD = cwd
	if err == nil && a.session == "" {
		err = errors.New("ACP returned no session ID")
	}
	return s, err
}
func (s sessionInfo) models() (string, []string) {
	var result []string
	key := ""
	var add func([]optionValue)
	add = func(values []optionValue) {
		for _, v := range values {
			if strings.HasPrefix(strings.ToLower(v.Value), "gemini") {
				result = append(result, v.Value)
			}
			add(v.Options)
		}
	}
	for _, o := range s.ConfigOptions {
		if o.Category == "model" || o.ID == "model" {
			key = o.ID
			add(o.Options)
			break
		}
	}
	if key == "" {
		for _, m := range s.Models.AvailableModels {
			if strings.HasPrefix(strings.ToLower(m.ModelID), "gemini") {
				result = append(result, m.ModelID)
			}
		}
	}
	return key, result
}
func (a *acp) selectModel(ctx context.Context, s sessionInfo, model string) error {
	key, models := s.models()
	found := false
	for _, m := range models {
		if m == model {
			found = true
			break
		}
	}
	if !found {
		return &httpError{400, "Requested Gemini model is not available to this ACP client"}
	}
	method := "session/set_config_option"
	params := object{"sessionId": a.session, "configId": key, "value": model}
	if key == "" {
		method = "session/set_model"
		params = object{"sessionId": a.session, "modelId": model}
	}
	_, err := a.call(ctx, method, params, nil)
	if err == nil {
		traceModel(ctx, model)
	}
	return err
}

type httpError struct {
	status  int
	message string
}

func (e *httpError) Error() string { return e.message }

type chatMessage struct {
	Role      string          `json:"role"`
	Content   json.RawMessage `json:"content"`
	ToolCalls json.RawMessage `json:"tool_calls,omitempty"`
}
type chatRequest struct {
	Images   []inputImage  `json:"-"`
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
	Stream   bool          `json:"stream"`
}

func promptBody(raw []byte) (chatRequest, string, error) {
	var req chatRequest
	var fields map[string]json.RawMessage
	bad := func(s string) (chatRequest, string, error) { return req, "", &httpError{400, s} }
	if json.Unmarshal(raw, &req) != nil || json.Unmarshal(raw, &fields) != nil {
		return bad("Invalid Chat Completions JSON")
	}
	if !strings.HasPrefix(req.Model, "antigravity-acp/gemini") {
		return bad("Select an antigravity-acp/ Gemini model")
	}
	for _, k := range []string{"tools", "functions", "tool_choice", "function_call", "modalities", "audio"} {
		if v := fields[k]; len(v) > 0 && string(v) != "null" && !(k == "tools" && string(v) == "[]") {
			return bad(k + " is not supported by the ACP chat adapter")
		}
	}
	if len(req.Messages) == 0 {
		return bad("messages must not be empty")
	}
	history := []object{}
	for _, m := range req.Messages {
		if m.Role != "system" && m.Role != "developer" && m.Role != "user" && m.Role != "assistant" {
			return bad("Only system, developer, user and assistant text messages are supported")
		}
		if len(m.ToolCalls) > 0 && string(m.ToolCalls) != "null" && string(m.ToolCalls) != "[]" {
			return bad("Tool history is not supported")
		}
		var content any
		if json.Unmarshal(m.Content, &content) != nil {
			return bad("Invalid message content")
		}
		content, err := normalizeContent(content, &req.Images, true)
		if err != nil {
			return bad(err.Error())
		}
		history = append(history, object{"role": m.Role, "content": content})
	}
	if req.Messages[len(req.Messages)-1].Role != "user" {
		return bad("The last message must be a user message")
	}
	transcript, _ := json.Marshal(history)
	prompt := "You are serving a chat with optional attached images. Answer the final user message using the conversation below as context. Follow the supplied system and developer instructions when consistent with your own instructions. Do not use tools, browse, read or modify files, or execute commands. Do not describe the JSON wrapper. The conversation is encoded as a JSON array:\n" + string(transcript)
	return req, prompt, nil
}
func env(k, fallback string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return fallback
}
func sendJSON(w http.ResponseWriter, status int, v any) error {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	return json.NewEncoder(w).Encode(v)
}
func sendError(w http.ResponseWriter, err error) {
	status := 502
	var h *httpError
	if errors.As(err, &h) {
		status = h.status
	}
	var r *rpcError
	if errors.As(err, &r) && r.Code == -32000 {
		status = 503
	}
	sendJSON(w, status, object{"error": object{"message": err.Error(), "type": "antigravity_acp_error"}})
}

func modelList(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	a, err := startACP(ctx)
	if err != nil {
		sendError(w, err)
		return
	}
	defer a.close()
	s, err := a.newSession(ctx)
	if err != nil {
		sendError(w, err)
		return
	}
	_, models := s.models()
	data := []object{}
	for _, id := range models {
		data = append(data, object{"id": "antigravity-acp/" + id, "object": "model", "owned_by": "antigravity-acp"})
	}
	sendJSON(w, 200, object{"object": "list", "data": data})
}
func chat(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, requestLimit))
	if err != nil {
		sendError(w, &httpError{413, "Request is too large"})
		return
	}
	traceRequestedModel(r.Context(), raw)
	if isAgentChat(raw) {
		chatAgent(w, r, raw)
		return
	}
	req, prompt, err := promptBody(raw)
	if err != nil {
		sendError(w, err)
		return
	}
	if sessionReuse(r) {
		plan, e := planChatAgent(raw)
		if e != nil {
			sendError(w, e)
			return
		}
		plan.Prompt = prompt
		plan.Policy = "chat"
		writer := &chatAgentWriter{w: w, stream: req.Stream, model: req.Model, id: identifier("chatcmpl-acp-"), created: time.Now().Unix()}
		runReusablePlan(w, r, plan, writer)
		return
	}
	ctx := r.Context()
	a, err := startACP(ctx)
	if err != nil {
		sendError(w, err)
		return
	}
	defer a.close()
	if err = a.checkImages(req.Images); err != nil {
		sendError(w, err)
		return
	}
	blocks, err := imagePrompt(ctx, prompt, req.Images)
	if err != nil {
		sendError(w, err)
		return
	}
	s, err := a.newSession(ctx)
	if err != nil {
		sendError(w, err)
		return
	}
	if err = a.selectModel(ctx, s, strings.TrimPrefix(req.Model, "antigravity-acp/")); err != nil {
		sendError(w, err)
		return
	}
	nonce := make([]byte, 16)
	if _, err = rand.Read(nonce); err != nil {
		sendError(w, err)
		return
	}
	id := "chatcmpl-acp-" + hex.EncodeToString(nonce)
	created := time.Now().Unix()
	var answer strings.Builder
	started := false
	emit := func(delta object, finish any) error {
		v := object{"id": id, "object": "chat.completion.chunk", "created": created, "model": req.Model, "choices": []object{{"index": 0, "delta": delta, "finish_reason": finish}}}
		b, _ := json.Marshal(v)
		if _, err := fmt.Fprintf(w, "data: %s\n\n", b); err != nil {
			return err
		}
		return http.NewResponseController(w).Flush()
	}
	if req.Stream {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Accel-Buffering", "no")
		started = true
		if err = emit(object{"role": "assistant"}, nil); err != nil {
			return
		}
	}
	update := func(raw json.RawMessage) error {
		var p struct {
			SessionID string `json:"sessionId"`
			Update    struct {
				Kind    string `json:"sessionUpdate"`
				Content struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"update"`
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			return err
		}
		if p.SessionID != a.session {
			return nil
		}
		if p.Update.Kind == "tool_call" {
			return errors.New("Agent attempted a tool call; tools are disabled for this chat provider")
		}
		if p.Update.Kind != "agent_message_chunk" {
			return nil
		}
		if p.Update.Content.Type != "text" {
			return errors.New("Agent returned unsupported non-text content")
		}
		if answer.Len()+len(p.Update.Content.Text) > 8<<20 {
			return errors.New("Agent response exceeded the chat size limit")
		}
		answer.WriteString(p.Update.Content.Text)
		if req.Stream {
			return emit(object{"content": p.Update.Content.Text}, nil)
		}
		return nil
	}
	result, err := a.call(ctx, "session/prompt", object{"sessionId": a.session, "prompt": blocks}, update)
	var stop struct {
		StopReason string `json:"stopReason"`
	}
	if err == nil {
		err = json.Unmarshal(result, &stop)
	}
	finish := "stop"
	if err == nil {
		switch stop.StopReason {
		case "end_turn":
		case "max_tokens":
			finish = "length"
		case "refusal":
			finish = "content_filter"
		default:
			err = fmt.Errorf("ACP turn did not complete: %s", stop.StopReason)
		}
	}
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		if started {
			b, _ := json.Marshal(object{"error": object{"message": err.Error(), "type": "antigravity_acp_error"}})
			_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
			_ = http.NewResponseController(w).Flush()
		} else {
			sendError(w, err)
		}
		return
	}
	if req.Stream {
		if emit(object{}, finish) == nil {
			if _, err = io.WriteString(w, "data: [DONE]\n\n"); err == nil {
				if http.NewResponseController(w).Flush() == nil {
					traceComplete(ctx, false)
				}
			}
		}
		return
	}
	if sendJSON(w, 200, object{"id": id, "object": "chat.completion", "created": created, "model": req.Model, "choices": []object{{"index": 0, "message": object{"role": "assistant", "content": answer.String()}, "finish_reason": finish}}}) == nil {
		traceComplete(ctx, false)
	}
}
func main() {
	if (len(os.Args) == 3 || len(os.Args) == 4) && os.Args[1] == "--client-mcp" {
		if err := serveClientMCP(os.Args[2], os.Args[3:]...); err != nil {
			os.Exit(1)
		}
		return
	}
	if err := configureConcurrency(); err != nil {
		log.Print(err)
		return
	}
	token, err := os.ReadFile(env("BRIDGE_TOKEN_FILE", "/run/acp-token"))
	if err != nil {
		log.Print("Bridge token is unavailable")
		return
	}
	secret := strings.TrimSpace(string(token))
	if len(secret) < 32 {
		log.Print("Bridge token must contain at least 32 characters")
		return
	}
	work := env("WORK_ROOT", "/work")
	if !filepath.IsAbs(work) {
		log.Print("WORK_ROOT must be absolute")
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	accounts, err = newAccountManager(ctx, env("HOME", "/home/acp"))
	if err != nil {
		log.Print("Account manager is unavailable")
		return
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /admin/accounts", accounts.handle)
	mux.HandleFunc("POST /admin/accounts/refresh", accounts.refreshDetails)
	for _, path := range []string{"accounts/select", "accounts/rename", "accounts/delete", "oauth/start", "oauth/status", "oauth/complete", "oauth/cancel"} {
		mux.HandleFunc("POST /admin/"+path, accounts.handle)
	}
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		concurrency := admission.snapshot()
		cache := liveSessions.stats()
		sendJSON(w, 200, object{"status": "ok", "active_requests": active.Load(), "concurrency": concurrency, "version": "0.11.1", "session_cache": cache, "request_stats": requestStatsSnapshot(), "recent_requests": recentRequestSnapshot()})
	})
	mux.HandleFunc("GET /v1/models", withAdmission(modelList))
	mux.HandleFunc("POST /v1/chat/completions", observeRequest(withAdmission(chat)))
	mux.HandleFunc("POST /v1/responses", observeRequest(withAdmission(responses)))
	srv := &http.Server{Addr: env("LISTEN_ADDR", ":8328"), ReadHeaderTimeout: 15 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+secret)) != 1 {
			sendJSON(w, 401, object{"error": object{"message": "Unauthorized"}})
			return
		}
		mux.ServeHTTP(w, r)
	})}
	srv.BaseContext = func(_ net.Listener) context.Context { return ctx }
	go func() {
		<-ctx.Done()
		liveSessions.closeAll()
		c, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(c)
	}()
	log.Print("ACP bridge listening")
	if err = srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Print(err)
	}
}
