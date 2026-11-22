package main

import (
	"errors"
	"io/fs"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"

	"wails-gui/internal/workonit"
)

// The watch on Activity (computer-activity v1.0.0 rule 4, v1.1.0 15e): a
// watch row is a conversation, by name and length, with who spoke and
// whether it has a mark or a note. An hour with no conversation in it (no
// speech) stays an hour row. Forget on either names everything that goes:
// the store deletes by the hour, so forgetting a conversation takes its
// hours, their notes, the follow-ups made from them, the claims Isoform read
// from those words and every other conversation in the same hours. Work
// already sent to an agent stays with the agent and is only named.

// errWatchConversationGone (watch_conversation_forget.go) also answers a row
// whose conversation's first words changed (an hour was deleted or withdrawn).

// watchConversationReach is how far past a window conversations are read,
// so one that starts in it is read whole and an hour drawn on by one that
// started before it is not listed as an hour of its own.
const watchConversationReach = 2 * time.Hour

// activityFeedWatch returns the watch's rows that start in [startMs, endMs).
func (a *App) activityFeedWatch(startMs, endMs int64) ([]ActivityFeedRow, error) {
	out := []ActivityFeedRow{}
	store := a.watchRecordingStore()
	if store == nil {
		return out, nil
	}
	recordings, err := store.all()
	if err != nil {
		return nil, err
	}
	convs, err := a.watchConversationsNear(store, recordings, startMs, endMs)
	if err != nil {
		return nil, err
	}
	return activityFeedWatchRows(recordings, convs, startMs, endMs), nil
}

// GetActivityWatchRows is the Watch tab's list: the same rows as the feed's,
// watch only.
func (a *App) GetActivityWatchRows(startMs, endMs int64) ([]ActivityFeedRow, error) {
	if a == nil || endMs <= startMs {
		return []ActivityFeedRow{}, nil
	}
	rows, err := a.activityFeedWatch(startMs, endMs)
	if err != nil {
		return nil, err
	}
	rows = activityFeedWithBasis(rows)
	sortActivityFeed(rows)
	return rows, nil
}

func watchRecordingStartMs(rec mobileRecording) (int64, bool) {
	started, err := time.Parse(time.RFC3339, rec.view().StartedAt)
	if err != nil {
		return 0, false
	}
	return started.UnixMilli(), true
}

func watchRecordingDevice(rec mobileRecording) string {
	if rec.Source == "phone" {
		return "phone"
	}
	return "watch"
}

// activityFeedWatchRows builds one row per conversation that starts in the
// window and one per hour in it that no conversation draws on.
func activityFeedWatchRows(recordings []mobileRecording, convs []WatchConversation, startMs, endMs int64) []ActivityFeedRow {
	out := []ActivityFeedRow{}
	byID := map[string]mobileRecording{}
	for _, rec := range recordings {
		byID[rec.ID] = rec
	}
	talked := map[string]bool{}
	for _, c := range convs {
		for _, id := range c.RecordingIDs {
			talked[id] = true
		}
		if c.StartAt < startMs || c.StartAt >= endMs {
			continue
		}
		out = append(out, watchConversationRow(c, byID))
	}
	for _, rec := range recordings {
		if rec.State == mobileRecordingDeleted || talked[rec.ID] {
			continue
		}
		at, ok := watchRecordingStartMs(rec)
		if !ok || at < startMs || at >= endMs {
			continue
		}
		out = append(out, ActivityFeedRow{ID: rec.ID, Kind: "watch", StartMs: at, EndMs: at + int64(rec.view().DurationSeconds*1000),
			Words: rec.Words, Device: watchRecordingDevice(rec), RecordingID: rec.ID, Basis: ActivityFeedBasis{Setting: basisWatch}})
	}
	return out
}

// watchConversationRow is a conversation as a row: its name, length, who
// spoke, its marks and whether it has notes. Never its words.
func watchConversationRow(c WatchConversation, byID map[string]mobileRecording) ActivityFeedRow {
	row := ActivityFeedRow{ID: c.ID, Kind: "watch", StartMs: c.StartAt, EndMs: c.EndAt, Title: c.Name, Words: c.Words,
		Device: "watch", Conversation: true, Speakers: []string{}, Basis: ActivityFeedBasis{Setting: basisWatch},
		Marks:   len(mobileConversationView(c, byID).Marks),
		HasNote: strings.TrimSpace(c.Summary) != "" || len(c.Decisions) > 0 || len(c.Followups) > 0}
	if len(c.RecordingIDs) > 0 {
		if rec, ok := byID[c.RecordingIDs[0]]; ok {
			row.Device = watchRecordingDevice(rec)
		}
	}
	for _, speaker := range c.Speakers {
		switch {
		case speaker == mobileRecordingSpeakerOwner:
			row.Speakers = append(row.Speakers, mobileRecordingSpeakerOwner)
		case strings.TrimSpace(c.SpeakerNames[speaker]) != "":
			row.Speakers = append(row.Speakers, strings.TrimSpace(c.SpeakerNames[speaker]))
		default:
			row.OtherVoices++
		}
	}
	return row
}

// watchConversationsNear builds the conversations around [startMs, endMs)
// from the hours near it only, reading further on while one that starts in
// the window runs to the edge. It only reads: names, notes and speaker
// names are applied as kept, never pruned, so a partial read loses nothing.
func (a *App) watchConversationsNear(store *mobileRecordingStore, recordings []mobileRecording, startMs, endMs int64) ([]WatchConversation, error) {
	from := startMs - watchConversationReach.Milliseconds()
	to := endMs + watchConversationReach.Milliseconds()
	latest := from
	for _, rec := range recordings {
		if at, ok := watchRecordingStartMs(rec); ok && at > latest {
			latest = at
		}
	}
	for {
		var near []mobileRecording
		for _, rec := range recordings {
			if at, ok := watchRecordingStartMs(rec); ok && at >= from && at < to {
				near = append(near, rec)
			}
		}
		speech, err := a.watchConversationSpeechOf(near, time.UnixMilli(from))
		if err != nil {
			return nil, err
		}
		convs := buildWatchConversations(speech, watchConversationSilence)
		cut := false
		for _, c := range convs {
			if c.StartAt < endMs && c.EndAt > to-watchConversationReach.Milliseconds() {
				cut = true
			}
		}
		if !cut || to > latest {
			applyKeptWatchConversationExtras(store.dir, convs)
			return convs, nil
		}
		to += 6 * time.Hour.Milliseconds()
	}
}

// applyKeptWatchConversationExtras sets the kept names, notes and speaker
// names that fit each conversation's words, changing nothing on disk.
func applyKeptWatchConversationExtras(dir string, convs []WatchConversation) {
	watchConversationNamesMu.Lock()
	names := readWatchConversationNames(dir)
	watchConversationNamesMu.Unlock()
	applyWatchConversationNames(convs, names)
	for i := range convs {
		convs[i].Decisions, convs[i].Followups, convs[i].ItemIDs = []string{}, []WatchConversationFollowup{}, []string{}
		convs[i].SpeakerNames = map[string]string{}
	}
	watchConversationNotesMu.Lock()
	notes := readWatchConversationNotes(dir)
	watchConversationNotesMu.Unlock()
	applyWatchConversationNotes(convs, notes)
	watchSpeakerNamesMu.Lock()
	speakers := readWatchSpeakerNames(dir)
	watchSpeakerNamesMu.Unlock()
	for i := range convs {
		entry, ok := speakers[convs[i].ID]
		if !ok || entry.Hash != watchConversationHash(convs[i]) {
			continue
		}
		for speaker, name := range entry.Names {
			if containsString(convs[i].Speakers, speaker) {
				convs[i].SpeakerNames[speaker] = name
			}
		}
	}
}

// watchRowForgetPlan is what Forget on a watch row takes, resolved from the
// store at the moment it is asked.
type watchRowForgetPlan struct {
	title string
	goes  []ActivityForgetItem
	// recordingIDs are the hours deleted (later parts go with each).
	recordingIDs []string
	loopIDs      []string
	claimIDs     []string
}

// watchConversationIDStart reads when a conversation began from its id
// ("<device>:<unix ms>").
func watchConversationIDStart(id string) (int64, bool) {
	cut := strings.LastIndex(id, ":")
	if cut <= 0 {
		return 0, false
	}
	at, err := strconv.ParseInt(id[cut+1:], 10, 64)
	return at, err == nil
}

// watchForgetPlanFor resolves a watch row's id, a conversation's or an
// hour's, to everything its Forget removes.
func (a *App) watchForgetPlanFor(id string) (watchRowForgetPlan, error) {
	plan := watchRowForgetPlan{goes: []ActivityForgetItem{}}
	store := a.watchRecordingStore()
	if store == nil {
		return plan, errWatchRecordingNotFound
	}
	recordings, err := store.all()
	if err != nil {
		return plan, err
	}
	byID := map[string]mobileRecording{}
	for _, rec := range recordings {
		byID[rec.ID] = rec
	}
	primary := ""
	var hours []string
	if mobileValidUUID(id) {
		rec, ok := byID[id]
		if !ok {
			return plan, errWatchRecordingNotFound
		}
		at, _ := watchRecordingStartMs(rec)
		hours = []string{id}
		near, err := a.watchConversationsNear(store, recordings, at, at+1)
		if err != nil {
			return plan, err
		}
		for _, c := range near {
			if containsString(c.RecordingIDs, id) {
				return plan, errWatchConversationGone // it holds a conversation now: forget that row
			}
		}
	} else {
		at, ok := watchConversationIDStart(id)
		if !ok {
			return plan, errWatchConversationGone
		}
		near, err := a.watchConversationsNear(store, recordings, at, at+1)
		if err != nil {
			return plan, err
		}
		for _, c := range near {
			if c.ID == id {
				primary, hours = c.ID, append([]string{}, c.RecordingIDs...)
				plan.title = c.Name
			}
		}
		if primary == "" {
			return plan, errWatchConversationGone
		}
	}
	// Later parts of an hour are the same hour to the person.
	taken := map[string]bool{}
	for _, h := range hours {
		taken[h] = true
		for _, more := range store.continuationIDs(h) {
			taken[more] = true
		}
	}
	plan.recordingIDs = hours

	// Every conversation drawing on those hours goes, whole or in part.
	from, to := int64(1<<62), int64(0)
	for rid := range taken {
		if at, ok := watchRecordingStartMs(byID[rid]); ok {
			from, to = min(from, at), max(to, at+1)
		}
	}
	var affected []WatchConversation
	if to > 0 {
		near, err := a.watchConversationsNear(store, recordings, from, to)
		if err != nil {
			return plan, err
		}
		for _, c := range near {
			for _, rid := range c.RecordingIDs {
				if taken[rid] {
					affected = append(affected, c)
					break
				}
			}
		}
	}
	sort.SliceStable(affected, func(i, j int) bool { return affected[i].StartAt < affected[j].StartAt })

	words, audio := false, false
	sessions := map[string]bool{}
	for rid := range taken {
		rec, ok := byID[rid]
		if !ok {
			continue
		}
		words = words || rec.Words > 0
		audio = audio || !rec.AudioRemoved
		sessions["rec-"+rid] = true
		if rec.SessionID != "" {
			sessions[rec.SessionID] = true
		}
	}
	if words {
		plan.goes = append(plan.goes, ActivityForgetItem{Kind: "words"})
	}
	if audio {
		plan.goes = append(plan.goes, ActivityForgetItem{Kind: "audio"})
	}
	var itemIDs []string
	for _, c := range affected {
		hasNote := strings.TrimSpace(c.Summary) != "" || len(c.Decisions) > 0 || len(c.Followups) > 0
		switch {
		case c.ID == primary && hasNote:
			plan.goes = append(plan.goes, ActivityForgetItem{Kind: "notes"})
		case c.ID != primary:
			plan.goes = append(plan.goes, ActivityForgetItem{Kind: "conversation", Title: c.Name, StartMs: c.StartAt, EndMs: c.EndAt})
		}
		itemIDs = append(itemIDs, c.ItemIDs...)
	}
	plan.goes = append(plan.goes, a.watchForgetFollowups(itemIDs, &plan)...)
	plan.goes = append(plan.goes, a.watchForgetClaims(sessions, &plan)...)
	plan.goes = append(plan.goes, a.watchForgetAgentWork(itemIDs)...)
	plan.goes = append(plan.goes, ActivityForgetItem{Kind: "copies"})
	return plan, nil
}

// watchForgetFollowups names each follow-up made from the notes, by its
// commitment's title, where it lives in Your work.
func (a *App) watchForgetFollowups(itemIDs []string, plan *watchRowForgetPlan) []ActivityForgetItem {
	out := []ActivityForgetItem{}
	if len(itemIDs) == 0 {
		return out
	}
	purpose, err := a.purposeProductStore()
	for _, item := range itemIDs {
		loopID := strings.TrimPrefix(item, watchFollowupItemPrefix)
		if loopID == "" || containsString(plan.loopIDs, loopID) {
			continue
		}
		plan.loopIDs = append(plan.loopIDs, loopID)
		title := ""
		if err == nil && purpose != nil {
			if detail, err := purpose.GetDetail(loopID); err == nil {
				title = purposeCurrentLedgerEvent(detail).Title
			}
		}
		out = append(out, ActivityForgetItem{Kind: "followup", Title: title})
	}
	return out
}

// watchForgetClaims names the things Isoform thinks about you that were read
// from those hours' words: the memories that cite their transcripts, which
// About you is built from.
func (a *App) watchForgetClaims(sessions map[string]bool, plan *watchRowForgetPlan) []ActivityForgetItem {
	out := []ActivityForgetItem{}
	entries, err := a.GetMemoryEntries()
	if err != nil {
		log.Printf("[activity] memories not read for a watch Forget: %s", activityPrivateLogField("error", err.Error()))
		return out
	}
	for _, entry := range entries {
		for _, sid := range entry.ActivitySessionIDs {
			if sessions[sid] {
				plan.claimIDs = append(plan.claimIDs, entry.ID)
				out = append(out, ActivityForgetItem{Kind: "claim", Title: strings.TrimSpace(entry.Title)})
				break
			}
		}
	}
	return out
}

// watchForgetAgentWork names work already sent to an agent from those
// follow-ups: it stays with the agent and is not counted (15e rule 3).
func (a *App) watchForgetAgentWork(itemIDs []string) []ActivityForgetItem {
	out := []ActivityForgetItem{}
	if len(itemIDs) == 0 {
		return out
	}
	handoffs, err := a.GetWorkOnItHandoffs()
	if err != nil {
		return out
	}
	for _, h := range handoffs {
		if h.SourceKind != workonit.SourceWorkItem || !containsString(itemIDs, h.SourceID) {
			continue
		}
		name := h.AgentName
		if agent, ok := workonit.AgentByID(h.AgentID); ok && name == "" {
			name = agent.Name
		}
		if name == "" {
			name = h.AgentID
		}
		out = append(out, ActivityForgetItem{Kind: "agent_work", Title: name})
	}
	return out
}

// forgetWatchClaim forgets one claim memory. A claim already gone is done,
// so a retry after a partial failure completes. A seam for tests.
var forgetWatchClaim = func(a *App, id string) error {
	preview, err := a.GetMemoryForgetPreview(id)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := a.ForgetMemoryEntry(id, preview.ConfirmationToken); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// forgetWatchRow removes a watch row and all its plan names. The plan is
// read again now: a conversation whose words changed since the preview is
// refused, never guessed at. What was made from the words goes first (the
// claims, then the follow-ups) and the hours last, so a failure part way
// leaves the conversation in place to Forget again; a claim never outlives
// the words it came from with no row left to reach it by. The plan only
// names claims still kept, so a retry finishes the rest.
func (a *App) forgetWatchRow(id string) error {
	plan, err := a.watchForgetPlanFor(id)
	if err != nil {
		return err
	}
	for _, claim := range plan.claimIDs {
		if err := forgetWatchClaim(a, claim); err != nil {
			return err
		}
	}
	if len(plan.loopIDs) > 0 {
		if _, err := a.purgePurposeOpenLoops(plan.loopIDs); err != nil {
			a.dismissWatchConversationCommitments(plan.loopIDs)
			return err
		}
	}
	for _, rid := range plan.recordingIDs {
		if err := a.DeleteWatchRecording(rid); err != nil && !errors.Is(err, errWatchRecordingNotFound) {
			return err
		}
	}
	if a.ctx != nil {
		a.emitBrowserEvent("activity:activities-updated")
	}
	return nil
}
