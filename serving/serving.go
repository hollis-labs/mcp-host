// Package serving exposes a registered logical server (internal/registry.Entry)
// as its own addressable MCP endpoint over stdio and/or HTTP. It talks
// only to the registry.Transport interface, so it is agnostic to
// whether a given logical server is process-mode (T3) or inprocess-mode
// (T4) — exactly what the ADR's per-logical-server addressability
// requirement calls for. Per that same requirement, each logical server
// gets its own go-mcp server with its own name/tool list; there is no
// merged/mux'd endpoint across logical servers in v1.
package serving

import (
	"context"
	"net/http"
	"strings"

	"github.com/hollis-labs/go-mcp/auth"
	"github.com/hollis-labs/go-mcp/budget"
	gmcpserver "github.com/hollis-labs/go-mcp/server"
	httptransport "github.com/hollis-labs/go-mcp/transport/http"

	"github.com/hollis-labs/mcp-host/config"
	"github.com/hollis-labs/mcp-host/registry"
)

const identityVersion = "dev"

// BuildMCPServer builds a go-mcp/server.Server named after entry,
// exposing every tool entry.Tools() currently has cached. Each tool is
// bridged to a call against entry's own transport — the actual
// process/inprocess subprocess is never directly visible to the MCP
// client on the other end of this server. entry.DiscoverTools must have
// been called at least once before this (internal/host.BuildRegistry
// does this at startup for every logical server).
func BuildMCPServer(entry *registry.Entry) *gmcpserver.Server {
	instructions := entry.Description
	srv := gmcpserver.NewServer(entry.ID, identityVersion, gmcpserver.WithInstructions(instructions))
	for _, tool := range entry.Tools() {
		srv.RegisterTool(bridgeTool(entry, tool))
	}
	return srv
}

// bridgeTool adapts one discovered registry.Tool into a go-mcp/server.Tool
// registration whose handler forwards the call to entry's transport.
func bridgeTool(entry *registry.Entry, tool registry.Tool) gmcpserver.Tool {
	inputSchema := tool.InputSchema
	if inputSchema == nil {
		// The official SDK's AddTool panics on a nil schema (it requires
		// an explicit {"type":"object",...}, never infers one) — a
		// relayed tool whose own server declared no schema at all would
		// otherwise crash registration here rather than degrade to
		// "accepts no arguments," which is what an absent schema actually
		// means for a strict MCP server.
		inputSchema = gmcpserver.EmptyObjectSchema()
	}
	return gmcpserver.Tool{
		Name:        tool.Name,
		Description: tool.Description,
		InputSchema: inputSchema,

		// A relayed tool's own server may not have declared annotations at
		// all (nothing enforces it on that end the way go-mcp/server
		// enforces it on this one). Missing means unknown, and unknown
		// defaults to the conservative reading (false) on every hint —
		// never inferred as safe — for exactly the reason go-mcp/server's
		// own contract requires these fields explicitly in the first
		// place: a client gating approval on a false claim of
		// read-only/idempotent is the failure mode this guards against.
		ReadOnlyHint:    boolHint(tool.Annotations, "readOnlyHint"),
		DestructiveHint: boolHint(tool.Annotations, "destructiveHint"),
		IdempotentHint:  boolHint(tool.Annotations, "idempotentHint"),
		OpenWorldHint:   boolHint(tool.Annotations, "openWorldHint"),

		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			res, err := entry.CallTool(ctx, tool.Name, args)
			if err != nil {
				return nil, budget.NewToolError("upstream_unreachable", err.Error()).
					WithRetryable(true)
			}
			text := joinTextContent(res.Content)
			if res.IsError {
				return nil, budget.NewToolError("tool_error", text)
			}
			return text, nil
		},
	}
}

func boolHint(annotations map[string]any, key string) bool {
	v, ok := annotations[key]
	if !ok {
		return false
	}
	b, ok := v.(bool)
	return ok && b
}

func joinTextContent(content []registry.ToolContent) string {
	var sb strings.Builder
	for _, c := range content {
		if c.Type != "text" || c.Text == "" {
			continue
		}
		if sb.Len() > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString(c.Text)
	}
	return sb.String()
}

// MountHTTP wraps srv's Streamable HTTP handler with cfg's origin
// allowlist and, when cfg declares bearer tokens, go-mcp/auth's
// StaticProvider middleware. No tokens configured means no auth —
// go-mcp/auth's own "opt-in, never mandatory" default.
func MountHTTP(srv *gmcpserver.Server, cfg *config.HTTPServeConfig) http.Handler {
	handler := httptransport.NewHandler(srv, httptransport.HandlerOptions{
		AllowedOrigins: cfg.AllowedOrigins,
	})
	if len(cfg.BearerTokens) == 0 {
		return handler
	}
	tokens := make(map[string]string, len(cfg.BearerTokens))
	for _, tok := range cfg.BearerTokens {
		tokens[tok] = ""
	}
	provider := auth.NewStaticProvider(tokens)
	return auth.HTTPMiddleware(provider)(handler)
}
