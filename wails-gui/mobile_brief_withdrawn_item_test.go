package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// A withdrawn noticed item removes only the items that cite it. The brief
// keeps what is left, and the withdrawn words appear nowhere in the delivery.
func TestMobileBriefWithdrawnNoticedItemDropsOnlyItsOwnItems(t *testing.T) {
	app := newMobileBriefPurposeTestApp(t)
	now := noticedTestAt(20, 0)
	seeded := seedBriefNoticed(t, app, noticedTestDay, 3)
	brief := briefWithNoticed(t, app, now)
	before := len(brief.Sections.Noticed)
	if before < 2 {
		t.Fatalf("setup: want at least 2 noticed items, got %d", before)
	}
	withdrawn := seeded[0]
	seedNoticedDismissal(t, app, withdrawn)
	got, withheld := app.mobileBriefDelivery(brief)
	if withheld.Reason != "" {
		t.Fatalf("one withdrawn item withheld the whole brief: %+v", withheld)
	}
	if len(got.Sections.Noticed) != before-1 {
		t.Fatalf("remaining noticed items = %d, want %d", len(got.Sections.Noticed), before-1)
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), withdrawn.Text) {
		t.Fatalf("withdrawn text reached the delivery: %s", withdrawn.Text)
	}
}
