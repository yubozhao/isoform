package main

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

	briefcore "wails-gui/internal/brief"
)

// mobileBriefShadowTimeout bounds one shadow run: its gather and two model calls.
const mobileBriefShadowTimeout = 10 * time.Minute

// mobileBriefShadowDailyCalls caps the brief model calls the shadow may make in
// one local day. Each shadow run makes two calls, so this allows a few runs a
// day without taking over the model.
const mobileBriefShadowDailyCalls = 8

// mobileBriefModelBudget counts the brief model calls the shadow makes, per
// local day. The shadow's calls count against it, and the shadow is skipped
// once the day's cap is spent. The old pipeline's calls are not charged yet.
// The zero value is ready to use.
type mobileBriefModelBudget struct {
	mu    sync.Mutex
	day   string
	calls int
}

func (b *mobileBriefModelBudget) charge(now time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.rollLocked(now)
	b.calls++
}

// low reports whether the day's calls have reached the cap.
func (b *mobileBriefModelBudget) low(now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.rollLocked(now)
	return b.calls >= mobileBriefShadowDailyCalls
}

func (b *mobileBriefModelBudget) rollLocked(now time.Time) {
	day := now.Format("2006-01-02")
	if day != b.day {
		b.day, b.calls = day, 0
	}
}

// mobileBriefShadowDeps adapts the app to the shadow package. Each method wraps
// an existing app function; nothing new reads a source.
type mobileBriefShadowDeps struct {
	app    *App
	budget *mobileBriefModelBudget
	now    func() time.Time
}

func (d *mobileBriefShadowDeps) clock() time.Time {
	if d.now != nil {
		return d.now()
	}
	return time.Now()
}

// Snapshot runs the existing gather once, with the same source fence and the
// same stated-exclusion filter the old builder uses, and maps the result to the
// shadow's items. A source that changes during the read fails the snapshot.
func (d *mobileBriefShadowDeps) Snapshot(ctx context.Context, w briefcore.Window) (briefcore.Inputs, error) {
	app := d.app
	if app == nil {
		return briefcore.Inputs{}, errors.New("shadow has no app")
	}
	var gathered *mobileBriefGatherRevision
	if app.store != nil {
		rev, err := app.readMobileBriefGatherRevision(ctx)
		if err != nil {
			return briefcore.Inputs{}, errors.New("source fence unavailable")
		}
		gathered = rev
		defer gathered.close()
	}
	gatherCtx := context.WithValue(ctx, mobileBriefGatherContextKey{}, true)
	if gathered != nil && gathered.memory != nil {
		gatherCtx = context.WithValue(gatherCtx, mobileBriefPinnedMemoryKey{}, gathered.memory)
	}
	privacy := app.briefPrivacyPrint()
	in, err := collectMobileBriefInput(gatherCtx, app, w.Slot, w.Start, w.End)
	if err != nil {
		return briefcore.Inputs{}, errors.New("collect failed")
	}
	in = mobileBriefWithoutExcluded(in, app.mobileBriefExclusions())
	if gathered != nil {
		if changed := app.mobileBriefGatherChange(ctx, gathered); changed != "" {
			return briefcore.Inputs{}, errors.New("sources changed during gather")
		}
	}
	if app.briefPrivacyPrint() != privacy {
		return briefcore.Inputs{}, errors.New("privacy changed during gather")
	}
	return briefcore.Inputs{
		Snapshot: briefcore.Snapshot{Window: w, TakenAt: d.clock(), Items: mobileBriefShadowItems(in)},
		Language: d.Language(),
		Print:    privacy,
	}, nil
}

// mobileBriefShadowItems maps the gathered sections to the shadow's items. Each
// list goes through the same candidate gate and order the old builder uses, so
// protected, sensitive and untitled records are dropped here, once.
func mobileBriefShadowItems(in mobileBriefInput) []briefcore.Item {
	var out []briefcore.Item
	add := func(section string, items []mobileBriefCandidate) {
		for _, c := range items {
			out = append(out, briefcore.Item{
				Section:    section,
				SourceKind: c.SourceKind,
				SourceID:   c.SourceID,
				Title:      c.Title,
				Detail:     c.Detail,
				Status:     c.Status,
				Kind:       c.Kind,
				At:         c.At,
			})
		}
	}
	add(briefcore.SectionWaitingOnYou, prepareMobileBriefPurposeCandidates(in.WaitingOnYou, mobileBriefOldestTouchFirst))
	add(briefcore.SectionWorkingToward, prepareMobileBriefPurposeCandidates(in.WorkingToward, mobileBriefPurposeFirst))
	add(briefcore.SectionCommitments, prepareMobileBriefPurposeCandidates(in.Commitments, mobileBriefPurposeFirst))
	add(briefcore.SectionMoved, prepareMobileBriefCandidates(in.Moved, mobileBriefReviewFirst))
	add(briefcore.SectionDoing, prepareMobileBriefCandidates(in.Doing, mobileBriefNewestFirst))
	add(briefcore.SectionWatching, prepareMobileBriefCandidates(in.Watching, mobileBriefNewestFirst))
	add(briefcore.SectionMeetings, prepareMobileBriefCandidates(in.Meetings, mobileBriefUpcomingFirst))
	add(briefcore.SectionPeople, prepareMobileBriefCandidates(in.People, mobileBriefNewestFirst))
	add(briefcore.SectionOpenLoops, prepareMobileBriefCandidates(in.OpenLoops, mobileBriefOpenLoopFirst))
	add(briefcore.SectionLearned, prepareMobileBriefCandidates(in.Learned, mobileBriefNewestFirst))
	add(briefcore.SectionDeliverables, prepareMobileBriefCandidates(in.Deliverables, mobileBriefNewestFirst))
	add(briefcore.SectionSchedules, prepareMobileBriefCandidates(in.Schedules, mobileBriefNewestFirst))
	add(briefcore.SectionNoticed, prepareMobileBriefCandidates(in.Noticed, mobileBriefNewestFirst))
	return out
}

// Ask makes one brief model call and charges it to the day's budget. It uses
// the same route as the old narrative call.
func (d *mobileBriefShadowDeps) Ask(ctx context.Context, prompt string) (string, error) {
	if d.app == nil {
		return "", errors.New("shadow has no app")
	}
	d.budget.charge(d.clock())
	return d.app.mobileBriefModelCall(ctx, prompt)
}

// Speak is not part of this step: audio stays with the old pipeline.
func (d *mobileBriefShadowDeps) Speak(context.Context, string, string) ([]byte, error) {
	return nil, errors.New("shadow does not speak")
}

func (d *mobileBriefShadowDeps) ClosedEyes() bool {
	return d.app != nil && d.app.IsClosedEyesModeEnabled()
}

func (d *mobileBriefShadowDeps) Language() string {
	if d.app == nil {
		return "en"
	}
	return mobileBriefVoiceLang(d.app.currentUILanguageName())
}

// Emit is a no-op: the shadow announces nothing.
func (d *mobileBriefShadowDeps) Emit(string, any) {}

func (d *mobileBriefShadowDeps) BudgetLow() bool {
	return d.budget.low(d.clock())
}

// startBriefShadow runs the shadow for a scheduled brief in the background. It
// returns at once, so the old brief is never delayed or changed by it.
func (s *mobileBriefService) startBriefShadow(brief MobileBriefV1) {
	// With the new writer live (newBrief) the shadow has nothing to compare.
	if s == nil || s.app == nil || s.store == nil || s.store.dir == "" || !s.config.load().ShadowCompose || s.newBriefEnabled() {
		return
	}
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	s.wg.Add(1)
	if s.shadowCtx == nil {
		s.shadowCtx, s.shadowCancel = context.WithCancel(context.Background())
	}
	ctx := s.shadowCtx
	s.mu.Unlock()
	go func() {
		defer s.wg.Done()
		runCtx, cancel := context.WithTimeout(ctx, mobileBriefShadowTimeout)
		defer cancel()
		s.runBriefShadow(runCtx, brief)
	}()
}

// runBriefShadow builds the shadow copy for brief. Every outcome is logged as
// counts or a stage name only, and nothing it does touches the old brief.
func (s *mobileBriefService) runBriefShadow(ctx context.Context, brief MobileBriefV1) {
	window := briefcore.Window{
		Slot:      brief.Slot,
		LocalDate: brief.LocalDate,
		Timezone:  brief.Timezone,
		Start:     mobileBriefParseTime(brief.WindowStart),
		End:       mobileBriefParseTime(brief.AsOf),
	}
	old := briefcore.ShadowOld{
		Narrative: len(brief.Narrative),
		Cards:     len(brief.Cards),
		Evidence:  len(brief.Evidence),
		Sections:  map[string]int{},
	}
	counts := map[string]int{}
	for _, line := range mobileBriefSectionLines(brief.Sections) {
		counts[line.name] = len(line.items)
	}
	for _, name := range briefcore.ComposedSectionNames() {
		old.Sections[name] = counts[name]
	}
	deps := &mobileBriefShadowDeps{app: s.app, budget: &s.shadowBudget, now: s.now}
	_, err := briefcore.RunShadow(ctx, deps, s.store.dir, brief.BriefID, window, old)
	switch {
	case err == nil:
		// RunShadow logged its count line.
	case errors.Is(err, briefcore.ErrClosedEyes):
		log.Printf("[brief-shadow] id=%s skipped: closed eyes is on", brief.BriefID)
	case errors.Is(err, briefcore.ErrBudgetLow):
		log.Printf("[brief-shadow] id=%s skipped: model budget is low", brief.BriefID)
	default:
		// The error text can name a path or a store, so only the fact is logged.
		log.Printf("[brief-shadow] id=%s failed; the brief is unchanged", brief.BriefID)
	}
}
