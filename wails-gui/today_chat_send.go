package main

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"wails-gui/storage"
)

// PrepareTodayChatTurn performs an early read of the editable draft. Final
// send admission repeats the source gates before publishing any user history,
// including titles whose protected provenance is behind a Purpose reference.
func (a *App) PrepareTodayChatTurn(tag string, prompt string, ref TodayChatContextRef) (string, error) {
	turnCtx, finish, err := a.beginChatTurn(tag)
	if err != nil {
		return "", err
	}
	defer a.finishChatPreparation(tag, finish)
	ctx, cancel := context.WithTimeout(turnCtx, 10*time.Second)
	defer cancel()
	if !a.isMainChatSession(ctx, tag) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		return "", errTodayChatItemUnavailable
	}
	item, _, err := a.loadTodayChatContextItem(ctx, ref)
	if err != nil {
		return "", err
	}
	if ref.GeneratedFingerprint != "" && ref.GeneratedFingerprint != todayChatPrefillFingerprint(todayChatPreparedPrefill(item)) {
		return "", errTodayChatItemUnavailable
	}
	if len(ref.GeneratedPeople) > 0 && !slices.Equal(ref.GeneratedPeople, todayChatSafePeople(item.Prefill.People)) {
		return "", errTodayChatItemUnavailable
	}
	prompt, err = todayChatCheckedWords(prompt, item, ref)
	if err != nil {
		return "", err
	}
	return prompt, ctx.Err()
}

// todayChatCheckedWords is the visible request as it may be sent: refused when
// it is a card's words sent unedited after they stopped standing, otherwise
// with any stale generated label replaced by the item's current one, keeping
// the user's surrounding wording and additions. The order matters: replacing
// first would let unedited stale words pass.
func todayChatCheckedWords(prompt string, item todayChatItem, ref TodayChatContextRef) (string, error) {
	if todayChatCarriesStaleWords(prompt, item, ref) {
		return "", errTodayChatItemUnavailable
	}
	currentLabel := todayChatItemLabel(item)
	for _, prior := range append(item.PriorLabels, ref.GeneratedTitle) {
		if prior != "" && prior != currentLabel {
			prompt = strings.ReplaceAll(prompt, prior, currentLabel)
		}
	}
	return todayChatRedactPrompt(prompt), nil
}

// todayChatRedactPrompt is the redaction for the visible Today request:
// PII and codes are removed as at every Today output boundary, while dates
// and years stay so "due 2026-10-20" reads the same in the bubble.
func todayChatRedactPrompt(prompt string) string {
	return todayChatRedactKeepingDates(prompt)
}

type todayChatTurnRejected struct{ cause error }

func (e todayChatTurnRejected) Error() string        { return errTodayChatItemUnavailable.Error() }
func (e todayChatTurnRejected) Unwrap() error        { return e.cause }
func (e todayChatTurnRejected) Is(target error) bool { return target == errTodayChatItemUnavailable }

// SendPromptWithTodayContext resolves a Today reference only when the user
// sends. The evidence bundle stays out of the visible user bubble and any
// process-global pending selection, so another chat cannot consume it.
func (a *App) SendPromptWithTodayContext(prompt string, workDir string, model string, sessionID string, tag string, images []ImageData, files []FileData, planMode bool, thinkingEnabled bool, uiLanguage string, traceID string, userItem StreamItemData, ref TodayChatContextRef) (int, error) {
	return a.sendPromptWithTodayContext(prompt, workDir, model, sessionID, tag, images, files, planMode, thinkingEnabled, uiLanguage, traceID, userItem, ref, promptOriginMac, nil)
}

// The phone shares the computer's source resolution and atomic admission, but
// retains its own origin so brief context cannot grant computer-only tools.
func (a *App) sendPromptWithTodayContext(prompt string, workDir string, model string, sessionID string, tag string, images []ImageData, files []FileData, planMode bool, thinkingEnabled bool, uiLanguage string, traceID string, userItem StreamItemData, ref TodayChatContextRef, origin promptOrigin, onAdmitted func(int)) (int, error) {
	// Direct callers must get the same redaction as draft preparation.
	prompt = todayChatRedactPrompt(prompt)
	var result todayChatContextResult
	order, admitted := -1, false
	if userItem.ID == "" || userItem.Kind != "user" {
		return order, errTodayChatItemUnavailable
	}
	prepare := func(turnCtx context.Context) (err error) {
		ctx, cancel := context.WithTimeout(turnCtx, 10*time.Second)
		defer cancel()
		if origin == promptOriginMac && ref.SourceKind == "topic" && a.IsClosedEyesModeEnabled() {
			ctx = context.WithValue(ctx, pausedTopicReadKey{}, true)
		}
		if !a.isMainChatSession(ctx, tag) {
			if err := ctx.Err(); err != nil {
				return err
			}
			return fmt.Errorf("Today context requires the chat")
		}
		result, err = a.buildTodayChatContext(ctx, ref)
		if err != nil {
			return err
		}
		// Retrieval may have waited while sources were changed or withdrawn.
		// Re-read and protect the generated facts through the history commit.
		content := todayChatRedactPrompt(prompt)
		result, order, err = a.admitTodayChatTurn(ctx, tag, storage.StreamItemRow{
			ID: userItem.ID, Kind: "user", Content: content,
			Images: userItem.Images, Files: userItem.Files, TopicID: todayChatTopicID(ref),
		}, ref, result.Related, isPanelAskOrigin(origin))
		if err == nil {
			admitted = true
			if binding := topicTurnFromContext(turnCtx); binding != nil {
				binding.correctionRef = &ref
			}
			if origin == promptOriginPanelAsk || origin == promptOriginMac {
				setTodayTurnSource(turnCtx, ref.TelemetryAction)
			}
			if isPanelAskOrigin(origin) {
				a.beginPanelAskAnswer(ref, userItem.ID)
			}
			if onAdmitted != nil {
				onAdmitted(order)
			}
			if result.topicMetadata != "" {
				if binding := topicTurnFromContext(turnCtx); binding != nil {
					binding.userItemID = userItem.ID
					binding.metadata = result.topicMetadata
					if pausedTopicReadAllowed(ctx) {
						since, _ := a.observationPauseCoverageStart()
						binding.pausedAt.Store(since)
					}
				}
			}
			a.mainChatRowsChanged(tag)
			a.emitBrowserEvent(mainChatItemsAppendedEvent, map[string]any{"sessionId": tag, "itemId": userItem.ID, "sortOrder": order, "kind": "user", "source": "today"})
			_ = a.observeCommunicationMessage(communicationMessage{Source: "isoform_chat", App: "Isoform", MessageID: userItem.ID, AuthorID: "owner", OwnerID: "owner", Text: content, SentAt: time.Now(), BodyAvailable: true})
		}
		return err
	}
	var posted sync.Once
	composer := func(query, language string) string {
		// Keep normal tool and assistant instructions, with the item-specific
		// bundle replacing generic memory, people and recent-activity context.
		composed := a.composePromptFor(query, true, tag, "", skillHubDomainChat, language, string(origin), "today")
		if ref.SourceKind != todayChatPausedKind {
			// A paused draft attached nothing, so it gets no context receipt.
			posted.Do(func() {
				a.recordContextEntries(result.Uses, "today_chat", tag)
				title := result.Title
				if ref.AboutTitle != "" {
					title = ref.AboutTitle
				}
				a.postTodayContextCounts(tag, userItem.ID, result.Counts, title, ref.SourceKind, ref.AboutID)
			})
		}
		return result.Text + composed
	}
	var err error
	if origin == promptOriginPhone || isPanelAskOrigin(origin) {
		kickoff := ""
		if isPanelAskOrigin(origin) {
			kickoff = "agentloop.user.send_clicked"
		}
		err = a.executePrompt(prompt, workDir, model, sessionID, tag, nil, planMode, thinkingEnabled, uiLanguage, nil, composer, traceID, kickoff, origin, prepare)
	} else {
		err = a.sendPromptWithAttachments(prompt, workDir, model, sessionID, tag, images, files, planMode, thinkingEnabled, uiLanguage, traceID, composer, prepare)
	}
	if admitted {
		if err != nil && isPanelAskOrigin(origin) {
			a.cancelPanelAskAnswer(userItem.ID)
		}
		// executePrompt can observe Stop immediately after commit, before its
		// runner starts. The accepted user intent remains like normal chat.
		if err != nil && (origin == promptOriginPhone || origin == promptOriginPhoneAsk) {
			a.emitAgentEvent(AgentEvent{Type: "error", Tag: tag, Error: "The computer could not continue this message. Try again."})
			a.emitAgentEvent(AgentEvent{Type: "close", Tag: tag})
		}
		return order, nil
	}
	if err != nil {
		return -1, todayChatTurnRejected{cause: err}
	}
	return -1, errTodayChatItemUnavailable
}

// Counts are desktop-only metadata. The dedicated kind stays out of prompt
// history and the phone's existing projection of user-visible chat rows. The
// receipt also names what the message was attached to, the title the draft
// already showed, so the thread reads "About: {title}" (phone-chat v1.0.0
// rule 10, phone-map B10).
func (a *App) postTodayContextCounts(tag string, userItemID string, counts TodayChatContextCounts, about, aboutKind, aboutID string) {
	if a == nil || a.store == nil {
		return
	}
	payload, err := json.Marshal(counts)
	if err != nil {
		return
	}
	receipt := map[string]string{"userItemId": userItemID}
	if about = strings.TrimSpace(about); about != "" && aboutKind != todayChatPausedKind {
		receipt["about"] = truncateAtRuneBoundary(about, 400)
		receipt["aboutKind"] = aboutKind
		if aboutID != "" {
			receipt["aboutId"] = aboutID
		}
	}
	turn, _ := json.Marshal(receipt)
	a.appendMainChatItem(context.Background(), tag, storage.StreamItemRow{
		ID: uuid.NewString(), Kind: "context_status", ToolName: "today_context", Content: string(payload), ToolInput: string(turn),
	}, "today_context")
}
