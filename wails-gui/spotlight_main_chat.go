package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"wails-gui/internal/persistence"
	"wails-gui/storage"
)

// Quick chat answers in the one Chat thread (computer-chat v1.0.0 rules 10
// and 11): its first question and follow-ups are normal main-chat turns, so
// "Open in Chat" finds the exchange already there. The spotlight registers a
// mirror-only state for the main chat's tag and forwards the turn's events
// to the panel; the window, or the desktop turn keeper when the window stores
// nothing, writes the rows as for every main-chat turn.

// quickChatTurns marks main-chat tags whose current turn came from Quick
// chat. The panel shows the turn's text, its sources line (computer-chat
// v1.2.0 rule 5) and its hand-off card (v1.0.0 rule 11), mirrored by
// mirrorMainChatRowToSpotlight; the card's sheet opens in the panel.
var quickChatTurns sync.Map

func (a *App) quickChatTurnActive(tag string) bool {
	_, ok := quickChatTurns.Load(tag)
	return ok
}

// quickChatMirroredRows are the main-chat rows a Quick chat exchange shows in
// the panel as they are written.
var quickChatMirroredRows = map[string]bool{chatSourcesLineTool: true, chatHandoffCardTool: true}

// mirrorMainChatRowToSpotlight sends a row written into the main chat to the
// Quick chat panel mirroring that exchange, so a sources line or a hand-off
// card shows under the panel's answer as it does in Chat.
func (a *App) mirrorMainChatRowToSpotlight(mainID string, row storage.StreamItemRow, sortOrder int) {
	if a == nil || a.spotlightStream == nil || !quickChatMirroredRows[row.ToolName] {
		return
	}
	a.spotlightTagsMu.Lock()
	state := a.spotlightTags[mainID]
	a.spotlightTagsMu.Unlock()
	if state == nil || !state.main {
		return
	}
	state.mu.Lock()
	from := state.fromSortOrder
	state.mu.Unlock()
	if from < 0 || sortOrder < from {
		return
	}
	row.SortOrder = sortOrder
	a.spotlightStream.Publish(mainID, spotlightStreamEvent{Kind: "item", Data: jsonEncodeOrEmpty(persistence.ToStreamItemData(row))})
}

var (
	errSpotlightChatBusy = errors.New("Chat is answering another message")
	errSpotlightMainEdit = errors.New("edit this message in Chat")
)

// spotlightMainChatTag is the main chat's tag when this submission belongs
// in it: a first question while a main chat exists, or a follow-up on a
// mirrored exchange. Empty keeps Quick chat's own session.
func (a *App) spotlightMainChatTag(sessionLocalID string) string {
	if sessionLocalID != "" {
		if a.spotlightIsMainTag(sessionLocalID) {
			return sessionLocalID
		}
		return ""
	}
	if a == nil || a.store == nil {
		return ""
	}
	main, err := a.store.LoadMainSession(context.Background())
	if err != nil || main == nil {
		return ""
	}
	return main.LocalID
}

func (a *App) spotlightIsMainTag(tag string) bool {
	if tag == "" {
		return false
	}
	a.spotlightTagsMu.Lock()
	state := a.spotlightTags[tag]
	a.spotlightTagsMu.Unlock()
	return state != nil && state.main
}

func (a *App) startSpotlightMainTurn(prompt, bundleID, tag, requestID string, isFollowup bool) (spotlightSubmitResponse, error) {
	a.mu.Lock()
	busy := a.activeTags[tag]
	a.mu.Unlock()
	if busy {
		return spotlightSubmitResponse{}, errSpotlightChatBusy
	}
	a.spotlightTagsMu.Lock()
	if a.spotlightTags == nil {
		a.spotlightTags = make(map[string]*spotlightTagState)
	}
	state := a.spotlightTags[tag]
	switch {
	case isFollowup && (state == nil || !state.main):
		a.spotlightTagsMu.Unlock()
		return spotlightSubmitResponse{}, fmt.Errorf("unknown spotlight session")
	case !isFollowup:
		// Each summon mirrors a new exchange.
		state = &spotlightTagState{done: closedChan(), main: true, fromSortOrder: -1}
		a.spotlightTags[tag] = state
	}
	a.spotlightTagsMu.Unlock()

	state.operationMu.Lock()
	defer state.operationMu.Unlock()
	id := "spotlight-u-" + requestID
	if requestID == "" {
		id = "spotlight-u-" + uuid.NewString()
	} else if prior, err := a.store.LoadStreamItem(context.Background(), id); err != nil || prior != nil {
		// A request from a prior host lifetime has an unknown outcome; it
		// does not run again.
		if err != nil {
			return spotlightSubmitResponse{}, err
		}
		return spotlightSubmitResponse{}, fmt.Errorf("prior spotlight request outcome is unavailable")
	}
	// What's on screen goes with the first question only while its chip was
	// there (the pill sends no bundle ID otherwise). It is captured now but
	// stashed only once this turn is admitted, so no other main-chat turn
	// can read it; the prompt checks Closed Eyes and sensitive apps again.
	var screen *focusedAppSnapshot
	if !isFollowup && strings.TrimSpace(bundleID) != "" {
		screen = a.captureFocusedAppContext(bundleID, quickChatSubmitCaptureBudget)
	}
	done := make(chan struct{})
	state.mu.Lock()
	state.done = done
	state.mu.Unlock()
	a.publishSpotlightUserPromptEcho(tag, prompt)
	quickChatTurns.Store(tag, true)
	result, err := a.sendMainChatAs(id, prompt, nil, nil, promptOriginMac, func() {
		if screen != nil {
			stashFocusedAppContext(tag, screen)
		}
	})
	if err != nil {
		quickChatTurns.Delete(tag)
		close(done)
		a.bridgeAgentEventToSpotlight(AgentEvent{Type: "error", Tag: tag}, state)
		return spotlightSubmitResponse{}, err
	}
	state.mu.Lock()
	if state.fromSortOrder < 0 {
		state.fromSortOrder = result.SortOrder
	}
	state.mu.Unlock()
	go func() {
		defer close(done)
		defer quickChatTurns.Delete(tag)
		// The turn starts asynchronously; done closes once it has settled.
		deadline := time.Now().Add(2 * time.Second)
		for {
			a.mu.Lock()
			active := a.activeTags[tag]
			a.mu.Unlock()
			if active || time.Now().After(deadline) {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		for {
			a.mu.Lock()
			active := a.activeTags[tag]
			a.mu.Unlock()
			if !active {
				return
			}
			time.Sleep(25 * time.Millisecond)
		}
	}()
	return spotlightSubmitResponse{SessionLocalID: tag, UserItemID: id, SortOrder: result.SortOrder}, nil
}
