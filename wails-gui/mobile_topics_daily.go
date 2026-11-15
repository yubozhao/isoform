package main

import (
	"context"
	"errors"
	"net/http"

	"wails-gui/internal/topicday"
)

// The daily topics on the phone (PROTOCOL.md 15.9): the saved copy, passed
// through the person's choices again on its way off the computer, and the
// answers to its proposals. Task instructions never leave the computer.

const mobileTopicsDailyNameMax = 160

type mobileTopicsDailyView struct {
	Enabled bool             `json:"enabled"`
	Day     string           `json:"day,omitempty"`
	BuiltAt int64            `json:"builtAt,omitempty"`
	Topics  []DailyTopicView `json:"topics"`
}

// dailyTopicsForPhone is GetDailyTopics with the same filter as the build
// applied again now, and every line bounded.
func (a *App) dailyTopicsForPhone(ctx context.Context) (mobileTopicsDailyView, error) {
	view, err := a.GetDailyTopics()
	if err != nil {
		return mobileTopicsDailyView{Topics: []DailyTopicView{}}, err
	}
	out := mobileTopicsDailyView{Enabled: view.Enabled, Day: view.Day, BuiltAt: view.BuiltAt, Topics: []DailyTopicView{}}
	if !view.Enabled {
		return out, nil
	}
	store, err := a.topicsDailyStore()
	if err != nil {
		return out, err
	}
	feedback, err := store.Feedback()
	if err != nil {
		return out, err
	}
	choices := a.topicsDailyChoices(feedback)
	for _, t := range view.Topics {
		if !choices.Allowed(t.ID, t.Name, nil) {
			continue
		}
		t.Name = mobileChatBound(t.Name, mobileTopicsDailyNameMax)
		t.Why, t.Latest, t.Next = mobileChatBound(t.Why, 400), mobileChatBound(t.Latest, 400), mobileChatBound(t.Next, 400)
		if t.Proposal != nil {
			p := *t.Proposal
			p.Title, p.Why, p.Result = mobileChatBound(p.Title, 200), mobileChatBound(p.Why, 400), mobileChatBound(p.Result, 400)
			t.Proposal = &p
		}
		out.Topics = append(out.Topics, t)
	}
	return out, nil
}

func (bridge *mobileBridge) serveTopicsDaily(w http.ResponseWriter, r *http.Request) {
	view, err := bridge.app.dailyTopicsForPhone(r.Context())
	if err != nil {
		mobileJSON(w, http.StatusServiceUnavailable, nil, errMobileStoreUnavailable.Error())
		return
	}
	mobileJSON(w, http.StatusOK, view, "")
}

func (bridge *mobileBridge) serveTopicsDailyAnswer(w http.ResponseWriter, r *http.Request, device mobileDeviceRecord) {
	var input struct {
		IdempotencyKey string `json:"idempotencyKey"`
		TopicID        string `json:"topicId"`
		ProposalID     string `json:"proposalId"`
		Verdict        string `json:"verdict"`
	}
	if !mobileDecode(w, r, &input) {
		return
	}
	key, valid := mobileKey(device.DeviceID, input.IdempotencyKey)
	if !valid {
		mobileJSON(w, http.StatusBadRequest, nil, "an idempotency key of 1 to 128 bytes is required")
		return
	}
	switch input.Verdict {
	case topicday.VerdictYes, topicday.VerdictNo, topicday.VerdictNotMine:
	default:
		mobileJSON(w, http.StatusBadRequest, nil, "verdict must be yes, no or not_mine")
		return
	}
	bridge.runReceipted(w, device, key, r.Method+" "+r.URL.Path, func(w http.ResponseWriter) {
		topic, err := bridge.app.answerDailyTopicProposal(r.Context(), input.TopicID, input.ProposalID, input.Verdict)
		switch {
		case err == nil:
			mobileJSON(w, http.StatusOK, topic, "")
		case errors.Is(err, topicday.ErrNotFound), errors.Is(err, errDailyTopicNotAllowed):
			mobileJSON(w, http.StatusNotFound, nil, "this topic is no longer shown")
		case errors.Is(err, errDailyTopicAnswered):
			mobileJSON(w, http.StatusConflict, nil, errDailyTopicAnswered.Error())
		default:
			mobileJSON(w, http.StatusServiceUnavailable, nil, "the answer could not be saved; try again")
		}
	})
}
