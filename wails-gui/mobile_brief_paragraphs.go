package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
)

// mobileBriefWithParagraphTopics derives navigation only from the published
// registry and the paragraph's original sources. Neither a saved id nor model
// output is authority. Recheck the snapshot's complete source closure on reads.
func (a *App) mobileBriefWithParagraphTopics(ctx context.Context, brief MobileBriefV1) MobileBriefV1 {
	brief = mobileBriefWithoutParagraphTopics(brief)
	if !featureOn(featureBriefExtras) || a == nil || a.topicSource == nil || a.IsClosedEyesModeEnabled() || len(brief.Narrative) == 0 {
		return brief
	}
	snapshots := a.topicsSvc().store.load().ChatTopics
	if len(snapshots) == 0 {
		return brief
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return a.mobileBriefWithParagraphTopicsFrom(ctx, brief, a.collectTodayChatSources(ctx, time.Now()))
}

// This cache belongs to one source read, never to the persisted registry cache.
// A batch validates the registry once and shares its links across brief reads.
type mobileBriefParagraphTopicLinks struct {
	loaded bool
	links  []mobileBriefParagraphTopicLink
}

type mobileBriefParagraphTopicLink struct {
	topic ChatTopic
	keys  ChatTopicKeys
}

func (a *App) mobileBriefWithParagraphTopicsFrom(ctx context.Context, brief MobileBriefV1, sources todayChatSources) MobileBriefV1 {
	brief = mobileBriefWithoutParagraphTopics(brief)
	if !featureOn(featureBriefExtras) || a == nil || a.topicSource == nil || a.IsClosedEyesModeEnabled() || ctx.Err() != nil || len(brief.Narrative) == 0 {
		return brief
	}
	cache := sources.briefTopics
	if cache == nil {
		cache = &mobileBriefParagraphTopicLinks{}
	}
	if !cache.loaded {
		cache.loaded = true
		for _, snapshot := range a.topicsSvc().store.load().ChatTopics {
			id := snapshot.Topic.ID
			if !validChatTopicID(id) {
				continue
			}
			topic, keys, err := a.chatTopicSnapshotFromSources(ctx, id, sources)
			if err == nil {
				cache.links = append(cache.links, mobileBriefParagraphTopicLink{topic: topic, keys: keys})
			}
		}
	}
	evidence := mobileBriefEvidenceByRef(brief)
	for _, link := range cache.links {
		topic, keys := link.topic, link.keys
		for i := range brief.Narrative {
			para := &brief.Narrative[i]
			if para.TopicID != "" {
				continue
			}
			for _, key := range para.Refs {
				ref, ok := evidence[key]
				if !ok {
					continue
				}
				kind := ref.SourceKind
				if kind == mobileBriefRecapSource && strings.HasPrefix(ref.SourceID, threadOutcomeMomentPrefix) {
					kind = "activity_outcome"
				}
				matched := a.mainChatRefMatchesTopic(ctx, mainChatEvidenceRef{Kind: kind, ID: ref.SourceID}, keys, sources)
				// Following topics can also be named by their original find or
				// interest ref; no title matching or fabricated topic identity.
				for _, source := range topic.Refs {
					if source.SourceKind == ref.SourceKind && source.SourceID == ref.SourceID {
						matched = true
					}
				}
				if matched {
					para.TopicID, para.TopicName = topic.ID, topic.Name
					break
				}
			}
		}
	}
	if a.IsClosedEyesModeEnabled() || ctx.Err() != nil {
		return mobileBriefWithoutParagraphTopics(brief)
	}
	return brief
}

func mobileBriefWithoutParagraphTopics(brief MobileBriefV1) MobileBriefV1 {
	brief.Narrative = mobileBriefParasWithoutTopics(brief.Narrative)
	return brief
}

func mobileBriefParasWithoutTopics(paras []MobileBriefPara) []MobileBriefPara {
	if len(paras) == 0 {
		return paras
	}
	paras = append([]MobileBriefPara(nil), paras...)
	for i := range paras {
		paras[i].TopicID, paras[i].TopicName = "", ""
	}
	return paras
}

func mobileBriefQuestionID(brief MobileBriefV1, para MobileBriefPara, paragraph, slot int) string {
	// Position alone can be reused after privacy removes an earlier paragraph.
	// Bind identity to the text and original sources, independent of ref numbers.
	facts := mobileBriefCardFacts(brief, MobileBriefItem{Title: para.Text, Refs: para.Refs}, "paragraph")
	return fmt.Sprintf("question:%s:%d:%d", facts, paragraph, slot)
}

// Paragraph chips share existing item cards where possible, then spend the
// remaining draft budget on question cards.
func mobileBriefParagraphCards(brief MobileBriefV1, language string, cards []MobileBriefCard, remaining int) []MobileBriefCard {
	lang := mobileBriefVoiceLang(language)
	cards = append([]MobileBriefCard(nil), cards...)
	for i := range cards {
		if cards[i].Kind == mobileBriefCardItem {
			cards[i].Paragraph, cards[i].Label = nil, ""
		}
	}
	type candidate struct {
		paragraph int
		priority  int
		label     string
		message   string
		subject   MobileBriefItem
		linked    int
	}
	attention := mobileBriefAttentionItems(brief)
	evidence := mobileBriefEvidenceByRef(brief)
	attentionOrder := map[string]int{}
	for i, item := range attention {
		if len(item.Refs) == 0 {
			continue
		}
		ref := evidence[item.Refs[0]]
		key := ref.SourceKind + "\x00" + ref.SourceID
		if _, exists := attentionOrder[key]; !exists {
			attentionOrder[key] = i
		}
	}
	itemCards := map[string]int{}
	for i, card := range cards {
		if card.Kind == mobileBriefCardItem {
			itemCards[card.ID] = i
		}
	}
	var candidates []candidate
	for index, para := range brief.Narrative {
		if index >= mobileBriefNarrativeMaxParagraphs {
			break
		}
		if index == len(brief.Narrative)-1 && strings.ContainsAny(para.Text, "?？") {
			continue
		}
		if len(para.Refs) == 0 {
			continue
		}
		if brief.rankedEdition && para.chipWords == nil {
			found := false
			for _, card := range brief.Cards {
				if card.Kind == mobileBriefCardQuestion && card.Paragraph != nil && *card.Paragraph == index {
					found = true
				}
			}
			if !found {
				continue
			}
		}
		subject, section, ok := mobileBriefCardSubjectSection(brief, para.Refs)
		if !ok {
			continue
		}
		title := mobileBriefText(mobileBriefCardTitle(subject), 80)
		if title == "" {
			continue
		}
		label, message, ok := mobileBriefParagraphChipCopy(brief, section, subject, lang)
		if !ok {
			continue
		}
		label = mobileBriefQuestionChipLabel(label, lang)
		if label == "" {
			continue
		}
		priority := len(attentionOrder) + index
		if len(subject.Refs) > 0 {
			ref := evidence[subject.Refs[0]]
			if rank, found := attentionOrder[ref.SourceKind+"\x00"+ref.SourceID]; found {
				priority = rank
			}
		}
		linked := -1
		if len(subject.Refs) > 0 && !brief.rankedEdition {
			ref := evidence[subject.Refs[0]]
			if _, found := attentionOrder[ref.SourceKind+"\x00"+ref.SourceID]; found {
				if cardIndex, exists := itemCards["item:"+mobileBriefCardItemKey(subject)]; exists {
					linked = cardIndex
				}
			}
		}
		candidates = append(candidates, candidate{paragraph: index, priority: priority, label: label,
			message: message, subject: subject, linked: linked})
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].priority != candidates[j].priority {
			return candidates[i].priority < candidates[j].priority
		}
		return candidates[i].paragraph < candidates[j].paragraph
	})
	seenLabels, seenSources, seenParagraphs := map[string]bool{}, map[string]bool{}, map[int]bool{}
	for _, item := range candidates {
		labelKey := strings.ToLower(strings.Join(strings.Fields(item.label), " "))
		ref := ""
		if len(item.subject.Refs) > 0 {
			source := evidence[item.subject.Refs[0]]
			ref = source.SourceKind + "\x00" + source.SourceID
		}
		if seenParagraphs[item.paragraph] || seenLabels[labelKey] || ref != "\x00" && seenSources[ref] || len(seenLabels) >= 3 {
			continue
		}
		if item.linked >= 0 {
			paragraph := item.paragraph
			cards[item.linked].Paragraph, cards[item.linked].Label = &paragraph, item.label
		} else {
			if remaining <= 0 {
				continue
			}
			para := brief.Narrative[item.paragraph]
			paragraph := item.paragraph
			cards = append(cards, MobileBriefCard{ID: mobileBriefQuestionID(brief, para, item.paragraph, 0), Kind: mobileBriefCardQuestion,
				Mode: mobileBriefCardSend, Paragraph: &paragraph, Message: item.message, Label: item.label, Refs: append([]string{}, para.Refs...),
				Facts: mobileBriefCardFacts(brief, item.subject, mobileBriefModelDigest(struct {
					Text, TopicID string
					Refs          []string
				}{para.Text, para.TopicID, para.Refs})), Writer: mobileBriefCardWriterPattern})
			remaining--
		}
		seenLabels[labelKey], seenParagraphs[item.paragraph] = true, true
		if ref != "\x00" {
			seenSources[ref] = true
		}
	}
	return mobileBriefLimitParagraphChips(brief, cards)
}

func mobileBriefParagraphChipCopy(brief MobileBriefV1, section string, item MobileBriefItem, lang string) (string, string, bool) {
	title := mobileBriefText(mobileBriefCardTitle(item), 80)
	if title == "" {
		return "", "", false
	}
	switch {
	case section == mobileBriefSectionWaitingOnYou || item.Kind == "waiting_on_reply":
		return pick(lang, "Draft the reply to "+title, "起草给"+title+"的回复"),
			pick(lang, "Draft my reply to "+title+". Don't send it.", "起草我给"+title+"的回复，先不要发送。"), true
	case section == mobileBriefSectionMeetings && mobileBriefFutureCalendarItem(brief, item):
		at := mobileBriefParseTime(item.At)
		if at.IsZero() {
			return "", "", false
		}
		when := briefShortClock(at.In(mobileBriefNarrativeLocation(brief)).Hour(), at.In(mobileBriefNarrativeLocation(brief)).Minute(), lang)
		if person := mobileBriefMeetingPerson(item.Detail); person != "" {
			return pick(lang, "Prep for "+when+" with "+person, "准备"+when+"和"+person+"的会"),
				pick(lang, "Prep me for my "+when+" meeting with "+person+": what it's about and what I should bring.",
					"帮我准备"+when+"和"+person+"的会：会议内容和我该准备什么。"), true
		}
		return pick(lang, "Prep for "+title+" at "+when, "准备"+when+"的"+title),
			pick(lang, "Prep me for "+title+" at "+when+": what it's about and what I should bring.",
				"帮我准备"+when+"的"+title+"：会议内容和我该准备什么。"), true
	case section == mobileBriefSectionCommitments:
		return pick(lang, "Find time for "+title, "为“"+title+"”留出时间"),
			pick(lang, "Find time on my calendar for "+title+" and suggest a slot. Don't book anything.",
				"在我的日历里为“"+title+"”找一个时段并给出建议，先不要预订。"), true
	case section == mobileBriefSectionWorkingToward:
		return pick(lang, "What's left on "+title+"?", "“"+title+"”还差什么？"),
			pick(lang, "What's left on "+title+", and what's the next step?", "“"+title+"”还差什么？下一步是什么？"), true
	case section == mobileBriefSectionDeliverables:
		return pick(lang, "Open "+title, "打开"+title),
			pick(lang, "Open "+title+" and walk me through it.", "打开"+title+"，带我看一遍。"), true
	case brief.rankedEdition && item.Kind == "one_time_work" && item.Status == "needs_review":
		return pick(lang, "Open "+title, "打开"+title),
			pick(lang, "Open "+title+" and walk me through it.", "打开"+title+"，带我看一遍。"), true
	default:
		return "", "", false
	}
}

func mobileBriefParagraphChipLabel(brief MobileBriefV1, item MobileBriefItem, lang string) string {
	subject, section, ok := mobileBriefCardSubjectSection(brief, item.Refs)
	if !ok {
		return ""
	}
	label, _, ok := mobileBriefParagraphChipCopy(brief, section, subject, lang)
	if !ok {
		return ""
	}
	return mobileBriefQuestionChipLabel(label, lang)
}

func mobileBriefMeetingPerson(detail string) string {
	parts := strings.Split(detail, ";")
	if len(parts) > 0 && strings.EqualFold(strings.TrimSpace(parts[len(parts)-1]), "invite not answered") {
		parts = parts[:len(parts)-1]
	}
	if len(parts) < 3 {
		return ""
	}
	attendees := strings.TrimSpace(parts[2])
	for _, separator := range []string{",", "、", " and ", "和"} {
		attendees = strings.ReplaceAll(attendees, separator, "\x00")
	}
	for _, candidate := range strings.Split(attendees, "\x00") {
		person := strings.TrimSpace(candidate)
		lower := strings.ToLower(person)
		if person == "" || strings.ContainsAny(person, "@+0123456789") || strings.Contains(lower, " others") {
			continue
		}
		plausible := true
		for _, r := range person {
			if !unicode.IsLetter(r) && !unicode.IsSpace(r) && r != '.' && r != '\'' && r != '’' && r != '-' {
				plausible = false
				break
			}
		}
		if plausible {
			return person
		}
	}
	return ""
}

// Question labels are display-only and never replace the original message.
func mobileBriefQuestionChipLabel(value, lang string) string {
	label := strings.Join(strings.Fields(value), " ")
	startsWithAction := false
	if lang == "zh" {
		for _, prefix := range []string{"如何", "什么", "怎么", "为何", "为什么", "哪里", "何时", "哪些", "能否", "是否", "帮", "起草", "准备", "写", "总结", "检查", "查看", "继续", "安排", "给", "为", "打开"} {
			startsWithAction = startsWithAction || strings.HasPrefix(label, prefix)
		}
		startsWithAction = startsWithAction || strings.HasPrefix(label, "“") && strings.Contains(label, "？")
	} else if words := strings.Fields(label); len(words) > 0 {
		first := strings.ToLower(words[0])
		startsWithAction = mobileBriefCardLabelVerbs[first]
		for _, question := range []string{"what", "what's", "what’s", "where", "when", "why", "how", "who", "which", "can", "could", "should", "would", "will", "is", "are", "do", "does"} {
			startsWithAction = startsWithAction || first == question
		}
	}
	lower := strings.ToLower(label)
	if strings.Contains(lower, "this topic") || strings.Contains(label, "这个话题") ||
		strings.EqualFold(label, "What should I consider next?") || label == "如何继续推进？" || !startsWithAction {
		return ""
	}
	// Chips are written, never pasted or cut off (today-review v1.1.4 #5):
	// one that is too long, or already ends in an ellipsis, is not offered.
	if mobileBriefChipLabelWidth(label) > mobileBriefChipLabelMaxWidth || strings.HasSuffix(label, "…") || strings.HasSuffix(label, "...") {
		return ""
	}
	return label
}

// mobileBriefChipLabelMaxWidth is the widest chip label: 32 characters in
// English, 16 in Chinese (a Chinese character is two columns wide).
const mobileBriefChipLabelMaxWidth = 32

func mobileBriefChipLabelWidth(label string) int {
	width := 0
	for _, r := range label {
		if unicode.Is(unicode.Han, r) || (r >= 0x3000 && r <= 0x30ff) || (r >= 0xff00 && r <= 0xffef) {
			width += 2
		} else {
			width++
		}
	}
	return width
}

func mobileBriefLimitParagraphChips(brief MobileBriefV1, cards []MobileBriefCard) []MobileBriefCard {
	evidence := mobileBriefEvidenceByRef(brief)
	labels, sources, paragraphs := map[string]bool{}, map[string]bool{}, map[int]bool{}
	closingSources := map[string]bool{}
	if closing := mobileBriefClosingParagraph(brief); closing.Text != "" && len(brief.Narrative) > 1 {
		for _, id := range closing.Refs {
			if ref, ok := evidence[id]; ok && ref.SourceKind+ref.SourceID != "" {
				closingSources[ref.SourceKind+"\x00"+ref.SourceID] = true
			}
		}
	}
	count := 0
	out := make([]MobileBriefCard, 0, len(cards))
	for _, card := range cards {
		chip := card.Kind == mobileBriefCardQuestion || card.Kind == mobileBriefCardItem && card.Paragraph != nil
		if !chip {
			out = append(out, card)
			continue
		}
		lose := func() {
			if card.Kind == mobileBriefCardQuestion {
				return
			}
			card.Paragraph, card.Label = nil, ""
			out = append(out, card)
		}
		if card.Paragraph == nil || strings.TrimSpace(card.Label) == "" {
			lose()
			continue
		}
		index := *card.Paragraph
		label := strings.ToLower(strings.Join(strings.Fields(card.Label), " "))
		source := ""
		if len(card.Refs) > 0 {
			ref := evidence[card.Refs[0]]
			source = ref.SourceKind + "\x00" + ref.SourceID
		}
		// One offer per item: the closing question makes the offer for its
		// subject, so that subject gets no chip (today-review v1.1.4 #4).
		if index < 0 || index >= len(brief.Narrative) || paragraphs[index] || labels[label] ||
			source != "\x00" && sources[source] || count >= 3 || closingSources[source] {
			lose()
			continue
		}
		paragraphs[index], labels[label] = true, true
		if source != "\x00" {
			sources[source] = true
		}
		count++
		out = append(out, card)
	}
	return out
}
