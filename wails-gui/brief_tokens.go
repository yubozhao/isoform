package main

import (
	"context"
	"log"
	"os"
	"strings"
	"time"

	briefcore "wails-gui/internal/brief"
)

// Rendering the writer's day and time tokens (docs/brief-writer-tokens.md).
// The model writes {when:eN}; the app puts in the cited item's day, relative
// to the reader's day, and its clock time when the item has one. The words
// come from the brief's clock (brief_clock.go) and nowhere else.

// mobileBriefTokenTimes maps each cited ref to its item's time: a meeting's
// start, otherwise a due time. allDay marks a time with no clock (a due date).
func mobileBriefTokenTimes(brief MobileBriefV1) (times map[string]time.Time, allDay map[string]bool) {
	times, allDay = map[string]time.Time{}, map[string]bool{}
	for ref, items := range mobileBriefItemsByRef(brief) {
		for _, item := range items {
			value, dated := item.At, false
			if value == "" && item.DueAt != "" {
				value, dated = item.DueAt, true
			}
			if at := mobileBriefParseTime(value); !at.IsZero() {
				if _, seen := times[ref]; !seen {
					times[ref], allDay[ref] = at, dated
				}
			}
		}
	}
	return times, allDay
}

// mobileBriefTimedRefs is the refs a token may name.
func mobileBriefTimedRefs(brief MobileBriefV1) map[string]bool {
	times, _ := mobileBriefTokenTimes(brief)
	timed := make(map[string]bool, len(times))
	for ref := range times {
		timed[ref] = true
	}
	return timed
}

// mobileBriefTokensEnabled is the switch for the writer's tokens: on unless
// ISOFORM_BRIEF_TOKENS is "off" (the rollback to the old day words).
func mobileBriefTokensEnabled() bool {
	return !strings.EqualFold(strings.TrimSpace(os.Getenv("ISOFORM_BRIEF_TOKENS")), "off")
}

// newBriefTemplateRefs turns a writer template's material ids into the brief's
// evidence refs. A template with a token whose id got no ref is dropped, and
// its paragraph keeps the old path.
func newBriefTemplateRefs(template string, refIDs map[string]string) string {
	if template == "" {
		return ""
	}
	byID := make(map[string]string, len(refIDs))
	for ref, id := range refIDs {
		byID[id] = ref
	}
	ok := true
	out := briefcore.ReplaceTokens(template, func(id string) string {
		ref, found := byID[id]
		if !found {
			ok = false
			return ""
		}
		return "{when:" + ref + "}"
	})
	if !ok {
		return ""
	}
	return out
}

// mobileBriefTemplateWords is brief with each tokenized paragraph's text set to
// its template, for what must not change with the reader's day (the
// recording's binding).
func mobileBriefTemplateWords(brief MobileBriefV1) MobileBriefV1 {
	changed := false
	for _, para := range brief.Narrative {
		changed = changed || para.Template != ""
	}
	if !changed {
		return brief
	}
	brief.Narrative = append([]MobileBriefPara(nil), brief.Narrative...)
	for i := range brief.Narrative {
		if brief.Narrative[i].Template != "" {
			brief.Narrative[i].Text = brief.Narrative[i].Template
		}
	}
	return brief
}

// mobileBriefRenderTokens renders each tokenized paragraph for a reader at now,
// in the brief's zone and the given display language: its text becomes its
// template with every token put in. A token whose item has no time is left
// out. Paragraphs without a template are unchanged.
func mobileBriefRenderTokens(brief MobileBriefV1, now time.Time, language string) MobileBriefV1 {
	times, allDay := mobileBriefTokenTimes(brief)
	loc := mobileBriefNarrativeLocation(brief)
	lang := mobileBriefVoiceLang(language)
	render := func(ref string) string {
		at, ok := times[ref]
		if !ok {
			return ""
		}
		day := mobileBriefDayWords(at, now, loc, lang)
		if allDay[ref] {
			return day
		}
		local := at.In(loc)
		clock := briefShortClock(local.Hour(), local.Minute(), language)
		if briefChinese(language) {
			return day + clock
		}
		return day + " at " + clock
	}
	brief.Narrative = append([]MobileBriefPara(nil), brief.Narrative...)
	for i := range brief.Narrative {
		if template := brief.Narrative[i].Template; template != "" {
			brief.Narrative[i].Text = briefcore.ReplaceTokens(template, render)
		}
	}
	return brief
}

// mobileBriefReadNow is the reader's now: the brief service's clock when there
// is one, so a test's clock and the app's agree.
func (a *App) mobileBriefReadNow() time.Time {
	if a != nil {
		if service, ok := a.mobileBriefSource().(*mobileBriefService); ok && service != nil {
			return service.now()
		}
	}
	return time.Now()
}

// mobileBriefTokensShadowUntil ends the side-by-side check of the writer's
// tokens against the old writer (docs/brief-writer-tokens.md): until then each
// brief written with tokens is written once more the old way, in the
// background, and only counts are logged. Nothing of the old draft is stored
// or sent.
var mobileBriefTokensShadowUntil = time.Date(2026, 11, 23, 0, 0, 0, 0, time.Local)

func (s *mobileBriefService) startTokensShadow(material briefcore.Material, lang string, draft briefcore.Draft, codes []string, ask newBriefAsk) {
	if s == nil || ask == nil || !time.Now().Before(mobileBriefTokensShadowUntil) || !s.beginWork() {
		return
	}
	old := material
	old.Tokens = false
	go func() {
		defer s.wg.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		raw, err := ask(ctx, briefcore.WritePrompt(old, lang))
		if err != nil {
			log.Printf("[brief-tokens-shadow] old writer call failed")
			return
		}
		oldDraft, oldCodes, err := briefcore.RepairDraft(raw, old)
		if err != nil {
			log.Printf("[brief-tokens-shadow] old draft unusable")
			return
		}
		log.Printf("[brief-tokens-shadow] %s", briefcore.TokensShadowLine(draft, oldDraft, codes, oldCodes))
	}()
}
