package main

import (
	"context"
	"errors"
	"time"

	"wails-gui/storage"
)

type TaskOfferReference = mobileOfferReference

type TaskSuggestionDraft struct {
	Prompt    string             `json:"prompt"`
	Reference TaskOfferReference `json:"reference"`
}

var errTaskOfferUnavailable = errors.New("TASK_OFFER_UNAVAILABLE")

func (a *App) PrepareTaskSuggestion(id, version string) (TaskSuggestionDraft, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ref := TaskOfferReference{ID: id, Version: version}
	view, err := a.mobileOffers(ctx)
	if err != nil {
		return TaskSuggestionDraft{}, errTaskOfferUnavailable
	}
	for _, offer := range view.Offers {
		if offer.ID == id && offer.Version == version && offer.Kind != "suggestion" {
			prompt, _, err := a.mobileOfferChatAdmission(ctx, offer.Prompt, []mobileOfferReference{ref})
			if err != nil {
				return TaskSuggestionDraft{}, errTaskOfferUnavailable
			}
			return TaskSuggestionDraft{Prompt: prompt, Reference: ref}, nil
		}
	}
	return TaskSuggestionDraft{}, errTaskOfferUnavailable
}

// Both chat surfaces publish offer-derived words only under the same source,
// file and policy fences. The prospective user row remains invisible until
// the final read and activity revision are protected through commit.
func (a *App) appendOfferChatTurn(ctx context.Context, store *storage.Store, tag string, row storage.StreamItemRow, originalPrompt string, refs []mobileOfferReference) (int, error) {
	reader := a.mobileOfferAdmissionReader(ctx)
	publication, err := a.beginMobileOfferPublication(ctx, reader)
	if err != nil {
		return 0, err
	}
	defer publication.release()
	return store.AppendStreamItemChecked(ctx, tag, row, func(readOnly *storage.Store) error {
		if mobileChatOfferCommitHook != nil {
			mobileChatOfferCommitHook()
		}
		reader.store = readOnly
		current, err := a.mobileOfferChatTextReadOnly(ctx, originalPrompt, refs, reader)
		if err != nil {
			return err
		}
		if current != row.Content {
			return errMobileOfferUnavailable
		}
		if err := publication.lockActivity(ctx); err != nil {
			return err
		}
		return publication.validateFiles(ctx)
	})
}

// SendPromptWithOfferReferences keeps edited offer text source-derived in
// ordinary desktop sessions, independently of the main-chat Today protocol.
func (a *App) SendPromptWithOfferReferences(prompt, workDir, model, sessionID, tag string, images []ImageData, files []FileData, planMode, thinkingEnabled bool, uiLanguage, traceID string, userItem StreamItemData, refs []TaskOfferReference) (int, error) {
	if err := a.chatSessionRefused(tag); err != nil {
		return -1, err
	}
	if a == nil || a.store == nil || len(refs) == 0 || !mobileOfferReferencesValid(refs) || userItem.ID == "" || userItem.Kind != "user" {
		return -1, errTaskOfferUnavailable
	}
	order, admitted := -1, false
	prepare := func(ctx context.Context) error {
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		session, err := a.store.LoadSession(ctx, tag)
		if err != nil || session == nil {
			return errTaskOfferUnavailable
		}
		text, provenance, err := a.mobileOfferChatAdmission(ctx, prompt, refs)
		if err != nil {
			return errTaskOfferUnavailable
		}
		order, err = a.appendOfferChatTurn(ctx, a.store, tag, storage.StreamItemRow{
			ID: userItem.ID, Kind: "user", Content: text, Images: userItem.Images, Files: userItem.Files, AutomationContext: provenance,
		}, prompt, refs)
		if err != nil {
			return errTaskOfferUnavailable
		}
		admitted = true
		a.mainChatRowsChanged(tag)
		a.emitBrowserEvent(mainChatItemsAppendedEvent, map[string]any{"sessionId": tag, "itemId": userItem.ID, "sortOrder": order, "kind": "user", "source": "offer"})
		return nil
	}
	err := a.sendPromptWithAttachments(prompt, workDir, model, sessionID, tag, images, files, planMode, thinkingEnabled, uiLanguage, traceID, nil, prepare)
	if admitted {
		return order, nil
	}
	if err != nil {
		return -1, errTaskOfferUnavailable
	}
	return -1, errTaskOfferUnavailable
}
