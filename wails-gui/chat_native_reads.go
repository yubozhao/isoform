package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"wails-gui/internal/meetingdetect"
)

// chatNativeReadMaxLimit caps how many rows one list or search read asks for.
const chatNativeReadMaxLimit = 100

const chatNativeReadHint = "Use the native Isoform tools first: ActivityDay for a local date, ActivityList for recent sessions, ActivitySearch to find sessions, and ActivitySession for details or a meeting transcript. Use MemorySearch for recall, MemoryList to browse notes, and MemoryShow to read one. Cite the refs the tools print.\n\n"

// A meeting permits its own calling app. Other messaging apps and webmail
// remain outside Chat, including on mixed-app calls (phone-chat rule 4).
func chatMeetingIdentity(meeting, app string) bool {
	return meetingdetect.IsTranscriptionSupportedMeetingName(meeting) &&
		(strings.EqualFold(app, meeting) || meetingdetect.TranscriptionSupportedNativeMeetingForBundle(app).MeetingName == meeting || meeting == "Zoom" && strings.EqualFold(app, "zoom.us"))
}

func (p *mcpReadPrivacy) chatSourceAllowed(source activitySourceProvenance, meeting string) bool {
	if !p.chatAppsAllowed(meeting, source.Apps...) {
		return false
	}
	if source.MCPSnapshots != nil {
		for _, snap := range *source.MCPSnapshots {
			if snap.Private || !p.chatAppsAllowed(meeting, snap.Apps...) {
				return false
			}
			for _, host := range snap.Hosts {
				if topicPeopleHost("https://"+host) || !p.appAllowed(host) {
					return false
				}
			}
		}
	}
	return !source.Private
}

func (p *mcpReadPrivacy) chatSessionAllowed(row ActivitySessionRow) (bool, error) {
	source, err := p.app.activity.store.activitySessionProvenanceContext(p.ctx, row)
	if err != nil {
		return false, err
	}
	meeting := ""
	if row.IsMeeting {
		meeting = row.MeetingApp
	}
	return p.chatSourceAllowed(source, meeting), nil
}

func (p *mcpReadPrivacy) chatMemoryAllowed(e MemoryEntry) (bool, error) {
	// A list snapshot can predate an external edit or restore. Authorize only
	// the current regular note whose bytes supplied the title (phone-chat rule 4).
	if e.ID != "" {
		dir, err := memoryDir()
		if err != nil {
			return false, err
		}
		path := filepath.Join(dir, e.ID+".md")
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if !info.Mode().IsRegular() {
			return false, nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return false, err
		}
		if string(body) != e.Content {
			return false, errors.New("memory changed")
		}
	}
	deps := memoryReadDependenciesOf(e.Content)
	if !p.appAllowed(e.Apps...) || !p.appAllowed(deps.apps...) || topicPeopleApp(e.Source, e.Source) {
		return false, nil
	}
	if len(deps.sessions)+len(e.ActivitySessionIDs) > 0 && (p.app.activity == nil || p.app.activity.store == nil) {
		return false, nil
	}
	for _, id := range compactStrings(append(deps.sessions, e.ActivitySessionIDs...)) {
		row, err := p.app.activity.store.GetSessionByID(id)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if ok, err := p.chatSessionAllowed(*row); !ok || err != nil {
			return false, err
		}
	}
	return true, nil
}

// Chat reads only an existing journal. The CLI alone can request generation
// (computer-chat v1.2.0 rule 6). Check the narrative's own sources, including
// those outside the day or beyond the session list limit.
func (p *mcpReadPrivacy) dayJournal(date string) (string, error) {
	a := p.app
	snap, err := a.activity.store.readActivitySummarySnapshot(date, "daily", a.currentUILanguage())
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if !a.activitySummarySnapshotAllowsRead(date, "daily", snap) {
		return "", nil
	}
	var sources []activitySourceProvenance
	if json.Unmarshal([]byte(snap.Provenance), &sources) != nil || len(sources) == 0 {
		return "", nil
	}
	for _, src := range sources {
		meeting := ""
		if src.SessionID != "" {
			row, err := a.activity.store.GetSessionByID(src.SessionID)
			if errors.Is(err, sql.ErrNoRows) {
				return "", nil
			}
			if err != nil {
				return "", err
			}
			ok, err := p.chatSessionAllowed(*row)
			if err != nil || !ok {
				return "", err
			}
			if row.IsMeeting {
				meeting = row.MeetingApp
			}
		}
		if !p.chatSourceAllowed(src, meeting) {
			return "", nil
		}
	}
	data := DailySummaryData{AISummary: snap.Summary}
	a.secondPersonDaily(&data)
	return externalJournal(&data).Journal, nil
}

func (a *App) readNativeChatSource(ctx context.Context, name string, input map[string]any, capture *contextUseCapture) (string, error) {
	policy, err := a.mcpReadPrivacy(ctx)
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(name, "Activity") && (a.activity == nil || a.activity.store == nil) {
		return "", errors.New("activity unavailable")
	}
	limit := 20
	switch name {
	case "ActivitySearch":
		limit = 10
	case "MemoryList":
		limit = chatNativeReadMaxLimit
	}
	if v, ok := input["limit"]; ok && (name == "ActivitySearch" || name == "ActivityList" || name == "MemoryList") {
		limit, err = strconv.Atoi(fmt.Sprint(v))
		if err != nil || limit < 1 {
			return "", errors.New("invalid limit")
		}
		// The output is bounded; the read is bounded too.
		limit = min(limit, chatNativeReadMaxLimit)
	}
	var out strings.Builder
	epoch := a.contextUseEpoch()
	write := func(kind, id string, data any) error {
		raw, err := json.Marshal(data)
		if err != nil {
			return err
		}
		fmt.Fprintf(&out, "ref=%s:%s %s\n", kind, id, raw)
		capture.entries = append(capture.entries, contextUseEntry{kind, id, epoch})
		return nil
	}
	id, _ := input["id"].(string)
	switch name {
	case "MemoryList":
		dimension, _ := input["dimension"].(string)
		rows, err := a.externalMemoryList(dimension, limit, policy.chatMemoryAllowed)
		if err != nil {
			return "", err
		}
		for _, row := range rows {
			if err := write("memory", row.ID, row); err != nil {
				return "", err
			}
		}
	case "MemoryShow":
		row, err := a.filteredExternalMemory(id, policy.chatMemoryAllowed)
		if err != nil {
			return "", err
		}
		if err := write("memory", row.ID, row); err != nil {
			return "", err
		}
	case "ActivitySession":
		detail, err := a.activitySessionForExternalRead(id, policy.chatSessionAllowed)
		if err != nil {
			return "", err
		}
		if err := write("activity", id, detail); err != nil {
			return "", err
		}
	case "ActivityDay", "ActivityList":
		var start, end int64
		if name == "ActivityDay" {
			date, _ := input["date"].(string)
			start, end, err = activityDayBounds(date)
			if err != nil {
				return "", errors.New("invalid date")
			}
			journal, err := policy.dayJournal(date)
			if err != nil {
				return "", err
			}
			if journal == "" {
				journal = "No readable daily narrative is available."
			}
			fmt.Fprintln(&out, journal)
		}
		rows, err := a.externalActivityList(ctx, limit, 0, start, end, "", policy.chatSessionAllowed)
		if err != nil {
			return "", err
		}
		for _, row := range rows {
			fmt.Fprintf(&out, "%s to %s ", time.UnixMilli(row.StartTime).Format(time.RFC3339), time.UnixMilli(row.EndTime).Format(time.RFC3339))
			if err := write("activity", row.ID, row); err != nil {
				return "", err
			}
		}
	case "ActivitySearch":
		query, _ := input["query"].(string)
		times := map[string]int64{}
		hits, err := a.externalActivitySearch(ctx, query, limit, func(row ActivitySessionRow) (bool, error) {
			times[row.ID] = row.StartTime
			return policy.chatSessionAllowed(row)
		})
		if err != nil {
			return "", err
		}
		sort.SliceStable(hits, func(i, j int) bool { return times[hits[i].SessionID] > times[hits[j].SessionID] })
		for _, hit := range hits {
			if err := write("activity", hit.SessionID, hit); err != nil {
				return "", err
			}
		}
	default:
		return "", errors.New("unknown reader")
	}
	return out.String(), ctx.Err()
}

// Keep the head and tail of large reads within the shell's 32 KiB and
// 3000-line display budget, including the marker and complete UTF-8 text.
func boundChatRead(text string) string {
	const marker = "\n... <output truncated> ...\n"
	lines := strings.Split(text, "\n")
	if len(lines) > 3000 {
		text = strings.Join(lines[:1499], "\n") + marker + strings.Join(lines[len(lines)-1499:], "\n")
	}
	const maxBytes = 32 * 1024
	if len(text) <= maxBytes {
		return text
	}
	head := (maxBytes - len(marker)) / 2
	tail := len(text) - head
	for !utf8.RuneStart(text[head]) {
		head--
	}
	for !utf8.RuneStart(text[tail]) {
		tail++
	}
	return text[:head] + marker + text[tail:]
}
