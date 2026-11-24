package main

import (
	"errors"
	"log"
	"os"
	"path/filepath"
)

// noYansuAccount reports that the Yansu account is out of the product. Since
// 2026-10-12 the product is the desktop and the phone with the relay as the
// only server: there is no sign-in, no subscription or billing, no
// backend-relayed connector and no backend round-trip in any build. A build
// that brings an account back changes this one line.
func noYansuAccount() bool {
	return true
}

// errNoYansuAccount is returned by every Yansu backend API helper. The backend
// is not part of the product, so all round-trips fail closed with a clear,
// non-fatal error (callers degrade to offline instead of blocking UI).
var errNoYansuAccount = errors.New("The Yansu service is retired.")

// ArchivedConnectorInfo describes a previously-disconnected local connector whose
// credentials were archived so the user can reuse them (reconnect without
// re-entering the token). Returned by the per-platform Get*Archived bindings and
// consumed by the local connector setup wizards' "Reuse your previous bot" card.
type ArchivedConnectorInfo struct {
	Has   bool   `json:"has"`
	Label string `json:"label"`
}

// archiveLocalCreds copies <dir>/credentials.json to credentials.archived.json
// so a disconnect can later be "reused" without re-entering the token. No-op if
// no active credentials exist. Mirrors Feishu's archive flow for consistency.
func archiveLocalCreds(dir string) {
	data, err := os.ReadFile(filepath.Join(dir, "credentials.json"))
	if err != nil {
		return
	}
	if err := os.WriteFile(filepath.Join(dir, "credentials.archived.json"), data, 0600); err != nil {
		log.Printf("[connectors] archive creds in %s failed: %v", dir, err)
	}
}

// readArchivedLocalCreds returns the archived credentials bytes, or nil.
func readArchivedLocalCreds(dir string) []byte {
	data, err := os.ReadFile(filepath.Join(dir, "credentials.archived.json"))
	if err != nil {
		return nil
	}
	return data
}

// removeArchivedLocalCreds drops the archived copy (after a successful reuse or
// a fresh connect that supersedes it).
func removeArchivedLocalCreds(dir string) {
	_ = os.Remove(filepath.Join(dir, "credentials.archived.json"))
}
