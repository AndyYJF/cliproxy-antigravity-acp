package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The fixture speaks ACP over stdio and never authenticates or calls a provider.
func TestMain(m *testing.M) {
	if os.Getenv("ACP_TEST_HELPER") == "1" {
		if len(os.Args) > 2 && os.Args[2] == "--login" {
			fmt.Println("Open this link: https://accounts.google.com/o/oauth2/auth?state=fixture-state&client_id=fixture")
			scan := bufio.NewScanner(os.Stdin)
			if scan.Scan() && strings.Contains(scan.Text(), "fixture-code") {
				os.Exit(0)
			}
			os.Exit(1)
		}
		fakeACP()
		os.Exit(0)
	}
	os.Exit(m.Run())
}
func fakeACP() {
	var toolSocket, sessionID string
	var sessionCount, promptCount int
	scan := bufio.NewScanner(os.Stdin)
	scan.Buffer(make([]byte, 4096), 10<<20)
	write := func(v any) { _ = json.NewEncoder(os.Stdout).Encode(v) }
	for scan.Scan() {
		var msg rpcMessage
		if json.Unmarshal(scan.Bytes(), &msg) != nil {
			continue
		}
		result := any(object{})
		switch msg.Method {
		case "initialize":
			result = object{"protocolVersion": 1, "agentCapabilities": object{"promptCapabilities": object{"image": true}, "sessionCapabilities": object{"close": object{}}}}
		case "session/new":
			if sessionID != "" {
				write(object{"jsonrpc": "2.0", "id": msg.ID, "error": object{"code": -32603, "message": "previous session was not closed"}})
				continue
			}
			sessionCount++
			sessionID = fmt.Sprintf("local-fixture-%d", sessionCount)
			promptCount = 0
			var input struct{ MCPServers []struct{ Args []string } }
			_ = json.Unmarshal(msg.Params, &input)
			if len(input.MCPServers) > 0 && len(input.MCPServers[0].Args) > 2 {
				toolSocket = input.MCPServers[0].Args[2]
			}
			result = object{"sessionId": sessionID, "configOptions": []any{object{"id": "model", "category": "model", "options": []any{object{"value": "gemini-4-argon"}, object{"value": "gemini-4-hydrogen"}, object{"value": "gemini-pro-orchestrator-g1-pro"}, object{"value": "gemini-3.8-flash"}, object{"value": "claude-fixture"}}}}}
		case "session/close":
			if os.Getenv("ACP_TEST_CLOSE_FAIL") == "1" {
				write(object{"jsonrpc": "2.0", "id": msg.ID, "error": object{"code": -32603, "message": "close failed"}})
				continue
			}
			sessionID, toolSocket = "", ""
		case "session/cancel":
			return
		case "session/prompt":
			if delay, _ := strconv.Atoi(os.Getenv("ACP_TEST_DELAY_MS")); delay > 0 {
				time.Sleep(time.Duration(delay) * time.Millisecond)
			}
			promptCount++
			prompt := string(msg.Params)
			if strings.Contains(prompt, "__REUSE_TOOL__") || strings.Contains(prompt, "__REUSE_CUSTOM__") {
				write(object{"jsonrpc": "2.0", "method": "session/update", "params": object{"sessionId": sessionID, "update": object{"sessionUpdate": "tool_call", "title": "Run mcp_cpa_client_tools_client_tool_0", "rawInput": object{"value": "fixture"}}}})
				args := json.RawMessage(`{"value":"fixture"}`)
				if strings.Contains(prompt, "__REUSE_CUSTOM__") {
					args = json.RawMessage(`{"input":"RAW_PATCH"}`)
				}
				result, err := relayClientTool(toolSocket, clientInvocation{Name: "client_tool_0", Arguments: args})
				if err != nil {
					return
				}
				b, _ := json.Marshal(result)
				write(object{"jsonrpc": "2.0", "method": "session/update", "params": object{"sessionId": sessionID, "update": object{"sessionUpdate": "agent_message_chunk", "content": object{"type": "text", "text": string(b)}}}})
				write(object{"jsonrpc": "2.0", "id": msg.ID, "result": object{"stopReason": "end_turn"}})
				continue
			}
			if strings.Contains(prompt, "__CHECK_IMAGES__") || strings.Contains(prompt, "__CHECK_IMAGES_12__") {
				var p struct{ Prompt []object }
				_ = json.Unmarshal(msg.Params, &p)
				count := 0
				for _, b := range p.Prompt {
					if b["type"] == "image" && b["mimeType"] == "image/png" {
						count++
					}
				}
				expected := 2
				if strings.Contains(prompt, "__CHECK_IMAGES_12__") {
					expected = 12
				}
				if count != expected {
					write(object{"jsonrpc": "2.0", "id": msg.ID, "error": object{"code": -32602, "message": "native image blocks missing"}})
					continue
				}
			}
			if strings.Contains(prompt, "__CLIENT_TOOL__") || strings.Contains(prompt, "__SERVER_TOOL__") {
				title := "Run mcp_cpa_client_tools_client_tool_0"
				if strings.Contains(prompt, "__SERVER_TOOL__") {
					title = "Run shell command"
				}
				write(object{"jsonrpc": "2.0", "method": "session/update", "params": object{"sessionId": sessionID, "update": object{"sessionUpdate": "tool_call", "toolCallId": "test-call", "title": title, "rawInput": object{"value": "fixture", "toolSummary": "PRIVATE_CLI_SUMMARY"}}}})
				continue
			}
			if strings.Contains(prompt, "__WAIT__") {
				continue
			}
			if strings.Contains(prompt, "__OVERSIZED__") {
				fmt.Println(strings.Repeat("x", 9<<20))
				continue
			}
			if strings.Contains(prompt, "__PERMISSION__") {
				write(object{"jsonrpc": "2.0", "id": 99, "method": "session/request_permission", "params": object{}})
				continue
			}
			write(object{"jsonrpc": "2.0", "method": "session/update", "params": object{"sessionId": sessionID, "update": object{"sessionUpdate": "agent_thought_chunk", "content": object{"type": "text", "text": "PRIVATE_THOUGHT"}}}})
			text := "LOCAL_FIXTURE_OK"
			if strings.Contains(prompt, "__PROCESS_STATE__") {
				text = fmt.Sprintf("pid=%d session=%d turns=%d", os.Getpid(), sessionCount, promptCount)
			}
			write(object{"jsonrpc": "2.0", "method": "session/update", "params": object{"sessionId": sessionID, "update": object{"sessionUpdate": "agent_message_chunk", "content": object{"type": "text", "text": text}}}})
			if strings.Contains(prompt, "__FAIL__") {
				write(object{"jsonrpc": "2.0", "id": msg.ID, "error": object{"code": -32603, "message": "fixture failure"}})
				continue
			}
			result = object{"stopReason": "end_turn"}
		}
		if len(msg.ID) > 0 {
			write(object{"jsonrpc": "2.0", "id": msg.ID, "result": result})
		}
	}
}
func fixture(t *testing.T) string {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	t.Setenv("AGY_BINARY", binary)
	t.Setenv("WORK_ROOT", root)
	t.Setenv("ACP_TEST_HELPER", "1")
	return root
}
func TestChatInputValidation(t *testing.T) {
	base := object{"model": "antigravity-acp/gemini-3.8-flash", "messages": []any{object{"role": "user", "content": "hello"}}}
	for _, field := range []string{"tools", "functions", "audio", "modalities"} {
		t.Run(field, func(t *testing.T) {
			body := object{}
			for k, v := range base {
				body[k] = v
			}
			body[field] = 1
			raw, _ := json.Marshal(body)
			_, _, err := promptBody(raw)
			if err == nil {
				t.Fatalf("accepted unsupported %s", field)
			}
		})
	}
	raw := []byte(`{"model":"antigravity-acp/gemini-3.8-flash","messages":[{"role":"system","content":"be brief"},{"role":"user","content":[{"type":"text","text":"remember CEDAR"}]},{"role":"assistant","content":"OK"},{"role":"user","content":"recall"}]}`)
	_, prompt, err := promptBody(raw)
	if err != nil || !strings.Contains(prompt, "CEDAR") || !strings.Contains(prompt, "be brief") {
		t.Fatalf("history lost: %v", err)
	}
	for _, raw := range []string{`{"model":"gemini-3.8-flash","messages":[]}`, `{"model":"antigravity-acp/gemini-3.8-flash","messages":[{"role":"user","content":[{"type":"image_url"}]}]}`, `{"model":"antigravity-acp/gemini-3.8-flash","messages":[{"role":"assistant","content":"x"}]}`} {
		if _, _, err := promptBody([]byte(raw)); err == nil {
			t.Fatalf("accepted invalid payload: %s", raw)
		}
	}
}
func TestModelDiscoveryFiltersGemini(t *testing.T) {
	fixture(t)
	r := httptest.NewRecorder()
	modelList(r, httptest.NewRequest("GET", "/v1/models", nil))
	if r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	var d struct{ Data []object }
	_ = json.Unmarshal(r.Body.Bytes(), &d)
	if len(d.Data) != 4 || strings.Contains(r.Body.String(), "claude") {
		t.Fatal(r.Body.String())
	}
}
func runChat(t *testing.T, prompt string, stream bool) *httptest.ResponseRecorder {
	t.Helper()
	raw, _ := json.Marshal(object{"model": "antigravity-acp/gemini-3.8-flash", "messages": []any{object{"role": "user", "content": prompt}}, "stream": stream})
	r := httptest.NewRecorder()
	chat(r, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(string(raw))))
	return r
}
func TestChatAndStream(t *testing.T) {
	root := fixture(t)
	for _, stream := range []bool{false, true} {
		r := runChat(t, "hello", stream)
		body := r.Body.String()
		if r.Code != 200 || !strings.Contains(body, "LOCAL_FIXTURE_OK") || strings.Contains(body, "PRIVATE_THOUGHT") {
			t.Fatal(r.Code, body)
		}
		if stream != strings.Contains(body, "[DONE]") {
			t.Fatal("incorrect SSE completion")
		}
	}
	entries, _ := os.ReadDir(root)
	if len(entries) != 0 || active.Load() != 0 {
		t.Fatal("process/workspace not released")
	}
}
func TestStreamFailureIsNotSuccess(t *testing.T) {
	fixture(t)
	r := runChat(t, "__FAIL__", true)
	if strings.Contains(r.Body.String(), "[DONE]") || !strings.Contains(r.Body.String(), "fixture failure") {
		t.Fatal(r.Body.String())
	}
}
func TestPermissionRequestRejected(t *testing.T) {
	fixture(t)
	r := runChat(t, "__PERMISSION__", false)
	if r.Code != 502 || !strings.Contains(r.Body.String(), "cannot approve tools") {
		t.Fatal(r.Code, r.Body.String())
	}
}
func TestCancellationReleasesProcess(t *testing.T) {
	fixture(t)
	a, err := startACP(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer a.close()
	_, err = a.newSession(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err = a.call(ctx, "session/prompt", object{"prompt": "__WAIT__"}, nil)
	if err == nil || time.Since(started) > 2*time.Second {
		t.Fatal("cancellation failed", err)
	}
	if _, err = os.Stat(a.cwd); !os.IsNotExist(err) {
		t.Fatal("workspace retained")
	}
}
func TestOversizedACPOutputFailsPromptly(t *testing.T) {
	fixture(t)
	a, err := startACP(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer a.close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	started := time.Now()
	_, err = a.call(ctx, "session/prompt", object{"prompt": "__OVERSIZED__"}, nil)
	if err == nil || time.Since(started) > time.Second {
		t.Fatal("oversized ACP output did not stop promptly", err)
	}
}
func TestConcurrencyLimit(t *testing.T) {
	limiter := newRequestLimiter(2, 0, time.Second)
	if limiter.acquire(context.Background()) != nil || limiter.acquire(context.Background()) != nil {
		t.Fatal("slots unavailable")
	}
	defer limiter.release()
	defer limiter.release()
	err := limiter.acquire(context.Background())
	if h, ok := err.(*httpError); !ok || h.status != 429 {
		t.Fatal("concurrency limit not enforced", err)
	}
}
