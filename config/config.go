// Package config loads and validates mcp-host's logical-server
// configuration: the list of independently addressable MCP servers the
// daemon hosts, and how each one is backed and exposed.
package config

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// TransportMode selects which of mcp-host's two backing transports a
// logical server uses. See the ADR (project/atlas/workspace/adr/
// adr_mcp_host_dual_transport) for the full process/inprocess tradeoff.
type TransportMode string

const (
	// TransportProcess backs a logical server with a fully independent,
	// real MCP server, reached over stdio/http/sse.
	TransportProcess TransportMode = "process"
	// TransportInprocess backs a logical server with a subprocess plugin
	// speaking plugin-sdk/subprocess's dialect; mcp-host performs the MCP
	// handshake on the plugin's behalf.
	TransportInprocess TransportMode = "inprocess"
)

// Config is the top-level daemon configuration.
type Config struct {
	LogicalServers []LogicalServer `yaml:"logical_servers"`
}

// LogicalServer configures one independently addressable MCP server —
// its own name and tool list on the wire, never merged into a mux'd view
// with any other logical server (out of scope for v1 per the ADR).
type LogicalServer struct {
	ID          string        `yaml:"id"`
	Name        string        `yaml:"name"`
	Description string        `yaml:"description,omitempty"`
	Transport   TransportMode `yaml:"transport"`

	// Exactly one of Process/Inprocess is set, matching Transport.
	Process   *ProcessConfig   `yaml:"process,omitempty"`
	Inprocess *InprocessConfig `yaml:"inprocess,omitempty"`

	// Serve configures how this logical server is exposed on the wire.
	// Optional; a server with no Serve block is registered but not
	// exposed by the serving layer.
	Serve *ServeConfig `yaml:"serve,omitempty"`
}

// ProcessConfig backs a `process`-mode logical server: either a spawned
// stdio subprocess (Command set) that mcp-host supervises, or a dial to
// an already-running stdio/http/sse endpoint (URL set) that it does not.
// Exactly one of Command or URL must be set.
type ProcessConfig struct {
	// Transport selects how mcp-host reaches the real MCP server: "stdio",
	// "http", or "sse" — the same values go-mcp/client.ServerConfig.Transport
	// accepts. Defaults to "stdio" when Command is set, "http" when URL is
	// set; only needs to be given explicitly to select "sse" for a URL.
	Transport string `yaml:"transport,omitempty"`

	// Spawn fields — set Command to have mcp-host spawn and (by default)
	// supervise the subprocess itself.
	Command string            `yaml:"command,omitempty"`
	Args    []string          `yaml:"args,omitempty"`
	Env     map[string]string `yaml:"env,omitempty"`

	// Dial fields — set URL to connect to an already-running endpoint
	// mcp-host neither spawns nor supervises.
	URL            string            `yaml:"url,omitempty"`
	Headers        map[string]string `yaml:"headers,omitempty"`
	TimeoutSeconds int               `yaml:"timeout_seconds,omitempty"`

	// Supervise enables restart-on-crash for a spawned subprocess (only
	// meaningful when Command is set). Defaults to true.
	Supervise *bool `yaml:"supervise,omitempty"`
}

// InprocessConfig backs an `inprocess`-mode logical server: a subprocess
// plugin speaking plugin-sdk/subprocess's dialect. Always spawned and,
// by default, supervised by mcp-host — there is no dial variant, since an
// inprocess plugin never runs standalone.
type InprocessConfig struct {
	Command string            `yaml:"command"`
	Args    []string          `yaml:"args,omitempty"`
	Env     map[string]string `yaml:"env,omitempty"`

	// Supervise enables restart-on-crash. Defaults to true.
	Supervise *bool `yaml:"supervise,omitempty"`

	// Tools declares this plugin's tool catalog statically. Required,
	// non-empty. mcp-host never queries the running subprocess for its
	// tool list — see the package doc on transport/inprocess for why:
	// plugin-sdk's own subprocess.Serve has no mcp/list_tools support,
	// and Tangent's own plugin host deliberately never calls it either,
	// having tried and abandoned Nanite's live-discovery pattern. The
	// manifest here is the single source of truth; mcp/call_tool is
	// still a live RPC to the subprocess for each declared tool.
	Tools []ToolManifest `yaml:"tools"`
}

// ToolManifest statically declares one tool an inprocess plugin exposes
// — the inprocess-mode equivalent of what a process-mode MCP server
// would otherwise report live via tools/list.
type ToolManifest struct {
	Name        string         `yaml:"name"`
	Description string         `yaml:"description,omitempty"`
	InputSchema map[string]any `yaml:"input_schema,omitempty"`
	Annotations map[string]any `yaml:"annotations,omitempty"`
}

// ServeConfig configures how a logical server is exposed on the wire.
type ServeConfig struct {
	// Stdio, when true, exposes this logical server over the daemon's
	// own stdio. At most one logical server across a Config may set this
	// — stdio is a single shared channel with the host process itself.
	Stdio bool `yaml:"stdio,omitempty"`

	// HTTP, when set, exposes this logical server over HTTP at Path via
	// go-mcp/transport/http. A Config may expose any number of logical
	// servers over HTTP, each at its own path.
	HTTP *HTTPServeConfig `yaml:"http,omitempty"`
}

// HTTPServeConfig configures a logical server's HTTP exposure.
type HTTPServeConfig struct {
	Path           string   `yaml:"path"`
	AllowedOrigins []string `yaml:"allowed_origins,omitempty"`

	// BearerTokens, when non-empty, requires one of these tokens via
	// go-mcp/auth.StaticProvider. Empty means no auth (passthrough) —
	// the same "auth is opt-in, never mandatory" default go-mcp/auth
	// itself documents.
	BearerTokens []string `yaml:"bearer_tokens,omitempty"`
}

// Load reads and parses a YAML config file at path, then validates it.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}
	return Parse(data)
}

// Parse parses YAML config bytes and validates the result.
func Parse(data []byte) (*Config, error) {
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("config: parse: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// Validate checks the config for structural correctness: required
// fields, valid transport modes, mutually exclusive mode-specific
// blocks, unique IDs, and at most one stdio-exposed logical server.
func (c *Config) Validate() error {
	if len(c.LogicalServers) == 0 {
		return fmt.Errorf("config: logical_servers: at least one logical server is required")
	}

	seenIDs := make(map[string]struct{}, len(c.LogicalServers))
	stdioOwner := ""

	for i, ls := range c.LogicalServers {
		if err := ls.validate(); err != nil {
			return fmt.Errorf("config: logical_servers[%d] (%s): %w", i, ls.ID, err)
		}
		if _, dup := seenIDs[ls.ID]; dup {
			return fmt.Errorf("config: logical_servers[%d]: duplicate id %q", i, ls.ID)
		}
		seenIDs[ls.ID] = struct{}{}

		if ls.Serve != nil && ls.Serve.Stdio {
			if stdioOwner != "" {
				return fmt.Errorf("config: logical_servers[%d] (%s): stdio already claimed by %q — at most one logical server may serve over stdio", i, ls.ID, stdioOwner)
			}
			stdioOwner = ls.ID
		}
	}
	return nil
}

func (ls *LogicalServer) validate() error {
	if strings.TrimSpace(ls.ID) == "" {
		return fmt.Errorf("id is required")
	}
	if strings.TrimSpace(ls.Name) == "" {
		return fmt.Errorf("name is required")
	}

	switch ls.Transport {
	case TransportProcess:
		if ls.Inprocess != nil {
			return fmt.Errorf("transport %q must not set inprocess", ls.Transport)
		}
		if ls.Process == nil {
			return fmt.Errorf("transport %q requires a process block", ls.Transport)
		}
		if err := ls.Process.validate(); err != nil {
			return fmt.Errorf("process: %w", err)
		}
	case TransportInprocess:
		if ls.Process != nil {
			return fmt.Errorf("transport %q must not set process", ls.Transport)
		}
		if ls.Inprocess == nil {
			return fmt.Errorf("transport %q requires an inprocess block", ls.Transport)
		}
		if err := ls.Inprocess.validate(); err != nil {
			return fmt.Errorf("inprocess: %w", err)
		}
	case "":
		return fmt.Errorf("transport is required (must be %q or %q)", TransportProcess, TransportInprocess)
	default:
		return fmt.Errorf("unknown transport %q (must be %q or %q)", ls.Transport, TransportProcess, TransportInprocess)
	}

	if ls.Serve != nil {
		if err := ls.Serve.validate(); err != nil {
			return fmt.Errorf("serve: %w", err)
		}
	}
	return nil
}

func (p *ProcessConfig) validate() error {
	hasCommand := strings.TrimSpace(p.Command) != ""
	hasURL := strings.TrimSpace(p.URL) != ""

	switch {
	case hasCommand && hasURL:
		return fmt.Errorf("command and url are mutually exclusive (spawn xor dial)")
	case !hasCommand && !hasURL:
		return fmt.Errorf("exactly one of command or url is required")
	case hasCommand:
		if p.Transport != "" && p.Transport != "stdio" {
			return fmt.Errorf("command implies stdio transport, got %q", p.Transport)
		}
		if len(p.Headers) > 0 {
			return fmt.Errorf("headers is only valid with url")
		}
	case hasURL:
		if p.Transport == "stdio" {
			return fmt.Errorf("url is not valid with stdio transport")
		}
		if len(p.Args) > 0 || len(p.Env) > 0 {
			return fmt.Errorf("args/env are only valid with command")
		}
	}
	return nil
}

// SuperviseEnabled reports whether the spawned subprocess should be
// supervised, defaulting to true when unset.
func (p *ProcessConfig) SuperviseEnabled() bool {
	return p.Supervise == nil || *p.Supervise
}

func (p *InprocessConfig) validate() error {
	if strings.TrimSpace(p.Command) == "" {
		return fmt.Errorf("command is required")
	}
	if len(p.Tools) == 0 {
		return fmt.Errorf("tools: at least one tool is required (mcp-host never discovers an inprocess plugin's tools live)")
	}
	seen := make(map[string]struct{}, len(p.Tools))
	for i, t := range p.Tools {
		if strings.TrimSpace(t.Name) == "" {
			return fmt.Errorf("tools[%d]: name is required", i)
		}
		if _, dup := seen[t.Name]; dup {
			return fmt.Errorf("tools[%d]: duplicate tool name %q", i, t.Name)
		}
		seen[t.Name] = struct{}{}
	}
	return nil
}

// SuperviseEnabled reports whether the plugin subprocess should be
// supervised, defaulting to true when unset.
func (p *InprocessConfig) SuperviseEnabled() bool {
	return p.Supervise == nil || *p.Supervise
}

func (s *ServeConfig) validate() error {
	if s.HTTP != nil {
		if strings.TrimSpace(s.HTTP.Path) == "" {
			return fmt.Errorf("http.path is required")
		}
		if !strings.HasPrefix(s.HTTP.Path, "/") {
			return fmt.Errorf("http.path must start with \"/\", got %q", s.HTTP.Path)
		}
	}
	return nil
}

// ServerSummary is a config-static view of one logical server, for a
// consumer's CLI to display without spawning or dialing anything.
// ToolNames is only ever populated for inprocess mode (manifest-declared,
// known without touching the network or a subprocess); it's nil for
// process mode, whose tools are only known once actually served.
type ServerSummary struct {
	ID          string
	Name        string
	Description string
	Transport   TransportMode
	ToolNames   []string
}

// Summarize returns a ServerSummary for every logical server in c, in
// declaration order.
func (c *Config) Summarize() []ServerSummary {
	out := make([]ServerSummary, 0, len(c.LogicalServers))
	for _, ls := range c.LogicalServers {
		s := ServerSummary{ID: ls.ID, Name: ls.Name, Description: ls.Description, Transport: ls.Transport}
		if ls.Inprocess != nil {
			s.ToolNames = make([]string, len(ls.Inprocess.Tools))
			for i, t := range ls.Inprocess.Tools {
				s.ToolNames[i] = t.Name
			}
		}
		out = append(out, s)
	}
	return out
}
