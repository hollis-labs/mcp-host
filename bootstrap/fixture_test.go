package bootstrap

import (
	"context"
	"fmt"
	"os"
	"testing"

	gmcpserver "github.com/hollis-labs/go-mcp/server"
)

// TestMain makes the test binary act as a trivial standalone MCP server
// (one "ping" tool, over stdio) when fixtureEnvVar is set, instead of
// running the test suite — the same self-exec pattern
// internal/transport/process's own tests use, duplicated here since
// test-only symbols aren't importable across packages.
func TestMain(m *testing.M) {
	if os.Getenv(fixtureEnvVar) != "" {
		runFixtureServer()
		return
	}
	os.Exit(m.Run())
}

func runFixtureServer() {
	srv := gmcpserver.NewServer("station-fixture", "test")
	srv.RegisterTool(gmcpserver.Tool{
		Name:            "ping",
		Description:     "Echoes back the given message.",
		InputSchema:     gmcpserver.InputSchema(gmcpserver.StringProp("message", "message to echo", false)),
		ReadOnlyHint:    true,
		DestructiveHint: false,
		IdempotentHint:  true,
		OpenWorldHint:   false,
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			msg, _ := args["message"].(string)
			return map[string]any{"pong": fmt.Sprintf("pong:%s", msg)}, nil
		},
	})
	_ = srv.Run(context.Background())
	os.Exit(0)
}
