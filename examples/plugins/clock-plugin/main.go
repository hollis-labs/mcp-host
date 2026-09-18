// Command clock-plugin is a real, standalone inprocess-mode plugin: a
// lightweight subprocess speaking plugin-sdk/subprocess's JSON-RPC
// dialect, never implementing MCP itself — mcp-host performs the one
// real MCP handshake on its behalf (see transport/inprocess).
//
// This does NOT use plugin-sdk's subprocess.Serve helper, even though
// that is the documented way to build a plugin. Verified against
// plugin-sdk v0.5.0's subprocess/server.go: Serve's dispatch loop has a
// case for every method in protocol.go except mcp/list_tools — MethodInit,
// MethodLoad, MethodHealth, MethodMCPCallTool, etc. are all wired to a
// capability interface, but MethodListTools has no dispatch case and no
// corresponding capability interface at all. A plugin built on Serve,
// implementing every interface it offers, still cannot answer
// mcp/list_tools — the request 404s from Serve's own generic dispatch,
// with no extension point for a plugin author to add one. That's a real
// gap in the SDK, not a choice this example works around by accident;
// see T7's closing notes.
//
// So this hand-rolls the minimal host-facing loop directly against the
// wire protocol protocol.go documents (reusing its exported request/
// response and payload types for correctness), answering exactly the
// methods mcp-host's transport/inprocess.Transport calls:
// plugin/init, plugin/load, plugin/health, mcp/list_tools, mcp/call_tool,
// and plugin/unload. A future plugin-sdk release that adds
// mcp/list_tools support to Serve should let this shrink back down to a
// normal Serve(&clockPlugin{}) call.
package main

import (
	"bufio"
	"encoding/json"
	"os"
	"time"

	sdksub "github.com/hollis-labs/plugin-sdk/subprocess"
)

// listToolsResult/wireTool mirror the shape
// transport/inprocess expects for mcp/list_tools — see that
// package's doc comment for why this shape (not one plugin-sdk itself
// defines, since it defines none).
type listToolsResult struct {
	Tools []wireTool `json:"tools"`
}
type wireTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema,omitempty"`
	Annotations map[string]any `json:"annotations,omitempty"`
}

func main() {
	reader := bufio.NewReaderSize(os.Stdin, 64*1024)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			var req sdksub.RPCRequest
			if json.Unmarshal(line, &req) == nil {
				handle(&req)
			}
		}
		if err != nil {
			return // stdin closed — the host is shutting this plugin down
		}
	}
}

func handle(req *sdksub.RPCRequest) {
	var result any
	var rpcErr *sdksub.RPCError

	switch req.Method {
	case sdksub.MethodInit:
		result = sdksub.InitResult{
			ID: "clock-plugin", Name: "Clock Plugin", Version: "0.1.0",
			Description: "Reports the current time.",
			Protocol:    sdksub.ProtocolVersion,
		}
	case sdksub.MethodLoad:
		result = sdksub.LoadResult{}
	case sdksub.MethodUnload:
		result = map[string]bool{"ok": true}
	case sdksub.MethodHealth:
		result = sdksub.HealthResult{OK: true}
	case sdksub.MethodListTools:
		result = listToolsResult{Tools: []wireTool{{
			Name:        "now",
			Description: "Returns the current UTC time in RFC 3339 format.",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false},
			Annotations: map[string]any{
				"readOnlyHint":    true,
				"destructiveHint": false,
				"idempotentHint":  false, // the answer changes on every call
				"openWorldHint":   false,
			},
		}}}
	case sdksub.MethodMCPCallTool:
		var params sdksub.MCPCallRequest
		if b, err := json.Marshal(req.Params); err == nil {
			_ = json.Unmarshal(b, &params)
		}
		if params.ToolName != "now" {
			rpcErr = &sdksub.RPCError{Code: sdksub.ErrCodeNotFound, Message: "unknown tool " + params.ToolName}
			break
		}
		payload, _ := json.Marshal(map[string]any{"now": time.Now().UTC().Format(time.RFC3339)})
		result = sdksub.MCPCallResult{Content: payload}
	default:
		rpcErr = &sdksub.RPCError{Code: sdksub.ErrCodeMethodNotFound, Message: "unknown method " + req.Method}
	}

	if req.ID == 0 {
		return // notification — no response expected
	}
	respond(req.ID, result, rpcErr)
}

func respond(id int64, result any, rpcErr *sdksub.RPCError) {
	resp := sdksub.RPCResponse{JSONRPC: "2.0", ID: id}
	if rpcErr != nil {
		resp.Error = rpcErr
	} else {
		raw, err := json.Marshal(result)
		if err != nil {
			resp.Error = &sdksub.RPCError{Code: sdksub.ErrCodeInternal, Message: err.Error()}
		} else {
			resp.Result = raw
		}
	}
	data, err := json.Marshal(resp)
	if err != nil {
		return
	}
	data = append(data, '\n')
	_, _ = os.Stdout.Write(data)
}
