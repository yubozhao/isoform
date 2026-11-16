package main

import (
	"errors"
	"regexp"
	"strings"
	"time"
)

// A person correction ("it was Coral, not Jing"): the moments a brief stood
// on that named the wrong person are corrected as the person's own words,
// through the same lock as Activity's Not right (correctActivityText). The
// moment keeps the corrected words through later composes, and the
// composer's naming corrections carry the change into new titles, so
// tomorrow's brief reads the right name. A calendar event's title is the
// person's own calendar and is never rewritten; a meeting_attendance source
// only names its event.

var errPersonCorrectionNames = errors.New("say who it was and who it wasn't")

// PersonCorrectionResult says which moments a correction changed.
type PersonCorrectionResult struct {
	Corrected []string `json:"corrected"`
}

// personNamePattern matches the wrong name as a whole word, with a family
// name after a given name ("Jing" also matches "Jing Jin").
func personNamePattern(wrong string) *regexp.Regexp {
	return regexp.MustCompile(`\b` + regexp.QuoteMeta(wrong) + `(?:\s+\p{Lu}[\p{L}'’-]*)?\b`)
}

// replacePersonName swaps the wrong name for the right one in text. A
// possessive keeps its mark.
func replacePersonName(text, wrong, right string) string {
	return personNamePattern(wrong).ReplaceAllString(text, right)
}

// CorrectActivityPerson corrects one moment: every mention of wrong in its
// title and summary becomes right, kept as the person's own correction.
func (a *App) CorrectActivityPerson(activityID, wrong, right string) (bool, error) {
	wrong, right = strings.TrimSpace(wrong), strings.TrimSpace(right)
	if wrong == "" || right == "" || strings.EqualFold(wrong, right) {
		return false, errPersonCorrectionNames
	}
	if a.activity == nil || a.activity.store == nil {
		return false, errActivityNotFound
	}
	row, err := a.activity.store.GetActivityByID(strings.TrimSpace(activityID))
	if err != nil {
		return false, err
	}
	if row == nil {
		return false, errActivityNotFound
	}
	title := replacePersonName(row.Title, wrong, right)
	description := replacePersonName(row.Description, wrong, right)
	if title == row.Title && description == row.Description {
		return false, nil
	}
	changed, err := a.activity.store.correctActivityText(row.ID, title, description, time.Now())
	if err != nil {
		return false, err
	}
	if changed {
		a.emitBrowserEvent("activity:activities-updated")
	}
	return changed, nil
}

// CorrectBriefPerson applies a person correction to every moment a brief
// cited ("summary" sources) that names the wrong person.
func (a *App) CorrectBriefPerson(briefID, wrong, right string) (PersonCorrectionResult, error) {
	out := PersonCorrectionResult{Corrected: []string{}}
	if strings.TrimSpace(wrong) == "" || strings.TrimSpace(right) == "" {
		return out, errPersonCorrectionNames
	}
	service, _ := a.ensureMobileBriefService().(*mobileBriefService)
	if service == nil || service.store == nil {
		return out, errors.New("the brief is unavailable")
	}
	brief, found := service.store.load(strings.TrimSpace(briefID))
	if !found {
		return out, errors.New("the brief was not found")
	}
	return a.correctPersonInSources(brief.Evidence, wrong, right)
}

func (a *App) correctPersonInSources(evidence []MobileBriefRef, wrong, right string) (PersonCorrectionResult, error) {
	out := PersonCorrectionResult{Corrected: []string{}}
	seen := map[string]bool{}
	for _, ref := range evidence {
		if ref.SourceKind != "summary" || ref.SourceID == "" || seen[ref.SourceID] {
			continue
		}
		seen[ref.SourceID] = true
		changed, err := a.CorrectActivityPerson(ref.SourceID, wrong, right)
		if errors.Is(err, errActivityNotFound) {
			continue
		}
		if err != nil {
			return out, err
		}
		if changed {
			out.Corrected = append(out.Corrected, ref.SourceID)
		}
	}
	return out, nil
}
