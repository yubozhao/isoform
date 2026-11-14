package main

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	briefcore "wails-gui/internal/brief"
	"wails-gui/internal/pii"
)

type AboutYouBelief struct {
	Area             string   `json:"area,omitempty"`
	TopicIDs         []string `json:"topicIds,omitempty"`
	ID               string   `json:"id"`
	Title            string   `json:"title"`
	Status           string   `json:"status"`
	Confidence       int      `json:"confidence"`
	FirstLearnedAt   int64    `json:"firstLearnedAt"`
	UpdatedAt        int64    `json:"updatedAt"`
	SessionCount     int      `json:"sessionCount"`
	DayCount         int      `json:"dayCount"`
	SentMessageCount int      `json:"sentMessageCount,omitempty"`
	EvidenceFirstAt  int64    `json:"evidenceFirstAt,omitempty"`
	EvidenceLastAt   int64    `json:"evidenceLastAt,omitempty"`
	ConfirmedAt      int64    `json:"confirmedAt"`
	// NotQuiteAt is when the person said Not quite; Isoform isn't using it.
	NotQuiteAt int64 `json:"notQuiteAt"`
	// PortraitCandidate is one of the portrait's confirm rows: confirming it
	// lets the portrait say more.
	PortraitCandidate bool `json:"portraitCandidate"`
	// Source is the memory entry's source ("assistant-history", "activity",
	// "dream-synthesis", "user_edit", ...). SourceAssistant and SourceAt are set
	// for beliefs read from an assistant's history: "claude" or "codex", and the
	// session's time in unix ms. Otherwise they are empty and zero.
	Source          string `json:"source"`
	SourceAssistant string `json:"sourceAssistant"`
	SourceAt        int64  `json:"sourceAt"`
	// AgentPractice marks a belief about how the person runs their coding
	// agents (hubs, PRs, workers). Set only while the agent section is on.
	AgentPractice bool `json:"agentPractice,omitempty"`
}

type AboutYouTotals struct {
	Beliefs   int `json:"beliefs"`
	Confirmed int `json:"confirmed"`
}

type AboutYouStorage struct {
	Files       int   `json:"files"`
	Bytes       int64 `json:"bytes"`
	Chunks      int   `json:"chunks"`
	VectorReady bool  `json:"vectorReady"`
}

type AboutYouView struct {
	Beliefs     []AboutYouBelief `json:"beliefs"`
	NewThisWeek []string         `json:"newThisWeek"`
	Totals      AboutYouTotals   `json:"totals"`
	Storage     AboutYouStorage  `json:"storage"`
	// AgentSection groups agent-practice beliefs under one collapsed
	// section and caps them among the top beliefs (flag, default off).
	AgentSection bool `json:"agentSection,omitempty"`
}

// GetAboutYou projects the existing owner-visible memory list into belief metadata.
func (a *App) GetAboutYou() (perfValue AboutYouView, perfErr error) {
	defer logPageBinding("GetAboutYou", time.Now(), &perfValue)
	return derivedRead(a, derivedAboutYou, "", a.getAboutYouNow, derivedPortraitStamp)
}

func (a *App) getAboutYouNow() (AboutYouView, error) {
	all, err := a.GetMemoryEntries()
	if err != nil {
		return AboutYouView{}, err
	}
	// Y42: an entry whose text came from a prompt or template is hidden here;
	// the file stays on disk.
	entries := make([]MemoryEntry, 0, len(all))
	for _, entry := range all {
		if !memoryEntryIsPromptText(pii.DisplayText(extractTitle(entry.Content)), entry.Content) {
			entries = append(entries, entry)
		}
	}
	dir, err := memoryDir()
	if err != nil {
		return AboutYouView{}, err
	}
	out := AboutYouView{Beliefs: []AboutYouBelief{}, NewThisWeek: []string{}}
	now := time.Now()
	today := localBeliefDay(now)
	weekStart := today.AddDate(0, 0, -6).UnixMilli()
	nextDay := today.AddDate(0, 0, 1).UnixMilli()
	var newBeliefs []AboutYouBelief
	candidates := a.portraitService().candidateIDs()
	out.AgentSection = a.aboutYouAgentSectionEnabled()
	for _, entry := range entries {
		belief, err := a.aboutYouBelief(dir, entry, now)
		if err != nil {
			return AboutYouView{}, err
		}
		belief.PortraitCandidate = candidates[belief.ID]
		belief.AgentPractice = out.AgentSection && beliefNamesAgentPractice(belief.Title)
		out.Beliefs = append(out.Beliefs, belief)
		if belief.Status == "confirmed" {
			out.Totals.Confirmed++
		}
		// An answered belief lives in its group, not in New this week.
		if belief.Status != "confirmed" && belief.Status != "not_quite" && belief.FirstLearnedAt >= weekStart && belief.FirstLearnedAt < nextDay {
			newBeliefs = append(newBeliefs, belief)
		}
		out.Storage.Bytes += entry.SizeBytes
	}
	sort.Slice(newBeliefs, func(i, j int) bool {
		if newBeliefs[i].FirstLearnedAt == newBeliefs[j].FirstLearnedAt {
			return newBeliefs[i].ID < newBeliefs[j].ID
		}
		return newBeliefs[i].FirstLearnedAt > newBeliefs[j].FirstLearnedAt
	})
	out.NewThisWeek = topNewBeliefIDs(newBeliefs, 5)
	out.Totals.Beliefs, out.Storage.Files = len(entries), len(entries)
	status := a.GetMemoryStatus()
	out.Storage.Chunks, out.Storage.VectorReady = status.Chunks, status.VectorReady
	return out, nil
}

func localBeliefDay(at time.Time) time.Time {
	at = at.In(time.Local)
	return time.Date(at.Year(), at.Month(), at.Day(), 0, 0, 0, 0, at.Location())
}

func (a *App) aboutYouBelief(dir string, entry MemoryEntry, now time.Time) (AboutYouBelief, error) {
	out := AboutYouBelief{ID: entry.ID, Title: pii.DisplayText(extractTitle(entry.Content)), Area: entry.Area, TopicIDs: entry.TopicIDs, ConfirmedAt: memoryConfirmedAt(entry.Content), NotQuiteAt: memoryNotQuiteAt(entry.Content), UpdatedAt: entry.UpdatedAt, FirstLearnedAt: entry.UpdatedAt}
	fm := parseMemoryFrontmatter(entry.Content)
	out.SentMessageCount, out.EvidenceFirstAt, out.EvidenceLastAt = fm.SentMessageCount, fm.EvidenceFirstAt, fm.EvidenceLastAt
	out.Source = entry.Source
	if doc, ok := parseOKFDocument(legacyMemoryWithoutConfirmation(entry.Content)); ok && entry.Source == assistantHistorySource {
		out.SourceAssistant = okfString(doc.Frontmatter, "history_assistant")
		out.SourceAt = parseOKFTimestampMs(okfString(doc.Frontmatter, "history_session_at"))
	}
	if out.UpdatedAt == 0 {
		out.UpdatedAt = entry.ModTime
	}
	// The timestamp describes the live revision, while the oldest snapshot
	// preserves an earlier revision. Take the earliest frontmatter timestamp
	// from either, also considering snapshot filenames as an upper bound.
	// A filesystem mtime is only a last resort for undated legacy files.
	history, err := os.ReadDir(filepath.Join(dir, memoryHistoryDirName, entry.ID))
	if err != nil && !os.IsNotExist(err) {
		return out, err
	}
	for _, file := range history {
		if file.IsDir() || !strings.HasSuffix(file.Name(), ".md") {
			continue
		}
		snapshotAt, _ := strconv.ParseInt(strings.TrimSuffix(file.Name(), ".md"), 10, 64)
		data, err := os.ReadFile(filepath.Join(dir, memoryHistoryDirName, entry.ID, file.Name()))
		if err != nil {
			return out, err
		}
		for _, at := range []int64{snapshotAt, parseMemoryFrontmatter(string(data)).UpdatedAt} {
			if at > 0 && (out.FirstLearnedAt == 0 || at < out.FirstLearnedAt) {
				out.FirstLearnedAt = at
			}
		}
	}
	if out.FirstLearnedAt == 0 {
		out.FirstLearnedAt = entry.ModTime
	}
	// Denied source sessions contribute no derived counts or confidence.
	if a.memorySourceAllowsRead(entry.Content) && a.activity != nil && a.activity.store != nil {
		days := map[string]bool{}
		for _, id := range memoryEvidenceSessionIDs(entry.Content) {
			session, err := a.activity.store.GetSessionByID(id)
			if err != nil {
				return out, err
			}
			if session == nil || !a.activitySessionAllowsModelRead(a.activity.store, *session) {
				continue
			}
			out.SessionCount++
			days[time.UnixMilli(session.StartTime).In(time.Local).Format("2006-01-02")] = true
		}
		out.DayCount = len(days)
	}
	out.Status, out.Confidence = beliefStatusConfidence(out, entry.Source, now)
	return out, nil
}

func beliefStatusConfidence(belief AboutYouBelief, source string, now time.Time) (string, int) {
	status := "inferred"
	switch true {
	case belief.NotQuiteAt != 0:
		// Not quite outranks an earlier user edit: the person's latest answer.
		status = "not_quite"
	case belief.ConfirmedAt != 0 || source == "user_edit":
		return "confirmed", 4
	case localBeliefDay(time.UnixMilli(belief.FirstLearnedAt)).Equal(localBeliefDay(now)):
		status = "learned_today"
	}
	switch true {
	case belief.SessionCount >= 4 || belief.DayCount >= 2:
		return status, 3
	case belief.SessionCount >= 2:
		return status, 2
	default:
		return status, 1
	}
}

// aboutYouAgentSectionEnabledKey turns on the agent-practice section. It is
// off until Bo decides whether beliefs about how he runs his agents belong in
// About you (2026-11-13).
const aboutYouAgentSectionEnabledKey = "about_you_agent_section_enabled"

// aboutYouAgentPracticeTopCap is how many agent-practice beliefs may sit
// among the top beliefs (New this week) while the section is on.
const aboutYouAgentPracticeTopCap = 1

func (a *App) aboutYouAgentSectionEnabled() bool {
	if a == nil || a.store == nil {
		return false
	}
	value, err := a.store.GetUIState(context.Background(), aboutYouAgentSectionEnabledKey)
	return err == nil && strings.EqualFold(strings.TrimSpace(value), "true")
}

// beliefNamesAgentPractice reports a belief that names agent work, by the
// brief's agent words. "Agent of You" is a product, not agent work.
func beliefNamesAgentPractice(title string) bool {
	lower := strings.ReplaceAll(strings.ToLower(title), "agent of you", "")
	return briefcore.AgentCue(lower)
}

// topNewBeliefIDs takes the newest beliefs in order, letting at most
// aboutYouAgentPracticeTopCap agent-practice beliefs in.
func topNewBeliefIDs(beliefs []AboutYouBelief, limit int) []string {
	out := make([]string, 0, limit)
	agents := 0
	for _, belief := range beliefs {
		if len(out) == limit {
			break
		}
		if belief.AgentPractice {
			if agents == aboutYouAgentPracticeTopCap {
				continue
			}
			agents++
		}
		out = append(out, belief.ID)
	}
	return out
}
