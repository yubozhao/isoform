package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"wails-gui/internal/destinations"
)

const mcpReadClientHeader = "X-Isoform-MCP-Client"

// Serialize token changes with response publication. Revocation returns only
// after any already-authorized response has finished writing.
var mcpReadCredentialMu sync.RWMutex

func mcpReadClientCommand(client string) (string, error) {
	switch client {
	case "claude-code":
		return "claude", nil
	case "codex":
		return "codex", nil
	default:
		return "", errors.New("unknown Isoform MCP client")
	}
}

func mcpReadDirectory() (string, error) {
	root := configDirOrFallback()
	if root == "" {
		return "", errors.New("Isoform data folder unavailable")
	}
	return filepath.Join(root, "mcp-read"), nil
}

func ensureMCPReadDirectory() (string, error) {
	dir, err := mcpReadDirectory()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("Isoform MCP folder must be a private directory")
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

func mcpReadTokenPath(client string) (string, error) {
	if _, err := mcpReadClientCommand(client); err != nil {
		return "", err
	}
	dir, err := mcpReadDirectory()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, client+".token"), nil
}

func readMCPReadToken(client string) (string, error) {
	path, err := mcpReadTokenPath(client)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() != 64 {
		return "", errors.New("invalid Isoform MCP token file")
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) != 64 {
		return "", errors.New("Isoform MCP token unavailable")
	}
	return string(data), nil
}

// issueMCPReadToken is called only by the native Settings connection action.
// Reconnecting keeps an existing grant; revocation followed by connect rotates it.
// Caller holds mcpReadCredentialMu for writing.
func issueMCPReadToken(client string) (string, error) {
	path, err := mcpReadTokenPath(client)
	if err != nil {
		return "", err
	}
	if token, err := readMCPReadToken(client); err == nil {
		return token, nil
	}
	if _, err := ensureMCPReadDirectory(); err != nil {
		return "", err
	}
	token, err := newBrowserBridgeToken()
	if err != nil {
		return "", err
	}
	if err := writeBrowserBridgeToken(path, token); err != nil {
		return "", err
	}
	return token, nil
}

func validMCPReadRequest(r *http.Request) bool {
	want, err := readMCPReadToken(r.Header.Get(mcpReadClientHeader))
	if err != nil {
		return false
	}
	got, found := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	return found && subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

func (hb *hostBridge) publishMCPReadEndpoint() error {
	hb.mcpLifecycleMu.Lock()
	defer hb.mcpLifecycleMu.Unlock()
	dir, err := ensureMCPReadDirectory()
	if err != nil {
		return err
	}
	hb.mcpEndpointPath = filepath.Join(dir, "endpoint.json")
	if hb.mcpServer == nil {
		// Old TCP discovery is never reused, even after an unclean exit.
		if err := os.Remove(hb.mcpEndpointPath); err != nil && !os.IsNotExist(err) {
			return err
		}
		nonce, err := newBrowserBridgeToken()
		if err != nil {
			return err
		}
		hb.mcpSocketPath = filepath.Join(dir, "read-"+nonce[:12]+".sock")
		listener, err := net.Listen("unix", hb.mcpSocketPath)
		if err != nil {
			return errors.New("Isoform private read socket is unavailable")
		}
		if err := os.Chmod(hb.mcpSocketPath, 0o600); err != nil {
			listener.Close()
			return err
		}
		route := hostBridgeRoute{"/api/mcp/read", hostAccessMCPRead, http.MethodPost, hb.handleMCPRead}
		mux := http.NewServeMux()
		mux.HandleFunc(route.path, hb.guard(route, route.handler))
		hb.mcpServer = &http.Server{
			Handler: mux, ReadTimeout: 5 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 10 * time.Second,
			ConnContext: func(ctx context.Context, _ net.Conn) context.Context {
				return context.WithValue(ctx, mcpReadSocketContextKey{}, hb)
			},
		}
		go hb.mcpServer.Serve(listener)
	}
	data, _ := json.Marshal(map[string]string{"socket": filepath.Base(hb.mcpSocketPath)})
	if err := writeBrowserBridgeToken(hb.mcpEndpointPath, string(data)); err != nil {
		_ = hb.mcpServer.Close()
		hb.mcpServer = nil
		return err
	}
	return nil
}

type mcpReadSocketContextKey struct{}

func (hb *hostBridge) removeMCPReadEndpoint() {
	hb.mcpLifecycleMu.Lock()
	defer hb.mcpLifecycleMu.Unlock()
	if hb.mcpServer != nil {
		_ = hb.mcpServer.Close()
		hb.mcpServer = nil
	}
	data, _ := json.Marshal(map[string]string{"socket": filepath.Base(hb.mcpSocketPath)})
	removeBrowserBridgeToken(hb.mcpEndpointPath, string(data))
}

func (hb *hostBridge) handleMCPRead(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		hb.writeError(w, http.StatusBadRequest, "invalid MCP read request")
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		hb.writeError(w, http.StatusBadRequest, "invalid MCP read request")
		return
	}
	// Guard checked the token before parsing; check again before reading and
	// once more before publishing, including external file removal.
	if !validMCPReadRequest(r) {
		hb.writeError(w, http.StatusUnauthorized, "Isoform MCP read access revoked or unavailable")
		return
	}
	if hb.app == nil {
		hb.writeError(w, http.StatusServiceUnavailable, "Isoform is not ready")
		return
	}
	data, err := hb.app.callMCPReadTool(r.Context(), request.Name, request.Arguments)
	if err != nil {
		status := http.StatusServiceUnavailable
		var invalid *mcpReadValidationError
		if errors.As(err, &invalid) {
			status = http.StatusUnprocessableEntity
		}
		if errors.Is(err, errMCPReadPaused) {
			status = http.StatusLocked
		}
		hb.writeError(w, status, err.Error())
		return
	}
	mcpReadCredentialMu.RLock()
	defer mcpReadCredentialMu.RUnlock()
	if !validMCPReadRequest(r) {
		hb.writeError(w, http.StatusUnauthorized, "Isoform MCP read access revoked or unavailable")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	if err := json.NewEncoder(w).Encode(browserAPIResponse{OK: true, Data: data}); err == nil {
		destinations.RecordAnswer(r.Header.Get(mcpReadClientHeader))
	}
}
