package main

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// activitySecondPersonRule is added to every prompt whose words Activity
// shows (activity-review v1.0.0 P0 #1). secondPersonLine enforces it again
// on save and on read, so cached lines written before the rule follow it.
const activitySecondPersonRule = `Voice (required for every title, summary, description, step, notice and recap):
- Write to the person, in the second person: "You reviewed PR #12", "Your draft is open".
- Never write "the user", "the person", "this person" or the person's own name for them.
- Never phrase a guess ("This looks like…", "It seems…", "probably", "likely"): say what happened, or leave it out.
- In Chinese, write 你 for the person, never 用户 or 该用户.
- Speech marked "said aloud" or "heard" was picked up by the microphone or the computer's audio. It was not typed or sent, and who it was said to is unknown: never write that it was said to, told to, sent to or about the person whose chat, window or contact is on screen. Name who you talked with only when the speech itself names them.`

var (
	secondPersonGuess = regexp.MustCompile(`(?i)^(?:this|it|that)\s+(?:looks|seems|appears)\s+(?:like|to)\b|^(?:likely|probably|presumably)\b`)
	secondPersonZH    = regexp.MustCompile(`该用户|这位用户|用户`)
	secondPersonSplit = regexp.MustCompile(`[^.!?。！？\n]+[.!?。！？]*[ \t]*|\n`)
	secondPersonTheir = regexp.MustCompile(`\b(?:their|his|her)\b`)
	secondPersonSelf  = regexp.MustCompile(`\b(?:themselves|himself|herself)\b`)
)

var secondPersonVerb = map[string]string{
	"is": "are", "was": "were", "has": "have", "does": "do",
	"seems": "seem", "appears": "appear", "needs": "need", "wants": "want",
}

// activitySelfNames are the person's own names, from the display name, for
// secondPersonLine. The "the user" fallback of getUserDisplayName is no name.
func (a *App) activitySelfNames() []string {
	if a == nil {
		return nil
	}
	name := strings.TrimSpace(a.getUserDisplayName())
	if i := strings.Index(name, "@"); i > 0 {
		name = name[:i]
	}
	if name == "" || strings.EqualFold(name, "the user") {
		return nil
	}
	out := make([]string, 0, 4)
	for self := range threadSelfNames(name) {
		out = append(out, self)
	}
	if utf8.RuneCountInString(name) >= 2 && !containsFold(out, name) {
		out = append(out, name)
	}
	return out
}

func containsFold(list []string, s string) bool {
	for _, v := range list {
		if strings.EqualFold(v, s) {
			return true
		}
	}
	return false
}

// secondPersonLine rewrites a generated line so it talks to the person:
// "the user"/"the person"/their own name become "you" (with the verb
// agreeing), their possessive becomes "your", 用户 becomes 你, and a
// sentence that only guesses ("This looks like…") is dropped.
func secondPersonLine(text string, selfNames []string) string {
	if strings.TrimSpace(text) == "" {
		return text
	}
	subject := secondPersonSubjectFor(selfNames)
	var b strings.Builder
	for _, sentence := range secondPersonSplit.FindAllString(text, -1) {
		if sentence == "\n" {
			b.WriteString(sentence)
			continue
		}
		lead, body := secondPersonLead(sentence)
		if secondPersonGuess.MatchString(strings.TrimSpace(body)) {
			if lead != "" && strings.TrimSpace(lead) != "" {
				b.WriteString(lead)
			}
			continue
		}
		replaced := false
		body = subject.ReplaceAllStringFunc(body, func(m string) string {
			replaced = true
			return secondPersonWord(m)
		})
		if replaced {
			// Their own things are yours once they are "you".
			body = secondPersonTheir.ReplaceAllString(body, "your")
			body = secondPersonSelf.ReplaceAllString(body, "yourself")
		}
		body = secondPersonZH.ReplaceAllString(body, "你")
		b.WriteString(lead)
		b.WriteString(secondPersonCapitalize(body))
	}
	out := b.String()
	if strings.TrimSpace(out) == "" {
		return ""
	}
	return strings.TrimRight(out, " ")
}

func secondPersonSubjectFor(selfNames []string) *regexp.Regexp {
	alts := []string{`(?:the|this|that)\s+(?:user|person)`}
	for _, name := range selfNames {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		alts = append(alts, regexp.QuoteMeta(name))
	}
	return regexp.MustCompile(`(?i)\b(?:` + strings.Join(alts, "|") + `)(?:'s|’s)?(?:\s+(?:is|was|has|does|seems|appears|needs|wants))?\b`)
}

func secondPersonWord(m string) string {
	lower := strings.ToLower(m)
	for _, mark := range []string{"'s", "’s"} {
		if i := strings.Index(lower, mark); i >= 0 {
			return "your" + m[i+len(mark):]
		}
	}
	fields := strings.Fields(m)
	if last := strings.ToLower(fields[len(fields)-1]); secondPersonVerb[last] != "" && len(fields) > 1 {
		return "you " + secondPersonVerb[last]
	}
	return "you"
}

// secondPersonLead splits off Markdown markers and labels ("- ", "### ",
// "**Done:** ") so the sentence after them is the one checked.
func secondPersonLead(sentence string) (string, string) {
	i := 0
	for i < len(sentence) {
		r, size := utf8.DecodeRuneInString(sentence[i:])
		if unicode.IsSpace(r) || strings.ContainsRune("-*#>•", r) {
			i += size
			continue
		}
		break
	}
	lead, body := sentence[:i], sentence[i:]
	if j := strings.Index(body, ":"); j > 0 && j <= 14 && !strings.Contains(body[:j], " ") {
		k := j + 1
		for k < len(body) && (body[k] == ' ' || body[k] == '*') {
			k++
		}
		lead += body[:k]
		body = body[k:]
	}
	return lead, body
}

func secondPersonCapitalize(body string) string {
	for _, w := range []string{"you", "your"} {
		if strings.HasPrefix(body, w+" ") || strings.HasPrefix(body, w+"'") || body == w {
			return strings.ToUpper(body[:1]) + body[1:]
		}
	}
	return body
}

func secondPersonMoments(moments []MomentData, selfNames []string) []MomentData {
	for i := range moments {
		moments[i].Title = secondPersonLine(moments[i].Title, selfNames)
		moments[i].Subtitle = secondPersonLine(moments[i].Subtitle, selfNames)
		moments[i].Description = secondPersonLine(moments[i].Description, selfNames)
	}
	return moments
}

func (a *App) secondPersonDaily(data *DailySummaryData) {
	if data == nil {
		return
	}
	self := a.activitySelfNames()
	data.AISummary = secondPersonLine(data.AISummary, self)
	data.Moments = secondPersonMoments(data.Moments, self)
}

func (a *App) secondPersonNoticed(items []NoticedSuggestion) {
	self := a.activitySelfNames()
	for i := range items {
		items[i].Text = secondPersonLine(items[i].Text, self)
		items[i].Why = secondPersonLine(items[i].Why, self)
		items[i].MomentTitle = secondPersonLine(items[i].MomentTitle, self)
	}
}

func (a *App) secondPersonThreads(day *ActivityDayThreads) {
	self := a.activitySelfNames()
	for i := range day.Threads {
		if !day.Threads[i].UserTitled {
			day.Threads[i].Title = secondPersonLine(day.Threads[i].Title, self)
		}
	}
}

func (a *App) secondPersonTimeline(groups []ActivityTimelineGroup) {
	self := a.activitySelfNames()
	for i := range groups {
		if s := groups[i].Summary; s != nil {
			s.Title = secondPersonLine(s.Title, self)
			s.Description = secondPersonLine(s.Description, self)
		}
	}
}

func secondPersonOutcome(o *ActivityThreadOutcome, self []string) {
	if o == nil {
		return
	}
	o.Summary = secondPersonLine(o.Summary, self)
	o.NextStep = secondPersonLine(o.NextStep, self)
	o.Question = secondPersonLine(o.Question, self)
}
