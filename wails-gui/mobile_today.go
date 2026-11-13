package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"wails-gui/internal/engagement"
	"wails-gui/storage"
)

// MobileTodayV1 is a phone-safe snapshot assembled away from the request path.
type MobileTodayV1 struct {
	GeneratedAt string `json:"generatedAt"`
	// CurrentBrief names the one current brief by id and package version
	// (PROTOCOL.md section 8.1). LatestBrief stays for older phones.
	CurrentBrief  *mobileTodayBriefRef     `json:"currentBrief,omitempty"`
	LatestBrief   *mobileTodayBrief        `json:"latestBrief,omitempty"`
	Briefs        []mobileTodayBrief       `json:"briefs"`
	NeedsYou      []mobileTodayNeed        `json:"needsYou"`
	NeedsYouCount int                      `json:"needsYouCount"`
	TasksVersion  string                   `json:"tasksVersion,omitempty"`
	Day           mobileTodayDay           `json:"day"`
	Topics        []mobileTopicView        `json:"topics"`
	BriefRequest  *mobileTodayBriefRequest `json:"briefRequest,omitempty"`
	Schedule      MobileBriefSchedule      `json:"schedule"`
	NoticePacing  *mobileTodayNoticePacing `json:"noticePacing,omitempty"`
	// AssistantSignOut is additive (R75): present only when the chosen
	// assistant is confirmed signed out on the computer.
	AssistantSignOut *mobileTodayAssistantSignOut `json:"assistantSignOut,omitempty"`
	KeepAwake        bool                         `json:"keepAwake"`
	InstantPush      bool                         `json:"instantPush"`
	BriefSchedule    []mobileBriefSlotTime        `json:"briefSchedule"`
}

type mobileTodayNoticePacing struct {
	ActiveToday    int    `json:"activeToday"`
	LastQuietBreak string `json:"lastQuietBreak,omitempty"`
}

func mobileTodayPacing(svc *engagementService, now time.Time) *mobileTodayNoticePacing {
	if svc == nil {
		return nil
	}
	svc.mu.Lock()
	if svc.state == nil {
		svc.mu.Unlock()
		return nil
	}
	pace := &mobileTodayNoticePacing{}
	if svc.state.Day == now.Format("2006-01-02") {
		pace.ActiveToday = svc.state.InterruptsToday
	}
	if !svc.state.LastQuietBreak.IsZero() {
		pace.LastQuietBreak = mobileTimestamp(svc.state.LastQuietBreak)
	}
	budget := svc.prefs.DailyInterruptBudget
	svc.mu.Unlock()
	if svc.store != nil {
		midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
		notices, err := svc.store.EngagementNotificationsBetween(context.Background(), midnight, now)
		if err != nil {
			// An unreadable counter must not give the phone a fresh allowance.
			pace.ActiveToday = budget
			return pace
		}
		pace.ActiveToday = 0
		for _, notice := range notices {
			if engagementAutomaticNotification(engagement.Kind(notice.Kind)) {
				pace.ActiveToday++
			}
		}
	}
	return pace
}

type mobileTodayBriefRequest struct {
	State   string `json:"state"`
	ReadyAt string `json:"readyAt"`
}

type mobileTodayBriefRef struct {
	ID      string `json:"id"`
	Version string `json:"version"`
}

type mobileTodayBrief struct {
	ID string `json:"id"`
	// Kind and Version come from the brief's package; BodyVersion repeats
	// Version for phones that read only it.
	Kind            string  `json:"kind,omitempty"`
	Version         string  `json:"version,omitempty"`
	Slot            string  `json:"slot"`
	LocalDate       string  `json:"localDate"`
	ScheduledAt     string  `json:"scheduledAt,omitempty"`
	MadeAt          string  `json:"madeAt"`
	CatchUp         bool    `json:"catchUp,omitempty"`
	DurationSeconds float64 `json:"durationSeconds,omitempty"`
	Lead            string  `json:"lead"`
	BodyVersion     string  `json:"bodyVersion"`
}

type mobileTodayNeed struct {
	ID              string              `json:"id"`
	ChatID          string              `json:"chatId,omitempty"`
	Card            *mobileChatItemView `json:"card,omitempty"`
	Task            *MobileTaskView     `json:"task,omitempty"`
	NoticeID        string              `json:"noticeId"`
	CreatedAt       string              `json:"createdAt"`
	Kind            string              `json:"kind"`
	Priority        string              `json:"priority"`
	ExpiresAt       string              `json:"expiresAt,omitempty"`
	DestinationKind string              `json:"destinationKind,omitempty"`
	DestinationID   string              `json:"destinationId,omitempty"`
}

// Reads happen only during the background projection build. The same identity
// lookup is used when a chat card records a receipt for its engagement notice.
func mobileTodayNeedNotice(ctx context.Context, app *App, need mobileTodayNeed, createdAt string, now time.Time) (mobileTodayNeed, error) {
	if need.Task != nil {
		need.DestinationKind, need.DestinationID = "task", need.Task.ID
	} else if need.Card != nil && need.Card.Proactive != nil {
		need.DestinationKind, need.DestinationID = "chat", need.Card.Proactive.MomentID
	}
	need.NoticeID = "task:" + need.ID
	need.CreatedAt = createdAt
	need.Kind = string(engagement.KindWorkBlocked)
	need.Priority = string(engagement.InterruptionActive)
	if app == nil || app.store == nil || need.Card == nil || need.Card.Proactive == nil {
		return need, nil
	}
	cardID := need.Card.Proactive.MomentID
	row, err := app.store.GetEngagementMoment(ctx, cardID)
	if err != nil {
		return need, err
	}
	if row == nil {
		id := mainChatEngagementMomentID(cardID)
		row, err = app.store.GetEngagementMoment(ctx, id)
	}
	if err == nil && row == nil && need.Card.Proactive.Work != nil {
		id, lookupErr := app.mainChatReceiptMomentID(ctx, cardID)
		if lookupErr != nil {
			return need, lookupErr
		}
		row, err = app.store.GetEngagementMoment(ctx, id)
	}
	if err != nil || row == nil {
		return need, err
	}
	moment := momentFromRow(*row)
	need.NoticeID = row.ID
	need.CreatedAt = mobileTimestamp(moment.CreatedAt)
	need.Kind = row.Kind
	if !moment.ExpiresAt.IsZero() {
		need.ExpiresAt = mobileTimestamp(moment.ExpiresAt)
	}
	if spec, ok := engagement.Spec(moment.Kind); ok {
		need.Priority = string(spec.Interruption(moment, now))
	} else {
		need.Priority = string(engagement.InterruptionPassive)
	}
	return need, nil
}

type mobileTodayRange struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

type mobileTodayDay struct {
	Next              *TodayDayShapeEvent `json:"next,omitempty"`
	Free              []mobileTodayRange  `json:"free"`
	CalendarConnected bool                `json:"calendarConnected"`
}

// serveTodayMeetingPrep resolves the selected calendar source again at the
// time of the read. A withdrawn source never turns a cached event into data.
func (bridge *mobileBridge) serveTodayMeetingPrep(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" || len(id) > 2048 || bridge.app == nil {
		mobileJSON(w, http.StatusNotFound, nil, "meeting unavailable")
		return
	}
	ctx := r.Context()
	stamp, err := bridge.app.peopleReadStamp(ctx)
	if err != nil {
		mobileJSON(w, http.StatusServiceUnavailable, nil, "meeting unavailable")
		return
	}
	prep, err := bridge.app.todayBeforeYouTalkMeeting(ctx, id, time.Now(), true)
	if errors.Is(err, errTodayChatItemUnavailable) || err == nil && prep == nil {
		mobileJSON(w, http.StatusNotFound, nil, "meeting unavailable")
		return
	}
	if err != nil {
		mobileJSON(w, http.StatusServiceUnavailable, nil, "meeting unavailable")
		return
	}
	release, err := bridge.app.lockPeoplePublication(ctx, stamp)
	if err != nil {
		mobileJSON(w, http.StatusServiceUnavailable, nil, "meeting unavailable")
		return
	}
	defer release()
	mobileJSON(w, http.StatusOK, prep, "")
}

type mobileTodayCache struct {
	mu sync.RWMutex
	// publishMu orders privacy invalidation with the event loop's publish of the
	// projection it copied. Request handlers never take it.
	publishMu sync.Mutex
	buildMu   sync.Mutex
	// build replaces buildToday in unit tests.
	build      func() (MobileTodayV1, error)
	value      *MobileTodayV1
	built      bool
	hash       [32]byte
	version    uint64
	fullParts  []string
	briefParts []string
	epoch      uint64
	updates    chan struct{}
	deadlines  chan struct{}
	changed    chan struct{}
	wake       chan struct{}
	once       sync.Once
	// loopMu guards loopsStarted, so the Today loops start at most once.
	loopMu       sync.Mutex
	loopsStarted bool
	// Optional observer for the coalescer's unit tests.
	onEvent       func(uint64)
	buildInterval time.Duration // zero uses the production minimum
}

// mobileTodayFreeRanges ports TodayDayStrip's interval union and 45-minute
// minimum, clipped to the part of the strip that remains today.
func mobileTodayFreeRanges(shape TodayDayShape, now time.Time) []mobileTodayRange {
	type span struct{ start, end time.Time }
	start, e1 := time.Parse(time.RFC3339, shape.RangeStart)
	end, e2 := time.Parse(time.RFC3339, shape.RangeEnd)
	if e1 != nil || e2 != nil || !end.After(start) {
		return []mobileTodayRange{}
	}
	if now.After(start) {
		start = now
	}
	if !end.After(start) {
		return []mobileTodayRange{}
	}
	occupied := []span{}
	add := func(a, b string) {
		x, err1 := time.Parse(time.RFC3339, a)
		y, err2 := time.Parse(time.RFC3339, b)
		if err1 == nil && err2 == nil && y.After(x) {
			occupied = append(occupied, span{x, y})
		}
	}
	for _, event := range shape.Events {
		add(event.Start, event.End)
	}
	if shape.Hold != nil {
		add(shape.Hold.Start, shape.Hold.End)
	}
	sort.Slice(occupied, func(i, j int) bool { return occupied[i].start.Before(occupied[j].start) })
	out := []mobileTodayRange{}
	appendFree := func(a, b time.Time) {
		if b.Sub(a) >= 45*time.Minute {
			out = append(out, mobileTodayRange{Start: a.Format(time.RFC3339), End: b.Format(time.RFC3339)})
		}
	}
	cursor := start
	for _, event := range occupied {
		if !event.end.After(cursor) || !event.start.Before(end) {
			continue
		}
		if event.start.After(cursor) {
			appendFree(cursor, event.start)
		}
		if event.end.After(cursor) {
			cursor = event.end
		}
		if cursor.After(end) {
			cursor = end
		}
	}
	appendFree(cursor, end)
	return out
}

func mobileTodayDayFromShape(shape TodayDayShape, now time.Time) mobileTodayDay {
	day := mobileTodayDay{CalendarConnected: shape.CalendarConnected, Free: []mobileTodayRange{}}
	if !shape.CalendarConnected {
		return day
	}
	day.Free = mobileTodayFreeRanges(shape, now)
	for i := range shape.Events {
		start, err := time.Parse(time.RFC3339, shape.Events[i].Start)
		if err != nil || start.Before(now) {
			continue
		}
		if day.Next == nil {
			event := shape.Events[i]
			day.Next = &event
			continue
		}
		previous, _ := time.Parse(time.RFC3339, day.Next.Start)
		if start.Before(previous) {
			event := shape.Events[i]
			day.Next = &event
		}
	}
	return day
}

func mobileTodayBriefFrom(summary MobileBriefSummary, brief MobileBriefV1, schedule MobileBriefSchedule) mobileTodayBrief {
	out := mobileTodayBrief{ID: summary.BriefID, Slot: summary.Slot, LocalDate: summary.LocalDate, MadeAt: summary.AsOf, Lead: summary.Lead}
	if brief.Audio != nil && (brief.Audio.Status == "" || brief.Audio.Status == mobileBriefAudioStatusOK) {
		out.DurationSeconds = brief.Audio.DurationSeconds
	}
	clock := ""
	switch brief.Slot {
	case mobileBriefSlotMorning:
		clock = schedule.Morning
	case mobileBriefSlotEvening:
		clock = schedule.Evening
	}
	if clock != "" {
		loc, err := time.LoadLocation(brief.Timezone)
		if err != nil {
			loc = time.Local
		}
		if scheduled, err := time.ParseInLocation("2006-01-02 15:04", brief.LocalDate+" "+clock, loc); err == nil {
			out.ScheduledAt = scheduled.Format(time.RFC3339)
		}
	}
	if brief.Generation != nil {
		if brief.Generation.ScheduledAt != "" {
			out.ScheduledAt = brief.Generation.ScheduledAt
		}
		out.CatchUp = brief.Generation.Reason == "mac_asleep"
	}
	return out
}

func mobileTodayCatchUp(made time.Time, scheduled string, recorded bool) bool {
	scheduledAt, err := time.Parse(time.RFC3339, scheduled)
	if err != nil {
		return false
	}
	gap := made.Sub(scheduledAt)
	if gap < 0 {
		gap = -gap
	}
	// Legacy records may only have today's schedule as an approximation.
	// A large gap is still enough to identify an obviously missed slot.
	threshold := 4 * time.Hour
	if recorded {
		threshold = 10 * time.Minute
	}
	return gap > threshold
}

func (bridge *mobileBridge) buildToday() (MobileTodayV1, error) {
	svc := bridge.briefSource()
	if svc == nil {
		return MobileTodayV1{}, errMobileBriefUnavailable
	}
	now := time.Now()
	view := MobileTodayV1{GeneratedAt: mobileTimestamp(now), Briefs: []mobileTodayBrief{}, NeedsYou: []mobileTodayNeed{}, Topics: []mobileTopicView{}, Day: mobileTodayDay{Free: []mobileTodayRange{}}}
	view.Schedule, _ = svc.Schedule()
	view.KeepAwake = bridge.keepAwakeSetting()
	view.InstantPush = mobileInstantPushConfigured()
	view.BriefSchedule = mobileBriefSlotTimes(now, time.Local, view.Schedule)
	view.Briefs = bridge.todayBriefs(svc, now, view.Schedule)
	if len(view.Briefs) > 0 {
		latest := view.Briefs[0]
		view.LatestBrief = &latest
	}
	// The current brief is the one /brief/latest serves (mobile_brief_current.go).
	if current, ok := currentMobileBrief(svc); ok {
		if pkg, ok := bridge.briefPackage(current.BriefID); ok {
			view.CurrentBrief = &mobileTodayBriefRef{ID: current.BriefID, Version: pkg.Version}
		}
	}
	if source, ok := svc.(*mobileBriefService); ok {
		source.mu.Lock()
		if source.phoneRequest != nil {
			request := mobileTodayBriefRequest{State: source.phoneRequest.state, ReadyAt: formatMobileBriefTime(source.phoneRequest.readyAt)}
			view.BriefRequest = &request
		}
		source.mu.Unlock()
	}
	app := bridge.app
	if app == nil {
		return view, nil
	}
	view.NoticePacing = mobileTodayPacing(app.engagement, now)
	view.AssistantSignOut = app.mobileTodaySignOut(routeOrder(loadAIProviderConfig()))
	seen := map[string]bool{}
	if app.storeForMobile() != nil {
		cursor := ""
		pages := [][]mobileTodayNeed{}
		for {
			page, err := app.ListMainChatWaitingItems(cursor, 200)
			if err != nil {
				return MobileTodayV1{}, err
			}
			pageNeeds := []mobileTodayNeed{}
			for _, item := range page.Items {
				row := storage.StreamItemRow{ID: item.ID, Kind: item.Kind, Content: item.Content, ToolName: item.ToolName, ToolInput: item.ToolInput, SortOrder: item.SortOrder, CreatedAt: item.CreatedAt}
				card, visible := mobileChatItemFromRow(row)
				if !visible || card.Proactive == nil || !mobileTodayLiveCard(card.Proactive, now) {
					continue
				}
				id := card.Proactive.MomentID
				if seen[id] {
					continue
				}
				seen[id] = true
				if card.Proactive.Work != nil && card.Proactive.Work.HandoffID != "" {
					seen[card.Proactive.Work.HandoffID] = true
				}
				if card.Proactive.Permission != nil && card.Proactive.Permission.HandoffID != "" {
					seen[card.Proactive.Permission.HandoffID] = true
				}
				if card.Proactive.Target.Kind == mainChatTargetHandoff && card.Proactive.Target.ID != "" {
					seen[card.Proactive.Target.ID] = true
				}
				card = displayModelCopy(card, app.modelDisplayLanguage())
				need, err := mobileTodayNeedNotice(context.Background(), app, mobileTodayNeed{ID: id, ChatID: page.SessionID, Card: &card}, card.At, now)
				if err != nil {
					return MobileTodayV1{}, err
				}
				pageNeeds = append(pageNeeds, need)
			}
			pages = append(pages, pageNeeds)
			if !page.HasMore {
				break
			}
			if page.NextCursor == "" || page.NextCursor == cursor {
				return MobileTodayV1{}, errMobileStoreUnavailable
			}
			cursor = page.NextCursor
		}
		for i := len(pages) - 1; i >= 0; i-- {
			view.NeedsYou = append(view.NeedsYou, pages[i]...)
		}
		if !app.IsClosedEyesModeEnabled() {
			topics, err := app.GetTodayTopics()
			if err != nil {
				return MobileTodayV1{}, err
			}
			if topics.Refreshing && len(topics.Topics) == 0 {
				bridge.today.mu.RLock()
				if bridge.today.value != nil {
					view.Topics = append(view.Topics, bridge.today.value.Topics...)
				}
				bridge.today.mu.RUnlock()
			}
			view.Topics = append(view.Topics, mobileTodayTopicsWithChat(topics.Topics, func(id string) bool {
				page, err := app.ListMainChatTopicItems(id, "", 1)
				return err == nil && len(page.Items) > 0
			})...)
		}
	}
	if app.handoffStore != nil {
		entries, err := app.handoffStore.List()
		if err != nil {
			return MobileTodayV1{}, err
		}
		parts := make([]string, 0, len(entries))
		for _, entry := range entries {
			opened := int64(0)
			if entry.Thread != nil {
				opened = entry.Thread.OpenedAt
			}
			parts = append(parts, entry.ID+"\x00"+string(entry.Status)+"\x00"+strconv.FormatInt(entry.UpdatedAt, 10)+"\x00"+strconv.FormatInt(opened, 10))
		}
		sort.Strings(parts)
		sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
		view.TasksVersion = hex.EncodeToString(sum[:])
		subjects, err := mobileGoalSubjects(app)
		if err != nil {
			return MobileTodayV1{}, err
		}
		for _, task := range mobileTaskPageWithLimit(entries, subjects, now, len(entries)).Tasks {
			if task.Group != mobileTaskGroupNeedsYou || seen[task.ID] {
				continue
			}
			seen[task.ID] = true
			taskCopy := task
			need, err := mobileTodayNeedNotice(context.Background(), app, mobileTodayNeed{ID: task.ID, Task: &taskCopy}, task.CreatedAt, now)
			if err != nil {
				return MobileTodayV1{}, err
			}
			view.NeedsYou = append(view.NeedsYou, need)
		}
	}
	view.NeedsYouCount = len(view.NeedsYou)
	if app.store != nil {
		shape, err := app.GetTodayDayShape(now.In(time.Local).Format("2006-01-02"))
		if err != nil {
			return MobileTodayV1{}, err
		}
		view.Day = mobileTodayDayFromShape(shape, now)
	}
	return view, nil
}

// todayBriefs lists the readable briefs of the last 14 days, newest first, each
// named by its package version. The first is the current brief.
func (bridge *mobileBridge) todayBriefs(svc mobileBriefSource, now time.Time, schedule MobileBriefSchedule) []mobileTodayBrief {
	// One noticed lookup for the whole pass: every stored brief reads the same
	// days of Noticed, so the pass reads each day once, not once per brief.
	pass := withMobileBriefNoticedPass(context.Background(), bridge.app)
	briefs := []mobileTodayBrief{}
	scheduled := map[string]string{}
	if source, ok := svc.(*mobileBriefService); ok && source.runs != nil {
		for _, run := range source.runs.list() {
			if run.BriefID != "" && run.ScheduledAt != "" {
				scheduled[run.BriefID] = run.ScheduledAt
			}
		}
	}
	for _, row := range mobileBriefSummariesWithLeads(svc, mobileBriefStoreListMax) {
		made, err := time.Parse(time.RFC3339, row.AsOf)
		if err != nil || made.Before(now.AddDate(0, 0, -14)) || row.Lead == "" || row.NarrativeStatus != "ok" {
			continue
		}
		brief, ok := svc.GetMobileBrief(row.BriefID)
		if !ok {
			continue
		}
		pkg, ok := bridge.briefPackageContext(pass, row.BriefID)
		if !ok {
			continue
		}
		entry := mobileTodayBriefFrom(row, brief, schedule)
		entry.Kind = mobileBriefPackageKind(brief)
		entry.Version, entry.BodyVersion = pkg.Version, pkg.Version
		if at := scheduled[row.BriefID]; at != "" {
			entry.ScheduledAt = at
		}
		if !entry.CatchUp && (entry.Slot == mobileBriefSlotMorning || entry.Slot == mobileBriefSlotEvening) {
			recorded := scheduled[row.BriefID] != "" || brief.Generation != nil && brief.Generation.ScheduledAt != ""
			entry.CatchUp = mobileTodayCatchUp(made, entry.ScheduledAt, recorded)
		}
		briefs = append(briefs, entry)
	}
	return briefs
}

// The indexed chat read belongs to the background snapshot build, never the route.
func mobileTodayTopicsWithChat(topics []TopicView, hasItems func(string) bool) []mobileTopicView {
	result := []mobileTopicView{}
	for _, topic := range mobileBounded(topics, topicRegistryLimit) {
		if hasItems(topic.ID) {
			result = append(result, mobileTopicProjection(topic))
		}
	}
	return result
}

func mobileTodayLiveCard(card *mainChatProactive, now time.Time) bool {
	if card.Kind == "thread_update_unavailable" || mainChatProactiveRowClass(*card, now) != MainChatRowWaiting {
		return false
	}
	if card.Kind == "digest" {
		children := make([]mainChatProactive, 0, len(card.Items))
		for _, child := range card.Items {
			if mobileTodayLiveCard(&child, now) {
				children = append(children, child)
			}
		}
		card.Items = children
		return len(children) != 0
	}
	return true
}

func mobileTodayCardExpiry(card *mainChatProactive) time.Time {
	var next time.Time
	if card.ExpiresAt > 0 {
		next = time.UnixMilli(card.ExpiresAt)
	}
	for i := range card.Items {
		at := mobileTodayCardExpiry(&card.Items[i])
		if !at.IsZero() && (next.IsZero() || at.Before(next)) {
			next = at
		}
	}
	return next
}

const mobileTodayPartBytes = 24 * 1024

func mobileTodayParts(view MobileTodayV1) ([]string, error) {
	raw, err := json.Marshal(view)
	if err != nil {
		return nil, err
	}
	parts := make([]string, 0, (len(raw)+mobileTodayPartBytes-1)/mobileTodayPartBytes)
	for len(raw) > 0 {
		n := min(len(raw), mobileTodayPartBytes)
		parts = append(parts, base64.RawURLEncoding.EncodeToString(raw[:n]))
		raw = raw[n:]
	}
	return parts, nil
}

// serveToday answers from the cached projection and never builds one. While no
// projection is cached it answers 503 at once and asks the background loop to
// build one, so a slow read can never hold the phone's request open.
func (bridge *mobileBridge) serveToday(w http.ResponseWriter, r *http.Request, device mobileDeviceRecord) {
	bridge.today.mu.RLock()
	if bridge.today.value == nil {
		bridge.today.mu.RUnlock()
		bridge.noteTodayChanged()
		mobileJSON(w, http.StatusServiceUnavailable, nil, errMobileStoreUnavailable.Error())
		return
	}
	// The parts are replaced on each build, never mutated, so they stay valid after the lock is released.
	snapshot := *bridge.today.value
	fullParts, briefParts := bridge.today.fullParts, bridge.today.briefParts
	version := bridge.today.version
	bridge.today.mu.RUnlock()
	view := mobileTodayForDevice(snapshot, device)
	parts := fullParts
	if !mobileHasScope(device.Scopes, mobileScopeChatRead) {
		parts = briefParts
	}
	if rawPart := r.URL.Query().Get("part"); rawPart != "" {
		part, err := strconv.Atoi(rawPart)
		requestedVersion, versionErr := strconv.ParseUint(r.URL.Query().Get("version"), 10, 64)
		if err != nil || versionErr != nil || part < 0 {
			mobileJSON(w, http.StatusBadRequest, nil, "the Today snapshot part is invalid")
			return
		}
		if requestedVersion != version {
			mobileJSON(w, http.StatusConflict, nil, "the Today snapshot changed")
			return
		}
		if part >= len(parts) {
			mobileJSON(w, http.StatusBadRequest, nil, "the Today snapshot part is invalid")
			return
		}
		mobileJSON(w, http.StatusOK, map[string]any{"version": version, "part": part, "data": parts[part]}, "")
		return
	}
	if !mobileTopicResponseFits(view) {
		mobileJSON(w, http.StatusOK, map[string]any{"chunked": true, "version": version, "parts": len(parts)}, "")
		return
	}
	mobileJSON(w, http.StatusOK, view, "")
}

func mobileTodayForDevice(view MobileTodayV1, device mobileDeviceRecord) MobileTodayV1 {
	if !mobileHasScope(device.Scopes, mobileScopeChatRead) {
		filtered := make([]mobileTodayNeed, 0, len(view.NeedsYou))
		for _, need := range view.NeedsYou {
			if need.Card == nil {
				filtered = append(filtered, need)
			}
		}
		view.NeedsYou = filtered
		view.NeedsYouCount = len(filtered)
		view.Topics = []mobileTopicView{}
	}
	return view
}

// buildTodayView produces the projection. Tests replace the builder through
// today.build; production always uses buildToday.
func (bridge *mobileBridge) buildTodayView() (MobileTodayV1, error) {
	if bridge.today.build != nil {
		return bridge.today.build()
	}
	return bridge.buildToday()
}

// warmToday builds the first projection in the background loop, before any phone
// asks for one. It retries until a build succeeds or the bridge stops.
func (bridge *mobileBridge) warmToday() bool {
	for {
		if _, err := bridge.rebuildToday(); err == nil {
			return true
		}
		timer := time.NewTimer(10 * time.Second)
		select {
		case <-bridge.ctx.Done():
			timer.Stop()
			return false
		case <-timer.C:
		}
	}
}

func (bridge *mobileBridge) rebuildToday() (MobileTodayV1, error) {
	cache := &bridge.today
	cache.buildMu.Lock()
	defer cache.buildMu.Unlock()
	cache.mu.RLock()
	epoch := cache.epoch
	cache.mu.RUnlock()
	view, err := bridge.buildTodayView()
	if err != nil {
		return MobileTodayV1{}, err
	}
	compare := view
	compare.GeneratedAt = ""
	encoded, err := json.Marshal(compare)
	if err != nil {
		return MobileTodayV1{}, err
	}
	hash := sha256.Sum256(encoded)
	fullParts, err := mobileTodayParts(view)
	if err != nil {
		return MobileTodayV1{}, err
	}
	briefParts, err := mobileTodayParts(mobileTodayForDevice(view, mobileDeviceRecord{}))
	if err != nil {
		return MobileTodayV1{}, err
	}
	cache.mu.Lock()
	if cache.epoch != epoch {
		cache.mu.Unlock()
		return MobileTodayV1{}, errMobileStoreUnavailable
	}
	changed := cache.version == 0 || cache.hash != hash
	if changed {
		cache.version++
	}
	if changed || cache.value == nil {
		cache.value, cache.hash = &view, hash
		cache.fullParts, cache.briefParts = fullParts, briefParts
		cache.built = true
	} else {
		view = *cache.value
	}
	cache.mu.Unlock()
	select {
	case cache.deadlines <- struct{}{}:
	default:
	}
	if changed {
		select {
		case cache.changed <- struct{}{}:
		default:
		}
	}
	return view, nil
}

func (bridge *mobileBridge) startTodayCache() {
	bridge.today.once.Do(func() {
		bridge.today.updates = make(chan struct{}, 1)
		bridge.today.deadlines = make(chan struct{}, 1)
		bridge.today.changed = make(chan struct{}, 1)
		bridge.today.wake = make(chan struct{}, 1)
	})
	// A bridge without a lifecycle context (tests, or one built before start) has
	// no way to stop the loops, so they are not started. A call made before the
	// context is set must not use up the start: the first call that has one starts them.
	bridge.today.loopMu.Lock()
	defer bridge.today.loopMu.Unlock()
	if bridge.today.loopsStarted || bridge.ctx == nil || !bridge.trackBackground() {
		return
	}
	bridge.today.loopsStarted = true
	go bridge.todayLoop()
	if bridge.trackBackground() {
		go bridge.todayEventLoop()
	}
}

func (bridge *mobileBridge) todayEventLoop() {
	defer bridge.background.Done()
	var sent uint64
	var lastSent time.Time
	for {
		force := false
		select {
		case <-bridge.ctx.Done():
			return
		case <-bridge.today.changed:
		case <-bridge.today.wake:
			force = true
		}
		delay := time.Until(lastSent.Add(2 * time.Second))
		if delay < 0 {
			delay = 0
		}
		timer := time.NewTimer(delay)
		select {
		case <-bridge.ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		for len(bridge.today.changed) > 0 {
			<-bridge.today.changed
		}
		for len(bridge.today.wake) > 0 {
			<-bridge.today.wake
			force = true
		}
		// publishMu orders privacy invalidation with this publish. The lock is released
		// before the relay sends, so GET /today only ever waits for a short copy.
		bridge.today.publishMu.Lock()
		bridge.today.mu.RLock()
		if bridge.today.value == nil || bridge.today.version == sent && !force {
			bridge.today.mu.RUnlock()
			bridge.today.publishMu.Unlock()
			continue
		}
		view, version := *bridge.today.value, bridge.today.version
		bridge.today.mu.RUnlock()
		bridge.publishEventForDevices(mobileScopeBriefRead, func(device mobileDeviceRecord) []byte {
			payload, _ := json.Marshal(map[string]any{"event": "today_changed", "version": version, "snapshot": mobileTodayForDevice(view, device)})
			if len(payload) > mobileMaxMessagePlaintext {
				payload, _ = json.Marshal(map[string]any{"event": "today_changed", "version": version})
			}
			return payload
		})
		sent = version
		lastSent = time.Now()
		if bridge.today.onEvent != nil {
			bridge.today.onEvent(version)
		}
		bridge.today.publishMu.Unlock()
	}
}

// Tell active phones to retry queued writes when the Mac reconnects, even if
// the cached projection itself has not changed since before sleep.
func (bridge *mobileBridge) wakeTodayPhones() {
	bridge.startTodayCache()
	select {
	case bridge.today.wake <- struct{}{}:
	default:
	}
}

func (bridge *mobileBridge) noteTodayChanged() {
	bridge.startTodayCache()
	select {
	case bridge.today.updates <- struct{}{}:
	default:
	}
}

// Withdrawn data must not remain available from the fast cache while a
// background rebuild is in flight.
func (bridge *mobileBridge) invalidateToday() {
	// Waiting for publishMu lets a publish of the withdrawn projection finish first, so
	// no phone receives it after this returns.
	bridge.today.publishMu.Lock()
	bridge.today.mu.Lock()
	bridge.today.epoch++
	if bridge.today.value != nil {
		bridge.today.version++
	}
	bridge.today.value = nil
	bridge.today.fullParts, bridge.today.briefParts = nil, nil
	bridge.today.mu.Unlock()
	bridge.today.publishMu.Unlock()
	bridge.noteTodayChanged()
}

func (bridge *mobileBridge) todayLoop() {
	defer bridge.background.Done()
	// The first projection is built here, before any phone asks, so GET /today is answered from the cache.
	if !bridge.warmToday() {
		return
	}
	lastBuild := time.Now()
	interval := bridge.today.buildInterval
	if interval <= 0 {
		interval = 10 * time.Second
	}
	ticker := time.NewTicker(cadencePhoneToday)
	defer ticker.Stop()
	var expiry *time.Timer
	var expiryC <-chan time.Time
	defer func() {
		if expiry != nil {
			expiry.Stop()
		}
	}()
	for {
		select {
		case <-bridge.ctx.Done():
			return
		case <-bridge.today.deadlines:
			if expiry != nil {
				expiry.Stop()
			}
			expiryC = nil
			bridge.today.mu.RLock()
			var next time.Time
			if bridge.today.value != nil {
				for _, need := range bridge.today.value.NeedsYou {
					if need.Card == nil || need.Card.Proactive == nil {
						continue
					}
					at := mobileTodayCardExpiry(need.Card.Proactive)
					if at.IsZero() {
						continue
					}
					if next.IsZero() || at.Before(next) {
						next = at
					}
				}
			}
			bridge.today.mu.RUnlock()
			if !next.IsZero() {
				delay := time.Until(next)
				if delay < 0 {
					delay = 0
				}
				expiry = time.NewTimer(delay)
				expiryC = expiry.C
			}
		case <-expiryC:
			expiryC = nil
			bridge.noteTodayChanged()
		case <-ticker.C:
			bridge.noteTodayChanged()
		case <-bridge.today.updates:
			// Bursts of normal writes share a rebuild, with a minimum gap
			// even when updates continue arriving during a slow build.
			delay := time.Until(lastBuild.Add(interval))
			if delay < 0 {
				delay = 0
			}
			timer := time.NewTimer(delay)
			select {
			case <-bridge.ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			for len(bridge.today.updates) > 0 {
				<-bridge.today.updates
			}
			_, _ = bridge.rebuildToday()
			lastBuild = time.Now()
		}
	}
}
