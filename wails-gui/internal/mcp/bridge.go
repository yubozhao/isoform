package mcp

import (
	"context"
	"fmt"
	"sync"

	yansutools "yansu-agent/agent/tools"

	"github.com/mark3labs/mcp-go/mcp"
)

// BridgeManager is the narrow capability the Bridge needs from the
// MCP supervisor: read the cached tool list, force a refresh, and
// dispatch a tool call. *Manager satisfies this interface; tests can
// substitute a fake without spawning subprocesses.
type BridgeManager interface {
	Tools(id string) ([]mcp.Tool, error)
	RefreshTools(ctx context.Context, id string) ([]mcp.Tool, error)
	ToolCaller
}

// Bridge wires the MCP supervisor into the yansu-agent tools registry.
// It is the only piece of code that knows how to translate the
// "set of MCP tools currently exposed by server <id>" view into the
// "group of tools.Tool implementations registered under prefix
// mcp__<id>" view.
//
// The Bridge is intentionally tiny: lifecycle (spawn, shutdown) is the
// Manager's job; the Bridge just keeps the registry in sync after the
// caller (App lifecycle in commit 5) has decided what should be live.
type Bridge struct {
	manager       BridgeManager
	registry      *yansutools.Registry
	mu            sync.RWMutex
	exposureModes map[string]ExposureMode
	// trustLookup, when non-nil, is consulted on every Register to seed
	// the "skip approval" flag of freshly-built adapters. wails-gui
	// wires this to the persisted trusted-server set so a server marked
	// trusted in Settings stays trusted across restarts. Nil = the
	// bridge assumes no trust and always asks (the safe default for
	// embeddings that don't have a UI to grant trust).
	trustLookup func(serverID string) bool
	// adapterGroups tracks the live adapter instances per server so
	// SetTrusted can mutate them in place when the user toggles the
	// trust flag at runtime (no rebuild round-trip required).
	adapterGroups map[string][]yansutools.Tool
}

// ExposureMode controls how one MCP server is exposed to the agent
// registry. Direct mode preserves the historical behavior: every
// upstream tool is registered as an individual model-visible tool.
// Gateway mode registers only a small search/describe/call facade.
type ExposureMode string

const (
	ExposureModeDirect  ExposureMode = "direct"
	ExposureModeGateway ExposureMode = "gateway"
)

// NewBridge returns a Bridge bound to the given manager and the
// registry the agent loop dispatches tool_use blocks against.
func NewBridge(manager BridgeManager, registry *yansutools.Registry) *Bridge {
	return &Bridge{
		manager:       manager,
		registry:      registry,
		exposureModes: make(map[string]ExposureMode),
		adapterGroups: make(map[string][]yansutools.Tool),
	}
}

// SetTrustLookup installs the callback the bridge consults during
// Register to decide whether a freshly-built adapter should start in
// the trusted (skip-approval) state. Call this once at startup before
// the first Register; later calls replace the lookup but do not
// retroactively re-flag already-registered adapters — use
// SetTrustedAndRefresh for that.
func (b *Bridge) SetTrustLookup(fn func(serverID string) bool) {
	b.mu.Lock()
	b.trustLookup = fn
	b.mu.Unlock()
}

// trusted is the internal "should this server start trusted?" query
// against the configured lookup. Nil lookup → never trusted.
func (b *Bridge) trusted(serverID string) bool {
	b.mu.RLock()
	fn := b.trustLookup
	b.mu.RUnlock()
	if fn == nil {
		return false
	}
	return fn(serverID)
}

// SetExposureMode sets how a server should be registered on the next
// Register/Refresh call. Existing registry entries are not changed
// until Register is called, which lets callers update config and
// registration in an explicit order.
func (b *Bridge) SetExposureMode(serverID string, mode ExposureMode) error {
	switch mode {
	case "", ExposureModeDirect:
		b.mu.Lock()
		delete(b.exposureModes, serverID)
		b.mu.Unlock()
		return nil
	case ExposureModeGateway:
		b.mu.Lock()
		b.exposureModes[serverID] = mode
		b.mu.Unlock()
		return nil
	default:
		return fmt.Errorf("unsupported MCP exposure mode %q for server %q", mode, serverID)
	}
}

// Register reads the manager's current tool snapshot for serverID and
// installs an adapter group into the registry under the prefix
// derived from serverID. Calling Register again on the same serverID
// atomically replaces whatever was registered before — this is how
// tool list drift (a server that hot-reloads its tools) propagates.
//
// Tool names go through resolveUniqueExposedName so the registry sees
// an Anthropic-safe name even if the MCP server advertised a tool
// name with dots, slashes, or other characters outside Anthropic's
// allowed set. Conflicting sanitized names within a single server's
// tool set are disambiguated by suffix.
func (b *Bridge) Register(serverID string) error {
	// Fetch tools outside the lock — manager.Tools may block on the MCP
	// server's tools/list response (process IPC) and we don't want
	// SetTrustedAndRefresh / SetTrustLookup waiting on that.
	tools, err := b.manager.Tools(serverID)
	if err != nil {
		return fmt.Errorf("bridge register %q: %w", serverID, err)
	}

	// Hold the write lock for the entire trust-snapshot →
	// adapter-construction → registry-replace → adapterGroups-assign
	// sequence so SetTrustedAndRefresh can't interleave between the
	// trust read and the adapterGroups write. Without this, a trust
	// toggle that arrives mid-Register would flip the adapters we are
	// about to overwrite, and the freshly-built adapters would carry
	// the pre-toggle trust state. The exposureMode and trustLookup
	// reads access fields directly (not via the RLock helpers) to avoid
	// re-entering the lock.
	b.mu.Lock()
	defer b.mu.Unlock()

	trusted := false
	if b.trustLookup != nil {
		trusted = b.trustLookup(serverID)
	}

	mode := ExposureModeDirect
	if m, ok := b.exposureModes[serverID]; ok && m == ExposureModeGateway {
		mode = ExposureModeGateway
	}

	var adapters []yansutools.Tool
	switch mode {
	case ExposureModeGateway:
		adapters = NewGatewayTools(serverID, b.manager, trusted)
	default:
		taken := make(map[string]bool, len(tools))
		adapters = make([]yansutools.Tool, 0, len(tools))
		for _, t := range tools {
			exposed := resolveUniqueExposedName(serverID, t.Name, taken)
			adapters = append(adapters, NewToolAdapter(serverID, exposed, t, b.manager, trusted))
		}
	}
	if err := b.registry.ReplaceGroup(GroupPrefix(serverID), adapters); err != nil {
		return fmt.Errorf("bridge register %q: %w", serverID, err)
	}
	b.adapterGroups[serverID] = adapters
	return nil
}

// SetTrustedAndRefresh flips the per-server "skip approval" flag for
// every live adapter the bridge currently has registered for serverID,
// without round-tripping through the MCP manager. Used after the user
// toggles the trust switch (or picks "Always allow this server" on a
// prompt) so subsequent calls in the same registered set go through
// without surfacing a permission_request.
//
// If serverID has no registered adapters (server not yet started, or
// already unregistered) this is a no-op — the next Register call will
// pick up the new trust state via the configured trustLookup.
func (b *Bridge) SetTrustedAndRefresh(serverID string, trusted bool) {
	b.mu.RLock()
	group := b.adapterGroups[serverID]
	b.mu.RUnlock()
	for _, t := range group {
		switch v := t.(type) {
		case *McpToolAdapter:
			v.SetTrusted(trusted)
		case *gatewayCallTool:
			v.SetTrusted(trusted)
		}
	}
}

// Refresh re-fetches the server's tools/list (cost: one round-trip)
// and then Register's the new snapshot. Use this when the model has
// just been told a server's tools changed (e.g. via a notification),
// or proactively before a turn if the bridge has been idle.
func (b *Bridge) Refresh(ctx context.Context, serverID string) error {
	if _, err := b.manager.RefreshTools(ctx, serverID); err != nil {
		return fmt.Errorf("bridge refresh %q: %w", serverID, err)
	}
	return b.Register(serverID)
}

// Unregister removes every adapter for serverID from the registry.
// Used when a server is disabled or uninstalled; safe to call even if
// the server was never registered.
//
// Holds b.mu.Lock() across the ReplaceGroup + delete pair so a
// concurrent Register cannot interleave its own ReplaceGroup +
// adapterGroups assignment between our two steps and leave the two
// stores out of sync (e.g. registry pointing at fresh adapters that
// adapterGroups no longer tracks, so a later SetTrustedAndRefresh
// misses them).
func (b *Bridge) Unregister(serverID string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.registry.ReplaceGroup(GroupPrefix(serverID), nil); err != nil {
		return fmt.Errorf("bridge unregister %q: %w", serverID, err)
	}
	delete(b.adapterGroups, serverID)
	return nil
}

// GroupPrefix returns the tools.Registry group prefix for the given
// MCP server id. The id is underscore-normalised to match the exposed
// tool names produced by buildExposedName / resolveUniqueExposedName,
// so ReplaceGroup's "tool name must start with prefix+__" check stays
// in sync. Exposed so consumers (tests, logs) can compute the prefix
// without instantiating a Bridge.
func GroupPrefix(serverID string) string {
	return "mcp__" + normalizeForExposure(serverID)
}
