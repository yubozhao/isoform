package main

import (
	"context"
	"encoding/hex"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"wails-gui/storage"
)

const mobileBriefChangedMessage = "This brief changed. Refresh and try again."

// A saved Ask is a pointer to Chat rows, revalidated by the shared panel reader.
func (bridge *mobileBridge) serveBriefAskAnswer(w http.ResponseWriter, r *http.Request, briefID string) {
	if bridge.app == nil {
		mobileJSON(w, http.StatusServiceUnavailable, nil, "the computer is not ready yet")
		return
	}
	version := r.URL.Query().Get("version")
	query := r.URL.Query()
	quote, cardID, cardMessage := query.Get("text"), query.Get("cardId"), query.Get("cardMessage")
	weekTopicID, weekMessage := query.Get("weekTopicId"), query.Get("weekMessage")
	decoded, versionErr := hex.DecodeString(version)
	if !mobileValidBriefID(briefID) || versionErr != nil || len(decoded) != 32 {
		mobileJSON(w, http.StatusBadRequest, nil, "the brief reference is not valid")
		return
	}
	input := mobileBriefChatInput{BriefID: briefID, BriefVersion: version, Ask: true}
	var text string
	switch {
	case weekTopicID != "" && mobileValidID(weekTopicID) && strings.TrimSpace(weekMessage) != "" && len(weekMessage) <= 2*mobileBriefCardMessageBytes && cardID == "" && cardMessage == "" && quote == "" && query.Get("paragraph") == "":
		input.WeekTopicID, input.WeekMessage, text = weekTopicID, weekMessage, weekMessage
	case weekTopicID == "" && weekMessage == "" && cardID != "" && mobileValidID(cardID) && strings.TrimSpace(cardMessage) != "" && len(cardMessage) <= 2*mobileBriefCardMessageBytes && quote == "" && query.Get("paragraph") == "":
		input.CardID, input.CardMessage, text = cardID, cardMessage, cardMessage
	case weekTopicID == "" && weekMessage == "" && cardID == "" && cardMessage == "":
		paragraph, err := strconv.Atoi(query.Get("paragraph"))
		if err != nil || paragraph < 0 || strings.TrimSpace(quote) == "" || len(quote) > mobileChatBriefMax {
			mobileJSON(w, http.StatusBadRequest, nil, "the brief reference is not valid")
			return
		}
		input.Paragraph, input.ParagraphText = &paragraph, quote
	default:
		mobileJSON(w, http.StatusBadRequest, nil, "the brief reference is not valid")
		return
	}
	ref, _, err := bridge.app.mobileBriefChatReference(input, text)
	if err != nil {
		mobileJSON(w, http.StatusConflict, nil, mobileBriefChangedMessage)
		return
	}
	answer, err := bridge.app.GetTodayPanelAnswer(ref)
	if err != nil {
		mobileJSON(w, http.StatusInternalServerError, nil, "the answer could not be read")
		return
	}
	mobileJSON(w, http.StatusOK, map[string]any{"answer": answer}, "")
}

type mobileBriefChatInput struct {
	IdempotencyKey string `json:"idempotencyKey"`
	Text           string `json:"text"`
	BriefID        string `json:"briefId"`
	BriefVersion   string `json:"briefVersion"`
	OfferID        string `json:"offerId,omitempty"`
	// CardID names one of the brief's send cards, a button or a row's
	// question (backlog item 64): its words were put in the phone's box, and
	// Text is what the user sends, edited or not. CardMessage is the card's
	// words as the phone showed them; sent unedited after they stopped
	// standing, they are refused.
	CardID        string `json:"cardId,omitempty"`
	CardMessage   string `json:"cardMessage,omitempty"`
	Ask           bool   `json:"ask,omitempty"`
	Paragraph     *int   `json:"paragraph,omitempty"`
	ParagraphText string `json:"paragraphText,omitempty"`
	WeekTopicID   string `json:"weekTopicId,omitempty"`
	WeekMessage   string `json:"weekMessage,omitempty"`
}

// The phone supplies identities and editable text only. The computer resolves
// its own brief, evidence and optional offer through the same gates as Today.
func (bridge *mobileBridge) serveBriefChatMessage(w http.ResponseWriter, r *http.Request, device mobileDeviceRecord, chatID string) {
	if !mobileValidID(chatID) {
		mobileJSON(w, http.StatusBadRequest, nil, "the chat id is not valid")
		return
	}
	var input mobileBriefChatInput
	if !mobileDecode(w, r, &input) {
		return
	}
	key, valid := mobileKey(device.DeviceID, input.IdempotencyKey)
	if !valid {
		mobileJSON(w, http.StatusBadRequest, nil, "an idempotency key of 1 to 128 bytes is required")
		return
	}
	text := strings.TrimSpace(input.Text)
	if text == "" || len(input.Text) > mobileChatInputMax {
		mobileJSON(w, http.StatusBadRequest, nil, "the message must be 1 to 16384 bytes")
		return
	}
	version, err := hex.DecodeString(input.BriefVersion)
	if !mobileValidBriefID(input.BriefID) || err != nil || len(version) != 32 || (input.OfferID != "" && !mobileValidID(input.OfferID)) ||
		(input.CardID != "" && (!mobileValidID(input.CardID) || strings.TrimSpace(input.CardMessage) == "")) || len(input.CardMessage) > 2*mobileBriefCardMessageBytes ||
		(input.Paragraph != nil && (!input.Ask || input.CardID != "" || input.OfferID != "" || strings.TrimSpace(input.ParagraphText) == "")) ||
		(input.Paragraph == nil && input.ParagraphText != "") || len(input.ParagraphText) > mobileChatBriefMax ||
		(input.WeekTopicID != "" && (!input.Ask || !mobileValidID(input.WeekTopicID) || input.CardID != "" || input.OfferID != "" || input.Paragraph != nil || strings.TrimSpace(input.WeekMessage) == "")) ||
		(input.WeekTopicID == "" && input.WeekMessage != "") || len(input.WeekMessage) > 2*mobileBriefCardMessageBytes {
		mobileJSON(w, http.StatusBadRequest, nil, "the brief reference is not valid")
		return
	}
	bridge.runReceipted(w, device, key, r.Method+" "+r.URL.Path, func(w http.ResponseWriter) {
		store := bridge.app.storeForMobile()
		if store == nil {
			mobileJSON(w, http.StatusServiceUnavailable, nil, errMobileChatNotReady.Error())
			return
		}
		row, found, err := store.MobileChat(bridge.ctx, chatID)
		if err != nil {
			mobileJSON(w, http.StatusInternalServerError, nil, "the chat could not be read")
			return
		}
		if !found || !bridge.app.isMainChatSession(bridge.ctx, chatID) {
			mobileJSON(w, http.StatusNotFound, nil, "this brief can only be discussed in the chat on your computer")
			return
		}
		turnID, seq, err := bridge.startBriefChatTurn(row, text, input)
		if errors.Is(err, errTodayChatItemUnavailable) && !input.Ask && bridge.app.mobileBriefTextStandsAlone(input) {
			// The brief or card the phone showed moved on, but the person's
			// words are a question of their own ("Where do I stand on ...?").
			// They are answered as a plain turn in the same chat, with none of
			// the old brief's context attached, rather than refused.
			turnID, seq, err = bridge.startChatTurn(row, text, nil)
		}
		switch true {
		case err == nil:
			mobileJSON(w, http.StatusAccepted, map[string]any{"turnId": turnID, "seq": seq}, "")
		case errors.Is(err, errPromptAlreadyRunning), errors.Is(err, errMobileChatBusy):
			mobileJSON(w, http.StatusConflict, nil, "busy")
		case errors.Is(err, errMobileChatWorkDir):
			mobileJSON(w, http.StatusConflict, nil, errMobileChatWorkDir.Error())
		case errors.Is(err, errChatSourceAccessChanged):
			mobileJSON(w, http.StatusConflict, nil, errChatSourceAccessChanged.Error())
		case errors.Is(err, errTodayChatItemUnavailable), errors.Is(err, errTodayChatObservationPaused):
			mobileJSON(w, http.StatusConflict, nil, mobileBriefChangedMessage)
		default:
			log.Printf("[mobile-brief-chat] send failed: %v", err)
			mobileJSON(w, http.StatusInternalServerError, nil, "the message could not be sent")
		}
	})
}

type mobileBriefOpenerInput struct {
	IdempotencyKey string `json:"idempotencyKey"`
	BriefID        string `json:"briefId"`
	BriefVersion   string `json:"briefVersion"`
}

// "Talk about this brief" on the phone does what it does on the computer
// (backlog item 58): Isoform says the brief's opener first in the main chat,
// once, and the user's answer goes through brief-messages with the brief. The
// phone sends identities only; the opener's words are the computer's.
func (bridge *mobileBridge) serveBriefOpener(w http.ResponseWriter, r *http.Request, device mobileDeviceRecord, chatID string) {
	if !mobileValidID(chatID) {
		mobileJSON(w, http.StatusBadRequest, nil, "the chat id is not valid")
		return
	}
	var input mobileBriefOpenerInput
	if !mobileDecode(w, r, &input) {
		return
	}
	key, valid := mobileKey(device.DeviceID, input.IdempotencyKey)
	if !valid {
		mobileJSON(w, http.StatusBadRequest, nil, "an idempotency key of 1 to 128 bytes is required")
		return
	}
	version, err := hex.DecodeString(input.BriefVersion)
	if !mobileValidBriefID(input.BriefID) || err != nil || len(version) != 32 {
		mobileJSON(w, http.StatusBadRequest, nil, "the brief reference is not valid")
		return
	}
	bridge.runReceipted(w, device, key, r.Method+" "+r.URL.Path, func(w http.ResponseWriter) {
		if bridge.app.storeForMobile() == nil {
			mobileJSON(w, http.StatusServiceUnavailable, nil, errMobileChatNotReady.Error())
			return
		}
		if !bridge.app.isMainChatSession(bridge.ctx, chatID) {
			mobileJSON(w, http.StatusNotFound, nil, "this brief can only be discussed in the chat on your computer")
			return
		}
		brief, message, err := bridge.app.todayBriefOpener(bridge.ctx, input.BriefID, input.BriefVersion)
		if err == nil {
			err = bridge.app.postTodayBriefOpener(bridge.ctx, chatID, message, "today_brief_opener")
		}
		switch true {
		case err == nil:
			var answers []MobileBriefCard
			for _, card := range brief.Cards {
				if card.Kind == mobileBriefCardOpenerAnswer {
					answers = append(answers, card)
				}
			}
			mobileJSON(w, http.StatusOK, map[string]any{"posted": true, "message": message, "answers": answers}, "")
		case errors.Is(err, errTodayChatObservationPaused), errors.Is(err, errTodayChatItemUnavailable):
			mobileJSON(w, http.StatusConflict, nil, mobileBriefChangedMessage)
		default:
			log.Printf("[mobile-brief-chat] opener failed: %v", err)
			mobileJSON(w, http.StatusInternalServerError, nil, "the brief could not be opened")
		}
	})
}

type mobileBriefParagraphInput struct {
	IdempotencyKey string `json:"idempotencyKey"`
	BriefID        string `json:"briefId"`
	BriefVersion   string `json:"briefVersion"`
	Paragraph      *int   `json:"paragraph"`
	// Text is the paragraph as the phone showed it. A phone sends it only
	// when it asks again after a 409, so an older computer never sees it.
	Text string `json:"text,omitempty"`
}

func (bridge *mobileBridge) serveBriefParagraph(w http.ResponseWriter, r *http.Request, device mobileDeviceRecord, chatID string) {
	if !mobileValidID(chatID) {
		mobileJSON(w, http.StatusBadRequest, nil, "the chat id is not valid")
		return
	}
	var input mobileBriefParagraphInput
	if !mobileDecode(w, r, &input) {
		return
	}
	key, valid := mobileKey(device.DeviceID, input.IdempotencyKey)
	if !valid {
		mobileJSON(w, http.StatusBadRequest, nil, "an idempotency key of 1 to 128 bytes is required")
		return
	}
	version, err := hex.DecodeString(input.BriefVersion)
	if !mobileValidBriefID(input.BriefID) || err != nil || len(version) != 32 || input.Paragraph == nil {
		mobileJSON(w, http.StatusBadRequest, nil, "the brief reference is not valid")
		return
	}
	bridge.runReceipted(w, device, key, r.Method+" "+r.URL.Path, func(w http.ResponseWriter) {
		if bridge.app.storeForMobile() == nil {
			mobileJSON(w, http.StatusServiceUnavailable, nil, errMobileChatNotReady.Error())
			return
		}
		if !bridge.app.isMainChatSession(bridge.ctx, chatID) {
			mobileJSON(w, http.StatusNotFound, nil, "this brief can only be discussed in the chat on your computer")
			return
		}
		_, message, err := bridge.app.todayBriefParagraphMessage(
			bridge.ctx, input.BriefID, input.BriefVersion, *input.Paragraph, "", false,
		)
		if errors.Is(err, errTodayChatItemUnavailable) && strings.TrimSpace(input.Text) != "" {
			// The revision moved since the phone drew the brief. The quote is
			// Isoform's own words, so it is posted only when the brief as
			// delivered now still says exactly what the phone showed; a
			// paragraph that changed or went away keeps the 409, since quoting
			// it would put words in the chat that the brief no longer says.
			_, message, err = bridge.app.todayBriefParagraphMessage(
				bridge.ctx, input.BriefID, "", *input.Paragraph, input.Text, true,
			)
		}
		if err == nil {
			err = bridge.app.postTodayBriefOpener(bridge.ctx, chatID, message, "today_brief_paragraph")
		}
		switch true {
		case err == nil:
			mobileJSON(w, http.StatusOK, map[string]bool{"posted": true}, "")
		case errors.Is(err, errTodayChatObservationPaused), errors.Is(err, errTodayChatItemUnavailable):
			mobileJSON(w, http.StatusConflict, nil, mobileBriefChangedMessage)
		default:
			log.Printf("[mobile-brief-chat] paragraph opener failed: %v", err)
			mobileJSON(w, http.StatusInternalServerError, nil, "the brief paragraph could not be opened")
		}
	})
}

// mobileBriefTextStandsAlone reports whether a brief message that no longer
// matches its brief may still be answered as a plain chat turn: the person's
// own words, from a brief the computer still delivers with its words, while
// observation is on. A legacy offer tap stays refused, because the computer
// sends the offer's words there, never the phone's. A brief that delivery
// withdrew (a private or deleted source) stays refused, because its words
// came from what is no longer readable.
func (a *App) mobileBriefTextStandsAlone(input mobileBriefChatInput) bool {
	if input.OfferID != "" || a.IsClosedEyesModeEnabled() {
		return false
	}
	brief, ok := a.todayChatDeliveredBrief(input.BriefID)
	return ok && mobileBriefHasWords(brief)
}

func mobileBriefHasWords(brief MobileBriefV1) bool {
	for _, paragraph := range brief.Narrative {
		if strings.TrimSpace(paragraph.Text) != "" {
			return true
		}
	}
	return false
}

func (bridge *mobileBridge) startBriefChatTurn(row storage.MobileChatRow, prompt string, input mobileBriefChatInput) (string, int64, error) {
	chat := bridge.chat
	chat.sendMu.Lock()
	defer chat.sendMu.Unlock()
	if bridge.chatRunning(row.ID) {
		return "", 0, errMobileChatBusy
	}
	ref, prompt, err := bridge.app.mobileBriefChatReference(input, prompt)
	if err != nil {
		return "", 0, err
	}
	workDir, err := mobileChatWorkDir(row)
	if err != nil {
		return "", 0, err
	}
	model := row.Model
	if model == "" {
		model = bridge.app.chatTaskModel()
	}
	turnID := uuid.NewString()
	onAdmitted := func(order int) {
		bridge.app.mu.Lock()
		cancel := bridge.app.tagCancels[row.ID]
		bridge.app.mu.Unlock()
		chat.mu.Lock()
		chat.turns[row.ID] = turnID
		chat.replies[row.ID] = &mobileChatReply{turnID: turnID, userOrder: order, ask: input.Ask, cancel: cancel}
		delete(chat.errored, row.ID)
		chat.mu.Unlock()
		bridge.app.notifySpotlightSessionChanged(row.ID)
	}
	origin := promptOriginPhone
	if input.Ask {
		origin = promptOriginPhoneAsk
	}
	order, err := bridge.app.sendPromptWithTodayContext(prompt, workDir, model, row.SessionID, row.ID, nil, nil, row.PlanMode, row.ThinkingEnabled, "", "", StreamItemData{ID: uuid.NewString(), Kind: "user"}, ref, origin, onAdmitted)
	if err != nil {
		return "", 0, err
	}
	return turnID, int64(order) + 1, nil
}

// A tap on a brief card is the one send route Today uses (backlog items 56
// and 64). With cardId the phone put the card's words in its box, and the
// user's text, edited or not, goes with the card's sources read again. A
// legacy offerId tap sends the card's current words, never the phone's text.
// A brief message with neither keeps the user's own text.
// todayChatBriefForVersion is the brief revision a phone or Today named by its
// conversation key: the live read, or the sealed read the phone was given. The
// phone reads the sealed copy (seal v2) while a Today refresh renumbers the
// stored brief's evidence, so the two keys differ for the same words; checking
// the live read alone answered every card Ask with 409 (Nov 14).
func (a *App) todayChatBriefForVersion(briefID, version string) (MobileBriefV1, bool) {
	if brief, ok := a.todayChatDeliveredBrief(briefID); ok && brief.ConversationKey == version {
		return brief, true
	}
	if sealed, found := a.todayChatSealedBrief(briefID); found && sealed.ConversationKey == version {
		return sealed, true
	}
	return MobileBriefV1{}, false
}

func (a *App) mobileBriefChatReference(input mobileBriefChatInput, text string) (TodayChatContextRef, string, error) {
	ref := TodayChatContextRef{SourceKind: "brief", SourceID: input.BriefVersion, BriefID: input.BriefID}
	if input.WeekTopicID != "" {
		brief, ok := a.todayChatBriefForVersion(input.BriefID, input.BriefVersion)
		if !ok || brief.YourWeek == nil || brief.YourWeek.Ask == nil ||
			brief.YourWeek.Ask.TopicID != input.WeekTopicID || brief.YourWeek.Ask.Message != input.WeekMessage {
			return ref, text, errTodayChatItemUnavailable
		}
		ref.WeekAsk = true
		ref.ParagraphDigest = mobileBriefModelDigest(input.WeekTopicID + "\x00" + input.WeekMessage)
		return ref, text, nil
	}
	if input.Paragraph != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _, err := a.todayBriefParagraphMessage(ctx, input.BriefID, input.BriefVersion, *input.Paragraph, input.ParagraphText, true)
		if err != nil {
			return ref, text, err
		}
		ref.ParagraphDigest = mobileBriefModelDigest(strings.TrimSpace(input.ParagraphText))
		return ref, text, nil
	}
	cardID := input.CardID
	if cardID == "" {
		cardID = input.OfferID
	}
	if cardID == "" {
		return ref, text, nil
	}
	// Card IDs are scoped to the brief revision the phone showed: a tap on an
	// older revision may name a card that now does something else.
	brief, ok := a.todayChatBriefForVersion(input.BriefID, input.BriefVersion)
	if !ok {
		return ref, text, errTodayChatItemUnavailable
	}
	card, ok := mobileBriefFindCard(brief, cardID)
	if !ok && input.CardID == "" {
		card, ok = mobileBriefLegacyOfferCard(brief, cardID, a.currentUILanguageName())
	}
	if !ok || card.Mode != mobileBriefCardSend || input.CardID == "" && card.Kind != mobileBriefCardAction {
		return ref, text, errTodayChatItemUnavailable
	}
	ref = TodayChatContextRef{SourceKind: todayChatBriefCardKind, SourceID: card.ID, BriefID: brief.BriefID, GeneratedMessage: input.CardMessage}
	// The card must still stand, from the same read the send repeats.
	draft, err := a.PrepareTodayChatDraft(ref)
	if err != nil {
		return ref, text, err
	}
	if input.CardID == "" {
		return ref, draft.Message, nil
	}
	// The user's words, checked and named as on the computer's Send.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	item, _, err := a.loadTodayChatContextItem(ctx, ref)
	if err != nil {
		return ref, text, err
	}
	text, err = todayChatCheckedWords(text, item, ref)
	return ref, text, err
}
