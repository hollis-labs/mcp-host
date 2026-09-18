package process

import (
	"context"
	"fmt"

	gmcpclient "github.com/hollis-labs/go-mcp/client"

	"github.com/hollis-labs/mcp-host/config"
	"github.com/hollis-labs/mcp-host/registry"
)

// dialTransport backs a process-mode logical server whose config sets
// URL: an already-running MCP server mcp-host connects to (http or sse)
// but neither spawns nor supervises. go-mcp/client.Pool already owns
// exactly the right shape for this — dial-on-first-use, one retry on a
// recoverable error, a lazy health probe — so this is a thin adapter
// converting between the Pool's SDK types and registry.Tool/ToolResult.
type dialTransport struct {
	pool *gmcpclient.Pool
	name string
}

func newDialTransport(name string, cfg *config.ProcessConfig) (*dialTransport, error) {
	kind := cfg.Transport
	if kind == "" {
		kind = gmcpclient.TransportHTTP
	}

	pool := gmcpclient.NewPool(gmcpclient.WithIdentity(identityName, identityVersion))
	if err := pool.Register(name, gmcpclient.ServerConfig{
		Transport:      kind,
		URL:            cfg.URL,
		Headers:        cfg.Headers,
		TimeoutSeconds: cfg.TimeoutSeconds,
	}); err != nil {
		return nil, fmt.Errorf("process transport %q: register dial target: %w", name, err)
	}
	return &dialTransport{pool: pool, name: name}, nil
}

func (d *dialTransport) ListTools(ctx context.Context) ([]registry.Tool, error) {
	res, err := d.pool.ListTools(ctx, d.name)
	if err != nil {
		return nil, fmt.Errorf("process transport %q: tools/list: %w", d.name, err)
	}
	return convertSDKTools(res.Tools)
}

func (d *dialTransport) CallTool(ctx context.Context, name string, arguments map[string]any) (*registry.ToolResult, error) {
	res, _, err := d.pool.CallTool(ctx, d.name, name, arguments)
	if err != nil {
		return nil, fmt.Errorf("process transport %q: tools/call %s: %w", d.name, name, err)
	}
	return convertSDKCallResult(res), nil
}

// Close tears down the dial connection. Nothing about the remote server
// itself is affected — mcp-host never owned it.
func (d *dialTransport) Close() error {
	return d.pool.Close()
}
