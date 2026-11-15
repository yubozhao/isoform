package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"wails-gui/storage"
)

// The left-out list is people whose conversations Isoform never reads. It is
// kept per person, not per app, so every chat capture can honour it; today
// the Messages reader does (imessage_capture.go), and the Messages card in
// Settings > Observation shows and edits it. An unreadable list stops a read
// rather than guess.

// peopleLeftOutSet is the people the user left out.
func peopleLeftOutSet(ctx context.Context, store *storage.Store) (map[string]bool, error) {
	set := map[string]bool{}
	if store == nil {
		return set, nil
	}
	raw, err := store.GetUIState(ctx, peopleLeftOutKey)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(raw) == "" {
		return set, nil
	}
	var ids []string
	if err := json.Unmarshal([]byte(raw), &ids); err != nil {
		// An unreadable list leaves everyone out rather than guess.
		return nil, fmt.Errorf("the left-out list cannot be read: %w", err)
	}
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" {
			set[id] = true
		}
	}
	return set, nil
}

// SetPeopleLeftOut replaces the left-out list. Someone newly left out has
// what was kept from their Messages conversations deleted at once.
func (a *App) SetPeopleLeftOut(ids []string) error {
	if a == nil || a.store == nil {
		return errors.New("the store is not ready")
	}
	ctx := context.Background()
	if a.imessage != nil {
		a.imessage.writeMu.Lock()
		defer a.imessage.writeMu.Unlock()
	}
	if err := setPeopleLeftOut(ctx, a.store, ids); err != nil {
		return err
	}
	if _, err := a.store.DeleteMessageAsksOf(ctx, ids); err != nil {
		return err
	}
	a.emitBrowserEvent(imessageSyncedEvent)
	return nil
}

func setPeopleLeftOut(ctx context.Context, store *storage.Store, ids []string) error {
	seen := map[string]bool{}
	clean := []string{}
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" && !seen[id] {
			seen[id] = true
			clean = append(clean, id)
		}
	}
	sort.Strings(clean)
	raw, err := json.Marshal(clean)
	if err != nil {
		return err
	}
	return store.SetUIState(ctx, peopleLeftOutKey, string(raw))
}
