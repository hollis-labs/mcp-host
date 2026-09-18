// Package inprocess implements the `inprocess` transport mode: a
// logical server backed by a subprocess plugin speaking
// plugin-sdk/subprocess's JSON-RPC dialect. mcp-host performs the one
// real MCP handshake on the plugin's behalf; the plugin itself never
// implements MCP, only plugin/init, plugin/load, plugin/health, and
// mcp/call_tool.
//
// Tool discovery is manifest-driven, never live. config.InprocessConfig.Tools
// declares the plugin's full tool catalog statically, and this package
// never sends mcp/list_tools to the subprocess — ListTools just returns
// what the config already said. Two independent things point the same
// way here: plugin-sdk's own subprocess.Serve has no dispatch case for
// mcp/list_tools at all (verified against v0.5.0's server.go — every
// other documented method has one), so a plugin built the documented
// way (Serve + capability interfaces) cannot answer it regardless of
// what a host does; and Tangent's own plugin host (a separate app in
// this portfolio, also built on plugin-sdk) went further and says so
// explicitly in its own manifest package doc: it deliberately never
// calls mcp/list_tools, "because Nanite built runtime self-declaration
// and discarded it." Aligning with that verified, considered precedent
// — not working around a gap — is the design here. It also means a
// plugin can go back to using plugin-sdk's own subprocess.Serve exactly
// as documented (Plugin + MCPHandler, optionally HealthChecker): the
// one RPC method Serve can't answer is one this package never asks for.
//
// v1 assumes one plugin subprocess backs exactly one logical server —
// not the N-servers-per-plugin multiplexing Nanite's PluginMCPTransport
// supports via a "server" field on every call. Nothing in this
// portfolio yet needs an inprocess plugin to expose more than one
// logical server, and the simpler mapping is what the original task
// description recommends absent a concrete need. One consequence: for
// mcp/call_tool, this uses plugin-sdk's own canonical wire types
// (sdksub.MCPCallRequest/MCPCallResult) directly rather than Nanite's
// local Server-keyed shape, since there is no second server to
// disambiguate against.
package inprocess

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
	sdksub "github.com/hollis-labs/plugin-sdk/subprocess"

	"github.com/hollis-labs/mcp-host/config"
	"github.com/hollis-labs/mcp-host/registry"
)

const (
	identityVersion = "dev"

	connectTimeout = 30 * time.Second
	shutdownGrace  = 5 * time.Second
)

// healthCheckInterval/healthCheckTimeout drive a proactive plugin/health
// poll on top of the reactive "pipe closed" detection rpcTransport.Done
// already provides — a hung-but-still-running plugin process wouldn't
// otherwise be noticed. Package-level vars (not consts), mutable for
// testing, matching the official SDK's own defaultTerminateDuration
// pattern — a 30s interval is impractical to wait out in a unit test.
var (
	healthCheckInterval = 30 * time.Second
	healthCheckTimeout  = 5 * time.Second
)

// Transport backs an inprocess-mode logical server. It owns the plugin
// subprocess directly (spawn, pipes, stderr tail) for the same reason
// T3's process.SpawnTransport does: genuine supervision needs the
// os.ProcessState only an owning Wait can produce for
// supervise.ClassifyExit.
type Transport struct {
	name             string
	command          string
	args             []string
	env              []string
	tools            []registry.Tool
	policy           supervise.Policy
	superviseEnabled bool
	logger           *slog.Logger

	mu     sync.RWMutex
	cmd    *exec.Cmd
	rpc    *rpcTransport
	tail   *supervise.Tail
	closed bool

	stopCh   chan struct{}
	doneCh   chan struct{}
	restarts atomic.Int64
}

// New spawns the plugin subprocess named by cfg, performs the
// plugin/init + plugin/load handshake, and — once live — starts the
// supervision loop. ctx bounds the initial spawn and handshake only.
func New(ctx context.Context, name string, cfg *config.InprocessConfig, logger *slog.Logger) (*Transport, error) {
	if cfg == nil {
		return nil, fmt.Errorf("inprocess transport %q: nil config", name)
	}
	if logger == nil {
		logger = slog.Default()
	}

	t := &Transport{
		name:             name,
		command:          cfg.Command,
		args:             append([]string(nil), cfg.Args...),
		env:              buildEnv(cfg.Env),
		tools:            manifestTools(cfg.Tools),
		policy:           supervise.DefaultPolicy(),
		superviseEnabled: cfg.SuperviseEnabled(),
		logger:           logger,
		stopCh:           make(chan struct{}),
		doneCh:           make(chan struct{}),
	}

	cmd, rpc, err := t.spawnOnce(ctx)
	if err != nil {
		close(t.doneCh)
		return nil, fmt.Errorf("inprocess transport %q: initial spawn: %w", name, err)
	}
	t.setLive(cmd, rpc)

	go t.superviseLoop(cmd, rpc)
	return t, nil
}

// ListTools returns the config-declared tool catalog. It never touches
// the subprocess — see the package doc — so it succeeds even while the
// plugin is down or mid-restart; the catalog doesn't depend on live
// connectivity any more than a process-mode server's advertised name
// depends on whether a client happens to be connected right now.
func (t *Transport) ListTools(ctx context.Context) ([]registry.Tool, error) {
	out := make([]registry.Tool, len(t.tools))
	copy(out, t.tools)
	return out, nil
}

// manifestTools converts config-declared tool manifests into
// registry.Tool once, at construction time.
func manifestTools(manifests []config.ToolManifest) []registry.Tool {
	out := make([]registry.Tool, len(manifests))
	for i, m := range manifests {
		out[i] = registry.Tool{
			Name:        m.Name,
			Description: m.Description,
			InputSchema: m.InputSchema,
			Annotations: m.Annotations,
		}
	}
	return out
}

func (t *Transport) CallTool(ctx context.Context, name string, arguments map[string]any) (*registry.ToolResult, error) {
	rpc := t.liveRPC()
	if rpc == nil {
		return nil, fmt.Errorf("inprocess transport %q: not connected", t.name)
	}
	res, err := callResult[sdksub.MCPCallResult](ctx, rpc, sdksub.MethodMCPCallTool, sdksub.MCPCallRequest{
		ToolName:  name,
		Arguments: arguments,
	})
	if err != nil {
		return nil, fmt.Errorf("inprocess transport %q: mcp/call_tool %s: %w", t.name, name, err)
	}
	out := &registry.ToolResult{IsError: res.IsError}
	if len(res.Content) > 0 {
		out.Content = []registry.ToolContent{{Type: "text", Text: string(res.Content)}}
	}
	return out, nil
}

// RestartCount reports how many times this transport has respawned its
// plugin subprocess since construction.
func (t *Transport) RestartCount() int64 { return t.restarts.Load() }

// Pid returns the current generation's process ID, or 0 if none is
// live.
func (t *Transport) Pid() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if t.cmd == nil || t.cmd.Process == nil {
		return 0
	}
	return t.cmd.Process.Pid
}

// Close stops supervision and unloads/tears down the current
// generation. Idempotent.
//
// This does not reap the process itself — closing stopCh unblocks
// whichever generation superviseLoop is currently waiting on (via
// waitForDeathOrUnhealthy's own <-t.stopCh case), and that loop's own
// single reap call handles it, exactly once. Reaping here too would
// race two concurrent cmd.Wait calls on the same *exec.Cmd, which
// os/exec does not support.
func (t *Transport) Close() error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	t.closed = true
	close(t.stopCh)
	rpc := t.rpc
	t.mu.Unlock()

	if rpc != nil {
		// plugin/unload is best-effort — a failure or timeout here just
		// means the stdin close below (or, failing that, superviseLoop's
		// own reap escalating to SIGTERM/SIGKILL) tears it down instead.
		unloadCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, _ = rpc.call(unloadCtx, sdksub.MethodUnload, nil)
		cancel()
		rpc.closeWriter()
	}
	<-t.doneCh
	return nil
}

func (t *Transport) superviseLoop(cmd *exec.Cmd, rpc *rpcTransport) {
	defer close(t.doneCh)
	attempt := 0

	for {
		startedAt := time.Now()
		t.waitForDeathOrUnhealthy(cmd, rpc)
		exit := t.reap(cmd)
		t.clearLive()

		t.logger.Warn("station: plugin subprocess exited",
			"server", t.name,
			"kind", string(exit.Kind),
			"code", exit.Code,
			"signal", exit.Signal,
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
		cmd, rpc, respawnErr = t.respawnWithBackoff(&attempt)
		if respawnErr != nil {
			t.logger.Error("station: giving up restarting plugin subprocess", "server", t.name, "err", respawnErr)
			return
		}
		if cmd == nil {
			return
		}
		t.setLive(cmd, rpc)
	}
}

// waitForDeathOrUnhealthy blocks until the plugin subprocess's
// connection closes on its own (rpc.Done — reactive: it crashed, or
// stdin/stdout got torn down), Close was called (t.stopCh), or a
// periodic plugin/health poll fails (proactive: a hung-but-still-alive
// process forced dead so supervision can restart it).
func (t *Transport) waitForDeathOrUnhealthy(cmd *exec.Cmd, rpc *rpcTransport) {
	ticker := time.NewTicker(healthCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-rpc.Done():
			return
		case <-t.stopCh:
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), healthCheckTimeout)
			res, err := callResult[sdksub.HealthResult](ctx, rpc, sdksub.MethodHealth, nil)
			cancel()
			if err != nil || !res.OK {
				t.logger.Warn("station: plugin health check failed, forcing restart",
					"server", t.name, "err", err)
				if cmd.Process != nil {
					_ = cmd.Process.Kill()
				}
				return
			}
		}
	}
}

func (t *Transport) respawnWithBackoff(attempt *int) (*exec.Cmd, *rpcTransport, error) {
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

		cmd, rpc, err := t.spawnOnce(context.Background())
		if err == nil {
			return cmd, rpc, nil
		}
		t.logger.Error("station: plugin respawn failed", "server", t.name, "attempt", *attempt, "err", err)

		select {
		case <-t.stopCh:
			return nil, nil, nil
		default:
		}
	}
}

// spawnOnce starts the plugin subprocess and performs the
// plugin/init + plugin/load handshake over its stdin/stdout.
func (t *Transport) spawnOnce(ctx context.Context) (*exec.Cmd, *rpcTransport, error) {
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

	rpc := newRPCTransport(stdout, stdin)

	handshakeCtx := ctx
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		handshakeCtx, cancel = context.WithTimeout(ctx, connectTimeout)
		defer cancel()
	}

	// DataDir/CacheDir are left empty: mcp-host v1 has no per-plugin data
	// directory concept yet. A plugin that calls InitParams.ResolvedDataDir
	// gets ErrNoDataDir — a real gap for a future data-bearing plugin, not
	// one this transport can paper over.
	initRes, err := callResult[sdksub.InitResult](handshakeCtx, rpc, sdksub.MethodInit, sdksub.InitParams{
		Config:   map[string]string{},
		LogLevel: "info",
		HostInfo: sdksub.HostInfo{Version: identityVersion, Protocol: sdksub.ProtocolVersion},
	})
	if err != nil {
		killAndReap(cmd)
		return nil, nil, fmt.Errorf("plugin/init: %w", err)
	}
	if initRes.Protocol != sdksub.ProtocolVersion {
		killAndReap(cmd)
		return nil, nil, fmt.Errorf("plugin/init: protocol mismatch: host=%d plugin=%d", sdksub.ProtocolVersion, initRes.Protocol)
	}

	loadRes, err := callResult[sdksub.LoadResult](handshakeCtx, rpc, sdksub.MethodLoad, sdksub.LoadParams{})
	if err != nil {
		killAndReap(cmd)
		return nil, nil, fmt.Errorf("plugin/load: %w", err)
	}
	for _, skipped := range loadRes.SkippedRegistrations {
		t.logger.Info("station: plugin skipped registration",
			"server", t.name, "kind", skipped.Kind, "id", skipped.ID, "reason", skipped.Reason)
	}

	return cmd, rpc, nil
}

// reap blocks until cmd's process has actually exited and been waited
// on, escalating to SIGTERM then SIGKILL if it doesn't exit promptly.
// Must be called at most once per cmd, and only after nothing is still
// reading its stdout pipe (rpc's read loop has already stopped, since
// this runs after rpc.Done fired or a forced kill triggered it).
func (t *Transport) reap(cmd *exec.Cmd) supervise.Exit {
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

func killAndReap(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	_ = cmd.Wait()
}

func (t *Transport) setLive(cmd *exec.Cmd, rpc *rpcTransport) {
	t.mu.Lock()
	t.cmd = cmd
	t.rpc = rpc
	t.mu.Unlock()
	pid := 0
	if cmd.Process != nil {
		pid = cmd.Process.Pid
	}
	t.logger.Info("station: plugin subprocess spawned", "server", t.name, "pid", pid)
}

func (t *Transport) clearLive() {
	t.mu.Lock()
	t.cmd = nil
	t.rpc = nil
	t.mu.Unlock()
}

func (t *Transport) liveRPC() *rpcTransport {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.rpc
}

func (t *Transport) tailString() string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if t.tail == nil {
		return ""
	}
	return t.tail.String()
}

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
