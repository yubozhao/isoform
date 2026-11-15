package main

import (
	"context"
	"time"

	"wails-gui/storage"
)

// mobileBriefMessageAskSource is an ask or a commitment read from Messages
// (imessage_capture.go): Isoform's own sentence about it, never message
// text. It reaches the writer as a conversation: a follow-up when it has a
// next step.
const mobileBriefMessageAskSource = "message_ask"

// mobileBriefMessageAsk is one kept ask as a brief candidate, when every gate
// still allows it: observing not paused (or a paused read the brief allows),
// the Messages switch on, Messages not protected, the person not left out,
// inside retention, and not from a paused stretch. It is checked when the
// brief is built and again whenever it is read.
func (a *App) mobileBriefMessageAsk(ctx context.Context, id string, now time.Time) (mobileBriefCandidate, bool) {
	if a == nil || a.store == nil || id == "" || a.IsClosedEyesModeEnabled() && !pausedBriefReadAllowed(ctx) {
		return mobileBriefCandidate{}, false
	}
	if !a.isPermissionEnabledContext(ctx, messagesPermissionKey) || imessageProtected() {
		return mobileBriefCandidate{}, false
	}
	row, found, err := a.store.GetMessageAsk(ctx, id)
	if err != nil || !found || row.At > now.UnixMilli() || row.At < now.Add(-storage.MessageAskRetention).UnixMilli() {
		return mobileBriefCandidate{}, false
	}
	left, err := peopleLeftOutSet(ctx, a.store)
	if err != nil || left[row.PrincipalID] {
		return mobileBriefCandidate{}, false
	}
	pauses, err := a.observationPausesContext(ctx)
	if err != nil || pausedAt(pauses, time.UnixMilli(row.At)) {
		return mobileBriefCandidate{}, false
	}
	return mobileBriefMessageAskCandidate(row), true
}

func mobileBriefMessageAskCandidate(row storage.MessageAskRow) mobileBriefCandidate {
	at := time.UnixMilli(row.At)
	c := mobileBriefCandidate{
		SourceKind: mobileBriefMessageAskSource, SourceID: row.ID,
		Title: row.Summary, Person: row.PersonName, Kind: "conversation", Status: "done",
		At: at, LastTouch: at, OpenedAt: at, writerDetail: row.Summary,
	}
	if row.NextStep != "" {
		c.NextStep = row.NextStep
		c.Status = "needs_reply"
	}
	c.rankedSourceDigest = mobileBriefModelDigest(row)
	return c
}

// collectMobileBriefMessageAsks adds the asks of the recap window to the
// conversations the writer sees, newest first, within the same limit as the
// chats read from the screen.
func collectMobileBriefMessageAsks(ctx context.Context, a *App, in *mobileBriefInput) {
	if a == nil || a.store == nil {
		return
	}
	rows, err := a.store.ListMessageAsks(ctx, in.recapStart().UnixMilli())
	if err != nil {
		return
	}
	for _, row := range rows {
		if len(in.Conversations) >= mobileBriefConversationLimit {
			return
		}
		if !in.inRecapWindow(time.UnixMilli(row.At)) {
			continue
		}
		if c, ok := a.mobileBriefMessageAsk(ctx, row.ID, in.AsOf); ok {
			in.Conversations = append(in.Conversations, c)
		}
	}
}
