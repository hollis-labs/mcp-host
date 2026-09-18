package config

import (
	"strings"
	"testing"
)

func boolPtr(b bool) *bool { return &b }

func TestParse_ValidProcessSpawnAndInprocess(t *testing.T) {
	data := []byte(`
logical_servers:
  - id: echo
    name: Echo Server
    transport: process
    process:
      command: ./bin/echo-server
      args: ["-verbose"]
      env:
        FOO: bar
    serve:
      stdio: true
  - id: clock
    name: Clock Plugin
    transport: inprocess
    inprocess:
      command: ./bin/clock-plugin
      tools:
        - name: now
          description: Returns the current time.
          input_schema:
            type: object
            properties: {}
          annotations:
            readOnlyHint: true
    serve:
      http:
        path: /clock
        allowed_origins: ["https://example.com"]
        bearer_tokens: ["secret-token"]
`)
	cfg, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: unexpected error: %v", err)
	}
	if len(cfg.LogicalServers) != 2 {
		t.Fatalf("expected 2 logical servers, got %d", len(cfg.LogicalServers))
	}

	echo := cfg.LogicalServers[0]
	if echo.Transport != TransportProcess {
		t.Errorf("echo.Transport = %q, want %q", echo.Transport, TransportProcess)
	}
	if echo.Process == nil || echo.Process.Command != "./bin/echo-server" {
		t.Errorf("echo.Process = %+v, want Command ./bin/echo-server", echo.Process)
	}
	if !echo.Process.SuperviseEnabled() {
		t.Errorf("echo.Process.SuperviseEnabled() = false, want true (default)")
	}
	if echo.Serve == nil || !echo.Serve.Stdio {
		t.Errorf("echo.Serve.Stdio = false, want true")
	}

	clock := cfg.LogicalServers[1]
	if clock.Transport != TransportInprocess {
		t.Errorf("clock.Transport = %q, want %q", clock.Transport, TransportInprocess)
	}
	if clock.Inprocess == nil || clock.Inprocess.Command != "./bin/clock-plugin" {
		t.Errorf("clock.Inprocess = %+v, want Command ./bin/clock-plugin", clock.Inprocess)
	}
	if clock.Serve == nil || clock.Serve.HTTP == nil || clock.Serve.HTTP.Path != "/clock" {
		t.Errorf("clock.Serve.HTTP = %+v, want Path /clock", clock.Serve)
	}
	if len(clock.Inprocess.Tools) != 1 || clock.Inprocess.Tools[0].Name != "now" {
		t.Errorf("clock.Inprocess.Tools = %+v, want one tool named now", clock.Inprocess.Tools)
	}
}

func TestParse_ValidProcessDial(t *testing.T) {
	data := []byte(`
logical_servers:
  - id: remote
    name: Remote Server
    transport: process
    process:
      url: https://example.com/mcp
      timeout_seconds: 30
`)
	cfg, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: unexpected error: %v", err)
	}
	remote := cfg.LogicalServers[0]
	if remote.Process.URL != "https://example.com/mcp" {
		t.Errorf("remote.Process.URL = %q", remote.Process.URL)
	}
	if remote.Process.Command != "" {
		t.Errorf("remote.Process.Command = %q, want empty (dial mode)", remote.Process.Command)
	}
}

func TestValidate_RejectsMissingID(t *testing.T) {
	cfg := &Config{LogicalServers: []LogicalServer{{
		Name:      "No ID",
		Transport: TransportProcess,
		Process:   &ProcessConfig{Command: "x"},
	}}}
	assertErrContains(t, cfg.Validate(), "id is required")
}

func TestValidate_RejectsMissingName(t *testing.T) {
	cfg := &Config{LogicalServers: []LogicalServer{{
		ID:        "x",
		Transport: TransportProcess,
		Process:   &ProcessConfig{Command: "x"},
	}}}
	assertErrContains(t, cfg.Validate(), "name is required")
}

func TestValidate_RejectsUnknownTransport(t *testing.T) {
	cfg := &Config{LogicalServers: []LogicalServer{{
		ID: "x", Name: "X", Transport: "bogus",
	}}}
	assertErrContains(t, cfg.Validate(), `unknown transport "bogus"`)
}

func TestValidate_RejectsEmptyTransport(t *testing.T) {
	cfg := &Config{LogicalServers: []LogicalServer{{ID: "x", Name: "X"}}}
	assertErrContains(t, cfg.Validate(), "transport is required")
}

func TestValidate_RejectsProcessTransportWithInprocessBlock(t *testing.T) {
	cfg := &Config{LogicalServers: []LogicalServer{{
		ID: "x", Name: "X", Transport: TransportProcess,
		Process:   &ProcessConfig{Command: "x"},
		Inprocess: &InprocessConfig{Command: "y"},
	}}}
	assertErrContains(t, cfg.Validate(), "must not set inprocess")
}

func TestValidate_RejectsProcessMissingBlock(t *testing.T) {
	cfg := &Config{LogicalServers: []LogicalServer{{
		ID: "x", Name: "X", Transport: TransportProcess,
	}}}
	assertErrContains(t, cfg.Validate(), "requires a process block")
}

func TestValidate_RejectsInprocessMissingBlock(t *testing.T) {
	cfg := &Config{LogicalServers: []LogicalServer{{
		ID: "x", Name: "X", Transport: TransportInprocess,
	}}}
	assertErrContains(t, cfg.Validate(), "requires an inprocess block")
}

func TestValidate_RejectsInprocessMissingCommand(t *testing.T) {
	cfg := &Config{LogicalServers: []LogicalServer{{
		ID: "x", Name: "X", Transport: TransportInprocess,
		Inprocess: &InprocessConfig{},
	}}}
	assertErrContains(t, cfg.Validate(), "command is required")
}

func TestValidate_RejectsInprocessMissingTools(t *testing.T) {
	cfg := &Config{LogicalServers: []LogicalServer{{
		ID: "x", Name: "X", Transport: TransportInprocess,
		Inprocess: &InprocessConfig{Command: "x"},
	}}}
	assertErrContains(t, cfg.Validate(), "at least one tool is required")
}

func TestValidate_RejectsInprocessToolMissingName(t *testing.T) {
	cfg := &Config{LogicalServers: []LogicalServer{{
		ID: "x", Name: "X", Transport: TransportInprocess,
		Inprocess: &InprocessConfig{Command: "x", Tools: []ToolManifest{{Description: "no name"}}},
	}}}
	assertErrContains(t, cfg.Validate(), "tools[0]: name is required")
}

func TestValidate_RejectsInprocessDuplicateToolNames(t *testing.T) {
	cfg := &Config{LogicalServers: []LogicalServer{{
		ID: "x", Name: "X", Transport: TransportInprocess,
		Inprocess: &InprocessConfig{Command: "x", Tools: []ToolManifest{{Name: "now"}, {Name: "now"}}},
	}}}
	assertErrContains(t, cfg.Validate(), "duplicate tool name")
}

func TestValidate_RejectsProcessCommandAndURLTogether(t *testing.T) {
	cfg := &Config{LogicalServers: []LogicalServer{{
		ID: "x", Name: "X", Transport: TransportProcess,
		Process: &ProcessConfig{Command: "x", URL: "http://y"},
	}}}
	assertErrContains(t, cfg.Validate(), "mutually exclusive")
}

func TestValidate_RejectsProcessNeitherCommandNorURL(t *testing.T) {
	cfg := &Config{LogicalServers: []LogicalServer{{
		ID: "x", Name: "X", Transport: TransportProcess,
		Process: &ProcessConfig{},
	}}}
	assertErrContains(t, cfg.Validate(), "exactly one of command or url is required")
}

func TestValidate_RejectsStdioTransportWithURL(t *testing.T) {
	cfg := &Config{LogicalServers: []LogicalServer{{
		ID: "x", Name: "X", Transport: TransportProcess,
		Process: &ProcessConfig{URL: "http://y", Transport: "stdio"},
	}}}
	assertErrContains(t, cfg.Validate(), "url is not valid with stdio")
}

func TestValidate_RejectsArgsEnvWithURL(t *testing.T) {
	cfg := &Config{LogicalServers: []LogicalServer{{
		ID: "x", Name: "X", Transport: TransportProcess,
		Process: &ProcessConfig{URL: "http://y", Args: []string{"a"}},
	}}}
	assertErrContains(t, cfg.Validate(), "only valid with command")
}

func TestValidate_RejectsHeadersWithCommand(t *testing.T) {
	cfg := &Config{LogicalServers: []LogicalServer{{
		ID: "x", Name: "X", Transport: TransportProcess,
		Process: &ProcessConfig{Command: "x", Headers: map[string]string{"A": "b"}},
	}}}
	assertErrContains(t, cfg.Validate(), "headers is only valid with url")
}

func TestValidate_RejectsDuplicateIDs(t *testing.T) {
	cfg := &Config{LogicalServers: []LogicalServer{
		{ID: "x", Name: "X1", Transport: TransportProcess, Process: &ProcessConfig{Command: "a"}},
		{ID: "x", Name: "X2", Transport: TransportProcess, Process: &ProcessConfig{Command: "b"}},
	}}
	assertErrContains(t, cfg.Validate(), "duplicate id")
}

func TestValidate_RejectsMultipleStdioOwners(t *testing.T) {
	cfg := &Config{LogicalServers: []LogicalServer{
		{ID: "a", Name: "A", Transport: TransportProcess, Process: &ProcessConfig{Command: "x"}, Serve: &ServeConfig{Stdio: true}},
		{ID: "b", Name: "B", Transport: TransportProcess, Process: &ProcessConfig{Command: "y"}, Serve: &ServeConfig{Stdio: true}},
	}}
	assertErrContains(t, cfg.Validate(), "at most one logical server may serve over stdio")
}

func TestValidate_RejectsHTTPPathMissingSlash(t *testing.T) {
	cfg := &Config{LogicalServers: []LogicalServer{{
		ID: "x", Name: "X", Transport: TransportProcess,
		Process: &ProcessConfig{Command: "a"},
		Serve:   &ServeConfig{HTTP: &HTTPServeConfig{Path: "clock"}},
	}}}
	assertErrContains(t, cfg.Validate(), `must start with "/"`)
}

func TestValidate_RejectsEmptyLogicalServers(t *testing.T) {
	cfg := &Config{}
	assertErrContains(t, cfg.Validate(), "at least one logical server is required")
}

func TestSuperviseEnabled_DefaultsTrue(t *testing.T) {
	p := &ProcessConfig{Command: "x"}
	if !p.SuperviseEnabled() {
		t.Error("ProcessConfig.SuperviseEnabled() default = false, want true")
	}
	ip := &InprocessConfig{Command: "x"}
	if !ip.SuperviseEnabled() {
		t.Error("InprocessConfig.SuperviseEnabled() default = false, want true")
	}
}

func TestSuperviseEnabled_ExplicitFalse(t *testing.T) {
	p := &ProcessConfig{Command: "x", Supervise: boolPtr(false)}
	if p.SuperviseEnabled() {
		t.Error("ProcessConfig.SuperviseEnabled() = true, want false")
	}
}

func TestParse_RejectsMalformedYAML(t *testing.T) {
	_, err := Parse([]byte("logical_servers: [this is not: valid: yaml"))
	if err == nil {
		t.Fatal("Parse: expected error for malformed YAML, got nil")
	}
}

func TestLoad_MissingFile(t *testing.T) {
	_, err := Load("/nonexistent/station-config-test.yaml")
	if err == nil {
		t.Fatal("Load: expected error for missing file, got nil")
	}
}

func TestSummarize(t *testing.T) {
	cfg := &Config{LogicalServers: []LogicalServer{
		{
			ID: "echo", Name: "Echo", Description: "an echo server", Transport: TransportProcess,
			Process: &ProcessConfig{Command: "./echo-server"},
		},
		{
			ID: "clock", Name: "Clock", Transport: TransportInprocess,
			Inprocess: &InprocessConfig{Command: "./clock-plugin", Tools: []ToolManifest{{Name: "now"}}},
		},
	}}

	summaries := cfg.Summarize()
	if len(summaries) != 2 {
		t.Fatalf("Summarize() len = %d, want 2", len(summaries))
	}

	echo := summaries[0]
	if echo.ID != "echo" || echo.Name != "Echo" || echo.Transport != TransportProcess {
		t.Errorf("echo summary = %+v", echo)
	}
	if echo.ToolNames != nil {
		t.Errorf("echo.ToolNames = %v, want nil (process mode tools aren't statically known)", echo.ToolNames)
	}

	clock := summaries[1]
	if len(clock.ToolNames) != 1 || clock.ToolNames[0] != "now" {
		t.Errorf("clock.ToolNames = %v, want [now]", clock.ToolNames)
	}
}

func assertErrContains(t *testing.T, err error, substr string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error containing %q, got nil", substr)
	}
	if !strings.Contains(err.Error(), substr) {
		t.Fatalf("error = %q, want substring %q", err.Error(), substr)
	}
}
