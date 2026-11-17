package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"wails-gui/internal/handoff"
)

// mobileHoldProposal is a phone-readable H1 proposal. Token authenticates the
// exact slots and source that were displayed, including across an offline retry.
type mobileHoldProposal struct {
	Proposal  HoldProposal   `json:"proposal"`
	Calendars []holdCalendar `json:"calendars"`
	Why       string         `json:"why,omitempty"`
	Token     string         `json:"token"`
}

type mobileHoldToken struct {
	DeviceID       string       `json:"deviceId"`
	Proposal       HoldProposal `json:"proposal"`
	SourceRevision string       `json:"sourceRevision"`
}

var mobileHoldEnglishThing = regexp.MustCompile(`(?i)^\s*(?:hold|reserve|block|set aside)\b.+?\s+for\s+(?:the\s+)?(.+?)\s*[?.!]?\s*$`)
var mobileHoldChineseThing = regexp.MustCompile(`^\s*(?:把)?(?:今天|明天|周[一二三四五六日天]|星期[一二三四五六日天]).*?留给\s*(.+?)\s*[？。！]?\s*$`)

// Old offers carry prose rather than a typed work title. Only unambiguous
// titles can become calendar actions; everything else stays hidden.
func mobileHoldOfferThing(title string) string {
	for _, pattern := range []*regexp.Regexp{mobileHoldEnglishThing, mobileHoldChineseThing} {
		match := pattern.FindStringSubmatch(title)
		if len(match) == 2 {
			thing := normalizeHoldThing(HoldThing{Title: match[1]}).Title
			if thing != "" {
				return thing
			}
		}
	}
	return ""
}

func (bridge *mobileBridge) mobileHoldsAvailable(granted bool) bool {
	// Calendar holds are switched off in the rework (calendar-holds feature switch); the phone gets none while off.
	if !featureOn(featureCalendarHolds) {
		return false
	}
	if !granted || bridge.app == nil || bridge.app.store == nil {
		return false
	}
	if _, err := bridge.app.currentHoldCoordinator(); err != nil {
		return false
	}
	value, err := bridge.app.store.GetUIState(bridge.ctx, calendarOfferHoldsKey)
	return err == nil && value != "false"
}

func mobileCanReadHoldSource(device mobileDeviceRecord, kind string) bool {
	switch kind {
	case "task", "brief", "promise":
		return mobileHasScope(device.Scopes, mobileScopeBriefRead)
	case "up_next":
		return mobileHasScope(device.Scopes, mobileScopeOffersRead)
	default:
		return false
	}
}

func mobileHoldSourceRevision(thing HoldThing, due, why string) string {
	raw, _ := json.Marshal([]any{thing, due, why})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func (bridge *mobileBridge) holdToken(device mobileDeviceRecord, proposal HoldProposal, sourceRevision string) (string, error) {
	bridge.mu.Lock()
	identity := bridge.identity
	bridge.mu.Unlock()
	if identity == nil || identity.Private == nil {
		return "", errors.New("mobile identity unavailable")
	}
	payload, err := json.Marshal(mobileHoldToken{DeviceID: device.DeviceID, Proposal: proposal, SourceRevision: sourceRevision})
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, identity.Private.Bytes())
	mac.Write([]byte("isoform-mobile-hold-v1\x00"))
	mac.Write(payload)
	return base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func (bridge *mobileBridge) readHoldToken(device mobileDeviceRecord, token string) (mobileHoldToken, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 || len(token) > 8192 {
		return mobileHoldToken{}, errHoldUnavailable
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return mobileHoldToken{}, errHoldUnavailable
	}
	var decoded mobileHoldToken
	if json.Unmarshal(payload, &decoded) != nil || decoded.DeviceID != device.DeviceID {
		return mobileHoldToken{}, errHoldUnavailable
	}
	expected, err := bridge.holdToken(device, decoded.Proposal, decoded.SourceRevision)
	if err != nil || !hmac.Equal([]byte(expected), []byte(token)) {
		return mobileHoldToken{}, errHoldUnavailable
	}
	return decoded, nil
}

// mobileHoldSource reads only sources that already pass the phone's privacy
// projection. A phone cannot invent a title or source by editing the request.
func (bridge *mobileBridge) mobileHoldSource(ctx context.Context, kind, id string) (HoldThing, string, string, error) {
	if !mobileValidID(id) || bridge.app == nil {
		return HoldThing{}, "", "", errHoldUnavailable
	}
	switch kind {
	case "promise":
		thing, due, why, err := bridge.mobileHoldSource(ctx, "brief", mobileBriefItemKey("commitments", "purpose", id))
		if err != nil {
			return HoldThing{}, "", "", err
		}
		thing.SourceKind, thing.SourceID = kind, id
		return thing, due, why, nil
	case "up_next":
		offers, err := bridge.app.mobileOffersWithHolds(ctx, true)
		if err != nil {
			return HoldThing{}, "", "", err
		}
		for _, offer := range offers.Offers {
			if offer.ID == id && mobileCalendarHoldOffer.MatchString(offer.Title) && offer.Evidence != nil {
				title := mobileHoldOfferThing(offer.Title)
				if title != "" {
					return HoldThing{Title: title, SourceKind: kind, SourceID: id}, "", offer.Evidence.Text, nil
				}
			}
		}
	case "task":
		if bridge.app.handoffStore == nil {
			return HoldThing{}, "", "", errHoldUnavailable
		}
		entry, err := bridge.app.handoffStore.Get(id)
		if err != nil {
			return HoldThing{}, "", "", errHoldUnavailable
		}
		subjects, err := mobileGoalSubjects(bridge.app)
		if err != nil {
			return HoldThing{}, "", "", err
		}
		detail, visible := mobileTaskDetail(entry, subjects)
		if !visible {
			return HoldThing{}, "", "", errHoldUnavailable
		}
		return normalizeHoldThing(HoldThing{Title: detail.Title, SourceKind: kind, SourceID: id}), "", "", nil
	case "brief":
		svc := bridge.briefSource()
		if svc == nil {
			return HoldThing{}, "", "", errHoldUnavailable
		}
		brief, ok := currentMobileBrief(svc)
		if !ok {
			return HoldThing{}, "", "", errHoldUnavailable
		}
		brief = bridge.app.mobileBriefForDeliveryContext(ctx, brief)
		sections := brief.Sections
		for _, items := range [][]MobileBriefItem{sections.Noticed, sections.Decide, sections.Review, sections.Plan, sections.WaitingOnYou, sections.Commitments, sections.WorkingToward, sections.DayAhead, sections.Highlights, sections.Moved, sections.Doing, sections.Watching, sections.Meetings, sections.People, sections.OpenLoops, sections.Learned, sections.Deliverables, sections.Schedules} {
			for _, item := range items {
				if firstNonEmpty(item.Key, item.ID) == id && item.Title != "" {
					return normalizeHoldThing(HoldThing{Title: item.Title, SourceKind: kind, SourceID: id}), item.DueAt, item.AttentionReason, nil
				}
			}
		}
	}
	return HoldThing{}, "", "", errHoldUnavailable
}

func (bridge *mobileBridge) serveHoldProposal(w http.ResponseWriter, r *http.Request, device mobileDeviceRecord, granted bool) {
	if !bridge.mobileHoldsAvailable(granted) {
		mobileJSON(w, http.StatusServiceUnavailable, nil, "holds unavailable")
		return
	}
	kind := r.URL.Query().Get("source_kind")
	if !mobileCanReadHoldSource(device, kind) {
		mobileJSON(w, http.StatusForbidden, nil, "source permission required")
		return
	}
	thing, due, why, err := bridge.mobileHoldSource(r.Context(), kind, r.URL.Query().Get("source_id"))
	if err != nil {
		mobileJSON(w, http.StatusNotFound, nil, "source unavailable")
		return
	}
	view, err := bridge.mobileBuildHoldProposal(device, thing, due, why, granted)
	if err != nil {
		mobileJSON(w, http.StatusServiceUnavailable, nil, "holds unavailable")
		return
	}
	mobileJSON(w, http.StatusOK, view, "")
}

func (bridge *mobileBridge) mobileBuildHoldProposal(device mobileDeviceRecord, thing HoldThing, due, why string, granted bool) (mobileHoldProposal, error) {
	proposal, err := bridge.app.proposeHoldWithAccess(thing, due, 0, granted)
	if err != nil {
		return mobileHoldProposal{}, err
	}
	c, err := bridge.app.currentHoldCoordinator()
	if err != nil {
		return mobileHoldProposal{}, err
	}
	settings, err := bridge.app.holdSettings(c.calendar, granted)
	if err != nil {
		return mobileHoldProposal{}, err
	}
	token, err := bridge.holdToken(device, proposal, mobileHoldSourceRevision(thing, due, why))
	if err != nil {
		return mobileHoldProposal{}, err
	}
	return mobileHoldProposal{Proposal: proposal, Calendars: settings.Calendars, Why: why, Token: token}, nil
}

func (bridge *mobileBridge) serveHoldStatus(w http.ResponseWriter, r *http.Request, device mobileDeviceRecord) {
	id := r.URL.Query().Get("id")
	if !mobileValidID(id) || bridge.app == nil {
		mobileJSON(w, http.StatusBadRequest, nil, "invalid hold id")
		return
	}
	c, err := bridge.app.currentHoldCoordinator()
	if err != nil {
		mobileJSON(w, http.StatusServiceUnavailable, nil, "hold unavailable")
		return
	}
	c.mu.Lock()
	record, err := c.record(id)
	c.mu.Unlock()
	taskVisible := false
	if err == nil && record.SourceKind == "up_next" && mobileHasScope(device.Scopes, mobileScopeBriefRead) && bridge.app.handoffStore != nil {
		if entry, taskErr := bridge.app.handoffStore.Get(mobileHoldTaskID(id)); taskErr == nil {
			if subjects, subjectsErr := mobileGoalSubjects(bridge.app); subjectsErr == nil {
				_, taskVisible = mobileTaskDetail(entry, subjects)
			}
		}
	}
	if err == nil && !taskVisible && !mobileCanReadHoldSource(device, record.SourceKind) {
		mobileJSON(w, http.StatusForbidden, nil, "source permission required")
		return
	}
	if err == nil && !taskVisible {
		_, _, _, err = bridge.mobileHoldSource(r.Context(), record.SourceKind, record.SourceID)
	}
	if err != nil {
		mobileJSON(w, http.StatusNotFound, nil, "hold unavailable")
		return
	}
	status := statusForHold(record)
	if taskVisible {
		status.TaskID = mobileHoldTaskID(id)
		bridge.syncHoldOfferTask(status)
	}
	mobileJSON(w, http.StatusOK, status, "")
}

type mobileHoldAction struct {
	IdempotencyKey string        `json:"idempotencyKey"`
	Action         string        `json:"action"`
	Token          string        `json:"token,omitempty"`
	Slot           FindTimeRange `json:"slot,omitempty"`
	CalendarID     string        `json:"calendarId,omitempty"`
	HoldID         string        `json:"holdId,omitempty"`
}

func mobileHoldTaskID(holdID string) string { return "hold." + holdID }

// The task is keyed by H1's durable hold ID, so losing a phone receipt cannot
// create a second thread. It contains only a reference to the calendar record.
func (bridge *mobileBridge) ensureHoldOfferTask(proposal HoldProposal) (string, error) {
	if bridge.app.handoffStore == nil {
		return "", errTaskStoreNotReady
	}
	id := mobileHoldTaskID(proposal.ID)
	bridge.app.taskChoiceMu.Lock()
	defer bridge.app.taskChoiceMu.Unlock()
	if existing, err := bridge.app.handoffStore.Get(id); err == nil {
		if existing.Thread != nil {
			for _, leftover := range existing.Thread.Leftovers {
				if leftover.Kind == handoff.LeftoverCalendarHold && leftover.Ref == proposal.ID {
					return id, nil
				}
			}
		}
		return "", errHoldUnavailable
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	now := time.Now().UnixMilli()
	entry := handoff.Entry{ID: id, Title: proposal.Thing.Title, Status: handoff.StatusInProgress,
		Source: handoff.SourceMobile, SourceID: proposal.Thing.SourceID, CreatedAt: now, UpdatedAt: now,
		Thread: &handoff.Thread{Origin: &handoff.Origin{Kind: handoff.OriginUpNext, At: now},
			Leftovers: []handoff.Leftover{{Kind: handoff.LeftoverCalendarHold, Ref: proposal.ID, Label: proposal.Thing.Title, State: handoff.LeftoverPlaced}}}}
	if _, err := bridge.app.handoffStore.Save(entry); err != nil {
		return "", err
	}
	bridge.app.emitBrowserEvent("handoff:updated", map[string]any{"id": id})
	return id, nil
}

func (bridge *mobileBridge) attachHoldToTask(proposal HoldProposal) error {
	if bridge.app.handoffStore == nil {
		return errTaskStoreNotReady
	}
	_, err := bridge.app.handoffStore.MutateIfStatus(proposal.Thing.SourceID, []handoff.Status{handoff.StatusActive, handoff.StatusInProgress, handoff.StatusBlocked, handoff.StatusNeedsReview, handoff.StatusStale}, func(entry *handoff.Entry) {
		thread := ensureThread(entry)
		for _, leftover := range thread.Leftovers {
			if leftover.Kind == handoff.LeftoverCalendarHold && leftover.Ref == proposal.ID {
				return
			}
		}
		thread.Leftovers = append(thread.Leftovers, handoff.Leftover{Kind: handoff.LeftoverCalendarHold,
			Ref: proposal.ID, Label: proposal.Thing.Title, State: handoff.LeftoverPlaced})
	})
	return err
}

func (bridge *mobileBridge) discardUnwrittenHoldTask(proposal HoldProposal) {
	if bridge.app.handoffStore == nil {
		return
	}
	switch proposal.Thing.SourceKind {
	case "up_next":
		id := mobileHoldTaskID(proposal.ID)
		if entry, err := bridge.app.handoffStore.Get(id); err == nil && entry.Title == proposal.Thing.Title && entry.Source == handoff.SourceMobile && entry.SourceID == proposal.Thing.SourceID && entry.Body == "" && entry.Status == handoff.StatusInProgress && entry.Thread != nil && entry.Thread.Outcome == nil && len(entry.Thread.Events) == 0 && len(entry.Thread.Leftovers) == 1 && entry.Thread.Leftovers[0].Ref == proposal.ID {
			_, _ = bridge.app.handoffStore.DeleteIfUnchanged(entry)
		}
	case "task":
		_, _ = bridge.app.handoffStore.MutateThread(proposal.Thing.SourceID, func(thread *handoff.Thread) {
			for i := range thread.Leftovers {
				if thread.Leftovers[i].Kind == handoff.LeftoverCalendarHold && thread.Leftovers[i].Ref == proposal.ID {
					thread.Leftovers[i].State = handoff.LeftoverRemoved
				}
			}
		})
	}
}

// setHoldLeftover records what happened to a hold in the thread of the task
// it was held for, so the clean-up ask never repeats.
func (bridge *mobileBridge) setHoldLeftover(record holdRecord, state string) {
	if bridge.app.handoffStore == nil {
		return
	}
	ids := []string{mobileHoldTaskID(record.ID)}
	if record.SourceKind == "task" {
		ids = []string{record.SourceID}
	}
	for _, id := range ids {
		_, _ = bridge.app.handoffStore.MutateThread(id, func(thread *handoff.Thread) {
			for i := range thread.Leftovers {
				if thread.Leftovers[i].Kind == handoff.LeftoverCalendarHold && thread.Leftovers[i].Ref == record.ID {
					thread.Leftovers[i].State = state
				}
			}
		})
	}
}

func (bridge *mobileBridge) syncHoldOfferTask(status HoldStatus) {
	if bridge.app == nil || bridge.app.handoffStore == nil || status.ID == "" {
		return
	}
	id := mobileHoldTaskID(status.ID)
	entry, err := bridge.app.handoffStore.Get(id)
	if err != nil || entry.Status != handoff.StatusInProgress {
		return
	}
	switch status.State {
	case "held", "moved", "failed", "gone":
		_, _ = bridge.app.handoffStore.MutateIfStatus(id, []handoff.Status{handoff.StatusInProgress}, func(e *handoff.Entry) {
			e.Status = handoff.StatusActive
		})
	}
}

// A hold offer's action is finished once Calendar confirms it. Keep the
// underlying work open so the person can later mark that work done and get
// the cleanup choice, while projecting the placement as a Done outcome.
func (a *App) mobileHoldTaskProjection(entry handoff.Entry) handoff.Entry {
	if entry.Status != handoff.StatusActive || entry.Thread == nil || len(entry.Thread.Leftovers) != 1 {
		return entry
	}
	leftover := entry.Thread.Leftovers[0]
	if leftover.Kind != handoff.LeftoverCalendarHold || entry.ID != mobileHoldTaskID(leftover.Ref) {
		return entry
	}
	c, err := a.currentHoldCoordinator()
	if err != nil {
		return entry
	}
	c.mu.Lock()
	record, err := c.record(leftover.Ref)
	c.mu.Unlock()
	if err != nil || (record.State != "held" && record.State != "moved") {
		return entry
	}
	thread := *entry.Thread
	thread.Outcome = &handoff.Outcome{Kind: handoff.OutcomeHoldPlaced, By: handoff.OutcomeByIsoform,
		At: record.CreatedAt.UnixMilli(), Params: map[string]string{"start": record.Start.Format(time.RFC3339), "end": record.End.Format(time.RFC3339)}}
	entry.Thread = &thread
	entry.Status = handoff.StatusCompleted
	return entry
}

func (bridge *mobileBridge) serveHoldAction(w http.ResponseWriter, r *http.Request, device mobileDeviceRecord, granted bool) {
	var input mobileHoldAction
	if !mobileDecode(w, r, &input) {
		return
	}
	key, ok := mobileKey(device.DeviceID, input.IdempotencyKey)
	if !ok {
		mobileJSON(w, http.StatusBadRequest, nil, "idempotency key required")
		return
	}
	switch input.Action {
	case "hold", "undo_hold", "remove_hold", "undo_remove_hold", "select_calendar", "dismiss_hold", "undo_dismiss_hold":
	default:
		mobileJSON(w, http.StatusBadRequest, nil, "unknown hold action")
		return
	}
	raw, _ := json.Marshal(input)
	digest := sha256.Sum256(raw)
	route := r.Method + " " + r.URL.Path + ":" + hex.EncodeToString(digest[:])
	bridge.runReceipted(w, device, key, route, func(w http.ResponseWriter) {
		bridge.performHoldAction(w, device, input, granted)
	})
}

func (bridge *mobileBridge) performHoldAction(w http.ResponseWriter, device mobileDeviceRecord, input mobileHoldAction, granted bool) {
	if bridge.app == nil || (!granted && input.Action != "undo_hold" && input.Action != "undo_dismiss_hold") {
		mobileJSON(w, http.StatusServiceUnavailable, nil, "calendar unavailable")
		return
	}
	var status HoldStatus
	var err error
	var heldProposal HoldProposal
	var taskID string
	switch input.Action {
	case "hold":
		if !bridge.mobileHoldsAvailable(granted) {
			mobileJSON(w, http.StatusServiceUnavailable, nil, "holds unavailable")
			return
		}
		var token mobileHoldToken
		token, err = bridge.readHoldToken(device, input.Token)
		if err == nil {
			proposal := token.Proposal
			heldProposal = proposal
			if (proposal.Thing.SourceKind == "up_next" || proposal.Thing.SourceKind == "task") && bridge.app.handoffStore == nil {
				mobileJSON(w, http.StatusServiceUnavailable, nil, "task unavailable")
				return
			}
			if !mobileCanReadHoldSource(device, proposal.Thing.SourceKind) {
				mobileJSON(w, http.StatusForbidden, nil, "source permission required")
				return
			}
			thing, due, why, sourceErr := bridge.mobileHoldSource(bridge.ctx, proposal.Thing.SourceKind, proposal.Thing.SourceID)
			if sourceErr != nil || thing != proposal.Thing || mobileHoldSourceRevision(thing, due, why) != token.SourceRevision {
				err = errHoldUnavailable
			} else {
				// A receipt can fail to persist after H1 persisted the intent. A
				// retry must adopt only this exact request, never create it again.
				c, coordinatorErr := bridge.app.currentHoldCoordinator()
				if coordinatorErr != nil {
					err = coordinatorErr
				} else {
					c.mu.Lock()
					existing, readErr := c.record(proposal.ID)
					c.mu.Unlock()
					if readErr == nil {
						start, startErr := time.Parse(time.RFC3339, input.Slot.Start)
						end, endErr := time.Parse(time.RFC3339, input.Slot.End)
						if startErr != nil || endErr != nil || existing.SourceKind != thing.SourceKind || existing.SourceID != thing.SourceID || existing.Title != thing.Title || existing.CalendarID != input.CalendarID || !existing.Start.Equal(start) || !existing.End.Equal(end) {
							err = errHoldUnavailable
						} else {
							status = statusForHold(existing)
						}
					} else if errors.Is(readErr, sql.ErrNoRows) {
						start, parseErr := time.Parse(time.RFC3339, input.Slot.Start)
						if parseErr == nil && !start.After(c.now()) {
							mobileJSON(w, http.StatusConflict, map[string]string{"reason": "time_passed"}, "HOLD_TIME_PASSED")
							return
						}
						switch proposal.Thing.SourceKind {
						case "up_next":
							taskID, err = bridge.ensureHoldOfferTask(proposal)
						case "task":
							err = bridge.attachHoldToTask(proposal)
							taskID = proposal.Thing.SourceID
						}
						if err != nil {
							mobileJSON(w, http.StatusServiceUnavailable, nil, "task unavailable")
							return
						}
						status, err = bridge.app.holdTimeWithAccess(proposal, input.Slot, input.CalendarID, granted)
						if err != nil || status.State == "conflict" || status.State == "calendar_unavailable" {
							c.mu.Lock()
							_, checkErr := c.record(proposal.ID)
							c.mu.Unlock()
							if errors.Is(checkErr, sql.ErrNoRows) {
								bridge.discardUnwrittenHoldTask(proposal)
							}
						}
						if err == nil && status.State == "pending_write" && input.CalendarID != proposal.CalendarID {
							_ = bridge.app.setHoldCalendar(c.calendar, granted, input.CalendarID)
						}
					} else {
						err = readErr
					}
				}
			}
		}
	case "undo_hold":
		err = bridge.app.UndoHold(input.HoldID)
	case "remove_hold":
		err = bridge.app.RemoveHold(input.HoldID)
	case "undo_remove_hold":
		err = bridge.app.UndoRemoveHold(input.HoldID)
	case "select_calendar":
		if !bridge.mobileHoldsAvailable(granted) {
			err = errHoldUnavailable
			break
		}
		var coordinator *holdCoordinator
		coordinator, err = bridge.app.currentHoldCoordinator()
		if err == nil {
			err = bridge.app.setHoldCalendar(coordinator.calendar, granted, input.CalendarID)
		}
		status = HoldStatus{State: "calendar_selected", CalendarID: input.CalendarID}
	case "dismiss_hold":
		var token mobileHoldToken
		token, err = bridge.readHoldToken(device, input.Token)
		if err == nil {
			thing, due, why, sourceErr := bridge.mobileHoldSource(bridge.ctx, token.Proposal.Thing.SourceKind, token.Proposal.Thing.SourceID)
			if !mobileCanReadHoldSource(device, token.Proposal.Thing.SourceKind) || sourceErr != nil || thing != token.Proposal.Thing || mobileHoldSourceRevision(thing, due, why) != token.SourceRevision {
				err = errHoldUnavailable
			} else {
				var id string
				id, err = bridge.app.DismissHoldOffer(thing)
				status = HoldStatus{ID: id, State: "dismissed"}
			}
		}
	case "undo_dismiss_hold":
		err = bridge.app.UndoDismissHoldOffer(input.HoldID)
		status = HoldStatus{ID: input.HoldID, State: "restored"}
	}
	if err != nil {
		code := "failed"
		message := "HOLD_FAILED"
		if errors.Is(err, errHoldEdited) {
			code = "edited"
			message = "HOLD_EDITED"
		}
		mobileJSON(w, http.StatusConflict, map[string]string{"reason": code}, message)
		return
	}
	if input.Action == "hold" && heldProposal.Thing.SourceKind == "up_next" && (status.State == "pending_write" || status.State == "held") {
		if taskID == "" {
			taskID, err = bridge.ensureHoldOfferTask(heldProposal)
		}
		status.TaskID = taskID
		if err != nil {
			mobileJSON(w, http.StatusServiceUnavailable, nil, "task unavailable")
			return
		}
	}
	if input.Action == "hold" && heldProposal.Thing.SourceKind == "task" && (status.State == "pending_write" || status.State == "held") {
		if taskID == "" {
			err = bridge.attachHoldToTask(heldProposal)
		}
		if err != nil {
			mobileJSON(w, http.StatusServiceUnavailable, nil, "task unavailable")
			return
		}
		status.TaskID = heldProposal.Thing.SourceID
	}
	switch input.Action {
	case "undo_hold", "remove_hold", "undo_remove_hold":
		status, err = bridge.app.GetHoldStatus(input.HoldID)
		if err != nil {
			mobileJSON(w, http.StatusServiceUnavailable, nil, "hold unavailable")
			return
		}
		// A write grant alone does not grant a read of the originating task.
		// Undo still works after a read scope is withdrawn, with a minimal receipt.
		c, readErr := bridge.app.currentHoldCoordinator()
		if readErr != nil {
			mobileJSON(w, http.StatusServiceUnavailable, nil, "hold unavailable")
			return
		}
		c.mu.Lock()
		record, readErr := c.record(input.HoldID)
		c.mu.Unlock()
		if readErr != nil {
			mobileJSON(w, http.StatusServiceUnavailable, nil, "hold unavailable")
			return
		}
		if input.Action == "undo_hold" && record.SourceKind == "up_next" && record.EventID == "" && bridge.app.handoffStore != nil {
			bridge.discardUnwrittenHoldTask(HoldProposal{ID: record.ID, Thing: HoldThing{Title: record.Title, SourceKind: record.SourceKind, SourceID: record.SourceID}})
		}
		switch input.Action {
		case "undo_hold":
			if record.SourceKind == "task" {
				bridge.setHoldLeftover(record, handoff.LeftoverRemoved)
			}
		case "remove_hold":
			bridge.setHoldLeftover(record, handoff.LeftoverRemoved)
		case "undo_remove_hold":
			// Undo puts the event back and ends the clean-up ask.
			bridge.setHoldLeftover(record, handoff.LeftoverKept)
		}
		visible := mobileCanReadHoldSource(device, record.SourceKind)
		if visible {
			_, _, _, readErr = bridge.mobileHoldSource(bridge.ctx, record.SourceKind, record.SourceID)
			visible = readErr == nil
		}
		if !visible {
			status = HoldStatus{ID: status.ID, State: status.State}
		}
	}
	mobileJSON(w, http.StatusOK, status, "")
}
