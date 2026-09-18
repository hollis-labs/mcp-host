package registry

import (
	"context"
	"errors"
	"testing"
)

// fakeTransport is an in-memory Transport for tests — no real process or
// RPC involved (those arrive in T3/T4).
type fakeTransport struct {
	tools     []Tool
	listErr   error
	callErr   error
	closed    bool
	lastCall  string
	lastArgs  map[string]any
	callCount int
}

func (f *fakeTransport) ListTools(ctx context.Context) ([]Tool, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.tools, nil
}

func (f *fakeTransport) CallTool(ctx context.Context, name string, arguments map[string]any) (*ToolResult, error) {
	f.callCount++
	f.lastCall = name
	f.lastArgs = arguments
	if f.callErr != nil {
		return nil, f.callErr
	}
	return &ToolResult{Content: []ToolContent{{Type: "text", Text: "ok:" + name}}}, nil
}

func (f *fakeTransport) Close() error {
	f.closed = true
	return nil
}

func TestRegistry_AddGetList(t *testing.T) {
	r := New()
	ft := &fakeTransport{}

	entry, err := r.Add("echo", "Echo Server", "a test server", ft)
	if err != nil {
		t.Fatalf("Add: unexpected error: %v", err)
	}
	if entry.ID != "echo" || entry.Name != "Echo Server" {
		t.Fatalf("entry = %+v", entry)
	}

	got, ok := r.Get("echo")
	if !ok || got != entry {
		t.Fatalf("Get(echo) = %v, %v", got, ok)
	}

	if r.Len() != 1 {
		t.Fatalf("Len() = %d, want 1", r.Len())
	}
}

func TestRegistry_Add_RejectsEmptyID(t *testing.T) {
	r := New()
	if _, err := r.Add("", "X", "", &fakeTransport{}); err == nil {
		t.Fatal("Add: expected error for empty id, got nil")
	}
}

func TestRegistry_Add_RejectsNilTransport(t *testing.T) {
	r := New()
	if _, err := r.Add("x", "X", "", nil); err == nil {
		t.Fatal("Add: expected error for nil transport, got nil")
	}
}

func TestRegistry_Add_RejectsDuplicateID(t *testing.T) {
	r := New()
	if _, err := r.Add("echo", "Echo", "", &fakeTransport{}); err != nil {
		t.Fatalf("first Add: unexpected error: %v", err)
	}
	_, err := r.Add("echo", "Echo Again", "", &fakeTransport{})
	if err == nil {
		t.Fatal("Add: expected error for duplicate id, got nil")
	}
}

func TestRegistry_List_SortedByID(t *testing.T) {
	r := New()
	for _, id := range []string{"charlie", "alpha", "bravo"} {
		if _, err := r.Add(id, id, "", &fakeTransport{}); err != nil {
			t.Fatalf("Add(%s): %v", id, err)
		}
	}
	list := r.List()
	if len(list) != 3 {
		t.Fatalf("List() len = %d, want 3", len(list))
	}
	wantOrder := []string{"alpha", "bravo", "charlie"}
	for i, id := range wantOrder {
		if list[i].ID != id {
			t.Errorf("List()[%d].ID = %q, want %q", i, list[i].ID, id)
		}
	}
}

func TestRegistry_Remove_ClosesTransportAndDeregisters(t *testing.T) {
	r := New()
	ft := &fakeTransport{}
	if _, err := r.Add("echo", "Echo", "", ft); err != nil {
		t.Fatalf("Add: %v", err)
	}

	if !r.Remove("echo") {
		t.Fatal("Remove: expected true for existing entry")
	}
	if !ft.closed {
		t.Error("Remove: transport was not closed")
	}
	if _, ok := r.Get("echo"); ok {
		t.Error("Get: entry still present after Remove")
	}
	if r.Len() != 0 {
		t.Errorf("Len() = %d, want 0", r.Len())
	}
}

func TestRegistry_Remove_UnknownIDIsIdempotent(t *testing.T) {
	r := New()
	if r.Remove("nope") {
		t.Fatal("Remove: expected false for unknown id")
	}
}

func TestRegistry_HotAddAfterRemove(t *testing.T) {
	r := New()
	first := &fakeTransport{}
	if _, err := r.Add("echo", "Echo v1", "", first); err != nil {
		t.Fatalf("Add: %v", err)
	}
	r.Remove("echo")

	second := &fakeTransport{}
	entry, err := r.Add("echo", "Echo v2", "", second)
	if err != nil {
		t.Fatalf("re-Add after Remove: unexpected error: %v", err)
	}
	if entry.Name != "Echo v2" {
		t.Errorf("entry.Name = %q, want %q", entry.Name, "Echo v2")
	}
	if first.closed == false {
		t.Error("original transport should have been closed by Remove")
	}
}

func TestEntry_DiscoverTools_CachesSortedByName(t *testing.T) {
	ft := &fakeTransport{tools: []Tool{
		{Name: "zeta"}, {Name: "alpha"}, {Name: "mu"},
	}}
	r := New()
	entry, _ := r.Add("echo", "Echo", "", ft)

	tools, err := entry.DiscoverTools(context.Background())
	if err != nil {
		t.Fatalf("DiscoverTools: unexpected error: %v", err)
	}
	wantOrder := []string{"alpha", "mu", "zeta"}
	if len(tools) != len(wantOrder) {
		t.Fatalf("DiscoverTools returned %d tools, want %d", len(tools), len(wantOrder))
	}
	for i, name := range wantOrder {
		if tools[i].Name != name {
			t.Errorf("tools[%d].Name = %q, want %q", i, tools[i].Name, name)
		}
	}

	// Tools() reflects the cached, sorted result.
	cached := entry.Tools()
	for i, name := range wantOrder {
		if cached[i].Name != name {
			t.Errorf("cached tools[%d].Name = %q, want %q", i, cached[i].Name, name)
		}
	}
}

func TestEntry_DiscoverTools_PropagatesError(t *testing.T) {
	wantErr := errors.New("boom")
	ft := &fakeTransport{listErr: wantErr}
	r := New()
	entry, _ := r.Add("echo", "Echo", "", ft)

	_, err := entry.DiscoverTools(context.Background())
	if err == nil || !errors.Is(err, wantErr) {
		t.Fatalf("DiscoverTools error = %v, want wrapping %v", err, wantErr)
	}
}

func TestEntry_Tools_EmptyBeforeDiscover(t *testing.T) {
	r := New()
	entry, _ := r.Add("echo", "Echo", "", &fakeTransport{tools: []Tool{{Name: "x"}}})
	if got := entry.Tools(); len(got) != 0 {
		t.Errorf("Tools() before DiscoverTools = %v, want empty", got)
	}
}

func TestEntry_CallTool_DelegatesToTransport(t *testing.T) {
	ft := &fakeTransport{}
	r := New()
	entry, _ := r.Add("echo", "Echo", "", ft)

	res, err := entry.CallTool(context.Background(), "ping", map[string]any{"x": 1})
	if err != nil {
		t.Fatalf("CallTool: unexpected error: %v", err)
	}
	if len(res.Content) != 1 || res.Content[0].Text != "ok:ping" {
		t.Errorf("CallTool result = %+v", res)
	}
	if ft.lastCall != "ping" {
		t.Errorf("transport.lastCall = %q, want %q", ft.lastCall, "ping")
	}
	if ft.lastArgs["x"] != 1 {
		t.Errorf("transport.lastArgs = %v", ft.lastArgs)
	}
}

func TestEntry_CallTool_PropagatesError(t *testing.T) {
	wantErr := errors.New("tool failed")
	ft := &fakeTransport{callErr: wantErr}
	r := New()
	entry, _ := r.Add("echo", "Echo", "", ft)

	_, err := entry.CallTool(context.Background(), "ping", nil)
	if err == nil || !errors.Is(err, wantErr) {
		t.Fatalf("CallTool error = %v, want wrapping %v", err, wantErr)
	}
}

func TestRegistry_IndependentToolSetsPerServer(t *testing.T) {
	// Two logical servers never merge tool namespaces — each keeps its
	// own identity and tool set (per the ADR's per-logical-server
	// addressability requirement).
	r := New()
	echo, _ := r.Add("echo", "Echo", "", &fakeTransport{tools: []Tool{{Name: "ping"}}})
	clock, _ := r.Add("clock", "Clock", "", &fakeTransport{tools: []Tool{{Name: "now"}, {Name: "ping"}}})

	if _, err := echo.DiscoverTools(context.Background()); err != nil {
		t.Fatalf("echo DiscoverTools: %v", err)
	}
	if _, err := clock.DiscoverTools(context.Background()); err != nil {
		t.Fatalf("clock DiscoverTools: %v", err)
	}

	if len(echo.Tools()) != 1 {
		t.Errorf("echo.Tools() = %v, want 1 tool", echo.Tools())
	}
	if len(clock.Tools()) != 2 {
		t.Errorf("clock.Tools() = %v, want 2 tools", clock.Tools())
	}
	// Both servers legitimately export a "ping"/"now" tool named the same
	// as another server's tool — no collision handling needed since
	// namespaces are never merged.
}
