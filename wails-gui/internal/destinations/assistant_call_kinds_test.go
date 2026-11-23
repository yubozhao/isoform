package destinations_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"wails-gui/storage"
)

// Every kind of source the assistant call record keeps has a name on the
// page, in both languages, with its unit, and in the sheet's own list
// (design T15: an unnamed kind fails the destinations test).
func TestAssistantCallKindsAreNamed(t *testing.T) {
	sheet, err := os.ReadFile("../../frontend/src/components/settings/assistant-given-sheet.tsx")
	if err != nil {
		t.Fatal(err)
	}
	for _, loc := range []string{"en", "zh-CN"} {
		raw, err := os.ReadFile(filepath.Join("../../frontend/src/i18n/locales", loc, "settings.json"))
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			Given struct {
				Kinds  map[string]string `json:"kinds"`
				Counts map[string]string `json:"counts"`
			} `json:"given"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		for _, kind := range storage.AssistantCallSourceKinds() {
			if doc.Given.Kinds[kind] == "" {
				t.Errorf("%s: given.kinds.%s has no name", loc, kind)
			}
			if doc.Given.Counts[kind+"_one"] == "" || doc.Given.Counts[kind+"_other"] == "" {
				t.Errorf("%s: given.counts.%s has no count with its unit", loc, kind)
			}
			if loc == "en" && !strings.Contains(string(sheet), `"`+kind+`"`) {
				t.Errorf("SOURCE_KINDS in the sheet doesn't list %q", kind)
			}
		}
		if _, ok := doc.Given.Kinds["other"]; ok {
			t.Errorf("%s: \"Other\" is still a kind", loc)
		}
	}
}
