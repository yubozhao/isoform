package main

import (
	"crypto/sha256"
	"fmt"
	"strings"

	"wails-gui/internal/pii"
)

const knowledgeProvenanceVersion = "1"

// knowledgeProvenance records contributing sources separately from the Apps
// shown on the existing know-how page. An empty marker denotes an older note.
func knowledgeProvenance(content, previous string, sources, apps []string, complete bool) string {
	// A rewrite may preserve text or moments from the previous note. An
	// unverified previous version cannot become verified merely by rewriting it.
	if previous != "" && !knowledgeProvenanceKnown(previous) {
		complete = false
	}
	doc, ok := parseOKFDocument(content)
	if !ok {
		entry := parseKnowledgeFile("", content)
		frontmatter := map[string]any{"type": "Isoform Knowledge"}
		for _, line := range strings.Split(content, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "# ") {
				frontmatter["title"] = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "# "))
				break
			}
		}
		if entry.Date != "" {
			frontmatter["timestamp"] = entry.Date
		}
		if entry.Apps != "" {
			frontmatter["apps"] = splitCommaList(entry.Apps)
		}
		if len(entry.SessionIDs) > 0 {
			frontmatter["activity_sessions"] = entry.SessionIDs
		}
		if source := knowHowCard(entry).Source; source != "" {
			frontmatter["source"] = source
		}
		if kind, present := knowHowEntryKind(content); present {
			frontmatter["kind"] = kind
		}
		doc = okfDocument{Frontmatter: frontmatter, Body: content}
	}
	var oldSources, oldApps []string
	if old, ok := parseOKFDocument(previous); ok {
		oldSources = append(okfStringSlice(old.Frontmatter, "provenance_sources"), okfString(old.Frontmatter, "source"))
		oldApps = append(okfStringSlice(old.Frontmatter, "provenance_apps"), okfStringSlice(old.Frontmatter, "apps")...)
	} else if previous != "" {
		entry := parseKnowledgeFile("", previous)
		oldSources = []string{knowHowCard(entry).Source}
		oldApps = splitCommaList(entry.Apps)
	}
	for _, source := range sources {
		switch strings.ToLower(strings.TrimSpace(source)) {
		case "", "activity", "observed", "translation", "manual":
		default:
			apps = append(apps, source)
		}
	}
	doc.Frontmatter["provenance_sources"] = knowledgeProvenanceUnion(oldSources, okfStringSlice(doc.Frontmatter, "provenance_sources"), sources)
	doc.Frontmatter["provenance_apps"] = knowledgeProvenanceUnion(oldApps, okfStringSlice(doc.Frontmatter, "provenance_apps"), apps)
	doc.Frontmatter["provenance_version"] = knowledgeProvenanceVersion
	doc.Frontmatter["provenance_complete"] = complete
	delete(doc.Frontmatter, "provenance_hash")
	doc.Frontmatter["provenance_hash"] = knowledgeProvenanceHash(doc)
	return renderOKFDocument(doc.Frontmatter, doc.Body)
}

func knowledgeProvenanceUnion(lists ...[]string) []string {
	seen := make(map[string]bool)
	var result []string
	for _, list := range lists {
		for _, value := range list {
			value = strings.TrimSpace(value)
			key := strings.ToLower(value)
			if value != "" && !seen[key] {
				seen[key] = true
				result = append(result, value)
			}
		}
	}
	return result
}

func knowledgeProvenanceKnown(content string) bool {
	doc, ok := parseOKFDocument(content)
	if !ok || okfString(doc.Frontmatter, "provenance_version") != knowledgeProvenanceVersion || okfString(doc.Frontmatter, "provenance_complete") != "true" {
		return false
	}
	expected := okfString(doc.Frontmatter, "provenance_hash")
	return expected != "" && expected == knowledgeProvenanceHash(doc)
}

func knowledgeProvenanceHash(doc okfDocument) string {
	frontmatter := make(map[string]any, len(doc.Frontmatter))
	for key, value := range doc.Frontmatter {
		if key != "provenance_hash" && key != "kind" {
			frontmatter[key] = value
		}
	}
	sum := sha256.Sum256([]byte(renderOKFDocument(frontmatter, doc.Body)))
	return fmt.Sprintf("%x", sum)
}

// knowledgeGenerationProvenance captures all note summaries supplied to the
// extraction model, not just the note selected for an update.
type knowledgeGenerationProvenance struct {
	sources  []string
	apps     []string
	complete bool
}

func newKnowledgeGenerationProvenance() knowledgeGenerationProvenance {
	return knowledgeGenerationProvenance{complete: true}
}

func (p *knowledgeGenerationProvenance) addNote(_ string, content string) {
	doc, ok := parseOKFDocument(content)
	if !ok || !knowledgeProvenanceKnown(content) {
		p.complete = false
	}
	if ok {
		p.sources = knowledgeProvenanceUnion(p.sources, okfStringSlice(doc.Frontmatter, "provenance_sources"), []string{okfString(doc.Frontmatter, "source")})
		p.apps = knowledgeProvenanceUnion(p.apps, okfStringSlice(doc.Frontmatter, "provenance_apps"), okfStringSlice(doc.Frontmatter, "apps"))
	} else {
		entry := parseKnowledgeFile("", content)
		p.sources = knowledgeProvenanceUnion(p.sources, []string{knowHowCard(entry).Source})
		p.apps = knowledgeProvenanceUnion(p.apps, splitCommaList(entry.Apps))
	}
}

// Flip to "include" only if the owner accepts unverifiable older notes.
const legacyNotesPolicy = "exclude"

func workProfileKnowledgeEligible(entry KnowledgeEntry, legacyPolicy string) bool {
	if yourAgentEntryExcluded(entry) {
		return false
	}
	doc, ok := parseOKFDocument(entry.Content)
	sources := okfStringSlice(doc.Frontmatter, "provenance_sources")
	sources = append(sources, knowHowCard(entry).Source)
	apps := append(okfStringSlice(doc.Frontmatter, "provenance_apps"), splitCommaList(entry.Apps)...)
	for _, source := range sources {
		switch strings.ToLower(strings.TrimSpace(source)) {
		case "", "activity", "observed", "translation":
		default:
			return false
		}
	}
	for _, app := range apps {
		switch strings.ToLower(strings.TrimSpace(app)) {
		case "gmail", "slack", "discord", "feishu", "telegram", "wechat", "teams", "ringcentral", "whatsapp", "signal":
			return false
		}
		if yourAgentIsMailOrMessages(app) || pii.IsSensitiveAppName(app) {
			return false
		}
	}
	if !ok || okfString(doc.Frontmatter, "provenance_version") == "" {
		return legacyPolicy == "include"
	}
	if !knowledgeProvenanceKnown(entry.Content) {
		return false
	}
	return len(okfStringSlice(doc.Frontmatter, "provenance_sources")) > 0 && len(apps) > 0
}
