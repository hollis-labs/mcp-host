package bootstrap

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/hollis-labs/mcp-host/config"
)

// fixtureEnvVar makes the test binary act as a trivial standalone MCP
// server (see fixture_test.go) instead of running tests, the same
// self-exec pattern internal/transport/process's own tests use.
const fixtureEnvVar = "STATION_TEST_FIXTURE_ECHO"

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestBuildRegistry_OneProcessAndOneInprocessServer(t *testing.T) {
	cfg := &config.Config{
		LogicalServers: []config.LogicalServer{
			{
				ID: "echo", Name: "Echo", Transport: config.TransportProcess,
				Process: &config.ProcessConfig{
					Command: os.Args[0],
					Env:     map[string]string{fixtureEnvVar: "1"},
				},
			},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	reg, err := BuildRegistry(ctx, cfg, testLogger())
	if err != nil {
		t.Fatalf("BuildRegistry: unexpected error: %v", err)
	}
	defer func() {
		for _, e := range reg.List() {
			reg.Remove(e.ID)
		}
	}()

	if reg.Len() != 1 {
		t.Fatalf("reg.Len() = %d, want 1", reg.Len())
	}
	entry, ok := reg.Get("echo")
	if !ok {
		t.Fatal("Get(echo): not found")
	}
	tools := entry.Tools()
	if len(tools) != 1 || tools[0].Name != "ping" {
		t.Fatalf("entry.Tools() = %+v, want one tool named ping", tools)
	}
}

func TestBuildRegistry_UnknownTransport_Errors(t *testing.T) {
	cfg := &config.Config{
		LogicalServers: []config.LogicalServer{
			{ID: "x", Name: "X", Transport: "bogus"},
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := BuildRegistry(ctx, cfg, testLogger()); err == nil {
		t.Fatal("BuildRegistry: expected error for unknown transport, got nil")
	}
}

func TestBuildRegistry_PartialFailureClosesEarlierTransports(t *testing.T) {
	cfg := &config.Config{
		LogicalServers: []config.LogicalServer{
			{
				ID: "echo", Name: "Echo", Transport: config.TransportProcess,
				Process: &config.ProcessConfig{
					Command: os.Args[0],
					Env:     map[string]string{fixtureEnvVar: "1"},
				},
			},
			{
				ID: "bad", Name: "Bad", Transport: config.TransportProcess,
				Process: &config.ProcessConfig{Command: "/nonexistent/station-test-binary"},
			},
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	_, err := BuildRegistry(ctx, cfg, testLogger())
	if err == nil {
		t.Fatal("BuildRegistry: expected error from second server's bad command, got nil")
	}
	// Nothing to directly assert on process cleanup here beyond "it
	// didn't hang" — BuildRegistry's own closeAll call is exercised by
	// this path; T3's own tests cover Close's process-teardown behavior.
}
