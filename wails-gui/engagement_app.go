package main

import (
	"context"
	"errors"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"
	"wails-gui/internal/engagement"
	"wails-gui/internal/platform/activitymon"
	"wails-gui/internal/platform/notify"
	"wails-gui/storage"
)

// engagementMacAwayIdle is the idle time reported for the Mac when the app is
// not in front. The Mac has no input-idle probe yet, so presence is derived
// from the app being the active application: in front means the user is
// there, otherwise the phone is the better channel.
const engagementMacAwayIdle = time.Hour

// The two facts where a notification rings is decided by, read at the moment
// of deciding (notify v1.0.0 rule 1); tests replace them.
var (
	engagementInputIdle    = activitymon.IdleTime
	engagementScreenLocked = activitymon.IsScreenLocked
)

// startEngagement builds the Engagement service over the app store, wires
// the push, desktop and receipt paths, and starts the pacing loop.
func (a *App) startEngagement(ctx context.Context) {
	if a == nil || a.store == nil {
		return
	}
	svc := newEngagementService(a.store, a.engagementPresence, engagementDelivery{
		Push:    a.engagementPush,
		Desktop: a.engagementDesktop,
		Text:    a.engagementText,
		Email:   a.engagementEmail,
		Digest:  a.engagementDigest,
		Report:  a.postEngagementReport,
	})
	svc.lang = a.currentUILanguage
	svc.paceChanged = func() {
		if bridge := a.mobileBridgeForUse(); bridge != nil {
			bridge.noteTodayChanged()
		}
	}
	svc.settingsChanged = func(prefs engagement.Prefs) {
		kinds := make([]map[string]any, 0, len(engagement.AllKinds()))
		for _, kind := range engagement.AllKinds() {
			spec, _ := engagement.Spec(kind)
			kinds = append(kinds, map[string]any{"kind": string(kind), "enabled": prefs.KindEnabled(spec), "quietOnToday": prefs.KindQuietOnToday(kind), "deliveryMode": prefs.KindDeliveryMode(kind)})
		}
		a.emitBrowserEvent("engagement:settings-changed", map[string]any{"kinds": kinds})
	}
	svc.threadOutcomeCopy = a.renderThreadOutcomeMoment
	svc.prepareSource = a.prepareEngagementRead
	svc.sourceAllowsRead = a.engagementDeliveryAllowsRead
	svc.holdStagedListReminder = a.holdStagedBrainDumpReminder
	svc.chatReportRetry = a.engagementChatReportRetryAt
	svc.completion = a.engagementItemCompletedAt
	a.engagement = svc
	a.initializeFirstWeek(context.Background(), svc.now())
	svc.journey = a.checkEngagementJourney
	svc.afterPass = func() {
		if waiting, _ := a.store.GetUIState(context.Background(), firstReadWaitingForBriefKey); waiting == "true" {
			go a.maybePostFirstReads(context.Background())
		}
	}
	mobileNoticeReceiptHook = a.engagementReceipt
	mobileSMSReplyHook = a.handleSMSReply
	mobileSMSStateHook = a.handleSMSState
	svc.Start(ctx)
	a.startEngagementSignals(ctx)
	a.announceLatestBriefIfUnannounced()
	go a.backfillBriefChatCards(context.Background())
	log.Printf("[engagement] service started")
}

// stopEngagement ends the pacing loop at shutdown.
func (a *App) stopEngagement() {
	if a == nil || a.engagement == nil {
		return
	}
	mobileNoticeReceiptHook = nil
	mobileSMSReplyHook = nil
	mobileSMSStateHook = nil
	engagementPersonSightingHook = func(context.Context, string, string, int64) func() { return nil }
	a.engagement.Stop()
}

// engagementPresence reports where the user is (notify v1.0.0 rule 1): at
// the computer while its screen is unlocked and the last input was under 2
// minutes ago, in any app; on the phone when a paired device holds the push
// scope. Only those facts are read, at the moment of deciding, and none is
// kept.
func (a *App) engagementPresence(now time.Time) engagement.Presence {
	p := engagement.Presence{
		MacIdle:       engagementMacAwayIdle,
		TextAvailable: sharedTextChannel().Available(),
		SMSActive:     a.smsChannel().Active(),
		EmailActive:   a.emailChannel().Active(),
	}
	// A computer whose notifications are off in System Settings cannot show
	// a toast, so the user counts as away from it and the phone is the
	// channel. Otherwise the input idle time decides, in whichever app the
	// input was.
	if !notificationExplicitlyDenied() {
		p.MacForeground = notify.IsAppForeground()
		p.MacIdle = engagementInputIdle()
	}
	p.ScreenLocked = engagementScreenLocked()
	// With Closed Eyes on, input is not read: an unlocked computer is at it.
	p.ClosedEyes = a != nil && a.IsClosedEyesModeEnabled()
	bridge := a.mobileBridgeForUse()
	if bridge == nil {
		return p
	}
	readers := bridge.workReaders()
	for _, record := range bridge.devicesWithScope(mobileScopePush) {
		if readers[record.DeviceID] {
			p.PhoneReadsWork = true
		}
		p.PhoneRegistered = true
		if seen, err := time.Parse(time.RFC3339, record.LastSeenAt); err == nil && seen.After(p.PhoneLastSeen) {
			p.PhoneLastSeen = seen
		}
	}
	if p.PhoneLastSeen.IsZero() && p.PhoneRegistered {
		p.PhoneLastSeen = now
	}
	return p
}

// engagementPush seals the moment as a notice and publishes it to every
// paired phone with the push scope. A missing recipient or an offline relay
// is an error, so the service retries later.
func (a *App) engagementPush(_ context.Context, row storage.EngagementMomentRow, m engagement.Moment, v engagement.Verdict, text engagementText) error {
	if !a.engagementDeliveryAllowsRead(context.Background(), m) {
		return errors.New("outcome source held")
	}

	bridge := a.mobileBridgeForUse()
	if bridge == nil {
		return errors.New("the phone connection is off")
	}
	spec, ok := engagement.Spec(m.Kind)
	if !ok {
		return errors.New("unknown engagement kind " + string(m.Kind))
	}
	actions := make([]mobileNoticeAction, 0, len(spec.Actions))
	if m.Params["_activity_thread"] != "" {
		spec.Actions = []engagement.Action{{ID: "open"}}
	}
	for _, action := range spec.Actions {
		label := text.Actions[action.ID]
		if label == "" {
			label = action.ID
		}
		actions = append(actions, mobileNoticeAction{ID: action.ID, Label: label, Auth: action.Authenticated})
	}
	notice := mobileNotice{
		ID:        row.ID,
		Kind:      string(m.Kind),
		Title:     text.PushBody,
		Category:  text.Category,
		SubjectID: m.SubjectID,
		MomentID:  m.ID,
		Collapse:  row.ID,
		Level:     string(v.Interruption),
		Actions:   actions,
		CreatedAt: m.CreatedAt,
		ExpiresAt: m.ExpiresAt,
	}
	notice.DestinationKind, notice.DestinationID = mobileNoticeDestination(m.Kind, m.SubjectID, m.ID, m.Params["source_brief_id"], m.DedupeKey)
	devices := bridge.devicesWithScope(mobileScopePush)
	if item, ok := a.workItemForNotice(m); ok {
		if item.State == workStateAnswered {
			// Prep answered on a device (Not needed, or kept) needs no notice.
			return nil
		}
		readers := bridge.workReaders()
		answerable, older := []mobileDeviceRecord{}, []mobileDeviceRecord{}
		for _, device := range devices {
			if readers[device.DeviceID] {
				answerable = append(answerable, device)
			} else {
				older = append(older, device)
			}
		}
		if len(older) > 0 {
			if err := bridge.publishMobileNotice(notice, older); err != nil {
				return err
			}
		}
		if len(answerable) == 0 {
			return nil
		}
		// A phone that reads Your work answers from the notification
		// (phone-map rule 16): Not needed on prep, Useful on finished work.
		notice.WorkItemID = item.ID
		notice.Actions = append(append([]mobileNoticeAction{}, notice.Actions...), workNoticeAction(m.Kind))
		if text.PhoneTitle != "" {
			// Finished work reads "Back from {agent}" with its one sentence;
			// the phone's own Show Previews decides what the lock screen shows.
			notice.Title, notice.Body = text.PhoneTitle, text.PhoneBody
		}
		return bridge.publishMobileNotice(notice, answerable)
	}
	return bridge.publishMobileNotice(notice, devices)
}

// workNoticeAction is the answer a notice of this kind carries on a phone
// that reads Your work.
func workNoticeAction(kind engagement.Kind) mobileNoticeAction {
	if kind == engagement.KindMeetingPrep {
		return mobileNoticeAction{ID: "not_needed", Label: "Not needed"}
	}
	return mobileNoticeAction{ID: "useful", Label: "Useful"}
}

// workItemForNotice finds the Your work item a prep or finished-work moment is
// about: the meeting's prep, or the item whose Yes started the finished task.
func (a *App) workItemForNotice(m engagement.Moment) (WorkItem, bool) {
	if m.Kind != engagement.KindMeetingPrep && m.Kind != engagement.KindWorkFinished || m.SubjectID == "" {
		return WorkItem{}, false
	}
	list, err := a.workListNow()
	if err != nil {
		return WorkItem{}, false
	}
	for _, item := range list.Items {
		switch m.Kind {
		case engagement.KindMeetingPrep:
			if item.Source == workSourcePrep && item.Ref.EventID == m.SubjectID {
				return item, true
			}
		case engagement.KindWorkFinished:
			if item.Ref.TaskID == m.SubjectID || item.Ref.HandoffID == m.SubjectID {
				return item, true
			}
		}
	}
	return WorkItem{}, false
}

func mobileNoticeDestination(kind engagement.Kind, subject, moment, sourceBrief, dedupeKey string) (string, string) {
	switch kind {
	case engagement.KindWeeklyRecap:
		if mobileValidBriefID(sourceBrief) {
			return "brief", sourceBrief
		}
	case engagement.KindBriefReady:
		if mobileValidBriefID(subject) {
			return "brief", subject
		}
	case engagement.KindWorkBlocked, engagement.KindWorkFinished:
		if mobileNoticeDestinationIDValid(subject) {
			return "task", subject
		}
	default:
		// Direct chat cards retain their original identity in the dedupe key;
		// the engagement ID can be a one-way hash of that identity.
		if strings.HasPrefix(dedupeKey, "chat:") {
			cardID := strings.TrimPrefix(dedupeKey, "chat:")
			if mobileNoticeDestinationIDValid(cardID) {
				return "chat", cardID
			}
		}
		if mobileNoticeDestinationIDValid(moment) {
			return "chat", moment
		}
	}
	return "", ""
}

// engagementDesktop shows the moment as a desktop toast: the generic line as
// the title, the specific text as the body only while unlocked, and the kind's
// first action as the button the phone push also offers. What the user does
// with the toast is recorded as a receipt, as it is on the phone.
func (a *App) engagementDesktop(_ context.Context, row storage.EngagementMomentRow, m engagement.Moment, _ engagement.Verdict, text engagementText) error {
	if !a.engagementDeliveryAllowsRead(context.Background(), m) {
		return errors.New("outcome source held")
	}
	if strings.TrimSpace(text.Title) == "" {
		return errors.New("empty notification text")
	}
	if notificationExplicitlyDenied() {
		// Nothing would appear, so the moment is not counted as sent; the
		// service holds it and the router tries another channel later.
		return errors.New("notifications are off for Isoform in System Settings")
	}
	spec, _ := engagement.Spec(m.Kind)
	if m.Params["_activity_thread"] != "" {
		spec.Actions = []engagement.Action{{ID: "open"}}
	}
	receipt := func(state, action string) func() {
		return func() { a.engagementDesktopReceipt(row.ID, state, action) }
	}
	// Every action of the kind (notify v1.0.0 B6), and the answer Your work
	// takes from a notification: Not needed on prep, Useful on finished work.
	actions := engagementToastActions(spec.Actions, text)
	item, answerable := a.workItemForNotice(m)
	answerable = answerable && (item.State == workStateOpen || item.State == workStateBack)
	if answerable {
		work := workNoticeAction(m.Kind)
		actions = append(actions, toastAction{ID: work.ID, Label: work.Label})
	}
	title, body := engagementToastCopy(text, a.engagementPresence(time.Now()).ScreenLocked)
	showDesktopToastWithOptions(title, body, a.notifyIconPath(), engagementClickData(m), toastOptions{
		Actions: actions,
		OnAction: func(action string) {
			receipt(storage.EngagementReceiptOpened, action)()
			if answerable {
				a.answerWorkFromNotification(item.ID, action)
			}
		},
		OnShown:     receipt(storage.EngagementReceiptDelivered, ""),
		OnOpened:    receipt(storage.EngagementReceiptOpened, ""),
		OnDismissed: receipt(storage.EngagementReceiptDismissed, ""),
	})
	return nil
}

// engagementToastActions are a kind's actions as toast buttons, each with its
// words; an action without words is left out.
func engagementToastActions(specActions []engagement.Action, text engagementText) []toastAction {
	out := make([]toastAction, 0, len(specActions)+1)
	for _, action := range specActions {
		if label := strings.TrimSpace(text.Actions[action.ID]); label != "" {
			out = append(out, toastAction{ID: action.ID, Label: label})
		}
	}
	return out
}

// answerWorkFromNotification records the answer a notification's button
// gives, as the phone's notification does (surface "notification").
func (a *App) answerWorkFromNotification(itemID, action string) {
	var input WorkAnswerInput
	switch action {
	case "not_needed":
		input = WorkAnswerInput{Action: workActionNo, Reason: "not_needed"}
	case "useful":
		input = WorkAnswerInput{Action: workActionUseful}
	default:
		return
	}
	if _, err := a.answerWork(context.Background(), itemID, input, workAnswerMeta{Surface: workSurfaceNotification}); err != nil {
		log.Printf("[engagement] the notification's answer was not kept: %v", err)
	}
}

// A toast shown while the computer is locked (no phone could be reached, so
// it waits in Notification Center) carries only the generic line.
func engagementToastCopy(text engagementText, locked bool) (string, string) {
	if locked {
		return text.PushBody, ""
	}
	return text.PushBody, text.Title
}

// engagementClickData is what a clicked toast hands the frontend
// ("notification:clicked"): card notifications open their main-chat item,
// except weekly recaps, which open their source brief on Today.
func engagementClickData(m engagement.Moment) string {
	switch m.Kind {
	case engagement.KindMeetingEnded:
		if m.SubjectID != "" {
			return "transcript:" + m.SubjectID
		}
		return ""
	case engagement.KindAnswerReady:
		if sessionID, messageID := m.Params["session_id"], m.Params["message_id"]; sessionID != "" && messageID != "" {
			return "chat-message:" + sessionID + ":" + messageID
		}
		return "main-chat:work"
	case engagement.KindWeeklyRecap:
		if briefID := m.Params["source_brief_id"]; briefID != "" {
			return "today:brief:" + briefID
		}
		return "today:brief"
	default:
		if engagementChatCardKind(m.Kind) {
			id := m.ID
			if id == "" {
				id = engagementReportKey(m, time.Now())
			}
			if id != "" {
				return "main-chat-item:" + id
			}
		}
		return ""
	}
}

func answerReadyDedupeKey(id string) string { return "answer_ready:" + id }

func (a *App) produceAnswerReady(answer storage.OpenAnswerRow, item storage.StreamItemRow) {
	if a == nil || a.engagement == nil || a.store == nil {
		return
	}
	firstLine := strings.TrimSpace(strings.Split(strings.TrimSpace(item.Content), "\n")[0])
	if firstLine == "" {
		return
	}
	now := time.Now()
	moment := engagement.Moment{
		ID:           uuid.NewString(),
		Kind:         engagement.KindAnswerReady,
		SubjectID:    answer.ID,
		DedupeKey:    answerReadyDedupeKey(answer.ID),
		CreatedAt:    now,
		DueAt:        time.UnixMilli(answer.ExpiresAt),
		ExpiresAt:    time.UnixMilli(answer.ExpiresAt),
		HardDeadline: answer.WindowStart > 0,
		Stakes:       0.7,
		Params: map[string]string{
			"subject":    answer.Topic,
			"first_line": firstLine,
			"session_id": answer.ChatSessionID,
			"message_id": item.ID,
		},
	}
	if err := a.submitEngagementSignal(context.Background(), moment); err != nil {
		log.Printf("[engagement] answer ready for %s: %v", answer.ID, err)
	}
}

func engagementChatCardKind(kind engagement.Kind) bool {
	switch kind {
	case engagement.KindDecisionDue, engagement.KindCommitmentSlipping,
		engagement.KindPersonReappeared, engagement.KindGoalCheckIn,
		engagement.KindWorkFinished, engagement.KindWorkBlocked,
		engagement.KindBriefReady, engagement.KindBriefMissed,
		engagement.KindScheduleMissed, engagement.KindSuggestion,
		engagement.KindTopicFinds, engagement.KindLearnedToConfirm,
		engagement.KindCapturePaused, engagement.KindMeetingPrep,
		engagement.KindCalendarConflict, engagement.KindWaitingOnReply,
		engagement.KindPlaybookSuggested, engagement.KindPlaybookResult,
		engagement.KindReminder, engagement.KindFirstWeek, engagement.KindWeeklyRecap:
		return true
	default:
		return false
	}
}

// engagementDesktopReceipt records what the user did with a desktop toast.
func (a *App) engagementDesktopReceipt(momentID, state, action string) {
	if a == nil || a.engagement == nil {
		return
	}
	if state == storage.EngagementReceiptActed {
		state = storage.EngagementReceiptOpened
	}
	if err := a.engagement.Receipt(context.Background(), momentID, string(engagement.ChannelDesktop), state, action, time.Time{}); err != nil {
		log.Printf("[engagement] desktop receipt for %s: %v", momentID, err)
	}
}

// engagementReceipt records a phone receipt against the moment it belongs to.
// The notice id is the moment id, so unknown ids are ignored.
func (a *App) engagementReceipt(_ string, noticeID, state, action string, at time.Time) {
	a.engagementReceiptOn(engagement.ChannelPush, noticeID, state, action, at)
}

// engagementReceiptOn records a receipt from a channel and applies what an
// acted receipt decides (a suggestion answered), the same for a phone button
// and a texted reply code.
func (a *App) engagementReceiptOn(channel engagement.Channel, momentID, state, action string, at time.Time) {
	if a == nil || a.engagement == nil {
		return
	}
	ctx := context.Background()
	reportedState := state
	if state == storage.EngagementReceiptActed {
		// Older phones use acted for a button press. Only linked-item
		// completion can produce the measured acted state.
		state = storage.EngagementReceiptOpened
		if action == "dismiss" {
			state = storage.EngagementReceiptDismissed
		}
	}
	if err := a.engagement.Receipt(ctx, momentID, string(channel), state, action, at); err != nil {
		log.Printf("[engagement] receipt for %s: %v", momentID, err)
		return
	}
	a.applySuggestionReceipt(ctx, momentID, reportedState, action)
}

// devicesWithScope copies the paired device records that hold a scope.
func (bridge *mobileBridge) devicesWithScope(scope string) []mobileDeviceRecord {
	if bridge == nil {
		return nil
	}
	bridge.mu.Lock()
	defer bridge.mu.Unlock()
	out := make([]mobileDeviceRecord, 0, len(bridge.devices))
	for _, record := range bridge.devices {
		if record == nil || !mobileHasScope(record.Scopes, scope) {
			continue
		}
		out = append(out, *record)
	}
	return out
}

// announceLatestBriefIfUnannounced announces today's stored morning or evening
// brief once the Engagement service is up (see announceBriefStoredBeforeEngagement).
func (a *App) announceLatestBriefIfUnannounced() {
	svc, ok := a.mobileBriefSource().(*mobileBriefService)
	if !ok || svc == nil || svc.store == nil {
		return
	}
	brief, found := svc.store.latest()
	if !found || brief.LocalDate != time.Now().Format("2006-01-02") {
		return
	}
	a.announceBriefStoredBeforeEngagement(brief)
}
