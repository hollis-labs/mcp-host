# mcp-host

mcp-host is a dual-transport MCP plugin host, as a library: it hosts N
independently addressable "logical MCP servers," each backed by a real
standalone MCP server (`process` mode) or a lightweight plugin-sdk-dialect
subprocess (`inprocess` mode). `apps/station` is the first real consumer —
check it for a worked example before inventing a new one.

## Start Here

- `README.md` covers config format, the process/inprocess split, and the
  package map.
- `mcphost.go` (root package `mcphost`) is the complete "just run this"
  entrypoint — `Run(ctx, cfg, opts)`. Read this first; most new code should
  build on it or the packages it composes, not duplicate its wiring.
- `registry/transport.go` owns the one interface every backing transport
  implements (`ListTools`/`CallTool`) — read this before touching either
  transport package.
- `config/config.go` owns the YAML schema and every validation rule;
  `examples/config/host.yaml` is a complete worked example.

## Commands

```bash
gofmt -l .
go vet ./...
go test -race -count=1 ./...
```

There is no CI workflow and no Makefile in this repo, so these are the only
gate (same convention as `go-mcp`).

This repo is listed in `~/dev/hollis-labs/go.work` for local cross-module
development against `apps/station` before either is tagged/pushed anywhere.

## Boundaries

Deliberately decoupled from Nanite's `internal/mcp.Manager`: no uniform
tool-name namespace, no trust tiers, no first-party-builtin concept. Every
logical server keeps its own identity and tool set; this host never merges
them, and an aggregate/mux'd single endpoint across logical servers is
explicitly out of scope, not a missing feature.

No plugin marketplace, no multi-tenancy, no hot-reload from a live config
change — `registry.Registry` supports hot add/remove programmatically, but
nothing above it watches a config file for changes yet.

`transport/process`'s spawn variant does not route through
`go-mcp/client.Pool` — Pool's own stdio dial owns spawning internally and
never exposes the `*exec.Cmd`, which real supervision needs for
`supervise.ClassifyExit`. This library owns the process directly instead
(`mcpsdk.IOTransport` over pipes it controls) and uses Pool only for the
dial (URL) variant. Don't "simplify" the spawn path back onto Pool without
re-solving this.

`transport/inprocess` and `examples/plugins/clock-plugin` hand-roll the
plugin-side wire loop for `mcp/list_tools` rather than using
`plugin-sdk/subprocess.Serve` — verified against plugin-sdk v0.5.0,
`Serve`'s dispatch has no case for that method and no capability interface
exists for it, so a plugin built the documented way cannot answer it. See
`examples/plugins/clock-plugin/main.go`'s doc comment before assuming
`Serve` is usable for a new inprocess plugin's tool list.

A relayed tool with no declared input schema gets `EmptyObjectSchema()`
substituted before registration (`serving/serving.go`) — the official SDK's
`AddTool` panics on a nil schema rather than erroring, and a tool from an
upstream server/plugin that never declared one is a real, not-hypothetical
case.

`inprocess` plugins never receive a `DataDir`/`CacheDir` from `plugin/init`
— this library has no per-plugin data directory concept yet. A plugin
calling `InitParams.ResolvedDataDir()` gets `ErrNoDataDir`. Fine today (no
example plugin needs persistence); a real gap the first data-bearing plugin
will hit.

`mcphost.Run`'s identity strings (client/server name+version reported over
the wire) are currently hardcoded (`"mcp-host"`/`"dev"` in the transport
packages) rather than configurable. Fine for a single consumer; revisit if
a second consumer wants its own identity reported instead.
