package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Offers are draft text for main chat or navigation to Activity, never
// execution authority. Refs retain the same full-source delivery gate as the
// closing paragraph and its audio.
type MobileBriefOffer struct {
	ID string `json:"id"`
	// Action is "send" when choosing the offer starts the work in main chat,
	// and "prefill" when the user should read and edit the request first.
	// Briefs stored before 2026-10-19 carry the offer's kind here instead;
	// readers treat legacy values as "prefill". "open_activity" opens an
	// editable suggestion on the computer and is omitted from phone copies.
	Action string `json:"action"`
	// Kind says what the offer does: plan, reply, follow_up, order,
	// note_decision, prepare or review.
	Kind string `json:"kind,omitempty"`
	// Label is the chip text and names the subject; Prompt is the whole
	// request main chat receives; Text is the offer as its own question.
	Label  string                     `json:"label"`
	Prompt string                     `json:"prompt"`
	Text   string                     `json:"text"`
	Refs   []string                   `json:"refs"`
	Target *MobileBriefActivityTarget `json:"target,omitempty"`
}

const (
	mobileBriefOfferSend    = "send"
	mobileBriefOfferPrefill = "prefill"
	// mobileBriefOfferLabelRunes keeps a chip to one short line.
	mobileBriefOfferLabelRunes = 36
)

// mobileBriefOfferDraft is an offer with the clause the closing question
// uses for it ("plan the next steps for the Halcyon Arc lamp follow-up").
type mobileBriefOfferDraft struct {
	offer   MobileBriefOffer
	phrase  string
	subject string
	// tier orders the offers: the item's attention rank (1 to 7), then other
	// forward work with something behind it, then bare titles, then
	// Highlights.
	tier int
	// planSubject is set on the plain planning offer, whose clause two offers
	// can share: "plan the next steps for “A” or “B”".
	planSubject string
}

func mobileBriefConversationOffers(brief MobileBriefV1, language string) []MobileBriefOffer {
	var out []MobileBriefOffer
	for _, draft := range mobileBriefOfferDrafts(brief, language) {
		out = append(out, draft.offer)
	}
	return out
}

// mobileBriefOfferDrafts picks at most two offers: the brief's picks first,
// in their order (mobileBriefPicks), so the written closing can offer one of
// them; then items that need attention, by tier, then other forward work in
// brief order, preferring work Isoform has something on over a bare title
// ("Run payroll" with nothing recorded), and Highlights last. Two offers
// never share a subject or a label.
func mobileBriefOfferDrafts(brief MobileBriefV1, language string) []mobileBriefOfferDraft {
	return mobileBriefPickDrafts(mobileBriefOfferCandidates(brief, language))
}

// mobileBriefPickDrafts keeps at most two offers, one per subject and label.
func mobileBriefPickDrafts(candidates []mobileBriefOfferDraft) []mobileBriefOfferDraft {
	var out []mobileBriefOfferDraft
	seen := map[string]bool{}
	for _, draft := range candidates {
		label, subject := strings.ToLower(draft.offer.Label), "subject:"+strings.ToLower(draft.subject)
		if seen[label] || seen[subject] || draft.offer.Kind == "order" && seen["kind:order"] {
			continue
		}
		seen[label], seen[subject], seen["kind:"+draft.offer.Kind] = true, true, true
		draft.offer.ID = fmt.Sprintf("offer-%d", len(out)+1)
		out = append(out, draft)
		if len(out) == 2 {
			break
		}
	}
	return out
}

// mobileBriefOfferCandidates is every offer the brief could make, one per
// subject, in the order mobileBriefOfferDrafts takes them. Readers match a
// stored button to its subject here, so a brief written before the picks
// keeps its buttons.
func mobileBriefOfferCandidates(brief MobileBriefV1, language string) []mobileBriefOfferDraft {
	lang := mobileBriefVoiceLang(language)
	reference := mobileBriefTextReference(brief)
	allowed := map[string]bool{}
	for _, ref := range mobileBriefNarrativeInputEvidence(brief) {
		allowed[ref.Ref] = true
	}
	// Highlights drop the outcome body from their display projection. Recover
	// only the validated typed state from the matching primary source row.
	outcomes := map[string]*ActivityThreadOutcome{}
	for _, items := range [][]MobileBriefItem{brief.Sections.Moved, brief.Sections.Doing, brief.Sections.OpenLoops} {
		for _, item := range items {
			if item.Kind == mobileBriefThreadOutcomeSource && item.Outcome != nil && len(item.Refs) > 0 {
				outcomes[item.Refs[0]] = item.Outcome
			}
		}
	}
	var candidates []mobileBriefOfferDraft
	used := map[string]bool{}
	evidence := mobileBriefEvidenceByRef(brief)
	picks := mobileBriefPicks(brief)
	consider := func(section string, item MobileBriefItem, tier int) {
		if len(item.Refs) == 0 {
			return
		}
		if due := mobileBriefParseTime(item.DueAt); len(picks) > 0 && !due.IsZero() && mobileBriefCivilDays(due, reference, mobileBriefNarrativeLocation(brief)) < -mobileBriefPickOverdueDays {
			return
		}
		if section == mobileBriefSectionDoing || section == mobileBriefSectionLearned || section == "Highlights" {
			switch evidence[item.Refs[0]].SourceKind {
			case mobileBriefFileSource, "memory", "knowledge":
				return
			}
		}
		outcome := item.Outcome
		if outcome == nil && item.Kind == mobileBriefThreadOutcomeSource {
			outcome = outcomes[item.Refs[0]]
		}
		if outcome != nil || item.Kind == mobileBriefThreadOutcomeSource {
			item.Title = redactThreadOutcomeText(item.Title)
			item.NextStep = redactThreadOutcomeText(item.NextStep)
		}
		draft, ok := mobileBriefOfferFor(brief, section, item, outcome, lang)
		if !ok || mobileBriefBannedOutcomePhrase(draft.phrase+" "+draft.offer.Label+" "+draft.offer.Prompt) {
			return
		}
		used[item.Refs[0]] = true
		switch true {
		case tier != 0:
			draft.tier = tier
		case section == "Highlights":
			draft.tier = mobileBriefAttentionStalled + 3
		case item.AttentionRank > 0:
			draft.tier = item.AttentionRank
		case mobileBriefOfferGrounded(brief, item, outcome):
			draft.tier = mobileBriefAttentionStalled + 1
		default:
			draft.tier = mobileBriefAttentionStalled + 2
		}
		draft.offer.Refs = append([]string{}, item.Refs...)
		draft.phrase = normalizeBriefText(draft.phrase, language, reference, false)
		draft.offer.Prompt = normalizeBriefText(draft.offer.Prompt, language, reference, false)
		draft.offer.Text = pick(lang, "Want me to "+draft.phrase+"?", "要我"+draft.phrase+"吗？")
		candidates = append(candidates, draft)
	}
	// The picks lead, ahead of every attention tier: the first of each kind
	// in pick order, then the rest, so the two buttons are not two of a kind
	// (a meeting to prepare and a follow-up to write, not two meetings).
	firstOfTier := map[string]bool{}
	for i, p := range picks {
		tier := i - len(picks)
		if p.Tier == mobileBriefPickNoticed {
			tier = -1
		} else if !firstOfTier[p.Tier] {
			firstOfTier[p.Tier] = true
			tier -= len(picks)
		}
		consider(p.Section, p.Item, tier)
	}
	for _, section := range mobileBriefNarrativeSections(brief) {
		for _, item := range section.items {
			if len(item.Refs) == 0 || item.ChatMessageID != "" || !mobileBriefItemNarratable(item, allowed) || used[item.Refs[0]] || len(picks) > 0 && item.Kind == mobileBriefThreadOutcomeSource {
				continue
			}
			consider(section.name, item, 0)
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].tier < candidates[j].tier })
	return candidates
}

// mobileBriefOfferGrounded reports an item Isoform can do real work on: a
// recorded outcome or next step, evidence beyond its own record, a calendar
// slot ahead, a person waiting, a skipped call, or a recognizable subject.
func mobileBriefOfferGrounded(brief MobileBriefV1, item MobileBriefItem, outcome *ActivityThreadOutcome) bool {
	if outcome != nil || strings.TrimSpace(item.NextStep) != "" || len(item.Refs) > 1 ||
		item.Kind == "waiting_on_reply" || mobileBriefFutureCalendarItem(brief, item) {
		return true
	}
	if item.Kind == mobileBriefAttendanceSource {
		return item.Status == "skipped"
	}
	title := strings.TrimRight(mobileBriefText(mobileBriefVoiceLabel(item.Title), 140), " .。")
	context, _ := mobileBriefSubjectContext(brief, item, title, true)
	return context != ""
}

// mobileBriefOfferFor writes one offer for an item: a chip naming the subject,
// the clause for the closing question, and a prompt that says what to produce
// and what to use. The clause is said aloud and reaches the narrative model
// with the closing, so it names the subject plainly and never carries a
// Purpose's desired outcome; the prompt goes only to main chat.
func mobileBriefOfferFor(brief MobileBriefV1, section string, item MobileBriefItem, outcome *ActivityThreadOutcome, lang string) (mobileBriefOfferDraft, bool) {
	reference, loc := mobileBriefTextReference(brief), mobileBriefNarrativeLocation(brief)
	clean := func(value string) string {
		return strings.TrimRight(mobileBriefText(mobileBriefVoiceLabel(value), 140), " .。")
	}
	short := func(value string) string {
		return strings.TrimRight(agencyBoundedText(value, mobileBriefOfferLabelRunes), " .。")
	}
	if section == mobileBriefSectionNoticed && item.Kind == "noticed" {
		return mobileBriefNoticedOffer(brief, item, lang)
	}
	title := clean(item.Title)
	if title == "" || mobileBriefBannedOutcomePhrase(title) {
		return mobileBriefOfferDraft{}, false
	}
	// The subject as said in the clause ("the Halcyon Arc lamp follow-up"), and
	// as written in a main-chat prompt.
	subject := mobileBriefItemNoun(title, item.Kind, lang)
	if item.Kind == mobileBriefAttendanceSource || mobileBriefFutureCalendarItem(brief, item) {
		subject = mobileBriefNounSubject(title, lang)
	}
	quoted := pick(lang, title, "“"+title+"”")
	since := mobileBriefSinceWords(mobileBriefParseTime(item.Since), reference, loc, lang)
	draft := func(kind, action, label, phrase, prompt string) (mobileBriefOfferDraft, bool) {
		return mobileBriefOfferDraft{
			offer:   MobileBriefOffer{Action: action, Kind: kind, Label: label, Prompt: prompt},
			phrase:  phrase,
			subject: title,
		}, true
	}
	next := clean(item.NextStep)
	switch true {
	case item.Kind == "waiting_on_reply":
		waiting := ""
		if since != "" {
			waiting = pick(lang, ", who has been waiting on my reply "+since, "，对方"+since+"一直在等我回复")
		}
		return draft("reply", mobileBriefOfferSend,
			pick(lang, "Reply to "+short(title), "回复"+short(title)),
			pick(lang, "draft your reply to "+title, "起草给"+title+"的回复"),
			pick(lang, "Help me reply to "+title+waiting+". Open the mail thread they are waiting on, tell me in two lines what they asked, and draft a reply in my voice for me to review. Do not send anything.",
				"帮我回复"+title+waiting+"。打开对方在等的邮件，用两句话告诉我对方问了什么，再用我的语气起草一封回复给我过目。不要发送任何内容。"))
	case outcome != nil && outcome.Action == "order" && (outcome.State == "cart_left" || outcome.State == "checkout_started"):
		order, ok := draft("order", mobileBriefOfferPrefill,
			pick(lang, "Finish the order", "完成这笔订单"),
			pick(lang, "help you finish that order", "帮你完成这笔订单"),
			pick(lang, "Help me finish the order I left: "+title+". Show me what is in the cart and what is left before checkout. Do not place the order until I confirm.",
				"帮我完成没下完的订单：“"+title+"”。告诉我购物车里有什么、结账前还差哪一步。在我确认之前不要下单。"))
		order.subject = "order:" + title
		return order, ok
	case outcome != nil && (outcome.State == "conclusion" || outcome.State == "decision" || outcome.State == "decision_recorded"):
		if next != "" {
			return draft("note_decision", mobileBriefOfferSend, short(next),
				pick(lang, "help you "+mobileBriefLowerFirst(next), "帮你"+next),
				pick(lang, "This is settled: "+title+". Help me with the next step: "+mobileBriefLowerFirst(next)+". Use what Isoform recorded about it and show me the result.",
					"这件事已经确定：“"+title+"”。帮我完成下一步："+next+"。用 Isoform 记录的内容，并把结果给我看。"))
		}
		return draft("note_decision", mobileBriefOfferSend,
			pick(lang, "Add the decision to my plan", "把决定记入计划"),
			pick(lang, "add that decision to your plan", "把这个决定记入计划"),
			pick(lang, "This is settled: "+title+". Add it to my plan and suggest the next step it leads to.",
				"这件事已经确定：“"+title+"”。把它记入我的计划，并建议接下来的一步。"))
	case section == "Highlights":
		// A finished item offers only a recorded decision; anything else would
		// presume a next step that nothing recorded.
		return mobileBriefOfferDraft{}, false
	case outcome != nil:
		if next != "" {
			return draft("plan", mobileBriefOfferSend, short(next),
				pick(lang, "help you "+mobileBriefLowerFirst(next), "帮你"+next),
				pick(lang, "Where this stopped: "+title+". Help me with the next step: "+mobileBriefLowerFirst(next)+". Do the part you can with what Isoform recorded, and tell me what is left for me.",
					"停下的地方：“"+title+"”。帮我完成下一步："+next+"。用 Isoform 记录的内容先做你能做的部分，再告诉我还剩什么要我来做。"))
		}
		return draft("plan", mobileBriefOfferSend,
			pick(lang, "Pick up where I left off", "从上次停下的地方继续"),
			pick(lang, "help you pick up where you left off", "帮你从上次停下的地方继续"),
			pick(lang, "Where this stopped: "+title+". Tell me what is left and the first step to finish it, using what Isoform recorded.",
				"停下的地方：“"+title+"”。用 Isoform 记录的内容告诉我还剩什么，以及收尾的第一步。"))
	case item.Kind == mobileBriefAttendanceSource && item.Status == "skipped":
		return draft("follow_up", mobileBriefOfferSend,
			pick(lang, "Follow up on "+short(title), "跟进"+short(title)),
			pick(lang, "draft a follow-up for "+subject, "起草"+subject+"的跟进消息"),
			pick(lang, "Draft a short follow-up for "+title+". Isoform has no record of the call on this computer during its slot, so offer to catch up or find a new time. Show me the draft and do not send it.",
				"为“"+title+"”起草一条简短的跟进消息。Isoform 在这台电脑上没有这次通话的记录，所以提出补上沟通或另约时间。把草稿给我看，不要发送。"))
	case item.Kind == mobileBriefAttendanceSource && item.Status == "unknown":
		return draft("follow_up", mobileBriefOfferPrefill,
			pick(lang, "Follow up on "+short(title), "跟进"+short(title)),
			pick(lang, "help you follow up on "+subject, "帮你跟进"+subject),
			pick(lang, "Help me follow up on "+title+". First ask me whether I joined it on another device, then draft a short follow-up for me to review. Do not send it.",
				"帮我跟进“"+title+"”。先问我是否在别的设备上参加了，再起草一条简短的跟进消息给我过目。不要发送。"))
	case item.Status == "waiting" && item.Kind != "commitment":
		return draft("follow_up", mobileBriefOfferSend,
			pick(lang, "Follow up on "+short(title), "跟进"+short(title)),
			pick(lang, "draft a follow-up on "+subject, "起草"+subject+"的跟进消息"),
			pick(lang, "Draft a short follow-up about "+quoted+"."+mobileBriefOfferFacts(brief, item, lang)+" Show me the draft and do not send it.",
				"起草一条关于“"+title+"”的简短跟进消息。"+mobileBriefOfferFacts(brief, item, lang)+"把草稿给我看，不要发送。"))
	case item.Kind == "commitment" && strings.TrimSpace(item.DueAt) == "" && mobileBriefPersonFollowUp.MatchString(title):
		// A follow-up the user owes someone: the step is the note itself.
		about := strings.TrimSpace(mobileBriefFollowUpLead.ReplaceAllString(title, ""))
		if about == "" {
			about = title
		}
		short := strings.TrimSpace(strings.SplitN(about, ":", 2)[0])
		return draft("follow_up", mobileBriefOfferSend,
			pick(lang, "Follow up on "+agencyBoundedText(short, mobileBriefOfferLabelRunes-13), "跟进"+agencyBoundedText(short, mobileBriefOfferLabelRunes-2)),
			pick(lang, "draft the follow-up on "+short, "起草"+short+"的跟进消息"),
			pick(lang, "Draft the follow-up I owe: "+quoted+"."+mobileBriefOfferFacts(brief, item, lang)+" Write it in my voice for me to review, and do not send it.",
				"起草我欠下的跟进消息：“"+title+"”。"+mobileBriefOfferFacts(brief, item, lang)+"用我的语气写好给我过目，不要发送。"))
	case item.Kind == "meeting" && item.Status == "now":
		return draft("prepare", mobileBriefOfferSend,
			pick(lang, "Prep for "+short(mobileBriefSpokenTitle(title)), "准备"+short(title)),
			pick(lang, "get you ready for "+subject+", which is on now", "帮你准备正在进行的"+subject),
			pick(lang, "Help me get ready for "+title+", which is on now. In a few lines: what it is about, who is in it, and anything open with them that Isoform knows of.",
				"帮我准备正在进行的“"+title+"”。用几句话告诉我：会议主题、参会的人，以及 Isoform 知道的与他们之间未完成的事。"))
	case mobileBriefFutureCalendarItem(brief, item):
		when := mobileBriefWhen(item, reference, loc, lang)
		return draft("prepare", mobileBriefOfferSend,
			pick(lang, "Prep for "+short(mobileBriefSpokenTitle(title)), "准备"+short(title)),
			pick(lang, "help you prepare for "+subject+" "+when, "帮你准备"+when+"的"+subject),
			pick(lang, "Help me prepare for "+title+" "+when+". Gather what Isoform knows that relates to it: recent work on the topic, open items with the people in it, and my notes. Give me a short prep list.",
				"帮我准备"+when+"的“"+title+"”。收集 Isoform 掌握的相关内容：最近相关的工作、与参会的人之间未完成的事、我的笔记。给我一份简短的准备清单。"))
	case item.Kind == "one_time_work" && item.Status == "blocked":
		return draft("review", mobileBriefOfferSend,
			pick(lang, "Unblock "+short(title), "继续"+short(title)),
			pick(lang, "show you what "+subject+" needs from you", "告诉你"+subject+"需要你做什么"),
			pick(lang, title+" is blocked and waiting for me. Tell me exactly what it needs from me, with what you already have, so the work can continue.",
				"“"+title+"”已暂停，在等我处理。用你已有的信息告诉我它具体需要我做什么，好让工作继续。"))
	case item.Kind == "one_time_work" && item.Status == "needs_review":
		return draft("review", mobileBriefOfferSend,
			pick(lang, "Review "+short(title), "审阅"+short(title)),
			pick(lang, "walk you through "+subject, "带你看一遍"+subject),
			pick(lang, "Walk me through "+quoted+": what was done, what needs my decision, and what is left.",
				"带我看一遍“"+title+"”：做了什么、哪些需要我决定、还剩什么。"))
	}
	facts := mobileBriefOfferFacts(brief, item, lang)
	// The concrete deliverable first: the next step on the map, then a task
	// the title itself names; planning the next steps is the fallback.
	if next != "" {
		phrase := pick(lang, "help you "+mobileBriefLowerFirst(next), "帮你"+next)
		if !strings.Contains(strings.ToLower(next), strings.ToLower(title)) {
			phrase = pick(lang, phrase+" for "+subject, "帮你为"+subject+next)
		}
		return draft("plan", mobileBriefOfferSend, short(next), phrase,
			pick(lang, "Help me with the next step for "+quoted+": "+mobileBriefLowerFirst(next)+"."+facts+" Do the part you can with what Isoform recorded, and tell me what is left for me.",
				"帮我完成“"+title+"”的下一步："+next+"。"+facts+"用 Isoform 记录的内容先做你能做的部分，再告诉我还剩什么要我来做。"))
	}
	due := mobileBriefParseTime(item.DueAt)
	if task, ok := mobileBriefTaskPhrase(title, item.Kind, lang); ok {
		when := ""
		if !due.IsZero() {
			when = " today"
			if days := mobileBriefCivilDays(due, reference, loc); days > 0 {
				when = " by " + mobileBriefDayWords(due, reference, loc, lang)
			}
		}
		return draft("plan", mobileBriefOfferSend, short(title), "help you "+task+when,
			"Help me "+task+when+"."+facts+" Do the part you can with what Isoform recorded, and tell me what is left for me.")
	}
	if !due.IsZero() && lang == "zh" {
		when := "今天"
		if days := mobileBriefCivilDays(due, reference, loc); days > 0 {
			when = "在" + mobileBriefDayWords(due, reference, loc, lang) + "前"
		}
		return draft("plan", mobileBriefOfferSend, "完成"+short(title), "帮你"+when+"完成"+subject,
			"帮我"+when+"完成“"+title+"”。"+facts+"用 Isoform 记录的内容先做你能做的部分，再告诉我还剩什么要我来做。")
	}
	plan, ok := draft("plan", mobileBriefOfferSend,
		pick(lang, "Plan: "+short(title), "规划："+short(title)),
		pick(lang, "plan the next steps for "+subject, "规划"+subject+"的下一步"),
		pick(lang, "Help me plan the next steps for "+quoted+"."+facts+" Give me the three most useful next steps and the first one I can finish today, using what Isoform recorded about it. Ask me only for what is missing.",
			"帮我规划“"+title+"”的下一步。"+facts+"用 Isoform 记录的内容，给我最有用的三个下一步，以及今天就能完成的第一步。只问我缺少的信息。"))
	plan.planSubject = subject
	return plan, ok
}

// mobileBriefSubjectContext is what makes a vague title ("Discovery")
// recognizable: the Purpose's desired outcome, read from its legacy open-loop
// copy, or the next step on the map; next reports the latter. details=true
// allows the desired outcome. Its callers are the template and main-chat
// prompts; the spoken closing and the narrative model prompt never use it.
func mobileBriefSubjectContext(brief MobileBriefV1, item MobileBriefItem, title string, details bool) (string, bool) {
	if title == "" || !mobileBriefVagueTitle(title) || item.Kind == "waiting_on_reply" || item.Status == "no_recent_touch" || item.Kind == mobileBriefAttendanceSource {
		return "", false
	}
	usable := func(context string) string {
		context = strings.TrimRight(mobileBriefVoiceLabel(context), " .。")
		if context == "" || strings.EqualFold(context, title) || mobileBriefBannedOutcomePhrase(context) || threadOutcomeTypingNarration.MatchString(context) {
			return ""
		}
		return strings.TrimRight(agencyBoundedText(context, 60), " .。")
	}
	if details && len(item.Refs) > 0 {
		for _, loop := range brief.Sections.OpenLoops {
			if len(loop.Refs) > 0 && loop.Refs[0] == item.Refs[0] && loop.Kind != "one_time_work" && !loop.EvidenceUnavailable {
				if context := usable(loop.Detail); context != "" {
					return context, false
				}
			}
		}
	}
	if context := usable(item.NextStep); context != "" {
		return context, true
	}
	return "", false
}

// mobileBriefVagueTitle is a title too short to recognize on its own: one or
// two words, or up to four Han characters.
func mobileBriefVagueTitle(title string) bool {
	for _, r := range title {
		if unicode.Is(unicode.Han, r) {
			return utf8.RuneCountInString(title) <= 4
		}
	}
	return len(strings.Fields(title)) <= 2 && utf8.RuneCountInString(title) <= 24
}

// mobileBriefOfferFacts is what the brief already knows about an item, as one
// sentence for an offer's prompt: its age, its due day, the recorded state,
// the goal and the next step on the map. Main chat receives it; the
// narrative model never does.
func mobileBriefOfferFacts(brief MobileBriefV1, item MobileBriefItem, lang string) string {
	reference, loc := mobileBriefTextReference(brief), mobileBriefNarrativeLocation(brief)
	var facts []string
	if since := mobileBriefSinceWords(mobileBriefParseTime(item.Since), reference, loc, lang); since != "" {
		facts = append(facts, pick(lang, "open "+since, since+"一直未结"))
	}
	if due := mobileBriefDueWords(mobileBriefParseTime(item.DueAt), reference, loc, lang); due != "" {
		facts = append(facts, pick(lang, "it "+due, due))
	}
	if item.AttentionRank == mobileBriefAttentionStalled && item.AttentionReason != "" {
		facts = append(facts, pick(lang, mobileBriefLowerFirst(item.AttentionReason), item.AttentionReason))
	}
	title := strings.TrimRight(mobileBriefText(mobileBriefVoiceLabel(item.Title), 140), " .。")
	if context, next := mobileBriefSubjectContext(brief, item, title, true); context != "" && !next {
		facts = append(facts, pick(lang, "the goal is "+mobileBriefLowerFirst(context), "目标是"+context))
	}
	if next := strings.TrimRight(mobileBriefVoiceLabel(item.NextStep), " .。"); next != "" {
		facts = append(facts, pick(lang, "the next step in my plan is to "+mobileBriefLowerFirst(next), "计划里的下一步是"+next))
	}
	if len(facts) == 0 {
		return ""
	}
	return pick(lang, " What Isoform has: "+strings.Join(facts, "; ")+".", "Isoform 记录的情况："+strings.Join(facts, "；")+"。")
}

// mobileBriefLowerFirst lowercases the capital of a known common first word
// ("Draft the plan" -> "draft the plan"), never a name or an acronym ("Dana's
// onboarding doc", "RTK").
func mobileBriefLowerFirst(value string) string {
	runes := []rune(value)
	if len(runes) < 2 || !unicode.IsUpper(runes[0]) || !unicode.IsLower(runes[1]) {
		return value
	}
	first := strings.ToLower(strings.TrimRight(strings.SplitN(value, " ", 2)[0], ",.;:"))
	if !mobileBriefTaskVerbs[first] && !mobileBriefLooseVerbs[first] && !mobileBriefArticles[first] && !mobileBriefCommonLeadWords[first] {
		return value
	}
	runes[0] = unicode.ToLower(runes[0])
	return string(runes)
}

// mobileBriefClosing is the brief's last paragraph: one question that names
// the subjects plainly and offers the work ("Want me to help you draft the
// interview guide for Discovery, or draft your reply to Dana?").
func mobileBriefClosing(brief MobileBriefV1, language string) (MobileBriefPara, []MobileBriefOffer) {
	return mobileBriefClosingFrom(brief, mobileBriefOfferDrafts(brief, language), language)
}

func mobileBriefClosingFrom(brief MobileBriefV1, drafts []mobileBriefOfferDraft, language string) (MobileBriefPara, []MobileBriefOffer) {
	if len(drafts) == 0 {
		return MobileBriefPara{Text: "", Refs: uniqueSortedStrings(nil)}, nil
	}
	// One question about one thing, with its one button and Not now
	// (today-review v1.1.17 D18). Any other offer stays a line in the brief.
	var offers []MobileBriefOffer
	for _, draft := range drafts {
		offers = append(offers, draft.offer)
	}
	text := drafts[0].offer.Text
	return MobileBriefPara{Text: text, Refs: uniqueSortedStrings(append([]string(nil), drafts[0].offer.Refs...))}, offers
}

// mobileBriefDeliveredClosing is the question a delivered brief ends on: for
// written text, its last paragraph when that is a question (the model writes
// its own since backlog item 65), and none otherwise; for the template,
// Isoform's closing.
func mobileBriefDeliveredClosing(brief MobileBriefV1, language string) MobileBriefPara {
	if brief.NarrativeStatus == "ok" {
		if n := len(brief.Narrative); n > 0 && strings.ContainsAny(brief.Narrative[n-1].Text, "?？") {
			return brief.Narrative[n-1]
		}
		return MobileBriefPara{}
	}
	closing, _ := mobileBriefClosing(brief, language)
	return closing
}

// A revision key binds the editable draft to exactly the displayed brief text,
// offers and evidence. Audio status and mutable Today lists do not change it.
func mobileBriefConversationKey(brief MobileBriefV1) string {
	// Navigation is derived at read time. A rename, withdrawal or code revert
	// must not strand a draft for otherwise unchanged prose and evidence.
	brief = mobileBriefWithoutParagraphTopics(brief)
	brief.WeeklyRecap = mobileBriefParasWithoutTopics(brief.WeeklyRecap)
	raw, _ := json.Marshal(struct {
		ID          string
		AsOf        string
		Narrative   []MobileBriefPara
		Offers      []MobileBriefOffer
		Evidence    []MobileBriefRef
		WeeklyRecap []MobileBriefPara
	}{brief.BriefID, brief.AsOf, brief.Narrative, brief.Offers, brief.Evidence, brief.WeeklyRecap})
	return agencyPayloadHash(raw)
}
