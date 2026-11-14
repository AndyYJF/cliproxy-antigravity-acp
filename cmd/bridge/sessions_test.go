package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func cachedChat(t *testing.T, scope string, body object) *httptest.ResponseRecorder {
	t.Helper()
	raw, _ := json.Marshal(body)
	r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(string(raw)))
	r.Header.Set("X-ACP-Caller-Scope", scope)
	w := httptest.NewRecorder()
	chat(w, r)
	return w
}
func chatReply(t *testing.T, w *httptest.ResponseRecorder) object {
	t.Helper()
	var result struct{ Choices []struct{ Message object } }
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || len(result.Choices) != 1 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	return result.Choices[0].Message
}
func TestSessionChatReuseAndIsolation(t *testing.T) {
	fixture(t)
	defer liveSessions.closeAll()
	messages := []object{{"role": "user", "content": "remember alpha"}}
	body := object{"model": "antigravity-acp/gemini-3.8-flash", "messages": messages}
	first := cachedChat(t, "owner-a", body)
	reply := chatReply(t, first)
	body["messages"] = append(messages, reply, object{"role": "user", "content": "recall"})
	second := cachedChat(t, "owner-a", body)
	_ = chatReply(t, second)
	if second.Header().Get("X-ACP-Session-Cache") != "hit" || first.Header().Get("X-ACP-Session-Id") != second.Header().Get("X-ACP-Session-Id") {
		t.Fatal("chat session not reused", second.Header())
	}
	other := cachedChat(t, "owner-b", body)
	_ = chatReply(t, other)
	if other.Header().Get("X-ACP-Session-Cache") != "miss" || other.Header().Get("X-ACP-Process-Cache") != "cold" {
		t.Fatal("cross-owner reuse")
	}
	// Editing supplied history must never reuse hidden prior context.
	body["messages"] = []object{{"role": "user", "content": "different"}, reply, {"role": "user", "content": "recall"}}
	changed := cachedChat(t, "owner-a", body)
	_ = chatReply(t, changed)
	if changed.Header().Get("X-ACP-Session-Cache") != "miss" {
		t.Fatal("edited history reused")
	}
	liveSessions.clearIdle()
	if len(liveSessions.entries) != 0 {
		t.Fatal("idle sessions retained")
	}
}
func TestSessionNativeToolResultContinuation(t *testing.T) {
	fixture(t)
	defer liveSessions.closeAll()
	messages := []object{{"role": "user", "content": "__REUSE_TOOL__"}}
	body := object{"model": "antigravity-acp/gemini-3.8-flash", "messages": messages, "tools": []object{{"type": "function", "function": object{"name": "lookup", "parameters": object{"type": "object"}}}}}
	first := cachedChat(t, "owner-tool", body)
	reply := chatReply(t, first)
	calls := reply["tool_calls"].([]any)
	id := calls[0].(map[string]any)["id"]
	unrelated := object{"model": body["model"], "tools": body["tools"], "messages": []object{{"role": "user", "content": "independent while tool is pending"}}}
	other := cachedChat(t, "owner-tool", unrelated)
	_ = chatReply(t, other)
	if other.Header().Get("X-ACP-Process-Cache") != "cold" {
		t.Fatal("pending tool process donated")
	}
	body["messages"] = append(messages, reply, object{"role": "tool", "tool_call_id": id, "content": "ACTUAL_CLIENT_RESULT"})
	second := cachedChat(t, "owner-tool", body)
	out := chatReply(t, second)
	if second.Header().Get("X-ACP-Session-Cache") != "hit" || !strings.Contains(out["content"].(string), "ACTUAL_CLIENT_RESULT") {
		t.Fatal(second.Header(), second.Body.String())
	}
}
func TestSessionCancelAndEviction(t *testing.T) {
	fixture(t)
	defer liveSessions.closeAll()
	raw := `{"model":"antigravity-acp/gemini-3.8-flash","messages":[{"role":"user","content":"__WAIT__"}]}`
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(raw)).WithContext(ctx)
	r.Header.Set("X-ACP-Caller-Scope", "cancel")
	chat(httptest.NewRecorder(), r)
	if len(liveSessions.entries) != 0 {
		t.Fatal("canceled session retained")
	}
	p := sessionPool{entries: map[string]*liveSession{}}
	s, _, err := p.checkout("scope", "home", nil)
	if err != nil {
		t.Fatal(err)
	}
	p.finish(s, true)
	s.timer.Reset(time.Millisecond)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		p.mu.Lock()
		n := len(p.entries)
		p.mu.Unlock()
		if n == 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("idle session not expired")
}

func TestSessionConcurrentCheckoutDoesNotReadBusyHistory(t *testing.T) {
	p := sessionPool{entries: map[string]*liveSession{}}
	defer p.closeAll()
	history := []object{{"type": "message", "role": "user", "content": "one"}}
	s, _, err := p.checkout("scope", "home", history)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 100; i++ {
			s.history = historyHashes(history)
		}
	}()
	other, hit, err := p.checkout("scope", "home", history)
	if err != nil || hit || other == s {
		t.Fatal("leased session shared")
	}
	<-done
	p.finish(s, false)
	p.finish(other, false)
}

func TestResponseSessionToolContinuation(t *testing.T) {
	fixture(t)
	defer liveSessions.closeAll()
	for _, kind := range []string{"function", "custom"} {
		prompt := "__REUSE_TOOL__"
		if kind == "custom" {
			prompt = "__REUSE_CUSTOM__"
		}
		input := []object{{"type": "message", "role": "user", "content": prompt}}
		body := object{"model": "antigravity-acp/gemini-3.8-flash", "input": input, "tools": []object{{"type": kind, "name": "lookup", "parameters": object{"type": "object"}}}}
		run := func() *httptest.ResponseRecorder {
			raw, _ := json.Marshal(body)
			r := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(string(raw)))
			r.Header.Set("X-ACP-Caller-Scope", "responses-"+kind)
			w := httptest.NewRecorder()
			responses(w, r)
			return w
		}
		first := run()
		var result struct{ Output []object }
		if first.Code != 200 || json.Unmarshal(first.Body.Bytes(), &result) != nil || len(result.Output) != 1 {
			t.Fatal(first.Code, first.Body.String())
		}
		output := result.Output[0]
		outputKind := "function_call_output"
		if kind == "custom" {
			outputKind = "custom_tool_call_output"
		}
		body["input"] = append(input, output, object{"type": outputKind, "call_id": output["call_id"], "output": "REAL_RESPONSE_TOOL_RESULT"})
		second := run()
		if second.Code != 200 || second.Header().Get("X-ACP-Session-Cache") != "hit" || !strings.Contains(second.Body.String(), "REAL_RESPONSE_TOOL_RESULT") {
			t.Fatal(second.Header(), second.Body.String())
		}
	}
}

func TestIndependentRequestsReuseProcessWithFreshSessions(t *testing.T) {
	root := fixture(t)
	defer liveSessions.closeAll()
	var pid int
	var previousID string
	for i := 1; i <= 12; i++ {
		body := object{"model": "antigravity-acp/gemini-4-hydrogen", "messages": []object{{"role": "user", "content": fmt.Sprintf("__PROCESS_STATE__ request %d", i)}}}
		w := cachedChat(t, "independent", body)
		reply := chatReply(t, w)
		var gotPID, session, turns int
		if _, err := fmt.Sscanf(reply["content"].(string), "pid=%d session=%d turns=%d", &gotPID, &session, &turns); err != nil {
			t.Fatal(reply)
		}
		expected := "reused"
		if i == 1 {
			expected = "cold"
			pid = gotPID
		}
		if gotPID != pid || session != i || turns != 1 || w.Header().Get("X-ACP-Process-Cache") != expected || w.Header().Get("X-ACP-Session-Cache") != "miss" || w.Header().Get("X-ACP-Session-Id") == previousID {
			t.Fatal(i, reply, w.Header())
		}
		previousID = w.Header().Get("X-ACP-Session-Id")
		liveSessions.mu.Lock()
		if len(liveSessions.entries) != 1 {
			t.Fatal("unbounded sessions")
		}
		for _, s := range liveSessions.entries {
			entries, err := os.ReadDir(s.a.cwd)
			if err != nil || len(entries) != 1 {
				t.Fatal("old workspace retained", entries, err)
			}
		}
		liveSessions.mu.Unlock()
	}
	liveSessions.closeAll()
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatal("process workspace leaked", err, entries)
	}
}

func TestProcessReuseCloseFailureStartsFreshProcess(t *testing.T) {
	fixture(t)
	defer liveSessions.closeAll()
	t.Setenv("ACP_TEST_CLOSE_FAIL", "1")
	body := object{"model": "antigravity-acp/gemini-4-hydrogen", "messages": []object{{"role": "user", "content": "__PROCESS_STATE__"}}}
	first := cachedChat(t, "close-failure", body)
	a := chatReply(t, first)
	second := cachedChat(t, "close-failure", body)
	b := chatReply(t, second)
	if a["content"] == b["content"] || second.Header().Get("X-ACP-Process-Cache") != "cold" {
		t.Fatal(a, b, second.Header())
	}
}

func TestProcessReuseConcurrentFakeRequests(t *testing.T) {
	fixture(t)
	defer liveSessions.closeAll()
	t.Setenv("ACP_TEST_DELAY_MS", "20")
	for wave := 0; wave < 8; wave++ {
		var wg sync.WaitGroup
		results := make(chan *httptest.ResponseRecorder, 16)
		for i := 0; i < 16; i++ {
			wg.Add(1)
			go func(n int) {
				defer wg.Done()
				raw, _ := json.Marshal(object{"model": "antigravity-acp/gemini-4-hydrogen", "messages": []object{{"role": "user", "content": fmt.Sprintf("__PROCESS_STATE__ %d/%d", wave, n)}}})
				r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(string(raw)))
				r.Header.Set("X-ACP-Caller-Scope", "load-owner")
				w := httptest.NewRecorder()
				observeRequest(withAdmission(chat))(w, r)
				results <- w
			}(i)
		}
		wg.Wait()
		close(results)
		for w := range results {
			reply := chatReply(t, w)
			if !strings.HasSuffix(reply["content"].(string), "turns=1") || w.Header().Get("X-ACP-Session-Cache") != "miss" {
				t.Fatal("cross-request state", reply, w.Header())
			}
		}
		liveSessions.mu.Lock()
		count := len(liveSessions.entries)
		liveSessions.mu.Unlock()
		if count > admission.limit {
			t.Fatal("process bound exceeded", count)
		}
	}
}
