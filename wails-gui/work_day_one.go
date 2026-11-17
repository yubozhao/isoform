package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"sync"
	"time"
)

// Day one (computer-setup v1.0.0 rule 6, computer-today v1.1.0 rule 2;
// PROTOCOL.md 26.5): before the first brief, Your work holds real work made
// from what Isoform has already seen: today's meetings (from your calendar),
// what waits on your answer (from your mail), and what you were doing (from
// what's been seen since setup). It is read through the brief's own gathering,
// with the brief's privacy gates, and offered like the brief's work: Yes hands
// it to your agent, No takes a reason. It shows only during the first day.

const (
	workSourceDayOne = "day_one"
	workDayOneKey    = "work.day_one"
	// workDayOneFor is how long after setup day one lasts.
	workDayOneFor = 24 * time.Hour
	// workDayOneRefresh bounds how often the day's material is read again.
	workDayOneRefresh = 30 * time.Minute
	workDayOnePerKind = 2
	workDayOneMax     = 5

	workDayOneCalendar = "calendar"
	workDayOneMail     = "mail"
	workDayOneSeen     = "seen"
)

// workDayOneCollect gathers the day's material the way a brief does; tests
// replace it. It is set in init: gathering reaches Your work, which reads
// day one, so a package-level value would be an initialization cycle.
var workDayOneCollect func(ctx context.Context, a *App, since, now time.Time) (mobileBriefInput, error)

func init() {
	workDayOneCollect = func(ctx context.Context, a *App, since, now time.Time) (mobileBriefInput, error) {
		return collectMobileBriefInput(ctx, a, mobileBriefSlotOnRequest, since, now)
	}
}

type workDayOneEntry struct {
	ID      string `json:"id"`
	Origin  string `json:"origin"`
	Title   string `json:"title"`
	Noticed string `json:"noticed"`
	Prompt  string `json:"prompt"`
	At      string `json:"at,omitempty"`
}

type workDayOneCache struct {
	GeneratedAt string            `json:"generatedAt"`
	Items       []workDayOneEntry `json:"items"`
}

var workDayOneMu sync.Mutex

// workDayOneActive reports whether today is the person's first day.
func (a *App) workDayOneActive(now time.Time) bool {
	if a == nil || a.store == nil {
		return false
	}
	installed := a.firstWeekInstall(context.Background())
	return !installed.IsZero() && !now.Before(installed) && now.Sub(installed) < workDayOneFor
}

// workDayOneItems are day one's items, read again at most every half hour.
func (a *App) workDayOneItems(ctx context.Context, now time.Time) []WorkItem {
	if !a.workDayOneActive(now) {
		return nil
	}
	entries := a.workDayOneEntries(ctx, now)
	items := make([]WorkItem, 0, len(entries))
	for _, e := range entries {
		item := WorkItem{
			ID: e.ID, Kind: workKindOffered, Row: "offered", State: workStateOpen, Source: workSourceDayOne,
			Ref:     WorkRef{Origin: e.Origin},
			First:   []WorkTextPart{{Text: "Day one · from your " + workDayOneSourceWord(e.Origin), Key: "day_one"}},
			Noticed: e.Noticed, Work: WorkBody{Title: e.Title},
			Yes:     WorkYes{Does: "agent", Line: "Yes starts it with your agent. What it makes comes back here."},
			Reasons: workReasonsOffered, CreatedAt: mobileTimestamp(now),
		}
		if e.At != "" {
			item.First = append(item.First, WorkTextPart{Key: "clock", At: e.At})
		}
		items = append(items, item)
	}
	return items
}

func workDayOneSourceWord(origin string) string {
	switch origin {
	case workDayOneCalendar:
		return "calendar"
	case workDayOneMail:
		return "mail"
	default:
		return "files"
	}
}

// workDayOneEntries reads the cached entries, or gathers them again.
func (a *App) workDayOneEntries(ctx context.Context, now time.Time) []workDayOneEntry {
	workDayOneMu.Lock()
	defer workDayOneMu.Unlock()
	var cache workDayOneCache
	if raw, err := a.store.GetUIState(ctx, workDayOneKey); err == nil && raw != "" {
		_ = json.Unmarshal([]byte(raw), &cache)
	}
	if at, err := time.Parse(time.RFC3339, cache.GeneratedAt); err == nil && now.Sub(at) < workDayOneRefresh {
		return cache.Items
	}
	installed := a.firstWeekInstall(ctx)
	since := installed.Add(-24 * time.Hour)
	in, err := workDayOneCollect(ctx, a, since, now)
	if err != nil {
		return cache.Items
	}
	cache = workDayOneCache{GeneratedAt: mobileTimestamp(now), Items: workDayOneFrom(in, now)}
	if raw, err := json.Marshal(cache); err == nil {
		_ = a.store.SetUIState(ctx, workDayOneKey, string(raw))
	}
	return cache.Items
}

// workDayOneFrom makes the entries from the gathered material: at most two of
// each kind, five in all, only from sources the brief could show.
func workDayOneFrom(in mobileBriefInput, now time.Time) []workDayOneEntry {
	out := []workDayOneEntry{}
	add := func(origin string, list []mobileBriefCandidate, make func(mobileBriefCandidate) (workDayOneEntry, bool)) {
		n := 0
		for _, c := range list {
			if n >= workDayOnePerKind || len(out) >= workDayOneMax {
				return
			}
			if !workDayOneUsable(c) {
				continue
			}
			entry, ok := make(c)
			if !ok {
				continue
			}
			entry.Origin = origin
			entry.ID = "day_one:" + origin + ":" + workDayOneHash(c.SourceKind, c.SourceID)
			out = append(out, entry)
			n++
		}
	}
	add(workDayOneCalendar, in.Meetings, func(c mobileBriefCandidate) (workDayOneEntry, bool) {
		if c.At.IsZero() || !c.At.After(now) {
			return workDayOneEntry{}, false
		}
		title := mobileBriefText(c.Title, 120)
		with := ""
		if c.Person != "" {
			with = " with " + mobileBriefText(c.Person, 80)
		}
		return workDayOneEntry{
			Title: "Prepare for " + title, Noticed: title + with, At: mobileTimestamp(c.At),
			Prompt: "Prepare short notes for the meeting \"" + title + "\"" + with + ": what it's about, what was said before, and what to ask. Use only what's on this computer.",
		}, true
	})
	add(workDayOneMail, in.WaitingOnYou, func(c mobileBriefCandidate) (workDayOneEntry, bool) {
		title := mobileBriefText(c.Title, 120)
		who := mobileBriefText(c.Person, 80)
		head := "Reply about " + title
		if who != "" {
			head = "Reply to " + who
		}
		return workDayOneEntry{
			Title: head, Noticed: strings.TrimSpace(title + " " + mobileBriefText(c.Detail, 200)),
			Prompt: "Draft a reply about \"" + title + "\"" + workDayOneTo(who) + ". Don't send it; leave it as a draft for me to read.",
		}, true
	})
	add(workDayOneSeen, append(append([]mobileBriefCandidate{}, in.Moved...), in.Noticed...), func(c mobileBriefCandidate) (workDayOneEntry, bool) {
		title := mobileBriefText(c.Title, 120)
		return workDayOneEntry{
			Title: "Pick up " + title, Noticed: mobileBriefText(c.Detail, 200),
			Prompt: "Pick up where I left off on \"" + title + "\": say what's done, what's next, and do the next step if it's small.",
		}, true
	})
	return out
}

func workDayOneTo(who string) string {
	if who == "" {
		return ""
	}
	return " to " + who
}

// workDayOneUsable keeps the sources the brief itself could show.
func workDayOneUsable(c mobileBriefCandidate) bool {
	if c.EvidenceUnavailable || c.ChatMessageID != "" || strings.TrimSpace(c.SourceID) == "" || strings.TrimSpace(c.Title) == "" {
		return false
	}
	switch c.SourceKind {
	case mobileBriefWebSource, mobileBriefFileSource, "chat_message", "brief", "brief_item":
		return false
	}
	label := mobileBriefText(c.Title, mobileBriefLabelLimit)
	return mobileBriefCopySafe(label) && redactThreadOutcomeText(label) == label
}

func workDayOneHash(kind, id string) string {
	sum := sha256.Sum256([]byte(kind + "\x00" + id))
	return hex.EncodeToString(sum[:8])
}

// workDayOnePrompt is the task a Yes on a day-one item hands over.
func (a *App) workDayOnePrompt(ctx context.Context, id string) (string, bool) {
	for _, e := range a.workDayOneEntries(ctx, time.Now()) {
		if e.ID == id {
			return e.Prompt, true
		}
	}
	return "", false
}
