package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"wails-gui/internal/pii"
	"wails-gui/internal/platform/activitymon"
	"wails-gui/internal/sop"
)

// The confirm moment on How you do things (RC 3): when the same steps show up
// in the same order in three or more observed sessions, Isoform proposes a
// how-to. The proposal is a draft sop.Entry in a dedicated candidates store
// (~/.yansu-agent/know-how-candidates, the same file-first SOP bundle format
// with Scope and SourceIDs), kept apart from the Playbooks store so drafts
// never surface there. Confirm promotes the draft and writes the knowledge
// entry the grid shows; Not a how-to suppresses that exact step sequence for
// good, with Undo while the result line is on screen.
//
// Privacy: only sessions that pass the model-read gate (not protected, no
// sensitive app, outside every pause, Closed Eyes off) can seed or keep a
// candidate, and the gate is re-run on every read. Protecting an app or
// forgetting the underlying sessions withdraws the draft; Closed Eyes or a
// pause only hides it until they pass.

const (
	knowHowCandidateWindowDays  = 30
	knowHowCandidateMaxScan     = 400
	knowHowCandidateMinSessions = 3
	knowHowCandidateMinSteps    = 2
	knowHowCandidateMaxSteps    = 24
	knowHowCandidateMinDuration = 2 * time.Minute
	knowHowCandidateTTL         = 2 * time.Minute
	knowHowCandidateIDPrefix    = "knowhow-"
	knowHowCandidateMaxEditStep = 40
	knowHowCandidateStepRunes   = 300
)

// KnowHowCandidate is the proposal the page shows: the draft's title, trigger
// and steps plus the evidence summary (sessions, dates, mean duration).
type KnowHowCandidate struct {
	ID            string   `json:"id"`
	Title         string   `json:"title"`
	Description   string   `json:"description"`
	Trigger       string   `json:"trigger"`
	Steps         []string `json:"steps"`
	Apps          []string `json:"apps"`
	SessionIDs    []string `json:"sessionIds"`
	Times         int      `json:"times"`
	OccurredAtMs  []int64  `json:"occurredAtMs"` // session starts, oldest first
	AvgDurationMs int64    `json:"avgDurationMs"`
	Scope         string   `json:"scope"`
}

// KnowHowCandidateConfirm names the knowledge entry a confirm wrote, so the
// page can select the new card.
type KnowHowCandidateConfirm struct {
	KnowledgeID string `json:"knowledgeId"`
}

var errKnowHowCandidateNotFound = errors.New("know-how candidate not found")

// ── Detection (pure) ──

var knowHowStepDigits = regexp.MustCompile(`\d+`)

// knowHowStepKey is the signature form of one step: app and title, lowercased,
// whitespace collapsed, digits masked so dates and counters still match.
func knowHowStepKey(app, title string) string {
	norm := func(s string) string {
		return knowHowStepDigits.ReplaceAllString(strings.ToLower(strings.Join(strings.Fields(s), " ")), "#")
	}
	return norm(app) + "\x00" + norm(title)
}

// knowHowSessionSteps reduces a session's events to its step sequence: focus
// and app-switch moments with an app name, consecutive repeats collapsed. The
// label is what the user reads (the window title when there is one); the key
// is what the signature hashes.
func knowHowSessionSteps(events []activitymon.ActivityEvent) (labels []string, keys []string, titled []bool) {
	for _, event := range events {
		switch event.Kind {
		case activitymon.EventFocus, activitymon.EventAppSwitch:
		default:
			continue
		}
		app := strings.Join(strings.Fields(event.AppName), " ")
		if app == "" {
			continue
		}
		title := strings.Join(strings.Fields(event.WindowTitle), " ")
		// A window titled exactly like its app is the untitled step.
		if strings.EqualFold(title, app) {
			title = ""
		}
		key := knowHowStepKey(app, title)
		if len(keys) > 0 && keys[len(keys)-1] == key {
			continue
		}
		label := title
		if label == "" {
			label = app
		}
		labels = append(labels, label)
		keys = append(keys, key)
		titled = append(titled, title != "")
	}
	return labels, keys, titled
}

// knowHowSameWay groups sessions by their exact step sequence. Groups clear
// the bar with knowHowCandidateMinSessions occurrences or more.
type knowHowSameWay struct {
	signature   string
	labels      []string // display steps of the first occurrence
	titled      []bool
	occurrences []knowHowCandidateOccurrence
	apps        []string // first-seen order across occurrences
	titleCounts map[string]int
}

type knowHowCandidateOccurrence struct {
	sessionID string
	startMs   int64
	endMs     int64
}

func knowHowSignature(keys []string) string {
	sum := sha256.Sum256([]byte(strings.Join(keys, "\n")))
	return hex.EncodeToString(sum[:])
}

func knowHowCandidateID(signature string) string {
	return knowHowCandidateIDPrefix + signature[:12]
}

// detectKnowHowSameWay finds repeated step sequences. Sessions arrive newest
// first; groups are returned most occurrences first, ties broken by the most
// recent occurrence.
func detectKnowHowSameWay(sessions []ActivitySessionRow, eventsFor func(sessionID string) ([]activitymon.ActivityEvent, error)) []*knowHowSameWay {
	groups := map[string]*knowHowSameWay{}
	var order []string
	for _, session := range sessions {
		if session.IsMeeting || session.EndTime-session.StartTime < knowHowCandidateMinDuration.Milliseconds() {
			continue
		}
		events, err := eventsFor(session.ID)
		if err != nil || len(events) == 0 {
			continue
		}
		labels, keys, titled := knowHowSessionSteps(events)
		if len(keys) < knowHowCandidateMinSteps || len(keys) > knowHowCandidateMaxSteps {
			continue
		}
		signature := knowHowSignature(keys)
		group, ok := groups[signature]
		if !ok {
			group = &knowHowSameWay{signature: signature, labels: labels, titled: titled, titleCounts: map[string]int{}}
			groups[signature] = group
			order = append(order, signature)
		}
		for i, label := range labels {
			if i < len(titled) && titled[i] {
				group.titleCounts[label]++
			}
		}
		for _, event := range events {
			app := strings.Join(strings.Fields(event.AppName), " ")
			if app != "" && !knowHowStringSliceContains(group.apps, app) {
				group.apps = append(group.apps, app)
			}
		}
		group.occurrences = append(group.occurrences, knowHowCandidateOccurrence{
			sessionID: session.ID,
			startMs:   session.StartTime,
			endMs:     session.EndTime,
		})
	}
	out := make([]*knowHowSameWay, 0, len(groups))
	for _, signature := range order {
		group := groups[signature]
		if len(group.occurrences) >= knowHowCandidateMinSessions {
			out = append(out, group)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if len(out[i].occurrences) != len(out[j].occurrences) {
			return len(out[i].occurrences) > len(out[j].occurrences)
		}
		return out[i].occurrences[0].startMs > out[j].occurrences[0].startMs
	})
	return out
}

func knowHowStringSliceContains(slice []string, value string) bool {
	for _, item := range slice {
		if item == value {
			return true
		}
	}
	return false
}

// knowHowCandidateTitle is the window title seen most often across the
// occurrences; a session of untitled windows falls back to the first app.
func (g *knowHowSameWay) title(lang string) string {
	best, bestCount := "", 0
	for _, label := range g.labels {
		if count := g.titleCounts[label]; count > bestCount {
			best, bestCount = label, count
		}
	}
	if best == "" {
		app := ""
		if len(g.apps) > 0 {
			app = g.apps[0]
		}
		words := knowHowCandidateWords(lang)
		best = fmt.Sprintf(words.titleFallback, app)
	}
	runes := []rune(best)
	if len(runes) > 80 {
		best = strings.TrimSpace(string(runes[:80]))
	}
	return best
}

// ── Generated copy (the user's language; steps stay as observed) ──

type knowHowCandidateLang struct {
	titleFallback string
	description   string
	triggerStep   string
	triggerPlain  string
	appsJoin      func(apps []string) string
}

var knowHowCandidateCopy = map[string]knowHowCandidateLang{
	"en": {
		titleFallback: "%s routine",
		description:   "The same %d steps in %s, %d times.",
		triggerStep:   "When you open %s again.",
		triggerPlain:  "When the same task comes around again.",
		appsJoin: func(apps []string) string {
			switch len(apps) {
			case 0:
				return ""
			case 1:
				return apps[0]
			case 2:
				return apps[0] + " and " + apps[1]
			default:
				return strings.Join(apps[:len(apps)-1], ", ") + " and " + apps[len(apps)-1]
			}
		},
	},
	"zh-CN": {
		titleFallback: "%s 的固定流程",
		description:   "同样的 %d 个步骤，在%s里重复了 %d 次。",
		triggerStep:   "当你再次打开「%s」时。",
		triggerPlain:  "当同样的任务再次出现时。",
		appsJoin:      func(apps []string) string { return strings.Join(apps, "、") },
	},
}

func knowHowCandidateWords(lang string) knowHowCandidateLang {
	if words, ok := knowHowCandidateCopy[lang]; ok {
		return words
	}
	return knowHowCandidateCopy["en"]
}

// ── The candidate bundle (a draft sop.Entry) ──

const knowHowCandidateStepsHeading = "## Steps"

func knowHowCandidateBody(steps []string) string {
	var b strings.Builder
	b.WriteString(knowHowCandidateStepsHeading + "\n\n")
	for _, step := range steps {
		fmt.Fprintf(&b, "- %s\n", step)
	}
	return strings.TrimSpace(b.String()) + "\n"
}

func knowHowCandidateSteps(body string) []string {
	return knowHowSectionPoints(body, knowHowCandidateStepsHeading, knowHowCandidateMaxEditStep)
}

// knowHowCandidateDraft renders the draft bundle for a detected repetition.
func knowHowCandidateDraft(group *knowHowSameWay, lang string) sop.Entry {
	words := knowHowCandidateWords(lang)
	apps := words.appsJoin(group.apps)
	times := len(group.occurrences)
	sessionIDs := make([]string, 0, times)
	for i := times - 1; i >= 0; i-- { // occurrences arrive newest first
		sessionIDs = append(sessionIDs, group.occurrences[i].sessionID)
	}
	trigger := words.triggerPlain
	if len(group.labels) > 0 {
		trigger = fmt.Sprintf(words.triggerStep, group.labels[0])
	}
	return sop.Entry{
		ID:          knowHowCandidateID(group.signature),
		Title:       group.title(lang),
		Description: fmt.Sprintf(words.description, len(group.labels), apps, times),
		Trigger:     trigger,
		Status:      sop.StatusDraft,
		Source:      sop.SourceObserved,
		Scope:       "mine",
		SourceIDs:   sessionIDs,
		Body:        knowHowCandidateBody(group.labels),
	}
}

// ── Read gate: the evidence must still be readable, every time ──

// knowHowCandidateSourcesReadable re-runs the session read gate over every
// source session. A missing, protected, paused or sensitive source answers
// false. A store or policy read failure answers an error instead: temporary
// unavailability is a withholding, never proof of withdrawal.
func (a *App) knowHowCandidateSourcesReadable(entry sop.Entry) (bool, error) {
	if a == nil || a.activity == nil || a.activity.store == nil {
		return false, errors.New("activity store unavailable")
	}
	if len(entry.SourceIDs) == 0 {
		return false, nil
	}
	sessions, err := a.activity.store.GetSessionsByIDs(entry.SourceIDs)
	if err != nil {
		return false, err
	}
	if len(sessions) != len(entry.SourceIDs) {
		return false, nil
	}
	for _, id := range entry.SourceIDs {
		session := sessions[id]
		if session == nil {
			return false, nil
		}
		allowed, err := a.activitySessionAllowsModelReadResult(a.activity.store, *session)
		if err != nil {
			return false, err
		}
		if !allowed {
			return false, nil
		}
	}
	return true, nil
}

// knowHowCandidateEvidenceIntact is the durable half of the gate: every source
// session still exists and is neither protected, sensitive nor inside a pause
// window. Temporary unreadability (Closed Eyes) hides a draft; only this
// withdraws it — and only with a clean read. An error means the source or
// policy store could not be read, which is retryable and keeps the draft.
func (a *App) knowHowCandidateEvidenceIntact(entry sop.Entry) (bool, error) {
	if a == nil || a.activity == nil || a.activity.store == nil {
		return false, errors.New("activity store unavailable")
	}
	if len(entry.SourceIDs) == 0 {
		return false, nil
	}
	store := a.activity.store
	sessions, err := store.GetSessionsByIDs(entry.SourceIDs) // protected sessions come back missing
	if err != nil {
		return false, err
	}
	if len(sessions) != len(entry.SourceIDs) {
		return false, nil
	}
	policy, err := a.activityReadPrivacyPolicy()
	if err != nil {
		return false, err
	}
	for _, id := range entry.SourceIDs {
		session := sessions[id]
		if session == nil || session.TriageStatus == "protected" {
			return false, nil
		}
		source, err := store.activitySessionProvenance(*session)
		if err != nil {
			return false, err
		}
		if !policy.allowsStoredDerivative(source) {
			return false, nil
		}
	}
	return true, nil
}

// withdrawKnowHowCandidatesForProtection runs on protect-app: every draft
// whose evidence no longer passes the read gate is deleted. Confirmed entries
// stay; they are the user's reviewed know-how and hold only session ids.
func (a *App) withdrawKnowHowCandidatesForProtection() {
	if a == nil || a.knowHowCandidates == nil {
		return
	}
	a.knowHow.candMu.Lock()
	defer a.knowHow.candMu.Unlock()
	entries, err := a.knowHowCandidates.List()
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.Status != sop.StatusDraft || entry.Source != sop.SourceObserved {
			continue
		}
		intact, err := a.knowHowCandidateEvidenceIntact(entry)
		if err != nil || intact {
			// A temporary read failure is not proof of protection; keep the draft.
			continue
		}
		if err := a.knowHowCandidates.Delete(entry.ID); err == nil {
			a.invalidateKnowHowCandidateCacheLocked()
			a.emitKnowHowCandidateUpdated("withdrawn", entry.ID)
		}
	}
}

// ── Decline markers (durable, per step sequence) ──

func knowHowCandidateDeclineKey(id string) string {
	return "know_how_candidate_declined:" + id
}

func (a *App) knowHowCandidateDeclined(ctx context.Context, id string) bool {
	if a.store == nil {
		return false
	}
	value, err := a.store.GetUIState(ctx, knowHowCandidateDeclineKey(id))
	return err == nil && value == "1"
}

// ── View ──

// knowHowCandidateView projects a draft bundle for the page, with the display
// text PII-masked. Returns nil when the evidence no longer holds up or cannot
// be read right now.
func (a *App) knowHowCandidateView(entry sop.Entry) *KnowHowCandidate {
	readable, err := a.knowHowCandidateSourcesReadable(entry)
	if err != nil || !readable {
		return nil
	}
	sessions, err := a.activity.store.GetSessionsByIDs(entry.SourceIDs)
	if err != nil {
		return nil
	}
	view := &KnowHowCandidate{
		ID:    entry.ID,
		Title: pii.DisplayText(entry.Title),
		Scope: entry.Scope,
	}
	if view.Scope == "" {
		view.Scope = "mine"
	}
	view.Trigger = pii.DisplayText(entry.Trigger)
	view.Description = pii.DisplayText(entry.Description)
	steps := knowHowCandidateSteps(entry.Body)
	for i := range steps {
		steps[i] = pii.DisplayText(steps[i])
	}
	view.Steps = steps
	var totalMs int64
	appsSeen := map[string]bool{}
	occurrences := make([]knowHowCandidateOccurrence, 0, len(entry.SourceIDs))
	for _, id := range entry.SourceIDs {
		session := sessions[id]
		if session == nil {
			return nil
		}
		view.SessionIDs = append(view.SessionIDs, id)
		occurrences = append(occurrences, knowHowCandidateOccurrence{sessionID: id, startMs: session.StartTime, endMs: session.EndTime})
		totalMs += session.EndTime - session.StartTime
		var names []string
		if json.Unmarshal([]byte(session.AppNames), &names) == nil {
			for _, name := range names {
				if name = strings.TrimSpace(name); name != "" && !appsSeen[name] {
					appsSeen[name] = true
					view.Apps = append(view.Apps, name)
				}
			}
		}
	}
	sort.SliceStable(occurrences, func(i, j int) bool { return occurrences[i].startMs < occurrences[j].startMs })
	for _, occurrence := range occurrences {
		view.OccurredAtMs = append(view.OccurredAtMs, occurrence.startMs)
	}
	view.Times = len(occurrences)
	if view.Times > 0 {
		view.AvgDurationMs = totalMs / int64(view.Times)
	}
	return view
}

// ── Bindings ──

// knowHowCandidatePrivacy is the privacy fingerprint a cached proposal was
// published under, mirroring activityContextPrivacyFingerprint: the source
// store's privacy revision (odd while a change is in flight), the
// sensitive-app policy, the persisted observation pause log and Closed Eyes. A warm
// read is served only while every component still matches; any transition
// re-runs the read gates before anything is shown again.
type knowHowCandidatePrivacy struct {
	store         *ActivityStore
	revision      uint64
	pauses        [32]byte
	sensitiveApps string
	closedEyes    bool
}

func (a *App) knowHowCandidatePrivacySnapshot() (knowHowCandidatePrivacy, bool, error) {
	if a == nil || a.activity == nil || a.activity.store == nil {
		return knowHowCandidatePrivacy{}, false, nil
	}
	policy, err := a.activityReadPrivacyPolicy()
	if err != nil {
		return knowHowCandidatePrivacy{}, false, err
	}
	pauses, err := json.Marshal(policy.pauses)
	if err != nil {
		return knowHowCandidatePrivacy{}, false, err
	}
	snapshot := knowHowCandidatePrivacy{
		store:      a.activity.store,
		revision:   a.activity.store.readPrivacyRevision.Load(),
		pauses:     sha256.Sum256(pauses),
		closedEyes: a.IsClosedEyesModeEnabled(),
	}
	apps, err := json.Marshal(pii.GetUserSensitiveApps())
	if err != nil || snapshot.revision%2 != 0 {
		// Never publish a view assembled while a source change is in flight.
		return snapshot, false, err
	}
	snapshot.sensitiveApps = string(apps)
	return snapshot, true, nil
}

// GetKnowHowCandidate returns the one open proposal, detecting a new one when
// no draft is waiting. Read-gated on every call; a candidate whose evidence
// was forgotten, paused or protected is withdrawn instead of shown. The warm
// cache is served only while the privacy fingerprint it was computed under
// still matches exactly.
func (a *App) GetKnowHowCandidate() (perfValue *KnowHowCandidate, perfErr error) {
	defer logPageBinding("GetKnowHowCandidate", time.Now(), &perfValue)
	if a == nil || a.knowHowCandidates == nil || a.activity == nil || a.activity.store == nil {
		return nil, nil
	}
	a.knowHow.candMu.Lock()
	defer a.knowHow.candMu.Unlock()
	privacy, ok, err := a.knowHowCandidatePrivacySnapshot()
	if err != nil || !ok {
		return nil, err
	}
	view := a.knowHow.candCached
	fresh := time.Since(a.knowHow.candAt) < knowHowCandidateTTL && a.knowHow.candPrivacy == privacy
	if fresh && view != nil {
		// A closed source store is temporary unavailability even if its
		// revision has not changed. Re-run the source gate on warm reads.
		fresh, err = a.knowHowCandidateSourcesReadable(sop.Entry{SourceIDs: view.SessionIDs})
		if err != nil {
			return nil, err
		}
	}
	if !fresh {
		view, err = a.computeKnowHowCandidate()
		if err != nil {
			return nil, err
		}
	}
	// A source/policy transition during detection must not publish the view
	// assembled under the old policy, including on this first response.
	after, allowed, err := a.knowHowCandidatePrivacySnapshot()
	if err != nil || !allowed || after != privacy {
		return nil, err
	}
	if !fresh {
		a.knowHow.candAt = time.Now()
		a.knowHow.candCached = view
		a.knowHow.candPrivacy = privacy
	}
	return view, nil
}

func (a *App) computeKnowHowCandidate() (*KnowHowCandidate, error) {
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	entries, err := a.knowHowCandidates.List()
	if err != nil {
		return nil, err
	}
	// A waiting draft wins over a new detection. Drafts whose evidence is
	// gone are withdrawn on sight; drafts only temporarily unreadable (Closed
	// Eyes) hide until it passes, and a draft whose evidence cannot be read
	// right now is withheld with its edited fields kept.
	for _, entry := range entries {
		if entry.Status != sop.StatusDraft || entry.Source != sop.SourceObserved {
			continue
		}
		if a.knowHowCandidateDeclined(ctx, entry.ID) {
			if err := a.knowHowCandidates.Delete(entry.ID); err != nil {
				return nil, err
			}
			a.emitKnowHowCandidateUpdated("withdrawn", entry.ID)
			continue
		}
		intact, err := a.knowHowCandidateEvidenceIntact(entry)
		if err != nil {
			return nil, err
		}
		if !intact {
			if err := a.knowHowCandidates.Delete(entry.ID); err != nil {
				return nil, err
			}
			a.emitKnowHowCandidateUpdated("withdrawn", entry.ID)
			continue
		}
		readable, err := a.knowHowCandidateSourcesReadable(entry)
		if err != nil {
			return nil, err
		}
		if !readable {
			return nil, nil
		}
		return a.knowHowCandidateView(entry), nil
	}
	sessions, err := a.activity.store.modelSessionsOverlapping(time.Now().AddDate(0, 0, -knowHowCandidateWindowDays).UnixMilli(), time.Now().UnixMilli())
	if err != nil {
		return nil, err
	}
	if len(sessions) > knowHowCandidateMaxScan {
		sessions = sessions[:knowHowCandidateMaxScan]
	}
	readable := a.filterActivityModelSessions(a.activity.store, sessions)
	existing := map[string]bool{}
	for _, entry := range entries {
		existing[entry.ID] = true
	}
	for _, group := range detectKnowHowSameWay(readable, a.activity.store.GetSessionEvents) {
		id := knowHowCandidateID(group.signature)
		if existing[id] || a.knowHowCandidateDeclined(ctx, id) {
			continue
		}
		draft := knowHowCandidateDraft(group, a.currentUILanguage())
		saved, err := a.knowHowCandidates.Save(draft)
		if err != nil {
			return nil, err
		}
		a.emitKnowHowCandidateUpdated("proposed", saved.ID)
		return a.knowHowCandidateView(saved), nil
	}
	return nil, nil
}

// ConfirmKnowHowCandidate writes the how-to down: the knowledge entry the
// grid shows (source observed, scope mine, the sessions it came from), then
// the draft promoted to confirmed as the lifecycle record. Wails binding.
func (a *App) ConfirmKnowHowCandidate(id string) (KnowHowCandidateConfirm, error) {
	if a == nil || a.knowHowCandidates == nil {
		return KnowHowCandidateConfirm{}, errKnowHowCandidateNotFound
	}
	a.knowHow.candMu.Lock()
	defer a.knowHow.candMu.Unlock()
	entry, err := a.knowHowCandidates.Get(id)
	if err != nil || entry.Status != sop.StatusDraft {
		return KnowHowCandidateConfirm{}, errKnowHowCandidateNotFound
	}
	// Fail closed: nothing is written from evidence that is no longer readable.
	// Only evidence that is gone for good withdraws the draft; a temporary
	// unreadability (Closed Eyes) or a source outage just refuses the confirm
	// and keeps the draft.
	readable, err := a.knowHowCandidateSourcesReadable(entry)
	if err != nil {
		return KnowHowCandidateConfirm{}, err
	}
	if !readable {
		if intact, evidenceErr := a.knowHowCandidateEvidenceIntact(entry); evidenceErr == nil && !intact {
			_ = a.knowHowCandidates.Delete(id)
		}
		a.invalidateKnowHowCandidateCacheLocked()
		return KnowHowCandidateConfirm{}, errKnowHowCandidateNotFound
	}
	sessions, err := a.activity.store.GetSessionsByIDs(entry.SourceIDs)
	if err != nil {
		return KnowHowCandidateConfirm{}, err
	}
	apps := []string{}
	seen := map[string]bool{}
	for _, sessionID := range entry.SourceIDs {
		var names []string
		if session := sessions[sessionID]; session != nil && json.Unmarshal([]byte(session.AppNames), &names) == nil {
			for _, name := range names {
				if name = strings.TrimSpace(name); name != "" && !seen[name] {
					seen[name] = true
					apps = append(apps, name)
				}
			}
		}
	}
	dir, err := knowledgeDir()
	if err != nil {
		return KnowHowCandidateConfirm{}, err
	}
	unlock := memoryFileLocks.Acquire("knowledge:" + id)
	defer unlock()
	content := buildOKFConfirmedHowTo(id, entry, apps, time.Now())
	path := filepath.Join(dir, id+".md")
	previous, readErr := os.ReadFile(path)
	if readErr != nil && !os.IsNotExist(readErr) {
		return KnowHowCandidateConfirm{}, readErr
	}
	content = knowledgeProvenance(content, string(previous), []string{"observed"}, apps, true)
	if _, err := a.GetKnowledgeEntry(id); err == nil {
		if err := snapshotKnowledgeFile(dir, id); err != nil {
			return KnowHowCandidateConfirm{}, err
		}
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return KnowHowCandidateConfirm{}, err
	}
	if _, err := a.knowHowCandidates.Mutate(id, func(e *sop.Entry) {
		e.Status = sop.StatusConfirmed
		e.Scope = "mine"
		e.Source = sop.SourceObserved
	}); err != nil {
		return KnowHowCandidateConfirm{}, err
	}
	a.markMemoryDirty("knowhow-confirm")
	a.invalidateKnowHowCandidateCacheLocked()
	a.emitKnowHowCandidateUpdated("confirmed", id)
	a.emitBrowserEvent("insight:saved", map[string]string{"kind": "knowledge", "id": id, "title": entry.Title, "source": "observed"})
	if a.telemetry != nil {
		a.telemetry.Capture("knowhow.candidate_confirmed", map[string]any{"times": len(entry.SourceIDs)})
	}
	return KnowHowCandidateConfirm{KnowledgeID: id}, nil
}

// buildOKFConfirmedHowTo is the knowledge file a confirmed candidate becomes:
// the steps as Key Insights so the card grid reads them, and the scope the
// chip shows.
func buildOKFConfirmedHowTo(id string, entry sop.Entry, apps []string, now time.Time) string {
	steps := knowHowCandidateSteps(entry.Body)
	content := buildOKFKnowledgeMarkdown(id, entry.Title, entry.Description, now, "observed", apps, entry.SourceIDs, steps, "how_to")
	doc, ok := parseOKFDocument(content)
	if !ok {
		return content
	}
	doc.Frontmatter["scope"] = "mine"
	if strings.TrimSpace(entry.Trigger) != "" {
		doc.Frontmatter["trigger"] = entry.Trigger
	}
	return renderOKFDocument(doc.Frontmatter, doc.Body)
}

// UndoConfirmKnowHowCandidate takes a confirm back while its Undo is on
// screen: the knowledge entry goes, the draft returns. Wails binding.
func (a *App) UndoConfirmKnowHowCandidate(id string) error {
	if a == nil || a.knowHowCandidates == nil {
		return errKnowHowCandidateNotFound
	}
	a.knowHow.candMu.Lock()
	defer a.knowHow.candMu.Unlock()
	entry, err := a.knowHowCandidates.Get(id)
	if err != nil || entry.Status != sop.StatusConfirmed {
		return errKnowHowCandidateNotFound
	}
	if _, err := a.GetKnowledgeEntry(id); err == nil {
		if err := a.DeleteKnowledgeEntry(id); err != nil {
			return err
		}
	}
	if _, err := a.knowHowCandidates.Mutate(id, func(e *sop.Entry) {
		e.Status = sop.StatusDraft
	}); err != nil {
		return err
	}
	a.invalidateKnowHowCandidateCacheLocked()
	a.emitKnowHowCandidateUpdated("proposed", id)
	return nil
}

// DeclineKnowHowCandidate is Not a how-to: the draft is deleted and the same
// step sequence is never proposed again on this computer. The suppression is
// durable before the draft goes, or not at all. Wails binding.
func (a *App) DeclineKnowHowCandidate(id string) error {
	if a == nil || a.knowHowCandidates == nil {
		return errKnowHowCandidateNotFound
	}
	if a.store == nil {
		return fmt.Errorf("state store unavailable")
	}
	a.knowHow.candMu.Lock()
	defer a.knowHow.candMu.Unlock()
	entry, err := a.knowHowCandidates.Get(id)
	if err != nil || entry.Status != sop.StatusDraft {
		return errKnowHowCandidateNotFound
	}
	if err := a.store.SetUIState(context.Background(), knowHowCandidateDeclineKey(id), "1"); err != nil {
		return err
	}
	if err := a.knowHowCandidates.Delete(id); err != nil {
		return err
	}
	a.invalidateKnowHowCandidateCacheLocked()
	a.emitKnowHowCandidateUpdated("declined", id)
	if a.telemetry != nil {
		a.telemetry.Capture("knowhow.candidate_declined", map[string]any{})
	}
	return nil
}

// UndoDeclineKnowHowCandidate lifts the suppression while its Undo is on
// screen; the next read proposes the repetition again. Wails binding.
func (a *App) UndoDeclineKnowHowCandidate(id string) error {
	if a == nil || a.store == nil {
		return fmt.Errorf("state store unavailable")
	}
	a.knowHow.candMu.Lock()
	defer a.knowHow.candMu.Unlock()
	if err := a.store.SetUIState(context.Background(), knowHowCandidateDeclineKey(id), ""); err != nil {
		return err
	}
	a.invalidateKnowHowCandidateCacheLocked()
	a.emitKnowHowCandidateUpdated("proposed", id)
	return nil
}

// SaveKnowHowCandidate applies Edit steps: title, when to use it and the step
// list change; the draft stays a draft until Confirm. Wails binding.
func (a *App) SaveKnowHowCandidate(id, title, trigger string, steps []string) (*KnowHowCandidate, error) {
	if a == nil || a.knowHowCandidates == nil {
		return nil, errKnowHowCandidateNotFound
	}
	title = strings.TrimSpace(title)
	clean := make([]string, 0, len(steps))
	for _, step := range steps {
		step = strings.TrimSpace(step)
		if step == "" {
			continue
		}
		runes := []rune(step)
		if len(runes) > knowHowCandidateStepRunes {
			step = string(runes[:knowHowCandidateStepRunes])
		}
		clean = append(clean, step)
		if len(clean) == knowHowCandidateMaxEditStep {
			break
		}
	}
	if title == "" || len(clean) == 0 {
		return nil, fmt.Errorf("know-how candidate needs a title and at least one step")
	}
	a.knowHow.candMu.Lock()
	defer a.knowHow.candMu.Unlock()
	entry, err := a.knowHowCandidates.Get(id)
	if err != nil || entry.Status != sop.StatusDraft {
		return nil, errKnowHowCandidateNotFound
	}
	saved, err := a.knowHowCandidates.Mutate(id, func(e *sop.Entry) {
		e.Title = title
		e.Trigger = strings.TrimSpace(trigger)
		e.Body = knowHowCandidateBody(clean)
	})
	if err != nil {
		return nil, err
	}
	a.invalidateKnowHowCandidateCacheLocked()
	a.emitKnowHowCandidateUpdated("edited", id)
	return a.knowHowCandidateView(saved), nil
}

// invalidateKnowHowCandidateCacheLocked drops the warm publication. The caller
// holds candMu, so a compute in flight finishes first and its result is the
// one dropped — no stale publication survives a mutation or privacy hook.
func (a *App) invalidateKnowHowCandidateCacheLocked() {
	a.knowHow.candAt = time.Time{}
	a.knowHow.candCached = nil
	a.knowHow.candPrivacy = knowHowCandidatePrivacy{}
}

// invalidateKnowHowCandidateCache is the locking form for privacy lifecycle
// hooks that do not otherwise touch the candidate (sign-out).
func (a *App) invalidateKnowHowCandidateCache() {
	a.knowHow.candMu.Lock()
	defer a.knowHow.candMu.Unlock()
	a.invalidateKnowHowCandidateCacheLocked()
}

func (a *App) emitKnowHowCandidateUpdated(action, id string) {
	a.emitBrowserEvent("knowhow:candidate-updated", map[string]string{"action": action, "id": id})
}
