package main

/*
#include <stdint.h>
#include <stdlib.h>

typedef struct {
	void* ptr;
	size_t len;
} cliproxy_buffer;

typedef int (*cliproxy_host_call_fn)(void*, const char*, const uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_host_free_fn)(void*, size_t);

typedef struct {
	uint32_t abi_version;
	void* host_ctx;
	cliproxy_host_call_fn call;
	cliproxy_host_free_fn free_buffer;
} cliproxy_host_api;

typedef int (*cliproxy_plugin_call_fn)(char*, uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_plugin_free_fn)(void*, size_t);
typedef void (*cliproxy_plugin_shutdown_fn)(void);

typedef struct {
	uint32_t abi_version;
	cliproxy_plugin_call_fn call;
	cliproxy_plugin_free_fn free_buffer;
	cliproxy_plugin_shutdown_fn shutdown;
} cliproxy_plugin_api;

extern int cliproxyPluginCall(char*, uint8_t*, size_t, cliproxy_buffer*);
extern void cliproxyPluginFree(void*, size_t);
extern void cliproxyPluginShutdown(void);

static cliproxy_host_api host_copy;
static const cliproxy_host_api* stored_host;

static void store_host_api(const cliproxy_host_api* host) {
	host_copy = *host;
	stored_host = &host_copy;
}

static int call_host_api(const char* method, const uint8_t* request, size_t request_len, cliproxy_buffer* response) {
	if (stored_host == NULL || stored_host->call == NULL) {
		return 1;
	}
	return stored_host->call(stored_host->host_ctx, method, request, request_len, response);
}

static void free_host_buffer(void* ptr, size_t len) {
	if (stored_host != NULL && stored_host->free_buffer != NULL && ptr != NULL) {
		stored_host->free_buffer(ptr, len);
	}
}
*/
import "C"

import (
	"encoding/json"
	"errors"
	"fmt"
	"gopkg.in/yaml.v3"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"unsafe"
)

type object = map[string]any
type envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *failure        `json:"error,omitempty"`
}
type failure struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	HTTPStatus int    `json:"http_status,omitempty"`
}

func (e *failure) Error() string { return e.Message }

type config struct {
	BridgeURL string `yaml:"bridge_url"`
	TokenFile string `yaml:"token_file"`
}

var mu sync.RWMutex
var current config

const provider = "antigravity-acp"

func main() {}

//export cliproxy_plugin_init
func cliproxy_plugin_init(host *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
	if host == nil || plugin == nil || host.abi_version != 1 {
		return 1
	}
	C.store_host_api(host)
	plugin.abi_version = 1
	plugin.call = C.cliproxy_plugin_call_fn(C.cliproxyPluginCall)
	plugin.free_buffer = C.cliproxy_plugin_free_fn(C.cliproxyPluginFree)
	plugin.shutdown = C.cliproxy_plugin_shutdown_fn(C.cliproxyPluginShutdown)
	return 0
}

//export cliproxyPluginCall
func cliproxyPluginCall(method *C.char, request *C.uint8_t, size C.size_t, response *C.cliproxy_buffer) (status C.int) {
	if response == nil {
		return 1
	}
	response.ptr = nil
	response.len = 0
	defer func() {
		if recover() != nil {
			writeReply(response, nil, &failure{"plugin_error", "ACP plugin internal error", 500})
			status = 1
		}
	}()
	if method == nil || uint64(size) > 32<<20 {
		writeReply(response, nil, &failure{"request_error", "Invalid plugin request", 400})
		return 1
	}
	result, err := handle(C.GoString(method), C.GoBytes(unsafe.Pointer(request), C.int(size)))
	writeReply(response, result, err)
	if err != nil {
		return 1
	}
	return 0
}

//export cliproxyPluginFree
func cliproxyPluginFree(ptr unsafe.Pointer, size C.size_t) { C.free(ptr) }

//export cliproxyPluginShutdown
func cliproxyPluginShutdown() {}
func writeReply(out *C.cliproxy_buffer, value any, err error) {
	e := envelope{OK: err == nil}
	if err != nil {
		var f *failure
		if errors.As(err, &f) {
			e.Error = f
		} else {
			e.Error = &failure{"plugin_error", err.Error(), 502}
		}
	} else {
		e.Result, _ = json.Marshal(value)
	}
	raw, _ := json.Marshal(e)
	out.ptr = C.CBytes(raw)
	out.len = C.size_t(len(raw))
}
func hostCall(method string, request any, result any) error {
	raw, err := json.Marshal(request)
	if err != nil {
		return err
	}
	m := C.CString(method)
	defer C.free(unsafe.Pointer(m))
	p := C.CBytes(raw)
	defer C.free(p)
	var out C.cliproxy_buffer
	rc := C.call_host_api(m, (*C.uint8_t)(p), C.size_t(len(raw)), &out)
	if out.ptr == nil {
		return fmt.Errorf("CPA host callback unavailable: %s", method)
	}
	defer C.free_host_buffer(out.ptr, out.len)
	var e envelope
	if err = json.Unmarshal(C.GoBytes(out.ptr, C.int(out.len)), &e); err != nil {
		return err
	}
	if !e.OK || rc != 0 {
		if e.Error != nil {
			return e.Error
		}
		return errors.New("CPA callback failed")
	}
	if result != nil && len(e.Result) > 0 {
		return json.Unmarshal(e.Result, result)
	}
	return nil
}
func settings() config { mu.RLock(); defer mu.RUnlock(); return current }
func transport(callback, method, path string, body []byte) (object, error) {
	c := settings()
	token, err := os.ReadFile(c.TokenFile)
	if err != nil {
		return nil, errors.New("ACP bridge token file is unavailable")
	}
	return object{"host_callback_id": callback, "method": method, "url": strings.TrimRight(c.BridgeURL, "/") + path, "headers": http.Header{"Content-Type": {"application/json"}, "Authorization": {"Bearer " + strings.TrimSpace(string(token))}}, "body": body}, nil
}
func handle(method string, raw []byte) (any, error) {
	switch method {
	case "plugin.register", "plugin.reconfigure":
		var req struct {
			ConfigYAML []byte `json:"config_yaml"`
		}
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, err
		}
		var c config
		if err := yaml.Unmarshal(req.ConfigYAML, &c); err != nil {
			return nil, err
		}
		u, err := url.Parse(c.BridgeURL)
		if err != nil || u.Scheme != "http" || u.Host == "" || c.TokenFile == "" {
			return nil, errors.New("bridge_url (internal HTTP URL) and token_file are required")
		}
		mu.Lock()
		current = c
		mu.Unlock()
		return object{"schema_version": 10, "metadata": object{"Name": provider, "Version": pluginVersion, "Author": "AndyYJF", "GitHubRepository": "https://github.com/AndyYJF/cliproxy-antigravity-acp", "Logo": resourceBase + "/logo.svg", "ConfigFields": []object{{"Name": "bridge_url", "Type": "string", "Description": "ACP 内部桥接地址（Docker 内网）"}, {"Name": "token_file", "Type": "string", "Description": "内部桥接密钥文件路径（不是 CPA 或 Google 密钥）"}}}, "capabilities": object{"management_api": true, "model_provider": true, "model_router": true, "executor": true, "executor_model_scope": "static", "executor_input_formats": []string{"chat-completions", "responses"}, "executor_output_formats": []string{"chat-completions", "responses"}}}, nil
	case "management.register":
		return managementRegistration(), nil
	case "management.handle":
		return handleManagement(raw)
	case "model.route":
		var req struct{ RequestedModel string }
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, err
		}
		if strings.HasPrefix(req.RequestedModel, provider+"/") {
			return object{"Handled": true, "TargetKind": "self"}, nil
		}
		return object{"Handled": false}, nil
	case "executor.identifier":
		return object{"identifier": provider}, nil
	case "model.static", "model.for_auth":
		request, err := transport("", "GET", "/v1/models", nil)
		if err != nil {
			return nil, err
		}
		var reply struct {
			StatusCode int
			Body       []byte
		}
		if err = hostCall("host.http.do", request, &reply); err != nil {
			return nil, err
		}
		if reply.StatusCode != 200 {
			return nil, &failure{"bridge_error", bridgeError(reply.Body), reply.StatusCode}
		}
		models := []object{}
		if reply.StatusCode == 200 {
			var catalog struct {
				Data []struct {
					ID string `json:"id"`
				}
			}
			if err = json.Unmarshal(reply.Body, &catalog); err != nil {
				return nil, err
			}
			for _, m := range catalog.Data {
				if strings.HasPrefix(m.ID, provider+"/gemini") {
					models = append(models, object{"ID": m.ID, "Object": "model", "OwnedBy": provider, "DisplayName": m.ID, "SupportedGenerationMethods": []string{"chat"}, "UserDefined": true})
				}
			}
		}
		rememberCatalog(reply.Body)
		return object{"Provider": provider, "Models": models}, nil
	case "executor.execute", "executor.execute_stream":
		var req struct {
			Model, Format, SourceFormat string
			Headers                     http.Header
			Metadata                    map[string]any
			Payload                     []byte
			HostCallbackID              string `json:"host_callback_id"`
			StreamID                    string `json:"stream_id"`
		}
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, err
		}
		path := "/v1/chat/completions"
		switch req.Format {
		case "openai", "chat-completions":
		case "openai-response", "responses":
			path = "/v1/responses"
		default:
			return nil, &failure{"request_error", "ACP supports Chat Completions and Responses", 400}
		}
		if req.SourceFormat != "" && req.SourceFormat != req.Format && !(req.SourceFormat == "responses" && req.Format == "openai-response") && !(req.SourceFormat == "chat-completions" && req.Format == "openai") {
			return nil, &failure{"request_error", "Unsupported source protocol for ACP", 400}
		}
		if !strings.HasPrefix(req.Model, provider+"/gemini") {
			return nil, &failure{"request_error", "ACP requires a prefixed Gemini model", 400}
		}
		var body object
		if err := json.Unmarshal(req.Payload, &body); err != nil {
			return nil, err
		}
		neutralizeClientIdentity(body, path == "/v1/responses")
		body["model"] = req.Model
		body["stream"] = method == "executor.execute_stream"
		payload, _ := json.Marshal(body)
		request, err := transport(req.HostCallbackID, "POST", path, payload)
		if err != nil {
			return nil, err
		}
		if scope, ok := req.Metadata["caller_scope"].(string); ok && scope != "" {
			request["headers"].(http.Header).Set("X-ACP-Caller-Scope", scope)
		}
		// The host supplies the inbound headers; copy only the proxy's IP field.
		sourceIP := strings.TrimSpace(req.Headers.Get("X-Real-IP"))
		if sourceIP == "" {
			sourceIP = strings.TrimSpace(strings.Split(req.Headers.Get("X-Forwarded-For"), ",")[0])
		}
		if ip := net.ParseIP(sourceIP); ip != nil {
			request["headers"].(http.Header).Set("X-ACP-Source-IP", ip.String())
		}
		if method == "executor.execute" {
			var reply struct {
				StatusCode int
				Headers    http.Header
				Body       []byte
			}
			if err = hostCall("host.http.do", request, &reply); err != nil {
				return nil, err
			}
			if reply.StatusCode != 200 {
				return nil, &failure{"request_error", bridgeError(reply.Body), reply.StatusCode}
			}
			return object{"Payload": reply.Body, "Headers": reply.Headers}, nil
		}
		var reply struct {
			StatusCode int         `json:"status_code"`
			Headers    http.Header `json:"headers"`
			StreamID   string      `json:"stream_id"`
		}
		if err = hostCall("host.http.do_stream", request, &reply); err != nil {
			return nil, err
		}
		if reply.StreamID == "" {
			return nil, errors.New("CPA did not create an HTTP stream")
		}
		if reply.StatusCode != 200 {
			var chunk struct{ Payload []byte }
			_ = hostCall("host.http.stream_read", object{"stream_id": reply.StreamID}, &chunk)
			_ = hostCall("host.http.stream_close", object{"stream_id": reply.StreamID}, nil)
			return nil, &failure{"request_error", bridgeError(chunk.Payload), reply.StatusCode}
		}
		go forward(reply.StreamID, req.StreamID)
		return object{"headers": reply.Headers}, nil
	case "executor.count_tokens":
		return nil, &failure{"request_error", "Token counting is unavailable through ACP", 400}
	default:
		return nil, &failure{"unknown_method", "Unsupported method: " + method, 400}
	}
}
func bridgeError(raw []byte) string {
	var e struct{ Error struct{ Message string } }
	if json.Unmarshal(raw, &e) == nil && e.Error.Message != "" {
		return e.Error.Message
	}
	return "ACP bridge rejected the request"
}
func forward(upstream, downstream string) {
	var failureMessage string
	defer func() {
		_ = hostCall("host.http.stream_close", object{"stream_id": upstream}, nil)
		req := object{"stream_id": downstream}
		if failureMessage != "" {
			req["error_detail"] = object{"code": "stream_truncated", "message": failureMessage, "http_status": 502}
		}
		_ = hostCall("host.stream.close", req, nil)
	}()
	for {
		var chunk struct {
			Payload []byte `json:"payload"`
			Error   string `json:"error"`
			Done    bool   `json:"done"`
		}
		err := hostCall("host.http.stream_read", object{"stream_id": upstream}, &chunk)
		if err != nil {
			failureMessage = err.Error()
			return
		}
		if chunk.Error != "" {
			failureMessage = chunk.Error
			return
		}
		if len(chunk.Payload) > 0 {
			if err = hostCall("host.stream.emit", object{"stream_id": downstream, "payload": chunk.Payload}, nil); err != nil {
				return
			}
		}
		if chunk.Done {
			return
		}
	}
}
