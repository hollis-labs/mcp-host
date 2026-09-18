package registry

import (
	"context"
	"fmt"
	"sort"
	"sync"
)

// Entry is one registered logical server: its identity plus the
// transport backing it, and the tool list last discovered from that
// transport.
type Entry struct {
	ID          string
	Name        string
	Description string
	Transport   Transport

	mu    sync.RWMutex
	tools []Tool
}

// Tools returns this entry's last-discovered tool list, sorted by name
// for deterministic output (matching go-mcp/server's own tools/list
// ordering guarantee). Empty until DiscoverTools has been called at
// least once.
func (e *Entry) Tools() []Tool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]Tool, len(e.tools))
	copy(out, e.tools)
	return out
}

// DiscoverTools queries the entry's transport for its current tool list
// and caches the result on the entry, replacing whatever was cached
// before. Returns the freshly discovered tools.
func (e *Entry) DiscoverTools(ctx context.Context) ([]Tool, error) {
	tools, err := e.Transport.ListTools(ctx)
	if err != nil {
		return nil, fmt.Errorf("registry: discover tools for %q: %w", e.ID, err)
	}
	sorted := make([]Tool, len(tools))
	copy(sorted, tools)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })

	e.mu.Lock()
	e.tools = sorted
	e.mu.Unlock()

	out := make([]Tool, len(sorted))
	copy(out, sorted)
	return out, nil
}

// CallTool invokes a tool on the entry's transport directly; it does
// not require DiscoverTools to have been called first.
func (e *Entry) CallTool(ctx context.Context, toolName string, arguments map[string]any) (*ToolResult, error) {
	res, err := e.Transport.CallTool(ctx, toolName, arguments)
	if err != nil {
		return nil, fmt.Errorf("registry: call tool %s on %q: %w", toolName, e.ID, err)
	}
	return res, nil
}

// Registry holds every currently registered logical server, keyed by
// ID. Safe for concurrent use; servers may be added and removed at
// runtime (hot reload / plugin lifecycle).
type Registry struct {
	mu      sync.RWMutex
	entries map[string]*Entry
}

// New returns an empty Registry.
func New() *Registry {
	return &Registry{entries: make(map[string]*Entry)}
}

// Add registers a logical server under id. Returns an error if id is
// empty, transport is nil, or a server with the same id is already
// registered — silent overwrite is rejected the same way Nanite's
// Manager.AddServer rejects it: shadowing an existing logical server is
// a footgun regardless of which transport mode backs it.
func (r *Registry) Add(id, name, description string, transport Transport) (*Entry, error) {
	if id == "" {
		return nil, fmt.Errorf("registry: Add: id is required")
	}
	if transport == nil {
		return nil, fmt.Errorf("registry: Add %q: transport is nil", id)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.entries[id]; exists {
		return nil, fmt.Errorf("registry: Add %q: already registered", id)
	}
	entry := &Entry{ID: id, Name: name, Description: description, Transport: transport}
	r.entries[id] = entry
	return entry, nil
}

// Remove unregisters a logical server, closing its transport if it
// implements io.Closer's shape. Returns false if id was not registered
// (not an error — remove is idempotent from the caller's perspective).
func (r *Registry) Remove(id string) bool {
	r.mu.Lock()
	entry, ok := r.entries[id]
	if ok {
		delete(r.entries, id)
	}
	r.mu.Unlock()
	if !ok {
		return false
	}
	if closer, ok := entry.Transport.(interface{ Close() error }); ok {
		_ = closer.Close()
	}
	return true
}

// Get returns the entry registered under id, if any.
func (r *Registry) Get(id string) (*Entry, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	entry, ok := r.entries[id]
	return entry, ok
}

// List returns every registered entry, sorted by ID for deterministic
// iteration order.
func (r *Registry) List() []*Entry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Entry, 0, len(r.entries))
	for _, entry := range r.entries {
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Len reports how many logical servers are currently registered.
func (r *Registry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.entries)
}
