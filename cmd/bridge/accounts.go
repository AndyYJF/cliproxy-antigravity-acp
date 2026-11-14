package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"
)

type accountEntry struct {
	ID      string "json:\"id\""
	Label   string "json:\"label\""
	Created string "json:\"created_at\""
}
type accountRegistry struct {
	Active   string         "json:\"active_id\""
	Accounts []accountEntry "json:\"accounts\""
}
type oauthLogin struct {
	ID, AccountID, Label, URL, State, Status string
	Expires                                  time.Time
	cancel                                   context.CancelFunc
	stdin                                    io.WriteCloser
}
type accountManager struct {
	mu               sync.Mutex
	root, legacyHome string
	registry         accountRegistry
	job              *oauthLogin
	details          map[string]accountDetails
	detailBusy       map[string]bool
	ctx              context.Context
}

var accounts *accountManager
var accountIDPattern = regexp.MustCompile("^[a-f0-9]{32}$")
var oauthURLPattern = regexp.MustCompile("https://accounts\\.google\\.com/[^\\s\\x1b]+")

func newAccountManager(ctx context.Context, home string) (*accountManager, error) {
	m := &accountManager{ctx: ctx, legacyHome: home, root: filepath.Join(home, ".cpa-accounts")}
	if !filepath.IsAbs(home) {
		return nil, errors.New("Account home must be absolute")
	}
	if err := os.MkdirAll(m.root, 0700); err != nil {
		return nil, err
	}
	m.registry = accountRegistry{Active: "legacy", Accounts: []accountEntry{{ID: "legacy", Label: "现有 Antigravity 登录"}}}
	raw, err := os.ReadFile(filepath.Join(m.root, "accounts.json"))
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if err == nil {
		if json.Unmarshal(raw, &m.registry) != nil {
			return nil, errors.New("Invalid account registry")
		}
		found := false
		seen := map[string]bool{}
		for _, a := range m.registry.Accounts {
			if (a.ID != "legacy" && !accountIDPattern.MatchString(a.ID)) || seen[a.ID] {
				return nil, errors.New("Invalid account registry ID")
			}
			seen[a.ID] = true
			if a.ID == m.registry.Active {
				found = true
			}
		}
		if !found {
			return nil, errors.New("Active account is missing")
		}
	}
	return m, nil
}
func (m *accountManager) save() error {
	data, err := json.Marshal(m.registry)
	if err != nil {
		return err
	}
	path := filepath.Join(m.root, "accounts.json")
	if err = os.WriteFile(path+".next", data, 0600); err != nil {
		return err
	}
	return os.Rename(path+".next", path)
}
func (m *accountManager) home(id string) string {
	if id == "legacy" {
		return m.legacyHome
	}
	if !accountIDPattern.MatchString(id) {
		return ""
	}
	return filepath.Join(m.root, id)
}
func (m *accountManager) selectedHome() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.home(m.registry.Active)
}
func accountEnvironment(home string) []string {
	values := []string{}
	for _, v := range os.Environ() {
		key, _, _ := strings.Cut(v, "=")
		if key != "HOME" && key != "XDG_CONFIG_HOME" && key != "XDG_CACHE_HOME" && key != "XDG_DATA_HOME" {
			values = append(values, v)
		}
	}
	return append(values, "HOME="+home, "XDG_CONFIG_HOME="+filepath.Join(home, ".config"), "XDG_CACHE_HOME="+filepath.Join(home, ".cache"), "XDG_DATA_HOME="+filepath.Join(home, ".local", "share"))
}
func validAccountLabel(label string) bool {
	return strings.TrimSpace(label) != "" && utf8.RuneCountInString(label) <= 80 && !strings.ContainsAny(label, "\r\n\x00")
}
func (m *accountManager) snapshot() object {
	entries := []object{}
	for _, a := range m.registry.Accounts {
		entries = append(entries, object{"id": a.ID, "label": a.Label, "created_at": a.Created, "legacy": a.ID == "legacy", "active": a.ID == m.registry.Active, "email": accountEmail(m.home(a.ID)), "details": m.details[a.ID], "refreshing": m.detailBusy[a.ID]})
	}
	return object{"accounts": entries, "active_id": m.registry.Active}
}
func (m *accountManager) jobSnapshot() object {
	if m.job == nil {
		return object{"status": "idle"}
	}
	j := m.job
	result := object{"id": j.ID, "status": j.Status, "expires_at": j.Expires.UTC().Format(time.RFC3339)}
	if j.Status == "waiting" {
		result["authorization_url"] = j.URL
	}
	if j.Status == "completed" {
		result["account_id"] = j.AccountID
	}
	return result
}
func (m *accountManager) startLogin(label string) error {
	if m.job != nil && (m.job.Status == "starting" || m.job.Status == "waiting" || m.job.Status == "exchanging") {
		return &httpError{409, "An OAuth login is already in progress"}
	}
	if !validAccountLabel(label) {
		return &httpError{400, "Account label must contain 1–80 characters"}
	}
	if len(m.registry.Accounts) >= 20 {
		return &httpError{400, "At most 20 accounts are supported"}
	}
	id := identifier("")
	home := m.home(id)
	if err := os.Mkdir(home, 0700); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(m.ctx, 5*time.Minute)
	cmd := exec.Command(env("AGY_BINARY", "/usr/local/bin/agy"), "acp", "--login")
	cmd.Dir = home
	cmd.Env = accountEnvironment(home)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		_ = os.RemoveAll(home)
		return err
	}
	reader, writer := io.Pipe()
	cmd.Stdout = writer
	cmd.Stderr = writer
	if err = cmd.Start(); err != nil {
		cancel()
		_ = stdin.Close()
		_ = reader.Close()
		_ = writer.Close()
		_ = os.RemoveAll(home)
		return errors.New("Could not start official OAuth login")
	}
	j := &oauthLogin{ID: identifier("login_"), AccountID: id, Label: strings.TrimSpace(label), Status: "starting", Expires: time.Now().Add(5 * time.Minute), cancel: cancel, stdin: stdin}
	m.job = j
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		case <-done:
		}
	}()
	go func() {
		defer reader.Close()
		buf := make([]byte, 2048)
		tail := ""
		for {
			n, e := reader.Read(buf)
			if n > 0 {
				tail += string(buf[:n])
				if len(tail) > 16384 {
					tail = tail[len(tail)-16384:]
				}
				if candidate := oauthURLPattern.FindString(tail); candidate != "" && strings.ContainsAny(tail[strings.Index(tail, candidate)+len(candidate):], "\r\n ") {
					u, parseErr := url.Parse(candidate)
					if parseErr == nil && u.Scheme == "https" && u.Host == "accounts.google.com" && u.Query().Get("state") != "" {
						m.mu.Lock()
						if m.job == j && j.Status == "starting" {
							j.URL = candidate
							j.State = u.Query().Get("state")
							j.Status = "waiting"
						}
						m.mu.Unlock()
					}
				}
			}
			if e != nil {
				return
			}
		}
	}()
	go func() {
		err := cmd.Wait()
		_ = writer.Close()
		_ = stdin.Close()
		close(done)
		cancel()
		m.mu.Lock()
		defer m.mu.Unlock()
		if err == nil && j.Status == "exchanging" {
			entry := accountEntry{ID: id, Label: j.Label, Created: time.Now().UTC().Format(time.RFC3339)}
			m.registry.Accounts = append(m.registry.Accounts, entry)
			if m.save() == nil {
				j.Status = "completed"
				j.URL = ""
				j.State = ""
				return
			}
			m.registry.Accounts = m.registry.Accounts[:len(m.registry.Accounts)-1]
		}
		if j.Status != "cancelled" {
			if time.Now().After(j.Expires) {
				j.Status = "expired"
			} else {
				j.Status = "failed"
			}
		}
		j.URL = ""
		j.State = ""
		_ = os.RemoveAll(home)
	}()
	return nil
}
func (m *accountManager) handle(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	var input struct {
		ID    string "json:\"id\""
		Label string "json:\"label\""
		Code  string "json:\"code\""
	}
	if r.Method == "POST" {
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&input) != nil {
			sendError(w, &httpError{400, "Invalid account operation"})
			return
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var err error
	switch r.URL.Path {
	case "/admin/accounts":
		sendJSON(w, 200, m.snapshot())
		return
	case "/admin/oauth/start":
		err = m.startLogin(input.Label)
	case "/admin/oauth/status":
		if input.ID != "" && (m.job == nil || input.ID != m.job.ID) {
			err = &httpError{404, "OAuth login not found"}
		}
	case "/admin/oauth/cancel":
		if m.job == nil || input.ID != m.job.ID {
			err = &httpError{404, "OAuth login not found"}
		} else if m.job.Status == "starting" || m.job.Status == "waiting" || m.job.Status == "exchanging" {
			m.job.Status = "cancelled"
			m.job.URL = ""
			m.job.cancel()
		}
	case "/admin/oauth/complete":
		j := m.job
		if j == nil || input.ID != j.ID || j.Status != "waiting" || time.Now().After(j.Expires) {
			err = &httpError{409, "OAuth login is not waiting for authorization"}
			break
		}
		code := strings.TrimSpace(input.Code)
		if strings.HasPrefix(code, "http://") || strings.HasPrefix(code, "https://") {
			u, e := url.Parse(code)
			if e != nil || u.Query().Get("state") != j.State || u.Query().Get("code") == "" {
				err = &httpError{400, "OAuth callback state is invalid; paste the URL from this login"}
				break
			}
		} else if code == "" || len(code) > 8192 || strings.ContainsAny(code, "\r\n ") {
			err = &httpError{400, "Enter the authorization code or full callback URL"}
			break
		}
		if _, e := io.WriteString(j.stdin, code+"\n"); e != nil {
			err = errors.New("Official OAuth process is unavailable")
		} else {
			j.Status = "exchanging"
			j.URL = ""
		}
	case "/admin/accounts/select", "/admin/accounts/rename", "/admin/accounts/delete":
		idx := -1
		for i, a := range m.registry.Accounts {
			if a.ID == input.ID {
				idx = i
				break
			}
		}
		if idx < 0 {
			err = &httpError{404, "Account not found"}
			break
		}
		previous := accountRegistry{Active: m.registry.Active, Accounts: append([]accountEntry(nil), m.registry.Accounts...)}
		switch r.URL.Path {
		case "/admin/accounts/select":
			m.registry.Active = input.ID
		case "/admin/accounts/rename":
			if !validAccountLabel(input.Label) {
				err = &httpError{400, "Account label must contain 1–80 characters"}
			} else {
				m.registry.Accounts[idx].Label = strings.TrimSpace(input.Label)
			}
		case "/admin/accounts/delete":
			if input.ID == "legacy" || input.ID == m.registry.Active || active.Load() > 0 {
				err = &httpError{409, "Switch away from the account and wait for requests to finish before deleting; the original login is preserved"}
			} else {
				m.registry.Accounts = append(m.registry.Accounts[:idx], m.registry.Accounts[idx+1:]...)
			}
		}
		if err == nil {
			if err = m.save(); err != nil {
				m.registry = previous
			} else if r.URL.Path == "/admin/accounts/delete" {
				liveSessions.clearIdle()
				err = os.RemoveAll(m.home(input.ID))
				delete(m.details, input.ID)
			}
		}
		if err == nil {
			if r.URL.Path == "/admin/accounts/select" {
				liveSessions.clearIdle()
			}
			sendJSON(w, 200, m.snapshot())
			return
		}
	default:
		err = &httpError{404, "Unknown account operation"}
	}
	if err != nil {
		sendError(w, err)
		return
	}
	sendJSON(w, 200, m.jobSnapshot())
}
