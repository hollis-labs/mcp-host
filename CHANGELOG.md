# Changelog

All notable changes to this project will be documented in this file.

## v0.2.0 — 2026-09-18

### Changed

- **Breaking: `inprocess` tool discovery is now manifest-driven, never
  live.** `config.InprocessConfig` gains a required `tools` field
  declaring the plugin's full tool catalog (name, description, input
  schema, annotations) statically; `transport/inprocess.Transport.ListTools`
  no longer sends `mcp/list_tools` to the subprocess — it returns the
  config-declared catalog, which now works even while the plugin is down
  or mid-restart. This aligns with Tangent's own plugin host, which
  deliberately never calls `mcp/list_tools` "because Nanite built runtime
  self-declaration and discarded it" (its own manifest package doc,
  verified against source). It's also what makes plugin-sdk's documented
  `subprocess.Serve` helper usable for an inprocess plugin at all — see
  v0.1.0's now-resolved "Known gaps" entry below.
- `examples/plugins/clock-plugin` rewritten to use `subprocess.Serve`
  (`Plugin` + `MCPHandler` + `HealthChecker`) instead of hand-rolling the
  wire protocol — a direct consequence of the change above.
- Existing config files must add a `tools:` block to every `inprocess`
  logical server; see `examples/config/host.yaml`.

## v0.1.0 — 2026-09-18

First release: a dual-transport MCP plugin host, extracted from
`apps/station` (its first consumer) once the library/app seam became
obvious. See the ADR at `project/atlas/workspace/adr/adr_mcp_host_dual_transport`
(Tesseract) for the design this implements.

### Added

- `config` — YAML config schema for a flat list of logical MCP servers,
  each `process` (spawn or dial) or `inprocess` (always spawn), with
  parsing and full validation.
- `registry` — the `Transport` interface (`ListTools`/`CallTool`) both
  transport modes implement, and a per-logical-server registry with hot
  add/remove. No uniform tool namespace, no trust tiers — deliberately
  decoupled from Nanite's agent-host-specific `internal/mcp.Manager`.
- `transport/process` — `process` mode: dial an existing endpoint via
  `go-mcp/client.Pool`, or spawn and supervise a subprocess directly
  (bypassing Pool's own stdio dial, which never exposes the process
  needed for `go-mcp/supervise.ClassifyExit`).
- `transport/inprocess` — `inprocess` mode: a from-scratch host-side
  driver for `plugin-sdk/subprocess`'s JSON-RPC dialect (the SDK ships
  only the plugin side), with the same spawn+supervision shape as
  `transport/process`, plus a proactive `plugin/health` poll on top of
  reactive crash detection.
- `serving` — bridges a registered logical server onto its own
  `go-mcp/server.Server`, over stdio and/or HTTP via `go-mcp/transport/http`
  and `go-mcp/auth`, transport-agnostic by construction.
- `bootstrap` — wires config → registry → real transports together at
  startup.
- `mcphost.Run(ctx, cfg, opts)` (root package) — the complete "just run
  this" entrypoint: builds the registry, exposes every configured logical
  server, and blocks until shutdown.
- `examples/plugins/echo-server`, `examples/plugins/clock-plugin` — real,
  standalone reference plugins for each transport mode.

### Known gaps

See README.md's "Known gaps and boundaries" — notably, `plugin-sdk`
v0.5.0's `subprocess.Serve()` cannot answer `mcp/list_tools` at all (no
dispatch case, no capability interface), and `go-mcp/client.Pool`'s stdio
dial cannot be supervised (it never exposes the spawned process). Both are
verified, real limitations in dependencies this library works around
rather than gaps in this library itself.
