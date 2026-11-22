package main

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// Memory import arrives as claims (design answer T2, 2026-11-21): "Bring in
// what another AI knows about you" splits what it brings into one memory
// entry per line, each marked imported and unjudged, shown first under New
// this week until the person judges it.

const (
	// aboutYouImportShown is how many unjudged imported claims New this week
	// shows before "{n} more from {label}".
	aboutYouImportShown = 20
	// memoryImportClaimsMax bounds one import.
	memoryImportClaimsMax     = 500
	memoryImportClaimMaxRunes = 400
)

// AboutYouImportMore is the rest of one AI's unjudged imported claims.
type AboutYouImportMore struct {
	Label string   `json:"label"`
	IDs   []string `json:"ids"`
}

// importClaimLines splits imported text into claim lines: each bullet or line
// item, without headings, empty lines, rules, quotes or section labels.
func importClaimLines(text string) []string {
	out := []string{}
	seen := map[string]bool{}
	fence := false
	for _, raw := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "```") {
			fence = !fence
			continue
		}
		if fence || line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ">") || strings.Trim(line, "-*_= ") == "" {
			continue
		}
		line = trimListMarker(line)
		line = strings.Join(strings.Fields(line), " ")
		if line == "" || strings.HasSuffix(line, ":") || utf8.RuneCountInString(line) < 3 {
			continue
		}
		if utf8.RuneCountInString(line) > memoryImportClaimMaxRunes {
			line = strings.TrimSpace(string([]rune(line)[:memoryImportClaimMaxRunes-1])) + "…"
		}
		key := strings.ToLower(line)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, line)
		if len(out) == memoryImportClaimsMax {
			break
		}
	}
	return out
}

func trimListMarker(line string) string {
	for _, marker := range []string{"- [ ] ", "- [x] ", "- ", "* ", "+ ", "• "} {
		if rest, ok := strings.CutPrefix(line, marker); ok {
			return strings.TrimSpace(rest)
		}
	}
	// "1. " and "1) "
	digits := 0
	for digits < len(line) && line[digits] >= '0' && line[digits] <= '9' {
		digits++
	}
	if digits > 0 && digits+1 < len(line) && (line[digits] == '.' || line[digits] == ')') && line[digits+1] == ' ' {
		return strings.TrimSpace(line[digits+2:])
	}
	return line
}

// importedClaimNote is one imported claim's memory file.
func importedClaimNote(label, claim string, at time.Time) string {
	return "# " + claim + "\n- **Updated**: " + at.Format("2006-01-02 15:04") + "\n- **Source**: " + memoryImportSourcePrefix + label +
		"\n\n## Memory Points\n\n- " + claim + "\n"
}

// writeImportedClaims writes one memory entry per claim line. It returns how
// many were written and the errors of the rest.
func (a *App) writeImportedClaims(dir, label, text string) (int, []string) {
	label = strings.Join(strings.Fields(label), " ")
	if label == "" {
		label = "another AI"
	}
	now := time.Now()
	safe := sanitizeFilename(label)
	written, errs := 0, []string{}
	for index, claim := range importClaimLines(text) {
		id := fmt.Sprintf("imported-%s-%d-%03d", safe, now.UnixMilli(), index)
		if err := a.writeImportedMemoryFile(dir, id, importedClaimNote(label, claim, now)); err != nil {
			errs = append(errs, fmt.Sprintf("write %s: %v", id, err))
			continue
		}
		written++
	}
	return written, errs
}

// importedNewBeliefIDs orders unjudged imported claims, newest import first
// and in their own order within one, and keeps the first aboutYouImportShown;
// the rest go to ImportMore by label.
func importedNewBeliefIDs(beliefs []AboutYouBelief) ([]string, []AboutYouImportMore) {
	sort.SliceStable(beliefs, func(i, j int) bool {
		if beliefs[i].FirstLearnedAt != beliefs[j].FirstLearnedAt {
			return beliefs[i].FirstLearnedAt > beliefs[j].FirstLearnedAt
		}
		return beliefs[i].ID < beliefs[j].ID
	})
	shown := []string{}
	more := []AboutYouImportMore{}
	index := map[string]int{}
	for _, belief := range beliefs {
		if len(shown) < aboutYouImportShown {
			shown = append(shown, belief.ID)
			continue
		}
		i, ok := index[belief.ImportedFrom]
		if !ok {
			i = len(more)
			index[belief.ImportedFrom] = i
			more = append(more, AboutYouImportMore{Label: belief.ImportedFrom, IDs: []string{}})
		}
		more[i].IDs = append(more[i].IDs, belief.ID)
	}
	return shown, more
}
