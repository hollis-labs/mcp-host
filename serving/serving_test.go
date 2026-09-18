package serving

import (
	"context"
	"errors"
	"testing"

	"github.com/hollis-labs/go-mcp/budget"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hollis-labs/mcp-host/config"
	"github.com/hollis-labs/mcp-host/registry"
)

// fakeTransport is the same shape registry's own tests use — no real
// process or RPC involved.
type fakeTransport struct {
	tools   []registry.Tool
	results map[string]*registry.ToolResult
	callErr error
}

func (f *fakeTransport) ListTools(ctx context.Context) ([]registry.Tool, error) {
	return f.tools, nil
}

func (f *fakeTransport) CallTool(ctx context.Context, name string, arguments map[string]any) (*registry.ToolResult, error) {
	if f.callErr != nil {
		return nil, f.callErr
	}
	if res, ok := f.results[name]; ok {
		return res, nil
	}
	return &registry.ToolResult{Content: []registry.ToolContent{{Type: "text", Text: "default"}}}, nil
}

func buildTestEntry(t *testing.T, ft *fakeTransport) *registry.Entry {
	t.Helper()
	reg := registry.New()
	entry, err := reg.Add("echo", "Echo Server", "a test server", ft)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if _, err := entry.DiscoverTools(context.Background()); err != nil {
		t.Fatalf("DiscoverTools: %v", err)
	}
	return entry
}

func TestBuildMCPServer_RegistersDiscoveredTools(t *testing.T) {
	ft := &fakeTransport{tools: []registry.Tool{
		{Name: "ping", Description: "pings"},
		{Name: "pong", Description: "pongs", Annotations: map[string]any{"readOnlyHint": true, "idempotentHint": true}},
	}}
	entry := buildTestEntry(t, ft)
	srv := BuildMCPServer(entry)

	defs := srv.ToolDefinitions()
	if len(defs) != 2 {
		t.Fatalf("ToolDefinitions() len = %d, want 2", len(defs))
	}
	if defs[0].Name != "ping" || defs[1].Name != "pong" {
		t.Fatalf("ToolDefinitions() = %+v, want sorted [ping pong]", defs)
	}

	// Missing annotations default to false, never inferred as safe.
	if defs[0].Annotations.ReadOnlyHint {
		t.Error("ping (no annotations) ReadOnlyHint = true, want false (unknown defaults false)")
	}
	// Present annotations are honored.
	if !defs[1].Annotations.ReadOnlyHint || !defs[1].Annotations.IdempotentHint {
		t.Errorf("pong annotations = %+v, want ReadOnlyHint and IdempotentHint true", defs[1].Annotations)
	}
	if defs[1].Annotations.DestructiveHint {
		t.Error("pong DestructiveHint = true, want false (not declared)")
	}
}

func TestBridgeTool_SuccessReturnsJoinedText(t *testing.T) {
	ft := &fakeTransport{
		tools: []registry.Tool{{Name: "greet"}},
		results: map[string]*registry.ToolResult{
			"greet": {Content: []registry.ToolContent{
				{Type: "text", Text: "hello"},
				{Type: "text", Text: "world"},
			}},
		},
	}
	entry := buildTestEntry(t, ft)
	srv := BuildMCPServer(entry)

	result, err := srv.CallTool(context.Background(), "greet", map[string]any{})
	if err != nil {
		t.Fatalf("CallTool: unexpected error: %v", err)
	}
	text, ok := result.(string)
	if !ok {
		t.Fatalf("CallTool result type = %T, want string", result)
	}
	if text != "hello\nworld" {
		t.Errorf("CallTool result = %q, want %q", text, "hello\nworld")
	}
}

func TestBridgeTool_ToolErrorBecomesStructuredError(t *testing.T) {
	ft := &fakeTransport{
		tools: []registry.Tool{{Name: "fail"}},
		results: map[string]*registry.ToolResult{
			"fail": {IsError: true, Content: []registry.ToolContent{{Type: "text", Text: "boom"}}},
		},
	}
	entry := buildTestEntry(t, ft)
	srv := BuildMCPServer(entry)

	_, err := srv.CallTool(context.Background(), "fail", map[string]any{})
	if err == nil {
		t.Fatal("CallTool: expected error, got nil")
	}
	var toolErr *budget.ToolError
	if !errors.As(err, &toolErr) {
		t.Fatalf("CallTool error = %v (%T), want *budget.ToolError", err, err)
	}
	if toolErr.Message != "boom" {
		t.Errorf("toolErr.Message = %q, want %q", toolErr.Message, "boom")
	}
}

func TestBridgeTool_TransportErrorBecomesStructuredError(t *testing.T) {
	ft := &fakeTransport{
		tools:   []registry.Tool{{Name: "unreachable"}},
		callErr: errors.New("connection reset"),
	}
	entry := buildTestEntry(t, ft)
	srv := BuildMCPServer(entry)

	_, err := srv.CallTool(context.Background(), "unreachable", map[string]any{})
	if err == nil {
		t.Fatal("CallTool: expected error, got nil")
	}
	var toolErr *budget.ToolError
	if !errors.As(err, &toolErr) {
		t.Fatalf("CallTool error = %v (%T), want *budget.ToolError", err, err)
	}
	if !toolErr.Retryable {
		t.Error("toolErr.Retryable = false, want true for a transport-level failure")
	}
}

// TestBuildMCPServer_FullProtocolRoundTrip proves BuildMCPServer's
// result is a genuine, protocol-correct MCP server: a real
// mcpsdk.Client, connected over an in-memory transport (no stdio/http
// involved), performs a real tools/list + tools/call round trip.
func TestBuildMCPServer_FullProtocolRoundTrip(t *testing.T) {
	ft := &fakeTransport{
		tools: []registry.Tool{{Name: "ping", Description: "pings", Annotations: map[string]any{"readOnlyHint": true}}},
		results: map[string]*registry.ToolResult{
			"ping": {Content: []registry.ToolContent{{Type: "text", Text: "pong"}}},
		},
	}
	entry := buildTestEntry(t, ft)
	srv := BuildMCPServer(entry)

	ctx := context.Background()
	serverTransport, clientTransport := sdkmcp.NewInMemoryTransports()

	serverSession, err := srv.SDKServer().Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("server Connect: %v", err)
	}
	defer serverSession.Close()

	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "test-client", Version: "1"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client Connect: %v", err)
	}
	defer clientSession.Close()

	listRes, err := clientSession.ListTools(ctx, &sdkmcp.ListToolsParams{})
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(listRes.Tools) != 1 || listRes.Tools[0].Name != "ping" {
		t.Fatalf("ListTools = %+v, want one tool named ping", listRes.Tools)
	}
	if listRes.Tools[0].Annotations == nil || !listRes.Tools[0].Annotations.ReadOnlyHint {
		t.Errorf("ping annotations = %+v, want ReadOnlyHint true", listRes.Tools[0].Annotations)
	}

	callRes, err := clientSession.CallTool(ctx, &sdkmcp.CallToolParams{Name: "ping", Arguments: map[string]any{}})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if callRes.IsError {
		t.Fatalf("CallTool result IsError=true: %+v", callRes)
	}
	if len(callRes.Content) != 1 {
		t.Fatalf("CallTool content = %+v, want 1 block", callRes.Content)
	}
	text, ok := callRes.Content[0].(*sdkmcp.TextContent)
	if !ok || text.Text != "pong" {
		t.Fatalf("CallTool content[0] = %+v, want text %q", callRes.Content[0], "pong")
	}
}

func TestMountHTTP_NoTokensIsPassthrough(t *testing.T) {
	ft := &fakeTransport{tools: []registry.Tool{{Name: "ping"}}}
	entry := buildTestEntry(t, ft)
	srv := BuildMCPServer(entry)

	handler := MountHTTP(srv, &config.HTTPServeConfig{Path: "/mcp"})
	if handler == nil {
		t.Fatal("MountHTTP returned nil handler")
	}
}

func TestMountHTTP_WithTokensWrapsAuth(t *testing.T) {
	ft := &fakeTransport{tools: []registry.Tool{{Name: "ping"}}}
	entry := buildTestEntry(t, ft)
	srv := BuildMCPServer(entry)

	handler := MountHTTP(srv, &config.HTTPServeConfig{Path: "/mcp", BearerTokens: []string{"secret"}})
	if handler == nil {
		t.Fatal("MountHTTP returned nil handler")
	}
}
