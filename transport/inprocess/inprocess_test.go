package inprocess

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

func fixtureTools() []config.ToolManifest {
	return []config.ToolManifest{{
		Name:        "ping",
		Description: "Echoes back the given message.",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"message": map[string]any{"type": "string"}},
		},
		Annotations: map[string]any{"readOnlyHint": true, "idempotentHint": true},
	}}
}

func selfExecFixtureConfig(extraEnv map[string]string) *config.InprocessConfig {
	env := map[string]string{fixtureEnvVar: "1"}
	for k, v := range extraEnv {
		env[k] = v
	}
	return &config.InprocessConfig{
		Command: os.Args[0],
		Env:     env,
		Tools:   fixtureTools(),
	}
}

func TestNew_HandshakeListToolsAndCallTool(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	tr, err := New(ctx, "clock", selfExecFixtureConfig(nil), testLogger())
	if err != nil {
		t.Fatalf("New: unexpected error: %v", err)
	}
	defer tr.Close()

	tools, err := tr.ListTools(ctx)
	if err != nil {
		t.Fatalf("ListTools: unexpected error: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "ping" {
		t.Fatalf("ListTools = %+v, want one tool named ping", tools)
	}
	if !tools[0].Annotations["readOnlyHint"].(bool) {
		t.Errorf("tools[0].Annotations = %+v, want readOnlyHint true (from the manifest, not a live call)", tools[0].Annotations)
	}

	res, err := tr.CallTool(ctx, "ping", map[string]any{"message": "hi"})
	if err != nil {
		t.Fatalf("CallTool: unexpected error: %v", err)
	}
	if res.IsError {
		t.Fatalf("CallTool result IsError=true: %+v", res)
	}
	if len(res.Content) != 1 || res.Content[0].Text == "" {
		t.Fatalf("CallTool result = %+v, want non-empty text content", res)
	}
}

func TestNew_InvalidCommand_FailsFast(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := New(ctx, "bogus", &config.InprocessConfig{Command: "/nonexistent/station-test-plugin"}, testLogger())
	if err == nil {
		t.Fatal("New: expected error for nonexistent command, got nil")
	}
}

func TestClose_StopsProcessAndIsIdempotent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	tr, err := New(ctx, "clock", selfExecFixtureConfig(nil), testLogger())
	if err != nil {
		t.Fatalf("New: unexpected error: %v", err)
	}

	if err := tr.Close(); err != nil {
		t.Fatalf("Close: unexpected error: %v", err)
	}
	if err := tr.Close(); err != nil {
		t.Fatalf("second Close: unexpected error: %v", err)
	}

	// ListTools is manifest-driven, not live — it must keep answering
	// from config even after Close, the same way a process-mode server's
	// advertised name doesn't depend on an open connection.
	tools, err := tr.ListTools(ctx)
	if err != nil {
		t.Fatalf("ListTools after Close: unexpected error: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "ping" {
		t.Fatalf("ListTools after Close = %+v, want one tool named ping", tools)
	}

	if _, err := tr.CallTool(ctx, "ping", nil); err == nil {
		t.Fatal("CallTool after Close: expected error, got nil")
	}
}

// TestSupervisedRestart_OnCrash kills the plugin subprocess out from
// under a live Transport and confirms go-mcp/supervise-driven recovery
// (the reactive path — rpcTransport.Done fires the moment the dead
// process's stdout hits EOF), mirroring T3's process-mode restart test.
func TestSupervisedRestart_OnCrash(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	tr, err := New(ctx, "clock", selfExecFixtureConfig(nil), testLogger())
	if err != nil {
		t.Fatalf("New: unexpected error: %v", err)
	}
	defer tr.Close()

	if _, err := tr.CallTool(ctx, "ping", map[string]any{"message": "hi"}); err != nil {
		t.Fatalf("initial CallTool: unexpected error: %v", err)
	}

	pid := tr.Pid()
	if pid == 0 {
		t.Fatal("Pid() = 0, want a live process")
	}
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatalf("kill plugin (pid %d): %v", pid, err)
	}

	// Recovery is checked via CallTool, not ListTools — ListTools is
	// manifest-driven now and would report success (and the same tool
	// list) whether or not the subprocess is actually alive.
	deadline := time.Now().Add(10 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		if tr.RestartCount() > 0 {
			if _, err := tr.CallTool(ctx, "ping", map[string]any{"message": "hi"}); err == nil {
				return // recovered
			} else {
				lastErr = err
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("transport did not recover after kill within deadline; restarts=%d lastErr=%v", tr.RestartCount(), lastErr)
}

// TestSupervisedRestart_OnUnhealthy proves the proactive path: a plugin
// that stays alive but starts failing plugin/health gets killed and
// restarted by the periodic health poll, not just by a crash.
func TestSupervisedRestart_OnUnhealthy(t *testing.T) {
	origInterval, origTimeout := healthCheckInterval, healthCheckTimeout
	healthCheckInterval = 200 * time.Millisecond
	healthCheckTimeout = 2 * time.Second
	t.Cleanup(func() {
		healthCheckInterval, healthCheckTimeout = origInterval, origTimeout
	})

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Fail every health check from the start (unhealthyAfter=0).
	tr, err := New(ctx, "clock", selfExecFixtureConfig(map[string]string{fixtureUnhealthyAfterVar: "0"}), testLogger())
	if err != nil {
		t.Fatalf("New: unexpected error: %v", err)
	}
	defer tr.Close()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if tr.RestartCount() > 0 {
			return // the health poll forced a restart
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("transport did not restart on unhealthy status within deadline; restarts=%d", tr.RestartCount())
}

func TestSuperviseDisabled_DoesNotRestart(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cfg := selfExecFixtureConfig(nil)
	disabled := false
	cfg.Supervise = &disabled

	tr, err := New(ctx, "clock", cfg, testLogger())
	if err != nil {
		t.Fatalf("New: unexpected error: %v", err)
	}
	defer tr.Close()

	pid := tr.Pid()
	if pid == 0 {
		t.Fatal("Pid() = 0, want a live process")
	}
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatalf("kill plugin (pid %d): %v", pid, err)
	}

	time.Sleep(1 * time.Second)
	if tr.RestartCount() != 0 {
		t.Fatalf("RestartCount() = %d, want 0 with supervision disabled", tr.RestartCount())
	}
	if _, err := tr.CallTool(ctx, "ping", nil); err == nil {
		t.Fatal("CallTool after unsupervised crash: expected error, got nil")
	}
}
