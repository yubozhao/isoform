package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	briefpkg "wails-gui/internal/brief"
)

// shadowReport prints one count-only line per stored brief that has a shadow
// copy under briefs/shadow, comparing the built brief with the shadow's counts.
// It reads the built briefs and the shadow files' counts and section counts,
// and writes nothing. The words in a shadow file are never read here.
func shadowReport(dir string, results []result) {
	copies, withDiff := 0, 0
	for _, r := range results {
		if !r.BuiltOK {
			continue
		}
		newCounts, newSections, found := briefpkg.ReadShadowCounts(dir, r.ID)
		if !found {
			fmt.Printf("%s shadow=none\n", r.ID)
			continue
		}
		copies++
		oldSections := builtSectionCounts(filepath.Join(dir, r.ID+".json"))
		var differ []string
		for _, name := range briefpkg.ComposedSectionNames() {
			if oldSections[name] != newSections[name] {
				differ = append(differ, name)
			}
		}
		diff := "none"
		if len(differ) > 0 {
			withDiff++
			diff = strings.Join(differ, ",")
		}
		fmt.Printf("%s shadow=ok old_narrative=%d new_narrative=%d old_cards=%d new_cards=%d old_evidence=%d new_evidence=%d sections_diff=%s\n",
			r.ID, r.Built[0], newCounts.Narrative, r.Built[1], newCounts.Cards, r.Built[2], newCounts.Evidence, diff)
	}
	fmt.Printf("shadow_copies=%d shadow_with_section_diff=%d\n", copies, withDiff)
}

// builtSectionCounts counts the items of each composed section in a built brief
// file. Chief sections are not counted: the shadow does not compose them.
func builtSectionCounts(path string) map[string]int {
	out := map[string]int{}
	data, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	var built struct {
		Sections map[string][]json.RawMessage `json:"sections"`
	}
	if err := json.Unmarshal(data, &built); err != nil {
		return out
	}
	for _, name := range briefpkg.ComposedSectionNames() {
		out[name] = len(built.Sections[name])
	}
	return out
}
