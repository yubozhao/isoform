package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"wails-gui/storage"
)

// writePortraitBelief writes an About you belief that passes the MCP
// provenance checks (a manual belief needs no activity session).
func writePortraitBelief(t *testing.T, id, area, title string, confirmed bool) {
	t.Helper()
	dir := filepath.Join(configDirOrFallback(), "memory")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	text := renderOKFDocument(map[string]any{"type": "Isoform Memory", "title": title, "source": "manual", "dimension": "user_insight", "area": area, "read_provenance": memoryReadProvenanceJSON(memoryReadProvenance{Version: 1, Kinds: []string{"manual"}})}, title)
	if confirmed {
		text = "---\nconfirmed_at: \"2026-11-01T00:00:00Z\"\n---\n" + text
	}
	if err := os.WriteFile(filepath.Join(dir, id+".md"), []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
}

func callPortraitDraft(t *testing.T, a *App) mcpPortraitDraft {
	t.Helper()
	value, err := a.callMCPReadTool(context.Background(), "portrait_draft", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	return value.(mcpPortraitDraft)
}

func portraitDraftText(draft mcpPortraitDraft) string {
	var b strings.Builder
	for _, section := range draft.Sections {
		for _, line := range section.Lines {
			b.WriteString(section.ID + "|" + line.Source + "|" + line.Text.Text + "|" + line.Basis + "\n")
		}
	}
	return b.String()
}

func TestPortraitDraftSourcesEveryLineAndFiltersPrivacy(t *testing.T) {
	a := newMCPReadTestApp(t)
	writePortraitBelief(t, "work", "work", "Builds desktop apps in Go", true)
	writePortraitBelief(t, "tools", "tools", "Reviews every change before it merges", false)
	writePortraitBelief(t, "boundary", "work", "Never ships without tests", true)
	writePortraitBelief(t, "person", "people", "Talks with Dana every morning", true)
	writePortraitBelief(t, "email", "work", "Sends reports to alice@example.com", true)
	writePortraitBelief(t, "health", "routines", "Takes medication for anxiety before meetings", true)
	writePortraitBelief(t, "interest", "interests", "Likes hiking", true)
	for i, rel := range []string{"colleague", "colleague", "family"} {
		if _, err := a.store.CreatePrincipal(a.ctx, storage.PrincipalRow{Kind: storage.PrincipalKindContact, DisplayName: []string{"Dana Private", "Sam Hidden", "Mom Private"}[i], Relationship: rel, CreatedFrom: "manual"}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 4; i++ {
		day := time.Now().AddDate(0, 0, -i).Add(-time.Hour).UnixMilli()
		apps, _ := json.Marshal([]string{"Editor"})
		if err := a.activity.store.SaveSession(ActivitySessionRow{ID: "s" + string(rune('a'+i)), StartTime: day, EndTime: day + 60000, CreatedAt: day, AppNames: string(apps)}, nil, nil); err != nil {
			t.Fatal(err)
		}
	}

	draft := callPortraitDraft(t, a)
	if len(draft.Sections) != len(portraitSections) {
		t.Fatalf("sections=%d", len(draft.Sections))
	}
	for i, section := range draft.Sections {
		if section.ID != portraitSections[i] || section.Lines == nil {
			t.Fatalf("section %d = %+v", i, section)
		}
		for _, line := range section.Lines {
			if !portraitDraftSources[line.Source] || strings.TrimSpace(line.Basis) == "" || !line.Text.Untrusted || line.Text.Text == "" {
				t.Fatalf("line without a source or basis: %+v", line)
			}
		}
	}
	text := portraitDraftText(draft)
	for _, want := range []string{
		"works_on|about_you|Builds desktop apps in Go|you confirmed this",
		"how_they_work|about_you|Reviews every change before it merges|Isoform's guess",
		"wont_do|about_you|Never ships without tests",
		"works_with|who_you_know|Works with 2 colleagues Isoform knows of",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("draft lacks %q:\n%s", want, text)
		}
	}
	for _, leak := range []string{"Dana", "Sam", "Mom", "alice@", "example.com", "medication", "hiking"} {
		if strings.Contains(text, leak) {
			t.Errorf("draft leaked %q:\n%s", leak, text)
		}
	}
	if draft.Withheld < 3 {
		t.Errorf("withheld=%d, want the person, email and health beliefs counted:\n%s", draft.Withheld, text)
	}
	if !strings.Contains(draft.Note, "portrait_keep") {
		t.Errorf("note must tell the assistant when to keep: %q", draft.Note)
	}
	// Drafting never writes the kept portrait.
	path, _ := ownerPortraitPath()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("portrait_draft wrote %s: %v", path, err)
	}
}

func TestPortraitDraftAppsAreNamesAndDaysOnly(t *testing.T) {
	b := newPortraitDraftBuilder("Jordan Example", nil)
	now := time.Now()
	var sessions []portraitActivitySession
	for i := 0; i < 5; i++ {
		at := now.AddDate(0, 0, -i).UnixMilli()
		sessions = append(sessions, portraitActivitySession{ID: "s", Start: at, Apps: []string{"Xcode", "Terminal", "1Password", "Jordan's Notes"}})
	}
	portraitAddApps(b, sessions, func(app string) bool { return app != "1Password" })
	got := portraitDraftText(b.draft(false))
	if !strings.Contains(got, "how_they_work|activity|Works mostly in Terminal and Xcode|app names only, from activity on 5 of the last 30 days") {
		t.Fatalf("apps line = %q", got)
	}
	if strings.Contains(got, "1Password") || strings.Contains(got, "Jordan") {
		t.Fatalf("hidden or named app leaked: %q", got)
	}
	empty := newPortraitDraftBuilder("", nil)
	portraitAddApps(empty, sessions[:2], func(string) bool { return true })
	if got := portraitDraftText(empty.draft(false)); got != "" {
		t.Fatalf("an app seen on fewer than 3 days became a line: %q", got)
	}
}

func TestPortraitDraftHonorsPause(t *testing.T) {
	a := newMCPReadTestApp(t)
	a.closedEyes.enabled.Store(true)
	for _, name := range []string{"portrait_draft", "portrait_keep"} {
		if _, err := a.callMCPReadTool(a.ctx, name, json.RawMessage(`{}`)); !errors.Is(err, errMCPReadPaused) {
			t.Fatalf("%s while paused: %v", name, err)
		}
	}
}

func keepArgs(sections ...ownerPortraitSection) json.RawMessage {
	data, _ := json.Marshal(map[string]any{"sections": sections})
	return data
}

func TestPortraitKeepWritesOnlyOnCall(t *testing.T) {
	a := newMCPReadTestApp(t)
	path, err := ownerPortraitPath()
	if err != nil {
		t.Fatal(err)
	}
	callPortraitDraft(t, a)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("portrait exists before keep")
	}
	ctx := context.WithValue(context.Background(), mcpReadClientContextKey{}, "claude-code")
	portrait := keepArgs(
		ownerPortraitSection{ID: portraitSectionStrength, Lines: []ownerPortraitLine{{Text: "Turns vague asks into small shipped changes", Source: portraitSourcePerson}}},
		ownerPortraitSection{ID: portraitSectionWorksOn, Lines: []ownerPortraitLine{{Text: "  Builds desktop apps in Go  ", Source: portraitSourceAboutYou}, {Text: "Prefers explicit state machines", Source: portraitSourceMemory}}},
	)
	value, err := a.callMCPReadTool(ctx, "portrait_keep", portrait)
	if err != nil {
		t.Fatal(err)
	}
	result := value.(mcpPortraitKeepResult)
	if !result.Kept || result.Version != 1 || result.Lines != 3 || !strings.Contains(result.Note, "Nothing was uploaded") {
		t.Fatalf("result=%+v", result)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("kept file: %v %v", info, err)
	}
	state, err := ownerPortraitRead(path)
	if err != nil {
		t.Fatal(err)
	}
	if state.Phase != ownerPortraitKept || state.KeptFrom != "claude-code" || len(state.Sections) != 2 || state.Sections[0].ID != portraitSectionWorksOn || state.Sections[0].Lines[0].Text != "Builds desktop apps in Go" {
		t.Fatalf("state=%+v", state)
	}
	if _, err := a.callMCPReadTool(ctx, "portrait_keep", portrait); err != nil {
		t.Fatal(err)
	}
	if state, _ := ownerPortraitRead(path); state.Version != 2 {
		t.Fatalf("second keep version=%d", state.Version)
	}
	if err := a.forgetWorkProfile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("Forget everything kept the portrait")
	}
}

func TestPortraitKeepRefusesPrivateOrMalformedLines(t *testing.T) {
	a := newMCPReadTestApp(t)
	path, _ := ownerPortraitPath()
	line := func(section, text, source string) json.RawMessage {
		return keepArgs(ownerPortraitSection{ID: section, Lines: []ownerPortraitLine{{Text: text, Source: source}}})
	}
	cases := map[string]json.RawMessage{
		"email":           line(portraitSectionWorksOn, "Reach me at alice@example.com", portraitSourcePerson),
		"number":          line(portraitSectionWorksOn, "Shipped 40 releases", portraitSourcePerson),
		"health":          line(portraitSectionHow, "Works around my therapy sessions", portraitSourcePerson),
		"unknown_source":  line(portraitSectionWorksOn, "Builds apps", "website"),
		"unknown_section": keepArgs(ownerPortraitSection{ID: "secrets", Lines: []ownerPortraitLine{{Text: "Builds apps", Source: portraitSourcePerson}}}),
		"newline":         line(portraitSectionWorksOn, "Builds apps\nand more", portraitSourcePerson),
		"too_long":        line(portraitSectionWorksOn, strings.Repeat("a", portraitLineMaxRunes+1), portraitSourcePerson),
		"empty":           keepArgs(ownerPortraitSection{ID: portraitSectionWorksOn, Lines: []ownerPortraitLine{}}),
		"no_sections":     json.RawMessage(`{"sections":[]}`),
		"extra_field":     json.RawMessage(`{"sections":[],"upload":true}`),
		"duplicate":       keepArgs(ownerPortraitSection{ID: portraitSectionWorksOn, Lines: []ownerPortraitLine{{Text: "Builds apps", Source: portraitSourcePerson}}}, ownerPortraitSection{ID: portraitSectionWorksOn, Lines: []ownerPortraitLine{{Text: "Writes Go", Source: portraitSourcePerson}}}),
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := a.callMCPReadTool(a.ctx, "portrait_keep", args)
			var invalid *mcpReadValidationError
			if !errors.As(err, &invalid) {
				t.Fatalf("accepted or failed oddly: %v", err)
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatal("a refused keep wrote the portrait")
			}
		})
	}
}

func TestOwnerPortraitTransitionSequences(t *testing.T) {
	at := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	lines := []ownerPortraitSection{{ID: portraitSectionWorksOn, Lines: []ownerPortraitLine{{Text: "Builds apps", Source: portraitSourcePerson}}}}
	state := ownerPortraitState{Format: ownerPortraitFormat, Phase: ownerPortraitNone}
	steps := []struct {
		event   ownerPortraitEventKind
		phase   ownerPortraitPhase
		version int64
		effects []ownerPortraitEffect
	}{
		{ownerPortraitEventForget, ownerPortraitNone, 0, nil},
		{ownerPortraitEventKeep, ownerPortraitKept, 1, []ownerPortraitEffect{ownerPortraitEffectWrite}},
		{ownerPortraitEventKeep, ownerPortraitKept, 2, []ownerPortraitEffect{ownerPortraitEffectWrite}},
		{ownerPortraitEventForget, ownerPortraitNone, 0, []ownerPortraitEffect{ownerPortraitEffectRemove}},
		{ownerPortraitEventKeep, ownerPortraitKept, 1, []ownerPortraitEffect{ownerPortraitEffectWrite}},
	}
	for i, step := range steps {
		next, effects, err := ownerPortraitTransition(state, ownerPortraitEvent{Kind: step.event, Sections: lines, Client: "claude-code", At: at})
		if err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
		if next.Phase != step.phase || next.Version != step.version || len(effects) != len(step.effects) || (len(effects) > 0 && effects[0] != step.effects[0]) {
			t.Fatalf("step %d: %+v %v", i, next, effects)
		}
		if next.Phase == ownerPortraitKept && (next.KeptAt != "2026-11-24T12:00:00Z" || next.KeptFrom != "claude-code") {
			t.Fatalf("step %d kept metadata: %+v", i, next)
		}
		state = next
	}
	if _, _, err := ownerPortraitTransition(state, ownerPortraitEvent{Kind: "upload"}); err == nil {
		t.Fatal("unknown event accepted")
	}
	if _, _, err := ownerPortraitTransition(ownerPortraitState{Phase: "published"}, ownerPortraitEvent{Kind: ownerPortraitEventKeep}); err == nil {
		t.Fatal("unknown phase accepted")
	}
}
