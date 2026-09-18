// Command clock-plugin is the inprocess-mode reference plugin: a
// lightweight subprocess speaking plugin-sdk/subprocess's JSON-RPC
// dialect, never implementing MCP itself. mcp-host performs the one
// real MCP handshake on its behalf (see
// github.com/hollis-labs/mcp-host/transport/inprocess).
//
// Its tool catalog is declared in config (see examples/config/host.yaml's
// inprocess.tools block for this plugin's "now" tool), not here — this
// plugin only ever answers plugin/init, plugin/load, plugin/health, and
// mcp/call_tool, via plugin-sdk's own documented subprocess.Serve helper.
// See transport/inprocess's package doc for why mcp-host never sends
// mcp/list_tools, which is what makes plain subprocess.Serve usable here.
package main

import (
	"context"
	"encoding/json"
	"os"
	"time"

	plugin "github.com/hollis-labs/plugin-sdk"
	sdksub "github.com/hollis-labs/plugin-sdk/subprocess"
)

type clockPlugin struct{}

func (clockPlugin) Init(ctx context.Context, params sdksub.InitParams) (sdksub.InitResult, error) {
	return sdksub.InitResult{
		ID: "clock-plugin", Name: "Clock Plugin", Version: "0.1.0",
		Description: "Reports the current time.",
		Protocol:    sdksub.ProtocolVersion,
	}, nil
}

func (clockPlugin) Load(ctx context.Context) (sdksub.LoadResult, error) {
	return sdksub.LoadResult{}, nil
}

func (clockPlugin) Unload(ctx context.Context) error { return nil }

func (clockPlugin) Health(ctx context.Context) (sdksub.HealthStatus, error) {
	return sdksub.HealthStatus{OK: true}, nil
}

func (clockPlugin) MCPCallTool(ctx context.Context, req sdksub.MCPCallRequest) (sdksub.MCPCallResult, error) {
	if req.ToolName != "now" {
		return sdksub.MCPCallResult{}, plugin.ErrNotFound("unknown tool " + req.ToolName)
	}
	payload, err := json.Marshal(map[string]any{"now": time.Now().UTC().Format(time.RFC3339)})
	if err != nil {
		return sdksub.MCPCallResult{}, err
	}
	return sdksub.MCPCallResult{Content: payload}, nil
}

func main() {
	if err := sdksub.Serve(clockPlugin{}); err != nil {
		os.Exit(1)
	}
}
