package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"yansu-agent/agent/tools"
)

// Quick chat answers in the one Chat thread (computer-chat v1.0.0 rules 10
// and 11): its first question and follow-ups are normal main-chat turns, so
// "Open in Chat" finds the exchange already there. The spotlight registers a
// mirror-only state for the main chat's tag and forwards the turn's events
// to the panel; the window, or the desktop turn keeper when the window stores
// nothing, writes the rows as for every main-chat turn.

// quickChatTurns marks main-chat tags whose current turn came from Quick
// chat. The panel shows the turn's text but not Chat's cards, so such a turn
// gets no hand-off card or sources line; the model points at Open in Chat
// instead (computer-chat v1.0.0 rule 11 waits for the sheet over other apps).
var quickChatTurns sync.Map

// quickChatCardTools are the main-chat tools whose rows only Chat can show.
var quickChatCardTools = map[string]bool{"HandOffWork": true, "CiteSources": true}

func (a *App) quickChatTurnActive(tag string) bool {
	_, ok := quickChatTurns.Load(tag)
	return ok
}

// withoutQuickChatCards drops the card tools from a Quick chat turn.
func withoutQuickChatCards(list []tools.Tool) []tools.Tool {
	kept := list[:0:0]
	for _, tool := range list {
		if !quickChatCardTools[tool.Name()] {
			kept = append(kept, tool)
		}
	}
	return kept
}

// injectQuickChatContext tells a Quick chat turn where its cards are.
func (a *App) injectQuickChatContext(tag string) string {
	if !a.quickChatTurnActive(tag) {
		return ""
	}
	return `<quick-chat>
This message came from Quick chat, a small panel over another app. When the user asks for work, do not do it here and do not write it out: say in one sentence, in the user's language, that it is work for their agent and that they can hand it over from Open in Chat.
</quick-chat>

`
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
