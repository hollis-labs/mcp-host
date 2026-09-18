package process

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"sort"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/hollis-labs/go-mcp/supervise"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hollis-labs/mcp-host/config"
	"github.com/hollis-labs/mcp-host/registry"
)

const (
	identityName    = "mcp-host"
	identityVersion = "dev"

	// connectTimeout bounds the initial MCP handshake when the caller's
	// context carries no deadline of its own.
	connectTimeout = 30 * time.Second

	// shutdownGrace mirrors the MCP stdio shutdown sequence's own
	// default: how long to wait after closing stdin (or after a dead
	// process's exit is detected) before escalating to SIGTERM, and
	// again before escalating to SIGKILL.
	shutdownGrace = 5 * time.Second
)

// SpawnTransport backs a process-mode logical server whose config sets
// Command: mcp-host spawns and owns the subprocess directly (NOT via
// go-mcp/client.Pool's internal stdio dial) so that it can genuinely
// supervise it with go-mcp/supervise -- Pool's stdio dial is deliberately
// reactive-only (it never exposes the spawned *exec.Cmd, so nothing
// outside it could ever call ClassifyExit on a real os.ProcessState) and
// its own doc comment says as much: "see the package doc for why this
// shape, not a proactive supervisor." A real restart loop needs to own
// the process to reap it and classify its exit, so this type does that
// itself: one dedicated goroutine per generation calls the MCP session's
// Wait (which unblocks the moment the connection closes -- on an
// explicit Close, or the instant a dead subprocess's stdout hits EOF),
// then reaps the process via cmd.Wait for supervise.ClassifyExit, then
// -- if supervision is enabled -- respawns per supervise.Policy's
// backoff schedule.
type SpawnTransport struct {
	name             string
	command          string
	args             []string
	env              []string
	policy           supervise.Policy
	superviseEnabled bool
	logger           *slog.Logger

	mu      sync.RWMutex
	cmd     *exec.Cmd
	session *sdkmcp.ClientSession
	tail    *supervise.Tail
	closed  bool

	stopCh   chan struct{}
	doneCh   chan struct{} // closed when the supervision goroutine returns
	restarts atomic.Int64
}

func newSpawnTransport(ctx context.Context, name string, cfg *config.ProcessConfig, logger *slog.Logger) (*SpawnTransport, error) {
	t := &SpawnTransport{
		name:             name,
		command:          cfg.Command,
		args:             append([]string(nil), cfg.Args...),
		env:              buildEnv(cfg.Env),
		policy:           supervise.DefaultPolicy(),
		superviseEnabled: cfg.SuperviseEnabled(),
		logger:           logger,
		stopCh:           make(chan struct{}),
		doneCh:           make(chan struct{}),
	}

	cmd, session, err := t.spawnOnce(ctx)
	if err != nil {
		close(t.doneCh)
		return nil, fmt.Errorf("process transport %q: initial spawn: %w", name, err)
	}
	t.setLive(cmd, session)

	go t.superviseLoop(cmd, session)
	return t, nil
}

// RestartCount reports how many times this transport has respawned its
// subprocess since construction. Exposed for tests and diagnostics.
func (t *SpawnTransport) RestartCount() int64 { return t.restarts.Load() }

// Pid returns the current generation's process ID, or 0 if no process
// is currently live (down, restarting, or closed). Exposed for tests
// that need to simulate a crash by signaling the real process.
func (t *SpawnTransport) Pid() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if t.cmd == nil || t.cmd.Process == nil {
		return 0
	}
	return t.cmd.Process.Pid
}

func (t *SpawnTransport) ListTools(ctx context.Context) ([]registry.Tool, error) {
	session := t.liveSession()
	if session == nil {
		return nil, fmt.Errorf("process transport %q: not connected", t.name)
	}
	res, err := session.ListTools(ctx, &sdkmcp.ListToolsParams{})
	if err != nil {
		return nil, fmt.Errorf("process transport %q: tools/list: %w", t.name, err)
	}
	return convertSDKTools(res.Tools)
}

func (t *SpawnTransport) CallTool(ctx context.Context, name string, arguments map[string]any) (*registry.ToolResult, error) {
	session := t.liveSession()
	if session == nil {
		return nil, fmt.Errorf("process transport %q: not connected", t.name)
	}
	res, err := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		return nil, fmt.Errorf("process transport %q: tools/call %s: %w", t.name, name, err)
	}
	return convertSDKCallResult(res), nil
}

// Close stops supervision and tears the current generation down.
// Idempotent.
func (t *SpawnTransport) Close() error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	t.closed = true
	close(t.stopCh)
	session := t.session
	t.mu.Unlock()

	if session != nil {
		_ = session.Close()
	}
	<-t.doneCh
	return nil
}

// superviseLoop owns generations 1..N (generation 0 was already spawned
// synchronously by newSpawnTransport, and is handed in). Each iteration
// waits for the live session to close, reaps the process for
// ClassifyExit, and -- unless Close was called or supervision is
// disabled -- respawns per policy.
func (t *SpawnTransport) superviseLoop(cmd *exec.Cmd, session *sdkmcp.ClientSession) {
	defer close(t.doneCh)
	attempt := 0

	for {
		startedAt := time.Now()
		waitErr := session.Wait()
		exit := t.reap(cmd)
		t.clearLive()

		t.logger.Warn("station: process exited",
			"server", t.name,
			"kind", string(exit.Kind),
			"code", exit.Code,
			"signal", exit.Signal,
			"session_err", errString(waitErr),
			"stderr_tail", t.tailString(),
		)

		select {
		case <-t.stopCh:
			return
		default:
		}

		if !t.superviseEnabled {
			t.logger.Info("station: supervision disabled, not restarting", "server", t.name)
			return
		}

		if t.policy.StableFor > 0 && time.Since(startedAt) >= t.policy.StableFor {
			attempt = 0
		}

		var respawnErr error
		cmd, session, respawnErr = t.respawnWithBackoff(&attempt)
		if respawnErr != nil {
			t.logger.Error("station: giving up restarting process", "server", t.name, "err", respawnErr)
			return
		}
		if cmd == nil {
			// stopCh fired while waiting out backoff or retrying a
			// failed respawn.
			return
		}
		t.setLive(cmd, session)
	}
}

// respawnWithBackoff waits out the policy's backoff delay for *attempt,
// then spawns. A failed spawn attempt consumes another slot in the
// policy and retries, until the policy is exhausted (non-nil error) or
// Close stops the transport (all-nil return).
func (t *SpawnTransport) respawnWithBackoff(attempt *int) (*exec.Cmd, *sdkmcp.ClientSession, error) {
	for {
		delay, ok := t.policy.Next(*attempt)
		if !ok {
			return nil, nil, fmt.Errorf("restart policy exhausted after %d attempts", t.policy.Limit())
		}
		*attempt++
		t.restarts.Add(1)

		select {
		case <-time.After(delay):
		case <-t.stopCh:
			return nil, nil, nil
		}

		cmd, session, err := t.spawnOnce(context.Background())
		if err == nil {
			return cmd, session, nil
		}
		t.logger.Error("station: process respawn failed", "server", t.name, "attempt", *attempt, "err", err)

		select {
		case <-t.stopCh:
			return nil, nil, nil
		default:
		}
	}
}

// spawnOnce starts the subprocess and performs the MCP handshake over
// its stdin/stdout, using the SDK's IOTransport with pipes mcp-host owns
// directly (rather than mcpsdk.CommandTransport, which would call
// cmd.Start itself and never hand back the *exec.Cmd) -- ownership of
// cmd is exactly what superviseLoop/reap need for ClassifyExit.
func (t *SpawnTransport) spawnOnce(ctx context.Context) (*exec.Cmd, *sdkmcp.ClientSession, error) {
	cmd := exec.Command(t.command, t.args...)
	cmd.Env = t.env
	tail := &supervise.Tail{}
	cmd.Stderr = tail

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("stdout pipe: %w", err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("stdin pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, fmt.Errorf("start: %w", err)
	}

	t.mu.Lock()
	t.tail = tail
	t.mu.Unlock()

	connectCtx := ctx
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		connectCtx, cancel = context.WithTimeout(ctx, connectTimeout)
		defer cancel()
	}

	sdkClient := sdkmcp.NewClient(&sdkmcp.Implementation{Name: identityName, Version: identityVersion}, nil)
	transport := &sdkmcp.IOTransport{Reader: stdout, Writer: stdin}
	session, err := sdkClient.Connect(connectCtx, transport, nil)
	if err != nil {
		killAndReap(cmd)
		return nil, nil, fmt.Errorf("connect: %w", err)
	}
	return cmd, session, nil
}

// reap blocks until cmd's process has actually exited and been waited
// on, escalating to SIGTERM then SIGKILL if it doesn't exit promptly
// after its connection already closed (mirrors the MCP stdio shutdown
// sequence). Must be called at most once per cmd, and only after the
// session's Wait has already returned (so nothing is still reading the
// stdout pipe cmd.Wait needs to reclaim).
func (t *SpawnTransport) reap(cmd *exec.Cmd) supervise.Exit {
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(shutdownGrace):
		if cmd.Process != nil {
			_ = cmd.Process.Signal(syscall.SIGTERM)
		}
		select {
		case <-done:
		case <-time.After(shutdownGrace):
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
			<-done
		}
	}
	return supervise.ClassifyExit(cmd.ProcessState, time.Now())
}

// killAndReap is used only when spawnOnce's own MCP handshake fails
// (the process started but never became a usable session) -- there was
// no live session to close, so this skips straight to kill+wait rather
// than reap's closed-stdin-first sequence.
func killAndReap(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	_ = cmd.Wait()
}

func (t *SpawnTransport) setLive(cmd *exec.Cmd, session *sdkmcp.ClientSession) {
	t.mu.Lock()
	t.cmd = cmd
	t.session = session
	t.mu.Unlock()
	pid := 0
	if cmd.Process != nil {
		pid = cmd.Process.Pid
	}
	t.logger.Info("station: process spawned", "server", t.name, "pid", pid)
}

func (t *SpawnTransport) clearLive() {
	t.mu.Lock()
	t.cmd = nil
	t.session = nil
	t.mu.Unlock()
}

func (t *SpawnTransport) liveSession() *sdkmcp.ClientSession {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.session
}

func (t *SpawnTransport) tailString() string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if t.tail == nil {
		return ""
	}
	return t.tail.String()
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// buildEnv inherits the host environment and appends cfg's declared
// vars last (sorted, for reproducible ordering), so a per-server
// override wins over an inherited value with the same key. mcp-host has
// no equivalent of Nanite's env-allowlist discipline (S4b) -- it is a
// single-purpose daemon the operator configures directly, not a
// multi-tenant agent host mediating untrusted plugin installs.
func buildEnv(env map[string]string) []string {
	base := os.Environ()
	if len(env) == 0 {
		return base
	}
	extra := make([]string, 0, len(env))
	for k, v := range env {
		extra = append(extra, k+"="+v)
	}
	sort.Strings(extra)
	return append(base, extra...)
}
