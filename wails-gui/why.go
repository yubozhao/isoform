package main

import (
	"context"
	"errors"
	"log"
	"sort"
	"strings"
	"time"
)

// Why (redesign v1.0.0 rule 16): the moments a claim or a piece of work came
// from, by day and source, each with Not this. One read for every page that
// shows a Why: About you first, then Find, Chat and Today. A claim's moments
// come through GetMemoryEvidenceReadable's gates (moment titles only, never
// captured text; Closed Eyes, pauses and protected apps hide them), so this
// read never sees more than About you's detail pane did.

const (
	whyKindClaim = "claim"
	whyKindWork  = "work"

	whySourceScreen   = "screen"
	whySourceMail     = "mail"
	whySourceCalendar = "calendar"
	whySourceCall     = "call"
	whySourceWatch    = "watch"
	whySourceMessages = "messages"
	whySourceAgent    = "agent"
	whySourceChat     = "chat"

	// answerActionNotThis is Not this on one moment of a claim's Why. It is
	// kept under the claim's own moment item, never as the claim's answer.
	answerActionNotThis = "not_this"

	whyMomentsMax = 20

	// memoryImportSourcePrefix marks a claim brought in from another AI:
	// source "import:<label>".
	memoryImportSourcePrefix = "import:"
)

var (
	errWhyKind           = errors.New("why_kind_unknown")
	errWhyNotFound       = errors.New("why_not_found")
	errWhyMomentNotFound = errors.New("why_moment_not_found")
	// errWhyWorkNotYet: Not this on a piece of work is Today's to wire.
	errWhyWorkNotYet = errors.New("why_not_this_work_not_yet")
)

// WhyMoment is one moment a claim or work came from. Title is a moment title
// only, never captured text.
type WhyMoment struct {
	ID     string `json:"id"`
	At     int64  `json:"at"`
	Source string `json:"source"`
	App    string `json:"app,omitempty"`
	Title  string `json:"title,omitempty"`
	// ActivitySessionID opens the moment in Activity; FeedRowID opens a feed
	// row. At most one is set.
	ActivitySessionID string `json:"activitySessionId,omitempty"`
	FeedRowID         string `json:"feedRowId,omitempty"`
	CanNotThis        bool   `json:"canNotThis"`
}

// WhyDay is one local day's moments, newest first.
type WhyDay struct {
	Day     string      `json:"day"`
	Moments []WhyMoment `json:"moments"`
}

// WhySummary is what the source line needs ("From 6 emails and a call this
// week"): counts, never words. The frontend writes the words.
type WhySummary struct {
	Total    int            `json:"total"`
	BySource map[string]int `json:"bySource"`
	// ThisWeek counts the moments in the last seven local days.
	ThisWeek int `json:"thisWeek"`
	// ImportedFrom is set on a claim brought in from another AI ("ChatGPT").
	ImportedFrom string `json:"importedFrom,omitempty"`
}

// WhyView is a claim's or work's Why.
type WhyView struct {
	Kind    string     `json:"kind"`
	ID      string     `json:"id"`
	Summary WhySummary `json:"summary"`
	Days    []WhyDay   `json:"days"`
}

// GetWhy reads the moments a claim (a memory id) or a piece of work (a Your
// work id) came from.
func (a *App) GetWhy(kind, id string) (WhyView, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return WhyView{}, errWhyNotFound
	}
	switch kind {
	case whyKindClaim:
		moments, imported, err := a.whyClaimMoments(id)
		if err != nil {
			return WhyView{}, err
		}
		view := whyView(kind, id, moments, time.Now())
		view.Summary.ImportedFrom = imported
		return view, nil
	case whyKindWork:
		moments, err := a.whyWorkMoments(id)
		if err != nil {
			return WhyView{}, err
		}
		return whyView(kind, id, moments, time.Now()), nil
	default:
		return WhyView{}, errWhyKind
	}
}

func whyView(kind, id string, moments []WhyMoment, now time.Time) WhyView {
	view := WhyView{Kind: kind, ID: id, Summary: WhySummary{BySource: map[string]int{}}, Days: []WhyDay{}}
	sort.SliceStable(moments, func(i, j int) bool {
		if moments[i].At == moments[j].At {
			return moments[i].ID < moments[j].ID
		}
		return moments[i].At > moments[j].At
	})
	if len(moments) > whyMomentsMax {
		moments = moments[:whyMomentsMax]
	}
	weekStart := localBeliefDay(now).AddDate(0, 0, -6).UnixMilli()
	for _, moment := range moments {
		view.Summary.Total++
		view.Summary.BySource[moment.Source]++
		if moment.At >= weekStart {
			view.Summary.ThisWeek++
		}
		day := ""
		if moment.At > 0 {
			day = time.UnixMilli(moment.At).In(time.Local).Format("2006-01-02")
		}
		if n := len(view.Days); n > 0 && view.Days[n-1].Day == day {
			view.Days[n-1].Moments = append(view.Days[n-1].Moments, moment)
			continue
		}
		view.Days = append(view.Days, WhyDay{Day: day, Moments: []WhyMoment{moment}})
	}
	return view
}

// whyClaimMoments builds on GetMemoryEvidenceReadable, then adds the sessions
// with a coding assistant a belief from its history was seen in (times only,
// no titles), under the same memory gates.
func (a *App) whyClaimMoments(id string) ([]WhyMoment, string, error) {
	rows, err := a.GetMemoryEvidenceReadable(id)
	if err != nil {
		return nil, "", err
	}
	moments := make([]WhyMoment, 0, len(rows))
	for _, row := range rows {
		moments = append(moments, WhyMoment{ID: row.ActivitySessionID, At: row.At, Source: whySourceScreen, App: row.AppName, Title: row.Text,
			ActivitySessionID: row.ActivitySessionID, CanNotThis: true})
	}
	entry, err := a.GetMemoryEntry(id)
	if err != nil {
		return nil, "", err
	}
	imported := importedMemoryLabel(entry.Source)
	if entry.Source != assistantHistorySource {
		return moments, imported, nil
	}
	allow, err := a.memoryReadFilter()
	if err != nil {
		return nil, "", err
	}
	if !allow(id, entry.Content) || !a.memorySourceAllowsRead(entry.Content) {
		return moments, imported, nil
	}
	if _, err := a.mcpReadPrivacy(context.Background()); errors.Is(err, errMCPReadPaused) {
		return moments, imported, nil
	} else if err != nil {
		return nil, "", err
	}
	doc, ok := parseOKFDocument(legacyMemoryWithoutConfirmation(entry.Content))
	if !ok {
		return moments, imported, nil
	}
	app := whyAssistantName(okfString(doc.Frontmatter, "history_assistant"))
	seen := map[int64]bool{}
	for _, stamp := range historyBeliefStamps(doc.Frontmatter) {
		at := parseOKFTimestampMs(stamp)
		if at == 0 || seen[at] {
			continue
		}
		seen[at] = true
		moments = append(moments, WhyMoment{ID: "history:" + stamp, At: at, Source: whySourceAgent, App: app})
	}
	return moments, imported, nil
}

func whyAssistantName(assistant string) string {
	switch strings.ToLower(strings.TrimSpace(assistant)) {
	case "claude":
		return "Claude Code"
	case "codex":
		return "Codex"
	default:
		return ""
	}
}

// whyWorkMoments resolves a Your work item's Why links. A link to a session
// ("session:<id>") goes through the activity read gates; any other link is
// the item's own words, already on screen, so it shows as it is.
func (a *App) whyWorkMoments(id string) ([]WhyMoment, error) {
	list, err := a.GetWorkList()
	if err != nil {
		return nil, err
	}
	var item *WorkItem
	for i := range list.Items {
		if list.Items[i].ID == id {
			item = &list.Items[i]
			break
		}
	}
	if item == nil {
		return nil, errWhyNotFound
	}
	if len(item.Why) == 0 {
		return []WhyMoment{}, nil
	}
	if _, err := a.mcpReadPrivacy(context.Background()); errors.Is(err, errMCPReadPaused) {
		return []WhyMoment{}, nil
	} else if err != nil {
		return nil, err
	}
	moments := []WhyMoment{}
	for index, link := range item.Why {
		kind, ref, _ := strings.Cut(strings.TrimSpace(link.Ref), ":")
		moment := WhyMoment{ID: link.Ref, Source: whyLinkSource(kind), Title: strings.Join(strings.Fields(link.Label), " ")}
		if moment.ID == "" {
			moment.ID = "link:" + string(rune('a'+index%26))
		}
		if kind == "session" {
			if a.activity == nil || a.activity.store == nil {
				continue
			}
			session, err := a.activity.store.GetSessionByID(ref)
			if err != nil {
				return nil, err
			}
			if session == nil || !a.activitySessionAllowsModelRead(a.activity.store, *session) {
				continue
			}
			moment.At, moment.ActivitySessionID = session.StartTime, ref
		}
		if kind == "feed" {
			moment.FeedRowID = ref
		}
		if moment.Title == "" && moment.At == 0 {
			continue
		}
		moments = append(moments, moment)
	}
	return moments, nil
}

func whyLinkSource(kind string) string {
	switch kind {
	case "mail", "email":
		return whySourceMail
	case "calendar", "event", "meeting":
		return whySourceCalendar
	case "call":
		return whySourceCall
	case "watch":
		return whySourceWatch
	case "messages", "message":
		return whySourceMessages
	case "agent":
		return whySourceAgent
	case "chat":
		return whySourceChat
	default:
		return whySourceScreen
	}
}

// NotThis takes one moment off a claim: its evidence link goes and the claim
// stays. A claim left with no moments stays too, with nothing supporting it
// (its evidence list is kept, empty, so older links don't come back). The
// answer is kept in the ledger under the claim's moment.
func (a *App) NotThis(kind, id, momentID string) error {
	switch kind {
	case whyKindWork:
		return errWhyWorkNotYet
	case whyKindClaim:
	default:
		return errWhyKind
	}
	momentID = strings.TrimSpace(momentID)
	if momentID == "" || strings.HasPrefix(momentID, "history:") {
		return errWhyMomentNotFound
	}
	before, err := a.GetMemoryEvidenceReadable(id)
	if err != nil {
		return err
	}
	title := ""
	for _, row := range before {
		if row.ActivitySessionID == momentID {
			title = row.Text
		}
	}
	if title == "" {
		return errWhyMomentNotFound
	}
	// The readable list shows each moment title once. Take every session
	// that shows the same title off too, or Not this would bring it back.
	for range 8 {
		if err := a.removeClaimEvidenceSession(id, momentID); err != nil {
			return err
		}
		rows, err := a.GetMemoryEvidenceReadable(id)
		if err != nil {
			return err
		}
		momentID = ""
		for _, row := range rows {
			if row.Text == title {
				momentID = row.ActivitySessionID
				break
			}
		}
		if momentID == "" {
			break
		}
	}
	a.ledgerNotThis(id, title)
	return nil
}

func (a *App) removeClaimEvidenceSession(id, sessionID string) error {
	memoryMutationMu.RLock()
	defer memoryMutationMu.RUnlock()
	unlock := memoryFileLocks.Acquire(id)
	defer unlock()
	entry, err := a.GetMemoryEntry(id)
	if err != nil {
		return err
	}
	dir, err := memoryDir()
	if err != nil {
		return err
	}
	if err := checkMemoryWriteAllowed(dir, id, entry.Content, false); err != nil {
		return err
	}
	content, changed := memoryWithoutEvidenceSession(entry.Content, sessionID)
	if !changed {
		return nil
	}
	if err := writeMemoryFileAtomically(dir, id, content); err != nil {
		return err
	}
	a.markMemoryDirty("not_this")
	return nil
}

// memoryWithoutEvidenceSession drops one supporting session from a memory
// file. The privacy provenance (activity_sessions in OKF files) stays: it
// says what the belief was read from, not what supports it.
func memoryWithoutEvidenceSession(content, sessionID string) (string, bool) {
	legacy := legacyMemoryWithoutConfirmation(content)
	envelope := content[:len(content)-len(legacy)]
	if doc, ok := parseOKFDocument(legacy); ok {
		current := memoryEvidenceSessionIDs(legacy)
		kept := []string{}
		for _, id := range current {
			if id != sessionID {
				kept = append(kept, id)
			}
		}
		if len(kept) == len(current) {
			return content, false
		}
		doc.Frontmatter["evidence_sessions"] = kept
		return envelope + renderOKFDocument(doc.Frontmatter, doc.Body), true
	}
	lines := strings.SplitAfter(legacy, "\n")
	out := make([]string, 0, len(lines))
	changed, inBlock := false, false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "- **Activity Session**:") {
			value := strings.TrimSpace(strings.TrimPrefix(trimmed, "- **Activity Session**:"))
			inBlock = value == ""
			if value == sessionID {
				changed = true
				continue
			}
			out = append(out, line)
			continue
		}
		if inBlock {
			if trimmed == "" || !(strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t")) {
				inBlock = false
			} else if parseSessionBulletID(line) == sessionID {
				changed = true
				continue
			}
		}
		out = append(out, line)
	}
	if !changed {
		return content, false
	}
	return envelope + strings.Join(out, ""), true
}

// ledgerNotThis keeps Not this in the answers ledger. Its item is the claim's
// moment, so it never stands as the claim's own answer. The ledger never
// blocks the answer.
func (a *App) ledgerNotThis(id, momentTitle string) {
	if _, err := a.recordAnswer(AnswerRecord{ItemID: "belief:" + id + "#moment", Kind: answerKindBelief, Source: answerSourceAboutYou,
		Title: momentTitle, Action: answerActionNotThis, Surface: workSurfaceComputer}); err != nil {
		log.Printf("[answers] a Not this was not kept: %v", err)
	}
}

// importedMemoryLabel is the other AI's name on a claim brought in from it
// ("import:ChatGPT"), or "".
func importedMemoryLabel(source string) string {
	label, ok := strings.CutPrefix(strings.TrimSpace(source), memoryImportSourcePrefix)
	if !ok {
		return ""
	}
	return strings.TrimSpace(label)
}
