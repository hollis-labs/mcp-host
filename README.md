# mcp-host

mcp-host is a dual-transport MCP plugin host, as a library: it hosts any
number of independently addressable "logical MCP servers," each backed by
either a real standalone MCP server (`process` mode) or a lightweight
plugin-sdk-dialect subprocess (`inprocess` mode). See the ADR at
`project/atlas/workspace/adr/adr_mcp_host_dual_transport` (Tesseract) for the
full design and the process/inprocess tradeoff.

`github.com/hollis-labs/station` is the first real consumer — a small
binary wrapping `mcphost.Run` with flag parsing and its own config. Use it
as the worked example of building on this library.

## Status

Pre-1.0 (`v0.x`), first release. One consumer (`apps/station`) so far —
exported interfaces are new and may still shift between minor versions;
treat any minor bump as potentially breaking and read the CHANGELOG before
upgrading. Patch bumps (`v0.x.y`) are documentation and internal hardening
only. Built and tested end to end: config, registry, both transport modes,
per-logical-server serving, a top-level `Run` entrypoint, and two real
example plugins proving both modes work simultaneously. See
[`CHANGELOG.md`](./CHANGELOG.md) for release notes.

## Install

```bash
go get github.com/hollis-labs/mcp-host
```

## Quickstart

```go
cfg, err := config.Load("station.yaml")
if err != nil {
    log.Fatal(err)
}

ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
defer stop()

if err := mcphost.Run(ctx, cfg, mcphost.Options{HTTPAddr: ":8080"}); err != nil {
    log.Fatal(err)
}
```

That's a complete host. See `examples/plugins/` for two real, standalone
backing plugins (one per transport mode) and `examples/config/host.yaml` for
a config wiring both of them up.

## Config

YAML, one file, a flat list of logical servers:

```yaml
logical_servers:
  - id: echo                 # unique, required
    name: Echo Server        # display name, required
    description: ...         # optional
    transport: process       # "process" | "inprocess", required
    process: { ... }         # required when transport: process
    inprocess: { ... }       # required when transport: inprocess
    serve: { ... }           # optional — omit to register without exposing
```

`process` (`transport: process`) is exactly one of:

- **spawn** — `command`, `args`, `env`; the host owns, spawns, and (by
  default) supervises the subprocess itself, speaking real MCP over its
  stdio.
- **dial** — `url` (+ optional `headers`, `timeout_seconds`,
  `transport: http|sse`); an already-running endpoint the host connects to
  but neither spawns nor supervises.

`command` and `url` are mutually exclusive. `supervise: false` disables
restart-on-crash for a spawned subprocess (default `true`).

`inprocess` (`transport: inprocess`) always spawns: `command`, `args`,
`env`, optional `supervise: false`. See "Process vs inprocess" below for
which to pick.

`serve` exposes a logical server on the wire:

- `stdio: true` — at most one logical server per config may set this
  (stdio is a single shared channel with the host process itself).
- `http: {path, allowed_origins, bearer_tokens}` — any number of logical
  servers may each get their own HTTP path. Empty `bearer_tokens` means no
  auth (`go-mcp/auth`'s own "opt-in, never mandatory" default); non-empty
  requires one of the listed bearer tokens.

A logical server with no `serve` block is registered (its tools are
discovered at startup) but not reachable on the wire.

See `config/config.go` for the full validation rules.

## Process vs inprocess — which to pick

Per the ADR: `process` mode is a real, independent MCP server in any
language, isolated, usable standalone outside the host — heavier to author.
`inprocess` mode is a Go subprocess speaking plugin-sdk's own lightweight
dialect (`plugin/init`, `mcp/list_tools`, `mcp/call_tool`, ...); the host
performs the one real MCP handshake on its behalf. Lighter to author and
idiomatic for a small first-party Go tool, but coupled to this library's own
plugin-sdk dialect version and never runs standalone.

Default to `process` unless you're specifically writing something small and
Go-only for this host and don't need it to run anywhere else.

## Package map

- `config` — YAML config schema, parsing, and validation.
- `registry` — the `Transport` interface (`ListTools`/`CallTool`) both
  modes implement, and the per-logical-server registry. No uniform tool
  namespace, no trust tiers — each logical server keeps its own identity,
  deliberately decoupled from Nanite's agent-host-specific
  `internal/mcp.Manager`.
- `transport/process` — `process` mode: dial via `go-mcp/client.Pool`
  (URL) or host-owned spawn+supervision via `go-mcp/supervise` (Command).
- `transport/inprocess` — `inprocess` mode: this library's own host-side
  driver for plugin-sdk/subprocess's JSON-RPC dialect, plus the same
  spawn+supervision shape as `transport/process`, plus a proactive
  `plugin/health` poll on top of reactive crash detection.
- `serving` — bridges a registered logical server onto its own
  `go-mcp/server.Server`, over stdio and/or HTTP, transport-agnostic by
  construction.
- `bootstrap` — wires config → registry → real transports together;
  `mcphost.Run` (root package) builds on this for the complete daemon loop.
- `examples/plugins/echo-server`, `examples/plugins/clock-plugin` — real,
  standalone example plugins for each mode.

## Commands

```bash
gofmt -l .
go vet ./...
go test -race -count=1 ./...
```

No CI, no Makefile — these three are the only gate (same convention as
`go-mcp`).

## Known gaps and boundaries

- **No aggregate/merged endpoint.** Every logical server is its own MCP
  identity; there is no mux'd single endpoint across all of them.
  Explicitly out of scope for v1 per the ADR.
- **No plugin marketplace, no multi-tenancy.** Config-driven, single
  operator, out of scope for v1.
- **No hot-reload from a running config change.** `registry` supports hot
  add/remove programmatically; nothing wires that to a live config-file
  watch yet.
- **`inprocess` plugins get no `DataDir`/`CacheDir`.** `plugin/init`'s
  `InitParams.DataDir`/`CacheDir` are sent empty — a plugin that calls
  `ResolvedDataDir()` gets `ErrNoDataDir`. Fine for the current stateless
  examples; a real gap for a future plugin needing persistence.
- **`plugin-sdk`'s `subprocess.Serve()` cannot answer `mcp/list_tools`.**
  Verified against plugin-sdk v0.5.0: `Serve`'s dispatch loop has a case for
  every other documented method but that one, and no capability interface
  exists for it either — a plugin built the documented way (`Serve` plus
  capability interfaces) cannot expose its tool list to a host that calls
  it, full stop. `examples/plugins/clock-plugin` and
  `transport/inprocess`'s own tests hand-roll the plugin-side wire loop
  directly against `protocol.go` instead of using `Serve`, with the gap
  documented at the top of that example's `main.go`. A future plugin-sdk
  release adding `mcp/list_tools` support to `Serve` should let both shrink
  back down to a normal `Serve(...)` call. `mcp/list_tools`'s wire *shape*
  is also plugin-sdk's own gap: the SDK names the method but defines no
  request/response type for it at all, so this host's shape
  (`{"server":...}` / `{"tools":[...]}`, mirroring Nanite's own improvised
  convention) is an inferred compatibility choice, not a verified spec.
- **`go-mcp/client.Pool`'s stdio dial can't be supervised.** Pool's own
  stdio spawn is fully internal and reactive-only — it never exposes the
  spawned `*exec.Cmd`, so nothing outside it can ever get a real
  `os.ProcessState` for `supervise.ClassifyExit`. `transport/process`'s
  spawn variant therefore owns its subprocess directly (`mcpsdk.IOTransport`
  over pipes it controls) rather than routing it through Pool; Pool is used
  only for the dial (URL) variant, where its reconnect/health-probe shape
  is the right fit. `transport/inprocess` does the equivalent for
  plugin-sdk's dialect, which has no Pool-equivalent to begin with.

## License

MIT License — see [`LICENSE`](./LICENSE). © Hollis Labs.
