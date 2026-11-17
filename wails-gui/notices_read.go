package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"wails-gui/internal/engagement"
)

// Read anywhere, gone everywhere (notify v1.0.0 rules 8 and 9, B7; PROTOCOL.md
// 9.4). Reading a notice anywhere (opening the brief, the prep or the result
// in the app, on the phone or the watch, or pressing Read it, Not needed or
// Useful) takes its notification away everywhere: the computer closes its own
// toast at once, and the phone clears it from GET /notices/read on its next
// read and from the withdraw push. Swiping one away stays on that device.
//
// A notice is named by its id: a moment's notice id (the engagement moment
// id, the phone's yansu.id), and "brief:<briefId>" for the brief.

const (
	noticesReadKey  = "notices.read"
	noticesReadKeep = 48 * time.Hour
	noticesReadMax  = 500
	noticeBriefPre  = "brief:"
	// noticesWithdrawBatch is the relay's limit on ids in one withdraw push.
	noticesWithdrawBatch = 20
)

type noticeRead struct {
	ID string `json:"id"`
	At string `json:"at"`
}

var noticesReadMu sync.Mutex

// noticesWithdrawHook sends the phones' withdraw push for read notices (the
// relay's background push, B7); tests replace it.
var noticesWithdrawHook = func(a *App, ids []string) { a.sendNoticesWithdraw(ids) }

// sendNoticesWithdraw asks the relay for one background push per phone that
// takes the notices' notifications away. A relay that doesn't know the frame
// refuses it and stays connected; the phone's own poll still clears them.
func (a *App) sendNoticesWithdraw(ids []string) {
	bridge := a.mobileBridgeForUse()
	if bridge == nil {
		return
	}
	bridge.mu.Lock()
	client := bridge.client
	bridge.mu.Unlock()
	if client == nil {
		return
	}
	for len(ids) > 0 {
		batch := ids
		if len(batch) > noticesWithdrawBatch {
			batch = ids[:noticesWithdrawBatch]
		}
		ids = ids[len(batch):]
		for _, device := range bridge.devicesWithScope(mobileScopePush) {
			if err := client.send(map[string]any{"t": "withdraw", "deviceId": device.DeviceID, "ids": batch}); err != nil {
				log.Printf("[notices] withdraw push not sent: %v", err)
				return
			}
		}
	}
}

// noteNoticesRead records notices as read now: they leave the computer's
// screen, and the phones learn it.
func (a *App) noteNoticesRead(ids []string, at time.Time) {
	if a == nil || a.store == nil {
		return
	}
	clean := make([]string, 0, len(ids))
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" && len(id) <= 160 {
			clean = append(clean, id)
		}
	}
	if len(clean) == 0 {
		return
	}
	if at.IsZero() {
		at = time.Now()
	}
	noticesReadMu.Lock()
	list := a.noticesReadLocked(at)
	known := map[string]bool{}
	for _, read := range list {
		known[read.ID] = true
	}
	added := []string{}
	for _, id := range clean {
		if !known[id] {
			known[id] = true
			list = append(list, noticeRead{ID: id, At: mobileTimestamp(at)})
			added = append(added, id)
		}
	}
	if len(list) > noticesReadMax {
		list = list[len(list)-noticesReadMax:]
	}
	if raw, err := json.Marshal(list); err == nil {
		if err := a.store.SetUIState(context.Background(), noticesReadKey, string(raw)); err != nil {
			log.Printf("[notices] read notices not kept: %v", err)
		}
	}
	noticesReadMu.Unlock()
	if len(added) == 0 {
		return
	}
	closeDesktopToastsTagged(added)
	if noticesWithdrawHook != nil {
		go noticesWithdrawHook(a, added)
	}
}

// noticesReadLocked is the kept list without what is past the keep window.
func (a *App) noticesReadLocked(now time.Time) []noticeRead {
	raw, err := a.store.GetUIState(context.Background(), noticesReadKey)
	if err != nil || raw == "" {
		return []noticeRead{}
	}
	var list []noticeRead
	if json.Unmarshal([]byte(raw), &list) != nil {
		return []noticeRead{}
	}
	cutoff := now.Add(-noticesReadKeep)
	kept := list[:0]
	for _, read := range list {
		if at, err := time.Parse(time.RFC3339, read.At); err == nil && !at.Before(cutoff) {
			kept = append(kept, read)
		}
	}
	return kept
}

// noticeWasRead reports whether a notice is among the ones read anywhere.
func (a *App) noticeWasRead(id string) bool {
	if a == nil || a.store == nil {
		return false
	}
	now := time.Now()
	for _, read := range a.noticesReadSince(now.Add(-noticesReadKeep), now) {
		if read.ID == id {
			return true
		}
	}
	return false
}

// noticesReadSince are the notices read at or after since, oldest first.
func (a *App) noticesReadSince(since, now time.Time) []noticeRead {
	if a == nil || a.store == nil {
		return []noticeRead{}
	}
	noticesReadMu.Lock()
	list := a.noticesReadLocked(now)
	noticesReadMu.Unlock()
	out := []noticeRead{}
	for _, read := range list {
		if at, err := time.Parse(time.RFC3339, read.At); err == nil && !at.Before(since) {
			out = append(out, read)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At < out[j].At })
	return out
}

// noticeIDsForMoment are the ids a moment's notification may carry on a
// device: its notice id, and for the brief also "brief:<briefId>".
func noticeIDsForMoment(kind engagement.Kind, momentID, subjectID string) []string {
	ids := []string{momentID}
	if kind == engagement.KindBriefReady && subjectID != "" {
		ids = append(ids, noticeBriefPre+subjectID)
	}
	return ids
}

// noteMomentRead marks a moment's notification read: its toast and push go.
func (a *App) noteMomentRead(momentID string, at time.Time) {
	if a == nil || a.store == nil || momentID == "" {
		return
	}
	row, err := a.store.GetEngagementMoment(context.Background(), momentID)
	if err != nil || row == nil {
		return
	}
	a.noteNoticesRead(noticeIDsForMoment(engagement.Kind(row.Kind), row.ID, row.SubjectID), at)
}

// noteWorkItemRead marks read the notification about a Your work item that
// was answered: the meeting's prep, or the finished task's result.
func (a *App) noteWorkItemRead(item WorkItem, at time.Time) {
	if a == nil || a.store == nil {
		return
	}
	var key string
	switch {
	case item.Source == workSourcePrep && item.Ref.EventID != "":
		key = engagement.DedupeKey(engagement.KindMeetingPrep, item.Ref.EventID)
	case item.Ref.TaskID != "":
		key = engagement.DedupeKey(engagement.KindWorkFinished, item.Ref.TaskID)
	default:
		return
	}
	rows, err := a.store.SentEngagementMomentsSince(context.Background(), at.Add(-noticesReadKeep), 0)
	if err != nil {
		return
	}
	for _, row := range rows {
		if row.DedupeKey == key {
			a.noteNoticesRead(noticeIDsForMoment(engagement.Kind(row.Kind), row.ID, row.SubjectID), at)
		}
	}
}

// MarkBriefRead says the person opened this brief on the computer: its
// notification goes from the phone too.
func (a *App) MarkBriefRead(briefID string) error {
	if !mobileValidBriefID(briefID) {
		return nil
	}
	a.noteNoticesRead([]string{noticeBriefPre + briefID}, time.Now())
	return nil
}

// serveNoticesRead answers GET /notices/read?since=<RFC 3339>: the notices
// read anywhere in the last 48 hours, for the phone to clear.
func (bridge *mobileBridge) serveNoticesRead(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	since := now.Add(-noticesReadKeep)
	if raw := r.URL.Query().Get("since"); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			mobileJSON(w, http.StatusBadRequest, nil, "since must be an RFC 3339 time")
			return
		}
		if parsed.After(since) {
			since = parsed
		}
	}
	mobileJSON(w, http.StatusOK, map[string]any{"read": bridge.app.noticesReadSince(since, now)}, "")
}

// serveNoticesMarkRead answers POST /notices/read {"ids":[...]}: the phone
// read these in the app (the brief opened in Today), so they go everywhere.
func (bridge *mobileBridge) serveNoticesMarkRead(w http.ResponseWriter, r *http.Request) {
	var input struct {
		IDs []string `json:"ids"`
	}
	if !mobileDecode(w, r, &input) {
		return
	}
	if len(input.IDs) == 0 || len(input.IDs) > 50 {
		mobileJSON(w, http.StatusBadRequest, nil, "ids must hold 1 to 50 notice ids")
		return
	}
	bridge.app.noteNoticesRead(input.IDs, time.Now())
	mobileJSON(w, http.StatusOK, map[string]any{"read": len(input.IDs)}, "")
}
