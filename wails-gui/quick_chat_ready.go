package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"wails-gui/internal/pii"
)

// Ready work first in Quick chat (computer-chat v1.0.0 rule 9, v1.1.0 rule
// 7, redesign-work rule 15): when what the person typed matches work Isoform
// already made, that item comes first, with Open and Open in Chat. The match
// reads Your work's snapshot (work_list.go, Today's store) on this computer
// only; nothing is sent to a model and the typed words are never logged.

// quickChatReadyMaxQuery bounds the words the panel may send for a match.
const quickChatReadyMaxQuery = 500

// quickChatReadyStopWords are words that say nothing about which item is
// meant ("prep me for Lena" is about prep and Lena).
var quickChatReadyStopWords = map[string]bool{
	"a": true, "an": true, "the": true, "for": true, "me": true, "my": true, "to": true, "of": true, "on": true,
	"in": true, "and": true, "or": true, "with": true, "about": true, "is": true, "it": true, "this": true,
	"that": true, "what": true, "can": true, "you": true, "please": true, "i": true, "do": true, "get": true,
	"show": true, "give": true, "make": true, "your": true, "from": true, "at": true, "be": true,
}

// QuickChatReadyItem is the made item the panel shows first: "Ready · made
// at {time}", its title and when it's for.
type QuickChatReadyItem struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	MadeAt string `json:"madeAt"`
	// ForText and ForAt say when it's for: a prep's meeting and its start.
	ForText string `json:"forText,omitempty"`
	ForAt   string `json:"forAt,omitempty"`
}

// quickChatReadyWords are the typed words that can name an item.
func quickChatReadyWords(text string) []string {
	words := []string{}
	for _, word := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !(r == '\'' || r == '-' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r > 127)
	}) {
		word = strings.TrimSuffix(strings.Trim(word, "'-"), "'s")
		if len([]rune(word)) < 2 || quickChatReadyStopWords[word] {
			continue
		}
		words = append(words, word)
	}
	return words
}

// workItemMade says whether Isoform already made the item's work: a prep it
// prepared, or a result back from an agent, not one the person turned down.
// Offers, claims and requests are not made work.
func workItemMade(item WorkItem) bool {
	if item.Kind != workKindDone && item.Kind != workKindResult {
		return false
	}
	if item.Answer != nil {
		switch item.Answer.Action {
		case "no", "not_mine", "not_useful":
			return false
		}
	}
	return strings.TrimSpace(item.Work.Title) != ""
}

// quickChatReadyMatch finds the made item the typed words name. Every typed
// word is in its title or text, or every word of its title was typed; the
// item matching more words wins, then the newest. Closed Eyes: no matching.
func quickChatReadyMatch(list WorkList, text string, closedEyes bool) *QuickChatReadyItem {
	if closedEyes {
		return nil
	}
	typed := quickChatReadyWords(text)
	if len(typed) == 0 {
		return nil
	}
	var best *WorkItem
	bestScore := 0
	for i := range list.Items {
		item := list.Items[i]
		if !workItemMade(item) {
			continue
		}
		title := strings.ToLower(pii.DisplayText(item.Work.Title))
		body := title + "\n" + strings.ToLower(pii.DisplayText(item.Work.Text))
		score := 0
		for _, word := range typed {
			if strings.Contains(body, word) {
				score++
			}
		}
		titleWords := quickChatReadyWords(title)
		allTyped := score == len(typed)
		titleTyped := len(titleWords) > 0
		for _, word := range titleWords {
			found := false
			for _, t := range typed {
				if t == word {
					found = true
					break
				}
			}
			if !found {
				titleTyped = false
				break
			}
		}
		if !allTyped && !titleTyped {
			continue
		}
		if best == nil || score > bestScore || score == bestScore && item.CreatedAt > best.CreatedAt {
			best, bestScore = &list.Items[i], score
		}
	}
	if best == nil {
		return nil
	}
	return quickChatReadyItemOf(*best, list.GeneratedAt)
}

// quickChatReadyItemOf words the item for the panel. It was made by the time
// the list was built at the latest, so a prep never says it was made later.
func quickChatReadyItemOf(item WorkItem, generatedAt string) *QuickChatReadyItem {
	made := item.CreatedAt
	if at, err := time.Parse(time.RFC3339, made); err != nil {
		made = generatedAt
	} else if built, err := time.Parse(time.RFC3339, generatedAt); err == nil && at.After(built) {
		made = generatedAt
	}
	ready := &QuickChatReadyItem{ID: item.ID, Title: pii.DisplayText(item.Work.Title), MadeAt: made}
	for _, part := range item.First {
		switch part.Key {
		case "for_meeting":
			ready.ForText = part.Text
			if ready.ForAt == "" {
				ready.ForAt = part.At
			}
		case "when":
			ready.ForAt = part.At
		}
	}
	return ready
}

// QuickChatReady is the made item the typed words name, or nil.
func (a *App) quickChatReady(text string) (*QuickChatReadyItem, error) {
	if a.IsClosedEyesModeEnabled() {
		return nil, nil
	}
	list, err := a.workListNow()
	if err != nil {
		return nil, err
	}
	return quickChatReadyMatch(list, text, false), nil
}

// openQuickChatReady opens a made item from Quick chat. inChat opens Chat
// with the item attached and the typed words in the box, as a reply from an
// item does; else Today, where the item is.
func (a *App) openQuickChatReady(id, text string, inChat bool) error {
	list, err := a.workListNow()
	if err != nil {
		return err
	}
	item, ok := workFind(list, id)
	if !ok || !workItemMade(item) {
		return errTodayChatItemUnavailable
	}
	if a.ctx == nil {
		return nil
	}
	a.showMainWindowFromSpotlight()
	payload := map[string]string{"itemId": item.ID}
	if inChat {
		payload["prompt"] = strings.TrimSpace(text)
		if payload["prompt"] == "" {
			payload["prompt"] = item.Work.Title
		}
	}
	event := quickChatOpenWorkEvent
	if inChat {
		event = quickChatChatWorkEvent
	}
	a.emitBrowserEvent(event, payload)
	a.dismissSpotlight()
	return nil
}

const (
	quickChatOpenWorkEvent = "spotlight:open-work"
	quickChatChatWorkEvent = "spotlight:chat-work"
)

// ── /api/spotlight/ready and /api/spotlight/ready/open ───────────────

type spotlightReadyRequest struct {
	Text string `json:"text"`
}

type spotlightReadyResponse struct {
	Item *QuickChatReadyItem `json:"item"`
}

// handleSpotlightReady matches typed words against made work. A POST, so the
// words never land in a URL or an access log.
func (hb *hostBridge) handleSpotlightReady(w http.ResponseWriter, r *http.Request) {
	var req spotlightReadyRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&req); err != nil {
		hb.writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if hb.app == nil {
		hb.writeError(w, http.StatusServiceUnavailable, "app not initialized")
		return
	}
	text := req.Text
	if len(text) > quickChatReadyMaxQuery {
		text = text[:quickChatReadyMaxQuery]
	}
	item, err := hb.app.quickChatReady(text)
	if err != nil {
		hb.writeError(w, http.StatusInternalServerError, "ready work could not be read")
		return
	}
	writeJSON(w, http.StatusOK, spotlightReadyResponse{Item: item})
}

type spotlightReadyOpenRequest struct {
	ID     string `json:"id"`
	Text   string `json:"text"`
	InChat bool   `json:"inChat"`
}

func (hb *hostBridge) handleSpotlightReadyOpen(w http.ResponseWriter, r *http.Request) {
	var req spotlightReadyOpenRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 72<<10)).Decode(&req); err != nil || strings.TrimSpace(req.ID) == "" {
		hb.writeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	if hb.app == nil {
		hb.writeError(w, http.StatusServiceUnavailable, "app not initialized")
		return
	}
	if err := hb.app.openQuickChatReady(strings.TrimSpace(req.ID), req.Text, req.InChat); err != nil {
		hb.writeError(w, http.StatusConflict, "this item is not ready any more")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
