package main

import (
	"context"
	"encoding/json"
	"log"
	"strings"
	"sync"
	"time"

	"wails-gui/internal/engagement"
	"wails-gui/internal/privacy"
	"wails-gui/storage"
)

type MainChatWaitingState struct {
	SessionID     string           `json:"sessionId"`
	Day           string           `json:"day"`
	Count         int              `json:"count"`
	Conversations int              `json:"conversations"`
	Finished      int              `json:"finished"`
	Routine       int              `json:"routine"`
	Items         []StreamItemData `json:"items"`
	Revision      uint64           `json:"revision"`
	Ready         bool             `json:"ready"`
}

const mainChatWaitingChangedEvent = "main-chat:waiting-changed"

type mainChatWaitingStateHolder struct {
	mu            sync.Mutex
	state         MainChatWaitingState
	generation    uint64
	running       bool
	dirty         bool
	stopped       bool
	started       bool
	cancel        context.CancelFunc
	done          chan struct{}
	workers       sync.WaitGroup
	timer         *time.Timer
	retryTimer    *time.Timer
	retryDelay    time.Duration
	unsubscribe   func()
	beforeCompute func()
	compute       func() (MainChatWaitingState, time.Time, error)
}

func (a *App) GetMainChatWaiting() (perfValue MainChatWaitingState) {
	if chatFeatureErr() != nil {
		return perfValue
	}
	defer logPageBinding("GetMainChatWaiting", time.Now(), &perfValue)
	if a == nil {
		return MainChatWaitingState{Items: []StreamItemData{}}
	}
	a.mainChatWaiting.mu.Lock()
	perfValue = a.mainChatWaiting.state
	perfValue.Items = append([]StreamItemData(nil), perfValue.Items...)
	a.mainChatWaiting.mu.Unlock()
	if perfValue.Items == nil {
		perfValue.Items = []StreamItemData{}
	}
	return perfValue
}

func (a *App) startMainChatWaitingLifecycle(parent context.Context) {
	if a == nil {
		return
	}
	holder := &a.mainChatWaiting
	holder.mu.Lock()
	if holder.started || holder.stopped {
		holder.mu.Unlock()
		return
	}
	holder.started = true
	ctx, cancel := context.WithCancel(parent)
	holder.cancel = cancel
	holder.done = make(chan struct{})
	holder.unsubscribe = subscribePrivacyWithdrawals(func(privacy.Withdrawal) {
		a.markMainChatWaitingDirty("privacy-withdrawal")
	})
	done := holder.done
	holder.mu.Unlock()

	go func() {
		defer close(done)
		ticker := time.NewTicker(10 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				a.markMainChatWaitingDirty("safety-resync")
			}
		}
	}()
	a.markMainChatWaitingDirty("startup")
}

func (a *App) stopMainChatWaitingLifecycle() {
	if a == nil {
		return
	}
	holder := &a.mainChatWaiting
	holder.mu.Lock()
	holder.stopped = true
	cancel, done, unsubscribe := holder.cancel, holder.done, holder.unsubscribe
	holder.cancel = nil
	holder.unsubscribe = nil
	if holder.timer != nil {
		holder.timer.Stop()
		holder.timer = nil
	}
	if holder.retryTimer != nil {
		holder.retryTimer.Stop()
		holder.retryTimer = nil
	}
	holder.mu.Unlock()
	if unsubscribe != nil {
		unsubscribe()
	}
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
	holder.workers.Wait()
}

func (a *App) markMainChatWaitingDirty(reason string) {
	if a == nil {
		return
	}
	holder := &a.mainChatWaiting
	holder.mu.Lock()
	if holder.stopped || !holder.started {
		holder.mu.Unlock()
		return
	}
	holder.generation++
	if reason == "privacy-withdrawal" || strings.HasPrefix(reason, "privacy:") {
		holder.state.Count = 0
		holder.state.Items = []StreamItemData{}
		holder.state.Revision++
	}
	if holder.running {
		holder.dirty = true
		holder.mu.Unlock()
		return
	}
	holder.running = true
	holder.workers.Add(1)
	holder.mu.Unlock()
	go a.runMainChatWaitingWorker(reason)
}

func (a *App) runMainChatWaitingWorker(reason string) {
	holder := &a.mainChatWaiting
	defer holder.workers.Done()
	for {
		holder.mu.Lock()
		generation := holder.generation
		holder.dirty = false
		beforeCompute := holder.beforeCompute
		compute := holder.compute
		holder.mu.Unlock()
		if beforeCompute != nil {
			beforeCompute()
		}
		if compute == nil {
			compute = a.computeMainChatWaiting
		}
		state, nextExpiry, err := compute()
		if err != nil {
			log.Printf("[main-chat] waiting resync failed: %s", safeLogError(err))
			holder.mu.Lock()
			if holder.stopped {
				holder.running = false
				holder.dirty = false
				holder.mu.Unlock()
				return
			}
			if generation != holder.generation {
				holder.mu.Unlock()
				continue
			}
			if holder.retryTimer == nil {
				delay := holder.retryDelay
				if delay <= 0 {
					delay = 30 * time.Second
				}
				holder.retryTimer = time.AfterFunc(delay, func() {
					holder.mu.Lock()
					holder.retryTimer = nil
					holder.mu.Unlock()
					a.markMainChatWaitingDirty("retry")
				})
			}
			if holder.dirty {
				holder.mu.Unlock()
				continue
			}
			holder.running = false
			holder.mu.Unlock()
			return
		}
		holder.mu.Lock()
		if holder.stopped {
			holder.running = false
			holder.dirty = false
			holder.mu.Unlock()
			return
		}
		if generation != holder.generation {
			holder.mu.Unlock()
			continue
		}
		if holder.retryTimer != nil {
			holder.retryTimer.Stop()
			holder.retryTimer = nil
		}
		previous := holder.state
		state.Ready = true
		state.Revision = previous.Revision + 1
		changed := previous.Count != state.Count ||
			previous.SessionID != state.SessionID ||
			previous.Day != state.Day ||
			previous.Conversations != state.Conversations ||
			previous.Finished != state.Finished ||
			previous.Routine != state.Routine ||
			!sameWaitingItemIDs(previous.Items, state.Items)
		holder.state = state
		if holder.timer != nil {
			holder.timer.Stop()
			holder.timer = nil
		}
		if !nextExpiry.IsZero() {
			delay := time.Until(nextExpiry)
			if delay < 0 {
				delay = 0
			}
			holder.timer = time.AfterFunc(delay, func() {
				a.markMainChatWaitingDirty("expiry")
			})
		}
		runAgain := holder.dirty
		holder.dirty = false
		if !runAgain {
			holder.running = false
		}
		holder.mu.Unlock()

		if changed {
			a.emitBrowserEvent(mainChatWaitingChangedEvent, state)
		}
		if !runAgain {
			return
		}
		reason = "coalesced:" + reason
	}
}

func (a *App) computeMainChatWaiting() (MainChatWaitingState, time.Time, error) {
	now := time.Now()
	start := mainChatDayStart(now)
	out := MainChatWaitingState{Day: start.Format("2006-01-02"), Items: []StreamItemData{}}
	if a == nil || a.store == nil {
		return out, time.Time{}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ctx = withMainChatReadMemo(ctx)
	step := time.Now()
	snapshot, err := a.store.ReadMainChatView(ctx, start.UnixMilli())
	readTime := time.Since(step)
	if err != nil {
		log.Printf("[main-chat] waiting resync timing read=%s (failed)", readTime)
		return out, time.Time{}, withLogStage("read main chat view", err)
	}
	out.SessionID = snapshot.SessionID
	// Classify with only the display changes that can affect a row's class.
	// Full copy, receipt and brief enrichment is needed only for visible cards.
	step = time.Now()
	projected := make([]StreamItemData, len(snapshot.Items))
	for i, row := range snapshot.Items {
		if row.Kind != "system" {
			continue
		}
		switch row.ToolName {
		case mainChatProactiveTool:
			projected[i].ToolInput = a.projectMainChatWaitingClass(ctx, row.ToolInput)
		case mainChatObservationPauseTool:
			projected[i].ToolInput = a.projectObservationPause(row.ToolInput)
		}
	}
	classTime := time.Since(step)
	step = time.Now()
	out.Conversations, out.Finished, out.Routine = mainChatTodayCounts(snapshot.Items, projected, now)
	count, ids, _ := projectMainChatWaitingRows(snapshot.Items, projected, now, 5)
	out.Count = count
	nextExpiry := earliestMainChatWaitingExpiry(snapshot.Items, projected, now)
	countTime := time.Since(step)
	step = time.Now()
	selected := make([]storage.StreamItemRow, 0, len(ids))
	wanted := make(map[string]bool, len(ids))
	for _, id := range ids {
		wanted[id] = true
	}
	for _, row := range snapshot.Items {
		if wanted[row.ID] {
			selected = append(selected, row)
		}
	}
	visible := a.projectMainChatRows(context.WithValue(ctx, waitingProjectionTimingKey{}, true), selected)
	_, _, out.Items = projectMainChatWaitingRows(selected, visible, now, 5)
	visibleTime := time.Since(step)
	log.Printf("[main-chat] waiting resync timing read=%s classify=%s counts=%s visible=%s rows=%d selected=%d", readTime, classTime, countTime, visibleTime, len(snapshot.Items), len(selected))
	if err := ctx.Err(); err != nil {
		return MainChatWaitingState{Items: []StreamItemData{}}, time.Time{}, withLogStage("waiting budget after projection", err)
	}
	// Topic membership is optional enrichment with its own budget. Exhausting
	// it must not discard a valid count or prevent the worker from publishing.
	step = time.Now()
	release, err := a.projectMainChatView(context.Background(), out.SessionID, out.Items, now)
	log.Printf("[main-chat] waiting resync timing topics=%s", time.Since(step))
	if err != nil {
		return MainChatWaitingState{Items: []StreamItemData{}}, time.Time{}, withLogStage("project topic membership", err)
	}
	defer release()
	for i, j := 0, len(out.Items)-1; i < j; i, j = i+1, j-1 {
		out.Items[i], out.Items[j] = out.Items[j], out.Items[i]
	}
	return out, nextExpiry, nil
}

// Only these display transformations can change mainChatRowClassForDisplay.
// Keep content projection on the normal path before a card is exposed.
func (a *App) projectMainChatWaitingClass(ctx context.Context, input string) string {
	if len(input) > mobileChatPageBudget {
		return ""
	}
	var card mainChatProactive
	if json.Unmarshal([]byte(input), &card) != nil || card.MomentID == "" || card.Kind == "" {
		return ""
	}
	var project func(*mainChatProactive, bool)
	project = func(item *mainChatProactive, nested bool) {
		if item.Kind == string(engagement.KindPersonReappeared) || item.Kind == string(engagement.KindFirstWeek) && item.Target.Kind == "person" {
			item.Kind = "thread_update_unavailable"
			return
		}
		if item.Kind == "first_read" {
			// A top-level first read contributes no count whether available or
			// withdrawn. Inside a digest it can affect the envelope class.
			if nested {
				a.projectFirstRead(ctx, item, a.currentUILanguage())
			}
			return
		}
		if item.Kind == string(engagement.KindWeeklyRecap) && strings.TrimSpace(item.BriefID) == "" && !(item.Target.Kind == mainChatTargetToday && mobileValidBriefID(item.Target.ID)) {
			item.Kind = "thread_update_unavailable"
			return
		}
		if item.ActivityOutcomeSource != "" {
			a.projectThreadOutcomeItem(ctx, item, a.currentUILanguage())
		}
		for i := range item.Items {
			project(&item.Items[i], true)
		}
	}
	project(&card, false)
	raw, err := json.Marshal(card)
	if err != nil {
		return ""
	}
	return a.projectMainChatEngagementPrefs(mainChatProactiveTool, string(raw))
}

func (a *App) mainChatRowsChanged(sessionID string) {
	if a == nil {
		return
	}
	holder := &a.mainChatWaiting
	holder.mu.Lock()
	isMainSession := !holder.state.Ready || holder.state.SessionID == "" || holder.state.SessionID == sessionID
	holder.mu.Unlock()
	if !isMainSession && sessionID != "" && a.store != nil {
		session, err := a.store.LoadSession(context.Background(), sessionID)
		isMainSession = err == nil && session.Kind == storage.SessionKindMain
	}
	if isMainSession {
		a.markMainChatWaitingDirty("main-chat-write")
	}
}

func projectMainChatWaitingRows(rows []storage.StreamItemRow, projected []StreamItemData, now time.Time, limit int) (int, []string, []StreamItemData) {
	count := 0
	ids := make([]string, 0, min(limit, len(rows)))
	items := make([]StreamItemData, 0, min(limit, len(rows)))
	for i, row := range rows {
		if i >= len(projected) || mainChatRowClassForDisplay(row, projected[i].ToolInput, now) != MainChatRowWaiting {
			continue
		}
		count++
		if limit > 0 && len(items) < limit {
			ids = append(ids, row.ID)
			item := projected[i]
			if row.ToolName == mainChatProactiveTool {
				item.ToolInput = mainChatDigestWaitingProjection(item.ToolInput, now)
			}
			items = append(items, item)
		}
	}
	return count, ids, items
}

func mainChatDigestWaitingProjection(input string, now time.Time) string {
	var payload map[string]json.RawMessage
	if json.Unmarshal([]byte(input), &payload) != nil {
		return input
	}
	rawChildren, ok := payload["items"]
	if !ok {
		return input
	}
	var children []json.RawMessage
	if json.Unmarshal(rawChildren, &children) != nil {
		return input
	}
	waitingChildren := make([]json.RawMessage, 0, len(children))
	for _, rawChild := range children {
		var child mainChatProactive
		if json.Unmarshal(rawChild, &child) == nil && mainChatProactiveRowClass(child, now) == MainChatRowWaiting {
			waitingChildren = append(waitingChildren, rawChild)
		}
	}
	filtered, err := json.Marshal(waitingChildren)
	if err != nil {
		return input
	}
	payload["items"] = filtered

	decoder := json.NewDecoder(strings.NewReader(input))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return input
	}
	var start, end int
	found := false
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return input
		}
		key, ok := token.(string)
		if !ok {
			return input
		}
		valueStart := jsonValueStart(input, int(decoder.InputOffset()))
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return input
		}
		if key == "items" {
			start, end, found = valueStart, int(decoder.InputOffset()), true
		}
	}
	if !found || start < 0 || end > len(input) || start > end {
		return input
	}
	return input[:start] + string(payload["items"]) + input[end:]
}

func jsonValueStart(input string, offset int) int {
	for offset < len(input) {
		switch input[offset] {
		case ' ', '\t', '\n', '\r', ':':
			offset++
		default:
			return offset
		}
	}
	return offset
}

func earliestMainChatWaitingExpiry(rows []storage.StreamItemRow, projected []StreamItemData, now time.Time) time.Time {
	var earliest time.Time
	for i, row := range rows {
		if i >= len(projected) || row.ToolName != mainChatProactiveTool ||
			mainChatRowClassForDisplay(row, projected[i].ToolInput, now) != MainChatRowWaiting {
			continue
		}
		var card mainChatProactive
		if json.Unmarshal([]byte(projected[i].ToolInput), &card) != nil {
			continue
		}
		var visit func(mainChatProactive)
		visit = func(item mainChatProactive) {
			if item.ExpiresAt > now.UnixMilli() {
				expiry := time.UnixMilli(item.ExpiresAt)
				if earliest.IsZero() || expiry.Before(earliest) {
					earliest = expiry
				}
			}
			for _, child := range item.Items {
				if mainChatProactiveRowClass(child, now) == MainChatRowWaiting {
					visit(child)
				}
			}
		}
		visit(card)
	}
	return earliest
}

func sameWaitingItemIDs(left, right []StreamItemData) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i].ID != right[i].ID {
			return false
		}
	}
	return true
}
