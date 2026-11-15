//go:build offfeatures

// Turned-off feature (docs/rework-2026-11.md): run with -tags offfeatures.

package main

import (
	"strings"
	"testing"
	"time"

	"wails-gui/internal/engagement"
)

func TestPlaybookResultsKeepSeparateRunsWithinAnHour(t *testing.T) {
	for _, tc := range []struct {
		name       string
		minute     int
		useRunKey  bool
		statusRows int
	}{
		{"moment ids in one period", 35, false, 1},
		{"moment ids across digest boundary", 25, false, 2},
		{"run keys in one period", 35, true, 1},
		{"run keys across digest boundary", 25, true, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := mainChatTestApp(t)
			now := time.Date(2026, 10, 16, 8, tc.minute, 0, 0, time.Local)
			app.engagement = newEngagementService(app.store, nil, engagementDelivery{})
			app.engagement.now = func() time.Time { return now }
			moments := []engagement.Moment{
				{ID: "first-moment", Kind: engagement.KindPlaybookResult, SubjectID: "same-plan", DedupeKey: "playbook_result:first-run", CreatedAt: now, Params: map[string]string{"title": "Weekly review", "result": "First result."}},
				{ID: "second-moment", Kind: engagement.KindPlaybookResult, SubjectID: "same-plan", DedupeKey: "playbook_result:second-run", CreatedAt: now.Add(10 * time.Minute), Params: map[string]string{"title": "Weekly review", "result": "Second result."}},
			}
			if tc.useRunKey {
				for i := range moments {
					moments[i].ID = ""
				}
			}
			spec, _ := engagement.Spec(engagement.KindPlaybookResult)
			for _, moment := range moments {
				now = moment.CreatedAt
				if !app.postEngagementReport(moment, app.engagement.render(spec, moment)) {
					t.Fatal("playbook result was not acknowledged")
				}
			}
			entries := mainChatStatusEntries(t, app)
			if len(entries) != 2 || entries[0].ID == entries[1].ID ||
				!strings.Contains(entries[0].Text, "First result.") || !strings.Contains(entries[1].Text, "Second result.") {
				t.Fatalf("distinct runs of one plan were collapsed: %+v", entries)
			}
			before := mainChatStructuredRows(t, app, mainChatStatusTool)
			if len(before) != tc.statusRows {
				t.Fatalf("status periods = %d, want %d", len(before), tc.statusRows)
			}

			// Re-delivery after a restart and a new period acknowledges each
			// original result without repeating or moving either status row.
			now = now.Add(4 * time.Hour)
			reopened := &App{store: app.store, eventBroadcaster: NewEventBroadcaster()}
			reopened.engagement = newEngagementService(app.store, nil, engagementDelivery{})
			reopened.engagement.now = func() time.Time { return now }
			for _, moment := range moments {
				if !reopened.postEngagementReport(moment, reopened.engagement.render(spec, moment)) {
					t.Fatal("stored result retry was not acknowledged")
				}
			}
			after := mainChatStructuredRows(t, reopened, mainChatStatusTool)
			if len(mainChatStatusEntries(t, reopened)) != 2 || len(after) != len(before) {
				t.Fatalf("result retries duplicated the status entries: %+v", after)
			}
			for i := range before {
				if before[i].ID != after[i].ID || before[i].SortOrder != after[i].SortOrder {
					t.Fatalf("retry moved a prior result: before=%+v after=%+v", before, after)
				}
			}
		})
	}
}
