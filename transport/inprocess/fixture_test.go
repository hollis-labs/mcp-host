package inprocess

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync/atomic"
	"testing"

	sdksub "github.com/hollis-labs/plugin-sdk/subprocess"
)

const (
	fixtureEnvVar            = "STATION_TEST_FIXTURE_PLUGIN"
	fixtureUnhealthyAfterVar = "STATION_TEST_FIXTURE_UNHEALTHY_AFTER"
)

func TestMain(m *testing.M) {
	if os.Getenv(fixtureEnvVar) != "" {
		runFixturePlugin()
		return
	}
	os.Exit(m.Run())
}

// runFixturePlugin uses plugin-sdk/subprocess.Serve exactly as
// documented — Plugin + MCPHandler + HealthChecker. That's now valid:
// this package never sends mcp/list_tools (see the package doc), which
// was the one method Serve's own dispatch has no case for.
func runFixturePlugin() {
	unhealthyAfter := -1
	if v := os.Getenv(fixtureUnhealthyAfterVar); v != "" {
		fmt.Sscanf(v, "%d", &unhealthyAfter)
	}
	if err := sdksub.Serve(&fixturePlugin{unhealthyAfter: unhealthyAfter}); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

type fixturePlugin struct {
	unhealthyAfter int
	healthCalls    atomic.Int64
}

func (p *fixturePlugin) Init(ctx context.Context, params sdksub.InitParams) (sdksub.InitResult, error) {
	return sdksub.InitResult{ID: "fixture", Name: "Fixture Plugin", Version: "test", Protocol: sdksub.ProtocolVersion}, nil
}

func (p *fixturePlugin) Load(ctx context.Context) (sdksub.LoadResult, error) {
	return sdksub.LoadResult{}, nil
}

func (p *fixturePlugin) Unload(ctx context.Context) error { return nil }

func (p *fixturePlugin) Health(ctx context.Context) (sdksub.HealthStatus, error) {
	calls := p.healthCalls.Add(1)
	if p.unhealthyAfter >= 0 && calls > int64(p.unhealthyAfter) {
		return sdksub.HealthStatus{OK: false, Message: "forced unhealthy"}, nil
	}
	return sdksub.HealthStatus{OK: true}, nil
}

func (p *fixturePlugin) MCPCallTool(ctx context.Context, req sdksub.MCPCallRequest) (sdksub.MCPCallResult, error) {
	msg, _ := req.Arguments["message"].(string)
	payload, err := json.Marshal(map[string]any{"pong": "pong:" + msg})
	if err != nil {
		return sdksub.MCPCallResult{}, err
	}
	return sdksub.MCPCallResult{Content: payload}, nil
}
