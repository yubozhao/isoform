package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"github.com/google/uuid"
	"wails-gui/internal/pii"
	"wails-gui/storage"
)

type TodayClosingAnswer struct {
	CardID         string `json:"cardId"`
	Label          string `json:"label"`
	QuestionDigest string `json:"questionDigest"`
}

func todayClosingAnswerKey(briefID, digest string) string {
	sum := sha256.Sum256([]byte(briefID + "\x00" + digest))
	return "today_closing_answer:" + hex.EncodeToString(sum[:])
}

func todayNonHoldClosing(brief MobileBriefV1) bool {
	question := mobileBriefClosingParagraph(brief)
	return question.Text != "" && question.Hold == nil && !mobileBriefHoldQuestion.MatchString(question.Text)
}

func (a *App) todayClosingAnswer(brief MobileBriefV1) *TodayClosingAnswer {
	if !featureOn(featureBriefExtras) {
		return nil
	}
	if a == nil || a.store == nil || !todayNonHoldClosing(brief) {
		return nil
	}
	digest := mobileBriefClosingDigest(brief)
	if digest == "" {
		return nil
	}
	id, err := a.store.GetUIState(context.Background(), todayClosingAnswerKey(brief.BriefID, digest))
	if err != nil || id == "" {
		return nil
	}
	for _, card := range mobileBriefClosingAnswers(brief, brief.Cards) {
		if card.ID == id {
			return &TodayClosingAnswer{CardID: id, Label: card.Label, QuestionDigest: digest}
		}
	}
	return nil
}

// PostTodayClosingAnswer posts exactly the chosen, still-readable answer and
// folds it in the same transaction. A stale choice changes neither record.
func (a *App) PostTodayClosingAnswer(briefID, cardID, shownMessage, previous string) (TodayClosingAnswer, error) {
	if !featureOn(featureBriefExtras) {
		return TodayClosingAnswer{}, errFeatureOff(featureBriefExtras)
	}
	var empty TodayClosingAnswer
	if a == nil || a.store == nil || briefID == "" || cardID == "" || shownMessage == "" {
		return empty, errTodayChatItemUnavailable
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	main, err := a.ensureMainChat(ctx)
	if err != nil {
		return empty, err
	}
	ref := TodayChatContextRef{SourceKind: todayChatBriefCardKind, SourceID: cardID, BriefID: briefID, GeneratedMessage: shownMessage}
	for _, lock := range []struct {
		try     func() bool
		release func()
	}{
		{a.closedEyes.mu.TryLock, a.closedEyes.mu.Unlock}, {memoryMutationMu.TryLock, memoryMutationMu.Unlock},
		{memoryFileLocks.publication.TryLock, memoryFileLocks.publication.Unlock}, {pii.TryLockSensitiveAppsForPublication, pii.UnlockSensitiveAppsForPublication},
	} {
		if err := lockTodayChatPublication(ctx, lock.try); err != nil {
			return empty, err
		}
		defer lock.release()
	}
	release, err := a.lockTodayChatFileServices(ctx, ref)
	if err != nil {
		return empty, err
	}
	defer release()
	files, err := a.readTodayChatAdmissionFiles(ctx, ref)
	if err != nil {
		return empty, err
	}
	revision, err := a.store.ReadRevision(ctx)
	if err != nil {
		return empty, err
	}
	var activityRevision storage.ReadRevision
	if a.activity != nil && a.activity.store != nil {
		activityRevision, err = storage.DatabaseRevision(ctx, a.activity.store.db)
		if err != nil {
			return empty, err
		}
	}
	item, sources, err := a.loadTodayChatContextItem(ctx, ref)
	if err != nil || todayChatCarriesStaleWords(shownMessage, item, ref) {
		return empty, errTodayChatItemUnavailable
	}
	var brief MobileBriefV1
	var ok bool
	if item.pausedBrief {
		brief, ok = a.todayDesktopPausedBrief(ctx, briefID)
	} else {
		brief, ok = a.todayChatDeliveredBriefFrom(ctx, briefID, &sources)
	}
	if !ok || !todayNonHoldClosing(brief) {
		return empty, errTodayChatItemUnavailable
	}
	var chosen MobileBriefCard
	for _, card := range mobileBriefClosingAnswers(brief, brief.Cards) {
		if card.ID == cardID && card.Message == shownMessage {
			chosen = card
			break
		}
	}
	if chosen.ID == "" {
		return empty, errors.New("closing answer unavailable")
	}
	digest := mobileBriefClosingDigest(brief)
	if a.activity != nil && a.activity.store != nil {
		unlock, err := storage.LockDatabaseRevision(ctx, a.activity.store.db, activityRevision)
		if err != nil {
			return empty, err
		}
		defer unlock()
	}
	id := uuid.NewString()
	order, err := a.store.PostUIStateMessageIfCurrent(ctx, todayClosingAnswerKey(briefID, digest), previous, cardID, shownMessage, main.LocalID, id, revision, func() error { return a.validateTodayChatAdmissionFiles(ctx, ref, files) })
	if err != nil {
		return empty, err
	}
	if a.telemetry != nil {
		a.telemetry.Capture("chat.turn_sent", todayTurnProperties(&todayTurnSource{action: "T12"}))
	}
	a.mainChatRowsChanged(main.LocalID)
	a.emitBrowserEvent(mainChatItemsAppendedEvent, map[string]any{"sessionId": main.LocalID, "itemId": id, "sortOrder": order, "kind": "user", "source": "today"})
	a.emitBrowserEvent("brief:stored", briefID)
	return TodayClosingAnswer{CardID: cardID, Label: chosen.Label, QuestionDigest: digest}, nil
}
