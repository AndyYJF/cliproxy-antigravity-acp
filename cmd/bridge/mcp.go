package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
)

type clientTool struct {
	Name         string          `json:"name"`
	Description  string          `json:"description"`
	InputSchema  json.RawMessage `json:"inputSchema"`
	OriginalName string          `json:"-"`
	Namespace    string          `json:"-"`
	Custom       bool            `json:"-"`
}
type clientInvocation struct {
	CallID    string          `json:"-"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// This MCP process only hands a call back to the HTTP adapter. It never executes a client tool.
func serveClientMCP(configPath string, sockets ...string) error {
	raw, err := os.ReadFile(configPath)
	if err != nil {
		return err
	}
	var tools []clientTool
	if err = json.Unmarshal(raw, &tools); err != nil {
		return err
	}
	scan := bufio.NewScanner(os.Stdin)
	scan.Buffer(make([]byte, 4096), 8<<20)
	encoder := json.NewEncoder(os.Stdout)
	for scan.Scan() {
		var msg rpcMessage
		if json.Unmarshal(scan.Bytes(), &msg) != nil {
			continue
		}
		if len(msg.ID) == 0 || string(msg.ID) == "null" {
			continue
		}
		var result any
		var failure any
		switch msg.Method {
		case "initialize":
			result = object{"protocolVersion": "2024-11-05", "capabilities": object{"tools": object{}}, "serverInfo": object{"name": "cpa-client-tools", "version": "0.4.0"}}
		case "ping":
			result = object{}
		case "tools/list":
			result = object{"tools": tools}
		case "tools/call":
			var call clientInvocation
			if json.Unmarshal(msg.Params, &call) != nil {
				failure = object{"code": -32602, "message": "Invalid tool call"}
				break
			}
			found := false
			for _, tool := range tools {
				if tool.Name == call.Name {
					found = true
					break
				}
			}
			if !found || !json.Valid(call.Arguments) {
				failure = object{"code": -32602, "message": "Unknown client tool"}
				break
			}
			if len(sockets) > 0 {
				value, relayErr := relayClientTool(sockets[0], call)
				if relayErr != nil {
					failure = object{"code": -32603, "message": "Client tool relay unavailable"}
				} else {
					result = value
				}
				break
			}
			// The parent intercepts the native ACP event before execution. Fail closed
			// if a future CLI invokes MCP without first publishing that event.
			failure = object{"code": -32603, "message": "Client tool must be executed by the downstream client"}
		default:
			failure = object{"code": -32601, "message": "Method not found"}
		}
		reply := object{"jsonrpc": "2.0", "id": msg.ID}
		if failure != nil {
			reply["error"] = failure
		} else {
			reply["result"] = result
		}
		if err = encoder.Encode(reply); err != nil {
			return fmt.Errorf("MCP reply: %w", err)
		}
	}
	return scan.Err()
}
