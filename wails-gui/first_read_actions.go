package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"wails-gui/internal/engagement"
	"wails-gui/storage"
)

const firstReadUndoWindow = 5 * time.Second

type firstReadPortraitAnswer struct {
	MomentID        string `json:"momentId"`
	ConfirmedAt     int64  `json:"confirmedAt"`
	PreviousSaidAt  int64  `json:"previousSaidAt,omitempty"`
	PreviousVersion int64  `json:"previousVersion,omitempty"`
	Undone          bool   `json:"undone,omitempty"`
}

func firstReadAnswerKey(momentID string) string {
	return "first_read.answer." + engagement.HashPart(momentID)
}

func (a *App) loadFirstReadAnswer(ctx context.Context, momentID string) (*firstReadPortraitAnswer, error) {
	raw, err := a.store.GetUIState(ctx, firstReadAnswerKey(momentID))
	if err != nil || raw == "" {
		return nil, err
	}
	var answer firstReadPortraitAnswer
	if json.Unmarshal([]byte(raw), &answer) != nil || answer.MomentID != momentID || answer.ConfirmedAt <= 0 {
		return nil, errors.New("invalid confirmation receipt")
	}
	return &answer, nil
}

func (a *App) saveFirstReadAnswer(ctx context.Context, answer *firstReadPortraitAnswer) error {
	raw, err := json.Marshal(answer)
	if err != nil {
		return err
	}
	return a.store.SetUIState(ctx, firstReadAnswerKey(answer.MomentID), string(raw))
}

type FirstReadAnswerResult struct {
	State       string `json:"state"`
	ConfirmedAt int64  `json:"confirmedAt,omitempty"`
}

// AnswerFirstRead applies the person's explicit answer to the stored card.
// Text is accepted only for a correction and is also saved as a chat message.
func (a *App) AnswerFirstRead(momentID, action, text string) (FirstReadAnswerResult, error) {
	return a.answerFirstRead(context.Background(), momentID, action, text)
}

func (a *App) answerFirstRead(ctx context.Context, momentID, action, text string) (FirstReadAnswerResult, error) {
	var result FirstReadAnswerResult
	if a == nil || a.store == nil || strings.TrimSpace(momentID) == "" {
		return result, errors.New("first read unavailable")
	}
	mainChatProactiveActionMu.Lock()
	defer mainChatProactiveActionMu.Unlock()
	main, err := a.store.LoadMainSession(ctx)
	if err != nil || main == nil {
		return result, errors.New("first read unavailable")
	}
	row, err := a.store.FindProactiveStreamItem(ctx, main.LocalID, momentID)
	if err != nil || row == nil {
		return result, errors.New("first read unavailable")
	}
	var card mainChatProactive
	if json.Unmarshal([]byte(row.ToolInput), &card) != nil || card.Kind != "first_read" || card.MomentID != momentID {
		return result, errors.New("first read unavailable")
	}
	switch action {
	case "confirm_portrait":
		if card.State == "acted" && card.FirstReadConfirmedAt > 0 {
			return FirstReadAnswerResult{State: "acted", ConfirmedAt: card.FirstReadConfirmedAt}, nil
		}
		if card.State != "pending" && card.State != "opened" {
			return result, errors.New("first read was already answered")
		}
		at, err := a.confirmFirstRead(ctx, card)
		if err != nil {
			return result, err
		}
		card.State, card.FirstReadConfirmedAt = "acted", at
	case "undo_portrait":
		if card.State == "corrected" {
			// Not quite can be taken back within the same five seconds as That's me (P163).
			if err := a.undoFirstReadCorrection(ctx, card); err != nil {
				return result, err
			}
			card.State, card.FirstReadCorrectionHash, card.FirstReadCorrectedAt = "pending", "", 0
			break
		}
		if card.State == "pending" && card.FirstReadConfirmedAt == 0 {
			return FirstReadAnswerResult{State: "pending"}, nil
		}
		if card.State != "acted" || card.FirstReadConfirmedAt <= 0 {
			return result, errors.New("confirmation is unavailable")
		}
		if err := a.undoFirstRead(card); err != nil {
			return result, err
		}
		card.State, card.FirstReadConfirmedAt = "pending", 0
	case "correct_portrait":
		text = strings.TrimSpace(text)
		// A blank Not quite is allowed (P163 v1.0.1): it saves nothing and does not confirm the read.
		if len([]rune(text)) > 500 {
			return result, errors.New("correction must have at most 500 characters")
		}
		hash := agencyPayloadHash([]byte(text))
		if card.State == "corrected" {
			if card.FirstReadCorrectionHash == hash {
				return FirstReadAnswerResult{State: "corrected"}, nil
			}
			return result, errors.New("first read was already answered")
		}
		if card.State != "pending" && card.State != "opened" {
			return result, errors.New("first read was already answered")
		}
		if text == "" {
			if err := a.blankFirstRead(); err != nil {
				return result, err
			}
		} else if err := a.correctFirstRead(ctx, main.LocalID, card, text); err != nil {
			return result, err
		}
		card.State, card.FirstReadCorrectionHash = "corrected", hash
		card.FirstReadCorrectedAt = a.portraitService().now().UnixMilli()
	default:
		return result, errors.New("unknown first read answer")
	}
	payload, err := json.Marshal(card)
	if err != nil {
		return result, err
	}
	if err := a.store.UpdateStreamItemToolInput(ctx, row.ID, string(payload)); err != nil {
		return result, err
	}
	a.mainChatRowsChanged(main.LocalID)
	a.emitBrowserEvent(mainChatItemsAppendedEvent, map[string]any{"sessionId": main.LocalID, "itemId": row.ID, "kind": "system", "source": mainChatSourceEngagement})
	a.emitPortraitUpdated()
	if bridge := a.mobileBridgeForUse(); bridge != nil {
		bridge.publishChatTurn(main.LocalID, "", mobileChatEventDone)
	}
	return FirstReadAnswerResult{State: card.State, ConfirmedAt: card.FirstReadConfirmedAt}, nil
}

func (a *App) confirmFirstRead(ctx context.Context, card mainChatProactive) (int64, error) {
	s := a.portraitService()
	path, err := portraitPath()
	if err != nil {
		return 0, err
	}
	answer, err := a.loadFirstReadAnswer(ctx, card.MomentID)
	if err != nil {
		return 0, err
	}
	var confirmedAt int64
	err = s.withSources(ctx, func(entries []MemoryEntry) error {
		s.fileMu.Lock()
		defer s.fileMu.Unlock()
		doc, err := readPortrait(path)
		if err != nil || doc == nil || doc.GeneratedAt != card.FirstReadVersion {
			return errors.New("portrait changed")
		}
		live := liveInputs(doc.Inputs, entries)
		doc.keepSupported(live, confirmedIDs(entries))
		portrait := s.project(doc, live, entries)
		content := buildFirstRead(portrait, firstReadFacts{}, 0, "en")
		if content == nil || firstReadSentenceHash(content.Sentences) != card.FirstReadHash {
			return errors.New("portrait changed")
		}
		if answer != nil && !answer.Undone {
			confirmedAt = answer.ConfirmedAt
			if doc.SaidAt == answer.ConfirmedAt && doc.SaidVersion == card.FirstReadVersion {
				return nil
			}
			if doc.SaidAt != answer.PreviousSaidAt || doc.SaidVersion != answer.PreviousVersion {
				return errors.New("confirmation changed")
			}
		} else {
			confirmedAt = s.now().UnixMilli()
			if confirmedAt == doc.SaidAt {
				confirmedAt++
			}
			answer = &firstReadPortraitAnswer{MomentID: card.MomentID, ConfirmedAt: confirmedAt,
				PreviousSaidAt: doc.SaidAt, PreviousVersion: doc.SaidVersion}
			if err := a.saveFirstReadAnswer(ctx, answer); err != nil {
				return err
			}
		}
		doc.SaidAt, doc.SaidVersion = confirmedAt, doc.GeneratedAt
		return writePortrait(path, doc)
	})
	return confirmedAt, err
}

func (a *App) undoFirstRead(card mainChatProactive) error {
	s := a.portraitService()
	path, err := portraitPath()
	if err != nil {
		return err
	}
	ctx := context.Background()
	answer, err := a.loadFirstReadAnswer(ctx, card.MomentID)
	if err != nil || answer == nil || answer.ConfirmedAt != card.FirstReadConfirmedAt {
		return errors.New("confirmation is unavailable")
	}
	if answer.Undone {
		return nil
	}
	s.fileMu.Lock()
	defer s.fileMu.Unlock()
	doc, err := readPortrait(path)
	if err != nil || doc == nil {
		return errors.New("confirmation is unavailable")
	}
	alreadyRestored := doc.SaidAt == answer.PreviousSaidAt && doc.SaidVersion == answer.PreviousVersion
	if !alreadyRestored && s.now().After(time.UnixMilli(answer.ConfirmedAt).Add(firstReadUndoWindow)) {
		return errors.New("undo is no longer available")
	}
	if doc.SaidAt == answer.ConfirmedAt && doc.SaidVersion == card.FirstReadVersion {
		doc.SaidAt, doc.SaidVersion = answer.PreviousSaidAt, answer.PreviousVersion
		if err := writePortrait(path, doc); err != nil {
			return err
		}
	} else if !alreadyRestored {
		return errors.New("confirmation changed")
	}
	answer.Undone = true
	return a.saveFirstReadAnswer(ctx, answer)
}

// blankFirstRead is Not quite left blank: nothing is saved, and the portrait does not start from this read.
func (a *App) blankFirstRead() error {
	return a.portraitService().setSaid(0, false)
}

// undoFirstReadCorrection takes back a correction inside the undo window: the pending portrait note, the
// correction's memory entry and its chat message go, so the card can be answered again. A blank Not quite
// saved none of them.
func (a *App) undoFirstReadCorrection(ctx context.Context, card mainChatProactive) error {
	s := a.portraitService()
	if card.FirstReadCorrectedAt <= 0 || s.now().After(time.UnixMilli(card.FirstReadCorrectedAt).Add(firstReadUndoWindow)) {
		return errors.New("undo is no longer available")
	}
	if card.FirstReadCorrectionHash == agencyPayloadHash([]byte("")) {
		return nil
	}
	noteID := "first-read-correction-" + engagement.HashPart(card.MomentID)
	s.stateMu.Lock()
	if s.noteID == noteID {
		s.note, s.noteID, s.notQuite, s.force = "", "", false, false
	}
	s.stateMu.Unlock()
	if err := a.DeleteMemoryEntry(noteID); err != nil {
		return err
	}
	if err := a.store.DeleteStreamItemByID(ctx, "first-read-correction:"+engagement.HashPart(card.MomentID)); err != nil {
		return err
	}
	s.poke()
	return nil
}

func (a *App) correctFirstRead(ctx context.Context, chatID string, card mainChatProactive, text string) error {
	id := "first-read-correction:" + engagement.HashPart(card.MomentID)
	existing, err := a.store.LoadStreamItem(ctx, id)
	if err != nil {
		return err
	}
	if existing != nil {
		if existing.SessionLocalID != chatID || existing.Kind != "user" || existing.Content != text {
			return errors.New("correction changed")
		}
		return a.queuePortraitCorrection(text, "first-read-correction-"+engagement.HashPart(card.MomentID))
	}
	portrait, err := a.GetAboutYouPortrait()
	content := buildFirstRead(portrait, firstReadFacts{}, 0, "en")
	if err != nil || portrait.GeneratedAt != card.FirstReadVersion || content == nil || firstReadSentenceHash(content.Sentences) != card.FirstReadHash {
		return errors.New("portrait changed")
	}
	memoryID := "first-read-correction-" + engagement.HashPart(card.MomentID)
	line := strings.ReplaceAll(text, "\n", " ")
	if _, err := a.SaveMemoryEntry(memoryID, "# "+line+"\n\n## Memory Points\n\n- "+line+"\n"); err != nil {
		return err
	}
	if a.appendMainChatItem(ctx, chatID, storage.StreamItemRow{ID: id, Kind: "user", Content: text}, "chat") == "" {
		return fmt.Errorf("correction could not be added to chat")
	}
	return a.queuePortraitCorrection(text, memoryID)
}

func (bridge *mobileBridge) serveFirstReadAnswer(w http.ResponseWriter, r *http.Request, device mobileDeviceRecord) {
	var input struct {
		IdempotencyKey string `json:"idempotencyKey"`
		MomentID       string `json:"momentId"`
		Action         string `json:"action"`
		Text           string `json:"text,omitempty"`
	}
	if !mobileDecode(w, r, &input) {
		return
	}
	key, valid := mobileKey(device.DeviceID, input.IdempotencyKey)
	if !valid || input.MomentID != firstReadMoment(device.MobileDeviceView) {
		mobileJSON(w, http.StatusBadRequest, nil, "invalid first read or idempotency key")
		return
	}
	choice, _ := json.Marshal([]string{input.MomentID, input.Action, input.Text})
	route := r.Method + " " + r.URL.Path + " choice=" + agencyPayloadHash(choice)
	bridge.runReceipted(w, device, key, route, func(w http.ResponseWriter) {
		result, err := bridge.app.answerFirstRead(bridge.ctx, input.MomentID, input.Action, input.Text)
		if err != nil {
			mobileJSON(w, http.StatusConflict, nil, "the first read could not be updated")
			return
		}
		mobileJSON(w, http.StatusOK, result, "")
	})
}
