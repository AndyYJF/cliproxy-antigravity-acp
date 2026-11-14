package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func accountRequest(t *testing.T, m *accountManager, path string, input object) (int, object) {
	t.Helper()
	raw, _ := json.Marshal(input)
	w := httptest.NewRecorder()
	method := "POST"
	if path == "accounts" {
		method = "GET"
	}
	m.handle(w, httptest.NewRequest(method, "/admin/"+path, bytes.NewReader(raw)))
	var out object
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}
func waitLogin(t *testing.T, m *accountManager, status string) object {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		_, s := accountRequest(t, m, "oauth/status", object{})
		if s["status"] == status {
			return s
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("login did not reach", status)
	return nil
}
func TestOfficialOAuthLifecycleKeepsExistingAccount(t *testing.T) {
	fixture(t)
	home := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m, err := newAccountManager(ctx, home)
	if err != nil {
		t.Fatal(err)
	}
	if status, _ := accountRequest(t, m, "oauth/start", object{"label": "New account"}); status != 200 {
		t.Fatal(status)
	}
	pending := waitLogin(t, m, "waiting")
	id := pending["id"]
	if status, _ := accountRequest(t, m, "oauth/complete", object{"id": id, "code": "http://localhost/?code=fixture-code&state=wrong"}); status != 400 {
		t.Fatal("state mismatch accepted")
	}
	if status, _ := accountRequest(t, m, "oauth/complete", object{"id": id, "code": "http://localhost/?code=fixture-code&state=fixture-state"}); status != 200 {
		t.Fatal(status)
	}
	completed := waitLogin(t, m, "completed")
	added := completed["account_id"].(string)
	if m.selectedHome() != home {
		t.Fatal("new login auto-selected")
	}
	restored, err := newAccountManager(ctx, home)
	if err != nil || len(restored.registry.Accounts) != 2 {
		t.Fatal("account persistence failed", err)
	}
	if status, _ := accountRequest(t, m, "accounts/select", object{"id": added}); status != 200 {
		t.Fatal(status)
	}
	if status, _ := accountRequest(t, m, "accounts/delete", object{"id": added}); status != 409 {
		t.Fatal("active account deleted")
	}
	accountRequest(t, m, "accounts/select", object{"id": "legacy"})
	if status, _ := accountRequest(t, m, "accounts/delete", object{"id": added}); status != 200 {
		t.Fatal(status)
	}
	if _, err := os.Stat(filepath.Join(m.root, added)); !os.IsNotExist(err) {
		t.Fatal("credential directory retained")
	}
	if status, _ := accountRequest(t, m, "accounts/delete", object{"id": "legacy"}); status != 409 {
		t.Fatal("original credentials deleted")
	}
	_, view := accountRequest(t, m, "accounts", nil)
	raw, _ := json.Marshal(view)
	if strings.Contains(string(raw), home) {
		t.Fatal("private home exposed")
	}
}
func TestOAuthCancelAndInvalidRegistry(t *testing.T) {
	fixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	home := t.TempDir()
	m, _ := newAccountManager(ctx, home)
	accountRequest(t, m, "oauth/start", object{"label": "Cancel me"})
	pending := waitLogin(t, m, "waiting")
	accountRequest(t, m, "oauth/cancel", object{"id": pending["id"]})
	waitLogin(t, m, "cancelled")
	if m.selectedHome() != home {
		t.Fatal("cancel changed account")
	}
	if m.home("../../outside") != "" {
		t.Fatal("path traversal")
	}
}
