package main

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"wails-gui/storage"
)

// Tests coordinate a retry waiting for send serialization. Production leaves nil.
var mobileTopicSendBeforeLock func()

// Phone projections contain display data only, never source refs, attachments,
// executor metadata or the desktop renderer's internal fields.
type mobileTopicView struct {
	ID         string           `json:"id"`
	Kind       string           `json:"kind"`
	Name       string           `json:"name"`
	Status     string           `json:"status"`
	Latest     *TopicMoment     `json:"latest,omitempty"`
	Next       *TopicMoment     `json:"next,omitempty"`
	PeopleLine *TopicPeopleLine `json:"peopleLine,omitempty"`
	Why        *TopicWhy        `json:"why,omitempty"`
	NewStories int              `json:"newStories,omitempty"`
}

type mobileTopicListView struct {
	Topics     []mobileTopicView `json:"topics"`
	Refreshing bool              `json:"refreshing,omitempty"`
	HasTopics  bool              `json:"hasTopics,omitempty"`
}

type mobileTopicItem struct {
	ID      string              `json:"id"`
	Kind    string              `json:"kind"`
	At      int64               `json:"at"`
	Actor   string              `json:"actor,omitempty"`
	Text    string              `json:"text,omitempty"`
	Message *mobileChatItemView `json:"message,omitempty"`
}

type mobileTopicPage struct {
	SessionID  string            `json:"sessionId"`
	TopicID    string            `json:"topicId"`
	Items      []mobileTopicItem `json:"items"`
	HasMore    bool              `json:"hasMore"`
	NextCursor string            `json:"nextCursor"`
}

func mobileTopicProjection(topic TopicView) mobileTopicView {
	view := mobileTopicView{ID: topic.ID, Kind: topic.Kind, Name: mobileChatBound(topic.Name, 160), Status: topic.Status, NewStories: topic.NewStories}
	moment := func(value *TopicMoment) *TopicMoment {
		if value == nil {
			return nil
		}
		return &TopicMoment{Text: mobileChatBound(value.Text, 384), At: mobileChatClip(value.At, 64)}
	}
	view.Latest, view.Next = moment(topic.Latest), moment(topic.Next)
	if topic.Why != nil {
		view.Why = &TopicWhy{LastSeenAt: mobileChatClip(topic.Why.LastSeenAt, 64), InterestWhy: mobileChatBound(topic.Why.InterestWhy, 384)}
	}
	if topic.PeopleLine != nil {
		view.PeopleLine = &TopicPeopleLine{Count: topic.PeopleLine.Count, Names: []string{}}
		for _, name := range mobileBounded(topic.PeopleLine.Names, topicRegistryPeopleMax) {
			view.PeopleLine.Names = append(view.PeopleLine.Names, mobileChatBound(name, 96))
		}
	}
	return view
}

// Count the complete JSON response after the second escape layer used by the
// envelope's body string. The existing 48 KiB chat budget reserves room for the
// request UUID and response wrapper inside the 64 KiB message plaintext limit.
func mobileTopicResponseFits(data any) bool {
	body, err := json.Marshal(mobileResponse{OK: true, Data: data})
	if err != nil {
		return false
	}
	escaped, err := json.Marshal(string(body))
	return err == nil && len(escaped) <= mobileChatPageBudget
}

func (bridge *mobileBridge) serveTopicList(w http.ResponseWriter) {
	if bridge.app.storeForMobile() == nil {
		mobileJSON(w, http.StatusServiceUnavailable, nil, errMobileChatNotReady.Error())
		return
	}
	topics := []mobileTopicView{}
	refreshing, hasTopics := false, false
	if !bridge.app.IsClosedEyesModeEnabled() {
		view, err := bridge.app.GetTodayTopics()
		if err != nil {
			mobileTopicError(w, err)
			return
		}
		refreshing, hasTopics = view.Refreshing, view.HasTopics
		for _, topic := range mobileBounded(view.Topics, topicRegistryLimit) {
			topics = append(topics, mobileTopicProjection(topic))
		}
	}
	data := func() mobileTopicListView {
		return mobileTopicListView{Topics: topics, Refreshing: refreshing, HasTopics: hasTopics}
	}
	for !mobileTopicResponseFits(data()) && len(topics) > 0 {
		topics = topics[:len(topics)-1]
	}
	if bridge.app.IsClosedEyesModeEnabled() {
		topics = []mobileTopicView{}
		refreshing, hasTopics = false, false
	}
	mobileJSON(w, http.StatusOK, data(), "")
}

func (bridge *mobileBridge) serveTopicDetail(w http.ResponseWriter, r *http.Request, id string) {
	if !mobileValidID(id) || !validChatTopicID(id) {
		mobileTopicError(w, errTodayChatItemUnavailable)
		return
	}
	if bridge.app.storeForMobile() == nil {
		mobileTopicError(w, errMobileChatNotReady)
		return
	}
	if bridge.app.IsClosedEyesModeEnabled() {
		mobileTopicError(w, errTodayChatObservationPaused)
		return
	}
	// Refresh the production registry even when a brief links here before the
	// phone has requested the topic list. The desktop reader rechecks sources.
	if err := bridge.refreshPhoneTopics(id); err != nil {
		mobileTopicError(w, err)
		return
	}
	limit := mobileChatItemsDefault
	if raw := r.URL.Query().Get("limit"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > mobileChatItemsMax {
			mobileJSON(w, http.StatusBadRequest, nil, "limit must be between 1 and 50")
			return
		}
		limit = value
	}
	for {
		revision, err := bridge.app.captureTopicPageRevision(bridge.ctx)
		if err != nil {
			mobileTopicError(w, err)
			return
		}
		page, err := bridge.app.ListMainChatTopicItems(id, r.URL.Query().Get("cursor"), limit)
		if err != nil {
			mobileTopicError(w, err)
			return
		}
		view := mobileTopicPage{SessionID: page.SessionID, TopicID: page.TopicID, Items: []mobileTopicItem{}, HasMore: page.HasMore, NextCursor: page.NextCursor}
		for _, item := range page.Items {
			projected := mobileTopicItem{ID: item.ID, Kind: item.Kind, At: item.At, Actor: mobileChatClip(item.Actor, 64), Text: mobileChatBound(item.Text, mobileChatTextMax)}
			if item.Message != nil {
				message := item.Message
				phone, visible := mobileChatItemFromRow(storage.StreamItemRow{ID: message.ID, Kind: message.Kind, Content: message.Content, ToolName: message.ToolName, ToolInput: message.ToolInput, SortOrder: message.SortOrder, CreatedAt: item.At})
				if !visible {
					continue
				}
				projected.Message = &phone
			}
			view.Items = append(view.Items, projected)
		}
		view = displayModelCopy(view, bridge.app.modelDisplayLanguage())
		if !mobileTopicResponseFits(view) {
			if limit > 1 {
				limit = (limit + 1) / 2
				continue
			}
			// A structured item may consume the whole chat budget by itself.
			// Keep its bounded text fallback so the cursor can still advance.
			for i := range view.Items {
				if m := view.Items[i].Message; m != nil {
					m.Kind, m.Proactive, m.Status = "text", nil, nil
				}
			}
			if !mobileTopicResponseFits(view) {
				mobileJSON(w, http.StatusRequestEntityTooLarge, nil, "the response is too large for the phone connection")
				return
			}
		}
		if bridge.app.IsClosedEyesModeEnabled() {
			mobileTopicError(w, errTodayChatObservationPaused)
			return
		}
		release, err := bridge.app.validateTopicPageRevision(bridge.ctx, revision)
		if err != nil {
			mobileTopicError(w, err)
			return
		}
		defer release()
		mobileJSON(w, http.StatusOK, view, "")
		return
	}
}

func (bridge *mobileBridge) serveTopicMessage(w http.ResponseWriter, r *http.Request, device mobileDeviceRecord, id string) {
	if !mobileValidID(id) || !validChatTopicID(id) {
		mobileTopicError(w, errTodayChatItemUnavailable)
		return
	}
	var input struct {
		Text           string `json:"text"`
		IdempotencyKey string `json:"idempotencyKey"`
	}
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
	if mobileTopicSendBeforeLock != nil {
		mobileTopicSendBeforeLock()
	}
	// Serialize receipt lookup and persistence with admission. A retry queued
	// during admission must see the first send's receipt before checking busy.
	bridge.chat.sendMu.Lock()
	defer bridge.chat.sendMu.Unlock()
	bridge.runReceipted(w, device, key, r.Method+" "+r.URL.Path, func(w http.ResponseWriter) {
		if err := bridge.refreshPhoneTopics(id); err != nil {
			mobileTopicRequestError(w, err, "the message could not be sent")
			return
		}
		turnID := uuid.NewString()
		onAdmitted := func(sessionID string, order int) {
			bridge.chat.mu.Lock()
			bridge.chat.turns[sessionID] = turnID
			bridge.chat.replies[sessionID] = &mobileChatReply{turnID: turnID, userOrder: order}
			delete(bridge.chat.errored, sessionID)
			bridge.chat.mu.Unlock()
			bridge.app.notifySpotlightSessionChanged(sessionID)
		}
		bridge.app.notePhoneTurnSender(device)
		sent, err := bridge.app.sendMainChatWithTopic(text, id, promptOriginPhone, onAdmitted)
		if err != nil {
			mobileTopicRequestError(w, err, "the message could not be sent")
			return
		}
		mobileJSON(w, http.StatusAccepted, map[string]any{"turnId": turnID, "seq": int64(sent.SortOrder) + 1}, "")
	})
}

// Registry refresh is needed only for the production adapter; TopicSource owns
// its own lookup in other embeddings. Never backfill provenance on stale data.
func (bridge *mobileBridge) refreshPhoneTopics(id string) error {
	if bridge.app.IsClosedEyesModeEnabled() {
		return errTodayChatObservationPaused
	}
	if _, production := bridge.app.topicSource.(*registryTopicSource); production {
		if _, err := bridge.app.todayTopicViews(); err != nil {
			return err
		}
	}
	_, _, err := bridge.app.chatTopicSnapshot(bridge.ctx, id)
	return err
}

func mobileTopicError(w http.ResponseWriter, err error) {
	mobileTopicRequestError(w, err, "the topic could not be opened")
}

func mobileTopicRequestError(w http.ResponseWriter, err error, failureMessage string) {
	switch true {
	case errors.Is(err, errTodayChatObservationPaused):
		mobileJSON(w, http.StatusConflict, nil, errTodayChatObservationPaused.Error())
	case errors.Is(err, errTopicCursor):
		mobileJSON(w, http.StatusBadRequest, nil, errTopicCursor.Error())
	case errors.Is(err, errMobileChatNotReady), errors.Is(err, errMobileStoreUnavailable):
		mobileJSON(w, http.StatusServiceUnavailable, nil, errMobileChatNotReady.Error())
	case errors.Is(err, errPromptAlreadyRunning), errors.Is(err, errMobileChatBusy):
		mobileJSON(w, http.StatusConflict, nil, "busy")
	case errors.Is(err, errChatSourceAccessChanged), errors.Is(err, errTopicRegistryChanged), errors.Is(err, storage.ErrReadRevisionChanged):
		mobileJSON(w, http.StatusConflict, nil, errChatSourceAccessChanged.Error())
	case errors.Is(err, errMobileChatWorkDir):
		mobileJSON(w, http.StatusConflict, nil, errMobileChatWorkDir.Error())
	case errors.Is(err, errTodayChatItemUnavailable), errors.Is(err, errTopicNotFound):
		mobileJSON(w, http.StatusNotFound, nil, "topic not found")
	default:
		log.Printf("[mobile-topics] request failed: %v", err)
		mobileJSON(w, http.StatusInternalServerError, nil, failureMessage)
	}
}
