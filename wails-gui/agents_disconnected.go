package main

// Disconnected agents fade with Connect again for 30 days (phone-agent
// v1.2.0 rule 14). Only the name and when it left are kept: never a token or
// its hash, so nothing here can ever reconnect it. Connect again is a new
// join with a new key.

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"time"
)

const (
	agentsDisconnectedFile = "disconnected.json"
	agentDisconnectedKeep  = 30 * 24 * time.Hour
)

type disconnectedAgent struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	CatalogID      string `json:"catalogId,omitempty"`
	DisconnectedAt int64  `json:"disconnectedAt"`
}

func (a *App) disconnectedAgentsPath() (string, error) {
	dir, err := a.agentJoin.dirPath()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, agentsDisconnectedFile), nil
}

func (a *App) agentJoinClock() time.Time {
	if a.agentJoin.now != nil {
		return a.agentJoin.now()
	}
	return time.Now()
}

// disconnectedAgents are the ones that left in the last 30 days, newest
// first.
func (a *App) disconnectedAgents() []disconnectedAgent {
	a.agentJoin.fileMu.Lock()
	defer a.agentJoin.fileMu.Unlock()
	return a.disconnectedAgentsLocked()
}

func (a *App) disconnectedAgentsLocked() []disconnectedAgent {
	path, err := a.disconnectedAgentsPath()
	if err != nil {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var list []disconnectedAgent
	if json.Unmarshal(data, &list) != nil {
		return nil
	}
	cutoff := a.agentJoinClock().Add(-agentDisconnectedKeep).UnixMilli()
	kept := list[:0]
	for _, d := range list {
		if d.DisconnectedAt > cutoff {
			kept = append(kept, d)
		}
	}
	return kept
}

func (a *App) keepDisconnectedAgent(d disconnectedAgent) {
	a.agentJoin.fileMu.Lock()
	defer a.agentJoin.fileMu.Unlock()
	list := []disconnectedAgent{d}
	for _, old := range a.disconnectedAgentsLocked() {
		if old.ID != d.ID {
			list = append(list, old)
		}
	}
	path, err := a.disconnectedAgentsPath()
	if err == nil {
		err = writeFrontJSON(path, list)
	}
	if err != nil {
		log.Printf("[agents] disconnected agent not kept: %v", err)
	}
}
