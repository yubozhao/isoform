package main

import (
	"fmt"
	"log"
	"regexp"
	"strings"
	"unicode/utf8"
)

var morningBriefUnprovenSkip = regexp.MustCompile(`(?i)\b(skip|decline)\b|跳过|婉拒|不参加`)

// prepareRankedMorningDraft checks that the closing acts on the selected lead
// or slip, and uses the existing prepared-card validation and source gates.
func prepareRankedMorningDraft(brief MobileBriefV1, draft morningBriefDraft, selection morningBriefSelection, language string) (morningBriefDraft, error) {
	if selection.Lead == nil || len(draft.Paragraphs) < 1 {
		return morningBriefDraft{}, codeError("brief requires a closing question")
	}
	if err := mobileBriefRejectHoldOffer(draft.Paragraphs); err != nil {
		return morningBriefDraft{}, err
	}
	for _, paragraph := range draft.Paragraphs {
		if morningBriefUnprovenSkip.MatchString(paragraph.Text) {
			return morningBriefDraft{}, codeError("no selected item is proven skippable")
		}
	}
	// The closing question is optional: a draft whose closing does not hold is
	// kept without it rather than failing the brief.
	closing := len(draft.Paragraphs) >= 2
	last := draft.Paragraphs[len(draft.Paragraphs)-1]
	text := strings.TrimSpace(last.Text)
	if strings.Count(text, "?")+strings.Count(text, "？") != 1 || !strings.HasSuffix(text, "?") && !strings.HasSuffix(text, "？") {
		if strings.ContainsAny(text, "?？") {
			draft.Paragraphs = draft.Paragraphs[:len(draft.Paragraphs)-1]
		}
		closing = false
	}
	allowed := append([]string(nil), selection.Lead.Refs...)
	if selection.Slip != nil {
		allowed = append(allowed, selection.Slip.Refs...)
	}
	if closing {
		for _, ref := range last.Refs {
			if !containsString(allowed, ref) {
				draft.Paragraphs, closing = draft.Paragraphs[:len(draft.Paragraphs)-1], false
				break
			}
		}
	}
	if len(draft.Paragraphs) == 0 {
		return morningBriefDraft{}, codeError("brief requires a closing question")
	}
	brief.Narrative = draft.Paragraphs
	brief.Cards = nil
	var words []mobileBriefCardWords
	for i := range brief.Narrative {
		if chip := brief.Narrative[i].chipWords; chip != nil && i < len(brief.Narrative)-1 && !mobileCalendarHoldOffer.MatchString(chip.Label) && !mobileCalendarHoldOffer.MatchString(chip.Message) {
			words = append(words, mobileBriefCardWords{ID: mobileBriefQuestionID(brief, brief.Narrative[i], i, 0), Label: chip.Label, Message: chip.Message})
		}
	}
	digest := mobileBriefClosingDigest(brief)
	for i, answer := range draft.Answers {
		if digest == "" {
			break
		}
		answer.ID, answer.AnswerTo = fmt.Sprintf("answer:%s:%d", digest, i), digest
		words = append(words, answer)
	}
	for _, card := range mobileBriefApplyCardWords(brief, mobileBriefPatternCards(brief, language), words, language) {
		// The writer may decline a chip. Never replace a rejected chip with a
		// template label that repeats a raw task title.
		if card.Kind == mobileBriefCardQuestion && card.Writer != mobileBriefCardWriterModel {
			continue
		}
		if card.Kind == mobileBriefCardQuestion {
			if subject, ok := mobileBriefCardSubject(brief, card.Refs); ok && morningBriefRawTaskLabel(card.Label, mobileBriefCardTitle(subject)) {
				continue
			}
		}
		draft.Cards = append(draft.Cards, card)
	}
	if closing && len(mobileBriefClosingAnswers(brief, draft.Cards)) != 2 {
		// No two grounded answers: the closing question goes, with any answer
		// chips, and the rest of the brief stands.
		closing = false
		draft.Paragraphs = draft.Paragraphs[:len(draft.Paragraphs)-1]
	}
	if !closing {
		brief.Narrative = draft.Paragraphs
		var kept []MobileBriefCard
		for _, card := range draft.Cards {
			if card.AnswerTo == "" {
				kept = append(kept, card)
			}
		}
		draft.Cards = kept
		if len(draft.Paragraphs) == 0 {
			return morningBriefDraft{}, codeError("closing requires two grounded answers")
		}
		log.Printf("[mobile-brief] ranked draft kept without its closing question")
	}
	for i := range draft.Paragraphs {
		draft.Paragraphs[i].chipWords = nil
	}
	return draft, nil
}

func morningBriefRawTaskLabel(label, title string) bool {
	label, title = strings.ToLower(strings.TrimSpace(label)), strings.ToLower(strings.TrimSpace(title))
	if title == "" || !strings.Contains(label, title) {
		return false
	}
	words := strings.Fields(title)
	if len(words) > 1 && mobileBriefCardLabelVerbs[words[0]] || len(words) >= 3 || utf8.RuneCountInString(title) > 18 {
		return true
	}
	for _, prefix := range []string{"支付", "确保", "准备", "完成", "发送", "安排", "查看"} {
		if strings.HasPrefix(title, prefix) {
			return true
		}
	}
	return false
}
