package main

// The brief's "Your agents:" paragraph (brief-agents v1.0.0, N1). It is built
// here from the design's word table and briefAgentNews, after the writer has
// written the rest: the model never writes it. It says only what Isoform
// knows ("finished", "has had … since", "no word yet"), results first
// (overnight ones leading a morning brief), then hand-offs with no word and
// where to act. Requests between agents (rule 2's middle) wait for N3.

import (
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"wails-gui/internal/pii"
)

// briefAgentSource is the evidence kind of a hand-off the agents paragraph
// names. Its id is the hand-off id Agents and Your work use.
const briefAgentSource = "agent_handoff"

const (
	// briefAgentResultsMax and briefAgentNoWordMax keep the paragraph short
	// (brief-agents goal 3).
	briefAgentResultsMax = 3
	briefAgentNoWordMax  = 2
	// briefAgentItemsMax is how many results sit under the paragraph as
	// items (rule 5).
	briefAgentItemsMax = 2
	// briefItemsMax is the brief's five items (redesign-today rule 5).
	briefItemsMax = 5
)

// briefAgentWords is the word table, by language.
type briefAgentWords struct {
	lead, finished, overnight, noWord, whereToAct, aHandoff string
}

var (
	briefAgentWordsEn = briefAgentWords{
		lead:       "Your agents:",
		finished:   "{agent} finished {title}.",
		overnight:  "Overnight: {agent} finished {title}. It's waiting for you to judge.",
		noWord:     "{agent} has had {title} since {time}, with no word yet.",
		whereToAct: "You can ask again or take it back from Your work.",
		aHandoff:   "a hand-off",
	}
	briefAgentWordsZh = briefAgentWords{
		lead:       "你的智能体：",
		finished:   "{agent}完成了{title}。",
		overnight:  "昨夜：{agent}完成了{title}，等你来评判。",
		noWord:     "{agent}从{time}起接手{title}，还没有回音。",
		whereToAct: "你可以在你的工作里再问一次，或把它收回。",
		aHandoff:   "一项交接",
	}
)

// briefAgentsWords are the words for the brief's language.
func briefAgentsWords(language string) briefAgentWords {
	if briefChinese(language) {
		return briefAgentWordsZh
	}
	return briefAgentWordsEn
}

// briefAgentTitle is a hand-off's title as the paragraph says it, or ok
// false when it fails the brief's privacy and copy checks: then the
// sentence names "a hand-off" instead.
func briefAgentTitle(title string) (string, bool) {
	title = strings.Join(strings.Fields(title), " ")
	title = strings.TrimRight(title, ".。!?！？")
	if title == "" || !briefAgentTextAllowed(title) {
		return "", false
	}
	return title, true
}

// briefAgentMasked is a mask left in a title ("[REDACTED]", "[EMAIL]"):
// the title already lost what it named.
var briefAgentMasked = regexp.MustCompile(`\[[A-Z][A-Z_]*(:[^\]]*)?\]`)

// briefAgentTextAllowed is the check every brief sentence passes when it is
// read (redaction, codes, sensitive and restricted words, placeholders).
func briefAgentTextAllowed(text string) bool {
	return !briefAgentMasked.MatchString(text) && mobileBriefCopySafe(text) && !pii.HasRedaction(text) && pii.RedactCodes(text) == text &&
		!proactiveVoiceRestrictedText(text) && redactThreadOutcomeText(text) == text
}

// briefAgentLowerFirst lowers a title's first letter in English when the
// next one is lower too ("Plan the outline" reads "plan the outline"), and
// leaves names and acronyms ("Q4 plan", "API docs").
func briefAgentLowerFirst(title string) string {
	first, size := utf8.DecodeRuneInString(title)
	if size == 0 || !unicode.IsUpper(first) {
		return title
	}
	next, _ := utf8.DecodeRuneInString(title[size:])
	if !unicode.IsLower(next) {
		return title
	}
	return string(unicode.ToLower(first)) + title[size:]
}

// briefAgentNamed is one hand-off the paragraph names.
type briefAgentNamed struct {
	ID    string
	Title string
	At    int64
}

// briefAgentsParagraphText is the paragraph's words and the hand-offs it
// names, in the order they are said. Empty when nothing happened (rule 1).
func briefAgentsParagraphText(news BriefAgentNews, language string, loc *time.Location, asOf time.Time) (string, []briefAgentNamed) {
	if news.Empty() {
		return "", nil
	}
	if loc == nil {
		loc = time.Local
	}
	words := briefAgentsWords(language)
	fill := func(template, agent, title, at string) string {
		agent = strings.TrimSpace(agent)
		if agent == "" {
			agent = "your agent"
			if briefChinese(language) {
				agent = "你的智能体"
			}
		}
		return strings.NewReplacer("{agent}", agent, "{title}", title, "{time}", at).Replace(template)
	}
	titleOf := func(raw string) string {
		if title, ok := briefAgentTitle(raw); ok {
			if briefChinese(language) {
				return title
			}
			return briefAgentLowerFirst(title)
		}
		return words.aHandoff
	}
	var sentences []string
	var named []briefAgentNamed
	// Overnight results lead (rule 3), then the rest, newest first.
	results := make([]BriefAgentResult, 0, len(news.Results))
	for _, r := range news.Results {
		if r.Overnight {
			results = append(results, r)
		}
	}
	for _, r := range news.Results {
		if !r.Overnight {
			results = append(results, r)
		}
	}
	for i, r := range results {
		if i == briefAgentResultsMax {
			break
		}
		template := words.finished
		if r.Overnight {
			template = words.overnight
		}
		title := titleOf(r.Title)
		sentences = append(sentences, fill(template, r.AgentName, title, ""))
		named = append(named, briefAgentNamed{ID: r.LinkID, Title: title, At: r.At})
	}
	// Requests between agents (rule 2's middle part) go here once agents can
	// ask each other (N3).
	quiet := 0
	for _, n := range news.NoWord {
		if quiet == briefAgentNoWordMax {
			break
		}
		since := time.UnixMilli(n.Since).In(loc)
		if n.Since == 0 {
			continue
		}
		at := computerClock(since, language)
		if y, m, d := since.Date(); y != asOf.In(loc).Year() || m != asOf.In(loc).Month() || d != asOf.In(loc).Day() {
			at = briefAgentDay(since, language) + " " + at
		}
		title := titleOf(n.Title)
		sentences = append(sentences, fill(words.noWord, n.AgentName, title, at))
		named = append(named, briefAgentNamed{ID: n.ID, Title: title, At: n.Since})
		quiet++
	}
	if quiet > 0 {
		// Hand-offs with no word aren't items: the sentence says where to act
		// (rule 7).
		sentences = append(sentences, words.whereToAct)
	}
	if len(named) == 0 {
		return "", nil
	}
	sep := " "
	if briefChinese(language) {
		sep = ""
	}
	return words.lead + sep + strings.Join(sentences, sep), named
}

// briefAgentDay is a past day in the brief's words: "Tuesday", "11月18日".
func briefAgentDay(at time.Time, language string) string {
	if briefChinese(language) {
		return at.Format("1月2日")
	}
	return at.Format("Monday")
}

// insertBriefAgentsParagraph puts the agents paragraph into a brief that is
// otherwise final, before audio is made from it: before the closing question
// when there is one, else last. Each hand-off it names is evidence of kind
// agent_handoff, so Your work can place its result under the paragraph. It
// returns false when it added nothing.
func insertBriefAgentsParagraph(brief *MobileBriefV1, news BriefAgentNews, language string, loc *time.Location) bool {
	if brief == nil {
		return false
	}
	asOf := mobileBriefParseTime(brief.AsOf)
	if asOf.IsZero() {
		asOf = time.Now()
	}
	text, named := briefAgentsParagraphText(news, language, loc, asOf)
	if text == "" || !mobileBriefCopySafe(text) || len(brief.Evidence)+len(named) > mobileBriefEvidenceLimit {
		return false
	}
	var refs []string
	for _, n := range named {
		ref := mobileBriefNextEvidenceRef(brief.Evidence)
		brief.Evidence = append(brief.Evidence, MobileBriefRef{
			Ref: ref, SourceKind: briefAgentSource, SourceID: n.ID, Label: n.Title,
			ObservedAt: formatMobileBriefTime(time.UnixMilli(n.At)),
		})
		refs = append(refs, ref)
	}
	para := MobileBriefPara{Text: text, Refs: refs}
	at := len(brief.Narrative)
	if closing := mobileBriefClosingParagraph(*brief); closing.Text != "" {
		at = len(brief.Narrative) - 1
	}
	// The narrative's parts stay done-then-next: what's ahead is next.
	for _, p := range brief.Narrative {
		if p.Part != "" {
			para.Part = briefPartNext
			break
		}
	}
	brief.Narrative = append(brief.Narrative[:at], append([]MobileBriefPara{para}, brief.Narrative[at:]...)...)
	if brief.NarrativeStatus == "quiet" {
		brief.NarrativeStatus = "ok"
	}
	return true
}

// mobileBriefNextEvidenceRef is the next free "eN" ref.
func mobileBriefNextEvidenceRef(evidence []MobileBriefRef) string {
	used := map[string]bool{}
	for _, e := range evidence {
		used[e.Ref] = true
	}
	for n := len(evidence) + 1; ; n++ {
		if ref := "e" + strconv.Itoa(n); !used[ref] {
			return ref
		}
	}
}

// briefAgentsParagraphIndex is the agents paragraph in a brief, and the
// hand-off ids it names, or -1.
func briefAgentsParagraphIndex(brief MobileBriefV1) (int, map[string]bool) {
	kinds := map[string]MobileBriefRef{}
	for _, e := range brief.Evidence {
		kinds[e.Ref] = e
	}
	for i, p := range brief.Narrative {
		if len(p.Refs) == 0 {
			continue
		}
		ids := map[string]bool{}
		all := true
		for _, ref := range p.Refs {
			e, ok := kinds[ref]
			if !ok || e.SourceKind != briefAgentSource {
				all = false
				break
			}
			ids[e.SourceID] = true
		}
		if all {
			return i, ids
		}
	}
	return -1, nil
}

// appendBriefAgentsParagraph adds "Your agents:" to a brief whose writer is
// done (brief-agents rule 1), from what happened since the last brief. In
// the morning, everything since the evening brief is overnight (rule 3).
// Nothing is said while Closed Eyes is on.
func (a *App) appendBriefAgentsParagraph(brief *MobileBriefV1, since time.Time, slot, language string, loc *time.Location) bool {
	if a == nil || brief == nil || a.IsClosedEyesModeEnabled() {
		return false
	}
	var overnight time.Time
	if slot == mobileBriefSlotMorning {
		overnight = since
	}
	return insertBriefAgentsParagraph(brief, a.briefAgentNews(since, overnight), language, loc)
}
