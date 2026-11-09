package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The page retains its existing Apps while the profile sees every source.
func workProfileTestNote(id, title, description string, at time.Time, source string, apps, sessions, points []string) string {
	return knowledgeProvenance(buildOKFKnowledgeMarkdown(id, title, description, at, source, apps, sessions, points), "", []string{source}, apps, true)
}

func TestWorkProfileKnowledgeUpdateCanHideMailProvenance(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir, err := knowledgeDir()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "observed.md")
	initial := workProfileTestNote("observed", "Client updates", "You write concise updates.", time.Now(), "activity", []string{"Editor"}, []string{"session"}, nil)
	if err := os.WriteFile(path, []byte(initial), 0600); err != nil {
		t.Fatal(err)
	}

	const canary = "fictional mail source content"
	generator := &MemoryGenerator{}
	_, _, err = generator.updateKnowledgeFileDetailed(memorySourceGmail, "fixture", &knowledgeGenerationResponse{
		UpdateID: "observed", Title: "Client updates", Description: canary, provenance: newKnowledgeGenerationProvenance(),
	})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	entry := parseKnowledgeFile("observed.md", string(data))
	if !strings.Contains(entry.Content, canary) {
		t.Fatal("fixture update did not write the source content")
	}
	if entry.Apps != "Editor" || knowHowCard(entry).Source != "activity" {
		t.Fatal("the provenance behavior changed; reassess the work-profile source boundary")
	}
	if yourAgentEntryExcluded(entry) {
		t.Fatal("the existing page filter changed")
	}
	doc, ok := parseOKFDocument(entry.Content)
	if !ok || okfString(doc.Frontmatter, "provenance_version") != knowledgeProvenanceVersion || !strings.Contains(strings.Join(okfStringSlice(doc.Frontmatter, "provenance_sources"), ","), "gmail") {
		t.Fatal("update did not retain source provenance")
	}
	if workProfileKnowledgeEligible(entry, legacyNotesPolicy) {
		t.Fatal("mail-derived text reached the work profile")
	}
}

func TestWorkProfileLegacyPolicy(t *testing.T) {
	legacy := parseKnowledgeFile("legacy.md", "# Client updates\n\nYou write concise updates.\n")
	if workProfileKnowledgeEligible(legacy, "exclude") {
		t.Fatal("legacy note admitted by default")
	}
	if !workProfileKnowledgeEligible(legacy, "include") {
		t.Fatal("legacy policy override did not admit the note")
	}
	if legacyNotesPolicy != "exclude" {
		t.Fatal("default must exclude unverifiable notes")
	}
}

func TestWorkProfileRelearningPreservesAllSources(t *testing.T) {
	dir := t.TempDir()
	initial := workProfileTestNote("lesson", "Writing client updates", "You write concise updates.", time.Now(), "activity", []string{"Editor"}, []string{"one"}, nil)
	if err := os.WriteFile(filepath.Join(dir, "lesson.md"), []byte(initial), 0600); err != nil {
		t.Fatal(err)
	}
	_, ok := relearnKnowHowWithProvenance(dir, "Writing client updates", "other", KnowHowMoment{At: time.Now().UnixMilli()}, []string{"You include a private detail."}, "gmail", nil)
	if !ok {
		t.Fatal("relearning did not match")
	}
	data, err := os.ReadFile(filepath.Join(dir, "lesson.md"))
	if err != nil {
		t.Fatal(err)
	}
	entry := parseKnowledgeFile("lesson.md", string(data))
	doc, ok := parseOKFDocument(entry.Content)
	if !ok || !strings.Contains(strings.Join(okfStringSlice(doc.Frontmatter, "provenance_apps"), ","), "gmail") || !strings.Contains(strings.Join(okfStringSlice(doc.Frontmatter, "provenance_apps"), ","), "Editor") {
		t.Fatal("relearning did not accumulate source apps")
	}
	if workProfileKnowledgeEligible(entry, "exclude") {
		t.Fatal("relearned mail content was accepted")
	}
}

func TestWorkProfileLegacyRelearningDoesNotCertifyOldText(t *testing.T) {
	dir := t.TempDir()
	content := workProfileTestNote("lesson", "Writing client updates", "You write concise updates.", time.Now(), "activity", []string{"Editor"}, nil, nil)
	doc, _ := parseOKFDocument(content)
	delete(doc.Frontmatter, "provenance_version")
	if err := os.WriteFile(filepath.Join(dir, "lesson.md"), []byte(renderOKFDocument(doc.Frontmatter, doc.Body)), 0600); err != nil {
		t.Fatal(err)
	}
	_, ok := relearnKnowHowWithProvenance(dir, "Writing client updates", "other", KnowHowMoment{At: time.Now().UnixMilli()}, []string{"You write a clear opening."}, "activity", []string{"Editor"})
	if !ok {
		t.Fatal("relearning did not match")
	}
	data, err := os.ReadFile(filepath.Join(dir, "lesson.md"))
	if err != nil {
		t.Fatal(err)
	}
	if knowledgeProvenanceKnown(string(data)) {
		t.Fatal("retained legacy text was certified")
	}
}

func TestWorkProfileRejectsChangedContentEvenWithLegacyOverride(t *testing.T) {
	content := workProfileTestNote("lesson", "Client updates", "You write concise updates.", time.Now(), "activity", []string{"Editor"}, nil, nil)
	content = strings.Replace(content, "You write concise updates.", "You disclose private material.", 1)
	entry := parseKnowledgeFile("lesson.md", content)
	if workProfileKnowledgeEligible(entry, "include") {
		t.Fatal("an invalid provenance marker was treated as legacy")
	}
}

func TestWorkProfileModelContextProvenance(t *testing.T) {
	clean := workProfileTestNote("clean", "Client updates", "You write concise updates.", time.Now(), "activity", []string{"Editor"}, nil, nil)
	mail := workProfileTestNote("mail", "Mail update", "You write private details.", time.Now(), "gmail", []string{"Gmail"}, nil, nil)
	context := newKnowledgeGenerationProvenance()
	context.addNote("clean", clean)
	context.addNote("mail", mail)
	output := knowledgeProvenance(clean, "", context.sources, context.apps, context.complete)
	entry := parseKnowledgeFile("output.md", output)
	if !knowledgeProvenanceKnown(output) || workProfileKnowledgeEligible(entry, "include") {
		t.Fatal("mail context was not recorded and excluded")
	}
	legacyDoc, _ := parseOKFDocument(clean)
	delete(legacyDoc.Frontmatter, "provenance_version")
	legacy := renderOKFDocument(legacyDoc.Frontmatter, legacyDoc.Body)
	legacyContext := newKnowledgeGenerationProvenance()
	legacyContext.addNote("legacy", legacy)
	if legacyContext.complete {
		t.Fatal("legacy context was certified")
	}
	output = knowledgeProvenance(clean, "", legacyContext.sources, legacyContext.apps, legacyContext.complete)
	if knowledgeProvenanceKnown(output) {
		t.Fatal("output copied from legacy context was certified")
	}
}

func TestWorkProfileManualLegacyEditKeepsSources(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir, err := knowledgeDir()
	if err != nil {
		t.Fatal(err)
	}
	original := "# Client updates\n\n- **Source**: activity\n- **Apps**: Editor\n\n## Key Insights\n\n- You write concise updates.\n"
	if err := os.WriteFile(filepath.Join(dir, "lesson.md"), []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	updated := "# Client updates\n\n## Key Insights\n\n- You write clear updates.\n"
	entry, err := (&App{}).SaveKnowledgeEntry("lesson", updated)
	if err != nil {
		t.Fatal(err)
	}
	doc, ok := parseOKFDocument(entry.Content)
	if !ok || knowledgeProvenanceKnown(entry.Content) || !strings.Contains(strings.Join(okfStringSlice(doc.Frontmatter, "provenance_apps"), ","), "Editor") || !strings.Contains(strings.Join(okfStringSlice(doc.Frontmatter, "provenance_sources"), ","), "manual") {
		t.Fatal("manual edit lost its prior sources or marker")
	}
	if workProfileKnowledgeEligible(entry, "include") {
		t.Fatal("manual edit was treated as a clean observed source")
	}
	if knowHowCard(entry).Title != "Client updates" || len(knowHowCard(entry).Points) != 1 {
		t.Fatal("manual edit changed the know-how card")
	}
}

func TestWorkProfileRewriteCannotCertifyLegacyOrTamperedNote(t *testing.T) {
	clean := workProfileTestNote("lesson", "Client updates", "You write concise updates.", time.Now(), "activity", []string{"Editor"}, nil, nil)
	legacy := buildOKFKnowledgeMarkdown("lesson", "Client updates", "Old text", time.Now(), "activity", []string{"Editor"}, nil, nil)
	tampered := strings.Replace(clean, "You write concise updates.", "Unverified text.", 1)
	for _, previous := range []string{legacy, tampered} {
		updated := knowledgeProvenance(clean, previous, []string{"activity"}, []string{"Editor"}, true)
		if knowledgeProvenanceKnown(updated) || workProfileKnowledgeEligible(parseKnowledgeFile("lesson.md", updated), "include") {
			t.Fatal("rewrite certified an unverified previous note")
		}
	}
}
