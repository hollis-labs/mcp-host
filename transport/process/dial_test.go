package process

import (
	"context"
	"testing"
	"time"

	"github.com/hollis-labs/mcp-host/config"
)

func TestDial_ListToolsAndCallTool(t *testing.T) {
	ts := newFixtureHTTPServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	tr, err := New(ctx, "echo-http", &config.ProcessConfig{URL: ts.URL, Transport: "http"}, testLogger())
	if err != nil {
		t.Fatalf("New: unexpected error: %v", err)
	}
	defer tr.(*dialTransport).Close()

	tools, err := tr.ListTools(ctx)
	if err != nil {
		t.Fatalf("ListTools: unexpected error: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "ping" {
		t.Fatalf("ListTools = %+v, want one tool named ping", tools)
	}

	res, err := tr.CallTool(ctx, "ping", map[string]any{"message": "hi"})
	if err != nil {
		t.Fatalf("CallTool: unexpected error: %v", err)
	}
	if res.IsError {
		t.Fatalf("CallTool result IsError=true: %+v", res)
	}
}

func TestDial_UnreachableURL_ErrorsOnCall(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	tr, err := New(ctx, "echo-http", &config.ProcessConfig{URL: "http://127.0.0.1:1/mcp", Transport: "http"}, testLogger())
	if err != nil {
		// Dial is lazy (go-mcp/client.Pool's own design), so New itself
		// should succeed even for an unreachable URL.
		t.Fatalf("New: unexpected error: %v", err)
	}
	defer tr.(*dialTransport).Close()

	if _, err := tr.ListTools(ctx); err == nil {
		t.Fatal("ListTools against unreachable URL: expected error, got nil")
	}
}
