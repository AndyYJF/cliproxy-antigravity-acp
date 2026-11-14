package main

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func setWebForTest(t *testing.T, enabled bool) {
	t.Helper()
	nativeWebSettings.Lock()
	old, path := nativeWebSettings.Enabled, nativeWebSettings.path
	nativeWebSettings.Enabled = enabled
	nativeWebSettings.Unlock()
	t.Cleanup(func() {
		nativeWebSettings.Lock()
		nativeWebSettings.Enabled = old
		nativeWebSettings.path = path
		nativeWebSettings.Unlock()
	})
}
func TestNativeWebAcrossEndpoints(t *testing.T) {
	fixture(t)
	defer liveSessions.closeAll()
	for _, enabled := range []bool{false, true} {
		for _, reuse := range []bool{false, true} {
			for _, api := range []string{"chat", "agent", "responses"} {
				t.Run(api+map[bool]string{true: "/on", false: "/off"}[enabled]+map[bool]string{true: "/reuse", false: "/cold"}[reuse], func(t *testing.T) {
					setWebForTest(t, enabled)
					body := object{"model": "antigravity-acp/gemini-4-hydrogen"}
					path := "/v1/chat/completions"
					if api == "responses" {
						path = "/v1/responses"
						body["input"] = []object{{"role": "user", "content": "__NATIVE_WEB__"}}
					} else {
						body["messages"] = []object{{"role": "user", "content": "__NATIVE_WEB__"}}
						if api == "agent" {
							body["tools"] = []object{{"type": "function", "function": object{"name": "client_test", "parameters": object{"type": "object", "properties": object{}}}}}
						}
					}
					raw, _ := json.Marshal(body)
					r := httptest.NewRequest("POST", path, bytes.NewReader(raw))
					if reuse {
						r.Header.Set("X-ACP-Caller-Scope", "native-web-fixture-"+api)
					}
					w := httptest.NewRecorder()
					handler := chat
					if api == "responses" {
						handler = responses
					}
					observeRequest(handler)(w, r)
					if enabled && (w.Code != 200 || !strings.Contains(w.Body.String(), "LOCAL_FIXTURE_OK")) {
						t.Fatal(w.Code, w.Body.String())
					}
					if !enabled && w.Code == 200 {
						t.Fatal("disabled native tool was accepted", w.Body.String())
					}
					records := recentRequestSnapshot()
					if enabled && (records[0].WebSearches != 1 || records[0].WebReads != 1 || !records[0].NativeWeb) {
						t.Fatalf("missing web counts: %+v", records[0])
					}
				})
			}
		}
	}
}
func TestNativeWebSettingsPersistence(t *testing.T) {
	setWebForTest(t, false)
	home := t.TempDir()
	if err := loadNativeWebSettings(home); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	handleNativeWebSettings(w, httptest.NewRequest("POST", "/admin/settings", strings.NewReader(`{"native_web":true}`)))
	if w.Code != 200 || !nativeWebEnabled() {
		t.Fatal(w.Code, w.Body.String())
	}
	if err := loadNativeWebSettings(home); err != nil || !nativeWebEnabled() {
		t.Fatal("setting was not persisted", err)
	}
	w = httptest.NewRecorder()
	handleNativeWebSettings(w, httptest.NewRequest("POST", "/admin/settings", strings.NewReader(`{"native_web":"true"}`)))
	if w.Code != 400 {
		t.Fatal("invalid setting accepted")
	}
}
func TestNativeWebSwitchSeparatesCachedSessions(t *testing.T) {
	fixture(t)
	setWebForTest(t, true)
	defer liveSessions.closeAll()
	history := []object{{"role": "user", "content": "hello"}}
	body := object{"model": "antigravity-acp/gemini-4-hydrogen", "messages": history}
	first := cachedChat(t, "web-switch", body)
	reply := chatReply(t, first)
	body["messages"] = append(history, reply, object{"role": "user", "content": "continue"})
	nativeWebSettings.Lock()
	nativeWebSettings.Enabled = false
	nativeWebSettings.Unlock()
	next := cachedChat(t, "web-switch", body)
	if next.Header().Get("X-ACP-Session-Cache") == "hit" {
		t.Fatal("reused web-enabled session after disabling")
	}
}
func TestNativeWebIdentityAndPermission(t *testing.T) {
	n := newNativeWeb(true)
	if handled, _ := n.update(json.RawMessage(`{"sessionUpdate":"tool_call","toolCallId":"fake","name":"run_command","title":"search_web"}`)); handled {
		t.Fatal("title spoofing accepted")
	}
	raw := json.RawMessage(`{"sessionUpdate":"tool_call","toolCallId":"web1","name":"search_web"}`)
	for i := 0; i < 2; i++ {
		if handled, err := n.update(raw); !handled || err != nil {
			t.Fatal(handled, err)
		}
	}
	if n.searches != 1 {
		t.Fatal("duplicate event counted")
	}
	result, handled, err := n.permission(json.RawMessage(`{"toolCall":{"toolCallId":"web1"},"options":[{"optionId":"all","kind":"allow_always"},{"optionId":"once","kind":"allow_once"}]}`))
	b, _ := json.Marshal(result)
	if err != nil || !handled || !strings.Contains(string(b), "once") {
		t.Fatal(result, handled, err)
	}
}
