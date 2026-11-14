package brief

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Day words in the written brief. The material gives each item a day word
// relative to the reader's day ("yesterday", "Thursday"), and the model turns
// them into prose. Two slips follow from that (brief of Nov 14, 2026):
//   - it named the wrong weekday for "yesterday" ("Thursday was full" on a
//     Saturday), because the prompt never said which weekday yesterday was;
//   - it gave a next step the date an item slipped on ("do a set November 5"),
//     a date in the past.
// The prompt now names the reader's days, and the repair checks both.

// Date repair codes.
const (
	CodeDayFixed       = "weekday_fixed"
	CodeDayUnsupported = "weekday_unsupported"
	CodePastDateNext   = "past_date_in_next"
)

var weekdayNames = []string{"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"}

var weekdayPattern = regexp.MustCompile(`\b(Sunday|Monday|Tuesday|Wednesday|Thursday|Friday|Saturday)s?\b`)

var monthNumbers = map[string]time.Month{
	"january": 1, "jan": 1, "february": 2, "feb": 2, "march": 3, "mar": 3, "april": 4, "apr": 4, "may": 5,
	"june": 6, "jun": 6, "july": 7, "jul": 7, "august": 8, "aug": 8, "september": 9, "sep": 9, "sept": 9,
	"october": 10, "oct": 10, "november": 11, "nov": 11, "december": 12, "dec": 12,
}

var monthDayPattern = regexp.MustCompile(`(?i)(\bon\s+)?\b(january|february|march|april|may|june|july|august|september|october|november|december|jan|feb|mar|apr|jun|jul|aug|sept|sep|oct|nov|dec)\.?\s+(\d{1,2})(?:st|nd|rd|th)?\b`)

// reasonWords lead a past date that is a reason, not when to act
// ("slipping since November 5").
var reasonWords = map[string]bool{"since": true, "from": true, "after": true, "before": true, "until": true, "on": false}

// dayPrompt names the reader's days for the prompt.
func (m Material) dayPrompt() string {
	if m.Today.IsZero() {
		return ""
	}
	today := m.Today
	yesterday, tomorrow := today.AddDate(0, 0, -1), today.AddDate(0, 0, 1)
	return "Today is " + today.Format("Monday, January 2") + ". Yesterday was " + yesterday.Format("Monday, January 2") +
		". Tomorrow is " + tomorrow.Format("Monday, January 2") + ".\n"
}

// itemDay is the weekday an item's When word names, or "" when it names none.
func (m Material) itemDay(when string) string {
	fields := strings.Fields(strings.ToLower(when))
	if len(fields) > 0 && fields[0] == "due" {
		fields = fields[1:]
	}
	if len(fields) == 0 {
		return ""
	}
	switch fields[0] {
	case "today":
		return m.Today.Weekday().String()
	case "yesterday":
		return m.Today.AddDate(0, 0, -1).Weekday().String()
	case "tomorrow":
		return m.Today.AddDate(0, 0, 1).Weekday().String()
	}
	for _, name := range weekdayNames {
		if strings.EqualFold(fields[0], name) {
			return name
		}
	}
	return ""
}

// checkDays repairs the day words of one paragraph against the items it cites.
// A weekday no cited item supports is replaced, in the done part, by the one
// past day all its items share, and only reported otherwise. In the next part,
// a past date that is not a reason ("since November 5") becomes "today".
func (m Material) checkDays(text string, done bool, cited []MaterialItem, codes *[]string) string {
	if m.Today.IsZero() || m.Lang == "zh" {
		return text
	}
	allowed := map[string]bool{m.Today.Weekday().String(): true}
	pastDays := map[string]bool{}
	for _, item := range cited {
		day := m.itemDay(item.When)
		if day == "" {
			continue
		}
		allowed[day] = true
		if !forwardGroups[item.Group] {
			pastDays[day] = true
		}
	}
	text = weekdayPattern.ReplaceAllStringFunc(text, func(word string) string {
		name := strings.TrimSuffix(word, "s")
		if allowed[name] {
			return word
		}
		if done && len(pastDays) == 1 {
			for day := range pastDays {
				*codes = append(*codes, CodeDayFixed)
				return strings.Replace(word, name, day, 1)
			}
		}
		*codes = append(*codes, CodeDayUnsupported)
		return word
	})
	if done {
		return text
	}
	return m.withoutPastDates(text, codes)
}

// withoutPastDates turns a past month-day date in a next step into "today",
// unless a reason word leads it.
func (m Material) withoutPastDates(text string, codes *[]string) string {
	matches := monthDayPattern.FindAllStringSubmatchIndex(text, -1)
	if len(matches) == 0 {
		return text
	}
	var b strings.Builder
	last := 0
	for _, match := range matches {
		start, end := match[0], match[1]
		month := monthNumbers[strings.ToLower(text[match[4]:match[5]])]
		day, err := strconv.Atoi(text[match[6]:match[7]])
		if month == 0 || err != nil || day < 1 || day > 31 {
			continue
		}
		date := time.Date(m.Today.Year(), month, day, 0, 0, 0, 0, m.Today.Location())
		// A date without a year is the nearest one: in November, January is
		// next year's, and in January, December is last year's.
		switch {
		case date.After(m.Today.AddDate(0, 6, 0)):
			date = date.AddDate(-1, 0, 0)
		case date.Before(m.Today.AddDate(0, -6, 0)):
			date = date.AddDate(1, 0, 0)
		}
		if !date.Before(m.Today) || match[2] < 0 && reasonWords[strings.ToLower(lastWord(text[:start]))] {
			continue
		}
		b.WriteString(text[last:start])
		b.WriteString("today")
		last = end
		*codes = append(*codes, CodePastDateNext)
	}
	b.WriteString(text[last:])
	return strings.Join(strings.Fields(b.String()), " ")
}

func lastWord(text string) string {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return ""
	}
	return strings.Trim(fields[len(fields)-1], ",.;:")
}
