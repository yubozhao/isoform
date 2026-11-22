package main

import "time"

// risky_commands_config.go owns the Settings toggle "Ask before running risky
// commands". Off by default: attended chats run Bash without asking, while the
// agent's hard blocks (private keys, password stores, Isoform's own data) stay
// in force either way. When on, executePrompt passes the flag on the attended
// chat TaskInput so the agent loop asks before commands that change things
// outside the project, send data, or cannot be checked. Unattended runs never
// read this value.

// askBeforeRiskyCommands reports the persisted preference. A missing or
// unreadable config means off, the default.
func askBeforeRiskyCommands() bool {
	return loadConfig().AskBeforeRiskyCommands
}

// GetAskBeforeRiskyCommands is the Wails binding the risky-commands settings card
// reads on mount.
func (a *App) GetAskBeforeRiskyCommands() (perfValue bool, perfErr error) {
	defer logPageBinding("GetAskBeforeRiskyCommands", time.Now(), &perfValue)
	configMu.Lock()
	defer configMu.Unlock()
	cfg, err := loadConfigChecked()
	if err != nil {
		return false, err
	}
	return cfg.AskBeforeRiskyCommands, nil
}

// SetAskBeforeRiskyCommands persists the preference. The next attended chat
// turn picks it up; a turn already running keeps the value it started with.
func (a *App) SetAskBeforeRiskyCommands(enabled bool) error {
	return updateConfig(func(c *yansuConfig) {
		c.AskBeforeRiskyCommands = enabled
	})
}
