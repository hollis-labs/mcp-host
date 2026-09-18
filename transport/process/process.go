// Package process implements the `process` transport mode: a logical
// server backed by a fully independent, real MCP server. Two variants,
// selected by config: dial (URL set — an already-running endpoint
// mcp-host connects to but does not own) and spawn (Command set —
// mcp-host owns, connects to, and supervises the subprocess itself). See
// the ADR at project/atlas/workspace/adr/adr_mcp_host_dual_transport
// for the process/inprocess tradeoff this mode is one half of.
package process

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/hollis-labs/mcp-host/config"
	"github.com/hollis-labs/mcp-host/registry"
)

// New builds a process-mode registry.Transport from cfg. ctx bounds the
// initial connection attempt (spawn's handshake, or nothing for dial,
// which connects lazily on first use like the rest of go-mcp/client.Pool).
// A nil logger defaults to slog.Default().
func New(ctx context.Context, name string, cfg *config.ProcessConfig, logger *slog.Logger) (registry.Transport, error) {
	if cfg == nil {
		return nil, fmt.Errorf("process transport %q: nil config", name)
	}
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.URL != "" {
		return newDialTransport(name, cfg)
	}
	return newSpawnTransport(ctx, name, cfg, logger)
}
