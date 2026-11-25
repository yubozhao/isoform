package main

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

func (a *App) LoadMainChatProactiveItem(momentID string) (perfValue *StreamItemData, perfErr error) {
	if err := chatFeatureErr(); err != nil {
		return perfValue, err
	}
	defer logPageBinding("LoadMainChatProactiveItem", time.Now(), &perfValue)
	if a == nil || a.store == nil || momentID == "" {
		return nil, errors.New("chat item is unavailable")
	}
	ctx := context.Background()
	main, err := a.store.LoadMainSession(ctx)
	if err != nil || main == nil {
		return nil, err
	}
	row, err := a.store.FindProactiveStreamItem(ctx, main.LocalID, momentID)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, errors.New("chat item was not found")
	}
	item := &StreamItemData{
		ID:        row.ID,
		Kind:      row.Kind,
		Content:   row.Content,
		ToolName:  row.ToolName,
		ToolInput: row.ToolInput,
		SessionID: main.LocalID,
		SortOrder: row.SortOrder,
	}
	items := []StreamItemData{*item}
	a.projectStreamItems(items)
	if release, err := a.projectMainChatView(ctx, main.LocalID, items, time.Now()); err != nil {
		return nil, err
	} else if release != nil {
		defer release()
	}
	var projected mainChatProactive
	if json.Unmarshal([]byte(items[0].ToolInput), &projected) != nil {
		return nil, errors.New("chat item is unavailable")
	}
	selected := findMainChatProactiveCard(&projected, momentID)
	if selected == nil {
		return nil, errors.New("chat item is unavailable")
	}
	if projected.MomentID != momentID {
		// A digest may contain several cards. The panel receives only the
		// selected, privacy-projected child, never a sibling's content.
		data, err := json.Marshal(selected)
		if err != nil {
			return nil, err
		}
		items[0].ToolInput = string(data)
		items[0].Content = ""
	}
	return &items[0], nil
}

func (a *App) ReturnToMainChatPlace(momentID string) (bool, error) {
	if err := chatFeatureErr(); err != nil {
		return false, err
	}
	if a == nil || a.store == nil || momentID == "" {
		return false, nil
	}
	ctx := context.Background()
	main, err := a.store.LoadMainSession(ctx)
	if err != nil || main == nil {
		return false, err
	}
	row, err := a.store.FindProactiveStreamItem(ctx, main.LocalID, momentID)
	if err != nil || row == nil {
		return false, err
	}
	var item mainChatProactive
	if json.Unmarshal([]byte(row.ToolInput), &item) != nil {
		return false, errors.New("proactive message is invalid")
	}
	card := findMainChatProactiveCard(&item, momentID)
	if card == nil || card.Place == nil || card.Place.BundleID == "" {
		return false, nil
	}
	if card.ActivityOutcomeSource != "" && !a.threadOutcomeSourceReadable(ctx, card.ActivityOutcomeSource) {
		return false, nil
	}
	activate := activateMainChatPlaceApp
	if activate == nil {
		return false, nil
	}
	return activate(card.Place.BundleID), nil
}

func findMainChatProactiveCard(item *mainChatProactive, momentID string) *mainChatProactive {
	if item == nil {
		return nil
	}
	if item.MomentID == momentID {
		return item
	}
	for i := range item.Items {
		if card := findMainChatProactiveCard(&item.Items[i], momentID); card != nil {
			return card
		}
	}
	return nil
}
