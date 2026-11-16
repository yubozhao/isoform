package main

import (
	"regexp"
	"strings"
	"time"
	"unicode"
)

var briefRelativeDay = regexp.MustCompile(`(?i)\b(?:today|tomorrow|yesterday)\b|今天|明天|昨天`)

// A stored brief's relative days refer to the day it was made. Project them
// onto the reader's day without changing the stored record.
func mobileBriefRelativeDaysForRead(brief MobileBriefV1, now time.Time, language string) MobileBriefV1 {
	return mobileBriefRelativeDaysAtRead(brief, now, briefWordsDay(brief), mobileBriefNarrativeLocation(brief), language)
}

func mobileBriefRelativeDaysAtRead(brief MobileBriefV1, now, base time.Time, loc *time.Location, language string) MobileBriefV1 {
	if base.IsZero() || now.IsZero() {
		return brief
	}
	if loc == nil {
		loc = time.Local
	}
	rewrite := func(text string, itemDate time.Time) string {
		return briefRelativeDay.ReplaceAllStringFunc(text, func(word string) string {
			offset := 0
			switch strings.ToLower(word) {
			case "tomorrow", "明天":
				offset = 1
			case "yesterday", "昨天":
				offset = -1
			}
			target := base.In(loc).AddDate(0, 0, offset)
			// The cited item's own date words the day only when it is the day
			// the word names, give or take the one day a brief made ahead
			// shifts. A habit due on November 5 that the writer says slipped
			// "yesterday" keeps yesterday: the word is not about its due date
			// (brief of Nov 14, 2026, read "Do a set November 5").
			if !itemDate.IsZero() {
				if days := mobileBriefCivilDays(itemDate, target, loc); days >= -1 && days <= 1 {
					target = itemDate
				}
			}
			result := mobileBriefDayWords(target, now, loc, mobileBriefVoiceLang(language))
			if len(word) > 0 && word[0] >= 'A' && word[0] <= 'Z' {
				runes := []rune(result)
				if len(runes) > 0 {
					runes[0] = unicode.ToUpper(runes[0])
					result = string(runes)
				}
			}
			return result
		})
	}
	valid := make([]bool, len(brief.Cards))
	for i, card := range brief.Cards {
		valid[i] = mobileBriefAnswerMatches(brief, card)
	}
	changed := false
	items := mobileBriefItemsByRef(brief)
	evidence := mobileBriefEvidenceByRef(brief)
	itemDate := func(para MobileBriefPara) time.Time {
		if len(para.Refs) != 1 || len(briefRelativeDay.FindAllString(para.Text, -1)) != 1 {
			return time.Time{}
		}
		var found time.Time
		for _, item := range items[para.Refs[0]] {
			value := ""
			if evidence[para.Refs[0]].SourceKind == mobileBriefCalendarSource || item.Kind == "meeting" {
				value = item.At
			}
			if item.DueAt != "" && (value == "" || strings.Contains(strings.ToLower(para.Text), "due") || strings.Contains(para.Text, "到期")) {
				value = item.DueAt
			}
			at := mobileBriefParseTime(value)
			if at.IsZero() {
				continue
			}
			if !found.IsZero() && mobileBriefCivilDays(at, found, loc) != 0 {
				return time.Time{}
			}
			found = at
		}
		return found
	}
	brief.Narrative = append([]MobileBriefPara(nil), brief.Narrative...)
	for i := range brief.Narrative {
		if brief.Narrative[i].Template != "" {
			// Rendered from its tokens for this reader (brief_tokens.go).
			continue
		}
		old := brief.Narrative[i].Text
		brief.Narrative[i].Text = rewrite(old, itemDate(brief.Narrative[i]))
		changed = changed || old != brief.Narrative[i].Text
	}
	brief.WeeklyRecap = append([]MobileBriefPara(nil), brief.WeeklyRecap...)
	for i := range brief.WeeklyRecap {
		old := brief.WeeklyRecap[i].Text
		brief.WeeklyRecap[i].Text = rewrite(old, itemDate(brief.WeeklyRecap[i]))
		changed = changed || old != brief.WeeklyRecap[i].Text
	}
	brief.Cards = append([]MobileBriefCard(nil), brief.Cards...)
	for i := range brief.Cards {
		card := &brief.Cards[i]
		date := itemDate(MobileBriefPara{Refs: card.Refs, Text: card.Message})
		card.Label, card.Message = rewrite(card.Label, date), rewrite(card.Message, date)
		if valid[i] {
			card.AnswerTo = mobileBriefClosingDigest(brief)
			card.Facts = card.AnswerTo
		}
	}
	brief.Offers = append([]MobileBriefOffer(nil), brief.Offers...)
	for i := range brief.Offers {
		offer := &brief.Offers[i]
		date := itemDate(MobileBriefPara{Refs: offer.Refs, Text: offer.Text})
		offer.Label, offer.Prompt, offer.Text = rewrite(offer.Label, date), rewrite(offer.Prompt, date), rewrite(offer.Text, date)
	}
	if changed {
		brief.Audio = mobileBriefWithoutStaleAudio(brief.Audio)
	}
	return brief
}
