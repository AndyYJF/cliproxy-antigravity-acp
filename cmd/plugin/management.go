package main

import (
	"embed"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"
)

var pluginVersion = "0.12.0"

const resourceBase = "/v0/resource/plugins/antigravity-acp"
const managementBase = "/v0/management/plugins/antigravity-acp"

//go:embed web/*
var managementFiles embed.FS
var catalogMu sync.RWMutex
var catalog = catalogSnapshot{Models: []string{}}

type catalogSnapshot struct {
	Models    []string `json:"models"`
	CheckedAt string   `json:"checked_at"`
	Error     string   `json:"error,omitempty"`
}

func rememberCatalog(raw []byte) error {
	var data struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return err
	}
	ids := []string{}
	seen := map[string]bool{}
	for _, m := range data.Data {
		if strings.HasPrefix(m.ID, provider+"/gemini") && !seen[m.ID] {
			ids = append(ids, m.ID)
			seen[m.ID] = true
		}
	}
	catalogMu.Lock()
	catalog = catalogSnapshot{Models: ids, CheckedAt: time.Now().UTC().Format(time.RFC3339)}
	catalogMu.Unlock()
	return nil
}
func snapshotCatalog() catalogSnapshot {
	catalogMu.RLock()
	defer catalogMu.RUnlock()
	s := catalog
	s.Models = append([]string{}, catalog.Models...)
	return s
}

var accountRoutes = map[string]string{
	"/settings": "POST",
	"/accounts": "GET", "/accounts/refresh": "POST", "/accounts/select": "POST", "/accounts/rename": "POST", "/accounts/delete": "POST", "/oauth/start": "POST", "/oauth/status": "POST", "/oauth/complete": "POST", "/oauth/cancel": "POST",
}

func managementRegistration() object {
	result := object{
		"resources": []object{
			{"Path": "/overview", "Menu": "Antigravity ACP", "Description": "Gemini 通道状态、模型目录与请求路由"},
			{"Path": "/account-helpers.js"}, {"Path": "/app.js"}, {"Path": "/style.css"}, {"Path": "/logo.svg"}, {"Path": "/codex-models.json"},
		},
		"routes": []object{
			{"Method": "GET", "Path": managementBase + "/status"},
			{"Method": "POST", "Path": managementBase + "/models/refresh"},
		},
	}
	routes := result["routes"].([]object)
	for path, method := range accountRoutes {
		routes = append(routes, object{"Method": method, "Path": managementBase + path})
	}
	result["routes"] = routes
	return result
}
func managementResponse(status int, contentType string, body []byte) object {
	return object{"StatusCode": status, "Headers": http.Header{
		"Content-Type": {contentType}, "Cache-Control": {"no-store"}, "X-Content-Type-Options": {"nosniff"},
		"Referrer-Policy":         {"no-referrer"},
		"Content-Security-Policy": {"default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self'; base-uri 'none'; form-action 'self'"},
	}, "Body": body}
}
func managementJSON(status int, value any) object {
	raw, _ := json.Marshal(value)
	return managementResponse(status, "application/json; charset=utf-8", raw)
}
func readBridge(callback, path string, timeout time.Duration) ([]byte, error) {
	return callBridge(callback, "GET", path, nil, timeout)
}
func callBridge(callback, method, path string, body []byte, timeout time.Duration) ([]byte, error) {
	request, err := transport(callback, method, path, body)
	if err != nil {
		return nil, err
	}
	var operation struct {
		ID string `json:"operation_id"`
	}
	if err = hostCall("host.http.operation_open", object{"host_callback_id": callback}, &operation); err != nil {
		return nil, err
	}
	request["operation_id"] = operation.ID
	cancel := func() {
		_ = hostCall("host.http.cancel", object{"host_callback_id": callback, "operation_id": operation.ID}, nil)
	}
	timer := time.AfterFunc(timeout, cancel)
	defer timer.Stop()
	defer cancel()
	var reply struct {
		StatusCode int
		Body       []byte
	}
	if err = hostCall("host.http.do", request, &reply); err != nil {
		return nil, err
	}
	if reply.StatusCode != http.StatusOK {
		return nil, &failure{"bridge_error", bridgeError(reply.Body), reply.StatusCode}
	}
	return reply.Body, nil
}
func handleManagement(raw []byte) (any, error) {
	var req struct {
		Method, Path   string
		Body           []byte
		HostCallbackID string `json:"host_callback_id"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	assets := map[string]struct{ Name, Type string }{
		resourceBase + "/overview":           {"index.html", "text/html; charset=utf-8"},
		resourceBase + "/codex-models.json":  {"codex-models.json", "application/json; charset=utf-8"},
		resourceBase + "/account-helpers.js": {"account-helpers.js", "application/javascript; charset=utf-8"},
		resourceBase + "/app.js":             {"app.js", "application/javascript; charset=utf-8"},
		resourceBase + "/style.css":          {"style.css", "text/css; charset=utf-8"},
		resourceBase + "/logo.svg":           {"logo.svg", "image/svg+xml"},
	}
	if asset, ok := assets[req.Path]; ok && req.Method == "GET" {
		b, err := managementFiles.ReadFile("web/" + asset.Name)
		if err != nil {
			return nil, err
		}
		return managementResponse(200, asset.Type, b), nil
	}
	if method, ok := accountRoutes[strings.TrimPrefix(req.Path, managementBase)]; ok && strings.HasPrefix(req.Path, managementBase+"/") && req.Method == method {
		if len(req.Body) > 16<<10 {
			return managementJSON(413, object{"error": "管理请求过大"}), nil
		}
		timeout := 12 * time.Second
		if req.Path == managementBase+"/accounts/refresh" {
			timeout = 40 * time.Second
		}
		body, err := callBridge(req.HostCallbackID, method, "/admin"+strings.TrimPrefix(req.Path, managementBase), req.Body, timeout)
		if err != nil {
			status := 502
			if f, ok := err.(*failure); ok {
				status = f.HTTPStatus
			}
			return managementJSON(status, object{"error": err.Error()}), nil
		}
		if req.Path == managementBase+"/accounts/select" {
			catalogMu.Lock()
			catalog = catalogSnapshot{Models: []string{}, Error: "账号已切换，请更新模型目录并重新加载插件。"}
			catalogMu.Unlock()
		}
		return managementResponse(200, "application/json; charset=utf-8", body), nil
	}
	switch {
	case req.Method == "GET" && req.Path == managementBase+"/status":
		bridge := object{"online": false}
		body, err := readBridge(req.HostCallbackID, "/healthz", 8*time.Second)
		if err == nil {
			var health struct {
				NativeWeb      bool `json:"native_web"`
				RecentRequests []struct {
					NativeWeb          bool     `json:"native_web"`
					WebSearches        int      `json:"web_searches"`
					WebReads           int      `json:"web_reads"`
					ProcessCache       string   `json:"process_cache,omitempty"`
					SetupMS            float64  `json:"setup_ms"`
					ID                 string   `json:"id"`
					Endpoint           string   `json:"endpoint"`
					RequestedModel     string   `json:"requested_model,omitempty"`
					Model              string   `json:"model,omitempty"`
					AccountID          string   `json:"account_id,omitempty"`
					AccountLabel       string   `json:"account_label,omitempty"`
					AccountEmail       string   `json:"account_email,omitempty"`
					SessionCache       string   `json:"session_cache"`
					SessionReason      string   `json:"session_reason,omitempty"`
					SessionDifferences []string `json:"session_differences,omitempty"`
					SessionID          string   `json:"session_id,omitempty"`
					SourceIP           string   `json:"source_ip,omitempty"`
					SourceIPKind       string   `json:"source_ip_kind,omitempty"`
					Status             string   `json:"status"`
					StartedAt          string   `json:"started_at"`
					DurationMS         float64  `json:"duration_ms"`
					QueueMS            float64  `json:"queue_ms"`
				} `json:"recent_requests"`
				Concurrency *struct {
					Active         int   `json:"active"`
					Queued         int   `json:"queued"`
					Limit          int   `json:"limit"`
					QueueLimit     int   `json:"queue_limit"`
					QueueTimeoutMS int64 `json:"queue_timeout_ms"`
				} `json:"concurrency"`
				Status       string `json:"status"`
				Active       int    `json:"active_requests"`
				RequestStats struct {
					Total uint64 `json:"total"`
					Since string `json:"since"`
				} `json:"request_stats"`
				Version      string `json:"version"`
				SessionCache *struct {
					ProcessHits uint64 `json:"process_hits"`
					Sessions    int    `json:"sessions"`
					Idle        int    `json:"idle"`
					Hits        uint64 `json:"hits"`
					Misses      uint64 `json:"misses"`
					Bypassed    uint64 `json:"bypassed"`
				} `json:"session_cache"`
			}
			if json.Unmarshal(body, &health) == nil && health.Status == "ok" {
				bridge = object{"online": true, "native_web": health.NativeWeb, "active_requests": health.Active, "version": health.Version, "session_cache": health.SessionCache, "request_stats": health.RequestStats, "recent_requests": health.RecentRequests, "concurrency": health.Concurrency}
			} else {
				bridge["message"] = "桥接服务返回了无法识别的状态，请检查服务日志。"
			}
		} else {
			bridge["message"] = "暂时无法连接桥接服务，请检查容器和内部连接配置。"
		}
		return managementJSON(200, object{"plugin_version": pluginVersion, "bridge": bridge, "catalog": snapshotCatalog(), "checked_at": time.Now().UTC().Format(time.RFC3339)}), nil
	case req.Method == "POST" && req.Path == managementBase+"/models/refresh":
		body, err := readBridge(req.HostCallbackID, "/v1/models", 40*time.Second)
		if err == nil {
			err = rememberCatalog(body)
		}
		if err != nil {
			catalogMu.Lock()
			catalog.Error = "模型目录更新失败；保留上次成功结果。请检查官方 CLI 登录状态与连接。"
			catalogMu.Unlock()
			return managementJSON(502, object{"error": "模型目录更新失败，请检查 CLI 登录状态与连接。", "catalog": snapshotCatalog()}), nil
		}
		return managementJSON(200, object{"catalog": snapshotCatalog()}), nil
	default:
		return managementJSON(404, object{"error": "页面或操作不存在"}), nil
	}
}
