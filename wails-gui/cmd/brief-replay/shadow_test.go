package main

import (
	"os"
	"path/filepath"
	"testing"

	briefpkg "wails-gui/internal/brief"
)

const shadowTestID = "2026-10-09-morning"

// TestBuiltSectionCountsCountsComposedSectionsOnly checks that the built-brief
// side of the comparison counts the composed sections and nothing else.
func TestBuiltSectionCountsCountsComposedSectionsOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), shadowTestID+".json")
	body := `{"briefId":"` + shadowTestID + `","sections":{"moved":[{"title":"a"},{"title":"b"}],"decide":[{"title":"chief"}],"noticed":[{"title":"c"}]}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	counts := builtSectionCounts(path)
	if counts["moved"] != 2 || counts["noticed"] != 1 {
		t.Fatalf("moved=%d noticed=%d, want 2 and 1", counts["moved"], counts["noticed"])
	}
	if _, chief := counts["decide"]; chief {
		t.Fatal("a chief section was counted")
	}
	for _, name := range briefpkg.ComposedSectionNames() {
		if _, ok := counts[name]; !ok {
			t.Fatalf("composed section %s missing from the counts", name)
		}
	}
}

// TestShadowReportReadsShadowCountsOnly writes a shadow file in the shape the
// shadow package writes and checks that the report reads its counts without
// failing on the words it carries.
func TestShadowReportReadsShadowCountsOnly(t *testing.T) {
	dir := t.TempDir()
	built := `{"briefId":"` + shadowTestID + `","sections":{"moved":[{"title":"a"}]}}`
	if err := os.WriteFile(filepath.Join(dir, shadowTestID+".json"), []byte(built), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, briefpkg.ShadowDirName), 0o700); err != nil {
		t.Fatal(err)
	}
	shadow := `{"schema":1,"id":"` + shadowTestID + `","counts":{"narrative":1,"cards":0,"evidence":2},"sections":{"moved":1,"doing":0}}`
	if err := os.WriteFile(filepath.Join(dir, briefpkg.ShadowDirName, shadowTestID+".json"), []byte(shadow), 0o600); err != nil {
		t.Fatal(err)
	}
	counts, sections, ok := briefpkg.ReadShadowCounts(dir, shadowTestID)
	if !ok || counts.Narrative != 1 || counts.Evidence != 2 || sections["moved"] != 1 {
		t.Fatalf("shadow counts = %+v sections=%v ok=%v", counts, sections, ok)
	}
	results := []result{{ID: shadowTestID, BuiltOK: true, Built: [3]int{1, 0, 2}}}
	shadowReport(dir, results)
}
