package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// Only routing metadata is retained in memory; active snapshots never expose request bodies.
const recentRequestLimit = 20

type requestRecord struct {
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
}
type requestRecordKey struct{}

var recentRequests = struct {
	sync.Mutex
	total   uint64
	since   string
	items   []requestRecord
	running map[string]requestRecord
}{since: time.Now().UTC().Format(time.RFC3339Nano), items: []requestRecord{}, running: map[string]requestRecord{}}

func requestTrace(ctx context.Context) *requestRecord {
	record, _ := ctx.Value(requestRecordKey{}).(*requestRecord)
	return record
}
func observeRequest(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		record := &requestRecord{ID: identifier("req_"), Endpoint: r.URL.Path, SessionCache: "not_checked", Status: "queued", SessionReason: "not_admitted", StartedAt: start.UTC().Format(time.RFC3339Nano)}
		if ip := net.ParseIP(r.Header.Get("X-ACP-Source-IP")); ip != nil {
			record.SourceIP = ip.String()
			record.SourceIPKind = "proxy_header"
		}
		r = r.WithContext(context.WithValue(r.Context(), requestRecordKey{}, record))
		recentRequests.Lock()
		recentRequests.total++
		recentRequests.running[record.ID] = *record
		recentRequests.Unlock()
		defer func() {
			if record.Status == "queued" || record.Status == "running" {
				record.Status = "failed"
			}
			record.DurationMS = float64(time.Since(start).Microseconds()) / 1000
			if r.Context().Err() != nil && record.Status != "completed" && record.Status != "tool_call" {
				record.Status = "cancelled"
			}
			recentRequests.Lock()
			defer recentRequests.Unlock()
			delete(recentRequests.running, record.ID)
			recentRequests.items = append([]requestRecord{*record}, recentRequests.items...)
			if len(recentRequests.items) > recentRequestLimit {
				recentRequests.items = recentRequests.items[:recentRequestLimit]
			}
		}()
		next(w, r)
	}
}
func requestStatsSnapshot() object {
	recentRequests.Lock()
	defer recentRequests.Unlock()
	return object{"total": recentRequests.total, "since": recentRequests.since}
}
func recentRequestSnapshot() []requestRecord {
	recentRequests.Lock()
	defer recentRequests.Unlock()
	running := make([]requestRecord, 0, len(recentRequests.running))
	for _, record := range recentRequests.running {
		started, _ := time.Parse(time.RFC3339Nano, record.StartedAt)
		record.DurationMS = float64(time.Since(started).Microseconds()) / 1000
		running = append(running, record)
	}
	sort.Slice(running, func(i, j int) bool { return running[i].StartedAt > running[j].StartedAt })
	return append(running, recentRequests.items...)
}
func publishRequestTrace(record *requestRecord) {
	if record == nil {
		return
	}
	recentRequests.Lock()
	recentRequests.running[record.ID] = *record
	recentRequests.Unlock()
}
func traceRequestedModel(ctx context.Context, raw []byte) {
	if record := requestTrace(ctx); record != nil {
		var body struct {
			Model string `json:"model"`
		}
		if json.Unmarshal(raw, &body) == nil && len(body.Model) <= 256 {
			record.RequestedModel = body.Model
			publishRequestTrace(record)
		}
	}
}
func traceAccount(ctx context.Context, home string) {
	record := requestTrace(ctx)
	if record == nil || accounts == nil {
		return
	}
	accounts.mu.Lock()
	defer accounts.mu.Unlock()
	for _, account := range accounts.registry.Accounts {
		if accounts.home(account.ID) == home {
			record.AccountID, record.AccountLabel = account.ID, account.Label
			record.AccountEmail = accountEmail(home)
			publishRequestTrace(record)
			return
		}
	}
}
func traceModel(ctx context.Context, model string) {
	if record := requestTrace(ctx); record != nil {
		record.Model = "antigravity-acp/" + strings.TrimPrefix(model, "antigravity-acp/")
		publishRequestTrace(record)
	}
}
func traceComplete(ctx context.Context, tool bool) {
	if record := requestTrace(ctx); record != nil {
		record.Status = "completed"
		if tool {
			record.Status = "tool_call"
		}
	}
}

// Completion is explicit: a stream can fail after its HTTP 200 headers.
type tracedAgentWriter struct {
	agentWriter
	ctx      context.Context
	toolCall bool
}

func (w *tracedAgentWriter) tool(tool clientTool, call clientInvocation) error {
	err := w.agentWriter.tool(tool, call)
	if err == nil {
		w.toolCall = true
	}
	return err
}
func (w *tracedAgentWriter) complete() error {
	err := w.agentWriter.complete()
	if err == nil {
		traceComplete(w.ctx, w.toolCall)
	}
	return err
}
