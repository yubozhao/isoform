package main

import (
	"context"
	"time"
)

// Old week answers lack an origin marker. Project one only when the current
// explicit answer has exactly one matching legacy main-chat row in the week.
// This is display-only; it never rewrites the row or grants authority.
func (a *App) projectLegacyWeekAnswer(ctx context.Context, items []StreamItemData) {
	if a == nil || a.store == nil {
		return
	}
	candidate := false
	for _, item := range items {
		if item.Kind == "user" && item.AutomationContext == "" {
			candidate = true
			break
		}
	}
	if !candidate {
		return
	}
	svc := a.ensureMobileBriefService()
	if svc == nil {
		return
	}
	now := time.Now()
	loc := time.Local
	if service, ok := svc.(*mobileBriefService); ok {
		now = service.now()
		if service.location != nil {
			loc = service.location
		}
	}
	if now.IsZero() {
		return
	}
	start, err := time.ParseInLocation("2006-01-02", morningBriefWeekStart(now, loc), loc)
	if err != nil {
		return
	}
	answer, err := a.store.GetStreamPageUIState(ctx, "morning_brief_week_intent:"+morningBriefWeekStart(now, loc))
	if err != nil || answer == "" {
		return
	}
	matching := false
	for _, item := range items {
		if item.Kind == "user" && item.AutomationContext == "" && item.Content == answer {
			matching = true
			break
		}
	}
	if !matching {
		return
	}
	brief, ok := svc.GetMobileBrief(start.Format("2006-01-02") + "-morning")
	if !ok || !a.morningBriefWeekEligible(brief, now) {
		return
	}
	id, err := a.store.UniqueLegacyMainAnswer(ctx, answer, mobileBriefAsOfTime(brief).UnixMilli(), start.AddDate(0, 0, 7).UnixMilli())
	if err != nil || id == "" {
		return
	}
	for i := range items {
		if items[i].ID == id && items[i].AutomationContext == "" {
			items[i].WeekAnswerBriefID = brief.BriefID
		}
	}
}
