package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/mail"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

type accountTier struct {
	Name string `json:"name,omitempty"`
	ID   string `json:"id,omitempty"`
}
type quotaBucket struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Window    string   `json:"window,omitempty"`
	Remaining *float64 `json:"remaining_fraction"`
	ResetTime string   `json:"reset_time,omitempty"`
	Disabled  bool     `json:"disabled,omitempty"`
}
type quotaGroup struct {
	Name        string        `json:"name"`
	Description string        `json:"description,omitempty"`
	Buckets     []quotaBucket `json:"buckets"`
}
type accountDetails struct {
	Tier          accountTier  `json:"tier"`
	TierCheckedAt string       `json:"tier_checked_at,omitempty"`
	Groups        []quotaGroup `json:"groups"`
	CheckedAt     string       `json:"checked_at,omitempty"`
	AttemptedAt   string       `json:"attempted_at,omitempty"`
	Error         string       `json:"error,omitempty"`
	TierError     string       `json:"tier_error,omitempty"`
}

// Only the cached account email is decoded. Tokens never enter the response or logs.
func accountEmail(home string) string {
	f, err := os.Open(filepath.Join(home, ".gemini", "antigravity-cli", "antigravity-oauth-token"))
	if err != nil {
		return ""
	}
	defer f.Close()
	var profile struct {
		Email string `json:"id_token_email"`
	}
	if json.NewDecoder(io.LimitReader(f, 1<<20)).Decode(&profile) != nil {
		return ""
	}
	value := strings.TrimSpace(profile.Email)
	parsed, err := mail.ParseAddress(value)
	if err != nil || parsed.Address != value || len(value) > 254 {
		return ""
	}
	return value
}

type quotaOutput struct{ bytes.Buffer }

func (b *quotaOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 1<<20 {
		return 0, errors.New("Official quota output exceeded limit")
	}
	return b.Buffer.Write(p)
}

// ACP 1.5.7 exposes the tier, but not quota. Its official read-only CLI command
// is the fallback; never send /usage as an unadvertised ACP inference prompt.
func readOfficialQuota(ctx context.Context, home string) ([]quotaGroup, error) {
	cmd := exec.CommandContext(ctx, env("AGY_BINARY", "/usr/local/bin/agy"), "-p", "/usage", "--output-format", "json")
	cmd.Dir = home
	cmd.Env = accountEnvironment(home)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
	var out quotaOutput
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	if cmd.Run() != nil {
		return nil, errors.New("官方 CLI 配额查询未完成，请稍后重试。")
	}
	var result struct {
		Status  string `json:"status"`
		Turns   *int   `json:"num_turns"`
		Command struct {
			Name string `json:"name"`
			Data struct {
				Groups []quotaGroup `json:"groups"`
			} `json:"data"`
		} `json:"command"`
	}
	if json.Unmarshal(out.Bytes(), &result) != nil || result.Status != "SUCCESS" || result.Turns == nil || *result.Turns != 0 || result.Command.Name != "usage" {
		return nil, errors.New("官方 CLI 未返回只读配额数据。")
	}
	groups := []quotaGroup{}
	for _, group := range result.Command.Data.Groups {
		if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(group.Name)), "gemini") {
			continue
		}
		for i := range group.Buckets {
			b := &group.Buckets[i]
			if b.Remaining != nil && (*b.Remaining < 0 || *b.Remaining > 1) {
				b.Remaining = nil
			}
		}
		groups = append(groups, group)
	}
	return groups, nil
}

func (m *accountManager) refreshDetails(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	var input struct {
		ID string `json:"id"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil {
		sendError(w, &httpError{400, "Invalid account operation"})
		return
	}
	m.mu.Lock()
	found := false
	for _, entry := range m.registry.Accounts {
		if entry.ID == input.ID {
			found = true
			break
		}
	}
	if !found {
		m.mu.Unlock()
		sendError(w, &httpError{404, "Account not found"})
		return
	}
	if m.details == nil {
		m.details = map[string]accountDetails{}
		m.detailBusy = map[string]bool{}
	}
	details := m.details[input.ID]
	attempted, _ := time.Parse(time.RFC3339, details.AttemptedAt)
	// Deduplicate concurrent requests and enforce a short retry / five-minute success cache.
	ttl := 30 * time.Second
	if details.Error == "" && details.CheckedAt != "" && details.TierError == "" {
		ttl = 5 * time.Minute
	}
	if m.detailBusy[input.ID] || time.Since(attempted) < ttl {
		snapshot := m.snapshot()
		m.mu.Unlock()
		sendJSON(w, 200, snapshot)
		return
	}
	home := m.home(input.ID)
	m.detailBusy[input.ID] = true
	active.Add(1) // Account deletion must not remove a home used by a CLI process.
	m.mu.Unlock()
	defer active.Add(-1)
	ctx, cancel := context.WithTimeout(r.Context(), 35*time.Second)
	defer cancel()
	details.AttemptedAt = time.Now().UTC().Format(time.RFC3339)
	details.TierError = ""
	a, err := startACPHome(ctx, home)
	if err == nil {
		var session sessionInfo
		session, err = a.newSession(ctx)
		if err == nil {
			details.Tier = session.Meta.Antigravity.Tier
			details.TierCheckedAt = time.Now().UTC().Format(time.RFC3339)
		}
		a.close()
	}
	if err != nil {
		details.TierError = "ACP 套餐信息暂时不可用。"
	}
	groups, err := readOfficialQuota(ctx, home)
	details.Error = ""
	if err != nil {
		details.Error = err.Error()
	} else {
		details.Groups = groups
		details.CheckedAt = time.Now().UTC().Format(time.RFC3339)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.details[input.ID] = details
	m.detailBusy[input.ID] = false
	sendJSON(w, 200, m.snapshot())
}
