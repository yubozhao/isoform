package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"wails-gui/internal/pii"
)

// MobileBriefCard is one prepared click on the brief (backlog item 56): a
// button, a row's question, or what Isoform says first when the user opens
// "Talk about this brief". Cards are made with the brief, saved in the same
// file so Today and the phone show the same thing, and checked again at every
// click by the one send route (resolveTodayChatBriefCard). No client builds
// their text.
type MobileBriefCard struct {
	// Paragraph is the zero-based narrative index for a question, including 0.
	Paragraph *int   `json:"paragraph,omitempty"`
	ID        string `json:"id"`
	// Kind is "action" (a button: a verb and the thing), "item" (the question
	// a row's "Chat about this" asks), "question" (a paragraph's prepared
	// question), or "opener".
	Kind string `json:"kind"`
	// Mode is "send" (the message is the user's and Isoform answers at once)
	// or "speak_first" (the message is Isoform's own; the user answers it),
	// or "open_activity" (navigation only, with an editable Activity target).
	Mode   string                     `json:"mode"`
	Target *MobileBriefActivityTarget `json:"target,omitempty"`
	// Label is an action button's or paragraph question chip's display text.
	Label string `json:"label,omitempty"`
	// Message is the exact chat message.
	Message string `json:"message"`
	// Refs are the card's sources in this brief's evidence.
	Refs []string `json:"refs"`
	// ItemKey names the row an item card belongs to: the item's key, or its
	// id in briefs without keys.
	ItemKey string `json:"itemKey,omitempty"`
	// Facts digests what the message was written from. A card whose facts
	// changed since is written again from the current facts.
	Facts string `json:"facts,omitempty"`
	// AnswerTo binds a closing answer to the exact question and its facts.
	// Empty on ordinary action cards; they must never be shown as answers.
	AnswerTo string `json:"answerTo,omitempty"`
	// Writer is "model" when the brief's model wrote the words in the same
	// pass as the brief, and "pattern" for Isoform's fixed sentences.
	Writer string `json:"writer,omitempty"`
}

const (
	mobileBriefCardQuestion     = "question"
	mobileBriefCardAction       = "action"
	mobileBriefCardItem         = "item"
	mobileBriefCardOpener       = "opener"
	mobileBriefCardOpenerAnswer = "opener_answer"

	mobileBriefCardOpenActivity = "open_activity"
	mobileBriefCardSend         = "send"
	mobileBriefCardSpeakFirst   = "speak_first"

	mobileBriefCardWriterModel   = "model"
	mobileBriefCardWriterPattern = "pattern"
	// Isoform's own answers under a closing question it rewrote or wrote
	// (today-review v1.1.8 #3): one per offer, then "Not now".
	mobileBriefCardWriterClosing = "closing"

	mobileBriefOpenerCardID = "opener"
	// Bounds for model-written words; Isoform's own sentences stay within them.
	mobileBriefCardLabelRunes    = mobileBriefOfferLabelRunes
	mobileBriefCardMessageBytes  = 1200
	mobileBriefCardQuestionBytes = 400
)

// mobileBriefCardRow is a row Today offers "Chat about this" on.
type mobileBriefCardRow struct {
	section string
	item    MobileBriefItem
}

// mobileBriefCardRows lists the rows that get an item card, in Today's order.
// Rows Today does not show (what you looked at, learned, schedules that ran)
// and rows without sources get none.
func mobileBriefCardRows(brief MobileBriefV1) []mobileBriefCardRow {
	var rows []mobileBriefCardRow
	add := func(section string, items []MobileBriefItem) {
		for _, item := range items {
			if item.EvidenceUnavailable || len(item.Refs) == 0 || item.Kind == "find" || item.Kind == "day_shape" {
				continue
			}
			rows = append(rows, mobileBriefCardRow{section: section, item: item})
		}
	}
	s := brief.Sections
	add("upcoming", brief.Upcoming)
	add(mobileBriefSectionMeetings, s.Meetings)
	add(mobileBriefSectionWorkingToward, s.WorkingToward)
	add(mobileBriefSectionCommitments, s.Commitments)
	add(mobileBriefSectionWaitingOnYou, s.WaitingOnYou)
	add(mobileBriefSectionMoved, s.Moved)
	add(mobileBriefSectionOpenLoops, s.OpenLoops)
	add(mobileBriefSectionPeople, s.People)
	add(mobileBriefSectionDeliverables, s.Deliverables)
	return rows
}

// mobileBriefCardItemKey is the row identity an item card is filed under.
func mobileBriefCardItemKey(item MobileBriefItem) string {
	if key := strings.TrimSpace(item.Key); key != "" {
		return key
	}
	return item.ID
}

// mobileBriefCardFacts digests the facts a card's words come from: the
// subject as delivered now, plus what the card does.
func mobileBriefCardFacts(brief MobileBriefV1, item MobileBriefItem, extra string) string {
	evidence := mobileBriefEvidenceByRef(brief)
	var sources []string
	for _, id := range item.Refs {
		ref := evidence[id]
		sources = append(sources, ref.SourceKind+"\x00"+ref.SourceID)
	}
	// Since is filled in when the brief is read (the time since the item was
	// seen), so it is display-time text and is not part of the facts.
	var protected []string
	ownVoice := func(value string) string {
		if !brief.ownVoiceFacts || !mobileBriefDeviceName.MatchString(value) {
			return value
		}
		if protected == nil {
			protected = mobileBriefQuotedContent(brief)
		}
		return mobileBriefOwnVoiceText(value, protected...)
	}
	raw, _ := json.Marshal(struct {
		Title, Status, Kind, DueAt, At, Reason, NextStep, Extra string
		Rank                                                    int
		Sources                                                 []string
		Day                                                     string
	}{ownVoice(item.Title), item.Status, item.Kind, item.DueAt, item.At, ownVoice(item.AttentionReason), ownVoice(item.NextStep), ownVoice(extra),
		item.AttentionRank, sources, brief.LocalDate})
	return agencyPayloadHash(raw)
}

// mobileBriefCardTitle is how a card names its subject, or "" when the item
// cannot be named plainly.
func mobileBriefCardTitle(item MobileBriefItem) string {
	title := item.Title
	if item.Outcome != nil || item.Kind == mobileBriefThreadOutcomeSource {
		title = redactThreadOutcomeText(title)
	}
	title = strings.TrimRight(mobileBriefText(mobileBriefVoiceLabel(title), 140), " .。")
	if title == "" || mobileBriefBannedOutcomePhrase(title) || threadOutcomeTypingNarration.MatchString(title) {
		return ""
	}
	return title
}

// mobileBriefItemQuestion is a row's question in the user's own words, from
// Isoform's fixed sentences: it names the item and its due day or why it is
// on Today ("Where do I stand on “Pay the Citi card”, due Friday, and what's
// next?").
func mobileBriefItemQuestion(brief MobileBriefV1, section string, item MobileBriefItem, lang string) string {
	title := mobileBriefCardTitle(item)
	if title == "" {
		return ""
	}
	reference, loc := mobileBriefTextReference(brief), mobileBriefNarrativeLocation(brief)
	q := "“" + title + "”"
	since := mobileBriefSinceWords(mobileBriefParseTime(item.Since), reference, loc, lang)
	due := mobileBriefParseTime(item.DueAt)
	switch true {
	case item.Kind == "waiting_on_reply":
		if since != "" {
			return pick(lang, title+" has been waiting on my reply "+since+". What do they need, and how should I reply?",
				title+since+"一直在等我回复。对方需要什么，我该怎么回？")
		}
		return pick(lang, title+" is waiting on my reply. What do they need, and how should I reply?",
			title+"在等我回复。对方需要什么，我该怎么回？")
	case item.Status == "no_recent_touch":
		return pick(lang, "When did I last talk to "+title+", and what should I follow up on?",
			"我上次和"+title+"联系是什么时候？我该跟进什么？")
	case item.Kind == "one_time_work" && item.Status == "blocked":
		return pick(lang, q+" is blocked and waiting for me. What do you need from me to continue?",
			q+"已暂停，在等我处理。要继续下去，你需要我做什么？")
	case item.Kind == "one_time_work" && item.Status == "needs_review":
		return pick(lang, q+" is ready for my review. What was done, and what needs my decision?",
			q+"等我审阅。做了什么，哪些需要我决定？")
	case !due.IsZero():
		day := mobileBriefDayWords(due, reference, loc, lang)
		if mobileBriefCivilDays(due, reference, loc) < 0 {
			return pick(lang, "Where do I stand on "+q+", which was due "+day+", and what's next?",
				q+"（"+day+"已到期）目前进展如何，下一步是什么？")
		}
		return pick(lang, "Where do I stand on "+q+", due "+day+", and what's next?",
			q+"（"+day+"到期）目前进展如何，下一步是什么？")
	case item.Outcome != nil || item.Kind == mobileBriefThreadOutcomeSource:
		// An observed thread's title is already a sentence about where it
		// stopped ("The sailing gloves are still in the cart").
		said := title + pick(lang, ".", "。")
		state := ""
		if item.Outcome != nil {
			state = item.Outcome.State
		}
		switch state {
		case "cart_left":
			return said + pick(lang, " What's left before I order?", "下单前还差什么？")
		case "checkout_started":
			return said + pick(lang, " What's left to finish the order?", "完成订单还差什么？")
		case "draft":
			return said + pick(lang, " What's left to finish it?", "要完成它还差什么？")
		}
		return said + pick(lang, " What should I do next?", "接下来我该做什么？")
	case mobileBriefFutureCalendarItem(brief, item):
		when := mobileBriefWhen(item, reference, loc, lang)
		if when == "" {
			return pick(lang, "Help me prepare for "+q+".", "帮我准备"+q+"。")
		}
		return pick(lang, "Help me prepare for "+q+" "+when+".", "帮我准备"+when+"的"+q+"。")
	case section == "upcoming":
		return pick(lang, "What will "+q+" do when it runs next, and does it need anything from me?",
			q+"下次运行时会做什么？需要我做什么吗？")
	case section == mobileBriefSectionMoved:
		return pick(lang, "What's left on "+q+", and what should I do next?", q+"还剩什么？接下来我该做什么？")
	case section == mobileBriefSectionDeliverables:
		return pick(lang, "Walk me through "+q+" and what's left to do.", "带我看看"+q+"，以及还剩什么要做。")
	case section == mobileBriefSectionPeople:
		return pick(lang, "What should I follow up on with "+title+"?", "我该和"+title+"跟进什么？")
	case section == mobileBriefSectionMeetings:
		return pick(lang, "What should I follow up on from "+q+"?", q+"之后我该跟进什么？")
	}
	if since != "" {
		return pick(lang, "Where do I stand on "+q+", open "+since+", and what's next?",
			q+since+"一直未结，目前进展如何，下一步是什么？")
	}
	return pick(lang, "Where do I stand on "+q+", and what's next?", q+"目前进展如何，下一步是什么？")
}

// mobileBriefActionLabel turns an offer's chip into a button: a verb and the
// thing ("Plan: Discovery" becomes "Plan Discovery").
func mobileBriefActionLabel(label string) string {
	label = strings.TrimSpace(label)
	if rest, ok := strings.CutPrefix(label, "Plan: "); ok {
		return "Plan " + rest
	}
	if rest, ok := strings.CutPrefix(label, "规划："); ok {
		return "规划" + rest
	}
	return label
}

// mobileBriefCardSubjectSection finds the row an offer is about: the item
// whose primary ref is the offer's first ref.
func mobileBriefCardSubjectSection(brief MobileBriefV1, refs []string) (MobileBriefItem, string, bool) {
	if len(refs) == 0 {
		return MobileBriefItem{}, "", false
	}
	var projectedItem MobileBriefItem
	var projectedSection string
	projected := false
	for _, line := range append(mobileBriefSectionLines(brief.Sections), mobileBriefSectionLine{"upcoming", brief.Upcoming}) {
		for _, item := range line.items {
			if len(item.Refs) == 0 || item.Refs[0] != refs[0] || item.EvidenceUnavailable {
				continue
			}
			switch line.name {
			case mobileBriefSectionDecide, mobileBriefSectionReview, mobileBriefSectionPlan,
				mobileBriefSectionDayAhead, mobileBriefSectionHighlights:
				if !projected {
					projectedItem, projectedSection, projected = item, line.name, true
				}
			default:
				return item, line.name, true
			}
		}
	}
	if projected {
		return projectedItem, projectedSection, true
	}
	return MobileBriefItem{}, "", false
}

func mobileBriefCardSubject(brief MobileBriefV1, refs []string) (MobileBriefItem, bool) {
	if len(refs) == 0 {
		return MobileBriefItem{}, false
	}
	for _, line := range append(mobileBriefSectionLines(brief.Sections), mobileBriefSectionLine{"upcoming", brief.Upcoming}) {
		for _, item := range line.items {
			if len(item.Refs) > 0 && item.Refs[0] == refs[0] && !item.EvidenceUnavailable {
				return item, true
			}
		}
	}
	return MobileBriefItem{}, false
}

// mobileBriefAttentionItems is "What needs your attention" as Today lists it:
// anchored items with a reason, by tier then age.
func mobileBriefAttentionItems(brief MobileBriefV1) []MobileBriefItem {
	evidence := mobileBriefEvidenceByRef(brief)
	var out []MobileBriefItem
	seen := map[string]bool{}
	for _, row := range mobileBriefCardRows(brief) {
		item := row.item
		if strings.TrimSpace(item.AttentionReason) == "" || mobileBriefItemAnchor(item, evidence) == "" {
			continue
		}
		ref := evidence[item.Refs[0]]
		identity := ref.SourceKind + "\x00" + ref.SourceID
		if seen[identity] {
			continue
		}
		seen[identity] = true
		out = append(out, item)
	}
	rank := func(item MobileBriefItem) int {
		if item.AttentionRank > 0 {
			return item.AttentionRank
		}
		return 1 << 20
	}
	since := func(item MobileBriefItem) int64 {
		if at := mobileBriefParseTime(item.Since); !at.IsZero() {
			return at.UnixMilli()
		}
		return 1 << 62
	}
	sort.SliceStable(out, func(i, j int) bool {
		if rank(out[i]) != rank(out[j]) {
			return rank(out[i]) < rank(out[j])
		}
		return since(out[i]) < since(out[j])
	})
	return out
}

// mobileBriefOpenerMessage is what Isoform says first when the user opens the
// brief: what to start with and why, then one question. A written brief (or
// one being written) starts from its own top pick, or the first thing on its
// day, so the opener never raises an item the text left out (backlog item
// 65); the template starts from what needs attention, as its text does.
func mobileBriefOpenerMessage(brief MobileBriefV1, lang string) (string, MobileBriefItem, bool) {
	reference, loc := mobileBriefTextReference(brief), mobileBriefNarrativeLocation(brief)
	if brief.NarrativeStatus == "" || brief.NarrativeStatus == "ok" {
		var start MobileBriefItem
		reason := ""
		if picks := mobileBriefPicks(brief); len(picks) > 0 {
			if picks[0].Tier != mobileBriefPickNoticed {
				start, reason = picks[0].Item, mobileBriefPickReason(brief, picks[0], lang)
			}
		} else if day := mobileBriefDayItems(brief); len(day) > 0 {
			start, reason = day[0], mobileBriefPickReason(brief, mobileBriefPick{Tier: mobileBriefPickMeeting, Item: day[0]}, lang)
		}
		if title := mobileBriefCardTitle(start); title != "" && reason != "" {
			return pick(lang, "I'd start with “"+title+"”: "+reason+". Want to begin there?",
				"我建议先从“"+title+"”开始："+reason+"。要从这里开始吗？"), start, true
		}
	}
	for _, item := range mobileBriefAttentionItems(brief) {
		title := mobileBriefCardTitle(item)
		if title == "" {
			continue
		}
		q := "“" + title + "”"
		if due := mobileBriefParseTime(item.DueAt); !due.IsZero() {
			return pick(lang, "I'd start with "+q+": it "+mobileBriefDueWords(due, reference, loc, lang)+". Want to begin there?",
				"我建议先从"+q+"开始："+mobileBriefDueWords(due, reference, loc, lang)+"。要从这里开始吗？"), item, true
		}
		reason := strings.TrimRight(strings.TrimSpace(item.AttentionReason), " .。")
		return pick(lang, "I'd start with "+q+": "+mobileBriefLowerFirst(reason)+". Want to begin there?",
			"我建议先从"+q+"开始："+reason+"。要从这里开始吗？"), item, true
	}
	for _, offer := range brief.Offers {
		if offer.Action == mobileBriefCardOpenActivity {
			continue
		}
		if item, ok := mobileBriefCardSubject(brief, offer.Refs); ok {
			if title := mobileBriefCardTitle(item); title != "" {
				return pick(lang, "I'd start with “"+title+"”. Want to begin there?", "我建议先从“"+title+"”开始。要从这里开始吗？"), item, true
			}
		}
	}
	return "", MobileBriefItem{}, false
}

// mobileBriefPatternCards writes every card of a delivered brief from
// Isoform's fixed sentences: the buttons (at most two, from the brief's
// offers), a question for every row Today shows, and the opener.
func mobileBriefPatternCards(brief MobileBriefV1, language string) []MobileBriefCard {
	if !featureOn(featureBriefExtras) {
		return nil
	}
	lang := mobileBriefVoiceLang(language)
	var cards []MobileBriefCard
	// A button keeps its offer's id, which the phone names, and reads the
	// offer as it would be written for its subject now: a subject that
	// changed since the brief gets a button for what it needs now, and one
	// the brief can no longer offer anything on gets none. Any subject the
	// brief could offer counts, so a button stored before the picks (backlog
	// item 65) stays.
	current := map[string]MobileBriefOffer{}
	for _, draft := range mobileBriefOfferCandidates(brief, language) {
		if offer := draft.offer; len(offer.Refs) > 0 && current[offer.Refs[0]].Label == "" {
			current[offer.Refs[0]] = offer
		}
	}
	for _, stored := range brief.Offers {
		if len(stored.Refs) == 0 || len(cards) == 2 {
			continue
		}
		offer, ok := current[stored.Refs[0]]
		label := mobileBriefActionLabel(offer.Label)
		message := strings.TrimSpace(offer.Prompt)
		if !ok || label == "" || message == "" && offer.Action != mobileBriefCardOpenActivity {
			continue
		}
		subject, _ := mobileBriefCardSubject(brief, offer.Refs)
		mode := mobileBriefCardSend
		if offer.Action == mobileBriefCardOpenActivity {
			mode = mobileBriefCardOpenActivity
		}
		cards = append(cards, MobileBriefCard{ID: stored.ID, Kind: mobileBriefCardAction, Mode: mode, Target: offer.Target,
			Label: label, Message: message, Refs: append([]string{}, offer.Refs...),
			Facts: mobileBriefCardFacts(brief, subject, offer.Kind), Writer: mobileBriefCardWriterPattern})
	}
	seen := map[string]bool{}
	evidence := mobileBriefEvidenceByRef(brief)
	for _, row := range mobileBriefCardRows(brief) {
		// What was only seen in activity is never a button (mobile_brief_anchor.go).
		if mobileBriefItemAnchor(row.item, evidence) == "" {
			continue
		}
		key := mobileBriefCardItemKey(row.item)
		message := mobileBriefItemQuestion(brief, row.section, row.item, lang)
		if key == "" || seen[key] || message == "" {
			continue
		}
		seen[key] = true
		cards = append(cards, MobileBriefCard{ID: "item:" + key, Kind: mobileBriefCardItem, Mode: mobileBriefCardSend,
			Message: message, Refs: append([]string{}, row.item.Refs...), ItemKey: key,
			Facts: mobileBriefCardFacts(brief, row.item, row.section), Writer: mobileBriefCardWriterPattern})
	}
	var openerAnswers []MobileBriefCard
	if message, item, ok := mobileBriefOpenerMessage(brief, lang); ok {
		cards = append(cards, MobileBriefCard{ID: mobileBriefOpenerCardID, Kind: mobileBriefCardOpener, Mode: mobileBriefCardSpeakFirst,
			Message: message, Refs: append([]string{}, item.Refs...),
			Facts: mobileBriefCardFacts(brief, item, mobileBriefCardOpener), Writer: mobileBriefCardWriterPattern})
		openerAnswers = append(openerAnswers,
			MobileBriefCard{ID: "opener:yes", Kind: mobileBriefCardOpenerAnswer, Mode: mobileBriefCardSend,
				Label: pick(lang, "Start here", "从这里开始"), Message: pick(lang, "Help me start with this. What should I do first?", "帮我从这里开始。我应该先做什么？"), Refs: append([]string{}, item.Refs...), Writer: mobileBriefCardWriterPattern},
			MobileBriefCard{ID: "opener:other", Kind: mobileBriefCardOpenerAnswer, Mode: mobileBriefCardSend,
				Label: pick(lang, "Show me another option", "看看其他选择"), Message: pick(lang, "What else could I start with?", "我还可以先从什么开始？"), Refs: append([]string{}, item.Refs...), Writer: mobileBriefCardWriterPattern})
	}
	// Once prose exists, the closing row contains only answers written for
	// that question. Offers still serve their existing routes, but are not answers.
	if len(brief.Narrative) > 0 {
		kept := cards[:0]
		for _, card := range cards {
			if card.Kind != mobileBriefCardAction {
				kept = append(kept, card)
			}
		}
		cards = append(kept, mobileBriefClosingAnswers(brief, brief.Cards)...)
	}
	if ask, ok := yourWeekAskCard(brief); ok {
		cards = append(cards, ask)
	}
	return append(mobileBriefParagraphCards(brief, language, cards, todayChatPrepareLimit-3-len(cards)), openerAnswers...)
}

// mobileBriefCardsWithWritten keeps the words the brief's model wrote for a
// card while the card's facts are unchanged; otherwise the card reads
// Isoform's sentences from the current facts.
func mobileBriefCardsWithWritten(cards, stored []MobileBriefCard) []MobileBriefCard {
	written := map[string]MobileBriefCard{}
	for _, card := range stored {
		if card.Writer == mobileBriefCardWriterModel {
			written[card.ID] = card
		}
	}
	out := make([]MobileBriefCard, 0, len(cards))
	for _, card := range cards {
		if model, ok := written[card.ID]; ok && card.Mode != mobileBriefCardOpenActivity && model.Mode == card.Mode && model.Facts == card.Facts && model.Kind == card.Kind {
			card.Message, card.Writer = model.Message, mobileBriefCardWriterModel
			if card.Kind == mobileBriefCardQuestion || card.Kind == mobileBriefCardAction && model.Label != "" {
				card.Label = model.Label
			}
		}
		out = append(out, card)
	}
	return out
}

// Legacy phone offer sends still resolve their original offers through the
// current gates. These cards are internal compatibility data, never answers.
func mobileBriefLegacyOfferCard(brief MobileBriefV1, id, language string) (MobileBriefCard, bool) {
	for _, offer := range brief.Offers {
		if offer.ID == id {
			brief.Narrative, brief.Cards = nil, nil
			card, ok := mobileBriefFindCard(MobileBriefV1{Cards: mobileBriefPatternCards(brief, language)}, id)
			return card, ok && card.Kind == mobileBriefCardAction && card.Mode == mobileBriefCardSend
		}
	}
	return MobileBriefCard{}, false
}

// mobileBriefFindCard returns a delivered card by id.
func mobileBriefFindCard(brief MobileBriefV1, id string) (MobileBriefCard, bool) {
	for _, card := range brief.Cards {
		if card.ID == id {
			return card, true
		}
	}
	return MobileBriefCard{}, false
}

// ── Words written by the brief's model ──

// mobileBriefCardWords is what the brief's model writes for the cards in the
// same pass as the brief, so they share its goal.
type mobileBriefCardWords struct {
	ID       string `json:"id"`
	Label    string `json:"label,omitempty"`
	Message  string `json:"message"`
	AnswerTo string `json:"-"`
}

// mobileBriefCardPrompt asks the brief's model to write the cards' words.
// Each line gives a card's id, what it does, its subject and Isoform's own
// sentence for it.
func mobileBriefCardPrompt(brief MobileBriefV1, cards []MobileBriefCard, languageName string) string {
	var lines []string
	attention := map[string]bool{}
	for _, item := range mobileBriefAttentionItems(brief) {
		attention[mobileBriefCardItemKey(item)] = true
	}
	for _, card := range cards {
		if card.Mode == mobileBriefCardOpenActivity {
			continue
		}
		switch card.Kind {
		case mobileBriefCardAction:
			lines = append(lines, card.ID+" | button | label: "+card.Label+" | request: "+card.Message)
		case mobileBriefCardItem:
			if attention[card.ItemKey] {
				lines = append(lines, card.ID+" | question | "+card.Message)
			}
		case mobileBriefCardOpener:
			lines = append(lines, card.ID+" | opener | "+card.Message)
		}
	}
	if len(lines) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(pii.ModelOutputInstructions)
	b.WriteString("\nCards: the clicks on this brief. Rewrite each one's words in the same voice and with the same goal as the brief, keeping its subject and facts. Isoform's own wording is given; improve it only where yours is clearer and as specific.\n")
	b.WriteString("- button: a label of a verb and the thing, at most 36 characters (Draft reply to Dana, Set up Citi payment), and the exact request the user sends: what to produce, from what, and that nothing is sent or bought without them.\n")
	b.WriteString("- question: the exact question the user asks about the item, one sentence ending in a question mark, naming the item plainly with its due day or reason.\n")
	b.WriteString("- opener: what Isoform says first when the user opens the brief: what to start with and why, ending with one question.\n")
	b.WriteString("Never quote or describe what the user typed. Do not add facts, people or dates that the line does not give.\n")
	fmt.Fprintf(&b, "Write them in %s. Add to the JSON reply: \"cards\":[{\"id\":\"...\",\"label\":\"... (buttons only)\",\"message\":\"...\"}]\n", languageName)
	b.WriteString(strings.Join(lines, "\n") + "\n")
	return b.String()
}

// parseMobileBriefCardWords reads the cards from the brief model's reply.
func parseMobileBriefCardWords(raw string) ([]mobileBriefCardWords, error) {
	decoder := json.NewDecoder(strings.NewReader(stripMobileBriefCodeFence(raw)))
	var reply struct {
		Cards []mobileBriefCardWords `json:"cards"`
	}
	if err := decoder.Decode(&reply); err != nil {
		return nil, err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("cards have content after the JSON object")
	}
	return reply.Cards, nil
}

// mobileBriefCardWordsValid checks the model's words for one card: it names
// the subject plainly, a label is a verb and the thing within the limit, and
// nothing narrates or repeats typing.
func mobileBriefCardWordsValid(card MobileBriefCard, words mobileBriefCardWords, subject, lang string) bool {
	message := strings.TrimSpace(words.Message)
	if message == "" || !mobileBriefCopySafe(message) || !mobileBriefCopySafe(words.Label) || mobileBriefBannedOutcomePhrase(message) || threadOutcomeTypingNarration.MatchString(message) ||
		strings.ContainsAny(message, "\n\r") || mobileBriefInterfaceSentence.MatchString(message) || redactThreadOutcomeText(message) != message {
		return false
	}
	switch card.Kind {
	case mobileBriefCardAction:
		label := strings.TrimSpace(words.Label)
		if len(message) > mobileBriefCardMessageBytes || !mobileBriefCardLabelValid(label, lang) || threadOutcomeTypingNarration.MatchString(label) {
			return false
		}
		// No verb list for Chinese: its button names the thing it acts on.
		if lang == "zh" && !mobileBriefNamesSubject(label, subject) {
			return false
		}
	case mobileBriefCardQuestion:
		label := strings.TrimSpace(words.Label)
		if len(message) > mobileBriefCardQuestionBytes || utf8.RuneCountInString(label) == 0 ||
			utf8.RuneCountInString(label) > 40 || mobileBriefQuestionChipLabel(label, lang) != label ||
			!mobileBriefNamesSubject(label, subject) || mobileBriefUnsafeQuestionMessage(message, lang) {
			return false
		}
	case mobileBriefCardItem:
		if len(message) > mobileBriefCardQuestionBytes || !strings.HasSuffix(message, "?") && !strings.HasSuffix(message, "？") ||
			mobileBriefUnsafeQuestionMessage(message, lang) {
			return false
		}
	case mobileBriefCardOpener:
		if len(message) > mobileBriefCardQuestionBytes || strings.Count(message, "?")+strings.Count(message, "？") != 1 {
			return false
		}
	default:
		return false
	}
	return mobileBriefNamesSubject(message, subject)
}

func mobileBriefUnsafeQuestionMessage(message, lang string) bool {
	message = strings.TrimSpace(message)
	if lang == "zh" {
		for _, prefix := range []string{"发送", "发给", "支付", "预订", "购买", "分享", "转发"} {
			if strings.HasPrefix(message, prefix) {
				return true
			}
		}
		return false
	}
	words := strings.Fields(message)
	if len(words) == 0 {
		return false
	}
	first := strings.ToLower(strings.Trim(words[0], "\"“‘'([{"))
	switch first {
	case "send", "email", "message", "pay", "order", "book", "share", "post", "call", "buy", "submit", "delete", "forward":
		return true
	default:
		return false
	}
}

// mobileBriefCardLabelVerbs are the verbs an English button may start with.
var mobileBriefCardLabelVerbs = map[string]bool{
	"add": true, "answer": true, "ask": true, "book": true, "call": true, "check": true, "confirm": true,
	"continue": true, "decide": true, "draft": true, "email": true, "file": true, "finish": true,
	"find": true, "fix": true, "follow": true, "message": true, "note": true, "open": true, "order": true, "pay": true, "pick": true,
	"plan": true, "prep": true, "prepare": true, "reply": true, "review": true, "schedule": true,
	"send": true, "set": true, "share": true, "start": true, "unblock": true, "update": true, "write": true,
}

// mobileBriefCardLabelValid checks a button label: a verb and the thing, on
// one short line.
func mobileBriefCardLabelValid(label, lang string) bool {
	// A formatted clock owns its colon; headings and other punctuation
	// remain invalid in an action label.
	prose := briefTextClock.ReplaceAllStringFunc(label, func(clock string) string {
		if _, _, valid := briefClockParts(clock); valid {
			return ""
		}
		return clock
	})
	if lang == "zh" {
		// Chinese clocks are 24-hour with no period: "9:00", "17:28" (R47).
		prose = briefTextBareClock.ReplaceAllString(prose, "")
	}
	if label == "" || utf8.RuneCountInString(label) > mobileBriefCardLabelRunes || strings.ContainsAny(prose, "\n\r?？!！:：\"“”") {
		return false
	}
	if lang == "zh" {
		return true
	}
	words := strings.Fields(label)
	return len(words) >= 2 && mobileBriefCardLabelVerbs[strings.ToLower(words[0])]
}

// mobileBriefNamesSubject reports whether text names the subject plainly: a
// word of four or more letters from the title, or for Chinese two of its Han
// characters in a row.
func mobileBriefNamesSubject(text, subject string) bool {
	text, subject = strings.ToLower(text), strings.ToLower(subject)
	if subject == "" {
		return true
	}
	if strings.Contains(text, subject) {
		return true
	}
	for _, word := range strings.FieldsFunc(subject, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if utf8.RuneCountInString(word) >= 4 && !unicode.Is(unicode.Han, []rune(word)[0]) && strings.Contains(text, word) {
			return true
		}
	}
	han := []rune{}
	for _, r := range subject {
		if unicode.Is(unicode.Han, r) {
			han = append(han, r)
		} else {
			han = append(han, 0)
		}
	}
	for i := 0; i+1 < len(han); i++ {
		if han[i] != 0 && han[i+1] != 0 && strings.Contains(text, string(han[i:i+2])) {
			return true
		}
	}
	return false
}

// mobileBriefApplyCardWords keeps each valid model-written card's words; any
// other card keeps Isoform's sentence.
func mobileBriefApplyCardWords(brief MobileBriefV1, cards []MobileBriefCard, words []mobileBriefCardWords, languageName string) []MobileBriefCard {
	if !featureOn(featureBriefExtras) {
		return nil
	}
	lang := mobileBriefVoiceLang(languageName)
	byID := map[string]mobileBriefCardWords{}
	for _, word := range words {
		word.Label = normalizeBriefText(word.Label, languageName, mobileBriefTextReference(brief), false)
		word.Message = normalizeBriefText(word.Message, languageName, mobileBriefTextReference(brief), false)
		byID[strings.TrimSpace(word.ID)] = word
	}
	out := make([]MobileBriefCard, 0, len(cards))
	for _, card := range cards {
		card.Label = normalizeBriefText(card.Label, languageName, mobileBriefTextReference(brief), false)
		card.Message = normalizeBriefText(card.Message, languageName, mobileBriefTextReference(brief), false)
		word, ok := byID[card.ID]
		subject := ""
		if item, found := mobileBriefCardSubject(brief, card.Refs); found {
			subject = mobileBriefCardTitle(item)
		}
		// Words are checked against the subject they name; a card whose
		// subject is not in the brief keeps Isoform's sentence.
		if ok && card.Mode != mobileBriefCardOpenActivity && subject != "" && mobileBriefCardWordsValid(card, word, subject, lang) {
			card.Message, card.Writer = strings.TrimSpace(word.Message), mobileBriefCardWriterModel
			if card.Kind == mobileBriefCardAction || card.Kind == mobileBriefCardQuestion {
				card.Label = strings.TrimSpace(word.Label)
			}
		}
		if !mobileBriefCardCopySafe(card) {
			continue
		}
		if card.Kind == mobileBriefCardQuestion {
			card.Label = mobileBriefQuestionChipLabel(card.Label, lang)
			if card.Label == "" {
				continue
			}
		} else if card.Kind == mobileBriefCardItem && card.Paragraph != nil {
			card.Label = mobileBriefQuestionChipLabel(card.Label, lang)
		}
		out = append(out, card)
	}
	// Only closing.answers can supply answer words, bound after validating the
	// closing in the same model reply. Ordinary cards never acquire AnswerTo.
	var answers []MobileBriefCard
	for _, word := range words {
		if word.AnswerTo != "" {
			word.Label = normalizeBriefText(word.Label, languageName, mobileBriefTextReference(brief), false)
			word.Message = normalizeBriefText(word.Message, languageName, mobileBriefTextReference(brief), false)
			answers = append(answers, MobileBriefCard{ID: word.ID, Kind: mobileBriefCardAction, Mode: mobileBriefCardSend,
				Label: strings.TrimSpace(word.Label), Message: strings.TrimSpace(word.Message), AnswerTo: word.AnswerTo,
				Facts: word.AnswerTo, Refs: mobileBriefClosingParagraph(brief).Refs, Writer: mobileBriefCardWriterModel})
		}
	}
	out = append(out, mobileBriefClosingAnswers(brief, answers)...)
	return mobileBriefLimitParagraphChips(brief, out)
}

// mobileBriefWithCards completes a delivered brief for reading (Today, the
// phone and the brief's chat): attention only for anchored items, the
// template when there is no written text, and every card checked against the
// cards as delivered now. Every reader applies it, so they show the same
// cards and conversation key.
func (a *App) mobileBriefWithCards(brief MobileBriefV1) MobileBriefV1 {
	return a.mobileBriefWithCardsFrom(context.Background(), brief, nil)
}

func (a *App) mobileBriefWithCardsFrom(ctx context.Context, brief MobileBriefV1, sources *todayChatSources) MobileBriefV1 {
	if a == nil {
		return brief
	}
	language := a.modelDisplayLanguageContext(ctx)
	languageName := uiLanguageDisplayName(language)
	brief = mobileBriefWithDisplayTimes(brief, languageName)
	brief = mobileBriefWithoutUnanchoredAttention(brief)
	brief = a.mobileBriefWithTemplate(brief)
	if mobileBriefStaticEmpty(brief) {
		return brief
	}
	if brief.NarrativeStatus == "quiet" {
		return mobileBriefHygiene(brief)
	}
	if a.IsClosedEyesModeEnabled() && !pausedBriefReadAllowed(ctx) {
		brief = mobileBriefWithoutParagraphTopics(brief)
		brief.Cards = nil
		return brief
	}
	closing := mobileBriefClosingParagraph(brief)
	brief = mobileBriefBlankExcluded(brief, a.mobileBriefExclusions())
	brief = mobileBriefRewrittenClosing(brief, closing, languageName)
	if sources == nil {
		brief = a.mobileBriefWithParagraphTopics(ctx, brief)
	} else if sources.skipBriefTopics {
		brief = mobileBriefWithoutParagraphTopics(brief)
	} else {
		brief = a.mobileBriefWithParagraphTopicsFrom(ctx, brief, *sources)
	}
	var cards []MobileBriefCard
	for _, card := range mobileBriefCardsWithWritten(mobileBriefPatternCards(brief, languageName), brief.Cards) {
		if !mobileBriefCardCopySafe(card) {
			continue
		}
		if card.Kind == mobileBriefCardAction && !mobileBriefAnswerMatches(brief, card) {
			continue
		}
		// "Talk about this brief" opens on a brief that has text to talk about.
		if card.Kind == mobileBriefCardOpener && len(brief.Narrative) == 0 {
			continue
		}
		card.Label = normalizeBriefText(card.Label, languageName, mobileBriefTextReference(brief), false)
		card.Message = normalizeBriefText(card.Message, languageName, mobileBriefTextReference(brief), false)
		if card.Kind == mobileBriefCardQuestion || card.Kind == mobileBriefCardItem && card.Paragraph != nil {
			card.Label = mobileBriefQuestionChipLabel(card.Label, mobileBriefVoiceLang(languageName))
			if card.Kind == mobileBriefCardQuestion && card.Label == "" {
				continue
			}
		}
		cards = append(cards, card)
	}
	if !slices.ContainsFunc(cards, func(card MobileBriefCard) bool { return card.Kind == mobileBriefCardAction && card.AnswerTo != "" }) {
		cards = append(cards, mobileBriefClosingChips(brief, languageName)...)
	}
	brief.Cards = mobileBriefLimitParagraphChips(brief, cards)
	brief = mobileBriefHygiene(brief)
	brief = displayModelCopy(brief, language)
	brief.ConversationKey = mobileBriefConversationKey(brief)
	return brief
}
