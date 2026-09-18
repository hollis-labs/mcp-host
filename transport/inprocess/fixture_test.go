package inprocess

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"testing"

	sdksub "github.com/hollis-labs/plugin-sdk/subprocess"
)

const (
	fixtureEnvVar            = "STATION_TEST_FIXTURE_PLUGIN"
	fixtureUnhealthyAfterVar = "STATION_TEST_FIXTURE_UNHEALTHY_AFTER"
)

func TestMain(m *testing.M) {
	if os.Getenv(fixtureEnvVar) != "" {
		runFixturePlugin()
		return
	}
	os.Exit(m.Run())
}

// runFixturePlugin hand-implements plugin-sdk/subprocess's documented
// wire protocol (protocol.go) directly, rather than using
// subprocess.Serve — Serve's own dispatch loop has no case for
// MethodListTools (verified against v0.5.0's server.go), so a plugin
// built the documented way (subprocess.Serve + MCPHandler) cannot
// currently answer mcp/list_tools at all. This fixture instead validates
// mcp-host's host-side dispatch against the protocol protocol.go itself
// documents, independent of that gap in the SDK's reference plugin-side
// implementation. See T4's closing notes for T7.
func runFixturePlugin() {
	healthCalls := 0
	unhealthyAfter := -1
	if v := os.Getenv(fixtureUnhealthyAfterVar); v != "" {
		fmt.Sscanf(v, "%d", &unhealthyAfter)
	}

	reader := bufio.NewReaderSize(os.Stdin, 64*1024)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			var req sdksub.RPCRequest
			if json.Unmarshal(line, &req) == nil {
				handleFixtureRequest(&req, &healthCalls, unhealthyAfter)
			}
		}
		if err != nil {
			break
		}
	}
	os.Exit(0)
}

func handleFixtureRequest(req *sdksub.RPCRequest, healthCalls *int, unhealthyAfter int) {
	var result any
	var rpcErr *sdksub.RPCError

	switch req.Method {
	case sdksub.MethodInit:
		result = sdksub.InitResult{ID: "fixture", Name: "Fixture Plugin", Version: "test", Protocol: sdksub.ProtocolVersion}
	case sdksub.MethodLoad:
		result = sdksub.LoadResult{}
	case sdksub.MethodUnload:
		result = map[string]bool{"ok": true}
	case sdksub.MethodHealth:
		*healthCalls++
		if unhealthyAfter >= 0 && *healthCalls > unhealthyAfter {
			result = sdksub.HealthResult{OK: false, Message: "forced unhealthy"}
		} else {
			result = sdksub.HealthResult{OK: true}
		}
	case sdksub.MethodListTools:
		result = pluginListToolsResult{Tools: []wireTool{{
			Name:        "ping",
			Description: "Echoes back the given message.",
			InputSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{"message": map[string]any{"type": "string"}},
			},
		}}}
	case sdksub.MethodMCPCallTool:
		var params sdksub.MCPCallRequest
		if b, err := json.Marshal(req.Params); err == nil {
			_ = json.Unmarshal(b, &params)
		}
		msg, _ := params.Arguments["message"].(string)
		payload, _ := json.Marshal(map[string]any{"pong": "pong:" + msg})
		result = sdksub.MCPCallResult{Content: payload}
	default:
		rpcErr = &sdksub.RPCError{Code: sdksub.ErrCodeMethodNotFound, Message: "unknown method " + req.Method}
	}

	if req.ID == 0 {
		return // notification, no response expected
	}
	resp := sdksub.RPCResponse{JSONRPC: "2.0", ID: req.ID}
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
