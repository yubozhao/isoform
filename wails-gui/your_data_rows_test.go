package main

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"wails-gui/internal/destinations"
)

// What leaves ends with "That's every place your data goes." (data-leaves
// v1.0.0 rules 3-4). This is the proof the line rests on: every destination
// that sends the person's content maps to a row in DESTINATION_ROWS, and each
// of those rows is on the page. A new content destination without a row fails
// here, and so does a row that's gone from the page.
func TestEveryContentDestinationHasAWhatLeavesRow(t *testing.T) {
	read := func(path string) string {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	card := read("frontend/src/components/settings/what-leaves-card.tsx")
	page := read("frontend/src/components/settings/pages/your-data-page.tsx")

	block := regexp.MustCompile(`(?s)export const DESTINATION_ROWS = \{(.*?)\} as const;`).FindStringSubmatch(card)
	if len(block) != 2 {
		t.Fatal("DESTINATION_ROWS missing from what-leaves-card.tsx")
	}
	rows := map[string]string{}
	for _, m := range regexp.MustCompile(`(?m)^\s*([a-z_]+):\s*"([A-Za-z]+)",?\s*$`).FindAllStringSubmatch(block[1], -1) {
		rows[m[1]] = m[2]
	}
	if len(rows) == 0 {
		t.Fatal("DESTINATION_ROWS has no entries")
	}

	listed := map[string]bool{}
	for _, d := range destinations.List() {
		listed[string(d.ID)] = true
		if d.Content == destinations.ContentNone {
			continue
		}
		if _, ok := rows[string(d.ID)]; !ok {
			t.Errorf("content destination %q has no What leaves row", d.ID)
		}
	}
	both := card + page
	for id, row := range rows {
		if !listed[id] {
			t.Errorf("DESTINATION_ROWS names %q, which isn't a destination", id)
		}
		if !strings.Contains(both, `data-your-data-row="`+row+`"`) && !strings.Contains(both, `id="`+row+`"`) {
			t.Errorf("row %q for %q isn't on the page", row, id)
		}
	}
	if !strings.Contains(card, `t("yourData.everywhere")`) {
		t.Error("the card no longer ends with the every-place line")
	}
}
