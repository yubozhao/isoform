package main

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"wails-gui/internal/platform/imessage"
	"wails-gui/storage"
)

// MessagesOverview is what Settings > Observation shows about Messages.
type MessagesOverview struct {
	Supported bool `json:"supported"`
	// Granted is Full Disk Access: Isoform can open the Messages database.
	Granted    bool   `json:"granted"`
	Enabled    bool   `json:"enabled"`
	LastReadAt string `json:"lastReadAt"`
	LastError  string `json:"lastError"`
	// Asks and Commitments count what was kept from the last two weeks.
	Asks        int `json:"asks"`
	Commitments int `json:"commitments"`
	// LeftOut is the people whose conversations are never read.
	LeftOut []LeftOutPerson `json:"leftOut"`
}

// LeftOutPerson is one person on the left-out list.
type LeftOutPerson struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// GetMessagesOverview reports access, the last read, what was kept and the
// left-out list. It never asks macOS for anything.
func (a *App) GetMessagesOverview() (MessagesOverview, error) {
	view := MessagesOverview{Supported: systemMessagesHistorySupported(), LeftOut: []LeftOutPerson{}}
	if a == nil || a.store == nil {
		return view, errors.New("the store is not ready")
	}
	ctx := context.Background()
	view.Granted = view.Supported && messagesHistoryAccess()
	enabled, err := a.messagesSwitchRead(ctx)
	if err != nil {
		return view, err
	}
	view.Enabled = enabled
	last, lastErr, _ := a.imessage.status()
	view.LastError = lastErr
	if !last.IsZero() {
		view.LastReadAt = last.UTC().Format(time.RFC3339)
	} else if stored, err := a.store.GetUIState(ctx, imessageLastReadKey); err == nil {
		view.LastReadAt = stored
	}
	rows, err := a.store.ListMessageAsks(ctx, time.Now().Add(-storage.MessageAskRetention).UnixMilli())
	if err != nil {
		return view, err
	}
	for _, row := range rows {
		switch row.Kind {
		case storage.MessageAskAsked:
			view.Asks++
		case storage.MessageAskCommitted:
			view.Commitments++
		}
	}
	left, err := peopleLeftOutSet(ctx, a.store)
	if err != nil {
		return view, err
	}
	for id := range left {
		person := LeftOutPerson{ID: id}
		if p, err := a.store.GetPrincipal(ctx, id); err == nil {
			person.Name = strings.TrimSpace(p.DisplayName)
		}
		view.LeftOut = append(view.LeftOut, person)
	}
	sort.Slice(view.LeftOut, func(i, j int) bool { return view.LeftOut[i].Name < view.LeftOut[j].Name })
	return view, nil
}

// ForgetMessages deletes everything kept from Messages. Reading goes on from
// where it is: messages already read are not read again.
func (a *App) ForgetMessages() error {
	if a == nil || a.store == nil {
		return errors.New("the store is not ready")
	}
	if a.imessage != nil {
		a.imessage.clearStored(context.Background(), "forgotten by the user")
	} else if _, err := a.store.DeleteAllMessageAsks(context.Background()); err != nil {
		return err
	}
	// What the assistant was given from Messages goes with it (See them).
	if _, err := a.store.ForgetAssistantCallsForKind(context.Background(), "messages"); err != nil {
		return err
	}
	a.emitBrowserEvent(imessageSyncedEvent)
	return nil
}

// SyncMessagesNow reads Messages right away: the Refresh button.
func (a *App) SyncMessagesNow() error {
	if a == nil || a.imessage == nil {
		return errors.New("Messages is not available on this computer")
	}
	enabled, err := a.messagesSwitchRead(context.Background())
	if err != nil {
		return err
	}
	if !enabled {
		return errors.New("messages reading is switched off")
	}
	if a.IsClosedEyesModeEnabled() {
		return errors.New("observing is paused")
	}
	if imessageProtected() {
		return errors.New("Messages is one of the apps you protect")
	}
	if a.imessage.access() != imessage.StateAllowed {
		return errors.New("Isoform does not have Full Disk Access")
	}
	_, err = a.imessage.Sync(context.Background(), "refresh")
	return err
}
