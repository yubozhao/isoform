package mcp

import (
	"fmt"
	"sort"
)

const (
	TransportStdio          = "stdio"
	TransportStreamableHTTP = "streamable_http"
	TransportHTTP           = "http"
)

// Spec is the manager-level view of an MCP server. Callers in the main
// package translate their richer config types into Spec when handing a
// server over to the manager. Keeping this type small lets the manager
// stay decoupled from persistence details (state.json, keychain, etc.).
type Spec struct {
	// ID is the stable server identifier (slug). Used as the lookup key
	// inside the manager and as the tools.Registry group prefix
	// component (the bridge derives "mcp__<ID>" from this).
	ID string

	// ManagedComposio identifies the app-managed Composio HTTP connection.
	ManagedComposio bool

	// Command is the absolute path or PATH-resolvable executable name
	// for stdio transport. Required for stdio.
	Command string
	Args    []string

	// Transport selects how the manager connects to the MCP server.
	// Empty string is treated as "stdio" for backward compatibility.
	// Supported values are "stdio", "http", and "streamable_http".
	Transport string

	// URL is required for HTTP transports.
	URL string

	// Headers are sent on streamable HTTP requests.
	Headers map[string]string

	// Env is the explicit environment to expose to the subprocess, in
	// addition to a small denylist-free passthrough (PATH, HOME, TZ,
	// LANG, USER, SHELL). Values are passed verbatim — callers are
	// responsible for resolving keychain-backed secrets before calling.
	Env map[string]string

	// WorkDir is the optional working directory for the subprocess. If
	// empty, the child inherits the parent's CWD.
	WorkDir string
}

// Validate enforces minimal spawn-time invariants. Persistence-layer
// validation (id pattern etc.) lives in the main package's
// validateMcpServerConfig — this is intentionally narrower so the
// manager can be unit-tested with bespoke specs.
func (s Spec) Validate() error {
	if s.ID == "" {
		return fmt.Errorf("spec: id required")
	}
	switch s.transport() {
	case TransportStdio:
		if s.Command == "" {
			return fmt.Errorf("spec %q: command required", s.ID)
		}
	case TransportStreamableHTTP:
		if s.URL == "" {
			return fmt.Errorf("spec %q: url required", s.ID)
		}
	default:
		return fmt.Errorf("spec %q: unsupported transport %q", s.ID, s.Transport)
	}
	return nil
}

func (s Spec) transport() string {
	switch s.Transport {
	case "":
		return TransportStdio
	case TransportHTTP:
		return TransportStreamableHTTP
	default:
		return s.Transport
	}
}

// envSlice returns the env in KEY=VALUE form with deterministic order
// (sorted keys), suitable for *exec.Cmd.Env.
func (s Spec) envSlice() []string {
	keys := make([]string, 0, len(s.Env))
	for k := range s.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+s.Env[k])
	}
	return out
}
