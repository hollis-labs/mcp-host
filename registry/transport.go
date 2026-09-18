// Package registry holds mcp-host's internal transport interface and the
// logical-server registry both plugin modes (process, inprocess) plug
// into. It is deliberately decoupled from Nanite's internal/mcp package:
// no uniform tool naming, no trust tiers, no first-party-builtin
// concept. Each logical server keeps its own identity and tool set,
// addressable independently — this host does not merge tools into one
// namespace (see the ADR's per-logical-server addressability
// requirement).
package registry

import "context"

// Tool describes one tool a logical server's transport advertises via
// tools/list.
type Tool struct {
	Name        string
	Description string
	InputSchema map[string]any
	// Annotations are the MCP spec's behavior hints (readOnlyHint,
	// destructiveHint, idempotentHint, openWorldHint).
	Annotations map[string]any
}

// ToolResult is the result of a tools/call invocation.
type ToolResult struct {
	Content []ToolContent
	IsError bool
}

// ToolContent is one content block in a ToolResult.
type ToolContent struct {
	Type string // "text", "image", "resource"
	Text string
}

// Transport is the interface both plugin modes implement: process (T3,
// a real standalone MCP server reached via go-mcp/client) and inprocess
// (T4, a plugin-sdk subprocess the daemon drives directly). The
// registry and serving layer talk to a logical server only through this
// interface — neither cares which mode backs a given server.
type Transport interface {
	ListTools(ctx context.Context) ([]Tool, error)
	CallTool(ctx context.Context, name string, arguments map[string]any) (*ToolResult, error)
}
