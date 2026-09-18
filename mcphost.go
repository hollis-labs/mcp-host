// Package mcphost is a dual-transport MCP plugin host: it hosts N
// independently addressable "logical MCP servers," each backed by
// either a real standalone MCP server (process mode, transport/process)
// or a lightweight plugin-sdk-dialect subprocess (inprocess mode,
// transport/inprocess). See the ADR at
// project/atlas/workspace/adr/adr_mcp_host_dual_transport (Tesseract)
// for the full design.
//
// Run is the complete "just run this" entrypoint: load a config.Config
// (see the config package) and call Run with it. A consumer that wants
// more control — custom tools beyond config-declared servers, a
// different transport, its own serving loop — can use bootstrap,
// registry, serving, and the transport packages directly instead; Run
// is a convenience layered on top of them, not the only way in.
package mcphost

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	gmcpserver "github.com/hollis-labs/go-mcp/server"

	"github.com/hollis-labs/mcp-host/bootstrap"
	"github.com/hollis-labs/mcp-host/config"
	"github.com/hollis-labs/mcp-host/serving"
)

// Options configures Run.
type Options struct {
	// Logger receives Run's own diagnostic logging. Defaults to
	// slog.Default() when nil.
	Logger *slog.Logger

	// HTTPAddr is the address to serve HTTP-exposed logical servers on,
	// if cfg configures any. Defaults to ":8080" when empty; unused if
	// no logical server in cfg sets serve.http.
	HTTPAddr string
}

// Run builds a registry from cfg (via bootstrap.BuildRegistry), exposes
// each logical server per its Serve block (via the serving package) over
// stdio and/or HTTP, and blocks until ctx is done or the stdio client
// disconnects. It returns nil on a clean, ctx-driven shutdown.
//
// At most one logical server may set serve.stdio (config.Config already
// enforces this at parse time) — Run blocks on that one server's own
// Run(ctx) for as long as it's connected. If no logical server sets
// serve.stdio, Run just blocks on ctx.Done() instead, so an HTTP-only
// config still serves until signaled.
func Run(ctx context.Context, cfg *config.Config, opts Options) error {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	httpAddr := opts.HTTPAddr
	if httpAddr == "" {
		httpAddr = ":8080"
	}

	reg, err := bootstrap.BuildRegistry(ctx, cfg, logger)
	if err != nil {
		return fmt.Errorf("mcphost: build registry: %w", err)
	}
	defer func() {
		for _, entry := range reg.List() {
			reg.Remove(entry.ID)
		}
	}()

	var stdioServer *gmcpserver.Server
	mux := http.NewServeMux()
	haveHTTP := false

	for _, ls := range cfg.LogicalServers {
		entry, ok := reg.Get(ls.ID)
		if !ok {
			return fmt.Errorf("mcphost: logical server %q missing from registry after build", ls.ID)
		}
		srv := serving.BuildMCPServer(entry)
		logger.Info("mcphost: logical server ready",
			"id", ls.ID, "name", ls.Name, "transport", string(ls.Transport), "tools", len(entry.Tools()))

		if ls.Serve == nil {
			logger.Warn("mcphost: logical server has no serve block; registered but not exposed", "id", ls.ID)
			continue
		}
		if ls.Serve.Stdio {
			stdioServer = srv
		}
		if ls.Serve.HTTP != nil {
			mux.Handle(ls.Serve.HTTP.Path, serving.MountHTTP(srv, ls.Serve.HTTP))
			haveHTTP = true
			logger.Info("mcphost: logical server exposed over HTTP", "id", ls.ID, "path", ls.Serve.HTTP.Path)
		}
	}

	var httpServer *http.Server
	if haveHTTP {
		httpServer = &http.Server{Addr: httpAddr, Handler: mux}
		go func() {
			logger.Info("mcphost: serving HTTP-exposed logical servers", "addr", httpAddr)
			if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				logger.Error("mcphost: http server failed", "err", err)
			}
		}()
	}

	if stdioServer == nil {
		logger.Info("mcphost: no logical server exposed over stdio; running until signaled")
		<-ctx.Done()
	} else {
		logger.Info("mcphost: serving stdio logical server")
		if err := stdioServer.Run(ctx); err != nil && ctx.Err() == nil {
			logger.Error("mcphost: stdio server failed", "err", err)
		}
	}

	if httpServer != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = httpServer.Shutdown(shutdownCtx)
		cancel()
	}

	return nil
}
