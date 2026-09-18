package process

import (
	"context"
	"log/slog"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/hollis-labs/mcp-host/config"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func selfExecFixtureConfig() *config.ProcessConfig {
	return &config.ProcessConfig{
		Command: os.Args[0],
		Env:     map[string]string{fixtureEnvVar: "1"},
	}
}

func TestSpawn_ListToolsAndCallTool(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	tr, err := New(ctx, "echo", selfExecFixtureConfig(), testLogger())
	if err != nil {
		t.Fatalf("New: unexpected error: %v", err)
	}
	defer tr.(*SpawnTransport).Close()

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
	if len(res.Content) == 0 {
		t.Fatal("CallTool result has no content")
	}
}

func TestSpawn_InvalidCommand_FailsFast(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := New(ctx, "bogus", &config.ProcessConfig{Command: "/nonexistent/station-test-binary"}, testLogger())
	if err == nil {
		t.Fatal("New: expected error for nonexistent command, got nil")
	}
}

func TestSpawn_Close_StopsProcessAndIsIdempotent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	tr, err := New(ctx, "echo", selfExecFixtureConfig(), testLogger())
	if err != nil {
		t.Fatalf("New: unexpected error: %v", err)
	}
	st := tr.(*SpawnTransport)

	if err := st.Close(); err != nil {
		t.Fatalf("Close: unexpected error: %v", err)
	}
	// Idempotent second close must not hang or error.
	if err := st.Close(); err != nil {
		t.Fatalf("second Close: unexpected error: %v", err)
	}

	if _, err := st.ListTools(ctx); err == nil {
		t.Fatal("ListTools after Close: expected error, got nil")
	}
}

// TestSpawn_SupervisedRestart kills the child process out from under a
// live SpawnTransport and confirms go-mcp/supervise-driven recovery: the
// transport respawns per DefaultPolicy and becomes callable again.
func TestSpawn_SupervisedRestart(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	tr, err := New(ctx, "echo", selfExecFixtureConfig(), testLogger())
	if err != nil {
		t.Fatalf("New: unexpected error: %v", err)
	}
	st := tr.(*SpawnTransport)
	defer st.Close()

	if _, err := st.ListTools(ctx); err != nil {
		t.Fatalf("initial ListTools: unexpected error: %v", err)
	}

	pid := st.Pid()
	if pid == 0 {
		t.Fatal("Pid() = 0, want a live process")
	}
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatalf("kill child (pid %d): %v", pid, err)
	}

	deadline := time.Now().Add(10 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		if st.RestartCount() > 0 {
			if _, err := st.ListTools(ctx); err == nil {
				return // recovered
			} else {
				lastErr = err
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("transport did not recover after kill within deadline; restarts=%d lastErr=%v", st.RestartCount(), lastErr)
}

func TestSpawn_SuperviseDisabled_DoesNotRestart(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	disabled := false
	cfg := selfExecFixtureConfig()
	cfg.Supervise = &disabled

	tr, err := New(ctx, "echo", cfg, testLogger())
	if err != nil {
		t.Fatalf("New: unexpected error: %v", err)
	}
	st := tr.(*SpawnTransport)
	defer st.Close()

	pid := st.Pid()
	if pid == 0 {
		t.Fatal("Pid() = 0, want a live process")
	}
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatalf("kill child (pid %d): %v", pid, err)
	}

	// Give the supervision goroutine time to notice the exit and confirm
	// it does NOT restart.
	time.Sleep(1 * time.Second)
	if st.RestartCount() != 0 {
		t.Fatalf("RestartCount() = %d, want 0 with supervision disabled", st.RestartCount())
	}
	if _, err := st.ListTools(ctx); err == nil {
		t.Fatal("ListTools after unsupervised crash: expected error, got nil")
	}
}
