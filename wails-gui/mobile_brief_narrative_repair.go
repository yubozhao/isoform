package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// The rules a brief model reply is held to, by name. A log line names the
// rule only, never the reply's text.
const (
	mobileBriefRuleInterface  = "interface_sentence"
	mobileBriefRuleJSON       = "json"
	mobileBriefRuleTrailing   = "trailing_content"
	mobileBriefRuleNoText     = "no_paragraphs"
	mobileBriefRuleBudget     = "reading_budget"
	mobileBriefRuleClosing    = "closing"
	mobileBriefRuleQuestion   = "question"
	mobileBriefRuleAge        = "age"
	mobileBriefRuleBanned     = "banned_phrase"
	mobileBriefRuleOrder      = "section_order"
	mobileBriefRuleHighlights = "highlights"
	mobileBriefRuleVoice      = "voice"
	mobileBriefRuleClock      = "clock_time"
	mobileBriefRulePronoun    = "dangling_pronoun"
)

// mobileBriefNarrativeRuleError is a reply that breaks one named rule.
type mobileBriefNarrativeRuleError struct{ rule, message string }

func (e *mobileBriefNarrativeRuleError) Error() string { return e.message }

func mobileBriefNarrativeRuleErr(rule, message string) error {
	return &mobileBriefNarrativeRuleError{rule: rule, message: message}
}

// mobileBriefNarrativeRule is the name of the rule err reports.
func mobileBriefNarrativeRule(err error) string {
	var rule *mobileBriefNarrativeRuleError
	if errors.As(err, &rule) {
		return rule.rule
	}
	return "unknown"
}

// mobileBriefNarrativeFromReply turns the brief model's reply into the
// brief's paragraphs, the model's own closing question last. A sentence that
// breaks a voice rule goes and the rest stays (repairMobileBriefNarrative);
// the closing must offer one of the brief's buttons by name
// (mobileBriefWrittenClosing). A reply that still breaks a rule returns the
// rule, and the caller retries once with it before the template. The log
// names the rules, never the text.
func mobileBriefNarrativeFromReply(brief MobileBriefV1, picks []mobileBriefPick, raw, languageName string) ([]MobileBriefPara, []mobileBriefCardWords, error) {
	paragraphs, words, _, _, err := mobileBriefNarrativeFromReplyWithWeek(brief, picks, raw, languageName, nil)
	return paragraphs, words, err
}

func mobileBriefNarrativeFromReplyWithWeek(brief MobileBriefV1, picks []mobileBriefPick, raw, languageName string, week *MobileBriefWeek) ([]MobileBriefPara, []mobileBriefCardWords, string, string, error) {
	allowed := mobileBriefPickRefs(brief, picks)
	var evidence []MobileBriefRef
	for _, ref := range brief.Evidence {
		if allowed[ref.Ref] {
			evidence = append(evidence, ref)
		}
	}
	paragraphs, err := parseMobileBriefNarrative(raw, evidence)
	if err != nil {
		return nil, nil, "", "", err
	}
	var reply mobileBriefNarrativeReply
	_ = json.Unmarshal([]byte(stripMobileBriefCodeFence(raw)), &reply)
	var repaired []string
	paragraphs, repaired = repairMobileBriefNarrative(brief, paragraphs, languageName)
	if len(repaired) > 0 {
		log.Printf("[mobile-brief] narrative repaired: rules=%s", strings.Join(uniqueSortedStrings(repaired), ","))
	}
	if len(paragraphs) == 0 {
		return nil, nil, "", "", mobileBriefNarrativeRuleErr(mobileBriefRuleNoText, "no paragraph was left after the voice rules")
	}
	if mobileBriefCalendarOnlyParagraph(brief, paragraphs[0]) {
		return nil, nil, "", "", mobileBriefNarrativeRuleErr(mobileBriefRuleOrder, "lead with the observed item")
	}
	// A brief with no button on its picks (the day alone, or picks Isoform
	// has no step for) asks nothing; any closing it wrote is left out.
	if len(picks) >= mobileBriefPickMinimum && len(mobileBriefPickOffers(brief, picks, languageName)) > 0 {
		closing, err := mobileBriefWrittenClosing(brief, reply.Closing, languageName)
		if err != nil {
			return nil, nil, "", "", err
		}
		paragraphs = append(paragraphs, closing)
	}
	paragraphs, err = checkMobileBriefNarrative(brief, paragraphs, languageName)
	if err != nil {
		return nil, nil, "", "", err
	}
	words, err := parseMobileBriefCardWords(raw)
	if err != nil {
		words = nil
	}
	for index := range paragraphs {
		if chip := paragraphs[index].chipWords; chip != nil {
			words = append(words, mobileBriefCardWords{ID: mobileBriefQuestionID(brief, paragraphs[index], index, 0), Label: chip.Label, Message: chip.Message})
		}
		paragraphs[index].chipWords = nil
	}
	if reply.Closing != nil && len(paragraphs) > 0 {
		written := brief
		written.Narrative = paragraphs
		digest := mobileBriefClosingDigest(written)
		for slot, answer := range reply.Closing.Answers {
			if slot >= 2 || digest == "" {
				break
			}
			answer.ID, answer.AnswerTo = fmt.Sprintf("answer:%s:%d", digest, slot), digest
			words = append(words, answer)
		}
	}
	weekLead, weekLeadFacts := validateYourWeekModelLead(reply.WeekLead, week, mobileBriefVoiceLang(languageName))
	return paragraphs, words, weekLead, weekLeadFacts, nil
}

var mobileBriefHoldClockRange = regexp.MustCompile(`(?i)(?:` + mobileBriefDisplayClock.String() + `)\s*(?:to|until|till|through|and|[-–—~～]|至|到)\s*(?:` + mobileBriefDisplayClock.String() + `)`)

var mobileBriefHoldQuestion = regexp.MustCompile(`(?i)\b(hold|reserve|block|set aside|make time)\b|预留|留出|留给|安排.*时间`)

// Until calendar holds are available, a generated closing must not offer one.
// Reject the reply rather than changing its meaning after generation.
func mobileBriefRejectHoldOffer(paragraphs []MobileBriefPara) error {
	if len(paragraphs) == 0 {
		return nil
	}
	closing := paragraphs[len(paragraphs)-1]
	if closing.Hold != nil || (strings.HasSuffix(strings.TrimSpace(closing.Text), "?") || strings.HasSuffix(strings.TrimSpace(closing.Text), "？")) && mobileBriefHoldQuestion.MatchString(closing.Text) {
		return mobileBriefNarrativeRuleErr(mobileBriefRuleClosing, "calendar hold offers are unavailable")
	}
	return nil
}

// A previously published brief can still contain the preview-only offer. Its
// answers and recording describe the same offer, so withdraw them together.
func mobileBriefWithoutHoldOffer(brief MobileBriefV1) MobileBriefV1 {
	if mobileBriefRejectHoldOffer(brief.Narrative) == nil {
		return brief
	}
	brief.Narrative = append([]MobileBriefPara{}, brief.Narrative[:len(brief.Narrative)-1]...)
	cards := make([]MobileBriefCard, 0, len(brief.Cards))
	for _, card := range brief.Cards {
		if card.Kind != mobileBriefCardAction {
			cards = append(cards, card)
		}
	}
	brief.Cards = cards
	brief.Audio = nil
	return brief
}

// mobileBriefWrittenClosing checks the model's closing: one question that
// ends the brief, offering one of the brief's ready buttons by its subject,
// within the voice rules. Its refs are that button's refs.
func mobileBriefWrittenClosing(brief MobileBriefV1, closing *mobileBriefNarrativeReplyClosing, languageName string) (MobileBriefPara, error) {
	if closing == nil || strings.TrimSpace(closing.Text) == "" {
		return MobileBriefPara{}, mobileBriefNarrativeRuleErr(mobileBriefRuleClosing, "the reply has no closing question")
	}
	text := normalizeBriefText(strings.TrimSpace(closing.Text), languageName, mobileBriefTextReference(brief), false)
	protected := mobileBriefQuotedContent(brief)
	rewritten, clean := rewriteMobileBriefOwnVoice(text, protected...)
	if !clean {
		return MobileBriefPara{}, mobileBriefNarrativeRuleErr(mobileBriefRuleClosing, "the closing breaks the voice rules")
	}
	text = rewritten
	if strings.Count(text, "?")+strings.Count(text, "？") != 1 || !strings.HasSuffix(text, "?") && !strings.HasSuffix(text, "？") {
		return MobileBriefPara{}, mobileBriefNarrativeRuleErr(mobileBriefRuleClosing, "the closing must be exactly one question, ending the brief")
	}
	var offer MobileBriefOffer
	found := false
	for _, candidate := range mobileBriefPickOffers(brief, mobileBriefPicks(brief), languageName) {
		if candidate.ID == strings.TrimSpace(closing.Card) {
			offer, found = candidate, true
		}
	}
	if !found || len(offer.Refs) == 0 {
		return MobileBriefPara{}, mobileBriefNarrativeRuleErr(mobileBriefRuleClosing, "the closing's card must be the id of one of the buttons listed")
	}
	if item, ok := mobileBriefCardSubject(brief, offer.Refs); !ok || strings.TrimSpace(mobileBriefCardTitle(item)) == "" || !mobileBriefCopySafe(mobileBriefCardTitle(item)) {
		return MobileBriefPara{}, mobileBriefNarrativeRuleErr(mobileBriefRuleClosing, "the closing has no named subject")
	} else {
		if subject := mobileBriefCardTitle(item); !mobileBriefNamesSubject(text, subject) && !mobileBriefNamesSomeone(text, subject) {
			return MobileBriefPara{}, mobileBriefNarrativeRuleErr(mobileBriefRuleClosing, "the closing must name the subject of the button it offers")
		}
	}
	if closing.Hold != nil && !mobileBriefHoldQuestion.MatchString(text) {
		return MobileBriefPara{}, mobileBriefNarrativeRuleErr(mobileBriefRuleClosing, "closing.hold requires a question that proposes reserving time")
	}
	day, err := todayDayShapeBriefDay(brief)
	if err != nil && closing.Hold != nil {
		return MobileBriefPara{}, mobileBriefNarrativeRuleErr(mobileBriefRuleClosing, "closing.hold requires a valid brief localDate")
	}
	hold, err := validatedTodayHold(closing.Hold, day, mobileBriefAsOfTime(brief))
	if err != nil {
		return MobileBriefPara{}, mobileBriefNarrativeRuleErr(mobileBriefRuleClosing, err.Error())
	}
	if !mobileBriefCopySafe(text) || len(text) > mobileBriefNarrativeMaxBytes || mobileBriefBannedOutcomePhrase(text) || mobileBriefVoiceBroken(text, protected...) ||
		!mobileBriefClosingClocksSupported(text, offer.Refs, mobileBriefPickClocks(brief, languageName), hold, mobileBriefNarrativeLocation(brief)) || mobileBriefInterfaceSentence.MatchString(text) {
		return MobileBriefPara{}, mobileBriefNarrativeRuleErr(mobileBriefRuleClosing, "the closing breaks the voice rules")
	}
	return MobileBriefPara{Text: text, Refs: uniqueSortedStrings(offer.Refs), Hold: hold}, nil
}

// A proposed clock belongs to the validated preview, not to an existing
// event. Ranges must match its ordered endpoints; one clock may name either
// endpoint. A different single clock still needs its original source refs.
func mobileBriefClosingClocksSupported(text string, refs []string, clocks map[string]map[string]bool, hold *TodayDayShapeHold, loc *time.Location) bool {
	if hold != nil {
		start, _ := time.Parse(time.RFC3339, hold.Start)
		end, _ := time.Parse(time.RFC3339, hold.End)
		start, end = start.In(loc), end.In(loc)
		mismatch := false
		text = mobileBriefHoldClockRange.ReplaceAllStringFunc(text, func(interval string) string {
			tokens := mobileBriefDisplayClock.FindAllString(interval, -1)
			if len(tokens) != 2 {
				mismatch = true
				return interval
			}
			sh, sm, sok := briefClockParts(tokens[0])
			eh, em, eok := briefClockParts(tokens[1])
			if sok && eok && sh == start.Hour() && sm == start.Minute() && eh == end.Hour() && em == end.Minute() {
				return ""
			}
			// A clock range in a hold question must describe this preview
			// in order, even when its clocks also appear in source refs.
			mismatch = true
			return interval
		})
		if mismatch {
			return false
		}
		remaining := mobileBriefDisplayClock.FindAllString(text, -1)
		// Two clocks must have been consumed as an explicitly recognised,
		// ordered hold range. Source refs cannot legitimise another pairing.
		if len(remaining) > 1 {
			return false
		}
		if len(remaining) == 1 {
			hour, minute, ok := briefClockParts(remaining[0])
			if ok && (hour == start.Hour() && minute == start.Minute() || hour == end.Hour() && minute == end.Minute()) {
				return true
			}
		}
	}
	return mobileBriefClocksSupported(text, refs, clocks)
}

// checkMobileBriefNarrative holds the written brief to its budget: at most
// mobileBriefMaxWords in all. Over it, the last paragraphs before the closing
// go, one at a time; a brief that still does not fit is refused.
func checkMobileBriefNarrative(brief MobileBriefV1, paragraphs []MobileBriefPara, languageName string) ([]MobileBriefPara, error) {
	var closing []MobileBriefPara
	body := paragraphs
	if n := len(paragraphs); n > 0 && strings.ContainsAny(paragraphs[n-1].Text, "?？") {
		body, closing = paragraphs[:n-1], paragraphs[n-1:]
	}
	if len(body) == 0 {
		return nil, mobileBriefNarrativeRuleErr(mobileBriefRuleNoText, "the brief has no paragraph besides the closing")
	}
	for len(body) > 1 && mobileBriefNarrativeWords(append(append([]MobileBriefPara{}, body...), closing...)) > mobileBriefMaxWords {
		body = body[:len(body)-1]
	}
	out := append(append([]MobileBriefPara{}, body...), closing...)
	if mobileBriefNarrativeWords(out) > mobileBriefMaxWords {
		return nil, mobileBriefNarrativeRuleErr(mobileBriefRuleBudget, "the brief is longer than 300 words")
	}
	return out, nil
}

// repairMobileBriefNarrative brings a reply within the voice rules where that
// loses little, so one stray sentence no longer costs the whole text. A clean
// own-device reference is rewritten. Uncertainty copy, an emoji, an ambiguous
// quoted reference, an unsupported clock, or a question is dropped; an
// exclamation mark becomes a full stop. A sentence right after a dropped one
// that opens with a pronoun goes too, since its noun went with the sentence
// before. It returns the rules it repaired.
func repairMobileBriefNarrative(brief MobileBriefV1, paragraphs []MobileBriefPara, languageName string) ([]MobileBriefPara, []string) {
	var repaired []string
	note := func(rule string) {
		if !slices.Contains(repaired, rule) {
			repaired = append(repaired, rule)
		}
	}
	reference := mobileBriefTextReference(brief)
	clocks := mobileBriefPickClocks(brief, languageName)
	protected := mobileBriefQuotedContent(brief)
	dropped := false
	var body []MobileBriefPara
	for _, paragraph := range paragraphs {
		text := normalizeBriefText(strings.TrimSpace(paragraph.Text), languageName, reference, false)
		var kept strings.Builder
		for _, sentence := range splitMobileBriefOwnVoiceSentences(text, protected) {
			rewritten, clean := rewriteMobileBriefOwnVoice(sentence, protected...)
			deviceRewritten := clean && rewritten != sentence
			if clean {
				sentence = rewritten
			}
			rule := ""
			switch true {
			case strings.TrimSpace(sentence) == "":
				continue
			case mobileBriefInterfaceSentence.MatchString(sentence):
				rule = mobileBriefRuleInterface
			case mobileBriefBannedOutcomePhrase(sentence):
				rule = mobileBriefRuleBanned
			case reEmoji.MatchString(sentence) || !clean:
				rule = mobileBriefRuleVoice
			case !mobileBriefClocksSupported(sentence, paragraph.Refs, clocks):
				rule = mobileBriefRuleClock
			case strings.ContainsAny(sentence, "?？"):
				rule = mobileBriefRuleQuestion
			case dropped && mobileBriefOpensWithPronoun(sentence):
				rule = mobileBriefRulePronoun
			}
			if rule != "" {
				note(rule)
				dropped = true
				continue
			}
			dropped = false
			if deviceRewritten {
				note(mobileBriefRuleVoice)
			}
			if strings.ContainsAny(sentence, "!！") {
				sentence = strings.NewReplacer("!", ".", "！", "。").Replace(sentence)
				note(mobileBriefRuleVoice)
			}
			kept.WriteString(sentence)
		}
		paragraph.Text = strings.TrimSpace(kept.String())
		if paragraph.Text == "" {
			continue
		}
		body = append(body, paragraph)
	}
	return body, repaired
}

// Known interface narration, including anaphoric variants. The repair log
// records this rule once per reply, without private sentence text.
var mobileBriefInterfaceSentence = regexp.MustCompile(`(?i)\bI\s+can\s+(?:open|show|add)\b[^.!?。！？]*(?:\b(?:in|on)\s+(?:the\s+)?[\p{L}][\p{L}\p{N}_ -]*|\bfor\s+that\s+too\b)|我可以[^。！？]*(?:(?:打开|显示|添加)[^。！？]*(?:页面|Activity|Today|活动|设置)|(?:页面|Activity|Today|活动|设置)[^。！？]*(?:打开|显示|添加))`)

// mobileBriefLeadPronouns open a sentence that leans on the one before it.
var mobileBriefLeadPronouns = map[string]bool{
	"it": true, "its": true, "it's": true, "they": true, "their": true, "them": true, "this": true, "that": true,
	"these": true, "those": true, "he": true, "she": true, "his": true, "her": true,
}

// mobileBriefOpensWithPronoun reports a sentence whose first word is a
// pronoun ("It has been open since last week.", "它已经…").
func mobileBriefOpensWithPronoun(sentence string) bool {
	sentence = strings.TrimLeftFunc(sentence, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsPunct(r) })
	for _, han := range []string{"它", "他", "她", "这", "那", "其"} {
		if strings.HasPrefix(sentence, han) {
			return true
		}
	}
	first := strings.ToLower(strings.ReplaceAll(strings.SplitN(sentence, " ", 2)[0], "’", "'"))
	return mobileBriefLeadPronouns[strings.TrimRight(first, ",.;:")]
}

// mobileBriefNamesSomeone reports text that says a name from the subject: a
// capitalized word of two or more letters ("Sam", "NMG"), as a whole word.
// Short names fail mobileBriefNamesSubject's four-letter rule.
func mobileBriefNamesSomeone(text, subject string) bool {
	for _, word := range strings.FieldsFunc(subject, func(r rune) bool { return !unicode.IsLetter(r) || unicode.Is(unicode.Han, r) }) {
		runes := []rune(word)
		if len(runes) < 2 || !unicode.IsUpper(runes[0]) {
			continue
		}
		for at := strings.Index(text, word); at >= 0; {
			before, _ := utf8.DecodeLastRuneInString(text[:at])
			after, _ := utf8.DecodeRuneInString(text[at+len(word):])
			if !mobileBriefASCIILetter(before) && !mobileBriefASCIILetter(after) {
				return true
			}
			next := strings.Index(text[at+len(word):], word)
			if next < 0 {
				break
			}
			at += len(word) + next
		}
	}
	return false
}

func mobileBriefASCIILetter(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z'
}
