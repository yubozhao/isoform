package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

type MorningBriefWeekQuestion struct {
	WeekStart      string `json:"weekStart"`
	Recommendation string `json:"recommendation,omitempty"`
}

// PostMorningBriefWeekAnswer saves the explicit answer and posts it to Chat
// atomically. It starts no agent run and cannot authorize an action.
func (a *App) PostMorningBriefWeekAnswer(briefID, previous, answer string) error {
	return a.postMorningBriefWeekAnswer(briefID, previous, answer, false)
}

// PostMorningBriefWeekAnswerFromChip records the explicit recommendation choice.
func (a *App) PostMorningBriefWeekAnswerFromChip(briefID, previous, answer string) error {
	return a.postMorningBriefWeekAnswer(briefID, previous, answer, true)
}

func (a *App) postMorningBriefWeekAnswer(briefID, previous, answer string, fromChip bool) error {
	err := a.postMorningBriefWeekAnswerOnce(briefID, previous, answer, fromChip)
	if err == nil {
		noteBriefActed()
	}
	return err
}

func (a *App) postMorningBriefWeekAnswerOnce(briefID, previous, answer string, fromChip bool) error {
	answer = strings.TrimSpace(answer)
	if answer == "" || utf8.RuneCountInString(answer) > 160 || strings.ContainsAny(answer, "\r\n") || !mobileBriefCopySafe(answer) || redactThreadOutcomeText(answer) != answer {
		return errors.New("invalid week intent")
	}
	subjects, err := mobileBriefMailSubjects(a)
	if err != nil || mobileBriefValueEchoesMail(answer, subjects) {
		return errors.New("week intent unavailable")
	}
	ctx := context.Background()
	main, err := a.ensureMainChat(ctx)
	if err != nil {
		return err
	}
	morningBriefWeekMu.Lock()
	defer morningBriefWeekMu.Unlock()
	svc := a.mobileBriefSource()
	if svc == nil {
		return errMobileBriefUnavailable
	}
	brief, ok := a.storedBriefForWrite(svc, briefID)
	now := time.Now()
	if service, isService := svc.(*mobileBriefService); isService {
		now = service.now()
	}
	if !ok || brief.BriefID != briefID || !a.morningBriefWeekEligible(brief, now) {
		return errors.New("week question unavailable")
	}
	at, loc := mobileBriefAsOfTime(brief), mobileBriefNarrativeLocation(brief)
	key := "morning_brief_week_intent:" + morningBriefWeekStart(at, loc)
	id := uuid.NewString()
	origin, err := json.Marshal(map[string]string{"weekAnswerBriefId": briefID})
	if err != nil {
		return err
	}
	order, err := a.store.PostUIStateAnswerWithContext(ctx, key, previous, answer, main.LocalID, id, string(origin))
	if err != nil {
		return err
	}
	if a.telemetry != nil {
		var source *todayTurnSource
		if fromChip {
			source = &todayTurnSource{action: "T12"}
		}
		a.telemetry.Capture("chat.turn_sent", todayTurnProperties(source))
	}
	a.mainChatRowsChanged(main.LocalID)
	a.emitBrowserEvent(mainChatItemsAppendedEvent, map[string]any{"sessionId": main.LocalID, "itemId": id, "sortOrder": order, "kind": "user", "source": "today"})
	a.emitBrowserEvent("mobile:updated")
	a.emitBrowserEvent("brief:stored", briefID)
	return nil
}

type TodayWeekAnswer struct {
	WeekStart string `json:"weekStart"`
	Answer    string `json:"answer"`
}

var morningBriefWeekMu sync.Mutex

func morningBriefWeekStart(at time.Time, loc *time.Location) string {
	if loc == nil {
		loc = time.Local
	}
	day := at.In(loc)
	offset := (int(day.Weekday()) + 6) % 7
	return day.AddDate(0, 0, -offset).Format("2006-01-02")
}

func (a *App) morningBriefWeekIntent(ctx context.Context, at time.Time, loc *time.Location) (string, error) {
	if a == nil || a.store == nil || at.IsZero() {
		return "", errors.New("week intent unavailable")
	}
	return a.store.GetUIState(ctx, "morning_brief_week_intent:"+morningBriefWeekStart(at, loc))
}

func (a *App) morningBriefWeekIntentBindingReadable(ctx context.Context, brief MobileBriefV1, binding *mobileBriefModelBinding) bool {
	if binding == nil || binding.WeekIntentDigest == "" {
		return true
	}
	intent, err := a.morningBriefWeekIntent(ctx, mobileBriefAsOfTime(brief), mobileBriefNarrativeLocation(brief))
	subjects, mailErr := mobileBriefMailSubjects(a)
	return err == nil && mailErr == nil && intent != "" && mobileBriefCopySafe(intent) && redactThreadOutcomeText(intent) == intent && !mobileBriefValueEchoesMail(intent, subjects) && mobileBriefModelDigest(intent) == binding.WeekIntentDigest
}

// This projection is made after the ordinary source-withdrawal filters. A
// recommendation is a current readable goal, never an inferred user answer.
func (a *App) morningBriefWeekQuestion(brief MobileBriefV1, now time.Time) *MorningBriefWeekQuestion {
	if !a.morningBriefWeekEligible(brief, now) {
		return nil
	}
	at, loc := mobileBriefAsOfTime(brief), mobileBriefNarrativeLocation(brief)
	intent, err := a.morningBriefWeekIntent(context.Background(), at, loc)
	if err != nil || intent != "" {
		return nil
	}
	question := &MorningBriefWeekQuestion{WeekStart: morningBriefWeekStart(at, loc)}
	terms := a.mobileBriefExclusions()
	if title := a.morningBriefPriorWeekRecommendation(brief, at, loc); title != "" && !mobileBriefExcluded(terms, title) {
		question.Recommendation = title
		return question
	}
	for _, item := range brief.Sections.WorkingToward {
		if !item.EvidenceUnavailable && len(item.Refs) > 0 && mobileBriefCopySafe(item.Title) && morningBriefRecommendationUsable(item) && !morningBriefRecommendationExcluded(brief, item, terms) {
			question.Recommendation = strings.TrimRight(mobileBriefText(item.Title, 160), " .。")
			break
		}
	}
	return question
}

func (a *App) morningBriefWeekEligible(brief MobileBriefV1, now time.Time) bool {
	if a == nil || a.store == nil || a.IsClosedEyesModeEnabled() || brief.Slot != mobileBriefSlotMorning || brief.NarrativeStatus != "ok" || len(brief.Narrative) == 0 {
		return false
	}
	at, loc := mobileBriefAsOfTime(brief), mobileBriefNarrativeLocation(brief)
	return !at.IsZero() && at.In(loc).Weekday() == time.Monday && morningBriefWeekStart(at, loc) == morningBriefWeekStart(now, loc)
}

// The desktop can show the explicit answer after the one-shot question has
// left the shared phone and audio projection. Never resurrect withheld text.
func (a *App) todayWeekAnswer(brief MobileBriefV1, now time.Time) *TodayWeekAnswer {
	if !a.morningBriefWeekEligible(brief, now) {
		return nil
	}
	at, loc := mobileBriefAsOfTime(brief), mobileBriefNarrativeLocation(brief)
	answer, err := a.morningBriefWeekIntent(context.Background(), at, loc)
	if err != nil || answer == "" {
		return nil
	}
	subjects, mailErr := mobileBriefMailSubjects(a)
	if mailErr != nil || !mobileBriefCopySafe(answer) || redactThreadOutcomeText(answer) != answer || mobileBriefValueEchoesMail(answer, subjects) {
		return nil
	}
	return &TodayWeekAnswer{WeekStart: morningBriefWeekStart(at, loc), Answer: answer}
}

// Match last week's readable Still open rows to current readable rows. Only
// the current title is used, so an old row cannot revive withdrawn wording.
func (a *App) morningBriefPriorWeekRecommendation(current MobileBriefV1, at time.Time, loc *time.Location) string {
	service, ok := a.mobileBriefSource().(*mobileBriefService)
	if !ok || service == nil || service.store == nil {
		return ""
	}
	weekStart := morningBriefWeekStart(at, loc)
	start, err := time.ParseInLocation("2006-01-02", weekStart, loc)
	if err != nil {
		return ""
	}
	for _, prior := range service.store.briefs() {
		priorAt := mobileBriefAsOfTime(prior)
		if priorAt.IsZero() || !priorAt.Before(start) || priorAt.Before(start.AddDate(0, 0, -7)) {
			continue
		}
		readable, _ := a.mobileBriefDelivery(prior)
		if readable.YourWeek == nil {
			continue
		}
		for _, open := range readable.YourWeek.Open {
			if title := morningBriefCurrentOpenTitle(readable, current, open.ItemKey, a.mobileBriefExclusions()); title != "" {
				return title
			}
		}
	}
	return ""
}

func morningBriefCurrentOpenTitle(prior, current MobileBriefV1, key string, terms []string) string {
	priorRefs, currentRefs := mobileBriefEvidenceByRef(prior), mobileBriefEvidenceByRef(current)
	identity := ""
	for _, line := range mobileBriefSectionLines(prior.Sections) {
		for _, item := range line.items {
			if mobileBriefCardItemKey(item) == key && len(item.Refs) > 0 {
				ref := priorRefs[item.Refs[0]]
				if ref.SourceKind != "" && ref.SourceID != "" {
					identity = mobileBriefSourceKey(ref.SourceKind, ref.SourceID)
				}
			}
		}
	}
	if identity == "" {
		return ""
	}
	for _, items := range [][]MobileBriefItem{current.Sections.WorkingToward, current.Sections.Commitments, current.Sections.WaitingOnYou, current.Sections.OpenLoops} {
		for _, item := range items {
			if item.EvidenceUnavailable || len(item.Refs) == 0 || !mobileBriefCopySafe(item.Title) || !morningBriefRecommendationUsable(item) || morningBriefRecommendationExcluded(current, item, terms) || item.Status == "completed" || item.Status == "resolved" || item.Status == "done" {
				continue
			}
			ref := currentRefs[item.Refs[0]]
			if mobileBriefSourceKey(ref.SourceKind, ref.SourceID) == identity {
				return strings.TrimRight(mobileBriefText(item.Title, 160), " .。")
			}
		}
	}
	return ""
}

// Check the source label as well as the displayed title: short names can omit
// the words in the underlying record that identify the excluded subject.
func morningBriefRecommendationExcluded(brief MobileBriefV1, item MobileBriefItem, terms []string) bool {
	if mobileBriefExcluded(terms, item.Title, item.Detail, item.Kind, item.NextStep) {
		return true
	}
	evidence := mobileBriefEvidenceByRef(brief)
	for _, ref := range item.Refs {
		if mobileBriefExcluded(terms, evidence[ref].Label) {
			return true
		}
	}
	return false
}

func morningBriefWeekQuestionSpeech(question *MorningBriefWeekQuestion, language string) string {
	if question == nil {
		return ""
	}
	text := pick(mobileBriefVoiceLang(language), "What is this week about?", "这周最重要的事是什么？")
	if question.Recommendation != "" {
		text += pick(mobileBriefVoiceLang(language), " I'd start with "+question.Recommendation+".", "我建议以"+question.Recommendation+"为重点。")
	}
	return text
}

// SaveMorningBriefWeekIntent records only an explicit answer to the current
// week's offered question. A stale phone cannot overwrite another answer.
func (a *App) SaveMorningBriefWeekIntent(briefID, answer string) error {
	answer = strings.TrimSpace(answer)
	if answer == "" || utf8.RuneCountInString(answer) > 160 || strings.ContainsAny(answer, "\r\n") || !mobileBriefCopySafe(answer) || redactThreadOutcomeText(answer) != answer {
		return errors.New("invalid week intent")
	}
	subjects, err := mobileBriefMailSubjects(a)
	if err != nil || mobileBriefValueEchoesMail(answer, subjects) {
		return errors.New("week intent unavailable")
	}
	morningBriefWeekMu.Lock()
	defer morningBriefWeekMu.Unlock()
	svc := a.mobileBriefSource()
	if svc == nil {
		return errMobileBriefUnavailable
	}
	brief, ok := a.storedBriefForWrite(svc, briefID)
	now := time.Now()
	if service, isService := svc.(*mobileBriefService); isService {
		now = service.now()
	}
	// The question is worked out now from the stored brief and the stored answer,
	// never from a display copy made before the answer was given.
	question := a.morningBriefWeekQuestion(brief, now)
	if !ok || question == nil {
		return errors.New("week question unavailable")
	}
	at, loc := mobileBriefAsOfTime(brief), mobileBriefNarrativeLocation(brief)
	if current, err := a.morningBriefWeekIntent(context.Background(), at, loc); err == nil && current != "" {
		return errors.New("week question unavailable")
	}
	if err := a.store.SetUIState(context.Background(), "morning_brief_week_intent:"+question.WeekStart, answer); err != nil {
		return err
	}
	briefSourceChanged()
	a.emitBrowserEvent("mobile:updated")
	a.emitBrowserEvent("brief:stored", briefID)
	return nil
}

// ChangeMorningBriefWeekIntent changes only the answer the desktop showed;
// another device's newer choice cannot be overwritten by a stale click.
func (a *App) ChangeMorningBriefWeekIntent(briefID, previous, answer string) error {
	answer = strings.TrimSpace(answer)
	if answer == "" || utf8.RuneCountInString(answer) > 160 || strings.ContainsAny(answer, "\r\n") || !mobileBriefCopySafe(answer) || redactThreadOutcomeText(answer) != answer {
		return errors.New("invalid week intent")
	}
	subjects, err := mobileBriefMailSubjects(a)
	if err != nil || mobileBriefValueEchoesMail(answer, subjects) {
		return errors.New("week intent unavailable")
	}
	morningBriefWeekMu.Lock()
	defer morningBriefWeekMu.Unlock()
	svc := a.mobileBriefSource()
	if svc == nil {
		return errMobileBriefUnavailable
	}
	brief, ok := a.storedBriefForWrite(svc, briefID)
	now := time.Now()
	if service, isService := svc.(*mobileBriefService); isService {
		now = service.now()
	}
	if !ok || brief.BriefID != briefID || !a.morningBriefWeekEligible(brief, now) {
		return errors.New("week question unavailable")
	}
	at, loc := mobileBriefAsOfTime(brief), mobileBriefNarrativeLocation(brief)
	current, err := a.morningBriefWeekIntent(context.Background(), at, loc)
	if err != nil || current == "" || current != previous {
		return errors.New("week answer changed")
	}
	if err := a.store.SetUIState(context.Background(), "morning_brief_week_intent:"+morningBriefWeekStart(at, loc), answer); err != nil {
		return err
	}
	briefSourceChanged()
	a.emitBrowserEvent("mobile:updated")
	a.emitBrowserEvent("brief:stored", briefID)
	return nil
}

func (bridge *mobileBridge) serveMorningBriefWeekIntent(w http.ResponseWriter, r *http.Request, device mobileDeviceRecord, briefID string) {
	var input struct {
		IdempotencyKey string `json:"idempotencyKey"`
		Answer         string `json:"answer"`
	}
	if !mobileValidID(briefID) {
		mobileJSON(w, http.StatusBadRequest, nil, "invalid request")
		return
	}
	if !mobileDecode(w, r, &input) {
		return
	}
	key, valid := mobileKey(device.DeviceID, input.IdempotencyKey)
	if !valid {
		mobileJSON(w, http.StatusBadRequest, nil, "invalid request")
		return
	}
	bridge.runReceipted(w, device, key, r.Method+" "+r.URL.Path, func(w http.ResponseWriter) {
		if bridge.app == nil || bridge.app.SaveMorningBriefWeekIntent(briefID, input.Answer) != nil {
			mobileJSON(w, http.StatusConflict, nil, "week answer unavailable")
			return
		}
		mobileJSON(w, http.StatusOK, struct {
			Saved bool `json:"saved"`
		}{true}, "")
	})
}

// Do not put a long verbatim commitment in the question while a checked
// short name is unavailable. The question itself is still useful alone.
func morningBriefRecommendationUsable(item MobileBriefItem) bool {
	if item.Kind != "commitment" {
		return true
	}
	title := strings.TrimSpace(item.Title)
	return utf8.RuneCountInString(title) <= 48 && len(strings.Fields(title)) <= 8
}

// storedBriefForWrite is the stored brief a write path checks. Writes read the
// stored brief, not the display copy, which can be older than a choice just made.
func (a *App) storedBriefForWrite(svc mobileBriefSource, briefID string) (MobileBriefV1, bool) {
	if service, ok := svc.(*mobileBriefService); ok && service != nil {
		return service.storedSource(briefID)
	}
	return svc.GetMobileBrief(briefID)
}
