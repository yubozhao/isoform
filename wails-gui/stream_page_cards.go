package main

import (
	"context"
	"log"
	"os"
	"time"
)

// A saved first page may hold proactive cards. Their words come from calendar,
// reminders, meeting notes and receipts, and a withdrawal of one of those
// sources changes neither the privacy stamp nor the stream item version. So a
// copy that holds cards is served only after each card's raw row has been run
// through the same projection the live read uses (projectStreamItemsContext and
// the page view, projectMainChatView), on the card rows alone. The gates read
// the same state the live path reads, so they cannot drift from it.
//
// The check runs after the copy is served (confirmStreamPageCardsLater): on a
// real stream of 48 cards the live gates took 1 to 16 s, so a check before the
// first paint never finished in time and the copy was never served. All the card
// checks share one deadline. When a card cannot be confirmed (the gates change
// it, or its stored row is missing), the copy is dropped and the page is told
// to reload, so it shows the live page, never the copy's cards again. This is
// the same rule as the full privacy check of a copy (verifyStreamPageCopy). A
// check that only ran out of time keeps the copy and the next refresh checks
// again: a timeout withdraws nothing, and every withdrawal made in Isoform
// drops the copy through the privacy stamp before any read.
const streamPageCardBudget = 30 * time.Second

// streamPageCardCount counts the proactive cards at the top level of a page.
func streamPageCardCount(page StreamItemPageData) int {
	n := 0
	for _, item := range page.Items {
		if item.ToolName == mainChatProactiveTool {
			n++
		}
	}
	return n
}

// rawProactiveCards copies the proactive cards of items as stored. It must run
// before projectStreamItems, which rewrites the items in place. The copy holds
// the values as the row stores them, so a read can project them again.
func rawProactiveCards(items []StreamItemData) []StreamItemData {
	var out []StreamItemData
	for _, item := range items {
		if item.ToolName == mainChatProactiveTool {
			out = append(out, item)
		}
	}
	return out
}

// confirmProactiveCards projects the copy's cards through the live gates under
// one shared deadline and returns the page with its cards replaced. It returns
// the time the check took and a reason, which is empty only when every card is
// confirmed. It never reads the stream store beyond the handful of lookups the
// live gates make for these rows, and it never touches the rest of the page.
func (a *App) confirmProactiveCards(key streamPageKey, saved streamPageSnapshot, now time.Time, budget time.Duration) (StreamItemPageData, int64, string) {
	start := time.Now()
	elapsed := func() int64 { return time.Since(start).Milliseconds() }
	stored := make(map[string]StreamItemData, len(saved.Cards))
	for _, card := range saved.Cards {
		stored[card.ID] = card
	}
	var positions []int
	var clones []StreamItemData
	for i, item := range saved.Page.Items {
		if item.ToolName != mainChatProactiveTool {
			continue
		}
		card, ok := stored[item.ID]
		if !ok {
			return StreamItemPageData{}, elapsed(), "card-unstored"
		}
		positions = append(positions, i)
		clones = append(clones, card)
	}
	if len(clones) == 0 {
		return saved.Page, elapsed(), ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	// One read of each source for the whole check, and per-step timings in its
	// own log line (stage=enrich_detail, label card_check).
	ctx = withMainChatReadMemo(ctx)
	ctx = context.WithValue(ctx, streamProjectionTimingKey{}, true)
	ctx = context.WithValue(ctx, streamProjectionLabelKey{}, "card_check")
	done := make(chan struct{})
	// On a timeout this goroutine finishes alone: it works on its own clones and
	// releases its publication fence when it ends.
	go func() {
		defer close(done)
		a.projectStreamItemsContext(ctx, clones)
		release, err := a.projectMainChatView(ctx, key.session, clones, now)
		if err == nil {
			release()
		}
	}()
	select {
	case <-done:
	case <-ctx.Done():
		return StreamItemPageData{}, elapsed(), "card-check-timeout"
	}
	if ctx.Err() != nil {
		return StreamItemPageData{}, elapsed(), "card-check-timeout"
	}
	page := saved.Page
	page.Items = append([]StreamItemData(nil), saved.Page.Items...)
	for n, i := range positions {
		// The live gates must reproduce the card as the copy saved it. A card they
		// change (a withdrawal, a removed source) is not confirmed.
		if clones[n].ID != page.Items[i].ID || clones[n].Content != page.Items[i].Content || clones[n].ToolInput != page.Items[i].ToolInput {
			return StreamItemPageData{}, elapsed(), "card-changed"
		}
		page.Items[i] = clones[n]
	}
	return page, elapsed(), ""
}

// removeStreamPageCopy deletes a key's saved copy at once. A copy that holds a
// card the live gates did not confirm must not wait for the next save: its raw
// card row would otherwise stay on disk after the card was withdrawn.
func removeStreamPageCopy(key streamPageKey) {
	if path, err := streamPageFile(key); err == nil {
		_ = os.Remove(path)
	}
}

// confirmStreamPageCardsLater confirms the cards of a served copy in the
// background, one check per page at a time. A card the live gates do not
// reproduce drops the copy and reloads the page.
func (a *App) confirmStreamPageCardsLater(key streamPageKey, saved streamPageSnapshot, budget time.Duration) {
	if streamPageCardCount(saved.Page) == 0 {
		return
	}
	st := &a.streamPages
	st.mu.Lock()
	if st.cardChecks == nil {
		st.cardChecks = map[streamPageKey]bool{}
	}
	if st.cardChecks[key] {
		st.mu.Unlock()
		return
	}
	st.cardChecks[key] = true
	st.cardChecksQueued.Add(1)
	st.mu.Unlock()
	go func() {
		defer func() {
			st.mu.Lock()
			delete(st.cardChecks, key)
			st.mu.Unlock()
			st.cardChecksDone.Add(1)
		}()
		_, ms, why := a.confirmProactiveCards(key, saved, time.Now(), budget)
		log.Printf("[stream-page] card_check reason=%s cards=%d card_check_ms=%d", streamPageReasonOrHeld(why), streamPageCardCount(saved.Page), ms)
		if why == "" || why == "card-check-timeout" {
			// A check that ran out of time withdrew nothing: the copy stays, and
			// the next refresh checks again. An in-app withdrawal drops the copy
			// on its own (the privacy stamp), before any check.
			return
		}
		// Drop the copy only when it is still the one that was served; a newer
		// save is the live page already.
		if current, reason := readRawStreamPageSnapshot(key); reason == "" && current.SavedAt.Equal(saved.SavedAt) {
			removeStreamPageCopy(key)
		}
		a.emitStreamPageReload(key.session, "card-check")
	}()
}

func streamPageReasonOrHeld(why string) string {
	if why == "" {
		return "confirmed"
	}
	return why
}
