package brief

import (
	"regexp"
	"strings"
)

// Day and time tokens (docs/brief-writer-tokens.md). The model writes
// {when:eN} where an item's day and time go, and never a day word, weekday,
// date or clock of its own. The app renders each token at read time from the
// item's time and the reader's day (wails-gui/brief_tokens.go, through the
// brief's clock), so a brief's day words cannot drift from its date.

// Token validation codes.
const (
	CodeFreeDayWord    = "free_day_word"
	CodeTokenUncited   = "token_uncited"
	CodeTokenUntimed   = "token_untimed"
	CodeTokenMalformed = "token_malformed"
)

var whenToken = regexp.MustCompile(`\{when:([a-z]\d+)\}`)

// A brace that opens a token but does not close one ("{when:e3", "{when:}").
var tokenLike = regexp.MustCompile(`\{\s*when\b[^}]*\}?`)

// Words the writer may not write itself: relative days, weekdays, month-day
// dates and clock times, in English and Chinese. A month name counts only with
// a day number ("may" is a word).
var freeDayWords = regexp.MustCompile(`(?i)\b(?:today|tonight|tomorrow|yesterday|mondays?|tuesdays?|wednesdays?|thursdays?|fridays?|saturdays?|sundays?)\b` +
	`|\b(?:jan(?:uary)?|feb(?:ruary)?|mar(?:ch)?|apr(?:il)?|may|june?|july?|aug(?:ust)?|sept?(?:ember)?|oct(?:ober)?|nov(?:ember)?|dec(?:ember)?)\.?\s+\d{1,2}(?:st|nd|rd|th)?\b` +
	`|\b\d{1,2}(?::\d{2})?\s*(?:a\.?m\.?|p\.?m\.?)(?:\W|$)|\b\d{1,2}:\d{2}\b` +
	`|今天|今晚|明天|昨天|周[一二三四五六日天]|星期[一二三四五六日天]|\d{1,2}月\d{1,2}[日号]|\d{1,2}点`)

// TokenRefs lists the refs that a text's when tokens name, in order.
func TokenRefs(text string) []string {
	var refs []string
	for _, m := range whenToken.FindAllStringSubmatch(text, -1) {
		refs = append(refs, m[1])
	}
	return refs
}

// ValidateTokens checks one written paragraph against the token rules: every
// token names a ref the paragraph cites and that has a time (timed), every
// token is well formed, and no day, date or clock words are written outright.
// It returns the codes that apply, sorted by first failure; none means valid.
func ValidateTokens(p Paragraph, timed map[string]bool) []string {
	var codes []string
	add := func(code string) {
		for _, c := range codes {
			if c == code {
				return
			}
		}
		codes = append(codes, code)
	}
	cited := make(map[string]bool, len(p.Refs))
	for _, ref := range p.Refs {
		cited[ref] = true
	}
	for _, ref := range TokenRefs(p.Text) {
		if !cited[ref] {
			add(CodeTokenUncited)
		}
		if !timed[ref] {
			add(CodeTokenUntimed)
		}
	}
	rest := whenToken.ReplaceAllString(p.Text, " ")
	if tokenLike.MatchString(rest) {
		add(CodeTokenMalformed)
	}
	if freeDayWords.MatchString(rest) {
		add(CodeFreeDayWord)
	}
	return codes
}

// StripTokens removes every token and the space it leaves, for a text that
// must be read without the app's rendering (a log line, a length count).
func StripTokens(text string) string {
	return strings.Join(strings.Fields(whenToken.ReplaceAllString(text, "")), " ")
}

// ReplaceTokens calls render for each token's ref and puts its result in place.
// A token whose ref render cannot place (render returns "") is left out.
func ReplaceTokens(text string, render func(ref string) string) string {
	out := whenToken.ReplaceAllStringFunc(text, func(token string) string {
		return render(whenToken.FindStringSubmatch(token)[1])
	})
	return strings.Join(strings.Fields(out), " ")
}

// Token codes in the writer's repair log.
const (
	CodeTokenKept     = "token_template"
	CodeTokenFallback = "token_fallback"
)

// tokenParagraph reads a tokens-mode paragraph. Every item a token names is
// cited. When the paragraph passes ValidateTokens it keeps its tokenized text
// as the template; either way the returned text has each token replaced by the
// item's when word for the brief's own day (the words the writer gave it), so
// a reader that knows nothing of tokens reads the same words as before.
func (m Material) tokenParagraph(text string, refs []string, codes *[]string) (string, string, []string) {
	ids := TokenRefs(text)
	if len(ids) == 0 {
		return text, "", refs
	}
	byID := make(map[string]MaterialItem, len(m.Items))
	timed := map[string]bool{}
	for _, item := range m.Items {
		byID[item.ID] = item
		if item.When != "" && item.Group != GroupContext {
			timed[item.ID] = true
		}
	}
	cited := map[string]bool{}
	for _, r := range refs {
		cited[strings.TrimSpace(r)] = true
	}
	for _, id := range ids {
		if timed[id] && !cited[id] {
			refs = append(refs, id)
			cited[id] = true
		}
	}
	template := ""
	if len(ValidateTokens(Paragraph{Text: text, Refs: refs}, timed)) == 0 {
		template = text
		*codes = append(*codes, CodeTokenKept)
	} else {
		*codes = append(*codes, CodeTokenFallback)
	}
	words := ReplaceTokens(text, func(id string) string { return byID[id].When })
	return words, template, refs
}

// TokensShadowLine compares a tokens draft with the old writer's draft of the
// same material, in counts only (no text): paragraphs per part, cited items
// shared and only on one side, templates kept and fallen back, and the old
// path's day repairs. It is the quality check that runs beside tokens while
// they are new (docs/brief-writer-tokens.md).
func TokensShadowLine(tokens, old Draft, tokenCodes, oldCodes []string) string {
	cited := func(d Draft) map[string]bool {
		out := map[string]bool{}
		for _, part := range [][]Paragraph{d.Done, d.Next} {
			for _, p := range part {
				for _, r := range p.Refs {
					out[r] = true
				}
			}
		}
		return out
	}
	newItems, oldItems := cited(tokens), cited(old)
	shared, onlyNew, onlyOld := 0, 0, 0
	for id := range newItems {
		if oldItems[id] {
			shared++
		} else {
			onlyNew++
		}
	}
	for id := range oldItems {
		if !newItems[id] {
			onlyOld++
		}
	}
	count := func(codes []string, want ...string) int {
		n := 0
		for _, c := range codes {
			for _, w := range want {
				if c == w {
					n++
				}
			}
		}
		return n
	}
	return "done=" + itoa(len(tokens.Done)) + "/" + itoa(len(old.Done)) +
		" next=" + itoa(len(tokens.Next)) + "/" + itoa(len(old.Next)) +
		" items_shared=" + itoa(shared) + " items_only_tokens=" + itoa(onlyNew) + " items_only_old=" + itoa(onlyOld) +
		" templates=" + itoa(count(tokenCodes, CodeTokenKept)) + " fallbacks=" + itoa(count(tokenCodes, CodeTokenFallback)) +
		" old_day_repairs=" + itoa(count(oldCodes, CodeDayFixed, CodeDayUnsupported, CodePastDateNext, CodeClockRemoved))
}
