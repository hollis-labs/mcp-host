// Package bootstrap wires mcp-host's config, registry, and transport
// packages together: for each configured logical server it builds the
// right transport and registers it, discovering its tool list before
// returning. A consumer that wants full control (custom tools beyond
// config-declared servers, a different transport) can use
// registry/config/transport directly instead — see the root mcphost
// package's Run for the complete "just run this" entrypoint built on
// top of this.
package bootstrap

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/hollis-labs/mcp-host/config"
	"github.com/hollis-labs/mcp-host/registry"
	"github.com/hollis-labs/mcp-host/transport/inprocess"
	"github.com/hollis-labs/mcp-host/transport/process"
)

// BuildRegistry constructs and registers every logical server in cfg,
// discovering its tools before returning. ctx bounds each logical
// server's initial spawn/connect and its first tool discovery call.
//
// On any error, every transport already constructed during this call —
// including the one that just failed, and any already added to the
// registry — is closed before returning, so a partial startup failure
// never leaks a spawned subprocess.
func BuildRegistry(ctx context.Context, cfg *config.Config, logger *slog.Logger) (*registry.Registry, error) {
	reg := registry.New()

	for _, ls := range cfg.LogicalServers {
		transport, err := buildTransport(ctx, ls, logger)
		if err != nil {
			closeAll(reg)
			return nil, fmt.Errorf("build logical server %q: %w", ls.ID, err)
		}

		entry, err := reg.Add(ls.ID, ls.Name, ls.Description, transport)
		if err != nil {
			closeTransport(transport)
			closeAll(reg)
			return nil, fmt.Errorf("register logical server %q: %w", ls.ID, err)
		}

		if _, err := entry.DiscoverTools(ctx); err != nil {
			closeAll(reg)
			return nil, fmt.Errorf("discover tools for %q: %w", ls.ID, err)
		}
	}

	return reg, nil
}

func buildTransport(ctx context.Context, ls config.LogicalServer, logger *slog.Logger) (registry.Transport, error) {
	switch ls.Transport {
	case config.TransportProcess:
		return process.New(ctx, ls.ID, ls.Process, logger)
	case config.TransportInprocess:
		t, err := inprocess.New(ctx, ls.ID, ls.Inprocess, logger)
		if err != nil {
			return nil, err
		}
		return t, nil
	default:
		return nil, fmt.Errorf("unknown transport %q", ls.Transport)
	}
}

func closeAll(reg *registry.Registry) {
	for _, entry := range reg.List() {
		reg.Remove(entry.ID)
	}
}

func closeTransport(t registry.Transport) {
	if closer, ok := t.(interface{ Close() error }); ok {
		_ = closer.Close()
	}
}
