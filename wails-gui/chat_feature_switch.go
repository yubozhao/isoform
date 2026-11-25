package main

import (
	"context"
	"log"
	"strings"

	"wails-gui/storage"
)

// featureChat: Chat, Quick chat and the chat apps (bots). Isoform drops them
// (docs/plans/scope-cut-2026-11.md, section 6, PR A1): with the switch off no
// chat turn runs, the chat bindings and phone routes answer as if absent, no
// Quick chat helper or hotkey starts, no bot starts, and nothing is posted to
// the main chat. The saved chat history is kept, hidden. YANSU_FEATURES=chat
// brings it back for one launch.
const featureChat featureSwitch = "chat"

// chatFeatureErr is nil when Chat is on, else the error its bindings return.
func chatFeatureErr() error {
	if featureOn(featureChat) {
		return nil
	}
	return errFeatureOff(featureChat)
}

// chatSessionRefused refuses a turn on a chat session (the main chat, a side
// chat or a Quick chat ask) while Chat is off. Task sessions, hand-offs and
// one-time work run as before.
func (a *App) chatSessionRefused(tag string) error {
	if chatFeatureErr() == nil {
		return nil
	}
	if strings.HasPrefix(tag, "spotlight_") {
		return errFeatureOff(featureChat)
	}
	if a == nil || a.store == nil || strings.TrimSpace(tag) == "" {
		return nil
	}
	row, err := a.store.LoadSession(context.Background(), tag)
	if err != nil || row == nil {
		return nil
	}
	switch row.Kind {
	case storage.SessionKindMain, storage.SessionKindSide:
		return errFeatureOff(featureChat)
	}
	return nil
}

// mobileChatRoute reports whether a phone route belongs to Chat: every
// /chats route, a brief's conversation and a brief paragraph's Ask.
func mobileChatRoute(method, path string) bool {
	if path == "/chats" || strings.HasPrefix(path, "/chats/") {
		return true
	}
	switch mobileRouteFamily(method, path) {
	case "POST /briefs/*/conversation", "GET /briefs/*/ask-answer":
		return true
	}
	return false
}

// startQuickChat starts the Quick chat helper and its global hotkey.
func (a *App) startQuickChat() {
	// Quick chat is part of Chat: with Chat off no helper starts, and one left
	// running by an earlier launch is stopped (scope cut 2026-11, A1).
	if chatFeatureErr() == nil {
		go func() {
			if err := a.spotlight.Start(a.ctx); err != nil {
				log.Printf("[spotlight] supervisor start failed: %v", err)
			}
		}()

		// Quick Chat global hotkey. Non-fatal on failure: the in-app
		// Cmd+J shortcut still works even if the global tap can't start
		// (e.g. Accessibility permission pending).
		go func() {
			if err := a.StartQuickChatHotkey(); err != nil {
				log.Printf("[quickchat] global hotkey unavailable: %v", err)
			}
		}()
	} else if bin := findSpotlightBinary(); bin != "" {
		go cleanupOrphanedSpotlightHelpers(bin)
	}
}

// startChatApps starts the chat apps' scanners and the bots that have a saved
// connection.
func (a *App) startChatApps() {
	// The chat apps (bots) and their scanners are part of Chat: with Chat off
	// none starts (scope cut 2026-11, A1).
	if chatFeatureErr() == nil {
		// Start Slack incremental scanner early so Trigger() is available
		// before tryAutoStartDaemon fires. The scanner goroutine no-ops while the
		// Furnace feature is paused (see furnaceScanEnabled).
		scanner := NewSlackIncrementalScanner(a)
		a.slackScanner = scanner
		go scanner.Run(a.ctx)

		// Start Telegram incremental scanner early so Trigger() is available
		// before tryAutoStartTelegramDaemon fires.
		telegramScanner := NewTelegramIncrementalScanner(a)
		a.telegramScanner = telegramScanner
		go telegramScanner.Run(a.ctx)

		// Auto-start Slack daemon if persisted config exists.
		go a.tryAutoStartDaemon()

		// Auto-start Feishu daemon if persisted config exists.
		go a.tryAutoStartFeishuDaemon()

		// Start Discord incremental scanner early so Trigger() is available
		// before tryAutoStartDiscordDaemon fires.
		discordScanner := NewDiscordIncrementalScanner(a)
		a.discordScanner = discordScanner
		go discordScanner.Run(a.ctx)

		// Auto-start Discord daemon if binding exists.
		go a.tryAutoStartDiscordDaemon()

		// Auto-start Telegram daemon if persisted config exists.
		go a.tryAutoStartTelegramDaemon()

		// Auto-start WeChat monitor if persisted config exists.
		go a.tryAutoStartWeChatMonitor()
	}
}
