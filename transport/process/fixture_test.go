package process

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"testing"

	gmcpserver "github.com/hollis-labs/go-mcp/server"
	httptransport "github.com/hollis-labs/go-mcp/transport/http"
)

// fixtureEnvVar, when set in the test process's environment, makes
// TestMain run a trivial standalone MCP server (one "ping" tool) instead
// of the test suite. spawn_test.go re-execs the test binary itself
// (os.Args[0]) with this var set, giving the spawn-mode tests a real,
// separate MCP server process without needing a separately built fixture
// binary.
const fixtureEnvVar = "STATION_TEST_FIXTURE_ECHO"

func TestMain(m *testing.M) {
	if os.Getenv(fixtureEnvVar) != "" {
		runFixtureServer()
		return
	}
	os.Exit(m.Run())
}

// newFixtureServer builds the same trivial "ping" tool server used by
// both the stdio fixture (runFixtureServer) and the HTTP dial tests
// (httptest.Server wrapping it directly, in-process).
func newFixtureServer() *gmcpserver.Server {
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
	return srv
}

// runFixtureServer serves newFixtureServer over stdio until the parent
// closes stdin, then exits. This is what a spawned "child" process
// actually runs when a spawn_test.go test sets cfg.Command to the test
// binary itself with fixtureEnvVar set.
func runFixtureServer() {
	srv := newFixtureServer()
	_ = srv.Run(context.Background())
	os.Exit(0)
}

// newFixtureHTTPServer starts an httptest.Server exposing newFixtureServer
// over go-mcp/transport/http, for the dial-mode (URL) tests.
func newFixtureHTTPServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := newFixtureServer()
	handler := httptransport.NewHandler(srv, httptransport.HandlerOptions{})
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	return ts
}
